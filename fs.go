// Package fs contains abstractions not provided by io/fs needed to provide services such
// as writing files.
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

// Writer provides a filesystem implememnting OpenFiler with a simple way to write and entire file.
type Writer interface {
	OpenFiler

	// Writes file with name (full path) a content to the file system. This implementation may
	// return fs.ErrExist if the file already exists and the FileSystem is write once. The FileMode
	// may or may not be honored, see the implementation details for more information.
	WriteFile(name string, data []byte, perm fs.FileMode) error
}
