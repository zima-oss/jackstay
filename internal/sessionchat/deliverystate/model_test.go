package deliverystate

// The delivery model of docs/design/chat-model/ChatDelivery.tla, run over this package's table: every
// interleaving of one delivery's pushers (two passes, either of which may pause or crash at any step),
// the recipient's hook, its reads, acks and answers, the sweep, the deadline, the recipient's end and
// a review mailbox's freeze, explored breadth first to the last reachable state. A variant replays a
// rule of the spec as it stood before the model (section 4.10's bare ID, section 4.4's two expiry
// notices, section 9.1's freeze) and must find the counterexample the fix removed.

import (
	"fmt"
	"strings"
	"testing"
)

// variant selects the rules the explored world follows.
type variant struct {
	bareIDIsEvidence      bool // as written: naming the bare message ID proves the engine showed it
	separateRequestExpiry bool // as written: a request and its delivery expire with a notice each
	freezeLeavesDelivery  bool // as written: a freeze closes the mailbox's requests, not their deliveries
	deadlineSkipsRequest  bool // as written: a pass's or reader's deadline check expires the delivery only
	failureReopens        bool // as written: a confirmed failure returns a closed request's delivery to pending
}

type passState struct {
	pc    string // idle, claimed, written, failed, dead
	reply EngineAnswer
}

type world struct {
	delivery        Delivery
	request         Request
	isRequest       bool
	fromMailbox     bool
	engine          string // what the engine really did: none, accepted, held, refused, full, dropped
	passes          [2]passState
	hook            string // idle, announced: recorded `delivered`, text not yet returned
	shown           int    // times the text reached the recipient's model by a push
	handleKnown     bool   // a frame the engine showed carried the handle and the request ID
	idKnown         bool   // the bare ID reached the recipient (another message's `thread`, `reply_to`)
	exposed         bool   // a read returned a native body the engine never accepted
	pushedClose     bool   // a request was pushed after it closed
	overdue         bool
	alive           bool
	deliveryNotices int
	requestNotices  int
}

type step struct {
	name string
	next world
}

var passNames = [2]string{"pass-1", "pass-2"}

func initialWorlds(rules variant) []world {
	var worlds []world
	for _, route := range Routes {
		for _, isRequest := range []bool{false, true} {
			for _, fromMailbox := range []bool{false, true} {
				if fromMailbox && !isRequest || route == Mailbox && isRequest {
					continue
				}
				start := world{
					delivery:    Delivery{Route: route, State: Pending, Request: isRequest && !rules.separateRequestExpiry},
					isRequest:   isRequest,
					fromMailbox: fromMailbox,
					engine:      "none",
					hook:        "idle",
					alive:       true,
				}
				start.passes[0].pc, start.passes[1].pc = "idle", "idle"
				if isRequest {
					start.request = Request{State: Open}
				}
				worlds = append(worlds, start)
			}
		}
	}
	return worlds
}

// naming is how the recipient can name the delivery now, under the variant's rules.
func (w world) naming(rules variant) (Naming, bool) {
	switch {
	case w.handleKnown:
		return ByHandle, true
	case w.idKnown && rules.bareIDIsEvidence:
		return ByHandle, true // the old rule: a bare ID counted as a frame's evidence
	case w.idKnown:
		return ByID, true
	}
	return ByNothing, false
}

// read records what a read returned: a native body the engine never accepted is an exposure.
func (w *world) read() {
	if w.delivery.Route == Native && w.engine != "accepted" {
		w.exposed = true
	}
}

func (w *world) setDelivery(next Delivery) {
	if w.delivery.Notice == NoNotice && next.Notice == NoticeOwed {
		w.deliveryNotices++
	}
	w.delivery = next
}

func (w *world) setRequest(next Request) {
	if w.request.Notice == NoNotice && next.Notice == NoticeOwed {
		w.requestNotices++
	}
	w.request = next
}

