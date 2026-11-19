package review

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
)

func TestTasksHavePortableCaseSensitiveNames(t *testing.T) {
	store := Store{Root: t.TempDir()}
	long := strings.Repeat("測", 66)
	names := []string{"feature/Review", "feature/review", long}
	for _, name := range names {
		if err := store.prepareTask(name); err != nil {
			t.Fatal(err)
		}
		folder := filepath.Base(store.TaskPath(name))
		if len(folder) != 64 || strings.ToLower(folder) != folder {
			t.Fatalf("task folder is not a lower-case SHA-256 component: %q", folder)
		}
		tree := filepath.Base(treePath(store, name, 1, "codex"))
		if len(tree) > 255 {
			t.Fatalf("pinned tree name exceeds the component limit: %d bytes", len(tree))
		}
	}
	if strings.EqualFold(store.TaskPath(names[0]), store.TaskPath(names[1])) ||
		strings.EqualFold(store.TaskLockPath(names[0]), store.TaskLockPath(names[1])) {
		t.Fatal("differently cased tasks share a folder or lock")
	}
	got, err := store.Tasks()
	if err != nil || strings.Join(got, ",") != strings.Join(names, ",") {
		t.Fatalf("tasks %q: %v", got, err)
	}
}

func TestLegacyTasksAreReadWithTheirExactSpelling(t *testing.T) {
	store := Store{Root: t.TempDir()}
	task := "feature/Review"
	legacy := filepath.Join(store.Root, "feature%2FReview")
	if err := writeJSON(filepath.Join(legacy, "round-1", RecordName), RoundRecord{Task: task, Round: 1, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadRound(task, 1); err != nil {
		t.Fatal(err)
	}
	old, err := flock.TryFile(filepath.Join(store.Root, ".locks", "feature%2FReview.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Release()
	if release, err := store.tryTaskLock(task); !errors.Is(err, flock.ErrBusy) {
		if release != nil {
			release()
		}
		t.Fatalf("old and new clients could both open the task: %v", err)
	}
	if store.TaskPath(task) != legacy || strings.EqualFold(store.TaskPath("feature/review"), legacy) {
		t.Fatal("the legacy folder was not resolved by its exact spelling")
	}
	got, err := store.Tasks()
	if err != nil || len(got) != 1 || got[0] != task {
		t.Fatalf("tasks %q: %v", got, err)
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
		if err := store.prepareTask(task); err != nil {
			t.Fatal(err)
		}
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

func TestPruneDoesNotDeleteTaskBeforeItCanLockTheIndex(t *testing.T) {
	store := Store{Root: t.TempDir()}
	task := "old"
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	if err := store.prepareTask(task); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(store.TaskPath(task), "round-1")
	if err := writeJSON(filepath.Join(folder, RecordName), RoundRecord{Task: task, Round: 1, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	older := now.AddDate(0, 0, -40)
	if err := os.Chtimes(folder, older, older); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.indexLockPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	removed, err := store.Prune(30, now)
	if err == nil || len(removed) != 0 {
		t.Fatalf("prune removed %q with inaccessible index: %v", removed, err)
	}
	if _, err := store.ReadRound(task, 1); err != nil {
		t.Fatalf("prune lost the task: %v", err)
	}
}
