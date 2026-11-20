package chatstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (f *fixture) inbox(key string, input InboxInput) InboxResult {
	f.t.Helper()
	result, err := f.store.Inbox(f.ctx, key, input)
	if err != nil {
		f.t.Fatalf("inbox of %s: %v", key, err)
	}
	if size := len(marshalCompact(result)); size > ResultBound {
		f.t.Fatalf("the result takes %d bytes, past %d", size, ResultBound)
	}
	return result
}

func unread() InboxInput { return InboxInput{UnreadOnly: true, MarkRead: true} }

func (f *fixture) native(key, name string) Session {
	f.t.Helper()
	session, err := f.store.Register(f.ctx, Registration{Key: key, KeySource: SourceEngine, Engine: EngineClaudeCode,
		Name: name, Channel: alpha, Delivery: DeliveryNative, State: StateIdle})
	if err != nil {
		f.t.Fatal(err)
	}
	return session
}

func (f *fixture) setDelivery(id, key string, change func(*Delivery)) {
	f.t.Helper()
	delivery := f.delivery(id, key)
	change(&delivery)
	if err := writeRecord(f.store.place.delivery(id, hashKey(key)), delivery, deliveryBound); err != nil {
		f.t.Fatal(err)
	}
}

func TestAnInboxReadReturnsTheWholeBodyOnceAndMarksItRead(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	sent := f.send("cc:a", SendInput{To: "b", Message: "hello there"})
	result := f.inbox("cc:b", unread())
	if len(result.Messages) != 1 || result.Messages[0].Message != "hello there" || !result.Messages[0].Complete ||
		result.Messages[0].State != "read" || result.Messages[0].From.Name != "a" {
		t.Fatalf("%+v", result)
	}
	if exists(filepath.Join(f.store.place.inbox(hashKey("cc:b")), sent.MessageID)) {
		t.Fatal("a read delivery keeps no inbox marker")
	}
	if again := f.inbox("cc:b", unread()); len(again.Messages) != 0 {
		t.Fatalf("read once: %+v", again)
	}
	all := f.inbox("cc:b", InboxInput{})
	if len(all.Messages) != 1 || all.Messages[0].State != "read" {
		t.Fatalf("unread_only false lists read ones too: %+v", all)
	}
}

func TestANativeDeliveryIsReadFromTheInboxLikeAnyOther(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.native("cc:n", "n")
	sent := f.send("cc:a", SendInput{To: "n", Message: "read me from the inbox"})
	result := f.inbox("cc:n", unread())
	if len(result.Messages) != 1 || result.Messages[0].Message != "read me from the inbox" || result.Messages[0].State != "read" {
		t.Fatalf("W-095: the text is read from the inbox, and the read is the receipt: %+v", result)
	}
	if f.delivery(sent.MessageID, "cc:n").State != "read" {
		t.Fatal("the receipt is read")
	}
}
func TestARefusedDoorbellHidesTheBody(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.native("cc:n", "n")
	refused := f.send("cc:a", SendInput{To: "n", Message: "refused"})
	f.setDelivery(refused.MessageID, "cc:n", func(delivery *Delivery) {
		delivery.State, delivery.Reason, delivery.Notice = "refused", "engine_refused", "owed"
	})
	if result := f.inbox("cc:n", InboxInput{IDs: []string{refused.MessageID}}); len(result.Messages) != 0 {
		t.Fatalf("%+v", result)
	}
}
func TestPagesFitTheBoundAndReassembleEveryBody(t *testing.T) {
	f := newFixture(t)
	f.session("cc:b", "b", alpha, StateIdle)
	bodies := map[string]string{}
	for index := range 4 {
		key := "cc:s" + strings.Repeat("x", index)
		f.session(key, "s"+strings.Repeat("x", index), alpha, StateIdle)
		body := strings.Repeat(string(rune('a'+index))+"\"é\n", MaxBodyBytes/5)
		sent := f.send(key, SendInput{To: "b", Message: body})
		bodies[sent.MessageID] = body
	}
	got := map[string]string{}
	input := unread()
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("following next_cursor never ended")
		}
		result := f.inbox("cc:b", input)
		for _, message := range result.Messages {
			if len(got[message.ID]) != message.Offset {
				t.Fatalf("%s continues at %d, have %d", message.ID, message.Offset, len(got[message.ID]))
			}
			got[message.ID] += message.Message
			whole := message.Complete && message.Offset == 0
			if message.State == "read" && !whole {
				t.Fatalf("only a body returned whole in one response is marked read: %+v", message.ID)
			}
		}
		if result.NextCursor == "" {
			break
		}
		input.Cursor = result.NextCursor
	}
	for id, body := range bodies {
		if got[id] != body {
			t.Fatalf("%s reassembled to %d bytes, want %d", id, len(got[id]), len(body))
		}
	}
	if _, err := f.store.Inbox(f.ctx, "cc:b", InboxInput{Cursor: input.Cursor, UnreadOnly: false}); input.Cursor != "" && ReasonOf(err) != ReasonInvalid {
		t.Fatalf("a cursor with other filters is refused: %v", err)
	}
}

func TestAcksMoveForwardAndOnlyTheRecipientMayAck(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	f.session("cc:c", "c", alpha, StateIdle)
	sent := f.send("cc:a", SendInput{To: "b", Message: "handle this"})
	_, err := f.store.Ack(f.ctx, "cc:c", sent.MessageID, "acted", "")
	f.refused(err, ReasonNotRecipient)
	acted, err := f.store.Ack(f.ctx, "cc:b", sent.MessageID, "", "done")
	if err != nil || acted.State != "acted" || !acted.Changed || f.delivery(sent.MessageID, "cc:b").Note != "done" {
		t.Fatalf("%+v %v", acted, err)
	}
	back, err := f.store.Ack(f.ctx, "cc:b", sent.MessageID, "read", "")
	if err != nil || back.State != "acted" || back.Changed {
		t.Fatalf("states only move forward: %+v %v", back, err)
	}
	expired := f.send("cc:a", SendInput{To: "b", Message: "too late", ExpiresInSeconds: 60})
	f.clock.advance(time.Minute)
	late, err := f.store.Ack(f.ctx, "cc:b", expired.MessageID, "read", "")
	if err != nil || late.State != "expired" || late.Detail == "" {
		t.Fatalf("an ack of an expired delivery says so: %+v %v", late, err)
	}
}

