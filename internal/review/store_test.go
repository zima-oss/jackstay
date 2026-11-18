package review

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
)

func TestTaskFolder(t *testing.T) {
	for _, name := range []string{"feature/review", "main", ".hidden", "a b%c", "claude/stage-1.x_y"} {
		folder := TaskFolder(name)
		back, ok := TaskName(folder)
		if !ok || back != name {
			t.Errorf("%q -> %q -> %q", name, folder, back)
		}
		if filepath.Base(folder) != folder || folder[0] == '.' {
			t.Errorf("%q is not one plain folder name", folder)
		}
	}
	if TaskFolder("feature/review") != "feature%2Freview" {
		t.Fatal(TaskFolder("feature/review"))
	}
}

func TestEntriesAndNumbers(t *testing.T) {
	store := Store{Root: t.TempDir()}
	task := "t/1"
	mk := func(name string, record *RoundRecord) {
		dir := filepath.Join(store.TaskPath(task), name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if record != nil {
			if err := writeJSON(filepath.Join(dir, RecordName), record); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("round-1", &RoundRecord{Task: task, Round: 1, Attempt: 1})
	mk("attempt-2", nil)
	mk("round-2", &RoundRecord{Task: task, Round: 2, Attempt: 3})
	mk("junk", nil)
	entries, err := store.Entries(task)
	if err != nil || len(entries) != 3 {
		t.Fatal(entries, err)
	}
	if entries[2].Round != 2 || NextAttempt(entries) != 4 || len(Rounds(entries)) != 2 {
		t.Fatalf("%+v", entries)
	}
	newest, ok, err := store.Newest(task)
	if err != nil || !ok || newest.Round != 2 {
		t.Fatal(newest, ok, err)
	}
}

func TestPruneAndIndex(t *testing.T) {
	store := Store{Root: t.TempDir()}
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	for task, age := range map[string]time.Duration{"old": 40 * 24 * time.Hour, "young": time.Hour, "busy": 40 * 24 * time.Hour} {
		dir := filepath.Join(store.TaskPath(task), "round-1")
		if err := writeJSON(filepath.Join(dir, RecordName), RoundRecord{Task: task, Round: 1, Attempt: 1,
			Runs: []ReviewerRun{{Reviewer: "codex", Outcome: "done"}}}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Rebuild(); err != nil {
		t.Fatal(err)
	}
	lines, skipped, _ := store.ReadIndex()
	if len(lines) != 3 || skipped != 0 {
		t.Fatal(lines, skipped)
	}
	busy, err := flock.TryFile(store.TaskLockPath("busy"))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Release()
	removed, err := store.Prune(30, now)
	if err != nil || len(removed) != 1 || removed[0] != "old" {
		t.Fatal(removed, err)
	}
	lines, _, _ = store.ReadIndex()
	if len(lines) != 2 {
		t.Fatalf("index after prune: %+v", lines)
	}
	if err := os.WriteFile(store.indexPath(), []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendIndex([]IndexLine{{Task: "young"}}); err != nil {
		t.Fatal(err)
	}
	lines, skipped, _ = store.ReadIndex()
	if len(lines) != 1 || skipped != 1 {
		t.Fatal(lines, skipped)
	}
}
