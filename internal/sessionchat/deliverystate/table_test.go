package deliverystate

import (
	"fmt"
	"testing"
)

// Every delivery the table can meet, and every event: Apply moves only along the table, leaves a
// delivery unchanged when it refuses, never leaves a final state, and every edge of the table is
// reached by some input.
func TestApplyFollowsTheTableExactly(t *testing.T) {
	reasons := []Reason{NoReason, Unconfirmed}
	kinds := []Kind{Claim, Announce, Answer, Failure, Stale, Expire, End, ReadWhole, AckRead, AckActed, Confirm, Freeze, CloseRequest}
	answers := []EngineAnswer{Accepted, HeldBy, RefusedBy, QueueFull, Lost}
	namings := []Naming{ByNothing, ByID, ByHandle}
	reached := map[Edge]bool{}
	inputs := 0
	for _, route := range Routes {
		for _, state := range States {
			for _, reason := range reasons {
				for _, request := range []bool{false, true} {
					for attempts := 0; attempts <= MaxAttempts; attempts++ {
						for _, owner := range []string{"", "pass-1"} {
							delivery := Delivery{Route: route, State: state, Reason: reason, Request: request, Attempts: attempts, Pusher: owner}
							for _, kind := range kinds {
								for _, answer := range answers {
									for _, overdue := range []bool{false, true} {
										for _, named := range namings {
											event := Event{Kind: kind, Pusher: "pass-1", Answer: answer, Overdue: overdue, Named: named}
											inputs++
											next, err := applyNoPanic(t, delivery, event)
											if err != nil {
												if next != delivery {
													t.Fatalf("%+v %+v: refused but changed to %+v", delivery, event, next)
												}
												continue
											}
											if Final(state) && next.State != state {
												t.Fatalf("%+v %+v: left the final state for %s", delivery, event, next.State)
											}
											if next.State != state {
												reached[Edge{state, next.State, kind}] = true
											}
											if next.Route != route && (route != Native || next.Route != Hooks || kind != Failure) {
												t.Fatalf("%+v %+v: route changed to %s", delivery, event, next.Route)
											}
											if (next.State == Refused || next.State == Dropped || next.State == Expired && !request) &&
												state != next.State && next.Notice != NoticeOwed {
												t.Fatalf("%+v %+v: became %s without an owed notice", delivery, event, next.State)
											}
											if next.State == Expired && request && next.Notice != delivery.Notice {
												t.Fatalf("%+v %+v: a request's delivery owes its own expiry notice", delivery, event)
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	for _, edge := range Table {
		if !reached[edge] {
			t.Errorf("no input reaches %s -[%s]-> %s", edge.From, edge.Kind, edge.To)
		}
	}
	t.Logf("%d inputs, %d edges of the table reached", inputs, len(reached))
}

func applyNoPanic(t *testing.T, delivery Delivery, event Event) (next Delivery, err error) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("%+v %+v: %v", delivery, event, recovered)
		}
	}()
	return Apply(delivery, event)
}

func TestAPassRecordsOnlyWhatItOwns(t *testing.T) {
	claimed, err := Apply(Delivery{Route: Native, State: Pending}, Event{Kind: Claim, Pusher: "pass-1"})
	if err != nil || claimed.State != Sending || claimed.Pusher != "pass-1" {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	swept, _ := Apply(claimed, Event{Kind: Stale})
	late, err := Apply(swept, Event{Kind: Answer, Pusher: "pass-1", Answer: RefusedBy})
	if err != ErrSuperseded || late != swept {
		t.Fatalf("a late answer after the sweep: %+v %v", late, err)
	}
	other, err := Apply(claimed, Event{Kind: Answer, Pusher: "pass-2", Answer: Accepted})
	if err != ErrSuperseded || other != claimed {
		t.Fatalf("another pass's answer: %+v %v", other, err)
	}
}

func TestTheFourthConfirmedFailureMovesTheRouteToHooks(t *testing.T) {
	for _, route := range []Route{Native, Bridge} {
		delivery := Delivery{Route: route, State: Pending}
		for attempt := 1; attempt <= MaxAttempts; attempt++ {
			claimed, err := Apply(delivery, Event{Kind: Claim, Pusher: "pass-1"})
			if err != nil {
				t.Fatalf("%s attempt %d: %v", route, attempt, err)
			}
			delivery, _ = Apply(claimed, Event{Kind: Failure, Pusher: "pass-1"})
		}
		want := map[Route]Route{Native: Hooks, Bridge: Bridge}[route]
		if delivery.Route != want || delivery.State != Pending || Due(delivery) {
			t.Fatalf("%s after %d failures: %+v", route, MaxAttempts, delivery)
		}
	}
}

func TestOnlyTheHandleNamesANativeDeliveryTheEngineHasNotShown(t *testing.T) {
	for _, state := range []State{Pending, Held} {
		delivery := Delivery{Route: Native, State: state}
		if _, err := Apply(delivery, Event{Kind: AckRead, Named: ByID}); err != ErrNotVisible {
			t.Errorf("%s: a bare ID acked it: %v", state, err)
		}
	}
	unconfirmed := Delivery{Route: Native, State: Delivered, Reason: Unconfirmed}
	if _, err := Apply(unconfirmed, Event{Kind: Confirm, Named: ByID}); err != ErrNotVisible {
		t.Errorf("a bare ID confirmed an unconfirmed push: %v", err)
	}
	confirmed, err := Apply(unconfirmed, Event{Kind: Confirm, Named: ByHandle})
	if err != nil || confirmed.State != Delivered || confirmed.Reason != NoReason {
		t.Errorf("the handle did not confirm it: %+v %v", confirmed, err)
	}
	if _, err := Apply(Delivery{Route: Poll, State: Pending}, Event{Kind: AckRead, Named: ByID}); err != nil {
		t.Errorf("a poll delivery needs no handle: %v", err)
	}
}

func TestARequestAndItsDeliveryExpireWithOneNotice(t *testing.T) {
	request := Request{State: Open}
	delivery := Delivery{Route: Native, State: Pending, Request: true}
	closedRequest, expired, err := ApplyMessage(request, delivery, Event{Kind: Claim, Pusher: "pass-1", Overdue: true})
	if err != nil || closedRequest.State != Closed || closedRequest.Notice != NoticeOwed ||
		expired.State != Expired || expired.Notice != NoNotice {
		t.Fatalf("got %+v %+v %v", closedRequest, expired, err)
	}
	if _, _, err := Respond(closedRequest, expired, false, ByHandle); err != ErrRequestClosed {
		t.Fatalf("an answer after the deadline: %v", err)
	}
}

func TestAClosedRequestIsNotPushedAgainAfterAFailure(t *testing.T) {
	request := Request{State: Open}
	delivery, _ := Apply(Delivery{Route: Native, State: Pending, Request: true}, Event{Kind: Claim, Pusher: "pass-1"})
	request, delivery, err := ExpireRequest(request, delivery) // the requesting mailbox froze
	if err != nil || delivery.State != Sending {
		t.Fatalf("freeze: %+v %v", delivery, err)
	}
	_, delivery, err = ApplyMessage(request, delivery, Event{Kind: Failure, Pusher: "pass-1"})
	if err != nil || delivery.State != Expired || Due(delivery) {
		t.Fatalf("after the failure: %+v %v", delivery, err)
	}
}

func TestNoticesAreShownOnce(t *testing.T) {
	sending, _ := ClaimNotice(NoticeOwed)
	if again, err := ClaimNotice(sending); err == nil {
		t.Fatalf("claimed twice: %s", again)
	}
	if back, _ := SettleNotice(sending, true); back != NoticeOwed {
		t.Fatalf("a confirmed failure: %s", back)
	}
	if shown, _ := SettleNotice(sending, false); shown != NoticeShown {
		t.Fatalf("an answer: %s", shown)
	}
	if _, err := ShowNotice(NoticeShown); err == nil {
		t.Fatal("shown twice")
	}
}

func TestADeliveryKeepsItsMessageWhileItMayStillBeRead(t *testing.T) {
	cases := []struct {
		delivery Delivery
		whole    bool
		want     bool
	}{
		{Delivery{Route: Native, State: Delivered}, true, false},
		{Delivery{Route: Native, State: Delivered}, false, true},
		{Delivery{Route: Native, State: Delivered, Reason: Unconfirmed}, true, true},
		{Delivery{Route: Hooks, State: Delivered}, true, true},
		{Delivery{Route: Native, State: Refused, Notice: NoticeOwed}, true, true},
		{Delivery{Route: Native, State: Refused, Notice: NoticeShown}, true, false},
		{Delivery{Route: Poll, State: Read}, true, false},
	}
	for _, test := range cases {
		if got := NeedsMessage(test.delivery, test.whole); got != test.want {
			t.Errorf("%s: NeedsMessage(%+v, whole %v) = %v", fmt.Sprint(test.delivery.State), test.delivery, test.whole, got)
		}
	}
}

func TestAResponseToAGoneRequesterOwesItsNotice(t *testing.T) {
	created := New(Native, false, true)
	if created.State != Dropped || created.Reason != RecipientEnded || created.Notice != NoticeOwed {
		t.Fatalf("got %+v", created)
	}
	if fresh := New(Hooks, true, false); fresh.State != Pending || fresh.Notice != NoNotice || !fresh.Request {
		t.Fatalf("got %+v", fresh)
	}
}
