package reviewpool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

func TestTerminalEventForLateMCPSubscriberOnly(t *testing.T) {
	pool, request, _ := fixture(t)
	job, created, err := pool.Enqueue(context.Background(), request)
	if err != nil || !created {
		t.Fatalf("CLI enqueue: %+v %v %v", job, created, err)
	}
	_, found, err := pool.RunNext(context.Background(), func(context.Context, Job) (int, error) { return review.ExitClear, nil })
	if err != nil || !found {
		t.Fatalf("run: %v %v", found, err)
	}
	saved, err := pool.load()
	if err != nil || len(saved.Events) != 0 {
		t.Fatalf("unsubscribed CLI job created outbox events: %+v %v", saved.Events, err)
	}
	sessionID := strings.Repeat("a", 64)
	if _, _, err := pool.EnqueueForSession(context.Background(), request, sessionID, filepath.Join(job.CommonDir, "other")); err == nil {
		t.Fatal("MCP submission crossed the configured project repository boundary")
	}
	duplicate, created, err := pool.EnqueueForSession(context.Background(), request, sessionID, job.CommonDir)
	if err != nil || created || duplicate.ID != job.ID {
		t.Fatalf("late subscription: %+v %v %v", duplicate, created, err)
	}
	if _, err := pool.StatusForSession(context.Background(), job.ID, strings.Repeat("b", 64), job.CommonDir); err == nil {
		t.Fatal("different session read subscribed job")
	}
	events, err := pool.Undelivered(context.Background(), sessionID, job.CommonDir)
	if err != nil || len(events) != 1 || events[0].Status != "complete" {
		t.Fatalf("late terminal event: %+v %v", events, err)
	}
	if err := pool.MarkDelivered(context.Background(), events[0], sessionID); err != nil {
		t.Fatal(err)
	}
	events, err = pool.Undelivered(context.Background(), sessionID, job.CommonDir)
	if err != nil || len(events) != 0 {
		t.Fatalf("delivered event repeated: %+v %v", events, err)
	}
}

func fixture(t *testing.T) (Pool, Request, string) {
	t.Helper()
	repo := testrepo.New(t, map[string]string{"jackstay.json": `{"jackstay_schema":2,"project":"pool-test"}`, "a.txt": "a\n"})
	base := repo.Git("rev-parse", "HEAD")
	repo.Commit("second", map[string]string{"a.txt": "b\n"})
	return Pool{Root: filepath.Join(repo.Home, "pool")}, Request{Repository: repo.Root, Base: "HEAD^", Head: "HEAD", Task: "pool-task", Reviewers: []string{"codex", "claude-code"}}, base
}

func TestRecoveredCompletedRoundIsNotRunAgain(t *testing.T) {
	pool, request, _ := fixture(t)
	job, _, err := pool.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	const token = "00112233445566778899aabbccddeeff"
	if err := pool.edit(context.Background(), func(saved *state) error {
		saved.Jobs[0].Status = "running"
		saved.Jobs[0].Attempts = 1
		saved.Jobs[0].AttemptID = token
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store := review.Store{Root: job.StoreRoot}
	folder := filepath.Join(store.TaskPath(job.Task), "round-1")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	exit := review.ExitBlocks
	record := review.RoundRecord{Task: job.Task, PoolAttempt: token, Round: 1, Base: job.Base,
		Commit: job.Head, PoolExit: &exit,
		Runs: []review.ReviewerRun{{Reviewer: "claude-code"}, {Reviewer: "codex"}}}
	content, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, review.RecordName), content, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, broken := range []review.RoundRecord{
		func() review.RoundRecord { changed := record; changed.Task = "other"; return changed }(),
		func() review.RoundRecord { changed := record; changed.PoolExit = nil; return changed }(),
	} {
		content, err := json.Marshal(broken)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, review.RecordName), content, 0o600); err != nil {
			t.Fatal(err)
		}
		claimed := job
		claimed.AttemptID = token
		if _, _, err := recordedRound(context.Background(), claimed); err == nil {
			t.Fatal("accepted incomplete or mismatched evidence")
		}
	}
	_, found, err := pool.RunNext(context.Background(), func(context.Context, Job) (int, error) {
		t.Fatal("unresolved round was replayed")
		return 0, nil
	})
	if err != nil || found {
		t.Fatalf("unresolved evidence: found=%v, err=%v", found, err)
	}
	unresolved, err := pool.List(context.Background())
	if err != nil || unresolved[0].Status != "running" || unresolved[0].AttemptID != token || unresolved[0].FailureClass != "unresolved" {
		t.Fatalf("lost correlation after unreadable record: %+v, %v", unresolved, err)
	}
	if err := os.WriteFile(filepath.Join(folder, review.RecordName), content, 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err = pool.RunNext(context.Background(), func(context.Context, Job) (int, error) {
		t.Fatal("completed round was repeated")
		return 0, nil
	})
	if err != nil || found {
		t.Fatalf("next after recovery: found=%v, err=%v", found, err)
	}
	listed, err := pool.List(context.Background())
	if err != nil || listed[0].Status != "complete" || listed[0].Exit != review.ExitBlocks || listed[0].Attempts != 1 {
		t.Fatalf("reconciled record: %+v, %v", listed, err)
	}
}

