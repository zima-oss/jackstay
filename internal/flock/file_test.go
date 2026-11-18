package flock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAPlainFileLockIsHeldOnceAndWaitedFor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folder", "x.lock")
	first, err := TryFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryFile(path); !errors.Is(err, ErrBusy) {
		t.Fatalf("second: %v", err)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := WaitFile(waitContext, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waited past its time: %v", err)
	}
	first.Release()
	first.Release()
	second, err := WaitFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
	var none *File
	none.Release()
}

func TestAPlainFileLockOnARemovedFileIsNotTheLockAtThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	held, err := TryFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	fresh, err := TryFile(path)
	if err != nil {
		t.Fatalf("the file made again is a lock of its own: %v", err)
	}
	fresh.Release()
}
