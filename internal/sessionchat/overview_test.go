package sessionchat

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
)

func TestReadOverviewDoesNotCreateOrPruneState(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	common := filepath.Join(t.TempDir(), ".git")
	if err := os.Mkdir(common, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := StoreFor(common, config.ChatPolicy{Enabled: true, MaxMessageBytes: 4096,
		MaxPending: 10, MaxSessions: 5, RetentionHours: 72})
	if err != nil {
		t.Fatal(err)
	}
	if summary, err := store.ReadOverview(); err != nil || summary != (Overview{}) {
		t.Fatalf("empty overview: %+v, %v", summary, err)
	}
	if _, err := os.Stat(filepath.Dir(store.path)); !os.IsNotExist(err) {
		t.Fatalf("read created chat directory: %v", err)
	}
	first, err := store.Register("first", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Register("second", "")
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := store.Send(first.ID, second.ID, "message", "private body", "key", "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return before.ModTime().Add(time.Minute) }
	if summary, err := store.ReadOverview(); err != nil || summary.Sessions != 2 || summary.Pending != 1 {
		t.Fatalf("pending overview: %+v, %v", summary, err)
	}
	after, err := os.Stat(store.path)
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("overview rewrote chat state: %v", err)
	}
	if err := store.Acknowledge(second.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	if summary, err := store.ReadOverview(); err != nil || summary.Pending != 0 {
		t.Fatalf("acknowledged overview: %+v, %v", summary, err)
	}
}

func TestReadOverviewRejectsFIFOWithoutOpeningWriter(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	common := filepath.Join(t.TempDir(), ".git")
	if err := os.Mkdir(common, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := StoreFor(common, config.ChatPolicy{Enabled: true, RetentionHours: 72})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(store.path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadOverview(); err == nil {
		t.Fatal("FIFO was accepted as chat state")
	}
}
