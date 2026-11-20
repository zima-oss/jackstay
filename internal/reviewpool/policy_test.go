package reviewpool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/review"
)

func TestPolicyChangeFailsClosedAndCreatesNewRequest(t *testing.T) {
	ctx := context.Background()
	pool, request, _ := fixture(t)
	request.Reviewers = []string{"codex"}
	first, created, err := pool.Enqueue(ctx, request)
	if err != nil || !created || first.PolicyID == "" {
		t.Fatalf("initial request: %+v, created %t, %v", first, created, err)
	}
	configPath := filepath.Join(request.Repository, "jackstay.json")
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	// Changing a selected reviewer's model changes the result of the requested
	// review even though its base, head, and task remain pinned.
	config := map[string]any{
		"jackstay_schema": 3, "project": "pool-test",
		"review": map[string]any{"reviewers": map[string]any{"codex": map[string]any{"model": "new-model"}}},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	callbackCalled := false
	stale, found, err := pool.RunNext(ctx, func(context.Context, Job) (int, error) {
		callbackCalled = true
		return 0, nil
	})
	if !found || err == nil || !strings.Contains(err.Error(), "review policy changed") || callbackCalled ||
		stale.ID != first.ID || stale.Status != "failed" || stale.FailureClass != "stale-policy" || stale.Exit != review.ExitUsage {
		t.Fatalf("stale request ran or hid status: %+v, found %t, callback %t, err %v", stale, found, callbackCalled, err)
	}
	if err := pool.Retry(ctx, first.ID); err == nil || !strings.Contains(err.Error(), "submit a new request") {
		t.Fatalf("stale request was retried under a new policy: %v", err)
	}
	second, created, err := pool.Enqueue(ctx, request)
	if err != nil || !created || second.ID == first.ID || second.PolicyID == first.PolicyID {
		t.Fatalf("changed policy reused request: %+v, created %t, %v", second, created, err)
	}
	_, found, err = pool.RunNext(ctx, func(context.Context, Job) (int, error) {
		callbackCalled = true
		return review.ExitClear, nil
	})
	if err != nil || !found || !callbackCalled {
		t.Fatalf("new request could not run: found %t, callback %t, %v", found, callbackCalled, err)
	}
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pool.Retry(ctx, first.ID); err != nil {
		t.Fatalf("retry under restored policy: %v", err)
	}
	restored, found, err := pool.RunNext(ctx, func(_ context.Context, job Job) (int, error) {
		if job.ID != first.ID || job.PolicyID != first.PolicyID {
			t.Fatalf("retry changed the pin: %+v", job)
		}
		return review.ExitClear, nil
	})
	if err != nil || !found || restored.Status != "complete" || restored.FailureClass != "" {
		t.Fatalf("restored request: %+v, found %t, %v", restored, found, err)
	}
}

func TestLegacyUnpinnedRequestNeedsResubmission(t *testing.T) {
	ctx := context.Background()
	pool, request, _ := fixture(t)
	_, _, err := pool.Enqueue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.edit(ctx, func(saved *state) error {
		saved.Jobs[0].PolicyID = ""
		saved.Jobs[0].ID = jobID(saved.Jobs[0])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stale, found, err := pool.RunNext(ctx, func(context.Context, Job) (int, error) {
		t.Fatal("legacy unpinned request started a reviewer")
		return 0, nil
	})
	if !found || err == nil || stale.FailureClass != "stale-policy" {
		t.Fatalf("legacy request: %+v, found %t, %v", stale, found, err)
	}
	newJob, created, err := pool.Enqueue(ctx, request)
	if err != nil || !created || newJob.ID == stale.ID {
		t.Fatalf("resubmission: %+v, created %t, %v", newJob, created, err)
	}
}