// apply runs one event through the table, with the message's request unless the variant expires
// the two apart.
func (w world) apply(event Event, rules variant) (world, bool) {
	next := w
	plain := rules.separateRequestExpiry || !w.isRequest ||
		rules.deadlineSkipsRequest && checksDeadline(event.Kind) ||
		rules.failureReopens && (event.Kind == Failure || event.Kind == Answer)
	if plain {
		delivery, err := Apply(w.delivery, event)
		if err != nil {
			return w, false
		}
		next.setDelivery(delivery)
		return next, true
	}
	request, delivery, err := ApplyMessage(w.request, w.delivery, event)
	if err != nil {
		return w, false
	}
	next.setRequest(request)
	next.setDelivery(delivery)
	return next, true
}

func successors(w world, rules variant) []step {
	var steps []step
	add := func(name string, next world) { steps = append(steps, step{name, next}) }
	byPusher := w.delivery.Route == Native || w.delivery.Route == Bridge

	// The passes of the detached pusher, or the bridge (sections 8.3 and 9.2).
	for index := range w.passes {
		pass := w.passes[index]
		name := passNames[index]
		if byPusher && pass.pc == "idle" {
			if claimed, ok := w.apply(Event{Kind: Claim, Pusher: name, Overdue: w.overdue}, rules); ok {
				if claimed.delivery.State == Sending {
					claimed.passes[index].pc = "claimed"
					if w.isRequest && w.request.State != Open {
						claimed.pushedClose = true
					}
				}
				add(name+" claims", claimed)
			}
		}
		switch pass.pc {
		case "claimed":
			failed := w
			failed.passes[index].pc = "failed"
			add(name+" fails before writing", failed)
			answers := []EngineAnswer{Accepted, HeldBy, RefusedBy, QueueFull}
			if w.delivery.Route == Bridge {
				answers = []EngineAnswer{Accepted}
			}
			for _, answer := range answers {
				written := w
				written.passes[index] = passState{pc: "written", reply: answer}
				switch answer {
				case Accepted:
					written.engine, written.shown, written.handleKnown = "accepted", w.shown+1, true
				case HeldBy:
					written.engine = "held"
				case RefusedBy:
					written.engine = "refused"
				case QueueFull:
					written.engine = "full"
				}
				add(fmt.Sprintf("%s writes, engine %s", name, answer), written)
			}
		case "written":
			for _, answer := range []EngineAnswer{pass.reply, Lost} {
				recorded := w
				if next, ok := w.apply(Event{Kind: Answer, Pusher: name, Answer: answer}, rules); ok {
					recorded = next
				}
				recorded.passes[index] = passState{pc: "idle"}
				add(fmt.Sprintf("%s records %s", name, answer), recorded)
			}
		case "failed":
			recorded := w
			if next, ok := w.apply(Event{Kind: Failure, Pusher: name}, rules); ok {
				recorded = next
			}
			recorded.passes[index] = passState{pc: "idle"}
			add(name+" records a confirmed failure", recorded)
		}
		if pass.pc == "claimed" || pass.pc == "written" || pass.pc == "failed" {
			crashed := w
			crashed.passes[index] = passState{pc: "dead"}
			add(name+" crashes", crashed)
		}
	}

	// Claude Code releases or silently discards a held copy (section 4.2).
	if w.engine == "held" {
		released := w
		released.engine, released.shown, released.handleKnown = "accepted", w.shown+1, true
		add("engine releases the held copy", released)
		discarded := w
		discarded.engine = "dropped"
		add("engine discards the held copy", discarded)
	}

	// The hooks route (section 8.4): recorded `delivered` under the lock, returned after it.
	if w.alive && w.hook == "idle" {
		if announced, ok := w.apply(Event{Kind: Announce, Overdue: w.overdue}, rules); ok {
			if announced.delivery.State == Delivered {
				announced.hook = "announced"
				if w.isRequest && w.request.State != Open {
					announced.pushedClose = true
				}
			}
			add("hook announces", announced)
		}
	}
	if w.hook == "announced" {
		returned := w
		returned.hook, returned.shown = "idle", w.shown+1
		add("hook returns the text", returned)
		lost := w
		lost.hook = "idle"
		add("hook crashes before returning", lost)
	}

	// The recipient's own calls; a review mailbox makes none.
	if w.alive && w.delivery.Route != Mailbox {
		if read, ok := w.apply(Event{Kind: ReadWhole, Overdue: w.overdue}, rules); ok {
			if read.delivery.State == Read {
				read.read()
			}
			add("chat.inbox reads it whole", read)
		}
		if Visible(w.delivery, ByNothing) && !w.overdue {
			again := w
			again.read()
			if again != w {
				add("chat.inbox returns it again", again)
			}
		}
		if named, ok := w.naming(rules); ok {
			if confirmed, ok := w.apply(Event{Kind: Confirm, Overdue: w.overdue, Named: named}, rules); ok {
				if Visible(confirmed.delivery, named) {
					confirmed.read()
				}
				add("chat.inbox names it in ids", confirmed)
			}
			for _, kind := range []Kind{AckRead, AckActed} {
				if acked, ok := w.apply(Event{Kind: kind, Overdue: w.overdue, Named: named}, rules); ok {
					add("chat.ack "+string(kind), acked)
				}
			}
			if w.isRequest && named == ByHandle {
				request, delivery, err := Respond(w.request, w.delivery, w.overdue, named)
				if err == nil || err == ErrRequestClosed {
					answered := w
					answered.setRequest(request)
					answered.setDelivery(delivery)
					add("chat.respond", answered)
				}
			}
		}
	}

	// Another message of the thread carries the bare ID (`thread`, `reply_to`).
	if !w.idKnown {
		leaked := w
		leaked.idKnown = true
		add("another message names its ID", leaked)
	}

	// The sweep: a stale `sending`, and deadlines nobody touched (sections 4.10 and 7.4).
	if next, err := Apply(w.delivery, Event{Kind: Stale}); err == nil {
		stale := w
		stale.setDelivery(next)
		add("sweep: sending older than 60 s", stale)
	}
	if w.overdue {
		if w.isRequest && w.request.State == Open {
			if rules.separateRequestExpiry {
				closed := w
				closed.setRequest(Request{State: Closed, Notice: NoticeOwed})
				add("sweep expires the request", closed)
			} else if request, delivery, err := ExpireRequest(w.request, w.delivery); err == nil {
				closed := w
				closed.setRequest(request)
				closed.setDelivery(delivery)
				add("sweep expires the request with its delivery", closed)
			}
		}
		if !w.isRequest || rules.separateRequestExpiry || w.request.State != Open {
			if next, err := Apply(w.delivery, Event{Kind: Expire}); err == nil {
				expired := w
				expired.setDelivery(next)
				add("sweep expires the delivery", expired)
			}
		}
	}

	// A review mailbox's freeze, of the recipient or of the requester (section 9.1).
	if w.alive && !w.overdue && w.delivery.Route == Mailbox {
		if next, err := Apply(w.delivery, Event{Kind: Freeze}); err == nil {
			frozen := w
			frozen.setDelivery(next)
			frozen.alive = false
			add("the recipient mailbox freezes", frozen)
		}
	}
	if w.fromMailbox && w.request.State == Open && !w.overdue {
		frozen := w
		if rules.freezeLeavesDelivery {
			frozen.setRequest(Request{State: Closed, Notice: NoticeOwed})
		} else if request, delivery, err := ExpireRequest(w.request, w.delivery); err == nil {
			frozen.setRequest(request)
			frozen.setDelivery(delivery)
		}
		add("the requesting mailbox freezes", frozen)
	}

	if w.alive {
		ended := w
		ended.alive = false
		if next, err := Apply(w.delivery, Event{Kind: End}); err == nil {
			ended.setDelivery(next)
		}
		add("the recipient ends", ended)
	}
	if !w.overdue {
		late := w
		late.overdue = true
		add("the deadline passes", late)
	}
	return steps
}

