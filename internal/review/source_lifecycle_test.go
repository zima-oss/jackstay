package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/review/runner"
	"github.com/zima-oss/jackstay/internal/review/sourceview"
)

func TestSourceViewSlotsAreSeparateFromReviewerSlots(t *testing.T) {
	h := newHarness(t)
	h.round.Settings.MaxConcurrent = 1
	first, err := h.round.takeSourceSlot(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	reviewerSlot, err := h.round.takeSlot(context.Background(), 1)
	if err != nil {
		t.Fatalf("source preparation held a reviewer slot: %v", err)
	}
	reviewerSlot.Release()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if second, err := h.round.takeSourceSlot(canceled, 1); !errors.Is(err, context.Canceled) {
		second.Release()
		t.Fatalf("another source view acquired the occupied permit: %v", err)
	}
	first.Release()
	if next, err := h.round.takeSourceSlot(context.Background(), 1); err != nil {
		t.Fatalf("source permit did not become available: %v", err)
	} else {
		next.Release()
	}
}

func TestNewAttemptReclaimsAbandonedSourcesUnderTheTaskLock(t *testing.T) {
	h := newHarness(t, Answer{Verdict: "approve", Summary: "clear"})
	task := "feature/x"
	folder := h.round.Store.TaskPath(task)
	stale := filepath.Join(folder, "attempt-1", "source")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "old.txt"), []byte("old"), 0o400); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedAttempt := filepath.Join(folder, "attempt-2")
	if err := os.Mkdir(linkedAttempt, 0o700); err != nil {
		t.Fatal(err)
	}
	for attempt, directory := range map[int]string{1: filepath.Dir(stale), 2: linkedAttempt} {
		if err := writeJSON(filepath.Join(directory, AttemptName), AttemptRecord{Task: task, Attempt: attempt}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(linkedAttempt, "source")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(folder, "attempt-4")); err != nil {
		t.Fatal(err)
	}

	lock, err := flock.TryFile(h.round.Store.TaskLockPath(task))
	if err != nil {
		t.Fatal(err)
	}
	if result := h.round.Run(context.Background(), Options{}); result.Status != ExitUsage {
		t.Fatalf("concurrent run should be refused: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(stale, "old.txt")); err != nil {
		t.Fatalf("busy run removed source owned by another task holder: %v", err)
	}
	lock.Release()

	result := h.round.Run(context.Background(), Options{})
	if result.Status != ExitClear || result.Record.Attempt != 3 {
		t.Fatalf("new attempt failed: %+v", result)
	}
	for _, path := range []string{stale, filepath.Join(linkedAttempt, "source")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("abandoned source remains at %s: %v", path, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(outside, "keep.txt")); err != nil || string(data) != "keep" {
		t.Fatalf("external symlink target was changed: %q, %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(folder, "attempt-4")); err != nil {
		t.Fatalf("an unrelated symlink entry was removed: %v", err)
	}
}

func TestSourceCleanupRejectsASymlinkedTaskDirectory(t *testing.T) {
	h := newHarness(t)
	task := "feature/x"
	outside := t.TempDir()
	attempt := filepath.Join(outside, "attempt-1")
	if err := writeJSON(filepath.Join(attempt, AttemptName), AttemptRecord{Task: task, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	view := filepath.Join(attempt, "source")
	if err := os.Mkdir(view, 0o700); err != nil {
		t.Fatal(err)
	}
	folder := h.round.Store.TaskPath(task)
	if err := os.MkdirAll(filepath.Dir(folder), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, folder); err != nil {
		t.Fatal(err)
	}
	if result := h.round.Run(context.Background(), Options{}); result.Status != ExitReviewer {
		t.Fatalf("symlinked task was accepted: %+v", result)
	}
	if _, err := os.Stat(view); err != nil {
		t.Fatalf("external attempt source was removed: %v", err)
	}
}

type failingSourceRepository struct {
	*fakeRepo
	builds int
}

func (repo *failingSourceRepository) BuildSource(context.Context, string, sourceview.Revisions) error {
	repo.builds++
	return errors.New("synthetic missing blob")
}

func TestSourcePreparationFailureAppearsInReviewLastEvidence(t *testing.T) {
	h := newHarness(t)
	broken := &failingSourceRepository{fakeRepo: h.repo}
	h.round.Repo = broken
	result := h.round.Run(context.Background(), Options{})
	if result.Status != ExitReviewer || broken.builds != 1 {
		t.Fatalf("source failure was not returned: %+v, builds %d", result, broken.builds)
	}
	report, err := os.ReadFile(filepath.Join(result.Folder, "report.md"))
	if err != nil || !strings.Contains(string(report), "synthetic missing blob") {
		t.Fatalf("review last has no diagnostic report: %q, %v", report, err)
	}
	runs := ReadRuns(result.Folder)
	if len(runs) != 1 || runs[0].Reviewer != "source-view" || runs[0].Outcome != runner.OutcomeFailed {
		t.Fatalf("source failure has no run record: %+v", runs)
	}
	if err := h.round.Store.Rebuild(); err != nil {
		t.Fatal(err)
	}
	lines, _, err := h.round.Store.ReadIndex()
	if err != nil || len(lines) != 1 || lines[0].Reviewer != "source-view" {
		t.Fatalf("source failure vanished from rebuilt history: %+v, %v", lines, err)
	}
}

func TestSourceSlotWaitDoesNotCreateAView(t *testing.T) {
	h := newHarness(t)
	h.round.Settings.MaxConcurrent = 1
	broken := &failingSourceRepository{fakeRepo: h.repo}
	h.round.Repo = broken
	held, err := h.round.takeSourceSlot(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result := h.round.Run(canceled, Options{})
	if result.Status != ExitReviewer || broken.builds != 0 || !strings.Contains(result.Next, "source view slot") {
		t.Fatalf("the occupied source permit was bypassed: %+v, builds %d", result, broken.builds)
	}
	if _, err := os.Stat(filepath.Join(result.Folder, "report.md")); err != nil {
		t.Fatalf("slot failure has no report: %v", err)
	}
}

func TestReviewerUsesTheSourceAdmissionQueueBudget(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	queue, cancelQueue := context.WithCancel(ctx)
	defer cancelQueue()
	view, err := h.round.takeSourceSlot(queue, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Release()
	occupied, err := h.round.takeSlot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Release()
	h.round.Settings.MaxConcurrent = 1
	h.round.Machine.RunReviewer = func(context.Context, runner.Request) runner.Result {
		t.Error("reviewer launched after the shared admission budget expired")
		return runner.Result{}
	}
	cancelQueue()
	run, _ := h.round.review(ctx, queue, h.round.Settings, plan{}, "codex", nil, t.TempDir(), "")
	if run.Outcome != OutcomeNoSlot {
		t.Fatalf("expired shared queue budget: %+v", run)
	}
}
