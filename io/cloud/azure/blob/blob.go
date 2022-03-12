/*
Package blob is an implementation of the io.FS for Azure blob storage. This package
foresakes all the options offered by the standard azure storage package to simplify
use. If you need options not provided here, your best solution is probably to use
the standard package.

This package supports two additional features over io.FS capabilities:
- Writing files opened with OpenFile()
- Locking files

This currently only support Block Blobs, not Append or Page. We may offer that
in the future with enough demand.

NOTE: NUMBER ONE MISTAKE: FORGETTING .CLOSE() on WRITING A FILE, SO IT DOES NOT WRITE THE FILE.

Open a Blob storage container:
	cred, err := msi.Token(msi.SystemAssigned{})
	if err != nil {
		panic(err)
	}

	fsys, err := NewFS("account", "container", *cred)
	if err != nil {
		// Do something
	}

Read an entire file:
	file, err := fsys.Open("users/jdoak.json")
	if err != nil {
		// Do something
	}

	// You could also do fsys.ReadFile() for simplicity.
	b, err := io.ReadAll(file)
	if err != nil {
		// Do something
	}

	fmt.Println(string(b))

Stream a file to stdout:
	file, err := fsys.Open("users/jdoak.json")
	if err != nil {
		// Do something
	}

	if _, err := io.Copy(os.Stdout, file); err != nil {
		// Do something
	}

Copy a file:
	src, err := os.Open("path/to/some/file")
	if err != nil {
		// Do something
	}

	dst, err := fsys.OpenFile("path/to/place/content", 0644, WithFlags(os.O_WRONLY | os.O_CREATE))
	if err != nil {
		// Do something
	}

	if _, err := io.Copy(dst, src); err != nil {
		// Do something
	}

	// The file is not actually written until the file is closed, so it is
	// important to know if Close() had an error.
	if err := dst.Close(); err != nil {
		// Do something
	}

Write a string to a file:
	file, err := fsys.OpenFile("users/jdoak.json", 0644, WithFlags(os.O_WRONLY | os.O_CREATE))
	if err != nil {
		// Do something
	}

	if _, err := io.WriteString(file, `{"Name":"John Doak"}`); err != nil {
		// Do something
	}

	// The file is not actually written until the file is closed, so it is
	// important to know if Close() had an error.
	if err := file.Close(); err != nil {
		// Do something
	}

Walk the file system and log all directories:
	err := fs.WalkDir(
		fsys,
		".",
		func(path string, d fs.DirEntry, err error) error {
			if !d.IsDir() {
				return nil
			}
			log.Println("dir: ", path)
			return nil
		},
	)
	if err != nil {
		// Do something
	}
*/
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net/url"
	"os"
	"path"
	"reflect"
	"sync"
	"time"

	"github.com/Azure/azure-storage-blob-go/azblob"
	jsfs "github.com/gopherfs/fs"
	"github.com/johnsiilver/golib/signal"
	"golang.org/x/sync/errgroup"
)

// File implements io.FS.File and io.Writer for blobs.
type File struct {
	flags   int
	contURL azblob.ContainerURL // Only set if File is a directory.
	u       azblob.BlockBlobURL
	fi      fileInfo
	path    string // The full path, used for directories

	// These are related to locking
	leaseID string
	expires time.Time
	closed  signal.Signaler

	mu sync.Mutex

	// For files that can be read.
	reader io.ReadCloser
	// For files that can write.
	writer io.WriteCloser
	// writeErr indicates if we have an error with writing.
	writeErr  error
	writeWait sync.WaitGroup

	transferManager azblob.TransferManager

	dirReader *dirReader // Usee when this represents a directory
}