func TestAnExpiredDeliveryOwesItsSenderOneNotice(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	sent := f.send("cc:a", SendInput{To: "b", Message: "read me soon", ExpiresInSeconds: 60})
	f.clock.advance(time.Minute)
	if result := f.inbox("cc:b", unread()); len(result.Messages) != 0 {
		t.Fatalf("a read past the deadline returns nothing: %+v", result)
	}
	if delivery := f.delivery(sent.MessageID, "cc:b"); delivery.State != "expired" || delivery.Notice != "owed" {
		t.Fatalf("%+v", delivery)
	}
	first := f.inbox("cc:a", unread())
	if len(first.Notices) != 1 || !strings.Contains(first.Notices[0], "expired unread") {
		t.Fatalf("%+v", first)
	}
	if second := f.inbox("cc:a", unread()); len(second.Notices) != 0 {
		t.Fatalf("a notice is shown once: %+v", second)
	}
}

func TestAnExpiredRequestOwesOneNoticeOnItsCommitRecord(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	asked := f.send("cc:a", SendInput{To: "b", Message: "decide", Request: &RequestInput{Expects: "decision"}, ExpiresInSeconds: 60})
	f.inbox("cc:b", unread()) // delivered and read before the deadline
	f.clock.advance(time.Minute)
	if err := f.store.Sweep(f.ctx); err != nil {
		t.Fatal(err)
	}
	if commit := f.commit(asked.MessageID); commit.RequestState != "expired" || commit.Notice != "owed" {
		t.Fatalf("a read request still closes at its deadline: %+v", commit)
	}
	if delivery := f.delivery(asked.MessageID, "cc:b"); delivery.State != "read" || delivery.Notice != "" {
		t.Fatalf("its read receipt stays true: %+v", delivery)
	}
	notices := f.inbox("cc:a", unread()).Notices
	if len(notices) != 1 || !strings.Contains(notices[0], "expired unanswered") {
		t.Fatalf("%v", notices)
	}
}

func TestAThreadShowsWhatItsReaderMaySee(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.native("cc:n", "n")
	f.session("cc:c", "c", beta, StateIdle)
	first := f.send("cc:n", SendInput{To: "a", Message: "from n"})
	f.clock.advance(time.Second)
	reply := f.send("cc:a", SendInput{To: "n", Message: "back to n", ReplyTo: first.MessageID})
	f.clock.advance(time.Second)
	refused := f.send("cc:a", SendInput{To: "n", Message: "its doorbell was refused", ReplyTo: first.MessageID})
	f.setDelivery(refused.MessageID, "cc:n", func(delivery *Delivery) {
		delivery.State, delivery.Reason, delivery.Notice = "refused", "engine_refused", "owed"
	})
	thread, err := f.store.Thread(f.ctx, "cc:n", ThreadInput{ThreadID: first.ThreadID})
	if err != nil || len(thread.Messages) != 3 || thread.Messages[0].Message != "from n" ||
		thread.Messages[1].Message != "back to n" ||
		!thread.Messages[2].Hidden || thread.Messages[2].Summary != "" || thread.Messages[2].From.Name != "" {
		t.Fatalf("%+v %v", thread, err)
	}
	if f.delivery(reply.MessageID, "cc:n").State != "pending" {
		t.Fatal("reading a thread changes no receipt")
	}
	f.send("cc:a", SendInput{To: "#alpha", Message: "news for alpha"})
	history, err := f.store.Thread(f.ctx, "cc:c", ThreadInput{Channel: "#alpha"})
	if err != nil || len(history.Messages) != 1 || history.Messages[0].Message != "news for alpha" {
		t.Fatalf("a channel's history is open to a session with no delivery of it: %+v %v", history, err)
	}
	_, err = f.store.Thread(f.ctx, "cc:c", ThreadInput{ThreadID: first.ThreadID, Channel: "#alpha"})
	f.refused(err, ReasonInvalid)
}

func TestTheOutboxShowsEachReceiptAndTheRecipientsLiveState(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.session("cc:c", "c", alpha, StateIdle)
	sent := f.send("cc:a", SendInput{To: "#alpha", Message: "to both"})
	f.inbox("cc:b", unread())
	outbox, err := f.store.Outbox(f.ctx, "cc:a", OutboxInput{})
	if err != nil || len(outbox.Messages) != 1 || outbox.Messages[0].Counts["read"] != 1 || outbox.Messages[0].Counts["pending"] != 1 {
		t.Fatalf("%+v %v", outbox, err)
	}
	rows, err := f.store.Outbox(f.ctx, "cc:a", OutboxInput{MessageID: sent.MessageID, States: []string{"read"}})
	if err != nil || len(rows.Recipients) != 1 || rows.Recipients[0].Session.Name != "b" || rows.Recipients[0].LiveState != "busy" ||
		rows.Recipients[0].Route != "hooks" {
		t.Fatalf("%+v %v", rows, err)
	}
	_, err = f.store.Outbox(f.ctx, "cc:b", OutboxInput{MessageID: sent.MessageID})
	f.refused(err, ReasonUnknownMessage)
}

