// Package durable keeps jackstay's small state files safe across a crash and across processes: a
// write lands whole or not at all (a temporary file beside the target, fsynced, renamed over it,
// and the folder fsynced so the rename survives), and a read-modify-write runs under a plain file
// lock (internal/flock) that is waited for with a bound, never forever.
package durable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/flock"
)

// LockTimeout bounds the wait for a state file's lock when the caller's context has no earlier
// deadline: a holder that keeps it longer is stuck, and the caller is told so.
const LockTimeout = 30 * time.Second

// ErrTooLarge is data, or a file, past the limit the caller gave.
var ErrTooLarge = errors.New("larger than its limit")

// ErrNotSynced is a write that replaced the file, but whose folder could not be fsynced: the new
// content is what a reader sees now, though a crash may still bring the old one back.
var ErrNotSynced = errors.New("written, but its folder was not synced")

// LockPath is the lock file that guards path: a file beside it, path + ".lock".
func LockPath(path string) string { return path + ".lock" }

// WriteAtomic replaces the file at path with data, whole or not at all: a temporary file in the
// same folder with the given mode, fsynced, renamed over path, then the folder fsynced. A limit
// above zero refuses data longer than limit bytes (ErrTooLarge). The folder must exist (MakeFolder).
// An error that matches ErrNotSynced comes after the rename: the file holds data.
func WriteAtomic(path string, data []byte, mode os.FileMode, limit int) error {
	if limit > 0 && len(data) > limit {
		return fmt.Errorf("%s: %d bytes: %w (%d)", path, len(data), ErrTooLarge, limit)
	}
	folder := filepath.Dir(path)
	temporary, err := os.CreateTemp(folder, "."+filepath.Base(path)+".*.partial")
	if err != nil {
		return err
	}
	name := temporary.Name()
	renamed := false
	defer func() {
		_ = temporary.Close()
		if !renamed {
			_ = os.Remove(name)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	renamed = true
	if err := SyncFolder(folder); err != nil {
		return fmt.Errorf("%s: %w: %w", path, ErrNotSynced, err)
	}
	return nil
}

// MakeFolder makes folder and its missing parents with mode, like os.MkdirAll, then fsyncs every
// folder above it, so folder survives a crash with what is written in it. Whether this call made a
// folder does not matter: another process, or an earlier call whose sync failed, may have made it,
// and its being there does not say its entry is on disk. A folder that will not sync fails the call
// when this process may write in it (it may hold a new entry); the walk ends quietly at the first
// folder this process may not write in, where nothing of its own can be pending.
func MakeFolder(folder string, mode os.FileMode) error {
	absolute, err := filepath.Abs(folder)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absolute, mode); err != nil {
		return err
	}
	for current, parent := absolute, filepath.Dir(absolute); parent != current; current, parent = parent, filepath.Dir(parent) {
		if unix.Access(parent, unix.W_OK) != nil {
			return nil
		}
		if err := syncFolder(parent); err != nil {
			return err
		}
	}
	return nil
}

// syncFolder is SyncFolder; a test makes it fail.
var syncFolder = SyncFolder

// SyncFolder fsyncs a folder, so a rename or a new name in it survives a crash.
func SyncFolder(folder string) error {
	directory, err := os.Open(folder) // #nosec G304 -- the caller's own state folder
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// ReadLimited reads the regular file at path, refusing one longer than limit bytes (ErrTooLarge)
// when limit is above zero. It does not follow a symbolic link at path and does not block on a
// pipe. A missing file is an error that matches os.ErrNotExist.
func ReadLimited(path string, limit int) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0) // #nosec G304 -- the caller's own state file
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	reader := io.Reader(file)
	if limit > 0 {
		reader = io.LimitReader(file, int64(limit)+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(data) > limit {
		return nil, fmt.Errorf("%s: %w (%d bytes)", path, ErrTooLarge, limit)
	}
	return data, nil
}

// Lock takes the lock that guards path (LockPath); see LockFile.
func Lock(ctx context.Context, path string) (*flock.File, error) {
	return LockFile(ctx, LockPath(path))
}

// LockFile takes the lock file at lockPath, making its folder when missing, and waits for it until
// ctx ends or LockTimeout passes. The lock taken is the file at the lock's path, not one removed
// meanwhile (flock.TryFile). The caller releases it.
func LockFile(ctx context.Context, lockPath string) (*flock.File, error) {
	bounded, cancel := context.WithTimeoutCause(ctx, LockTimeout,
		fmt.Errorf("%s: still held by another process after %s", lockPath, LockTimeout))
	defer cancel()
	return flock.WaitFile(bounded, lockPath)
}

// Update is a locked, bounded read-modify-write of the state file at path, under its own lock
// (LockPath); see UpdateUnder.
func Update(ctx context.Context, path string, mode os.FileMode, limit int, change func(current []byte) ([]byte, error)) error {
	return UpdateUnder(ctx, LockPath(path), path, mode, limit, change)
}

// UpdateUnder is a locked, bounded read-modify-write of the state file at path. Under the lock file
// at lockPath (LockFile) it reads the file (ReadLimited; nil when missing) and gives it to change;
// what change returns is written whole (WriteAtomic, with mode and limit), and nil leaves the file
// as it is. The file's folder is made (MakeFolder, 0700) when missing.
func UpdateUnder(ctx context.Context, lockPath, path string, mode os.FileMode, limit int, change func(current []byte) ([]byte, error)) error {
	if err := MakeFolder(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	held, err := LockFile(ctx, lockPath)
	if err != nil {
		return err
	}
	defer held.Release()
	current, err := ReadLimited(path, limit)
	if errors.Is(err, os.ErrNotExist) {
		current, err = nil, nil
	}
	if err != nil {
		return err
	}
	next, err := change(current)
	if err != nil || next == nil {
		return err
	}
	return WriteAtomic(path, next, mode, limit)
}
