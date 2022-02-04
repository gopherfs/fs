/*
Package groupcache is an fs.FS wrapper for caching purposes built around Brad Fitzpatrick's
groupcache.

This package behaves slightly different than our other cache packages due to its
unique characteristics such as key groupings and automatic cache filling. These
features which make groupcache great are slightly annoying in this context (nothing
is perfect for everything).
*/
package groupcache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/golang/groupcache"
	jsfs "github.com/gopherfs/fs"
	"github.com/gopherfs/fs/io/cache"
)

var _ cache.CacheFS = &FS{}

// FS represents a fs.FS that implements OpenFile() and allows reading and writing to a groupcache.
type FS struct {
	picker groupcache.PeerPicker
	mu     sync.Mutex
	groups map[string]*groupcache.Group

	openTimeout time.Duration

	filler cache.CacheFS
}

// New creates a new FS.
func New(picker groupcache.PeerPicker) (*FS, error) {
	f := &FS{
		picker:      picker,
		groups:      map[string]*groupcache.Group{},
		openTimeout: 3 * time.Second,
	}

	groupcache.RegisterPeerPicker(f.registation)
	return f, nil
}

func (f *FS) registation() groupcache.PeerPicker {
	return f.picker
}

// NewGroup creates a new groupcache group which acts like a top level directory.
// sizeInBytes is the maximum size in bytes that the group can hold. Trying to open
// a file in a path without a group that is recognized will fail.
func (f *FS) NewGroup(name string, sizeInBytes int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.groups[name]; ok {
		return fmt.Errorf("cannot create top directory(%s): already exists", name)
	}

	if err := isValid(name); err != nil {
		return err
	}

	f.groups[name] = groupcache.NewGroup(
		name,
		sizeInBytes,
		groupcache.GetterFunc(
			func(ctx groupcache.Context, key string, dest groupcache.Sink) error {
				b, err := f.filler.ReadFile(key)
				if err != nil {
					return err
				}
				return dest.SetBytes(b)
			},
		),
	)
	return nil
}

// SetFiller implements cache.SetFiller.SetFiller().
func (f *FS) SetFiller(fsys cache.CacheFS) {
	f.filler = fsys
}

func isValid(s string) error {
	for i := 0; i < len(s); i++ {
		if s[i] > unicode.MaxASCII {
			return fmt.Errorf("name must be ascii characters")
		}
		switch string(s[i]) {
		case ".":
			return fmt.Errorf("cannot have a '.' in the name")
		case "/":
			return fmt.Errorf("cannot have a / in the name")
		case `\`:
			return fmt.Errorf(`cannot have a \ in the name"`)
		}
		if unicode.IsSpace(rune(s[i])) {
			return fmt.Errorf("cannot have a space character in the name")
		}
	}
	return nil
}

func (f *FS) Open(name string) (fs.File, error) {
	ctx, cancel := context.WithTimeout(context.Background(), f.openTimeout)
	defer cancel()

	sp := strings.Split(name, "/")
	if len(sp) == 1 {
		return nil, fmt.Errorf("invalid path(%s)", name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	group, ok := f.groups[sp[0]]
	if !ok {
		return nil, fmt.Errorf("groupcache.FS: group(%s) from path(%s) does not exist", sp[0], name)
	}

	var data []byte
	err := group.Get(ctx, strings.Join(sp[1:], "/"), groupcache.AllocatingByteSliceSink(&data))
	if err != nil {
		return nil, err
	}

	return &readFile{
		content: data,
		fi:      fileInfo{name: name, size: int64(len(data))},
	}, nil
}

// OpenFile implements fs.OpenFiler.OpenFile().
// When writing a file, the file is not written until Close() is called on the file.
// Perms are ignored by OpenFile.
func (f *FS) OpenFile(name string, perms fs.FileMode, options ...jsfs.OFOption) (fs.File, error) {
	if len(options) > 0 {
		return nil, fmt.Errorf("groupcache.FS.OpenFile() does not support any options yet options were passed")
	}

	if f.filler == nil {
		return nil, fmt.Errorf("groupcache.FS.SetFiller has not been called")
	}

	return &writefile{
		name:    name,
		content: &bytes.Buffer{},
		client:  f.filler,
	}, nil
}

// ReadFile implements fs.ReadFileFS.ReadFile().
func (f *FS) ReadFile(name string) ([]byte, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	r := file.(*readFile)
	return r.content, nil
}

// Stat implements fs.StatFS.Stat(). The FileInfo returned name and size can be used,
// but the others are static values. ModTime will always be the zero value. It should
// be noted that this is simple a bad wrapper on Open(), so the content is read
// as I did not see a way to query Redis for just the key size (and to be honest,
// I didn't dig to hard).
func (f *FS) Stat(name string) (fs.FileInfo, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	rf := file.(*readFile)
	return rf.fi, nil
}

// WriteFile writes a file to name with content. This uses O_WRONLY | O_CREATE | O_TRUNC, so
// it will overrite an existing entry. If you passed WithWriteFileOFOptions(), it will
// use those options if name matches a regex. Passed perm must be 0644.
func (f *FS) WriteFile(name string, content []byte, perm fs.FileMode) error {
	if !perm.IsRegular() {
		return fmt.Errorf("non-regular file (perm mode bits are set)")
	}

	if perm != 0644 {
		return fmt.Errorf("only support mode 0644")
	}

	file, err := f.OpenFile(name, 0644)
	if err != nil {
		return err
	}

	wf := file.(*writefile)
	_, err = wf.Write(content)
	if err != nil {
		return err
	}
	return wf.Close()
}

type readFile struct {
	content []byte
	fi      fileInfo
	index   int
}

func (f *readFile) Stat() (fs.FileInfo, error) {
	return f.fi, nil
}

func (f *readFile) Read(b []byte) (int, error) {
	if f.index >= len(f.content) {
		return 0, io.EOF
	}

	n := copy(b, f.content[int(f.index):])
	f.index += n
	return n, nil
}

func (f *readFile) Close() error {
	return nil
}

type fileInfo struct {
	name string
	size int64
}

func (f fileInfo) Name() string {
	return f.name
}

func (f fileInfo) Size() int64 {
	return f.size
}

func (f fileInfo) Mode() fs.FileMode {
	return 0644
}

func (f fileInfo) ModTime() time.Time {
	return time.Time{}
}

func (f fileInfo) IsDir() bool {
	return false
}

func (f fileInfo) Sys() interface{} {
	return nil
}

func isFlagSet(flags, flag int) bool {
	return flags&flag != 0
}

type writefile struct {
	name    string
	content *bytes.Buffer

	sync.Mutex
	closed bool

	client jsfs.Writer
}

func (f *writefile) Stat() (fs.FileInfo, error) {
	f.Lock()
	defer f.Unlock()

	return nil, fmt.Errorf("Stat() not supported on a writeable fs.File")
}

func (f *writefile) Read(b []byte) (int, error) {
	f.Lock()
	defer f.Unlock()

	return 0, fmt.Errorf("Read() not supported on writeable fs.File")
}

func (f *writefile) Write(b []byte) (int, error) {
	f.Lock()
	defer f.Unlock()

	return f.content.Write(b)
}

func (f *writefile) Close() error {
	f.Lock()
	defer f.Unlock()
	if f.closed {
		return fmt.Errorf("file is closed")
	}

	err := f.client.WriteFile(f.name, f.content.Bytes(), 0644)
	if err == nil {
		f.closed = true
		return nil
	}
	return err
}
