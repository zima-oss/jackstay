package mergequeue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotDoesNotCreateOrChangeQueueFiles(t *testing.T) {
	service, repository := fixture(t)
	ctx := context.Background()
	place, _, err := service.place(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Snapshot(ctx); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent queue: got %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(filepath.Dir(place)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot created a queue directory: %v", err)
	}

	state := testedHandoff(t, service)
	before, err := os.ReadFile(place)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(place + ".lock"); err != nil {
		t.Fatal(err)
	}
	seen, err := service.Snapshot(ctx)
	if err != nil || seen.Stale || seen.Candidate != state.Candidate {
		t.Fatalf("snapshot changed current state: %+v, %v", seen, err)
	}
	if _, err := os.Stat(place + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot created a lock: %v", err)
	}
	gitCommand(t, repository, "switch", "main")
	if err := os.WriteFile(filepath.Join(repository, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "add", "later.txt")
	gitCommand(t, repository, "commit", "-m", "move base")
	seen, err = service.Snapshot(ctx)
	if err != nil || !seen.Stale {
		t.Fatalf("snapshot missed a moved ref: %+v, %v", seen, err)
	}
	after, err := os.ReadFile(place)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("snapshot changed persisted queue state")
	}
	if _, err := os.Stat(place + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot created a lock after a ref moved: %v", err)
	}
}

func TestStatusShowsStaleHandoff(t *testing.T) {
	state := &State{Candidate: "candidate", GateRunID: "gate", HandoffAt: time.Now(), Stale: true}
	if got := queueStatus(state); got != "stale" {
		t.Fatalf("stale handoff shown as %q", got)
	}
	state.Stale = false
	if got := queueStatus(state); got != "handed-off" {
		t.Fatalf("fresh handoff shown as %q", got)
	}
}