// Read implements fs.File.Read().
func (f *File) Read(p []byte) (n int, err error) {
	if isFlagSet(f.flags, os.O_RDONLY) {
		return 0, fmt.Errorf("File is not set to os.O_RDONLY")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.reader == nil {
		if err := f.fetchReader(); err != nil {
			return 0, err
		}
	}

	return f.reader.Read(p)
}

// Write implements io.Writer.Write().
func (f *File) Write(p []byte) (n int, err error) {
	if !isFlagSet(f.flags, os.O_WRONLY) {
		return 0, errors.New("cannot write to file without flag os.O_WRONLY")
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.leaseID != "" && time.Now().After(f.expires) {
		return 0, fmt.Errorf("lost lock on file")
	}

	if f.writer == nil {
		r, w := io.Pipe()
		f.writer = w

		f.writeWait.Add(1)
		go func() {
			defer f.writeWait.Done()
			_, err := azblob.UploadStreamToBlockBlob(
				context.Background(),
				r,
				f.u.ToBlockBlobURL(),
				azblob.UploadStreamToBlockBlobOptions{
					TransferManager: f.transferManager,
					AccessConditions: azblob.BlobAccessConditions{
						LeaseAccessConditions: azblob.LeaseAccessConditions{
							LeaseID: f.leaseID,
						},
					},
				},
			)
			if err != nil {
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.writeErr == nil {
					f.writeErr = err
				}
			}
		}()
	}
	if f.writeErr != nil {
		return 0, err
	}

	return f.writer.Write(p)
}

// Close implements fs.File.Close().
func (f *File) Close() error {
	if f.reader != nil {
		return f.reader.Close()
	}

	if f.writer != nil {
		f.writer.Close()
		f.writeWait.Wait()

		if !reflect.ValueOf(f.closed).IsZero() {
			defer f.closed.Close()
			f.closed.Signal(nil, signal.Wait())
			f.releaseLease()
		}
		return f.writeErr
	}

	return nil
}

// releaseLease will break a file lease or attempt to until the lease expires.
func (f *File) releaseLease() {
	releaseCtx, cancel := context.WithDeadline(context.Background(), f.expires)
	defer cancel()

	for {
		_, err := f.u.ReleaseLease(releaseCtx, f.leaseID, azblob.ModifiedAccessConditions{})
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			time.Sleep(1 * time.Second)
			continue
		}
		return
	}
}

// Stat implements fs.File.Stat().
func (f *File) Stat() (fs.FileInfo, error) {
	return f.fi, nil
}

func (f *File) fetchReader() error {
	resp, err := f.u.Download(context.Background(), 0, 0, azblob.BlobAccessConditions{}, false, azblob.ClientProvidedKeyOptions{})
	if err != nil {
		return err
	}

	f.reader = resp.Body(azblob.RetryReaderOptions{})
	return nil
}

// renew renews a lease lock on the file if one exists.
func (f *File) renew() {
	renewAt := time.Until(f.expires) / 2
	if renewAt < 0 {
		return
	}

	go func() {
		ticker := time.NewTicker(renewAt)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := f.renewLease(); err != nil {
					log.Printf("(%s) problem renewing lease: %s", f.path, err)
				}
				return
			case ack := <-f.closed.Receive():
				ack.Ack(nil)
				return
			}
		}
	}()
}

func (f *File) renewLease() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Until(f.expires))
	defer cancel()

	for {
		lease, err := f.u.RenewLease(ctx, f.leaseID, azblob.ModifiedAccessConditions{})
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			continue
		}
		f.mu.Lock()
		f.leaseID = lease.LeaseID()
		f.expires = lease.Date().Add(60 * time.Second)
		return nil
	}
}

// ReadDir implements fs.ReadDirFile.ReadDir().
func (f *File) ReadDir(n int) ([]fs.DirEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.fi.dir {
		return nil, fmt.Errorf("File is not a directory")
	}

	if f.dirReader == nil {
		dr, err := newDirReader(f.path, f.contURL)
		if err != nil {
			return nil, err
		}
		f.dirReader = dr
	}
	return f.dirReader.ReadDir(n)
}

type dirReader struct {
	sync.Mutex

	name    string
	path    string
	contURL azblob.ContainerURL
	items   []fs.DirEntry
	index   int
}

func newDirReader(dirPath string, contURL azblob.ContainerURL) (*dirReader, error) {
	dr := &dirReader{
		name:    path.Base(dirPath),
		path:    dirPath,
		contURL: contURL,
	}
	if err := dr.get(); err != nil {
		return nil, err
	}
	return dr, nil
}