func TestIdleSubscriptionsFireOnTheNextIdleOnly(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", NotifyWhenIdle: true})
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveBusy, false); err != nil {
		t.Fatal(err)
	}
	if len(f.record("cc:a").Notices) != 0 {
		t.Fatal("busy fires nothing")
	}
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveIdle, false); err != nil {
		t.Fatal(err)
	}
	notices := f.record("cc:a").Notices
	if len(notices) != 1 || notices[0].Kind != "idle" || len(f.record("cc:b").IdleWatchers) != 0 {
		t.Fatalf("%+v", notices)
	}
	shown := f.inbox("cc:a", unread()).Notices
	if len(shown) != 1 || !strings.Contains(shown[0], "is idle") {
		t.Fatalf("%v", shown)
	}

	// With a message, the subscription arms only once the message is shown: the idle before it
	// never counts.
	f.clock.advance(time.Minute)
	f.send("cc:a", SendInput{To: "b", Message: "tell me when done", NotifyWhenIdle: true})
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveIdle, false); err != nil {
		t.Fatal(err)
	}
	if len(f.inbox("cc:a", unread()).Notices) != 0 {
		t.Fatal("an unarmed subscription does not fire")
	}
	f.inbox("cc:b", unread())
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveIdle, false); err != nil {
		t.Fatal(err)
	}
	if fired := f.inbox("cc:a", unread()).Notices; len(fired) != 1 || !strings.Contains(fired[0], "is idle") {
		t.Fatalf("%v", fired)
	}
}

func TestASessionEndDropsWhatWaitsForItAndTellsItsWatchers(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	sent := f.send("cc:a", SendInput{To: "b", Message: "never read"})
	f.send("cc:a", SendInput{To: "b", NotifyWhenIdle: true})
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveGone, false); err != nil {
		t.Fatal(err)
	}
	if delivery := f.delivery(sent.MessageID, "cc:b"); delivery.State != "dropped" || delivery.Reason != "recipient_ended" {
		t.Fatalf("%+v", delivery)
	}
	notices := f.inbox("cc:a", unread()).Notices
	if len(notices) != 2 {
		t.Fatalf("one dropped notice and one ended notice: %v", notices)
	}
}

func TestAPersonsPromptResetsTheLoopCount(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	f.send("cc:a", SendInput{To: "b", Message: "one"})
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveBusy, false); err != nil || len(f.record("cc:b").Exchange) != 1 {
		t.Fatalf("a peer's prompt keeps it: %v", err)
	}
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveBusy, true); err != nil || len(f.record("cc:b").Exchange) != 0 {
		t.Fatalf("a person's prompt resets it: %v", err)
	}
}

func TestTheSweepSettlesAStaleSendingAsUnconfirmed(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.native("cc:n", "n")
	sent := f.send("cc:a", SendInput{To: "n", Message: "pushed, maybe"})
	f.setDelivery(sent.MessageID, "cc:n", func(delivery *Delivery) {
		delivery.State, delivery.Pusher, delivery.At = "sending", "pass-1", stamp(f.clock.at)
	})
	f.clock.advance(61 * time.Second)
	if err := f.store.Sweep(f.ctx); err != nil {
		t.Fatal(err)
	}
	if delivery := f.delivery(sent.MessageID, "cc:n"); delivery.State != "delivered" || delivery.Reason != "unconfirmed" || delivery.Pusher != "" {
		t.Fatalf("%+v", delivery)
	}
}

func TestRetentionRemovesDoneMessagesAfterSevenDays(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	sent := f.send("cc:a", SendInput{To: "b", Message: "keep a week", IdempotencyKey: "keep-a-week"})
	f.inbox("cc:b", unread())
	f.clock.advance(6 * 24 * time.Hour)
	if err := f.store.Sweep(f.ctx); err != nil || !exists(f.store.place.commit(sent.MessageID)) {
		t.Fatalf("kept for 7 days: %v", err)
	}
	f.clock.advance(25 * time.Hour)
	if err := f.store.Sweep(f.ctx); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.store.place.commit(sent.MessageID), f.store.place.envelope(sent.MessageID),
		f.store.place.body(sent.MessageID), f.store.place.idempotencyEntry("cc:a", "keep-a-week"),
		filepath.Join(f.store.place.outbox(hashKey("cc:a")), sent.MessageID)} {
		if exists(path) {
			t.Errorf("%s left after retention", path)
		}
	}
	if counted := f.measured(); counted.Messages != 0 {
		t.Fatalf("%+v", counted)
	}
}

func TestPressureRemovesOnlyDoneMessages(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	done := f.send("cc:a", SendInput{To: "b", Message: "read already"})
	f.inbox("cc:b", unread())
	f.clock.advance(time.Minute)
	waiting := f.send("cc:a", SendInput{To: "b", Message: "not read yet"})
	// Room for nothing new, even once the done message is gone.
	f.store.writeLimit = f.measured().Bytes + 100
	f.clock.advance(time.Minute)
	_, err := f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "b", Message: "needs room"})
	if exists(f.store.place.commit(done.MessageID)) {
		t.Fatal("a done message is removed for room")
	}
	if !exists(f.store.place.commit(waiting.MessageID)) {
		t.Fatal("a message a recipient may still read is never removed")
	}
	f.refused(err, ReasonCapacity)
}

func TestAnEndBeforeTheMessageArrivedOwesOneOutcomeForTheSubscription(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", Message: "tell me when done", NotifyWhenIdle: true})
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveGone, false); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, notice := range f.record("cc:a").Notices {
		kinds[notice.Kind]++
	}
	if kinds[NoticeNotDelivered] != 1 || kinds[NoticeEnded] != 0 {
		t.Fatalf("one not_delivered, no ended: %v", kinds)
	}
}

