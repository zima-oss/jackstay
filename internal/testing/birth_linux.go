package scopes

import (
	"io/fs"
	"time"
)

// bornAt is when a file was last modified: the birth time is not in Linux's stat.
func bornAt(info fs.FileInfo) time.Time { return info.ModTime() }
