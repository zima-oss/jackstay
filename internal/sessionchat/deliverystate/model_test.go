package deliverystate

// The delivery model of docs/design/chat-model/ChatDelivery.tla, run over this package's table: every
// interleaving of one delivery's steers (two bridge passes, either of which may pause or crash at any
// step), the native doorbell and its refusal, the recipient's reads, acks and answers, the sweep, the
// deadline, the recipient's end and a review mailbox's freeze, explored breadth first to the last
// reachable state. A variant replays a rule of the spec as it stood before the model (section 4.4's
// two expiry notices, section 9.1's freeze, and the others below) and must find the counterexample the
// fix removed.

import (
	"fmt"
	"strings"
	"testing"
)

// variant selects the rules the explored world follows.
type variant struct {
	separateRequestExpiry bool // as written: a request and its delivery expire with a notice each
	freezeLeavesDelivery  bool // as written: a freeze closes the mailbox's requests, not their deliveries
	deadlineSkipsRequest  bool // as written: a pass's or reader's deadline check expires the delivery only
	deadlineNeedsPending  bool // round 1: a deadline closed a request only while its delivery could expire
	freezeIgnoresPushes   bool // rounds 1 and 2: a freeze closed its questions while one was being steered
	markBeforeShow        bool // before W-096: a notice was marked shown, then its page returned
}

type passState struct {
	pc string // idle, claimed, written, failed, dead
}

type world struct {
	delivery        Delivery
	request         Request
	isRequest       bool
	fromMailbox     bool
	engine          string // what the app-server did with a steer: none, accepted
	passes          [2]passState
	shown           int  // times the text reached the recipient's model by a steer
	requestKnown    bool // the recipient was shown the request (a read, or a steer), so it can answer
	pushedClose     bool // a steer of a request began (a claim) after it closed
	lateOpen        bool // a step that checks the deadline found it passed and left the request open
	frozen          bool // the requesting mailbox froze
	writtenFrozen   bool // a pass that still owned its claim wrote a question its mailbox had frozen
	overdue         bool
	alive           bool
	deliveryNotices int
	requestNotices  int
	deliveryShows   int // times the delivery's notice reached the sender (2 stands for more than once)
	requestShows    int // times the request's notice reached the sender
}

type step struct {
	name string
	next world
}

var passNames = [2]string{"pass-1", "pass-2"}