func TestAnAnswerArmsTheRequestersIdleSubscription(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	asked := f.send("cc:a", SendInput{To: "b", Message: "decide", Request: &RequestInput{Expects: "decision"}, NotifyWhenIdle: true})
	if _, err := f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: asked.RequestID, Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	if watchers := f.record("cc:b").IdleWatchers; len(watchers) != 1 || !watchers[0].Armed {
		t.Fatalf("%+v", watchers)
	}
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveIdle, false); err != nil {
		t.Fatal(err)
	}
	if notices := f.record("cc:a").Notices; len(notices) != 1 || notices[0].Kind != NoticeIdle {
		t.Fatalf("%+v", notices)
	}
}

func TestSubscriptionsExpiringTogetherKeepEachOthersNotices(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateBusy)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", NotifyWhenIdle: true})
	f.send("cc:b", SendInput{To: "a", NotifyWhenIdle: true})
	f.clock.advance(13 * time.Hour)
	if err := f.store.Sweep(f.ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cc:a", "cc:b"} {
		if notices := f.record(key).Notices; len(notices) != 1 || notices[0].Kind != NoticeSubscriptionExpired {
			t.Fatalf("%s: %+v", key, notices)
		}
	}
}

func TestAReceiptMovingBetweenOutboxPagesHidesNoOtherRow(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	f.session("cc:c", "c", alpha, StateIdle)
	sent := f.send("cc:a", SendInput{To: "#alpha", Message: "to both"})
	first, err := f.store.Outbox(f.ctx, "cc:a", OutboxInput{MessageID: sent.MessageID, States: []string{"pending"}, Limit: 1})
	if err != nil || len(first.Recipients) != 1 || first.NextCursor == "" {
		t.Fatalf("%+v %v", first, err)
	}
	f.inbox("cc:"+first.Recipients[0].Session.Name, unread())
	second, err := f.store.Outbox(f.ctx, "cc:a", OutboxInput{MessageID: sent.MessageID, States: []string{"pending"}, Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Recipients) != 1 || second.Recipients[0].Session.Name == first.Recipients[0].Session.Name {
		t.Fatalf("%+v %v", second, err)
	}
}

func TestAThreadCursorSurvivesTheRemovalOfItsMessage(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	first := f.send("cc:a", SendInput{To: "b", Message: "one"})
	f.clock.advance(time.Second)
	f.send("cc:b", SendInput{To: "a", Message: "two", ReplyTo: first.MessageID})
	page, err := f.store.Thread(f.ctx, "cc:a", ThreadInput{ThreadID: first.ThreadID, Limit: 1})
	if err != nil || len(page.Messages) != 1 || page.NextCursor == "" {
		t.Fatalf("%+v %v", page, err)
	}
	if err := f.store.update(f.ctx, func(transaction *transaction) error {
		found, err := transaction.message(first.MessageID)
		if err != nil {
			return err
		}
		_, err = transaction.removeMessage(found.envelope)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rest, err := f.store.Thread(f.ctx, "cc:a", ThreadInput{ThreadID: first.ThreadID, Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(rest.Messages) != 1 || rest.Messages[0].Message != "two" {
		t.Fatalf("the next surviving message: %+v %v", rest, err)
	}
}

func TestAnOutboxFilterSeesARequestClosedByItsDeadline(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	f.send("cc:a", SendInput{To: "b", Message: "decide", Request: &RequestInput{Expects: "decision"}, ExpiresInSeconds: 60})
	f.inbox("cc:b", unread())
	f.clock.advance(time.Minute)
	found, err := f.store.Outbox(f.ctx, "cc:a", OutboxInput{RequestStates: []string{"expired"}})
	if err != nil || len(found.Messages) != 1 || found.Messages[0].Request.State != "expired" {
		t.Fatalf("%+v %v", found, err)
	}
}

func TestHiddenThreadEntriesStayWithinTheResultBound(t *testing.T) {
	f := newFixture(t)
	f.native("cc:n", "n")
	f.session("cc:a", "a", alpha, StateIdle)
	first := f.send("cc:n", SendInput{To: "a", Message: strings.Repeat("x", MaxBodyBytes)})
	f.clock.advance(time.Second)
	f.send("cc:n", SendInput{To: "a", Message: strings.Repeat("y", 14200), ReplyTo: first.MessageID})
	for index := range 30 { // under the loop count of 40 for the pair
		f.clock.advance(3 * time.Second)
		f.send("cc:a", SendInput{To: "n", Message: "hidden " + strings.Repeat("z", index), ReplyTo: first.MessageID})
	}
	input := ThreadInput{ThreadID: first.ThreadID, Limit: 100}
	for pages := 0; ; pages++ {
		page, err := f.store.Thread(f.ctx, "cc:n", input)
		if err != nil {
			t.Fatal(err)
		}
		if size := len(marshalCompact(page)); size > ResultBound {
			t.Fatalf("page %d takes %d bytes", pages, size)
		}
		if page.NextCursor == "" || pages > 10 {
			break
		}
		input.Cursor = page.NextCursor
	}
}

func TestOperationsBesideSendsRunTheDueSweep(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", NotifyWhenIdle: true})
	f.clock.advance(13 * time.Hour)
	if _, err := f.store.Observe(f.ctx, "cc:a", ObserveBusy, false); err != nil {
		t.Fatal(err)
	}
	if notices := f.record("cc:a").Notices; len(notices) != 1 || notices[0].Kind != NoticeSubscriptionExpired {
		t.Fatalf("%+v", notices)
	}
}

func TestAFullStoreRemovesDoneMessagesToMakeRoom(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	done := f.send("cc:a", SendInput{To: "b", Message: "read already"})
	f.inbox("cc:b", unread())
	f.clock.advance(time.Minute)
	f.store.writeLimit = f.measured().Bytes + 1000 // room for nothing new until the done one goes
	f.send("cc:a", SendInput{To: "b", Message: "fits once the done message is removed"})
	if exists(f.store.place.commit(done.MessageID)) {
		t.Fatal("pressure removed the done message")
	}
}
func TestARepeatedIDReturnsTheBodyOnce(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.native("cc:n", "n")
	sent := f.send("cc:a", SendInput{To: "n", Message: "named twice"})
	result := f.inbox("cc:n", InboxInput{IDs: []string{sent.MessageID, sent.MessageID}, UnreadOnly: true, MarkRead: true})
	if len(result.Messages) != 1 {
		t.Fatalf("%+v", result.Messages)
	}
}
func TestAThreadShowsARequestClosedByItsDeadline(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	asked := f.send("cc:a", SendInput{To: "b", Message: "decide", Request: &RequestInput{Expects: "decision"}, ExpiresInSeconds: 60})
	f.inbox("cc:b", unread())
	f.clock.advance(time.Minute)
	for _, key := range []string{"cc:a", "cc:b"} {
		thread, err := f.store.Thread(f.ctx, key, ThreadInput{ThreadID: asked.ThreadID})
		if err != nil || len(thread.Messages) != 1 || thread.Messages[0].Request.State != "expired" {
			t.Fatalf("%s: %+v %v", key, thread, err)
		}
	}
}

func TestAClosureCutShortStillEndsItsSubscriptionOnce(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	asked := f.send("cc:a", SendInput{To: "b", Message: "decide", Request: &RequestInput{Expects: "decision"}, NotifyWhenIdle: true})
	commit := f.commit(asked.MessageID)
	commit.RequestState, commit.Notice = "expired", "owed" // the delivery's write never happened
	if err := writeRecord(f.store.place.commit(asked.MessageID), commit, commitBound); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: asked.RequestID, Decision: "approve"})
	f.refused(err, ReasonRequestClosed)
	notices := f.record("cc:a").Notices
	if len(notices) != 1 || notices[0].Kind != NoticeNotDelivered || len(f.record("cc:b").IdleWatchers) != 0 {
		t.Fatalf("%+v", notices)
	}
}

func TestAnEndCutShortOwesOneOutcomeForTheSubscription(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	sent := f.send("cc:a", SendInput{To: "b", Message: "tell me when done", NotifyWhenIdle: true})
	// The end dropped the delivery and owed not_delivered, then stopped before it removed the
	// subscription.
	f.setDelivery(sent.MessageID, "cc:b", func(delivery *Delivery) {
		delivery.State, delivery.Reason, delivery.Notice = "dropped", "recipient_ended", "owed"
	})
	if err := os.Remove(filepath.Join(f.store.place.inbox(hashKey("cc:b")), sent.MessageID)); err != nil {
		t.Fatal(err)
	}
	recipient := f.record("cc:b")
	watcher := f.record("cc:a")
	watcher.Notices = append(watcher.Notices, SessionNotice{ID: subscriptionID(recipient, recipient.IdleWatchers[0]), Kind: NoticeNotDelivered,
		About: "[" + f.record("cc:b").Ref + "]", MessageID: sent.MessageID, At: stamp(f.clock.at), State: "owed"})
	if err := writeRecord(f.store.place.session(hashKey("cc:a")), watcher, sessionBound); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveGone, false); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, notice := range f.record("cc:a").Notices {
		kinds[notice.Kind]++
	}
	if kinds[NoticeNotDelivered] != 1 || kinds[NoticeEnded] != 0 || len(f.record("cc:b").IdleWatchers) != 0 {
		t.Fatalf("%v", kinds)
	}
}

func TestAnEndDropsWhatWaitsBehindADeliveredMessage(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	older := f.send("cc:a", SendInput{To: "b", Message: "older"})
	f.clock.advance(time.Second)
	newer := f.send("cc:a", SendInput{To: "b", Message: "newer"})
	f.setDelivery(older.MessageID, "cc:b", func(delivery *Delivery) { delivery.State = "delivered" })
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveGone, false); err != nil {
		t.Fatal(err)
	}
	if delivery := f.delivery(newer.MessageID, "cc:b"); delivery.State != "dropped" {
		t.Fatalf("%+v", delivery)
	}
}

func TestAClosureCutShortAfterItsDeliveryStillEndsItsSubscription(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	asked := f.send("cc:a", SendInput{To: "b", Message: "decide", Request: &RequestInput{Expects: "decision"}, NotifyWhenIdle: true})
	commit := f.commit(asked.MessageID)
	commit.RequestState, commit.Notice = "expired", "owed"
	if err := writeRecord(f.store.place.commit(asked.MessageID), commit, commitBound); err != nil {
		t.Fatal(err)
	}
	f.setDelivery(asked.MessageID, "cc:b", func(delivery *Delivery) { delivery.State, delivery.Reason = "expired", "expired" })
	_, err := f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: asked.RequestID, Decision: "approve"})
	f.refused(err, ReasonRequestClosed)
	if notices := f.record("cc:a").Notices; len(notices) != 1 || notices[0].Kind != NoticeNotDelivered {
		t.Fatalf("%+v", notices)
	}
}

