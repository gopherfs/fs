package simple

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	jsfs "github.com/gopherfs/fs"
)

// FS provides a simple memory structure that implements io/fs.FS and fs.Writer(above).
// This is great for aggregating several different embeded fs.FS into a single structure using
// Merge() below. It uses "/" unix separators and doesn't deal with any funky "\/" things.
// If you want to use this don't start trying to get complicated with your pathing.
// This structure is safe for concurrent reading or concurrent writing, but not concurrent
// read/write. Once finished writing files, you should call .RO() to lock it.
type FS struct {
	root *file

	writeMu sync.Mutex
	ro      bool

	pearson bool
	cache   []*file
	items   int
}

// SimpleOption provides an optional argument to NewSimple().
type SimpleOption func(s *FS)

// WithPearson will create a lookup cache using Pearson hashing to make lookups actually happen
// at O(1) (after the hash calc) instead of walking the file system tree after various strings
// splits. When using this, realize that you MUST be using ASCII characters.
func WithPearson() SimpleOption {
	return func(s *FS) {
		s.pearson = true
	}
}

// New is the constructor for Simple.
func New(options ...SimpleOption) *FS {
	return &FS{root: &file{name: ".", time: time.Now(), isDir: true}}
}

// Open implements fs.FS.Open().
func (s *FS) Open(name string) (fs.File, error) {
	if name == "/" || name == "" || name == "." {
		return s.root, nil
	}

	name = strings.TrimPrefix(name, ".")
	name = strings.TrimPrefix(name, "/")

	sp := strings.Split(name, "/")

	if s.pearson && s.ro {
		h := pearson([]byte(name))
		i := int(h) % (len(s.cache) + 1)
		if i >= len(s.cache) {
			return nil, fs.ErrNotExist
		}
		return s.cache[i].getCopy(), nil
	}

	dir := s.root
	for _, p := range sp {
		f, err := dir.Search(p)
		if err != nil {
			return nil, err
		}
		dir = f
	}
	return dir.getCopy(), nil
}

func (s *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	dir, err := s.findDir(name)
	if err != nil {
		return nil, err
	}
	return dir.objects, nil
}

// Mkdir provides a no-op Mkdir for FS. Directories are only made when
// files are written to them. But this allows for calls by code that can
// be swapped with an os FS to function.
func (s *FS) Mkdir(name string, perm fs.FileMode) error {
	return nil
}

// MkdirAll provides a no-op MkdirAll for FS. Directories are only made when
// files are written to them. But this allows for calls by code that can
// be swapped with an os FS to function.
func (s *FS) MkdirAll(path string, perm fs.FileMode) error {
	return nil
}

func (s *FS) findDir(name string) (*file, error) {
	switch name {
	case ".", "", "/":
		return s.root, nil
	}
	name = strings.TrimPrefix(name, ".")
	name = strings.TrimPrefix(name, "/")
	name = strings.TrimSuffix(name, "/")

	sp := strings.Split(name, "/")

	dir := s.root
	for _, p := range sp {
		f, err := dir.Search(p)
		if err != nil {
			return nil, fs.ErrNotExist
		}
		if !f.isDir {
			return nil, fs.ErrInvalid
		}
		dir = f
	}
	if !dir.isDir {
		return nil, fmt.Errorf("path(%s) is not a directory", name)
	}

	return dir, nil
}

// ReadFile implememnts ReadFileFS.ReadFile(). The slice returned by ReadFile is not
// a copy of the file's contents like Open().File.Read() returns. Modifying it will
// modifiy the content so BE CAREFUL.
func (s *FS) ReadFile(name string) ([]byte, error) {
	f, err := s.Open(name)
	if err != nil {
		return nil, err
	}
	r := f.(*file)
	if r.IsDir() {
		return nil, errors.New("cannot read a directory")
	}
	return r.content, nil
}

// Stat implements fs.StatFS.Stat().
func (s *FS) Stat(name string) (fs.FileInfo, error) {
	f, err := s.Open(name)
	if err == nil {
		return f.Stat()
	}
	d, err := s.findDir(name)
	if err != nil {
		return nil, fs.ErrNotExist
	}
	return d.Info()
}