func (d *dirReader) ReadDir(n int) ([]fs.DirEntry, error) {
	d.Lock()
	defer d.Unlock()

	if n <= 0 {
		return d.items, nil
	}

	if d.index < len(d.items) {
		ret := d.items[d.index:]
		d.index += n
		return ret, nil
	}

	return nil, io.EOF
}

func (d *dirReader) get() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if d.path == "." {
		d.path = ""
	} else {
		// If we are the root, you can't use '/' in the prefix, but if you are
		// not the root, you MUST use '/'.
		d.path += "/"
	}

	resp, err := d.contURL.ListBlobsHierarchySegment(
		ctx,
		azblob.Marker{},
		"/",
		azblob.ListBlobsSegmentOptions{
			Prefix:     d.path,
			MaxResults: math.MaxInt32,
		},
	)
	if err != nil {
		return err
	}

	for _, prefix := range resp.Segment.BlobPrefixes {
		n := path.Base(prefix.Name)
		item := &dirEntry{
			name: n,
			fi: fileInfo{
				name: n,
				dir:  true,
			},
		}
		d.items = append(d.items, item)
	}

	g, ctx := errgroup.WithContext(ctx)
	limiter := make(chan struct{}, 20)
	for _, blob := range resp.Segment.BlobItems {
		blob = blob
		n := path.Base(blob.Name)

		limiter <- struct{}{}
		g.Go(func() error {
			defer func() { <-limiter }()

			u := d.contURL.NewBlobURL(blob.Name)
			resp, err := u.GetProperties(ctx, azblob.BlobAccessConditions{}, azblob.ClientProvidedKeyOptions{})
			if err == nil {
				d.Lock()
				defer d.Unlock()
				d.items = append(d.items, &dirEntry{name: n, fi: newFileInfo(n, resp)})
			}
			return err
		})
	}
	return g.Wait()
}

type dirEntry struct {
	name string
	fi   fs.FileInfo
}

func (d dirEntry) Name() string {
	return d.name
}

func (d dirEntry) IsDir() bool {
	return d.fi.IsDir()
}

func (d dirEntry) Type() fs.FileMode {
	return d.fi.Mode()
}

func (d dirEntry) Info() (fs.FileInfo, error) {
	return d.fi, nil
}

// FS implements io/fs.FS
type FS struct {
	containerURL azblob.ContainerURL
}

// NewFS is the constructor for FS. It is recommended that you use blob/auth/msi to create
// the "cred".
func NewFS(account, container string, cred azblob.Credential) (*FS, error) {
	p := azblob.NewPipeline(cred, azblob.PipelineOptions{})
	blobPrimaryURL, _ := url.Parse("https://" + account + ".blob.core.windows.net/")
	bsu := azblob.NewServiceURL(*blobPrimaryURL, p)

	return &FS{
		containerURL: bsu.NewContainerURL(container),
	}, nil
}

// Open implements fs.FS.Open().
func (f *FS) Open(name string) (fs.File, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	u := f.containerURL.NewBlobURL(name)

	props, err := u.GetProperties(ctx, azblob.BlobAccessConditions{}, azblob.ClientProvidedKeyOptions{})
	if err != nil {
		return f.dirFile(ctx, name)
	}

	switch props.BlobType() {
	case azblob.BlobBlockBlob:
		return &File{
			contURL: f.containerURL,
			flags:   os.O_RDONLY,
			u:       u.ToBlockBlobURL(),
			fi:      newFileInfo(path.Base(name), props),
		}, nil
	}
	return nil, fmt.Errorf("%T type blobs are not currently supported", props.BlobType())
}

// ReadFile implements fs.ReadFileFS.ReadFile.
func (f *FS) ReadFile(name string) ([]byte, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if name == "." {
		name = ""
	}

	u := f.containerURL.NewBlobURL(name)

	_, err := u.GetProperties(ctx, azblob.BlobAccessConditions{}, azblob.ClientProvidedKeyOptions{})
	if err == nil {
		return nil, fmt.Errorf("ReadDir(%s) does not appear to be a directory", name)
	}

	file, err := f.dirFile(ctx, name)
	if err != nil {
		return nil, err
	}

	return file.ReadDir(-1)
}

