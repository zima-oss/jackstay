package chatstore

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// hook rings a session's doorbell from a hook, then marks what it told of, as the hook does once its
// answer is handed over.
func (f *fixture) hook(key, hook string, stopHookActive bool) Ring {
	f.t.Helper()
	ring, err := f.store.HookRing(f.ctx, key, hook, stopHookActive)
	if err != nil {
		f.t.Fatalf("%s hook of %s: %v", hook, key, err)
	}
	if err := f.store.MarkHookRung(f.ctx, key, ring.Told); err != nil {
		f.t.Fatalf("mark what the %s hook of %s told of: %v", hook, key, err)
	}
	return ring
}

func TestTheDoorbellCarriesCountsAndNoText(t *testing.T) {
	cases := map[string]string{
		DoorbellLine(1, 0, 0): "1 new message.",
		DoorbellLine(3, 1, 0): "3 new messages (1 urgent).",
		DoorbellLine(2, 0, 1): "2 new messages and 1 notice.",
		DoorbellLine(0, 0, 4): "4 notices.",
	}
	for line, want := range cases {
		if !strings.Contains(line, want) || !strings.Contains(line, "chat.inbox") {
			t.Errorf("%q lacks %q", line, want)
		}
	}
}

func TestAPromptRemindsOfEveryUnreadMessageEachTime(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", Message: "secret body", Priority: PriorityLater})
	for range 2 {
		ring, err := f.store.HookRing(f.ctx, "cc:b", HookPrompt, false)
		if err != nil || !strings.Contains(ring.Text, "1 new message") || strings.Contains(ring.Text, "secret") || ring.Block {
			t.Fatalf("%+v %v", ring, err)
		}
	}
	f.inbox("cc:b", unread())
	if ring, _ := f.store.HookRing(f.ctx, "cc:b", HookPrompt, false); ring.Text != "" {
		t.Fatalf("a read message is not rung for: %q", ring.Text)
	}
}

func TestAToolBatchRingsOnceForEachNewMessage(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", Message: "next"})
	f.clock.advance(time.Second)
	f.send("cc:a", SendInput{To: "b", Message: "later", Priority: PriorityLater})
	first := f.hook("cc:b", HookToolBatch, false)
	if !strings.Contains(first.Text, "1 new message") {
		t.Fatalf("a tool batch rings for next, not later: %q", first.Text)
	}
	if second := f.hook("cc:b", HookToolBatch, false); second.Text != "" {
		t.Fatalf("once per new message: %q", second.Text)
	}
}

// A hook stopped before its answer was handed over told nothing: its deliveries ring again.
func TestAHookStoppedBeforeItsAnswerRingsAgain(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", Message: "urgent", Priority: PriorityNow})
	if first, err := f.store.HookRing(f.ctx, "cc:b", HookToolBatch, false); err != nil || len(first.Told) != 1 {
		t.Fatalf("the ring tells of the delivery: %+v %v", first, err)
	}
	if again := f.hook("cc:b", HookToolBatch, false); !strings.Contains(again.Text, "1 new message") {
		t.Fatalf("an unmarked delivery rings again: %q", again.Text)
	}
	if third := f.hook("cc:b", HookToolBatch, false); third.Text != "" {
		t.Fatalf("a marked delivery does not: %q", third.Text)
	}
}

func TestAStopHoldsTheTurnForNowAtMostThreeTimesInARow(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", Message: "not urgent"})
	if ring, _ := f.store.HookRing(f.ctx, "cc:b", HookStop, false); ring.Block || f.record("cc:b").State != StateIdle {
		t.Fatalf("a Stop holds only for now, and otherwise makes the session idle: %+v", ring)
	}
	f.session("cc:b", "", alpha, StateBusy)
	for round := range 4 {
		f.clock.advance(3 * time.Second)
		f.send("cc:a", SendInput{To: "b", Message: "urgent " + strings.Repeat("!", round), Priority: PriorityNow})
		ring := f.hook("cc:b", HookStop, round > 0)
		if round < maxStopBlocks {
			if !ring.Block || !strings.Contains(ring.Text, "urgent") || f.record("cc:b").State != StateBusy {
				t.Fatalf("round %d: %+v", round, ring)
			}
			continue
		}
		if ring.Block || f.record("cc:b").State != StateIdle {
			t.Fatalf("a fourth hold in a row is not made: %+v", ring)
		}
	}
}