// invariant returns the first property the world breaks, or "".
func invariant(w world) string {
	switch {
	case w.shown > 1:
		return "AtMostOnce: the text reached the model twice"
	case w.exposed:
		return "NoEarlyExposure: a read showed a native body the engine never accepted"
	case (w.engine != "none" || w.hook == "announced") && w.delivery.State == Pending:
		return "NeverLooksUnpushed: a pushed delivery is recorded pending"
	case w.pushedClose:
		return "NoPushAfterClose: a closed request was pushed"
	case w.deliveryNotices > 1 || w.requestNotices > 1:
		return "NoticeOnce: one record owes two notices"
	case w.isRequest && w.delivery.State == Expired && w.request.State == Closed &&
		w.deliveryNotices+w.requestNotices != 1:
		return "NoticeOnce: a request and its delivery expired together with more than one notice"
	case (w.delivery.State == Refused || w.delivery.State == Dropped) && w.deliveryNotices != 1,
		w.delivery.State == Expired && w.deliveryNotices+w.requestNotices < 1:
		return "NoticeOnce: a final delivery owes no notice"
	case w.request.State == Answered && !Final(w.delivery.State):
		return "RequestAgrees: an answered request's delivery is not final"
	}
	return ""
}

// transitionProperty checks one step: final states stay, and deadlines come first.
func transitionProperty(from, to world) string {
	if Final(from.delivery.State) && to.delivery.State != from.delivery.State {
		return "FinalStable: a final state was left"
	}
	if from.overdue && Expirable(from.delivery.State) && to.delivery.State != from.delivery.State &&
		to.delivery.State != Expired && to.delivery.State != Dropped {
		return "DeadlineFirst: an overdue delivery moved somewhere other than expired"
	}
	return ""
}