func TestCanceledWorkerUsesDurableCompletedRound(t *testing.T) {
	pool, request, _ := fixture(t)
	job, _, err := pool.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished, found, err := pool.RunNext(ctx, func(_ context.Context, running Job) (int, error) {
		exit := review.ExitBlocks
		record := review.RoundRecord{Task: running.Task, PoolAttempt: running.AttemptID, Round: 1,
			Base: running.Base, Commit: running.Head, PoolExit: &exit,
			Runs: []review.ReviewerRun{{Reviewer: "claude-code"}, {Reviewer: "codex"}}}
		folder := filepath.Join(review.Store{Root: running.StoreRoot}.TaskPath(running.Task), "round-1")
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}
		content, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, review.RecordName), content, 0o600); err != nil {
			t.Fatal(err)
		}
		cancel()
		return review.ExitReviewer, context.Canceled
	})
	if err != nil || !found || finished.ID != job.ID || finished.Status != "complete" || finished.Exit != review.ExitBlocks {
		t.Fatalf("completed review lost after cancellation: %+v, found=%v, err=%v", finished, found, err)
	}
	_, found, err = pool.RunNext(context.Background(), func(context.Context, Job) (int, error) {
		t.Fatal("completed round was replayed")
		return 0, nil
	})
	if err != nil || found {
		t.Fatalf("completed review remained runnable: found=%v, err=%v", found, err)
	}
}

func TestCanceledWorkerWithoutRoundRetainsAttemptForRecovery(t *testing.T) {
	pool, request, _ := fixture(t)
	request.Reviewers = []string{"codex"}
	_, _, err := pool.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupted, found, err := pool.RunNext(ctx, func(context.Context, Job) (int, error) {
		cancel()
		return review.ExitReviewer, context.Canceled
	})
	if !errors.Is(err, context.Canceled) || !found || interrupted.Status != "running" || interrupted.AttemptID == "" {
		t.Fatalf("interrupted attempt was not recoverable: %+v, found=%v, err=%v", interrupted, found, err)
	}
	finished, found, err := pool.RunNext(context.Background(), func(_ context.Context, running Job) (int, error) {
		if running.ID != interrupted.ID || running.Attempts != 2 {
			t.Fatalf("interrupted attempt not retried: %+v", running)
		}
		return review.ExitClear, nil
	})
	if err != nil || !found || finished.Status != "complete" {
		t.Fatalf("retry result: %+v, found=%v, err=%v", finished, found, err)
	}
}

