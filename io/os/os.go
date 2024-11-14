// Package os provides an io.FS that is implemented using the os package.
package os

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	jsfs "github.com/gopherfs/fs"
)

// File implememnts fs.File.
type File struct {
	file *os.File
}

// OSFile returns the underlying *os.File.
func (f *File) OSFile() *os.File {
	return f.file
}

// ReadDir reads the directory named by dirname and returns a list of directory entries sorted by filename.
func (f *File) ReadDir(n int) ([]fs.DirEntry, error) {
	return f.file.ReadDir(n)
}

// Read reads up to len(b) bytes from the File. It returns the number of bytes read and any error encountered.
func (f *File) Read(b []byte) (n int, err error) {
	return f.file.Read(b)
}

// Seek sets the offset for the next Read or Write on file to offset, interpreted according to whence:
// 0 means relative to the origin of the file, 1 means relative to the current offset, and
// 2 means relative to the end. It returns the new offset and an error, if any.
func (f *File) Seek(offset int64, whence int) (ret int64, err error) {
	return f.file.Seek(offset, whence)
}

// Stat returns the FileInfo structure describing file. If there is an error, it will be of type *PathError.
func (f *File) Stat() (fs.FileInfo, error) {
	return f.file.Stat()
}

// Write writes len(b) bytes to the File. It returns the number of bytes written and an error, if any.
func (f *File) Write(b []byte) (n int, err error) {
	return f.file.Write(b)
}

// Close closes the File, rendering it unusable for I/O. It returns an error, if any.
func (f *File) Close() error {
	return f.file.Close()
}

// Sync commits the current contents of the file to stable storage. Typically,
// this means flushing the file system's in-memory copy of recently written data to disk.
func (f *File) Sync() error {
	return f.file.Sync()
}

// Truncate changes the size of the file. It does not change the I/O offset.
func (f *File) Truncate(size int64) error {
	return f.file.Truncate(size)
}

type fileInfo struct {
	fs.FileInfo
}

// FS is a filesystem tied to the os filesystem implementing:
//   - fs.ReadDirFS
//   - fs.StatFS
//   - fs.ReadFileFS
//   - fs.GlobFS
//
// using functions defined in the "os" and "filepath" packages. In addition
// this supports:
//   - gfs.Writer
//   - gfs.MkdirAllFS
//   - gfs.Remove
//
// Where "gfs" is github.com/gopherfs/fs .
type FS struct {
	rootedAt string
	logger   jsfs.Logger
}

// Option is an optional argumetn for FS.
type Option func(f *FS)

// WithLogger adds a custom Logger. Defaults to using the stdlib logger.
func WithLogger(l jsfs.Logger) Option {
	return func(f *FS) {
		f.logger = l
	}
}

// New is the constructor for FS.
func New(options ...Option) (*FS, error) {
	f := &FS{logger: jsfs.DefaultLogger{}}
	for _, o := range options {
		o(f)
	}
	return f, nil
}

// Open implements fs.FS.Open().
func (f *FS) Open(name string) (fs.File, error) {
	file, err := os.Open(filepath.Join(f.rootedAt, name))
	if err != nil {
		return nil, err
	}
	return &File{file}, nil
}

// ReadDir implements fs.ReadDirFS.ReadDir().
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(name)
}

// Stat implememnts fs.StatFS.Stat().
func (f *FS) Stat(name string) (fs.FileInfo, error) {
	fi, err := os.Stat(filepath.Join(f.rootedAt, name))
	if err != nil {
		return nil, err
	}
	return fileInfo{fi}, nil
}

// ReadFile implements fs.ReadFileFS.ReadFile().
func (f *FS) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(f.rootedAt, name))
}

// WriteFile implements jsfs.Writer.WriteFile(). If the file exists this will
// attempt to write over it.
func (f *FS) WriteFile(name string, content []byte, perm fs.FileMode) error {
	p := filepath.Join(f.rootedAt, name)

	return os.WriteFile(p, content, perm)
}

// Glob implements fs.GlobFS.Glob().
func (f *FS) Glob(pattern string) (matches []string, err error) {
	return filepath.Glob(filepath.Join(f.rootedAt, pattern))
}

type ofOptions struct {
	flags int
}

func (o *ofOptions) defaults() {
	if o.flags == 0 {
		o.flags = os.O_RDONLY
	}
}

// WithFlags sets the flags based on package "os" flag values. By default this is O_RDONLY.
func WithFlags(flags int) jsfs.OFOption {
	return func(i interface{}) error {
		v, ok := i.(*ofOptions)
		if !ok {
			return fmt.Errorf("WithFlags() call received %T, expected *os.ofOptions", i)
		}
		v.flags = flags
		return nil
	}
}

// OpenFile opens a file with the set flags and fs.FileMode. If you want to use the fs.File
// to write, you need to type assert if to *os.File. If Opening a file for
func (f *FS) OpenFile(name string, perms fs.FileMode, options ...jsfs.OFOption) (fs.File, error) {
	opts := ofOptions{}
	opts.defaults()

	for _, o := range options {
		if err := o(&opts); err != nil {
			return nil, err
		}
	}

	file, err := os.OpenFile(filepath.Join(f.rootedAt, name), opts.flags, perms)
	if err != nil {
		return nil, err
	}
	return &File{file}, nil
}

// Sub implements io/fs.SubFS.
func (f *FS) Sub(dir string) (fs.FS, error) {
	stat, err := f.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !stat.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", dir)
	}
	return &FS{logger: f.logger, rootedAt: filepath.Join(f.rootedAt, dir)}, nil
}

// Mkdir implements os.Mkdir().
func (f *FS) Mkdir(path string, perm fs.FileMode) error {
	return os.Mkdir(filepath.Join(f.rootedAt, path), perm)
}

// MkdirAll implements os.MkdirAll().
func (f *FS) MkdirAll(path string, perm fs.FileMode) error {
	return os.MkdirAll(filepath.Join(f.rootedAt, path), perm)
}

// Remove implements os.Remove().
func (f *FS) Remove(name string) error {
	return os.Remove(filepath.Join(f.rootedAt, name))
}

// RemoveAll implements os.RemoveAll().
func (f *FS) RemoveAll(path string) error {
	return os.RemoveAll(filepath.Join(f.rootedAt, path))
}