func TestANativeRingMarksWhatItToldOfWhateverCameBack(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.native("cc:n", "n")
	sent := f.send("cc:a", SendInput{To: "n", Message: "secret body"})
	due, err := f.store.DueRings(f.ctx)
	if err != nil || len(due) != 1 || due[0].Recipient != "cc:n" || strings.Contains(due[0].Text, "secret") {
		t.Fatalf("%+v %v", due, err)
	}
	if err := f.store.RecordRing(f.ctx, due[0], RingTaken); err != nil {
		t.Fatal(err)
	}
	if delivery := f.delivery(sent.MessageID, "cc:n"); !delivery.Rung || delivery.State != "pending" {
		t.Fatalf("a taken ring marks and changes no receipt: %+v", delivery)
	}
	if again, _ := f.store.DueRings(f.ctx); len(again) != 0 {
		t.Fatalf("nothing new to ring for: %+v", again)
	}
	if ring, _ := f.store.HookRing(f.ctx, "cc:n", HookPrompt, false); !strings.Contains(ring.Text, "1 new message") {
		t.Fatalf("the hooks still remind of it, since the ring proves nothing: %q", ring.Text)
	}
}

func TestAClosedConnectionRefusesWhatTheRingToldOf(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.native("cc:n", "n")
	sent := f.send("cc:a", SendInput{To: "n", Message: "refused"})
	due, _ := f.store.DueRings(f.ctx)
	if err := f.store.RecordRing(f.ctx, due[0], RingClosed); err != nil {
		t.Fatal(err)
	}
	delivery := f.delivery(sent.MessageID, "cc:n")
	if delivery.State != "refused" || delivery.Reason != "engine_refused" || delivery.Notice != "owed" {
		t.Fatalf("%+v", delivery)
	}
	if again, _ := f.store.DueRings(f.ctx); len(again) != 0 {
		t.Fatalf("a refused ring is not rung again: %+v", again)
	}
}

func TestAFailedRingTriesAgainLater(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.native("cc:n", "n")
	f.send("cc:a", SendInput{To: "n", Message: "nobody listens"})
	for _, wait := range ringRetry {
		due, _ := f.store.DueRings(f.ctx)
		if len(due) != 1 {
			t.Fatalf("due again after %s", wait)
		}
		if err := f.store.RecordRing(f.ctx, due[0], RingFailed); err != nil {
			t.Fatal(err)
		}
		if early, _ := f.store.DueRings(f.ctx); len(early) != 0 {
			t.Fatal("rung again before its wait")
		}
		f.clock.advance(wait)
	}
}

func TestOnlyAnIdleNativeSessionIsRungOnItsSocket(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.native("cc:n", "n")
	if _, err := f.store.Observe(f.ctx, "cc:n", ObserveBusy, false); err != nil {
		t.Fatal(err)
	}
	f.send("cc:a", SendInput{To: "n", Message: "for later"})
	if due, _ := f.store.DueRings(f.ctx); len(due) != 0 {
		t.Fatal("a busy session hears its hooks, not its socket")
	}
	if _, err := f.store.Observe(f.ctx, "cc:n", ObserveIdle, false); err != nil {
		t.Fatal(err)
	}
	if due, _ := f.store.DueRings(f.ctx); len(due) != 1 {
		t.Fatal("an idle one is rung")
	}
}

func TestANoticeAloneRingsOnce(t *testing.T) {
	f := newFixture(t)
	f.native("cc:n", "n")
	f.session("cc:b", "b", alpha, StateIdle)
	f.send("cc:n", SendInput{To: "b", Message: "short-lived", ExpiresInSeconds: 60})
	f.clock.advance(time.Minute)
	if err := f.store.Sweep(f.ctx); err != nil {
		t.Fatal(err)
	}
	due, _ := f.store.DueRings(f.ctx)
	if len(due) != 1 || !strings.Contains(due[0].Text, "1 notice") {
		t.Fatalf("%+v", due)
	}
	if err := f.store.RecordRing(f.ctx, due[0], RingClosed); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.store.DueRings(f.ctx); len(again) != 0 {
		t.Fatalf("a notice is rung for once, whatever came back: %+v", again)
	}
	if notices := f.inbox("cc:n", unread()).Notices; len(notices) != 1 {
		t.Fatalf("it stays owed until the inbox shows it: %v", notices)
	}
}

