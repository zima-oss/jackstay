package deliverystate

// The send model of docs/design/chat-model/ChatSend.tla, run over this package's journal rules: one
// send and its retry with the same idempotency key, a process crash or a machine crash (which loses
// a commit record whose folder was never fsynced) at every step, recovery, the pusher, sessions
// registering near the store's limit, and the leftovers sweep.

import (
	"strings"
	"testing"
)

type sendRules struct {
	recoveryIgnoresSync bool // as written: recovery rolls a written commit forward without fsyncing it
	derivedChargedLate  bool // as written: the derived files are charged when step 4 makes them
	retryIgnoresEntry   bool // as written: a retry that finds no idempotency entry is a fresh send
	chargeNotReserved   bool // as written: a kept entry holds only the charges of the files it wrote
}

// Small charges with the shape of the real ones: a send's own records, its derived files, a session.
const (
	modelLimit       = 6
	modelCore        = 2
	modelDerived     = 1
	modelSession     = 2
	modelMaxSessions = 2
)

type message struct {
	journal  bool
	files    bool
	commit   Commit
	followOn bool // the subscription it replaced, or the request it answered, on another record
	derived  bool
	markers  bool // false while derived: the markers were refused for room
	pushed   int
}

type sendWorld struct {
	messages    [2]message
	idempotency int // 0: none; otherwise the message (1 or 2) the entry names
	attempt     int // the attempt in progress or last made: 1 the send, 2 its retry
	phase       string
	answers     [2]string // none, accepted, uncertain, duplicate, capacity, crashed
	sessions    int
}

func (w sendWorld) used(rules sendRules) int {
	used := w.sessions * modelSession
	for _, message := range w.messages {
		if message.journal && !rules.derivedChargedLate && !rules.chargeNotReserved {
			used += modelCore + modelDerived // the whole admitted charge, until the entry goes
			continue
		}
		if message.files {
			used += modelCore
		}
		if message.derived && message.markers {
			used += modelDerived
		}
	}
	return used
}

// lockFree: no process holds the store's lock, and recovery has done what it can.
func (w sendWorld) lockFree(rules sendRules) bool {
	if w.phase != "idle" {
		return false
	}
	for _, message := range w.messages {
		if message.journal && (rules.recoveryIgnoresSync || message.commit != CommitWritten) {
			return false
		}
	}
	return true
}