type ofOptions struct {
	flags int
}

func (o *ofOptions) defaults() {
	if o.flags == 0 {
		o.flags = os.O_RDONLY
	}
}

// Flags sets the flags based on package "os" flag values. By default this is O_RDONLY.
func Flags(flags int) jsfs.OFOption {
	return func(i interface{}) error {
		v, ok := i.(*ofOptions)
		if !ok {
			return fmt.Errorf("Flags() call received %T, expected local *ofOptions", i)
		}
		v.flags = flags
		return nil
	}
}

// OpenFile implements OpenFiler. Supports flags O_RDONLY, O_WRONLY, O_CREATE, O_TRUNC and O_EXCL.
// The file returned by OpenFile is not thread-safe.
func (s *FS) OpenFile(name string, perms fs.FileMode, options ...jsfs.OFOption) (fs.File, error) {
	if !perms.IsRegular() {
		return nil, fmt.Errorf("FS does not support non-regular mode bits")
	}

	opts := ofOptions{}
	opts.defaults()
	for _, o := range options {
		if err := o(&opts); err != nil {
			return nil, err
		}
	}

	if isFlagSet(opts.flags, os.O_RDONLY) {
		return s.Open(name)
	}
	if s.ro {
		return nil, fmt.Errorf("in RO mode")
	}
	if !isFlagSet(opts.flags, os.O_WRONLY) {
		return nil, fmt.Errorf("only support O_RDONLY and O_WRONLY")
	}

	// The file already exists.
	if f, err := s.Open(name); err == nil {
		fi, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("file exists but could not Stat(): %w", err)
		}
		if fi.IsDir() {
			return nil, fmt.Errorf("cannot write to a directory")
		}
		if isFlagSet(opts.flags, os.O_EXCL) {
			return nil, fs.ErrExist
		}
		if isFlagSet(opts.flags, os.O_TRUNC) {
			return nil, fmt.Errorf("Simple only supports writing when a file exists if O_TRUNC set")
		}
		return &WRFile{f: f.(*file)}, nil
	}

	if !isFlagSet(opts.flags, os.O_CREATE) {
		return nil, fs.ErrNotExist
	}

	if err := s.WriteFile(name, []byte{}, 0660); err != nil {
		return nil, err
	}

	f, err := s.Open(name)
	if err != nil {
		return nil, fmt.Errorf("bug: we just wrote a file(%s) and then couldn't open it: %s", name, err)
	}
	return &WRFile{f: f.(*file)}, nil
}

func isFlagSet(flags int, flag int) bool {
	return flags&flag != 0
}

// WriteFile implememnts Writer. The content reference is copied, so modifying the original will
// modify it here. perm is ignored. WriteFile is not thread-safe.
func (s *FS) WriteFile(name string, content []byte, perm fs.FileMode) error {
	if s.ro {
		return fmt.Errorf("Simple is locked from writing")
	}
	if name == "" {
		panic("can't write a file at root")
	}

	if strings.HasSuffix(name, "/") {
		return fmt.Errorf("cannot write a file directory(%s)", name)
	}

	name = strings.TrimPrefix(name, ".")
	name = strings.TrimPrefix(name, "/")

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	dir := s.root
	sp := strings.Split(name, "/")
	for i := 0; i < len(sp)-1; i++ {
		f, err := dir.Search(sp[i])
		if err != nil {
			dir.createDir(sp[i])
			f, err = dir.Search(sp[i])
			if err != nil {
				panic("wtf?")
			}
			dir = f
			continue
		}
		if !f.isDir {
			return fmt.Errorf("name(%s) contains element(%d)(%s) that is not a directory", name, i, sp[i])
		}
		dir = f
	}

	n := sp[len(sp)-1]
	if _, err := dir.Search(n); err == nil {
		return fs.ErrExist
	}

	dir.addFile(&file{name: n, content: content, time: time.Now()})
	s.items++

	return nil
}

