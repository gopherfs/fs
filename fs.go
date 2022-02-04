/*
Package fs contains abstractions not provided by io/fs needed to provide services such
as writing files and utility functions that can be useful.

OpenFiler provides OpenFile() similar to the "os" package when you need to write file data.

Writer provides a WriteFile() similar to the "os" package when you want to write an entire file
at once.

OFOption provides a generic Option type for implementations of OpenFile() to use.

This package also introduces a Merge() function to allow merging of filesystem content into
another filesystem and the ability to tranform the content in some way (like optimizations).

Using Merge to optimize embed.FS Javascript into a subdirectory "js":
	optimized := simple.New(simple.WithPearson())

	err := Merge(
		optimized,
		somePkg.Embeded,
		"/js/",
		WithTransform(
			func(name string, content []byte) ([]byte, error){
				// If we are in debug mode, we want unoptimized Javascript
				if debug {
					return content, nil
				}
				switch path.Ext(name){
				case "js":
					return optimizeJS(content)
				case "go":
					return nil, nil
				}
				return content, nil
			},
		),
	)
	if err != nil {
		// Do something
	}
	optimized.RO()

The above code takes embedded Javscript stored in an embed.FS and if we are not in a debug mode,
optimized the Javscript with an optimizer. This allows us to keep our embed.FS local to the code
that directly uses it and create an overall filesystem for use by all our code while also
optmizing that code for production.
*/
package fs

import (
	"io/fs"
)

// OFOption is an option for the OpenFiler.OpenFile() call. The passed "o" arg
// is implementation dependent.
type OFOption func(o interface{}) error

// OpenFiler provides a more robust method of opening a file that allows for additional
// capabilities like writing to files. The fs.File and options are generic and implementation
// specific. To gain access to additional capabilities usually requires type asserting the fs.File
// to the implementation specific type.
type OpenFiler interface {
	fs.FS

	// OpenFile opens the file at name with fs.FileMode. The set of options is implementation
	// dependent. The fs.File that is returned should be type asserted to gain access to additional
	// capabilities. If opening for ReadOnly, generally the standard fs.Open() call is better.
	OpenFile(name string, perm fs.FileMode, options ...OFOption) (fs.File, error)
}

// Writer provides a filesystem implememnting OpenFiler with a simple way to write an entire file.
type Writer interface {
	OpenFiler

	// WriteFile writes a file's content to the file system. This implementation may
	// return fs.ErrExist if the file already exists. The FileMode
	// may or may not be honored by the implementation.
	WriteFile(name string, data []byte, perm fs.FileMode) error
}

// MkdirAllFS provides a filesystem that impelments MkdirAll(). An FS not implementing this is
// expected to create the directory structure on a file write.
type MkdirAllFS interface {
	OpenFiler

	// MkdirAll creates a directory named path, along with any necessary parents, and returns nil, or else returns an error.
	// The permission bits perm (before umask) are used for all directories that MkdirAll creates.
	// If path is already a directory, MkdirAll does nothing and returns nil.
	MkdirAll(path string, perm fs.FileMode) error
}
