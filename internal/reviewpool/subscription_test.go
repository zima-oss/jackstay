package reviewpool

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionClosurePreviewsAndPreservesOtherRecipient(t *testing.T) {
	ctx := context.Background()
	pool, request, _ := fixture(t)
	firstSession := strings.Repeat("a", 64)
	secondSession := strings.Repeat("b", 64)
	firstJob, created, err := pool.Enqueue(ctx, request)
	if err != nil || !created {
		t.Fatalf("enqueue: %v, %v", created, err)
	}
	for _, sessionID := range []string{firstSession, secondSession} {
		if _, created, err := pool.EnqueueForSession(ctx, request, sessionID, firstJob.CommonDir); err != nil || created {
			t.Fatalf("subscribe %s: %v, %v", sessionID, created, err)
		}
	}
	_, found, err := pool.RunNext(ctx, func(context.Context, Job) (int, error) { return 0, nil })
	if err != nil || !found {
		t.Fatalf("worker: %v, %v", found, err)
	}
	view, err := pool.ListSubscriptions(ctx, firstJob.ID)
	if err != nil || view.Generation != firstJob.Generation || len(view.Subscribers) != 2 ||
		view.Subscribers[0].SessionID != firstSession || view.Subscribers[0].Undelivered != 1 ||
		view.Subscribers[1].SessionID != secondSession || view.Subscribers[1].Undelivered != 1 {
		t.Fatalf("subscribers: %+v, %v", view, err)
	}
	before, err := os.ReadFile(pool.statePath())
	if err != nil {
		t.Fatal(err)
	}
	preview, err := pool.CloseSubscription(ctx, firstJob.ID, firstSession, view.Generation, false)
	if err != nil || preview.Applied || preview.AlreadyClosed || preview.Undelivered != 1 {
		t.Fatalf("closure preview: %+v, %v", preview, err)
	}
	after, err := os.ReadFile(pool.statePath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("preview mutated queue: %v", err)
	}
	if _, err := pool.CloseSubscription(ctx, firstJob.ID, firstSession, "outdated-generation", true); err == nil {
		t.Fatal("stale generation closed current subscription")
	}
	if _, err := pool.CloseSubscription(ctx, firstJob.ID, "not-a-session", view.Generation, true); err == nil {
		t.Fatal("invalid session identity accepted")
	}
	closed, err := pool.CloseSubscription(ctx, firstJob.ID, firstSession, view.Generation, true)
	if err != nil || !closed.Applied || closed.AlreadyClosed {
		t.Fatalf("closure apply: %+v, %v", closed, err)
	}
	retained, err := pool.ListSubscriptions(ctx, firstJob.ID)
	if err != nil || !retained.Subscribers[0].Closed || retained.Subscribers[1].Closed {
		t.Fatalf("closure affected another recipient: %+v, %v", retained, err)
	}
	if _, _, err := pool.EnqueueForSession(ctx, request, firstSession, firstJob.CommonDir); err == nil {
		t.Fatal("closed subscription silently reopened")
	}
	if _, _, err := pool.EnqueueForSession(ctx, request, secondSession, firstJob.CommonDir); err != nil {
		t.Fatalf("other recipient's duplicate submission: %v", err)
	}
	closedEvents, err := pool.Undelivered(ctx, firstSession, firstJob.CommonDir)
	if err != nil || len(closedEvents) != 0 {
		t.Fatalf("closed recipient still has queue delivery: %+v, %v", closedEvents, err)
	}
	openEvents, err := pool.Undelivered(ctx, secondSession, firstJob.CommonDir)
	if err != nil || len(openEvents) != 1 {
		t.Fatalf("other recipient lost delivery: %+v, %v", openEvents, err)
	}
	if err := pool.MarkDelivered(ctx, openEvents[0], firstSession); err == nil {
		t.Fatal("in-flight delivery marked accepted after closure")
	}
	if err := pool.MarkDelivered(ctx, openEvents[0], secondSession); err != nil {
		t.Fatalf("other recipient's delivery: %v", err)
	}
	repeated, err := pool.CloseSubscription(ctx, firstJob.ID, firstSession, view.Generation, true)
	if err != nil || repeated.Applied || !repeated.AlreadyClosed {
		t.Fatalf("idempotent closure: %+v, %v", repeated, err)
	}
	finished, err := pool.List(ctx)
	if err != nil || len(finished) != 1 {
		t.Fatalf("job after closure: %+v, %v", finished, err)
	}
	pruned, err := pool.Prune(ctx, finished[0].UpdatedAt.Add(31*24*time.Hour), true)
	if err != nil || pruned.Removed != 1 {
		t.Fatalf("closed recipient blocked history cleanup: %+v, %v", pruned, err)
	}
	newJob, created, err := pool.EnqueueForSession(ctx, request, firstSession, firstJob.CommonDir)
	if err != nil || !created || newJob.ID != firstJob.ID || newJob.Generation == firstJob.Generation {
		t.Fatalf("new generation after prune: %+v, %v, %v", newJob, created, err)
	}
}

func TestLegacySubscriptionClosureRequiresExplicitLegacyGeneration(t *testing.T) {
	ctx := context.Background()
	pool, request, _ := fixture(t)
	sessionID := strings.Repeat("c", 48)
	job, _, err := pool.Enqueue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.edit(ctx, func(saved *state) error {
		saved.Jobs[0].Generation = ""
		saved.subscribe(job.ID, sessionID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view, err := pool.ListSubscriptions(ctx, job.ID)
	if err != nil || view.Generation != "legacy" {
		t.Fatalf("legacy view: %+v, %v", view, err)
	}
	if _, err := pool.CloseSubscription(ctx, job.ID, sessionID, job.Generation, true); err == nil {
		t.Fatal("empty generation bypassed guard")
	}
	if result, err := pool.CloseSubscription(ctx, job.ID, sessionID, "legacy", true); err != nil || !result.Applied {
		t.Fatalf("legacy closure: %+v, %v", result, err)
	}
}
