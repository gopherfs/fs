/*
Package redis provides an io/fs.FS implementation that can be used in our cache.FS package.

Here's an example that simply accesses a local Redis instance:
	redisFS, err := redis.New(
		redis.Args{Addr: "127.0.0.1:6379"},
		// This causes all files to exire in 5 minutes.
		// You can write a complex ruleset to handle different files at
		// different rates.
		redis.WithWriteFileOFOptions(
			regexp.MustCompile(`.*`),
			redis.ExpireFiles(5 * time.Minute),
		),
	)
	if err != nil {
		// Do something
	}

	if err := redisFS.WriteFile("gopher.jpg", gopherBytes, 0644); err != nil {
		// Do something
	}
*/
package redis

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sync"
	"time"

	jsfs "github.com/gopherfs/fs"
	"github.com/gopherfs/fs/io/cache"

	"github.com/go-redis/redis/v8"
)

var _ cache.CacheFS = &FS{}

// Args is arguments to the Redis client.
type Args = redis.Options

// FS provides an io.FS implementation using Redis.
type FS struct {
	client      redis.Cmdable
	openTimeout time.Duration

	writeFileOFOptions []writeFileOptions
}

// Option is an optional argument for the New() constructor.
type Option func(f *FS) error

type writeFileOptions struct {
	regex   *regexp.Regexp
	options []jsfs.OFOption
}

// WithWriteFileOFOption uses a regex on the file path given and if it matches
// will apply the options provided on that file when .WriteFile() is called.
// First match wins. A "nil" for a regex applies to all that are not matched. It is suggested
// for speed reasons to keep this relatively small or the first rules should match
// the majority of files. This can be passed multiple times with different regexes.
func WithWriteFileOFOptions(regex *regexp.Regexp, options ...jsfs.OFOption) Option {
	return func(f *FS) error {
		f.writeFileOFOptions = append(f.writeFileOFOptions, writeFileOptions{regex: regex, options: options})
		return nil
	}
}

type ofOptions struct {
	flags       int
	expireFiles time.Duration
}

func (o *ofOptions) defaults() {
	o.flags = os.O_RDONLY
	// NOTE, this setting only works with redis server version >= 6.0
	// see https://pkg.go.dev/github.com/go-redis/redis/v8#Client.SetNX
	o.expireFiles = redis.KeepTTL
}

// ExpireFiles expires files at duration d. If not set for a file, redis.KeepTTL is used.
func ExpireFiles(d time.Duration) jsfs.OFOption {
	return func(o interface{}) error {
		opts, ok := o.(*ofOptions)
		if !ok {
			return fmt.Errorf("bug: redis.ofOptions was not passed(%T)", o)
		}
		opts.expireFiles = d
		return nil
	}
}

// Flags allows the passing of os.O_RDONLY/os.O_WRONLY/O_EXCL/O_TRUNC/O_CREATE flags to OpenFile().
// By default this is O_RDONLY.
func Flags(flags int) jsfs.OFOption {
	return func(o interface{}) error {
		opts, ok := o.(*ofOptions)
		if !ok {
			return fmt.Errorf("bug: redis.ofOptions was not passed(%T)", o)
		}
		opts.flags = flags
		return nil
	}
}

// New is the constructor for Redis.
func New(args Args, options ...Option) (*FS, error) {
	c := redis.NewClient(&args)

	r := &FS{
		client:      c,
		openTimeout: 3 * time.Second,
	}

	for _, o := range options {
		if err := o(r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Open implements fs.FS.Open().
func (f *FS) Open(name string) (fs.File, error) {
	ctx, cancel := context.WithTimeout(context.Background(), f.openTimeout)
	defer cancel()

	val, err := f.client.Get(ctx, name).Bytes()
	if err != nil {
		return nil, err
	}

	return &readFile{
		content: val,
		fi:      fileInfo{name: name, size: int64(len(val))},
	}, nil
}

// OpenFile implements fs.OpenFiler.OpenFile(). We support os.O_CREATE, os.O_EXCL, os.O_RDONLY, os.O_WRONLY,
// and os.O_TRUNC. If OpenFile is passed O_RDONLY, this calls Open() and ignores all options.
// When writing a file, the file is not written until Close() is called on the file.
func (f *FS) OpenFile(name string, mode fs.FileMode, options ...jsfs.OFOption) (fs.File, error) {
	opts := ofOptions{}
	opts.defaults()

	for _, o := range options {
		o(&opts)
	}

	if isFlagSet(opts.flags, os.O_RDONLY) {
		return f.Open(name)
	}

	if !isFlagSet(opts.flags, os.O_WRONLY) {
		return nil, fmt.Errorf("must set either O_RDONLY or O_WRONLY")
	}

	fileExists, err := f.exists(name)
	if err != nil {
		return nil, err
	}

	if fileExists {
		if isFlagSet(opts.flags, os.O_EXCL) {
			return nil, fs.ErrExist
		}
		if !isFlagSet(opts.flags, os.O_TRUNC) {
			return nil, fmt.Errorf("did not receive O_TRUNC when file exists. Redis only supports truncation")
		}
	} else {
		if !isFlagSet(opts.flags, os.O_CREATE) {
			return nil, fmt.Errorf("file (%s) did not exist and did not receive O_CREATE", name)
		}
	}

	return &writefile{
		name:    name,
		content: &bytes.Buffer{},
		ttl:     opts.expireFiles,
		client:  f.client,
	}, nil
}

// Remove attempts to remove file at name from FS.
func (f *FS) Remove(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result := f.client.Del(ctx, name)
	return result.Err()
}

func (f *FS) exists(name string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result := f.client.Exists(ctx, name)
	if result.Err() != nil {
		return false, fmt.Errorf("unable to determine if file(%s) exists: %w", name, result.Err())
	}
	if result.Val() == 1 {
		return true, nil
	}
	return false, nil
}

func isFlagSet(flags, flag int) bool {
	return flags&flag != 0
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

// WriteFile writes a file to name with content. This will overrite an existing entry.
// Passed perm must be 0644.
func (f *FS) WriteFile(name string, content []byte, perm fs.FileMode) error {
	var opts []jsfs.OFOption

	if !perm.IsRegular() {
		return fmt.Errorf("non-regular file (perm mode bits are set)")
	}

	if perm != 0644 {
		return fmt.Errorf("only support mode 0644")
	}

	for _, wfo := range f.writeFileOFOptions {
		if wfo.regex == nil {
			opts = wfo.options
			break
		}
		if wfo.regex.MatchString(name) {
			opts = wfo.options
			break
		}
	}

	opts = append(opts, Flags(os.O_WRONLY|os.O_CREATE|os.O_TRUNC))

	file, err := f.OpenFile(name, 0644, opts...)
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

type writefile struct {
	name    string
	content *bytes.Buffer
	ttl     time.Duration

	sync.Mutex
	closed bool

	client redis.Cmdable
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

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := f.client.Set(ctx, f.name, f.content.Bytes(), f.ttl).Err()
	if err == nil {
		f.closed = true
		return nil
	}
	return err
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