func sendSuccessors(w sendWorld, rules sendRules) []step2 {
	var steps []step2
	add := func(name string, next sendWorld) { steps = append(steps, step2{name, next}) }
	index := w.attempt - 1

	// A send, or its retry with the same idempotency key.
	if w.lockFree(rules) && (w.attempt == 0 || w.attempt == 1 && (w.answers[0] == "uncertain" || w.answers[0] == "crashed")) {
		next := w
		next.attempt = w.attempt + 1
		slot := next.attempt - 1
		charge := modelCore + modelDerived
		if rules.derivedChargedLate {
			charge = modelCore
		}
		kept := w.messages[0].journal && !rules.recoveryIgnoresSync && !rules.retryIgnoresEntry
		switch ResolveRetry(w.idempotency != 0, kept) {
		case Duplicate:
			next.answers[slot] = "duplicate"
		case StillUncertain:
			next.attempt = w.attempt // the retry changes nothing; the sender may try again later
			next.answers[0] = "uncertain"
		}
		switch {
		case next.answers[slot] != "none" || next.attempt == w.attempt:
		case !Admit(int64(w.used(rules)), int64(charge), modelLimit):
			next.answers[slot] = "capacity"
		default:
			next.phase = "journalled"
			next.messages[slot].journal = true
		}
		add("send attempt", next)
	}
	if w.phase != "idle" {
		next := w
		message := &next.messages[index]
		switch w.phase {
		case "journalled":
			message.files = true
			next.phase = "files"
			add("write body, envelope, deliveries", next)
		case "files":
			message.commit = CommitWritten
			next.phase = "committed"
			add("write the commit record", next)
		case "committed":
			synced := next
			synced.messages[index].commit = CommitDurable
			synced.phase = "follow"
			add("fsync the commit folder", synced)
			unsynced := next
			if rules.recoveryIgnoresSync {
				unsynced.phase = "follow"
			} else {
				unsynced.phase, unsynced.answers[index] = "idle", "uncertain"
			}
			add("the commit folder cannot be fsynced", unsynced)
		case "follow":
			message.followOn = true
			next.phase = "derived"
			add("apply the follow-ons", next)
		case "derived":
			message.derived = true
			message.markers = !rules.derivedChargedLate || Admit(int64(w.used(rules)), modelDerived, modelLimit)
			if message.markers {
				next.idempotency = index + 1
			}
			next.phase = "done"
			add("write the derived files", next)
		case "done":
			if message.commit == CommitDurable {
				message.journal = false
				next.answers[index] = "accepted"
			} else {
				next.answers[index] = "uncertain"
			}
			next.phase = "idle"
			add("finish", next)
		}
		crashed := w
		crashed.phase, crashed.answers[index] = "idle", "crashed"
		add("the process crashes", crashed)
	}

	// Recovery, before anything else is served.
	if w.phase == "idle" {
		for slot := range w.messages {
			message := w.messages[slot]
			if !message.journal {
				continue
			}
			for _, synced := range []bool{true, false} {
				decision := Recover(message.commit, synced)
				if rules.recoveryIgnoresSync && decision == KeepEntry {
					decision = RollForward
				}
				next := w
				recovered := &next.messages[slot]
				switch decision {
				case RollBack:
					recovered.files, recovered.journal = false, false
					if !rules.recoveryIgnoresSync && next.idempotency == slot+1 {
						next.idempotency = 0
					}
				case RollForward:
					if synced && !rules.recoveryIgnoresSync {
						recovered.commit = CommitDurable
					}
					recovered.followOn = true
					if !recovered.derived {
						recovered.derived = true
						recovered.markers = !rules.derivedChargedLate || Admit(int64(w.used(rules)), modelDerived, modelLimit)
						if recovered.markers {
							next.idempotency = slot + 1
						}
					}
					recovered.journal = false
				case KeepEntry:
					continue
				}
				add("recovery", next)
			}
		}
	}

	// The pusher pushes what is served; a machine crash loses what was never fsynced.
	if w.lockFree(rules) {
		for slot, message := range w.messages {
			served := Served(message.commit, message.journal)
			if rules.recoveryIgnoresSync {
				served = message.commit != NoCommit && !message.journal
			}
			if served && message.pushed == 0 {
				next := w
				next.messages[slot].pushed = 1
				add("the pusher pushes it", next)
			}
			if !message.journal && message.commit == NoCommit && message.files {
				next := w
				next.messages[slot].files, next.messages[slot].derived = false, false
				add("the sweep removes leftovers", next)
			}
		}
		if w.sessions < modelMaxSessions && Admit(int64(w.used(rules)), modelSession, modelLimit) {
			next := w
			next.sessions++
			add("a session registers", next)
		}
	}
	lost := w
	for slot := range lost.messages {
		if lost.messages[slot].commit == CommitWritten {
			lost.messages[slot].commit = NoCommit
		}
	}
	if w.phase != "idle" {
		lost.phase, lost.answers[index] = "idle", "crashed"
	}
	if lost != w {
		add("the machine crashes", lost)
	}
	return steps
}

type step2 struct {
	name string
	next sendWorld
}

func sendInvariant(w sendWorld, rules sendRules) string {
	if w.used(rules) > modelLimit {
		return "Capacity: the charges passed the write limit"
	}
	if w.messages[0].pushed+w.messages[1].pushed > 1 {
		return "AtMostOncePerKey: one send was pushed twice"
	}
	for slot, message := range w.messages {
		if w.idempotency == slot+1 && message.commit == NoCommit && !message.journal {
			return "DuplicateIsReal: the idempotency entry names a message that does not exist"
		}
		if message.followOn && message.commit == NoCommit && !message.journal {
			return "FollowOnAgrees: a subscription or an answer stands for a message that does not exist"
		}
		if message.derived && !message.markers {
			return "MarkersComplete: a committed send lost its markers for room"
		}
		if w.answers[slot] == "accepted" && message.commit != CommitDurable {
			return "AcceptedIsDurable: accepted, but a crash can undo it"
		}
	}
	return ""
}

