package reviewpool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

func fixture(t *testing.T) (Pool, Request, string) {
	t.Helper()
	repo := testrepo.New(t, map[string]string{"jackstay.json": `{"jackstay_schema":2,"project":"pool-test"}`, "a.txt": "a\n"})
	base := repo.Git("rev-parse", "HEAD")
	repo.Commit("second", map[string]string{"a.txt": "b\n"})
	return Pool{Root: filepath.Join(repo.Home, "pool")}, Request{Repository: repo.Root, Base: "HEAD^", Head: "HEAD", Task: "pool-task", Reviewers: []string{"codex", "claude-code"}}, base
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