func TestRetryBackoffIsDurableAndBounded(t *testing.T) {
	pool, request, _ := fixture(t)
	request.Reviewers = []string{"codex"}
	job, _, err := pool.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	failed, _, err := pool.RunNext(context.Background(), func(context.Context, Job) (int, error) { return review.ExitReviewer, nil })
	if err != nil || failed.FailureClass != "retryable" {
		t.Fatalf("classification: %+v, %v", failed, err)
	}
	if err := pool.Retry(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := pool.List(context.Background())
	if err != nil || listed[0].NotBefore.Before(time.Now().Add(25*time.Second)) {
		t.Fatalf("retry deadline: %+v, %v", listed, err)
	}
	_, found, err := pool.RunNext(context.Background(), func(context.Context, Job) (int, error) { t.Fatal("ran before backoff"); return 0, nil })
	if err != nil || found {
		t.Fatalf("delayed job: found=%v, err=%v", found, err)
	}
	if err := pool.edit(context.Background(), func(saved *state) error {
		saved.Jobs[0].NotBefore = time.Now().Add(-time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	finished, found, err := pool.RunNext(context.Background(), func(context.Context, Job) (int, error) { return review.ExitClear, nil })
	if err != nil || !found || finished.Status != "complete" || finished.Attempts != 2 {
		t.Fatalf("retry: %+v, %v", finished, err)
	}
}

func TestQueueAndAttemptLimits(t *testing.T) {
	pool, request, _ := fixture(t)
	job, _, err := pool.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.edit(context.Background(), func(saved *state) error {
		saved.Jobs[0].MaxAttempts = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, _, err := pool.RunNext(context.Background(), func(context.Context, Job) (int, error) { return review.ExitUsage, nil })
	if err != nil || result.FailureClass != "operator" {
		t.Fatalf("operator failure: %+v, %v", result, err)
	}
	if err := pool.Retry(context.Background(), job.ID); err == nil {
		t.Fatal("attempt limit was ignored")
	}
	request.Task = "another"
	if err := pool.edit(context.Background(), func(saved *state) error {
		for index := 0; index < maximumQueued; index++ {
			copyJob := job
			copyJob.ID = string(rune(index + 1000))
			copyJob.Status = "pending"
			saved.Jobs = append(saved.Jobs, copyJob)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pool.Enqueue(context.Background(), request); err == nil {
		t.Fatal("full queue accepted another job")
	}
}

func TestRetryCannotBypassPendingLimit(t *testing.T) {
	pool, request, _ := fixture(t)
	job, _, err := pool.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pool.RunNext(context.Background(), func(context.Context, Job) (int, error) {
		return review.ExitUsage, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := pool.edit(context.Background(), func(saved *state) error {
		saved.Jobs[0].MaxPending = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	request.Task = "other"
	if _, _, err := pool.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := pool.Retry(context.Background(), job.ID); err == nil {
		t.Fatal("retry bypassed pending limit")
	}
}

func TestEnqueuePinsRevisionsAndDeduplicatesReviewerOrder(t *testing.T) {
	pool, request, base := fixture(t)
	first, created, err := pool.Enqueue(context.Background(), request)
	if err != nil || !created || first.Base != base || first.Head == "HEAD" {
		t.Fatalf("first enqueue: %+v, created=%v, err=%v", first, created, err)
	}
	request.Reviewers = []string{"claude-code", "codex"}
	second, created, err := pool.Enqueue(context.Background(), request)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("duplicate enqueue: %+v, created=%v, err=%v", second, created, err)
	}
	if !slices.Equal(second.Reviewers, []string{"claude-code", "codex"}) {
		t.Fatalf("unstable reviewer set: %v", second.Reviewers)
	}
	info, err := os.Stat(pool.statePath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state permission: %v, %v", info, err)
	}
	request.Reviewers = []string{"codex"}
	third, created, err := pool.Enqueue(context.Background(), request)
	if err != nil || !created || third.ID == first.ID {
		t.Fatalf("different reviewer choice: %+v, created=%v, err=%v", third, created, err)
	}
}

func TestWorkerRecordsVerdictAndExplicitRetry(t *testing.T) {
	pool, request, _ := fixture(t)
	request.Reviewers = []string{"claude-code"}
	queued, _, err := pool.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	finished, found, err := pool.RunNext(context.Background(), func(_ context.Context, job Job) (int, error) {
		called++
		if job.ID != queued.ID || job.Status != "running" || job.Attempts != 1 {
			t.Fatalf("claimed %+v", job)
		}
		return review.ExitBlocks, nil
	})
	if err != nil || !found || finished.Status != "complete" || finished.Exit != review.ExitBlocks || called != 1 {
		t.Fatalf("verdict: %+v, found=%v, called=%d, err=%v", finished, found, called, err)
	}
	if err := pool.Retry(context.Background(), queued.ID); err == nil {
		t.Fatal("completed verdict was requeued")
	}
	_, found, err = pool.RunNext(context.Background(), func(context.Context, Job) (int, error) { t.Fatal("ran completed job"); return 0, nil })
	if err != nil || found {
		t.Fatalf("no work: found=%v, err=%v", found, err)
	}

	request.Task = "retry-task"
	failed, _, err := pool.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	result, found, err := pool.RunNext(context.Background(), func(context.Context, Job) (int, error) { return review.ExitUsage, nil })
	if err != nil || !found || result.Status != "failed" || result.ID != failed.ID {
		t.Fatalf("refusal: %+v, %v", result, err)
	}
	if err := pool.Retry(context.Background(), failed.ID); err != nil {
		t.Fatal(err)
	}
	result, found, err = pool.RunNext(context.Background(), func(_ context.Context, job Job) (int, error) {
		if job.Attempts != 2 {
			t.Fatalf("retry attempt: %d", job.Attempts)
		}
		return review.ExitClear, nil
	})
	if err != nil || !found || result.Status != "complete" {
		t.Fatalf("retry: %+v, %v", result, err)
	}
}

func TestWorkerLockAndInterruptedJobRecovery(t *testing.T) {
	pool, request, _ := fixture(t)
	request.Reviewers = []string{"codex"}
	first, _, err := pool.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Task = "later"
	_, _, err = pool.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := flock.TryFile(pool.workerPath())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = pool.RunNext(context.Background(), func(context.Context, Job) (int, error) { t.Fatal("overlapping worker"); return 0, nil })
	if !errors.Is(err, flock.ErrBusy) {
		t.Fatalf("worker lock: %v", err)
	}
	locked.Release()
	if err := pool.edit(context.Background(), func(saved *state) error {
		saved.Jobs[1].Status = "running"
		saved.Jobs[1].Attempts = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, _, err := pool.RunNext(context.Background(), func(_ context.Context, job Job) (int, error) {
		if job.ID == first.ID || job.Attempts != 2 {
			t.Fatalf("did not recover running job first: %+v", job)
		}
		return review.ExitReviewer, nil
	})
	if err != nil || result.Status != "failed" {
		t.Fatalf("recovered result: %+v, %v", result, err)
	}
}

func TestChangedRepositoryIdentityRefusesExecution(t *testing.T) {
	pool, request, _ := fixture(t)
	request.Reviewers = []string{"codex"}
	job, _, err := pool.Enqueue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.edit(context.Background(), func(saved *state) error { saved.Jobs[0].RootInode++; return nil }); err != nil {
		t.Fatal(err)
	}
	result, _, err := pool.RunNext(context.Background(), func(context.Context, Job) (int, error) { t.Fatal("ran changed repository"); return 0, nil })
	if err == nil || result.ID != job.ID || result.Status != "failed" {
		t.Fatalf("identity refusal: %+v, %v", result, err)
	}
}

func TestListDoesNotCreateStateAndRefusesSpecialFiles(t *testing.T) {
	pool := Pool{Root: filepath.Join(t.TempDir(), "absent")}
	jobs, err := pool.List(context.Background())
	if err != nil || len(jobs) != 0 {
		t.Fatalf("empty list: %v, %v", jobs, err)
	}
	if _, err := os.Stat(pool.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only list created %s: %v", pool.Root, err)
	}
	if err := os.MkdirAll(pool.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(pool.statePath(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.List(context.Background()); err == nil {
		t.Fatal("FIFO state was accepted")
	}
}