// A refused ring refuses every pending delivery it counted, also one an earlier ring told of
// (review round 1).
func TestARefusedRingRefusesEveryDeliveryItCounted(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.native("cc:n", "n")
	first := f.send("cc:a", SendInput{To: "n", Message: "ignored"})
	due, _ := f.store.DueRings(f.ctx)
	if err := f.store.RecordRing(f.ctx, due[0], RingTaken); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(3 * time.Second)
	second := f.send("cc:a", SendInput{To: "n", Message: "new work"})
	due, _ = f.store.DueRings(f.ctx)
	if len(due) != 1 || !strings.Contains(due[0].Text, "2 new messages") {
		t.Fatalf("%+v", due)
	}
	if err := f.store.RecordRing(f.ctx, due[0], RingClosed); err != nil {
		t.Fatal(err)
	}
	for _, sent := range []SendResult{first, second} {
		if delivery := f.delivery(sent.MessageID, "cc:n"); delivery.State != "refused" {
			t.Fatalf("%s is %s, not refused", sent.MessageID, delivery.State)
		}
	}
}

// A new notice rings even past the 50 an inbox page shows (review round 1).
func TestANewNoticePastAnInboxPageRings(t *testing.T) {
	f := newFixture(t)
	f.native("cc:n", "n")
	f.session("cc:b", "b", alpha, StateIdle)
	f.session("cc:c", "c", alpha, StateIdle)
	for index := range 51 {
		f.clock.advance(3 * time.Second)
		to := []string{"b", "c"}[index%2] // under the loop limit of each pair
		f.send("cc:n", SendInput{To: to, Message: fmt.Sprintf("short-lived %d", index), ExpiresInSeconds: 60})
		if index == 49 {
			f.clock.advance(time.Minute)
			if err := f.store.Sweep(f.ctx); err != nil {
				t.Fatal(err)
			}
			due, _ := f.store.DueRings(f.ctx)
			if len(due) != 1 {
				t.Fatalf("the first fifty ring: %+v", due)
			}
			if err := f.store.RecordRing(f.ctx, due[0], RingTaken); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.clock.advance(time.Minute)
	if err := f.store.Sweep(f.ctx); err != nil {
		t.Fatal(err)
	}
	if due, _ := f.store.DueRings(f.ctx); len(due) != 1 || !strings.Contains(due[0].Text, "51 notices") {
		t.Fatalf("the fifty-first rings: %+v", due)
	}
}

// A Stop that is not a continuation starts a fresh count, whatever an interrupted idle left
// (review round 1).
func TestAStopThatIsNotAContinuationCountsAfresh(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	if err := f.store.update(f.ctx, func(transaction *transaction) error {
		session, err := transaction.readSession(hashKey("cc:b"))
		if err != nil {
			return err
		}
		session.StopBlocks = maxStopBlocks // left by an idle whose write a stop cut short
		return transaction.writeSession(session)
	}); err != nil {
		t.Fatal(err)
	}
	f.send("cc:a", SendInput{To: "b", Message: "urgent", Priority: PriorityNow})
	f.hook("cc:b", HookStop, false)
	f.clock.advance(3 * time.Second)
	f.send("cc:a", SendInput{To: "b", Message: "urgent again", Priority: PriorityNow})
	if ring := f.hook("cc:b", HookStop, true); !ring.Block {
		t.Fatalf("the second hold in this turn is made: %+v", ring)
	}
}

// An error reading what a ring recorded is returned, never taken as the session gone (review
// round 1).
func TestARecordRingErrorIsNotAnOutcome(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.native("cc:n", "n")
	f.send("cc:a", SendInput{To: "n", Message: "x"})
	due, _ := f.store.DueRings(f.ctx)
	record := f.store.place.session(hashKey("cc:n"))
	if err := os.Chmod(record, 0o000); err != nil {
		t.Fatal(err)
	}
	err := f.store.RecordRing(f.ctx, due[0], RingTaken)
	if chmodErr := os.Chmod(record, 0o600); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if err == nil {
		if _, statErr := os.ReadFile(record); statErr == nil {
			t.Skip("this account reads an unreadable file (root)")
		}
		t.Fatal("an unreadable session record is an error")
	}
}
