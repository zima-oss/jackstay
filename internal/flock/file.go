package flock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// ErrBusy is a plain lock another process holds, when waiting was not asked for.
var ErrBusy = errors.New("held by another process")

// File is a plain exclusive `flock` on a file: no line, no tickets, no holder's words (the
// machine-wide lock's queue is Acquire's alone). The kernel gives it back when the process dies.
type File struct {
	mutex sync.Mutex
	file  *os.File
}

// TryFile locks the file at path without waiting, making it (and its folder) when missing;
// ErrBusy when another process holds it. The file locked must still be the file at the path: one
// removed and made again meanwhile would be a second lock, so that is ErrBusy too.
func TryFile(path string) (*File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- the caller's own lock file
	if err != nil {
		return nil, err
	}
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) // #nosec G115 -- a descriptor number
	if err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	opened, openedErr := file.Stat()
	named, namedErr := os.Stat(path)
	if openedErr != nil || namedErr != nil || !os.SameFile(opened, named) {
		_ = file.Close()
		return nil, ErrBusy
	}
	return &File{file: file}, nil
}

// WaitFile waits for the lock on path until the context ends (its cause is the error), looking
// again after pauses that grow from 50 ms to 1 s.
func WaitFile(ctx context.Context, path string) (*File, error) {
	pause := 50 * time.Millisecond
	span := &waitSpan{path: path, began: time.Now()}
	for {
		held, err := TryFile(path)
		if !errors.Is(err, ErrBusy) {
			span.end(err, "")
			return held, err
		}
		span.waiting()
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			span.end(context.Cause(ctx), "")
			return nil, context.Cause(ctx)
		case <-timer.C:
		}
		pause = min(pause*3/2, time.Second)
	}
}

// Release gives the lock back; a nil or released lock is nothing to give back. It may be called
// more than once, from any goroutine.
func (held *File) Release() {
	if held == nil {
		return
	}
	held.mutex.Lock()
	defer held.mutex.Unlock()
	if held.file != nil {
		_ = held.file.Close()
		held.file = nil
	}
}