type exploration struct {
	states    int
	violation string
	trace     []string
}

func explore(rules variant) exploration {
	type origin struct {
		parent world
		name   string
		root   bool
	}
	seen := map[world]origin{}
	var queue []world
	for _, start := range initialWorlds(rules) {
		if _, ok := seen[start]; !ok {
			seen[start] = origin{root: true}
			queue = append(queue, start)
		}
	}
	traceTo := func(last world, final string) []string {
		trace := []string{final}
		for current := last; ; {
			came := seen[current]
			if came.root {
				break
			}
			trace = append(trace, came.name)
			current = came.parent
		}
		for left, right := 0, len(trace)-1; left < right; left, right = left+1, right-1 {
			trace[left], trace[right] = trace[right], trace[left]
		}
		return trace
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if broken := invariant(current); broken != "" {
			return exploration{states: len(seen), violation: broken, trace: traceTo(current, "(state)")}
		}
		for _, next := range successors(current, rules) {
			if broken := transitionProperty(current, next.next); broken != "" {
				return exploration{states: len(seen), violation: broken, trace: traceTo(current, next.name)}
			}
			if _, ok := seen[next.next]; !ok {
				seen[next.next] = origin{parent: current, name: next.name}
				queue = append(queue, next.next)
			}
		}
	}
	return exploration{states: len(seen)}
}

func TestTheDeliveryModelHoldsEveryProperty(t *testing.T) {
	result := explore(variant{})
	if result.violation != "" {
		t.Fatalf("%s after:\n  %s", result.violation, strings.Join(result.trace, "\n  "))
	}
	t.Logf("delivery model: %d distinct states, every property holds", result.states)
	if result.states < 1000 {
		t.Fatalf("only %d states: the model no longer explores what it did", result.states)
	}
}

// Each rule the model changed in docs/design/chat.md: the rule as it stood is a counterexample.
func TestTheSpecBeforeTheModelBreaksAProperty(t *testing.T) {
	cases := []struct {
		name  string
		rules variant
		want  string
	}{
		{"a bare ID as evidence (section 4.10)", variant{bareIDIsEvidence: true}, "NoEarlyExposure"},
		{"two expiry notices for one request (section 4.4)", variant{separateRequestExpiry: true}, "NoticeOnce"},
		{"a freeze leaving its questions pushable (section 9.1)", variant{freezeLeavesDelivery: true}, "NoPushAfterClose"},
		{"a deadline check that forgets the request (section 4.10)", variant{deadlineSkipsRequest: true}, "NoticeOnce"},
		{"a confirmed failure reopening a closed request (section 8.3)", variant{failureReopens: true}, "NoPushAfterClose"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := explore(test.rules)
			if !strings.HasPrefix(result.violation, test.want) {
				t.Fatalf("want a %s counterexample, got %q", test.want, result.violation)
			}
			t.Logf("%s after:\n  %s", result.violation, strings.Join(result.trace, "\n  "))
		})
	}
}