// RO locks the file system from writing.
func (s *FS) RO() {
	s.ro = true

	if s.pearson {
		sl := make([]*file, s.items)

		fs.WalkDir(
			s,
			".",
			func(path string, d fs.DirEntry, err error) error {
				if d.IsDir() {
					return nil
				}
				h := pearson([]byte(path))
				i := int(h) % (len(s.cache) + 1)
				sl[i] = d.(*file)
				return nil
			},
		)
		s.cache = sl
	}
}

// Remove removes the named file or (empty) directory. If there is an error, it will be of type *PathError.
func (s *FS) Remove(name string) error {
	return s.remove(name, false)
}

// RemoveAll removes path and any children it contains. It removes
// everything it can but returns the first error it encounters.
// If the path does not exist, RemoveAll returns nil (no error).
// If there is an error, it will be of type *fs.PathError.
func (s *FS) RemoveAll(path string) error {
	return s.remove(path, true)
}

func (s *FS) remove(name string, removeAll bool) error {
	if name == "/" || name == "" || name == "." {
		return fmt.Errorf("cannot Remove() the root directory")
	}

	name = strings.TrimPrefix(name, ".")
	name = strings.TrimPrefix(name, "/")

	sp := strings.Split(name, "/")

	if s.pearson && s.ro {
		return &fs.PathError{
			Op:   "Remove",
			Path: name,
			Err:  fmt.Errorf("read only filesystem set"),
		}
	}

	parent := s.root
	var f *file
	for i, p := range sp {
		var err error
		f, err = parent.Search(p)
		if err != nil {
			return &fs.PathError{Op: "Remove", Path: name, Err: err}
		}

		// We are the last element.
		if i+1 == len(sp) {
			if removeAll {
				if !f.isDir {
					return &fs.PathError{Op: "Remove", Path: name, Err: fs.ErrInvalid}
				}
			} else {
				// Make sure what we are removing is a file.
				if f.isDir {
					return &fs.PathError{Op: "Remove", Path: name, Err: fs.ErrInvalid}
				}
			}
			if err := parent.remove(p, removeAll); err != nil {
				return &fs.PathError{Op: "Remove", Path: name, Err: err}
			}
		}

		// Only the last entry can be a file.
		if !f.isDir {
			return &fs.PathError{Op: "Remove", Path: name, Err: fs.ErrInvalid}
		}

		parent = f
	}
	if err := parent.remove(name, removeAll); err != nil {
		return &fs.PathError{Op: "Remove", Path: name, Err: err}
	}
	return nil
}

// WRFile provides an io.WriteCloser implementation.
type WRFile struct {
	content []byte
	f       *file
}

func (w *WRFile) Read(b []byte) (n int, err error) {
	return 0, fmt.Errorf("cannot read from a file in O_WRONLY")
}

func (w *WRFile) Stat() (fs.FileInfo, error) {
	return nil, fmt.Errorf("cannot stat a file in O_WRONLY")
}

func (w *WRFile) Write(b []byte) (n int, err error) {
	w.content = append(w.content, b...)
	return len(b), nil
}

func (w *WRFile) Close() error {
	w.f.content = w.content
	return nil
}

type file struct {
	name    string
	content []byte
	offset  int64
	time    time.Time
	isDir   bool

	objects []fs.DirEntry
}

func (f *file) getCopy() *file {
	n := *f
	return &n
}

// createDir creates a new *file representing a dir inside this file (which must represent a dir).
func (f *file) createDir(name string) {
	if !f.isDir {
		panic("bug: createDir() called on file with isDir == false")
	}

	n := &file{name: name, isDir: true}
	f.objects = append(f.objects, n)
	sort.Slice(f.objects,
		func(i, j int) bool {
			return f.objects[i].Name() < f.objects[j].Name()
		},
	)
}

func (f *file) addFile(nf *file) {
	if !f.isDir {
		panic("bug: cannot add a file to a non-directory")
	}
	f.objects = append(f.objects, nf)
	sort.Slice(f.objects,
		func(i, j int) bool {
			return f.objects[i].Name() < f.objects[j].Name()
		},
	)
}