func TestASubscriptionHasOneOutcomeWhateverCrashesBetween(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", Message: "tell me when done", NotifyWhenIdle: true})
	// Its expiry was written, then the process stopped before removing the subscription.
	recipient := f.record("cc:b")
	watcher := f.record("cc:a")
	watcher.Notices = append(watcher.Notices, SessionNotice{ID: subscriptionID(recipient, recipient.IdleWatchers[0]),
		Kind: NoticeSubscriptionExpired, About: "[" + recipient.Ref + "]", At: stamp(f.clock.at), State: "owed"})
	if err := writeRecord(f.store.place.session(hashKey("cc:a")), watcher, sessionBound); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveGone, false); err != nil {
		t.Fatal(err)
	}
	if notices := f.record("cc:a").Notices; len(notices) != 1 {
		t.Fatalf("one outcome: %+v", notices)
	}
}

func TestAFiringCutShortIsFinishedBeforeAnythingElse(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", NotifyWhenIdle: true})
	recipient := f.record("cc:b")
	id := subscriptionID(recipient, recipient.IdleWatchers[0])
	fire := firing{Recipient: hashKey("cc:b"), Watcher: "cc:a", Subscription: id,
		Notice: SessionNotice{ID: id, Kind: NoticeIdle, About: "[" + recipient.Ref + "]"}}
	// The process wrote the entry and the notice, then stopped before removing the subscription.
	if err := f.store.update(f.ctx, func(transaction *transaction) error {
		if err := transaction.publishJournal(journalEntry{Op: opFire, ID: "fire-test", Firing: &fire}); err != nil {
			return err
		}
		return transaction.owe("cc:a", fire.Notice)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveGone, false); err != nil {
		t.Fatal(err)
	}
	if notices := f.record("cc:a").Notices; len(notices) != 1 || notices[0].Kind != NoticeIdle || len(f.record("cc:b").IdleWatchers) != 0 {
		t.Fatalf("%+v", notices)
	}
}

func TestANewSubscriptionSeesAClosureCutShort(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	asked := f.send("cc:a", SendInput{To: "b", Message: "decide", Request: &RequestInput{Expects: "decision"}, NotifyWhenIdle: true})
	commit := f.commit(asked.MessageID)
	commit.RequestState, commit.Notice = "expired", "owed" // the delivery's write never happened
	if err := writeRecord(f.store.place.commit(asked.MessageID), commit, commitBound); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(time.Second)
	f.send("cc:a", SendInput{To: "b", Message: "another", NotifyWhenIdle: true})
	kinds := map[string]int{}
	for _, notice := range f.record("cc:a").Notices {
		kinds[notice.Kind]++
	}
	if kinds[NoticeNotDelivered] != 1 {
		t.Fatalf("the replaced subscription has its outcome: %v", kinds)
	}
}

func TestAnIdleCutShortStillFiresBeforeTheNextObservation(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", NotifyWhenIdle: true})
	// The idle was journalled and written, then the hook stopped before it fired anything.
	if err := f.store.update(f.ctx, func(transaction *transaction) error {
		session, err := transaction.readSession(hashKey("cc:b"))
		if err != nil {
			return err
		}
		session.State, session.IdleGeneration, session.IdleAt = StateIdle, session.IdleGeneration+1, stamp(transaction.now)
		entry := journalEntry{Op: opObserve, ID: "observe-test", Observed: &observation{Session: hashKey("cc:b"),
			State: StateIdle, Since: stamp(transaction.now), Generation: session.IdleGeneration, IdleAt: session.IdleAt}}
		if err := transaction.publishJournal(entry); err != nil {
			return err
		}
		return transaction.writeSession(session)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveBusy, false); err != nil {
		t.Fatal(err)
	}
	if notices := f.record("cc:a").Notices; len(notices) != 1 || notices[0].Kind != NoticeIdle {
		t.Fatalf("the idle fired before busy overwrote it: %+v", notices)
	}
}

func TestASubscriptionPastItsUntilExpiresEvenIfAnIdleComesLater(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", NotifyWhenIdle: true})
	f.clock.advance(12*time.Hour + time.Second)
	// No operation ran in between, so the first to see the expiry is the idle itself.
	if err := writeRecord(f.store.place.swept(), sweptRecord{V: 1, At: stamp(f.clock.at)}, 256); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveIdle, false); err != nil {
		t.Fatal(err)
	}
	if notices := f.record("cc:a").Notices; len(notices) != 1 || notices[0].Kind != NoticeSubscriptionExpired {
		t.Fatalf("%+v", notices)
	}
}

func TestADeliveryThatCannotBeReadKeepsItsMessage(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	sent := f.send("cc:a", SendInput{To: "b", Message: "unread"})
	path := f.store.place.delivery(sent.MessageID, hashKey("cc:b"))
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil { // read, but not a record
		t.Fatal(err)
	}
	if err := f.store.update(f.ctx, func(transaction *transaction) error {
		found, err := transaction.message(sent.MessageID)
		if err != nil {
			return err
		}
		if transaction.done(found) {
			t.Error("an unreadable delivery counted as done")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestASendersInboxShowsAnExpiryTheSweepHasNotSeen(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	f.send("cc:a", SendInput{To: "b", Message: "decide", Request: &RequestInput{Expects: "decision"}, ExpiresInSeconds: 60})
	f.clock.advance(time.Minute)
	if err := writeRecord(f.store.place.swept(), sweptRecord{V: 1, At: stamp(f.clock.at)}, 256); err != nil {
		t.Fatal(err)
	}
	if notices := f.inbox("cc:a", unread()).Notices; len(notices) != 1 || !strings.Contains(notices[0], "expired unanswered") {
		t.Fatalf("%v", notices)
	}
}

func TestARepeatedAckKeepsTheNote(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	sent := f.send("cc:a", SendInput{To: "b", Message: "read it"})
	if _, err := f.store.Ack(f.ctx, "cc:b", sent.MessageID, "read", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Ack(f.ctx, "cc:b", sent.MessageID, "read", "second"); err != nil {
		t.Fatal(err)
	}
	if note := f.delivery(sent.MessageID, "cc:b").Note; note != "first" {
		t.Fatalf("note %q", note)
	}
}

func TestAnIdleBeforeTheDeadlineFiresEvenWhenRecoveredAfterIt(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", NotifyWhenIdle: true})
	f.clock.advance(12*time.Hour - time.Minute)
	if err := f.store.update(f.ctx, func(transaction *transaction) error {
		session, err := transaction.readSession(hashKey("cc:b"))
		if err != nil {
			return err
		}
		session.State, session.Since, session.IdleGeneration = StateIdle, stamp(transaction.now), session.IdleGeneration+1
		session.IdleAt = session.Since
		entry := journalEntry{Op: opObserve, ID: "observe-test", Observed: &observation{Session: hashKey("cc:b"),
			State: StateIdle, Since: session.Since, Generation: session.IdleGeneration, IdleAt: session.IdleAt}}
		if err := transaction.publishJournal(entry); err != nil {
			return err
		}
		return transaction.writeSession(session)
	}); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(2 * time.Minute) // recovery runs past the subscription's until
	if _, err := f.store.Sessions(f.ctx); err != nil {
		t.Fatal(err)
	}
	if notices := f.record("cc:a").Notices; len(notices) != 1 || notices[0].Kind != NoticeIdle {
		t.Fatalf("the idle came first: %+v", notices)
	}
}

func TestAWatcherRecordThatCannotBeReadKeepsTheFiring(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", NotifyWhenIdle: true})
	watcher := f.store.place.session(hashKey("cc:a"))
	if err := os.Chmod(watcher, 0o000); err != nil {
		t.Fatal(err)
	}
	_, observeErr := f.store.Observe(f.ctx, "cc:b", ObserveIdle, false)
	if err := os.Chmod(watcher, 0o600); err != nil {
		t.Fatal(err)
	}
	if observeErr == nil {
		t.Skip("this account reads a file without permission (root)")
	}
	if _, err := f.store.Sessions(f.ctx); err != nil {
		t.Fatal(err)
	}
	if notices := f.record("cc:a").Notices; len(notices) != 1 || notices[0].Kind != NoticeIdle {
		t.Fatalf("recovery wrote the notice it owed: %+v", notices)
	}
}

func TestABodyThatFailsToLoadMarksNothingRead(t *testing.T) {
	f := newFixture(t)
	f.session("cc:b", "b", alpha, StateIdle)
	f.session("cc:s1", "s1", alpha, StateIdle)
	f.session("cc:s2", "s2", alpha, StateIdle)
	first := f.send("cc:s1", SendInput{To: "b", Message: "first"})
	f.clock.advance(time.Second)
	second := f.send("cc:s2", SendInput{To: "b", Message: "second"})
	body := f.store.place.body(second.MessageID)
	if err := os.Chmod(body, 0o000); err != nil {
		t.Fatal(err)
	}
	_, inboxErr := f.store.Inbox(f.ctx, "cc:b", unread())
	if err := os.Chmod(body, 0o600); err != nil {
		t.Fatal(err)
	}
	if inboxErr == nil {
		t.Skip("this account reads a file without permission (root)")
	}
	if f.delivery(first.MessageID, "cc:b").State != "pending" {
		t.Fatal("a failed page marked an earlier message read")
	}
}

func TestANoticeThatCannotBeMarkedIsNotShown(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	sent := f.send("cc:a", SendInput{To: "b", Message: "short-lived", ExpiresInSeconds: 60})
	f.clock.advance(time.Minute)
	if err := f.store.Sweep(f.ctx); err != nil {
		t.Fatal(err)
	}
	folder := f.store.place.deliveries(sent.MessageID)
	if err := os.Chmod(folder, 0o500); err != nil {
		t.Fatal(err)
	}
	result, err := f.store.Inbox(f.ctx, "cc:a", unread())
	if chmodErr := os.Chmod(folder, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Notices) != 0 || f.delivery(sent.MessageID, "cc:b").Notice != "owed" {
		t.Skipf("this account writes a read-only folder (root): %+v", result)
	}
	if again := f.inbox("cc:a", unread()); len(again.Notices) != 1 {
		t.Fatalf("the notice is shown once it can be marked: %+v", again)
	}
}

func TestAnAckThatTheDeadlineOvertakesSetsNoNote(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	sent := f.send("cc:a", SendInput{To: "b", Message: "too late", ExpiresInSeconds: 60})
	f.clock.advance(time.Minute)
	result, err := f.store.Ack(f.ctx, "cc:b", sent.MessageID, "read", "late note")
	if err != nil || result.Changed || result.State != "expired" || f.delivery(sent.MessageID, "cc:b").Note != "" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestARepeatedIdleAfterTheDeadlineDoesNotFire(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	sent := f.send("cc:a", SendInput{To: "b", Message: "tell me", NotifyWhenIdle: true})
	f.inbox("cc:b", unread()) // armed while b is idle
	_ = sent
	f.clock.advance(12*time.Hour + time.Second)
	if err := writeRecord(f.store.place.swept(), sweptRecord{V: 1, At: stamp(f.clock.at)}, 256); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveIdle, false); err != nil {
		t.Fatal(err)
	}
	if notices := f.record("cc:a").Notices; len(notices) != 1 || notices[0].Kind != NoticeSubscriptionExpired {
		t.Fatalf("an idle reported after until counts for nothing: %+v", notices)
	}
}

func TestAnEndAfterTheDeadlineIsAnExpiry(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", Message: "still pending", NotifyWhenIdle: true})
	f.clock.advance(12*time.Hour + time.Second)
	if err := writeRecord(f.store.place.swept(), sweptRecord{V: 1, At: stamp(f.clock.at)}, 256); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveGone, false); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, notice := range f.record("cc:a").Notices {
		kinds[notice.Kind]++
	}
	if kinds[NoticeSubscriptionExpired] != 1 || kinds[NoticeNotDelivered] != 0 {
		t.Fatalf("%v", kinds)
	}
}

func TestAnOpenMailboxKeepsItsMessagesPastTheirRetention(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	if _, err := f.store.Register(f.ctx, Registration{Key: "cx:round", KeySource: SourceEngine, Engine: EngineCodex,
		Name: "review:task/r1/codex", Channel: alpha, Mailbox: true, State: StateIdle}); err != nil {
		t.Fatal(err)
	}
	sent := f.send("cc:a", SendInput{To: "review:task/r1/codex", Message: "a note for the next round", ExpiresInSeconds: 60})
	f.clock.advance(8 * 24 * time.Hour)
	if err := f.store.Sweep(f.ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(f.store.place.commit(sent.MessageID)) {
		t.Fatal("retention removed what the next round must read")
	}
}

func TestAWatchedDeliveryThatCannotBeReadLeavesItsSubscriptionWaiting(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	sent := f.send("cc:a", SendInput{To: "b", Message: "watched", NotifyWhenIdle: true})
	path := f.store.place.delivery(sent.MessageID, hashKey("cc:b"))
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	_, observeErr := f.store.Observe(f.ctx, "cc:b", ObserveIdle, false)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if observeErr == nil {
		t.Skip("this account reads a file without permission (root)")
	}
	if len(f.record("cc:a").Notices) != 0 || len(f.record("cc:b").IdleWatchers) != 1 {
		t.Fatalf("no outcome from a read that failed: %+v", f.record("cc:a").Notices)
	}
}

func TestShowingASessionNoticeKeepsAnArmingFromTheSameRead(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	f.session("cc:c", "c", alpha, StateIdle)
	// b is owed a session notice (c is idle), and a's subscription waits on b reading a's message.
	f.send("cc:b", SendInput{To: "c", NotifyWhenIdle: true})
	f.clock.advance(time.Second)
	f.send("cc:a", SendInput{To: "b", Message: "tell me when idle", NotifyWhenIdle: true})
	result := f.inbox("cc:b", unread())
	if len(result.Messages) != 1 || len(result.Notices) != 1 {
		t.Fatalf("%+v", result)
	}
	if watchers := f.record("cc:b").IdleWatchers; len(watchers) != 1 || !watchers[0].Armed {
		t.Fatalf("the arming survives the notice's write: %+v", watchers)
	}
}

func TestAnEndReplayedAfterTheDeadlineDecidesAsTheEndWould(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	f.send("cc:a", SendInput{To: "b", Message: "never read", NotifyWhenIdle: true})
	f.clock.advance(12*time.Hour - time.Minute)
	if err := f.store.update(f.ctx, func(transaction *transaction) error {
		session, err := transaction.readSession(hashKey("cc:b"))
		if err != nil {
			return err
		}
		session.State, session.Since = StateGone, stamp(transaction.now)
		entry := journalEntry{Op: opObserve, ID: "observe-end", Observed: &observation{Session: hashKey("cc:b"),
			State: StateGone, Since: session.Since, Generation: session.IdleGeneration}}
		if err := transaction.publishJournal(entry); err != nil {
			return err
		}
		return transaction.writeSession(session)
	}); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(2 * time.Minute)
	if _, err := f.store.Sessions(f.ctx); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, notice := range f.record("cc:a").Notices {
		kinds[notice.Kind]++
	}
	if kinds[NoticeNotDelivered] != 1 || kinds[NoticeSubscriptionExpired] != 0 {
		t.Fatalf("%v", kinds)
	}
}

func TestAnUnreadableMailboxRecordHoldsItsMessages(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	if _, err := f.store.Register(f.ctx, Registration{Key: "cx:round", KeySource: SourceEngine, Engine: EngineCodex,
		Name: "review:task/r1/codex", Channel: alpha, Mailbox: true, State: StateIdle}); err != nil {
		t.Fatal(err)
	}
	sent := f.send("cc:a", SendInput{To: "review:task/r1/codex", Message: "for the next round", ExpiresInSeconds: 60})
	f.clock.advance(2 * time.Minute)
	if err := f.store.Sweep(f.ctx); err != nil { // the expiry is settled while the mailbox reads
		t.Fatal(err)
	}
	f.clock.advance(8 * 24 * time.Hour)
	held := false
	if err := f.store.update(f.ctx, func(transaction *transaction) error {
		found, err := transaction.message(sent.MessageID)
		if err != nil {
			return err
		}
		mailbox := transaction.place.session(hashKey("cx:round"))
		if err := os.Chmod(mailbox, 0o000); err != nil {
			return err
		}
		held = transaction.heldByMailbox(found.envelope)
		return os.Chmod(mailbox, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("a mailbox record it cannot read does not hold its message")
	}
}

func TestAReceiptCutShortBeforeItsArmingStillFiresOnTheNextIdle(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateBusy)
	sent := f.send("cc:a", SendInput{To: "b", Message: "tell me when done", NotifyWhenIdle: true})
	// The read was written, then the process was killed before the subscription was armed.
	f.setDelivery(sent.MessageID, "cc:b", func(delivery *Delivery) { delivery.State = "read" })
	if _, err := f.store.Observe(f.ctx, "cc:b", ObserveIdle, false); err != nil {
		t.Fatal(err)
	}
	if notices := f.record("cc:a").Notices; len(notices) != 1 || notices[0].Kind != NoticeIdle {
		t.Fatalf("the first idle after the read fires: %+v", notices)
	}
}

func TestTheSweepFinishesARemovalStoppedAfterItsUnlink(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	sent := f.send("cc:a", SendInput{To: "b", Message: "half removed"})
	if err := os.Remove(f.store.place.commit(sent.MessageID)); err != nil { // stopped right after the unlink
		t.Fatal(err)
	}
	before := f.measured().Bytes
	if err := f.store.Sweep(f.ctx); err != nil {
		t.Fatal(err)
	}
	if exists(f.store.place.envelope(sent.MessageID)) || exists(f.store.place.body(sent.MessageID)) {
		t.Fatal("the leftover files are removed")
	}
	if after := f.measured().Bytes; after >= before {
		t.Fatalf("their charge is freed: %d then %d", before, after)
	}
}
