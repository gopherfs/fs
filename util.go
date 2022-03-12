package fs

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
)

type mergeOptions struct {
	fileTransform FileTransform
}

// MergeOption is an optional argument for Merge().
type MergeOption func(o *mergeOptions)

// FileTransform gives the base name of a file and the content of the file. It returns
// the content that MAY be transformed in some way. If this return a nil for []byte and
// a nil error, this file is skipped.
type FileTransform func(name string, content []byte) ([]byte, error)

// WithTransform instructs the Merge() to use a FileTransform on the files it reads before
// writing them to the destination.
func WithTransform(ft FileTransform) MergeOption {
	return func(o *mergeOptions) {
		o.fileTransform = ft
	}
}

// Merge will merge "from" into "into" by walking "from" the root "/". Each file will be
// prepended with "prepend" which must start and end with "/". If into does not
// implement Writer, this will panic. If the file already exists, this will error and
// leave a partial copied fs.FS.
func Merge(into Writer, from fs.FS, prepend string, options ...MergeOption) error {
	// Note: Testing this is done inside simple_test.go, to avoid some recursive imports
	opt := mergeOptions{}
	for _, o := range options {
		o(&opt)
	}

	if prepend == "/" {
		prepend = ""
	}
	if prepend != "" {
		if !strings.HasSuffix(prepend, "/") {
			return fmt.Errorf("prepend(%s) does not end with '/'", prepend)
		}
		prepend = strings.TrimPrefix(prepend, ".")
		prepend = strings.TrimPrefix(prepend, "/")
	}

	fn := func(p string, d fs.DirEntry, err error) error {
		switch p {
		case "/", "":
			return nil
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(from, p)
		if err != nil {
			return err
		}

		if opt.fileTransform != nil {
			b, err = opt.fileTransform(path.Base(p), b)
			if err != nil {
				return err
			}
			if b == nil {
				return nil
			}
		}

		return into.WriteFile(path.Join(prepend, p), b, d.Type())
	}

	return fs.WalkDir(from, ".", fn)
}