// The distinct states TLC finds for the two modules (docs/design/chat-model/README.md).
const (
	deliveryStates = 27056
	sendStates     = 335
)

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
		rules.freezeLeavesDelivery && w.request.State == Closed ||
		rules.deadlineSkipsRequest && checksDeadline(event.Kind) ||
		rules.deadlineNeedsPending && !Expirable(w.delivery.State)
	if plain {
		delivery, err := Apply(w.delivery, event)
		if err != nil {
			return w, false
		}
		next.setDelivery(delivery)
		next.lateOpen = w.lateOpen || w.isRequest && event.Overdue && checksDeadline(event.Kind) && w.request.State == Open
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

	// The bridge's passes (section 9.2): the one route that still carries a message's text.
	for index := range w.passes {
		pass := w.passes[index]
		name := passNames[index]
		if w.delivery.Route == Bridge && pass.pc == "idle" {
			if claimed, ok := w.apply(Event{Kind: Claim, Pusher: name, Overdue: w.overdue}, rules); ok {
				if claimed.delivery.State == Sending && claimed.delivery.Pusher == name && w.delivery.State == Pending {
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
			written := w
			written.passes[index].pc = "written"
			if w.frozen && w.delivery.State == Sending && w.delivery.Pusher == name {
				written.writtenFrozen = true
			}
			written.engine, written.shown, written.requestKnown = "accepted", w.shown+1, true
			add(name+" steers", written)
		case "written":
			for _, answer := range []EngineAnswer{Accepted, Lost} {
				recorded := w
				if next, ok := w.apply(Event{Kind: Answer, Pusher: name, Answer: answer}, rules); ok {
					recorded = next
				}
				recorded.passes[index].pc = "idle"
				add(fmt.Sprintf("%s records %s", name, answer), recorded)
			}
		case "failed":
			recorded := w
			if next, ok := w.apply(Event{Kind: Failure, Pusher: name}, rules); ok {
				recorded = next
			}
			recorded.passes[index].pc = "idle"
			add(name+" records a confirmed failure", recorded)
		}
		if pass.pc == "claimed" || pass.pc == "written" || pass.pc == "failed" {
			crashed := w
			crashed.passes[index].pc = "dead"
			add(name+" crashes", crashed)
		}
	}

	// The native doorbell (section 8.3): a ring the engine takes or ignores changes nothing; one it
	// refuses (the connection closed) refuses the delivery. A hook's ring changes nothing at all.
	if w.alive && w.delivery.Route == Native {
		if refused, ok := w.apply(Event{Kind: RingRefused, Overdue: w.overdue}, rules); ok {
			add("the engine refuses the doorbell", refused)
		}
	}

	// The recipient's own calls; a review mailbox makes none.
	if w.alive && w.delivery.Route != Mailbox {
		if read, ok := w.apply(Event{Kind: ReadWhole, Overdue: w.overdue}, rules); ok {
			if read.delivery.State == Read && w.delivery.State != Read {
				read.requestKnown = true
			}
			add("chat.inbox reads it whole", read)
		}
		if peeked, ok := w.apply(Event{Kind: Peek, Overdue: w.overdue}, rules); ok {
			if Visible(peeked.delivery) {
				peeked.requestKnown = true
			}
			add("a read that marks nothing", peeked)
		}
		for _, kind := range []Kind{AckRead, AckActed} {
			if acked, ok := w.apply(Event{Kind: kind, Overdue: w.overdue}, rules); ok {
				add("chat.ack "+string(kind), acked)
			}
		}
		if w.isRequest && w.requestKnown {
			request, delivery, err := Respond(w.request, w.delivery, w.overdue)
			if err == nil || err == ErrRequestClosed {
				answered := w
				answered.setRequest(request)
				answered.setDelivery(delivery)
				add("chat.respond", answered)
			}
		}
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
				cut := w
				cut.setRequest(request)
				add("the sweep crashes after the request's write", cut)
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
	if w.fromMailbox && w.request.State == Open && !w.overdue &&
		(rules.freezeIgnoresPushes || FreezeMayProceed([]Delivery{w.delivery})) {
		frozen := w
		frozen.frozen = true
		if rules.freezeLeavesDelivery {
			frozen.setRequest(Request{State: Closed, Notice: NoticeOwed})
		} else if request, delivery, err := ExpireRequest(w.request, w.delivery); err == nil {
			frozen.setRequest(request)
			frozen.setDelivery(delivery)
		}
		add("the requesting mailbox freezes", frozen)
		if !rules.freezeLeavesDelivery {
			cut := w
			cut.frozen = true
			cut.setRequest(Request{State: Closed, Notice: NoticeOwed})
			add("the freeze crashes after the request's write", cut)
		}
	}

	// The sender's inbox returns an owed notice (section 8.5): marked shown only after its page is
	// returned, so a stop between the two returns it again (at least once, W-096).
	oneMore := func(shows int) int { return min(shows+1, 2) }
	show := func(name string, notice Notice, mark func(*world), shows func(*world) *int) {
		if notice != NoticeOwed {
			return
		}
		marked := w
		mark(&marked)
		*shows(&marked) = oneMore(*shows(&w))
		add(name+" is shown and marked", marked)
		if rules.markBeforeShow {
			lost := w
			mark(&lost)
			add(name+" is marked, then the step stops before its page", lost)
			return
		}
		again := w
		*shows(&again) = oneMore(*shows(&w))
		add(name+" is shown, then the step stops before the mark", again)
	}
	show("the delivery's notice", w.delivery.Notice, func(next *world) { next.delivery.Notice = NoticeShown },
		func(next *world) *int { return &next.deliveryShows })
	show("the request's notice", w.request.Notice, func(next *world) { next.request.Notice = NoticeShown },
		func(next *world) *int { return &next.requestShows })

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
	case w.delivery.Route != Bridge && w.shown > 0:
		return "ContentOnlyThroughReads: a doorbell route carried the text"
	case w.engine != "none" && w.delivery.State == Pending:
		return "NeverLooksUnpushed: a steered delivery is recorded pending"
	case w.pushedClose:
		return "NoPushStartsAfterClose: a push of a closed request began"
	case w.deliveryNotices > 1 || w.requestNotices > 1:
		return "NoticeOwedOnce: one record owes two notices"
	case w.isRequest && w.delivery.State == Expired && w.request.State == Closed &&
		w.deliveryNotices+w.requestNotices != 1:
		return "NoticeOwedOnce: a request and its delivery expired together with more than one notice"
	case (w.delivery.State == Refused || w.delivery.State == Dropped) && w.deliveryNotices != 1,
		w.delivery.State == Expired && w.deliveryNotices+w.requestNotices < 1:
		return "NoticeOwedOnce: a final delivery owes no notice"
	case w.delivery.Notice == NoticeShown && w.deliveryShows < 1, w.request.Notice == NoticeShown && w.requestShows < 1:
		return "NoticeAtLeastOnce: a notice was marked shown and never reached the sender"
	case w.request.State == Answered && !Final(w.delivery.State):
		return "RequestAgrees: an answered request's delivery is not final"
	case w.lateOpen:
		return "DeadlineClosesRequest: a step past the deadline left the request open"
	case w.writtenFrozen:
		return "NoWriteAfterFreeze: a question was written after its mailbox froze"
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
	// TLC finds the same number for docs/design/chat-model/ChatDelivery.tla; a change to either
	// model changes both, and README.md with them.
	if result.states != deliveryStates {
		t.Fatalf("%d states, not the %d TLC finds: the Go and TLA+ models no longer agree", result.states, deliveryStates)
	}
}

// Each rule the model changed in docs/design/chat.md: the rule as it stood is a counterexample.
func TestTheSpecBeforeTheModelBreaksAProperty(t *testing.T) {
	cases := []struct {
		name  string
		rules variant
		want  string
	}{
		{"two expiry notices for one request (section 4.4)", variant{separateRequestExpiry: true}, "NoticeOwedOnce"},
		{"a freeze leaving its questions pushable (section 9.1)", variant{freezeLeavesDelivery: true}, "NoPushStartsAfterClose"},
		{"a deadline check that forgets the request (section 4.10)", variant{deadlineSkipsRequest: true}, "NoticeOwedOnce"},
		{"a notice marked shown before its page (section 8.5, before W-096)", variant{markBeforeShow: true}, "NoticeAtLeastOnce"},
		{"an overdue read of a delivered request (section 4.10, review round 1)", variant{deadlineNeedsPending: true}, "DeadlineClosesRequest"},
		{"a freeze beside a steer under way (section 9.1, review rounds 1 and 2)", variant{freezeIgnoresPushes: true}, "NoWriteAfterFreeze"},
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
