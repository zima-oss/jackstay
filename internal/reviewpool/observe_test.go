package reviewpool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/telemetrytest"
)

func TestInspectSummarizesQueueWithoutIdentifiersOrWrites(t *testing.T) {
	pool := Pool{Root: t.TempDir()}
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	pending := Job{ID: fmt.Sprintf("%024x", 1), Repository: "/private/repository", Task: "private-task",
		Status: "pending", CreatedAt: old}
	saved := state{Version: 1, Jobs: []Job{
		pending,
		{Status: "pending", CreatedAt: now.Add(-time.Hour), NotBefore: now.Add(time.Minute)},
		{Status: "running", CreatedAt: old},
		{Status: "running", FailureClass: "unresolved", CreatedAt: old},
		{Status: "complete", Exit: review.ExitClear},
		{Status: "complete", Exit: review.ExitBlocks},
		{Status: "complete", Exit: review.ExitRedesign},
		{Status: "complete", Exit: review.ExitOwner},
		{Status: "failed", FailureClass: "retryable"},
		{Status: "failed", FailureClass: "operator"},
	}, Subscriptions: []subscription{
		{JobID: pending.ID, SessionID: "private-session-1"},
		{JobID: pending.ID, SessionID: "private-session-2"},
	}, Events: []event{{Notification: Notification{JobID: pending.ID}, Delivered: []string{"private-session-1"}}}}
	for index := range saved.Jobs {
		saved.Jobs[index].ID = fmt.Sprintf("%024x", index+1)
	}
	if err := pool.save(saved); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pool.statePath())
	if err != nil {
		t.Fatal(err)
	}
	view, err := pool.Inspect(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if view.Version != 1 || view.Total != 10 || view.Pending != 2 || view.Running != 2 || view.Complete != 4 ||
		view.Failed != 2 || view.Eligible != 1 || view.Delayed != 1 || view.Recoverable != 1 || view.Unresolved != 1 ||
		!view.Ready || view.OldestPendingAt == nil || !view.OldestPendingAt.Equal(old) ||
		view.OldestPendingAgeSeconds != 7200 {
		t.Fatalf("queue overview = %+v", view)
	}
	if view.Outcomes.Clear != 1 || view.Outcomes.Blocks != 1 || view.Outcomes.Redesign != 1 ||
		view.Outcomes.Owner != 1 || view.Outcomes.Retryable != 1 || view.Outcomes.Operator != 1 ||
		view.Notifications.Undelivered != 1 || view.Notifications.Materialized != 1 {
		t.Fatalf("outcomes/notifications = %+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{pending.ID, "/private/repository", "private-task", "private-session"} {
		if strings.Contains(string(encoded), sensitive) {
			t.Fatalf("operator overview leaked %q: %s", sensitive, encoded)
		}
	}
	after, err := os.ReadFile(pool.statePath())
	if err != nil || string(before) != string(after) {
		t.Fatalf("inspection modified state: %v", err)
	}
}

func TestInspectMissingPoolCreatesNoState(t *testing.T) {
	pool := Pool{Root: t.TempDir() + "/absent"}
	view, err := pool.Inspect(context.Background(), time.Now())
	if err != nil || view.Total != 0 || view.Ready {
		t.Fatalf("empty overview = %+v, %v", view, err)
	}
	if _, err := os.Stat(pool.Root); !os.IsNotExist(err) {
		t.Fatalf("inspection created pool directory: %v", err)
	}
}

func TestPoolTransitionTelemetryContainsOnlyFixedFields(t *testing.T) {
	home := telemetrytest.Begin(t, "review pool", nil, nil)
	recordPoolTransition(Job{ID: strings.Repeat("a", 24), Repository: "/private/repository", Task: "private-task",
		Status: "complete", Attempts: 2, Exit: review.ExitBlocks}, "finish")
	telemetry.Finish(0)
	spans := telemetrytest.Spans(t, home)
	if len(spans) != 1 || len(spans[0].Events) != 1 {
		t.Fatalf("pool transition event = %+v", spans)
	}
	event := spans[0].Events[0]
	if event.Name != telemetry.EventReviewPoolTransition || event.Text(telemetry.AttrReviewPoolJob) != strings.Repeat("a", 24) ||
		event.Text(telemetry.AttrReviewPoolState) != "complete" || event.Text(telemetry.AttrReviewPoolAction) != "finish" ||
		event.Number(telemetry.AttrReviewPoolAttempts) != 2 || event.Number(telemetry.AttrReviewPoolExit) != review.ExitBlocks {
		t.Fatalf("pool transition = %+v", event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "/private/repository") || strings.Contains(string(encoded), "private-task") {
		t.Fatalf("pool event exposed private input: %s", encoded)
	}
}