// remove removes the path from the file if file.isDir == true.
// If removeAll is set, the name must be a *file with .isDir == true
// and will remove it and all contained files.
func (f *file) remove(name string, removeAll bool) error {
	if len(f.objects) == 0 {
		return fs.ErrNotExist
	}

	if !f.isDir {
		return fmt.Errorf("not a directory")
	}

	x := sort.Search(
		len(f.objects),
		func(i int) bool {
			return f.objects[i].(*file).name >= name
		},
	)
	var found *file

	if x < len(f.objects) && f.objects[x].(*file).name == name {
		found = f.objects[x].(*file)
	}
	if found == nil {
		return fs.ErrNotExist
	}

	if removeAll {
		if !found.isDir {
			return fmt.Errorf("not a directory")
		}
	} else {
		if found.isDir {
			// Remove() can get rid of empty directories.
			if len(found.objects) > 0 {
				return fmt.Errorf("directory was not empty")
			}
		}
	}

	n := make([]fs.DirEntry, 0, len(f.objects)-1)
	switch x {
	case 0:
		n = append(n, f.objects[1:]...)
	case len(f.objects) - 1:
		n = f.objects[0 : len(f.objects)-1]
	default:
		n = append(n, f.objects[0:x]...)
		n = append(n, f.objects[x+1:]...)
	}
	f.objects = n
	return nil
}

// Search searches for the sub file named "name". This only works if isDir is true.
func (f *file) Search(name string) (*file, error) {
	if !f.isDir {
		return nil, errors.New("not a directory")
	}

	if len(f.objects) == 0 {
		return nil, fs.ErrNotExist
	}

	x := sort.Search(
		len(f.objects),
		func(i int) bool {
			return f.objects[i].(*file).name >= name
		},
	)
	if x < len(f.objects) && f.objects[x].(*file).name == name {
		return f.objects[x].(*file), nil
	}
	return nil, fs.ErrNotExist
}

func (f *file) Name() string {
	return f.name
}

func (f *file) IsDir() bool {
	return f.isDir
}

const fileMode fs.FileMode = 0444

func (f *file) Type() fs.FileMode {
	return fileMode
}

func (f *file) Info() (fs.FileInfo, error) {
	fi, _ := f.Stat()
	return fi, nil
}

func (f *file) Stat() (fs.FileInfo, error) {
	return fileInfo{
		name:  f.name,
		size:  int64(len(f.content)),
		time:  f.time,
		isDir: f.isDir,
	}, nil
}

// Read implements io.Reader.
func (f *file) Read(b []byte) (int, error) {
	if f.isDir {
		return 0, fmt.Errorf("cannot Read() a directory")
	}
	if len(b) == 0 {
		return 0, nil
	}
	if int(f.offset) >= len(f.content) {
		return 0, io.EOF
	}
	i := copy(b, f.content[f.offset:])
	f.offset += int64(i)
	return i, nil
}

// Seek implement io.Seeker.
func (f *file) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		if offset < 0 {
			return 0, fmt.Errorf("can't seek beyond start of file")
		}
		f.offset = offset
		return f.offset, nil
	case io.SeekCurrent:
		if f.offset+offset < 0 {
			return 0, fmt.Errorf("can't seek beyond start of file")
		}
		f.offset += offset
		return f.offset, nil
	case io.SeekEnd:
		if len(f.content)+int(offset) < 0 {
			return 0, fmt.Errorf("can't seek beyond start of file")
		}
		f.offset = int64(len(f.content)) + offset
		return f.offset, nil
	}
	return 0, fmt.Errorf("whence value was invalid(%d)", whence)
}

// Close implememnts io.Closer.
func (f *file) Close() error {
	return nil
}

// Sync provides a no-op implementation of the os.File.Sync() method.
func (f *file) Sync() error {
	return nil
}

type fileInfo struct {
	name  string
	size  int64
	time  time.Time
	isDir bool
}

func (f fileInfo) Name() string {
	return f.name
}

func (f fileInfo) Size() int64 {
	return f.size
}
func (f fileInfo) Mode() fs.FileMode {
	return fileMode
}
func (f fileInfo) ModTime() time.Time {
	return f.time
}
func (f fileInfo) IsDir() bool {
	return f.isDir
}
func (f fileInfo) Sys() interface{} {
	return nil
}
