/*
Package disk provides an FS that wraps the johnsiilver/fs/os package to be
used for a disk cache that expires files.

Example use:
	diskFS, err := disk.New(
		"",
		disk.WithExpireCheck(5 * time.Second),
		disk.WithExpireFiles(10 * time.Second),
	)
	if err != nil {
		// Do something
	}
	defer os.RemoveAll(diskFS.Location())
*/
package disk

import (
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	jsfs "github.com/gopherfs/fs"
	"github.com/gopherfs/fs/io/cache"
	osfs "github.com/gopherfs/fs/io/os"
)

const replaceWith = `_-_-_`

var _ cache.CacheFS = &FS{}

// FS provides a disk cache based on the johnsiilver/fs/os package. FS must have
// Close() called to stop internal goroutines.
type FS struct {
	fs *osfs.FS

	logger jsfs.Logger

	location       string
	openTimeout    time.Duration
	expireDuration time.Duration
	index          *index

	writeFileOFOptions []writeFileOptions

	closeCh   chan struct{}
	checkTime time.Duration
}

// Option is an optional argument for the New() constructor.
type Option func(f *FS) error

// WithExpireCheck changes at what interval we check for file expiration.
func WithExpireCheck(d time.Duration) Option {
	return func(f *FS) error {
		f.checkTime = d
		return nil
	}
}

func WithExpireFiles(d time.Duration) Option {
	return func(f *FS) error {
		f.expireDuration = d
		return nil
	}
}

// WithLogger allows setting a customer Logger. Defaults to using the
// stdlib logger.
func WithLogger(l jsfs.Logger) Option {
	return func(f *FS) error {
		f.logger = l
		return nil
	}
}

type writeFileOptions struct {
	regex   *regexp.Regexp
	options []jsfs.OFOption
}

// WithWriteFileOFOption uses a regex on the file path given and if it matches
// will apply the options provided on that file when .WriteFile() is called.
// First match wins. A "nil" for a regex applies to all that are not matched. It is suggested
// for speed reasons to keep this relatively small or the first rules should match
// the majority of files. This can be passed multiple times with different regexes. Options passed
// must be options from this package.
func WithWriteFileOFOptions(regex *regexp.Regexp, options ...jsfs.OFOption) Option {
	return func(f *FS) error {
		f.writeFileOFOptions = append(f.writeFileOFOptions, writeFileOptions{regex: regex, options: options})
		return nil
	}
}

// New creates a new FS that uses disk located at 'location' to store cache data.
// If location == "", a new cache root is setup in TEMPDIR with prepended name
// "diskcache_". It is the responsibility of the caller to cleanup the disk.
func New(location string, options ...Option) (*FS, error) {
	if location == "" {
		var err error
		location, err = ioutil.TempDir("", "diskcache_")
		if err != nil {
			return nil, err
		}
	} else {
		fi, err := os.Stat(location)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("location(%s) was not a directory", location)
		}
	}

	sys := &FS{
		logger:         jsfs.DefaultLogger{},
		location:       location,
		expireDuration: 30 * time.Minute,
		openTimeout:    3 * time.Second,
		checkTime:      1 * time.Minute,
	}

	for _, o := range options {
		if err := o(sys); err != nil {
			return nil, err
		}
	}

	fs, err := osfs.New(osfs.WithLogger(sys.logger))
	if err != nil {
		return nil, err
	}
	sys.fs = fs
	sys.index = newIndex(location, sys.logger, sys.expireDuration)

	go sys.expireLoop()

	return sys, nil
}

func (f *FS) Close() {
	close(f.closeCh)
}

// Location returns the location of our disk cache.
func (f *FS) Location() string {
	return f.location
}

// Open implements fs.FS.Open(). fs.File is an *johnsiilver/fs/os/File.
func (f *FS) Open(name string) (fs.File, error) {
	file, err := f.fs.Open(f.diskFilePath(name))
	if err != nil {
		return nil, err
	}

	return file, nil
}

type ofOptions struct {
	flags int
}

func (o *ofOptions) defaults() {
	o.flags = os.O_RDONLY
}

func (o ofOptions) toOsOFOptions() []jsfs.OFOption {
	var options []jsfs.OFOption
	if o.flags != 0 {
		options = append(options, osfs.WithFlags(o.flags))
	}
	return options
}

// FileMode sets the fs.FileMode when opening a file with OpenFile().
func WithFlags(flags int) jsfs.OFOption {
	return func(o interface{}) error {
		v, ok := o.(*ofOptions)
		if !ok {
			return fmt.Errorf("disk.WithFlags received wrong type %T", o)
		}
		v.flags = flags
		return nil
	}
}

// OpenFile implements fs.OpenFiler.OpenFile().
func (f *FS) OpenFile(name string, perms fs.FileMode, options ...jsfs.OFOption) (fs.File, error) {
	opts := ofOptions{}
	opts.defaults()

	for _, o := range options {
		if err := o(&opts); err != nil {
			return nil, err
		}
	}

	file, err := f.fs.OpenFile(f.diskFilePath(name), perms, opts.toOsOFOptions()...)
	if err != nil {
		return nil, err
	}

	f.index.addOrUpdate(name)

	return file, nil
}

// ReadFile implements fs.ReadFileFS.ReadFile().
func (f *FS) ReadFile(name string) ([]byte, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}

	return io.ReadAll(file)
}

func (f *FS) Stat(name string) (fs.FileInfo, error) {
	return f.fs.Stat(f.diskFilePath(name))
}

func (f *FS) WriteFile(name string, content []byte, perm fs.FileMode) error {
	if err := f.fs.WriteFile(f.diskFilePath(name), content, perm); err != nil {
		f.logger.Println("happened here: ", err)
		return err
	}
	f.logger.Println("worked file: ", f.diskFilePath(name))
	f.index.addOrUpdate(name)

	return nil
}

func (f *FS) expireLoop() {
	for {
		select {
		case <-f.closeCh:
			return
		case <-time.After(f.checkTime):
			f.index.deleteOld()
		}
	}
}

func (f *FS) diskFilePath(name string) string {
	return filepath.Join(f.location, nameTransform(name))
}

func nameTransform(name string) string {
	return strings.Replace(name, "/", "_slash_", -1)
}
