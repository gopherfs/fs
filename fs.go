// Package fs contains abstractions not provided by io/fs needed to provide services such
// as writing files.
package fs

import (
	"io/fs"
)

// OFOptions provides optional values for OpenFile() implementations.
type OFOptions struct {
	defaults Defaulter
	values map[interface{}]interface{}
}

// Defaulter allows putting a Defaulter that can be called to set defaults.
func (o *OFOptions) Defaulter(d Defaulter) {
	o.defauls = d
}

// Put puts a key and value into the OFOptions. This is used by implementations of OpenFile()
// to store their unique options. Like a Context value, keys should be private types to a package
// to avoid collisions.
func (o *OFOptions) Put(key, value interface{}) {
	o.values[key] = value
}

// Value retrieves a value at given key. If key does not exist, the returned value is nil.
func (o *OFOptions) Value(key interface{}) interface{} {
	return o.values[key]
}

// Defaulter is a function that sets default values on OFOptions.
type Defaulter func(o *OFOptions)

// OFOption is an option for the OpenFiler.OpenFile() call. The passed "o" arg
// is implementation dependent.
type OFOption func(o *OFOptions) error

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