// Stat implements fs.StatFS.Stat.
func (f *FS) Stat(name string) (fs.FileInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dir, err := f.dirFile(ctx, name)
	if err == nil {
		return dir.fi, nil
	}
	u := f.containerURL.NewBlobURL(name)

	props, err := u.GetProperties(ctx, azblob.BlobAccessConditions{}, azblob.ClientProvidedKeyOptions{})
	if err != nil {
		return nil, err
	}
	return newFileInfo(name, props), nil
}

func (f *FS) dirFile(ctx context.Context, name string) (*File, error) {
	switch name {
	case ".", "":
		return &File{
			path:    ".",
			contURL: f.containerURL,
			fi: fileInfo{
				name: ".",
				dir:  true,
			},
		}, nil
	}

	resp, err := f.containerURL.ListBlobsHierarchySegment(
		ctx,
		azblob.Marker{},
		"/",
		azblob.ListBlobsSegmentOptions{Prefix: name + `/`, MaxResults: math.MaxInt32},
	)
	if err != nil {
		return nil, err
	}

	if len(resp.Segment.BlobPrefixes) > 0 || len(resp.Segment.BlobItems) > 0 {
		return &File{
			path:    name,
			contURL: f.containerURL,
			fi: fileInfo{
				name: path.Base(name),
				dir:  true,
			},
		}, nil
	}

	return nil, &fs.PathError{
		Op:   "open",
		Path: name,
		Err:  errors.New("no such file or directory"),
	}
}

type rwOptions struct {
	lock bool
	tm   azblob.TransferManager
	flags int
}

func (o *rwOptions) defaults() {
	if o.flags == 0 {
		o.flags = os.O_RDONLY
	}
}

// WithLock locks the file and attempts to keep it locked until the file is closed.
// If the file in question is a directory, no lease it taken out.
func WithLock() jsfs.OFOption {
	return func(o interface{}) error {
		opt, ok := o.(*rwOptions)
		if !ok {
			return fmt.Errorf("WithLock passed to incorrect function")
		}
		opt.lock = true
		return nil
	}
}

// WithTransferManager allows you to provide one of azblob's TransferManagers or your
// own TransferManager for controlling file writes.
func WithTransferManager(tm azblob.TransferManager) jsfs.OFOption {
	return func(o interface{}) error {
		opt, ok := o.(*rwOptions)
		if !ok {
			return fmt.Errorf("WithTransferManager passed to incorrect function")
		}
		opt.tm = tm
		return nil
	}
}

func isFlagSet(flags int, flag int) bool {
	return flags&flag != 0
}

// Flags sets the flags based on package "os" flag values. By default this is os.O_RDONLY.
func WithFlags(flags int) jsfs.OFOption {
	return func(i interface{}) error {
		v, ok := i.(*rwOptions)
		if !ok {
			return fmt.Errorf("Flags() call received %T, expected local *ofOptions", i)
		}
		v.flags = flags
		return nil
	}
}

