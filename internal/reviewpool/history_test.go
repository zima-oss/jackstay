package reviewpool

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/review"
)

func TestPoolStateMigrationPreservesLegacyWorkAndDelivery(t *testing.T) {
	for _, version := range []int{legacyStateVersion, previousStateVersion} {
		t.Run(strconv.Itoa(version), func(t *testing.T) { checkPoolStateMigration(t, version) })
	}
}

func checkPoolStateMigration(t *testing.T, version int) {
	t.Helper()
	ctx := context.Background()
	pool := Pool{Root: filepath.Join(t.TempDir(), "pool")}
	preview, err := pool.Migrate(ctx, false)
	if err != nil || preview != (Migration{From: currentStateVersion, To: currentStateVersion}) {
		t.Fatalf("empty pool migration: %+v, %v", preview, err)
	}
	if _, err := os.Stat(pool.Root); !os.IsNotExist(err) {
		t.Fatalf("preview touched empty pool: %v", err)
	}
	legacy := state{
		Version: version,
		Jobs: []Job{
			{ID: "running", Status: "running", Attempts: 2, AttemptID: "existing-round-token"},
			{ID: "terminal", Status: "complete", Attempts: 1, CommonDir: "/legacy/.git"},
		},
		Subscriptions: []subscription{{JobID: "terminal", SessionID: "session"}},
		Events:        []event{{Notification: Notification{JobID: "terminal", Attempt: 1, Status: "complete"}, Delivered: []string{"session"}}},
	}
	if err := pool.save(legacy); err != nil {
		t.Fatal(err)
	}
	read := func() []byte {
		t.Helper()
		data, err := os.ReadFile(pool.statePath())
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := read()
	preview, err = pool.Migrate(ctx, false)
	if err != nil || preview != (Migration{From: version, To: currentStateVersion, Needed: true}) || !bytes.Equal(before, read()) {
		t.Fatalf("legacy preview changed state: %+v, %v", preview, err)
	}
	applied, err := pool.Migrate(ctx, true)
	if err != nil || applied != (Migration{From: version, To: currentStateVersion, Needed: true, Applied: true}) {
		t.Fatalf("migration apply: %+v, %v", applied, err)
	}
	upgraded := read()
	if !bytes.Equal(upgraded, bytes.Replace(before, []byte(`"version": `+strconv.Itoa(version)), []byte(`"version": `+strconv.Itoa(currentStateVersion)), 1)) {
		t.Fatal("upgrade changed data beyond the version marker")
	}
	again, err := pool.Migrate(ctx, true)
	if err != nil || again != (Migration{From: currentStateVersion, To: currentStateVersion}) || !bytes.Equal(upgraded, read()) {
		t.Fatalf("second migration: %+v, %v", again, err)
	}
	pending, err := pool.Undelivered(ctx, "session", "/legacy/.git")
	if err != nil || len(pending) != 0 {
		t.Fatalf("migration reissued old delivery: %+v, %v", pending, err)
	}
	if _, err := pool.StatusForSession(ctx, "terminal", "session", "/legacy/.git"); err != nil {
		t.Fatalf("migration lost subscription: %v", err)
	}
}

func TestPoolMaintenanceRefusesUnknownAndCorruptState(t *testing.T) {
	ctx := context.Background()
	pool := Pool{Root: t.TempDir()}
	for _, document := range []string{
		`{"version":99,"future":[]}`,
		`{"version":1,"version":2,"jobs":[]}`,
		`{"version":1,"jobs":[}`,
		`{"version":2,"jobs":[{"id":"same"},{"id":"same"}]}`,
		`{"version":2,"jobs":[{"id":"job","history_days":-1}]}`,
	} {
		if err := os.WriteFile(pool.statePath(), []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Migrate(ctx, true); err == nil {
			t.Errorf("migration accepted %s", document)
		}
		if _, err := pool.Prune(ctx, time.Now(), true); err == nil {
			t.Errorf("prune accepted %s", document)
		}
		got, err := os.ReadFile(pool.statePath())
		if err != nil || string(got) != document {
			t.Fatalf("bad state was modified: %s, %v", got, err)
		}
	}
}

func TestPoolPruneProtectsActiveAndUndeliveredJobs(t *testing.T) {
	ctx := context.Background()
	pool := Pool{Root: t.TempDir()}
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	old := now.Add(-31 * 24 * time.Hour)
	notice := Notification{JobID: "protected", Attempt: 1, Status: "complete"}
	saved := state{
		Version: 1,
		Jobs: []Job{
			{ID: "pending", Status: "pending", UpdatedAt: old},
			{ID: "running", Status: "running", UpdatedAt: old},
			{ID: "stale", Status: "failed", UpdatedAt: old},
			{ID: "boundary", Status: "complete", UpdatedAt: now.Add(-24 * time.Hour), HistoryDays: 1},
			{ID: "fresh", Status: "complete", UpdatedAt: now.Add(-30 * 24 * time.Hour).Add(time.Second)},
			{ID: "protected", Status: "complete", UpdatedAt: old},
		},
		Subscriptions: []subscription{
			{JobID: "protected", SessionID: "first"}, {JobID: "protected", SessionID: "second"},
			{JobID: "stale", SessionID: "first"},
		},
		Events: []event{
			{Notification: notice, Delivered: []string{"first"}},
			{Notification: Notification{JobID: "stale", Status: "failed"}, Delivered: []string{"first"}},
		},
	}
	if err := pool.save(saved); err != nil {
		t.Fatal(err)
	}
	report, err := pool.Prune(ctx, now, false)
	if err != nil || report != (PruneReport{Eligible: 2, Protected: 1}) {
		t.Fatalf("preview: %+v, %v", report, err)
	}
	untouched, err := pool.load()
	if err != nil || untouched.Version != 1 || len(untouched.Jobs) != len(saved.Jobs) {
		t.Fatalf("preview changed old state: %+v, %v", untouched, err)
	}
	report, err = pool.Prune(ctx, now, true)
	if err != nil || report != (PruneReport{Eligible: 2, Protected: 1, Removed: 2}) {
		t.Fatalf("apply: %+v, %v", report, err)
	}
	if err := pool.MarkDelivered(ctx, notice, "second"); err != nil {
		t.Fatal(err)
	}
	report, err = pool.Prune(ctx, now, true)
	if err != nil || report != (PruneReport{Eligible: 1, Removed: 1}) {
		t.Fatalf("after handoff: %+v, %v", report, err)
	}
	remaining, err := pool.load()
	if err != nil || remaining.Version != currentStateVersion || len(remaining.Jobs) != 3 || len(remaining.Subscriptions) != 0 || len(remaining.Events) != 0 {
		t.Fatalf("cleanup left inconsistent state: %+v, %v", remaining, err)
	}
	for index, want := range []string{"pending", "running", "fresh"} {
		if remaining.Jobs[index].ID != want {
			t.Fatalf("pruned protected job: %+v", remaining.Jobs)
		}
	}
}

func TestPoolAdmissionPausePreservesPinnedHistoryAndExistingWork(t *testing.T) {
	ctx := context.Background()
	pool, request, _ := fixture(t)
	setPolicy := func(accepting bool, days int) {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"jackstay_schema": 2, "project": "pool-policy",
			"review": map[string]any{"pool": map[string]any{"accepting": accepting, "history_days": days}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(request.Repository, "jackstay.json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setPolicy(true, 7)
	job, created, err := pool.Enqueue(ctx, request)
	if err != nil || !created || job.HistoryDays != 7 || job.Generation == "" {
		t.Fatalf("initial enqueue: %+v, %v, %v", job, created, err)
	}
	setPolicy(false, 1)
	duplicate, created, err := pool.EnqueueForSession(ctx, request, strings.Repeat("a", 64), job.CommonDir)
	if err != nil || created || duplicate.Generation != job.Generation || duplicate.HistoryDays != 7 {
		t.Fatalf("paused duplicate: %+v, %v, %v", duplicate, created, err)
	}
	newRequest := request
	newRequest.Task = "new-task"
	if _, _, err := pool.Enqueue(ctx, newRequest); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("paused pool accepted new request: %v", err)
	}
	finished, found, err := pool.RunNext(ctx, func(context.Context, Job) (int, error) {
		return review.ExitReviewer, nil
	})
	if err != nil || !found || finished.Status != "failed" {
		t.Fatalf("paused worker: %+v, %v, %v", finished, found, err)
	}
	if err := pool.Retry(ctx, job.ID); err != nil {
		t.Fatalf("paused retry: %v", err)
	}
}
