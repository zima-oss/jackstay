package review

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/chatevent"
	"github.com/zima-oss/jackstay/internal/durable"
)

// backgroundHarness submits against a fake repository; launches are recorded, and a launched
// process lives while alive says so.
type backgroundHarness struct {
	background Background
	launched   [][]string
	alive      bool
	now        time.Time
}

func newBackgroundHarness(t *testing.T) *backgroundHarness {
	t.Helper()
	h := &backgroundHarness{now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	h.background = Background{RepositoryRoot: "/work/repository", Settings: testSettings(), Repo: newFakeRepo(),
		Store: Store{Root: t.TempDir()}, Executable: "/bin/jst", Now: func() time.Time { return h.now },
		Launch: func(argv []string, _, _ string) (int, int64, <-chan struct{}, error) {
			h.launched = append(h.launched, argv)
			h.alive = true
			return 100 + len(h.launched), 1, nil, nil
		},
		Alive: func(int, int64) (bool, bool) { return h.alive, true }}
	return h
}

type recording struct{ events []chatevent.Event }

func (publisher *recording) Publish(_ context.Context, event chatevent.Event) error {
	publisher.events = append(publisher.events, event)
	return nil
}

func TestABackgroundReviewIsOneJstBgRunPinnedToItsCommits(t *testing.T) {
	h := newBackgroundHarness(t)
	submitted, err := h.background.Submit(context.Background(), Submission{Revision: "main..HEAD", Subscriber: "s1"})
	if err != nil || submitted.Joined {
		t.Fatalf("submit: %+v, %v", submitted, err)
	}
	want := []string{"/bin/jst", "bg", "--for", "38", "--name", "review-feature/x", "--", "/bin/jst", "review",
		"--task", "feature/x", "--reviewer", "codex", "--request", submitted.Request.ID, "--", "c0..c1"}
	if len(h.launched) != 1 || !slices.Equal(h.launched[0], want) {
		t.Fatalf("launched %v\nwant     %v", h.launched, want)
	}
	saved, found, err := h.background.Store.ReadRequest("feature/x")
	if err != nil || !found || saved.PID != 101 || saved.Head != "c1" || saved.Base != "c0" || !slices.Equal(saved.Subscribers, []string{"s1"}) {
		t.Fatalf("request: %+v, %v, %v", saved, found, err)
	}
}

func TestAnEqualSubmissionJoinsAndADifferentOneIsRefusedWhileItRuns(t *testing.T) {
	h := newBackgroundHarness(t)
	ctx := context.Background()
	first, err := h.background.Submit(ctx, Submission{Revision: "HEAD", Subscriber: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := h.background.Submit(ctx, Submission{Revision: "c1", Subscriber: "s2"})
	if err != nil || !joined.Joined || joined.Request.ID != first.Request.ID || len(h.launched) != 1 ||
		!slices.Equal(joined.Request.Subscribers, []string{"s1", "s2"}) {
		t.Fatalf("join: %+v, %v", joined, err)
	}
	if _, err := h.background.Submit(ctx, Submission{Revision: "c2", Subscriber: "s3"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("a different review of a busy task: %v", err)
	}
	if _, err := h.background.Submit(ctx, Submission{Revision: "HEAD", Reviewers: []string{"codex", "codex"}}); err == nil {
		t.Fatal("a reviewer named twice was taken")
	}
}

func TestARunThatDiedIsEndedAsLostBeforeAnotherStarts(t *testing.T) {
	h := newBackgroundHarness(t)
	ctx := context.Background()
	if _, err := h.background.Submit(ctx, Submission{Revision: "HEAD", Subscriber: "s1"}); err != nil {
		t.Fatal(err)
	}
	h.alive = false
	again, err := h.background.Submit(ctx, Submission{Revision: "HEAD", Subscriber: "s2"})
	if err != nil || again.Joined || len(h.launched) != 2 || !slices.Equal(again.Request.Subscribers, []string{"s2"}) {
		t.Fatalf("a review after a dead run: %+v, %v", again, err)
	}
	h.alive = false
	other, err := h.background.Submit(ctx, Submission{Revision: "c2", Subscriber: "s3"})
	if err != nil || !slices.Equal(other.Request.Subscribers, []string{"s3"}) {
		t.Fatalf("a different review after a dead run: %+v, %v", other, err)
	}
	for _, subscriber := range []string{"s1", "s2"} {
		owed, err := h.background.Store.Owed(subscriber)
		if err != nil || len(owed) != 1 || owed[0].Meta["verdict"] != "lost" {
			t.Fatalf("%s was not told its review was lost: %+v, %v", subscriber, owed, err)
		}
	}
}

func TestARequestNotYetLaunchedIsLiveForAMinuteOnly(t *testing.T) {
	h := newBackgroundHarness(t)
	h.background.Launch = func([]string, string, string) (int, int64, <-chan struct{}, error) {
		return 0, 0, nil, errors.New("no such program")
	}
	if _, err := h.background.Submit(context.Background(), Submission{Revision: "HEAD"}); err == nil {
		t.Fatal("a failed launch was not said")
	}
	if _, found, _ := h.background.Store.ReadRequest("feature/x"); found {
		t.Fatal("a request whose launch failed was kept")
	}
	if end, found, _ := h.background.Store.LatestEnd("feature/x"); !found || end.Status != ExitUsage || !strings.Contains(end.Next, "no such program") {
		t.Fatalf("a failed launch did not end its request for the sessions that joined: %+v", end)
	}
	young := Request{Created: h.now.Add(-30 * time.Second).Format(time.RFC3339)}
	old := Request{Created: h.now.Add(-2 * time.Minute).Format(time.RFC3339)}
	if !h.background.live(young) || h.background.live(old) {
		t.Fatal("an unlaunched request's life is not its first minute")
	}
	h.background.Alive = func(int, int64) (bool, bool) { return false, false }
	before := Request{PID: 5, Deadline: h.now.Add(time.Minute).Format(time.RFC3339)}
	after := Request{PID: 5, Deadline: h.now.Add(-time.Minute).Format(time.RFC3339)}
	if !h.background.live(before) || h.background.live(after) {
		t.Fatal("without a process table, a request lives until its deadline")
	}
}

func TestFinishingARunTellsItsSubscribersOnceAndKeepsWhatTheChatRefused(t *testing.T) {
	h := newBackgroundHarness(t)
	ctx := context.Background()
	submitted, err := h.background.Submit(ctx, Submission{Revision: "HEAD", Subscriber: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.background.Submit(ctx, Submission{Revision: "HEAD", Subscriber: "s2"}); err != nil {
		t.Fatal(err)
	}
	store := h.background.Store
	if err := store.Finish(ctx, "feature/x", "another", Result{}, &recording{}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.ReadRequest("feature/x"); !found {
		t.Fatal("finishing another request removed this one")
	}
	record := RoundRecord{Round: 2}
	if err := store.Finish(ctx, "feature/x", submitted.Request.ID, Result{Status: ExitBlocks, Folder: "/store/round-2",
		Record: &record, Next: "fix"}, chatevent.Default()); !errors.Is(err, chatevent.ErrUnavailable) {
		t.Fatalf("no chat here, yet: %v", err)
	}
	if _, found, _ := store.ReadRequest("feature/x"); found {
		t.Fatal("a finished request was kept")
	}
	published := &recording{}
	for _, subscriber := range []string{"s1", "s2"} {
		if err := h.background.DeliverOwed(ctx, subscriber, published); err != nil {
			t.Fatal(err)
		}
	}
	if len(published.events) != 2 || published.events[0].Key == published.events[1].Key {
		t.Fatalf("one event per subscriber, each its own key: %+v", published.events)
	}
	event := published.events[0]
	if event.Kind != chatevent.KindReviewFinished || event.Meta["verdict"] != "blocks" || event.Meta["round"] != "2" ||
		!strings.Contains(event.Summary, "round 2") || event.Attachments[0].URI != "jst:review/feature/x/2" ||
		event.Worktree != "/work/repository" {
		t.Fatalf("event: %+v", event)
	}
	for _, subscriber := range []string{"s1", "s2"} {
		if owed, _ := store.Owed(subscriber); len(owed) != 0 {
			t.Fatalf("a published event is still owed to %s", subscriber)
		}
	}
}

func TestARunServingARequestMarksItRunningAndRefusesAReplacedOne(t *testing.T) {
	h := newHarness(t, Answer{Verdict: "approve", Summary: "clear"})
	background := newBackgroundHarness(t)
	background.background.Store = h.round.Store
	submitted, err := background.background.Submit(context.Background(), Submission{Revision: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	h.round.Request = "replaced"
	if result := h.round.Run(context.Background(), Options{}); result.Status != ExitUsage || !strings.Contains(result.Next, "request") {
		t.Fatalf("a run for a replaced request ran: %+v", result)
	}
	h.round.Request = submitted.Request.ID
	result := h.round.Run(context.Background(), Options{})
	if result.Status != ExitClear || result.Record.Request != submitted.Request.ID {
		t.Fatalf("run: %+v", result)
	}
	saved, found, err := h.round.Store.ReadRequest("feature/x")
	if err != nil || !found || !saved.Running {
		t.Fatalf("the request was not marked running: %+v, %v, %v", saved, found, err)
	}
}

func TestAResultIsSavedBeforeItsRequestIsRetired(t *testing.T) {
	h := newBackgroundHarness(t)
	ctx := context.Background()
	submitted, err := h.background.Submit(ctx, Submission{Revision: "HEAD", Subscriber: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	store := h.background.Store
	// The ends folder cannot be made: the result cannot be saved, so the request must stay.
	if err := os.WriteFile(store.endsPath(), []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, "feature/x", submitted.Request.ID, Result{Status: ExitClear}, &recording{}); err == nil {
		t.Fatal("an unsaved result was not said")
	}
	if _, found, _ := store.ReadRequest("feature/x"); !found {
		t.Fatal("the request was retired although its result was not saved")
	}
}

func TestARunThatDiedIsToldToItsSessionsWithoutAnotherSubmission(t *testing.T) {
	h := newBackgroundHarness(t)
	ctx := context.Background()
	if _, err := h.background.Submit(ctx, Submission{Revision: "HEAD", Subscriber: "s1"}); err != nil {
		t.Fatal(err)
	}
	published := &recording{}
	if err := h.background.DeliverOwed(ctx, "s1", published); err != nil || len(published.events) != 0 {
		t.Fatalf("a live run was taken for lost: %+v, %v", published.events, err)
	}
	h.alive = false
	if err := h.background.DeliverOwed(ctx, "s1", published); err != nil {
		t.Fatal(err)
	}
	if len(published.events) != 1 || published.events[0].Meta["verdict"] != "lost" {
		t.Fatalf("the session was not told its run died: %+v", published.events)
	}
	if _, found, _ := h.background.Store.ReadRequest("feature/x"); found {
		t.Fatal("the dead request was kept")
	}
}

func TestStatusSaysHowTheLatestRequestEndedEvenWithoutAnAttempt(t *testing.T) {
	h := newBackgroundHarness(t)
	ctx := context.Background()
	submitted, err := h.background.Submit(ctx, Submission{Revision: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.background.Store.Finish(ctx, "feature/x", submitted.Request.ID,
		Result{Status: ExitEnvironment, Next: "Codex has no login"}, &recording{}); err != nil {
		t.Fatal(err)
	}
	h.alive = false
	status, err := h.background.StatusOf("feature/x")
	if err != nil || status.Running != nil || status.Last == nil || status.Last.Status != ExitEnvironment ||
		status.Last.Request.Head != "c1" || status.Last.Next != "Codex has no login" || status.Last.Outcome != "the environment refuses" {
		t.Fatalf("status: %+v, %v", status, err)
	}
}

func TestACrashAfterTheEndWasWrittenTellsTheSameOutcomeAgain(t *testing.T) {
	h := newBackgroundHarness(t)
	ctx := context.Background()
	submitted, err := h.background.Submit(ctx, Submission{Revision: "HEAD", Subscriber: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	store := h.background.Store
	// The run wrote its end and died before removing its request.
	saved, _, _ := store.ReadRequest("feature/x")
	if err := durable.MakeFolder(store.endsPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(store.endPath("feature/x", saved.ID), End{Request: saved, Status: ExitClear, Outcome: "clear",
		Next: "nothing blocks", Ended: "2026-11-20T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	h.alive = false
	published := &recording{}
	if err := h.background.DeliverOwed(ctx, "s1", published); err != nil {
		t.Fatal(err)
	}
	if len(published.events) != 1 || published.events[0].Meta["verdict"] != "clear" {
		t.Fatalf("recovery told another outcome than the one saved: %+v", published.events)
	}
	if _, found, _ := store.ReadRequest("feature/x"); found {
		t.Fatal("an ended request was kept")
	}
	if err := store.Finish(ctx, "feature/x", submitted.Request.ID, Result{Status: ExitBlocks}, published); err != nil || len(published.events) != 1 {
		t.Fatalf("a late finish told a second outcome: %+v, %v", published.events, err)
	}
}
