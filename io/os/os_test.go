package os

import "io/fs"

var (
	_ fs.ReadDirFile = &File{}

	_ fs.ReadDirFS  = &FS{}
	_ fs.StatFS     = &FS{}
	_ fs.ReadFileFS = &FS{}
	_ fs.GlobFS     = &FS{}
)
