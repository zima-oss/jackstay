package scopes

import (
	"io/fs"
	"syscall"
	"time"
)

// bornAt is when a file was made (its birth time), else its modification time.
func bornAt(info fs.FileInfo) time.Time {
	if status, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(status.Birthtimespec.Unix())
	}
	return info.ModTime()
}