// OpenFile implements github.com/gopherfs/fs.OpenFilerFS. When creating a new file, this will always be a block blob.
func (f *FS) OpenFile(name string, perms fs.FileMode, options ...jsfs.OFOption) (fs.File, error) {
	opts := rwOptions{}
	opts.defaults()

	for _, o := range options {
		if err := o(&opts); err != nil {
			return nil, err
		}
	}

	if opts.lock && !isFlagSet(opts.flags, os.O_WRONLY) {
		return nil, fmt.Errorf("only os.O_WRONLY support for locks")
	}

	if isFlagSet(opts.flags, os.O_RDONLY) {
		if opts.flags > 0 {
			return nil, fmt.Errorf("cannot set any other flag if os.O_RDONLY is set")
		}
		file, err := f.Open(name)
		if err != nil {
			return nil, err
		}
		return file.(*File), nil
	}

	if isFlagSet(opts.flags, os.O_EXCL) && !isFlagSet(opts.flags, os.O_CREATE) {
		return nil, fmt.Errorf("cannot set os.O_EXCL without os.O_CREATE")
	}
	if name == "." {
		name = ""
	}

	propCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dir, err := f.dirFile(propCtx, name)
	if err == nil {
		return dir, nil
	}
	u := f.containerURL.NewBlobURL(name)

	var (
		lresp   *azblob.BlobAcquireLeaseResponse
		expires time.Time
	)
	if opts.lock {
		expires = time.Now().Add(60 * time.Second)
		lresp, err = u.AcquireLease(propCtx, "", 60, azblob.ModifiedAccessConditions{})
		if err != nil {
			return nil, fmt.Errorf("could not acquire lease on file(%s): %w", name, err)
		}
	}

	props, err := u.GetProperties(propCtx, azblob.BlobAccessConditions{}, azblob.ClientProvidedKeyOptions{})

	// NOTE: These are not fully implemented because I have no idea what all the return
	// error codes are. So this is generally assuming that the error is that they can't
	// find the file.

	switch {
	// The user didn't specify to create the file and the file did not exist.
	case !isFlagSet(opts.flags, os.O_CREATE) && err != nil:
		return nil, &fs.PathError{
			Op:   "open",
			Path: name,
			Err:  fmt.Errorf("(%s): no such file or directory, if you want to create the file, must pass os.O_CREATE", err),
		}
	case isFlagSet(opts.flags, os.O_EXCL) && err == nil:
		return nil, &fs.PathError{
			Op:   "open",
			Path: name,
			Err:  fmt.Errorf("(%s)file already exists and passed os.O_EXCL", err),
		}
	}

	var leaseID string
	if lresp != nil {
		leaseID = lresp.LeaseID()
	}

	file := &File{
		flags:   opts.flags,
		u:       u.ToBlockBlobURL(),
		fi:      newFileInfo(name, props),
		leaseID: leaseID,
		expires: expires,
		closed:  signal.New(),
	}

	if file.leaseID != "" {
		file.renew()
	}
	return file, nil
}

// WriteFile implements jsfs.Writer. This implementation takes a lock on each file. Use OpenFile()
// if you do not with to use locking or want to use other options.
func (f *FS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	fsFile, err := f.OpenFile(name, 0644, WithFlags(os.O_WRONLY), WithLock())
	if err != nil {
		return err
	}

	file := fsFile.(*File)
	_, err = file.Write(data)
	if err != nil {
		return err
	}
	return file.Close()
}

// Sys is returned on a FileInfo.Sys() call.
type Sys struct {
	// Props holds propertis of the blobstore file.
	Props *azblob.BlobGetPropertiesResponse
}

type fileInfo struct {
	name string
	dir  bool
	resp *azblob.BlobGetPropertiesResponse
}

func newFileInfo(name string, resp *azblob.BlobGetPropertiesResponse) fileInfo {
	return fileInfo{
		name: name,
		resp: resp,
	}
}

// Name implements fs.FileInfo.Name().
func (f fileInfo) Name() string {
	return f.name
}

// Size implements fs.FileInfo.Size().
func (f fileInfo) Size() int64 {
	if f.dir {
		return 0
	}
	return f.resp.ContentLength()
}

// Mode implements fs.FileInfo.Mode(). This always returns 0660.
func (f fileInfo) Mode() fs.FileMode {
	if f.dir {
		return 0660 | fs.ModeDir
	}
	return 0660
}

// ModTime implements fs.FileInfo.ModTime(). If the blob is a directory, this
// is always the zero time.
func (f fileInfo) ModTime() time.Time {
	if f.dir {
		return time.Time{}
	}
	return f.resp.LastModified()
}

// IsDir implements fs.FileInfo.IsDir().
func (f fileInfo) IsDir() bool {
	return f.dir
}

// Sys implements fs.FileInfo.Sys(). If this is a dir, this returns nil.
func (f fileInfo) Sys() interface{} {
	if f.dir {
		return nil
	}
	return Sys{Props: f.resp}
}
