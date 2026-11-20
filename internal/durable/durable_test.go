package durable

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/zima-oss/jackstay/internal/flock"
)

func TestWriteAtomicReplacesTheFileWholeWithItsModeAndLeavesNoPartial(t *testing.T) {
	folder := t.TempDir()
	path := filepath.Join(folder, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(path, []byte("new"), 0o600, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("read %q %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info.Mode(), err)
	}
	entries, _ := os.ReadDir(folder)
	if len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestWriteAtomicRefusesDataPastItsLimitAndKeepsTheOldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(path, []byte("12345"), 0o600, 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "old" {
		t.Fatalf("changed: %q", data)
	}
	if err := WriteAtomic(filepath.Join(t.TempDir(), "missing", "x"), []byte("a"), 0o600, 0); err == nil {
		t.Fatal("wrote into a missing folder")
	}
}

func TestReadLimitedRefusesALinkAFolderAndAFileTooLarge(t *testing.T) {
	folder := t.TempDir()
	path := filepath.Join(folder, "state.json")
	if _, err := ReadLimited(path, 10); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadLimited(path, 5); err != nil || string(data) != "12345" {
		t.Fatalf("at the limit: %q %v", data, err)
	}
	if _, err := ReadLimited(path, 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("past the limit: %v", err)
	}
	link := filepath.Join(folder, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLimited(link, 10); err == nil {
		t.Fatal("followed a link")
	}
	if _, err := ReadLimited(folder, 10); err == nil {
		t.Fatal("read a folder")
	}
}

func TestUpdateSerializesWritersAndKeepsTheFileWhenToldTo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "count")
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			err := Update(context.Background(), path, 0o600, 64, func(current []byte) ([]byte, error) {
				count := 0
				if current != nil {
					count, _ = strconv.Atoi(string(current))
				}
				return []byte(strconv.Itoa(count + 1)), nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if data, _ := os.ReadFile(path); string(data) != "8" {
		t.Fatalf("count %q", data)
	}
	err := Update(context.Background(), path, 0o600, 64, func(current []byte) ([]byte, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "8" {
		t.Fatalf("kept %q", data)
	}
	refused := errors.New("refused")
	if err := Update(context.Background(), path, 0o600, 64, func([]byte) ([]byte, error) { return []byte("x"), refused }); !errors.Is(err, refused) {
		t.Fatalf("err %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "8" {
		t.Fatalf("written despite an error: %q", data)
	}
}

func TestUpdateGivesUpWhenTheLockIsHeldPastTheContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	held, err := flock.TryFile(LockPath(path))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err = Update(ctx, path, 0o600, 0, func([]byte) ([]byte, error) { called = true; return nil, nil })
	if err == nil || called {
		t.Fatalf("err %v, called %v", err, called)
	}
}

func TestUpdateUnderTakesTheNamedLockAndMakeFolderMakesTheChain(t *testing.T) {
	folder := t.TempDir()
	path := filepath.Join(folder, "a", "b", "state.json")
	lockPath := filepath.Join(folder, "named.lock")
	held, err := flock.TryFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := UpdateUnder(ctx, lockPath, path, 0o600, 0, func([]byte) ([]byte, error) { return []byte("x"), nil }); err == nil {
		t.Fatal("wrote under another's lock")
	}
	held.Release()
	if err := UpdateUnder(context.Background(), lockPath, path, 0o600, 0, func([]byte) ([]byte, error) { return []byte("x"), nil }); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "x" {
		t.Fatalf("read %q", data)
	}
	if _, err := os.Stat(LockPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a lock beside the file was made: %v", err)
	}
	if err := MakeFolder(filepath.Join(folder, "a", "b"), 0o700); err != nil {
		t.Fatal("an existing folder:", err)
	}
}

func TestMakeFolderFailsWhileAParentWillNotSyncAndARetrySyncsIt(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "store", "inner")
	failing := filepath.Join(root, "store")
	var synced []string
	syncFolder = func(path string) error {
		synced = append(synced, path)
		if path == failing {
			return &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
		}
		return SyncFolder(path)
	}
	t.Cleanup(func() { syncFolder = SyncFolder })
	for attempt := range 2 { // the second finds the folders made, and must not take them for synced
		if err := MakeFolder(folder, 0o700); err == nil {
			t.Fatalf("attempt %d: a parent did not sync, and MakeFolder said nothing", attempt)
		}
	}
	syncFolder = func(path string) error { synced = append(synced, path); return SyncFolder(path) }
	synced = nil
	if err := MakeFolder(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if len(synced) < 2 || synced[0] != failing || synced[1] != root {
		t.Fatalf("the retry synced %v; want %s and %s first", synced, failing, root)
	}
}