func exploreSend(rules sendRules) exploration {
	type origin struct {
		parent sendWorld
		name   string
		root   bool
	}
	start := sendWorld{phase: "idle", answers: [2]string{"none", "none"}}
	for slot := range start.messages {
		start.messages[slot].commit = NoCommit
	}
	seen := map[sendWorld]origin{start: {root: true}}
	queue := []sendWorld{start}
	traceTo := func(last sendWorld, final string) []string {
		trace := []string{final}
		for current := last; !seen[current].root; current = seen[current].parent {
			trace = append(trace, seen[current].name)
		}
		for left, right := 0, len(trace)-1; left < right; left, right = left+1, right-1 {
			trace[left], trace[right] = trace[right], trace[left]
		}
		return trace
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if broken := sendInvariant(current, rules); broken != "" {
			return exploration{states: len(seen), violation: broken, trace: traceTo(current, "(state)")}
		}
		for _, next := range sendSuccessors(current, rules) {
			if _, ok := seen[next.next]; !ok {
				seen[next.next] = origin{parent: current, name: next.name}
				queue = append(queue, next.next)
			}
		}
	}
	return exploration{states: len(seen)}
}

func TestTheSendModelHoldsEveryProperty(t *testing.T) {
	result := exploreSend(sendRules{})
	if result.violation != "" {
		t.Fatalf("%s after:\n  %s", result.violation, strings.Join(result.trace, "\n  "))
	}
	t.Logf("send model: %d distinct states, every property holds", result.states)
	if result.states < 100 {
		t.Fatalf("only %d states: the model no longer explores what it did", result.states)
	}
}

func TestTheSendBeforeTheModelBreaksAProperty(t *testing.T) {
	cases := []struct {
		name  string
		rules sendRules
		want  string
	}{
		{"recovery rolling forward an unsynced commit (section 7.2)", sendRules{recoveryIgnoresSync: true}, "DuplicateIsReal"},
		{"a retry beside a kept entry (section 7.2)", sendRules{retryIgnoresEntry: true}, "AtMostOncePerKey"},
		{"a kept entry's charge left unreserved (section 7.4)", sendRules{chargeNotReserved: true}, "Capacity"},
		{"derived files charged after the commit (section 7.4)", sendRules{derivedChargedLate: true}, "MarkersComplete"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			result := exploreSend(test.rules)
			if !strings.HasPrefix(result.violation, test.want) {
				t.Fatalf("want a %s counterexample, got %q", test.want, result.violation)
			}
			t.Logf("%s after:\n  %s", result.violation, strings.Join(result.trace, "\n  "))
		})
	}
}

func TestRecoveryKeepsAnEntryItCannotMakeDurable(t *testing.T) {
	cases := []struct {
		commit Commit
		synced bool
		want   Recovery
	}{
		{NoCommit, true, RollBack},
		{NoCommit, false, RollBack},
		{CommitWritten, true, RollForward},
		{CommitWritten, false, KeepEntry},
		{CommitDurable, false, RollForward},
	}
	for _, test := range cases {
		if got := Recover(test.commit, test.synced); got != test.want {
			t.Errorf("Recover(%s, %v) = %s, want %s", test.commit, test.synced, got, test.want)
		}
	}
	if Served(CommitWritten, false) || Served(CommitDurable, true) || !Served(CommitDurable, false) {
		t.Error("served before durable, or while journalled")
	}
}

func TestASendIsChargedForEverythingItCreates(t *testing.T) {
	charge := SendCharge(32<<10, 256, true)
	want := int64(32<<10 + 48<<10 + 1<<10 + 256*(4<<10+512) + 512 + 512)
	if charge != want {
		t.Fatalf("SendCharge = %d, want %d", charge, want)
	}
	if Admit(WriteLimit-charge+1, charge, WriteLimit) || !Admit(WriteLimit-charge, charge, WriteLimit) {
		t.Fatal("Admit does not hold the limit exactly")
	}
	if Admit(0, SessionCharge, SessionCharge-1) {
		t.Fatal("a session admitted past the limit")
	}
}
