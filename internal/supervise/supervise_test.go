package supervise

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/procs"
)

// lockedBuffer is a buffer written from several goroutines.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (out *lockedBuffer) Write(data []byte) (int, error) {
	out.mutex.Lock()
	defer out.mutex.Unlock()
	return out.buffer.Write(data)
}

func (out *lockedBuffer) String() string {
	out.mutex.Lock()
	defer out.mutex.Unlock()
	return out.buffer.String()
}

// fakeRun is a run over the fake world whose leader has ended (a zombie) unless it is told
// otherwise; its reap records what of the group still ran then.
type fakeRun struct {
	*running
	world        *world
	words        *lockedBuffer
	livingAtReap []int
	reaps        int
	// sentAtReap is how many signals had been sent when the leader was reaped.
	sentAtReap int
}

func newFakeRun(w *world, mode Mode, leader int) *fakeRun {
	words := &lockedBuffer{}
	spec := withDefaults(Spec{Mode: mode, System: w.system(), Clock: w.clock, Say: words, Who: "jst lock",
		Grace: 30 * time.Second, KillWait: 5 * time.Second, Settle: 300 * time.Millisecond})
	fake := &fakeRun{running: newRunning(spec, leader), world: w, words: words}
	fake.reapProcess = func() (*os.ProcessState, error) {
		fake.reaps++
		fake.sentAtReap = len(w.sentSignals())
		fake.livingAtReap = w.reap(leader)
		return nil, nil
	}
	return fake
}

func TestClearNothingLeftSendsNothingAndReaps(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "make")
	w.end(100)
	run := newFakeRun(w, Group, 100)
	result := run.clear(false, w.clock.Now())
	if len(w.sentSignals()) != 0 || result.left != nil || run.words.String() != "" || run.reaps != 1 {
		t.Fatalf("sent %v, left %v, said %q, reaped %d", w.sentSignals(), result.left, run.words.String(), run.reaps)
	}
}

func TestClearLeftoverThatStopsOnTerm(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "sh")
	w.add(101, 1, 100, 50, "sleep")
	w.end(100)
	run := newFakeRun(w, Group, 100)
	began := w.clock.Now()
	result := run.clear(false, began)
	if len(result.left) != 1 || result.left[0].PID != 101 {
		t.Fatalf("left %v", result.left)
	}
	signals := w.sentSignals()
	if len(signals) != 1 || signals[0].target != -100 || signals[0].sig != unix.SIGTERM {
		t.Fatalf("sent %v", signals)
	}
	if signals[0].at.Sub(began) < 300*time.Millisecond {
		t.Fatalf("SIGTERM before the settle was over: %v", signals[0].at.Sub(began))
	}
	words := run.words.String()
	if !strings.Contains(words, "left sleep[101] running") || strings.Contains(words, "SIGKILL") {
		t.Fatalf("said %q", words)
	}
	if run.reaps != 1 || len(run.livingAtReap) != 0 {
		t.Fatalf("reaped %d with %v living", run.reaps, run.livingAtReap)
	}
}

func TestClearLeftoverIgnoringTermIsKilledAfterTheGrace(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "sh")
	w.add(101, 1, 100, 50, "server").ignoresTerm = true
	w.end(100)
	run := newFakeRun(w, Group, 100)
	result := run.clear(false, w.clock.Now())
	signals := w.sentSignals()
	if len(signals) != 2 || signals[0].sig != unix.SIGTERM || signals[1].sig != unix.SIGKILL || signals[1].target != -100 {
		t.Fatalf("sent %v", signals)
	}
	if waited := signals[1].at.Sub(signals[0].at); waited < 30*time.Second {
		t.Fatalf("SIGKILL %v after SIGTERM, before the grace", waited)
	}
	if len(result.survivors) != 0 || !strings.Contains(run.words.String(), "killing it (SIGKILL)") {
		t.Fatalf("survivors %v, said %q", result.survivors, run.words.String())
	}
}

func TestClearAfterARequestWaitsTheDeadlineWithoutAnotherTerm(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "sh")
	w.add(101, 1, 100, 50, "deaf").ignoresTerm = true
	w.end(100)
	run := newFakeRun(w, Group, 100)
	deadline := w.clock.Now().Add(20 * time.Second)
	result := run.clear(true, deadline)
	signals := w.sentSignals()
	if len(signals) == 0 || signals[len(signals)-1].sig != unix.SIGKILL {
		t.Fatalf("sent %v", signals)
	}
	for _, each := range signals {
		if each.at.Before(deadline) || each.sig == unix.SIGTERM {
			t.Fatalf("a signal before the deadline, or another TERM: %v", signals)
		}
	}
	if result.left != nil {
		t.Fatalf("a stop that was asked for counts nothing as left: %v", result.left)
	}
}

func TestClearAnotherAccountsGroupIsNotWaitedFor(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "sh")
	w.add(101, 1, 100, 50, "sudo").row.UID = 0
	w.end(100)
	run := newFakeRun(w, Group, 100)
	began := w.clock.Now()
	result := run.clear(false, began)
	if len(result.foreign) != 1 || len(w.sentSignals()) != 0 || w.clock.Now() != began {
		t.Fatalf("foreign %v, sent %v, waited %v", result.foreign, w.sentSignals(), w.clock.Now().Sub(began))
	}
	if !strings.Contains(run.words.String(), "cannot be signalled by this account") {
		t.Fatalf("said %q", run.words.String())
	}
}

func TestClearNamesWhatSurvivesTheKill(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "sh")
	w.add(101, 1, 100, 50, "stuck").unkillable = true
	w.end(100)
	run := newFakeRun(w, Group, 100)
	result := run.clear(false, w.clock.Now())
	if len(result.survivors) != 1 || result.survivors[0].PID != 101 || !strings.Contains(run.words.String(), "STILL RUNNING") {
		t.Fatalf("survivors %v, said %q", result.survivors, run.words.String())
	}
	if run.reaps != 1 {
		t.Fatal("the leader was not reaped after the last KILL")
	}
}

func TestClearDoesNotWaitForWhatLeftTheGroup(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "sh")
	w.add(102, 100, 102, 102, "daemon").ignoresTerm = true // started a session of its own
	w.end(100)
	run := newFakeRun(w, Group, 100)
	result := run.clear(false, w.clock.Now())
	if len(result.survivors) != 0 || len(w.sentSignals()) != 0 || strings.Contains(run.words.String(), "SIGKILL") {
		t.Fatalf("survivors %v, sent %v, said %q", result.survivors, w.sentSignals(), run.words.String())
	}
}

// A leftover that starts a replacement in the group and ends between two
// reads left the group populated while every remembered member was gone, and the lock was let go.
// The group is named by the unreaped leader's id, so the replacement is found, and the leader is
// reaped only once nothing of the group runs.
func TestAReplacementStartedBetweenReadsIsFoundBeforeTheReap(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "sh")
	leftover := w.add(101, 1, 100, 50, "respawner")
	replacement := w.later(102, 1, 100, 50, "replacement") // reparented to 1, still in the group
	replacement.ignoresTerm = true
	leftover.replacement = replacement
	w.end(100)
	run := newFakeRun(w, Group, 100)
	result := run.clear(false, w.clock.Now())
	if w.processes[102].alive {
		t.Fatalf("the replacement runs on; sent %v", w.sentSignals())
	}
	signals := w.sentSignals()
	if last := signals[len(signals)-1]; last.target != -100 || last.sig != unix.SIGKILL {
		t.Fatalf("sent %v", signals)
	}
	if run.reaps != 1 || len(run.livingAtReap) != 0 || len(result.survivors) != 0 {
		t.Fatalf("reaped %d with %v living, survivors %v", run.reaps, run.livingAtReap, result.survivors)
	}
	nothingAfterTheReap(t, run)
}

func TestClearInASessionReachesItsMembersInOtherGroups(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 100, "swift-test")
	w.add(101, 1, 101, 100, "xctest").ignoresTerm = true // a group of its own in the session, reparented
	w.end(100)
	run := newFakeRun(w, Session, 100)
	result := run.clear(false, w.clock.Now())
	if w.processes[101].alive || len(result.left) != 1 || run.reaps != 1 || len(result.survivors) != 0 {
		t.Fatalf("left %v, survivors %v, sent %v", result.left, result.survivors, w.sentSignals())
	}
	sawMember := false
	for _, each := range w.sentSignals() {
		sawMember = sawMember || (each.target == 101 && each.sig == unix.SIGKILL)
	}
	if !sawMember {
		t.Fatalf("sent %v", w.sentSignals())
	}
}

// nothingAfterTheReap fails the test when a signal was sent after the leader was reaped: its ids
// are no longer the run's then.
func nothingAfterTheReap(t *testing.T, run *fakeRun) {
	t.Helper()
	if run.reaps != 1 {
		t.Fatalf("reaped %d times", run.reaps)
	}
	if after := run.world.sentSignals()[run.sentAtReap:]; len(after) != 0 {
		t.Fatalf("signalled after the reap: %v", after)
	}
}

// Without a table the leader was reaped first and the group
// probed and signalled by a number no longer the run's. Now the whole stop goes to the group by
// its id while the leader is unreaped, the leader is reaped last, and the result is unconfirmed.
func TestWithoutATableTheGroupIsStoppedBeforeTheReapAndUnconfirmed(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "sh")
	w.add(101, 1, 100, 50, "server").ignoresTerm = true
	w.end(100)
	w.readFails = true
	run := newFakeRun(w, Group, 100)
	var outcome Outcome
	run.clearAfterEnd(&outcome, false, w.clock.Now())
	signals := w.sentSignals()
	if len(signals) != 2 || signals[0] != (sent{target: -100, sig: unix.SIGTERM, at: signals[0].at}) ||
		signals[1].target != -100 || signals[1].sig != unix.SIGKILL || w.processes[101].alive {
		t.Fatalf("sent %v", signals)
	}
	if waited := signals[1].at.Sub(signals[0].at); waited < 30*time.Second {
		t.Fatalf("SIGKILL %v after SIGTERM", waited)
	}
	nothingAfterTheReap(t, run)
	if !outcome.Unconfirmed || len(outcome.Problems) != 1 || len(run.livingAtReap) != 0 {
		t.Fatalf("outcome %+v, living at the reap %v", outcome, run.livingAtReap)
	}
}

// A group that ends, and whose number is given to a stranger's group after the reap, is never
// signalled again: nothing is sent after the reap, whatever the table says.
func TestAGroupNumberGivenAgainAfterTheReapIsNotSignalled(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "sh")
	w.end(100)
	w.readFails = true
	run := newFakeRun(w, Group, 100)
	reap := run.reapProcess
	run.reapProcess = func() (*os.ProcessState, error) {
		state, err := reap()
		w.add(100, 1, 100, 77, "stranger") // the id given again, a group of its own
		return state, err
	}
	run.clear(true, w.clock.Now())
	nothingAfterTheReap(t, run)
	run.forward(unix.SIGTERM)
	if !(&Control{run: run.running}).Stop() || !w.processes[100].alive {
		t.Fatal("the stranger was signalled")
	}
	nothingAfterTheReap(t, run)
}

// A member outside the group was signalled by an id from an older table
// after a read failed; its id may be another process's by then. A failed read signals the group
// alone.
func TestAMemberIdReplacedAfterTheLastGoodReadIsNotSignalled(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 100, "swift-test")
	w.add(101, 1, 101, 100, "xctest").ignoresTerm = true // the session's, in a group of its own
	w.end(100)
	run := newFakeRun(w, Session, 100)
	if _, living, known := run.look(); !known || len(living) != 1 {
		t.Fatalf("living %v", living)
	}
	w.mutex.Lock()
	w.processes[101].alive = false
	w.mutex.Unlock()
	w.add(101, 1, 101, 101, "stranger") // the id given again, outside the session
	w.readFails = true
	var result cleared
	run.signalNow(unix.SIGKILL, &result)
	for _, each := range w.sentSignals() {
		if each.target == 101 {
			t.Fatalf("the stranger was signalled: %v", w.sentSignals())
		}
	}
	if !w.processes[101].alive {
		t.Fatal("the stranger was killed")
	}
}

func TestClearWithoutATableAndNothingLeftReapsLastAndIsUnconfirmed(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "sh")
	w.end(100)
	w.readFails = true
	run := newFakeRun(w, Group, 100)
	result := run.clear(false, w.clock.Now())
	nothingAfterTheReap(t, run)
	if !result.unconfirmed || result.left != nil {
		t.Fatalf("%+v", result)
	}
}

// The end was awaited by a reaping wait under the lock signalling needs
// (on linux at once, on darwin after a kqueue error), so no request to stop reached the command.
// The end is now awaited without reaping and without the lock; when it cannot be awaited it is
// looked for, and signals pass meanwhile.
func TestSignalsPassWhileTheEndCannotBeAwaited(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "server").ignoresTerm = true
	run := newFakeRun(w, Group, 100)
	var ended sync.Mutex
	isEnded := false
	run.exits = exitWatch{
		await: func(int) (exitSeen, error) { return exitSeen{}, errors.New("kevent: resource exhausted") },
		ended: func(int) (bool, error) {
			ended.Lock()
			defer ended.Unlock()
			return isEnded, nil
		},
	}
	done := make(chan exitSeen, 1)
	go func() { done <- run.awaitEnd(make(chan struct{})) }()
	<-w.clock.waiting // the look is waiting for its next turn
	run.forward(unix.SIGTERM)
	if signals := w.sentSignals(); len(signals) != 1 || signals[0].target != -100 || signals[0].sig != unix.SIGTERM {
		t.Fatalf("sent %v", signals)
	}
	ended.Lock()
	isEnded = true
	ended.Unlock()
	w.clock.advance(run.spec.System.Poll)
	<-done
	if run.reaps != 0 {
		t.Fatal("the end was learnt by reaping")
	}
	if len(run.problems) != 1 || !strings.Contains(run.problems[0].Error(), "looked for every") {
		t.Fatalf("problems %v", run.problems)
	}
}

// blockedWriter never returns from a write until released, and says when one began: a standard
// error nobody reads.
type blockedWriter struct {
	began   chan struct{}
	release chan struct{}
}

func (writer blockedWriter) Write(data []byte) (int, error) {
	select {
	case writer.began <- struct{}{}:
	default:
	}
	<-writer.release
	return len(data), nil
}

// The overdue kill wrote its words to a full standard error under the
// lock, before the KILL. The words go through a queue of their own now: KILL is sent while the
// stream is blocked, and signals still pass.
func TestABlockedErrorStreamDoesNotHoldBackTheKill(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "deaf").ignoresTerm = true
	run := newFakeRun(w, Group, 100)
	stream := blockedWriter{began: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(stream.release)
	queue := newSayQueue(stream)
	run.say = sayer(queue, "jst lock")
	for range sayCapacity + 10 {
		run.say("filling the queue")
	}
	<-stream.began
	exited := make(chan struct{})
	giveUp := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		_ = run.killWhenOverdue(exited, giveUp)
		close(finished)
	}()
	run.request(w.clock.Now(), "SIGTERM")
	run.forward(unix.SIGTERM)
	<-w.clock.waiting // the grace is running
	w.clock.advance(30 * time.Second)
	<-w.clock.waiting // KILL sent, its wait running
	killed := false
	for _, each := range w.sentSignals() {
		killed = killed || (each.target == -100 && each.sig == unix.SIGKILL)
	}
	if !killed || !run.killed {
		t.Fatalf("sent %v", w.sentSignals())
	}
	w.clock.advance(5 * time.Second)
	<-finished
	select {
	case <-giveUp:
	default:
		t.Fatal("the kill's wait was over and the look for the end was not told")
	}
	if dropped := queue.close(firedBound()); dropped < 10 {
		t.Fatalf("dropped %d", dropped)
	}
}

func TestControlStopsTheGroupUntilTheReap(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 100, "server")
	w.add(101, 1, 101, 100, "worker") // the session's, in a group of its own
	run := newFakeRun(w, Session, 100)
	control := &Control{run: run.running}
	if group, others := control.Members(); group != 100 || len(others) != 1 || others[0] != 101 {
		t.Fatalf("members %d %v", group, others)
	}
	if !control.Stop() {
		t.Fatal("the stop was not made")
	}
	if signals := w.sentSignals(); len(signals) != 2 || signals[0].target != -100 || signals[1].target != 101 {
		t.Fatalf("sent %v", signals)
	}
	if at, by := run.firstRequest(); at.IsZero() || by != "a stop from jackstay" {
		t.Fatalf("request %v %q", at, by)
	}
	select {
	case <-run.asked:
	default:
		t.Fatal("the overdue kill was not told")
	}
	run.reaps = 0
	run.reap()
	if !control.Stop() || len(w.sentSignals()) != 2 {
		t.Fatalf("signalled after the reap: %v", w.sentSignals())
	}
}

// A stop needs no table — the group is the unreaped
// leader's.
func TestControlStopsWithoutATable(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 100, "server")
	w.readFails = true
	run := newFakeRun(w, Session, 100)
	control := &Control{run: run.running}
	if !control.Stop() || w.processes[100].alive {
		t.Fatalf("sent %v", w.sentSignals())
	}
	if group, others := control.Members(); group != 100 || others != nil {
		t.Fatalf("members %d %v", group, others)
	}
}

func TestControlSaysWhenTheGroupCannotBeSignalled(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 100, "server").row.UID = 0
	run := newFakeRun(w, Session, 100)
	if (&Control{run: run.running}).Stop() {
		t.Fatal("a refused stop was said to be made")
	}
}

func TestSignalsKeepTheFirstAndPassEveryOneOn(t *testing.T) {
	incoming := make(chan os.Signal)
	signals := newSignals(context.Background(), incoming, newFakeClock())
	go signals.loop()
	defer signals.Close()
	incoming <- unix.SIGTERM
	<-signals.Context().Done()
	var stop *StopRequest
	if !errors.As(context.Cause(signals.Context()), &stop) || stop.Signal != unix.SIGTERM {
		t.Fatalf("cause %v", context.Cause(signals.Context()))
	}
	var mutex sync.Mutex
	var heard []syscall.Signal
	got := make(chan struct{}, 4)
	signals.attach(func(sig syscall.Signal) {
		mutex.Lock()
		heard = append(heard, sig)
		mutex.Unlock()
		got <- struct{}{}
	})
	<-got
	incoming <- unix.SIGINT
	<-got
	first, _, ok := signals.First()
	mutex.Lock()
	defer mutex.Unlock()
	if !ok || first != unix.SIGTERM || len(heard) != 2 || heard[0] != unix.SIGTERM || heard[1] != unix.SIGINT {
		t.Fatalf("first %v %v, heard %v", first, ok, heard)
	}
}

func TestSignalsAfterDetachAreHeldNotPassedOn(t *testing.T) {
	incoming := make(chan os.Signal)
	signals := newSignals(context.Background(), incoming, newFakeClock())
	go signals.loop()
	defer signals.Close()
	calls := 0
	signals.attach(func(syscall.Signal) { calls++ })
	signals.detach()
	incoming <- unix.SIGHUP
	<-signals.Context().Done()
	if calls != 0 {
		t.Fatalf("passed on after detach: %d", calls)
	}
}

func TestCleanRemovesEscapes(t *testing.T) {
	cases := map[string]string{
		"\x1b[1;31merror:\x1b[0m failed\r\n": "error: failed",
		"\x1b]0;title\x07plain":              "plain",
		"\x1b]8;;http://x\x1b\\link":         "link",
		"tab\there":                          "tab\there",
	}
	for raw, want := range cases {
		if got := Clean(raw); got != want {
			t.Errorf("Clean(%q) = %q, want %q", raw, got, want)
		}
	}
}

type recordingSink struct {
	mutex sync.Mutex
	lines []string
	fail  error
}

func (sink *recordingSink) Line(stream Stream, text string) error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	sink.lines = append(sink.lines, stream.String()+":"+text)
	return sink.fail
}

func TestLineReaderReadsEveryLineToTheEnd(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", maxLine+100)
	go func() {
		fmt.Fprintf(write, "one\n\x1b[32mtwo\x1b[0m\r\n%s\nlast without end", long)
		write.Close()
	}()
	sink := &recordingSink{fail: errors.New("the sink is full")}
	reading := startOutput(sink, []*lineReader{{file: read, stream: Stderr}})
	unread, problem := reading.finish(make(chan time.Time), time.Second)
	if unread != OverlongLine || problem == nil {
		t.Fatalf("unread %q, problem %v", unread, problem)
	}
	if len(sink.lines) != 5 || sink.lines[0] != "stderr:one" || sink.lines[1] != "stderr:two" ||
		len(sink.lines[2]) != len("stderr:")+maxLine || sink.lines[3] != "stderr:"+strings.Repeat("x", 100) ||
		sink.lines[4] != "stderr:last without end" {
		t.Fatalf("lines %d %q", len(sink.lines), sink.lines[:2])
	}
}

// A line longer than the bound is not cut off: its rest reaches the sink as further pieces (a veto
// at its end is seen) and the output counts as not wholly read; a line exactly at the bound is
// whole.
func TestAnOverlongLineReachesTheSinkInPieces(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		pieces []string
		unread string
	}{
		{"a veto beyond the bound", "Executed 3 tests\n" + strings.Repeat("x", maxLine+5) + "FATAL\r\nend\n",
			[]string{"Executed 3 tests", strings.Repeat("x", maxLine), "xxxxxFATAL", "end"}, OverlongLine},
		{"a line exactly at the bound", strings.Repeat("y", maxLine) + "\r\n", []string{strings.Repeat("y", maxLine)}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				_, _ = write.WriteString(c.text)
				_ = write.Close()
			}()
			sink := &recordingSink{}
			unread, _ := startOutput(sink, []*lineReader{{file: read, stream: Stdout}}).finish(make(chan time.Time), time.Second)
			if unread != c.unread || len(sink.lines) != len(c.pieces) {
				t.Fatalf("unread %q, %d lines", unread, len(sink.lines))
			}
			for index, piece := range c.pieces {
				if sink.lines[index] != "stdout:"+piece {
					t.Fatalf("line %d is %d bytes, want %d", index, len(sink.lines[index]), len("stdout:"+piece))
				}
			}
		})
	}
}

func TestOutputHeldOpenIsUnreadAfterTheBound(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	reading := startOutput(&recordingSink{}, []*lineReader{{file: read, stream: Stdout}})
	fmt.Fprintln(write, "one")
	bound := make(chan time.Time, 1)
	bound <- time.Time{}
	unread, _ := reading.finish(bound, 10*time.Second)
	if !strings.Contains(unread, "not read to its end within 10 s") {
		t.Fatalf("unread %q", unread)
	}
}

func firedBound() <-chan time.Time {
	bound := make(chan time.Time, 1)
	bound <- time.Time{}
	return bound
}

// The queued words reach a free writer before close returns (a WARNING before the outcome line).
func TestSayQueueCloseWritesWhatIsQueuedFirst(t *testing.T) {
	var out lockedBuffer
	queue := newSayQueue(&out)
	say := sayer(queue, "jst")
	say("one")
	say("WARNING: two %d", 2)
	if dropped := queue.close(make(chan time.Time)); dropped != 0 || out.String() != "jst: one\njst: WARNING: two 2\n" {
		t.Fatalf("dropped %d, wrote %q", dropped, out.String())
	}
	_, _ = queue.Write([]byte("late\n"))
	if dropped := queue.close(make(chan time.Time)); dropped != 1 {
		t.Fatalf("a line after the close: %d", dropped)
	}
}

// A writer nobody unblocks: close returns once its bound fires (the run's clock: 2 s), the lines
// still queued counted as dropped.
func TestSayQueueCloseGivesUpOnABlockedWriterAtTheBound(t *testing.T) {
	stream := blockedWriter{began: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(stream.release)
	queue := newSayQueue(stream)
	say := sayer(queue, "jst")
	say("held in the write")
	<-stream.began
	say("queued behind it")
	clock := newFakeClock()
	returned := make(chan int, 1)
	go func() { returned <- queue.close(clock.After(sayBound)) }()
	<-clock.waiting
	select {
	case <-returned:
		t.Fatal("close returned before the bound")
	default:
	}
	clock.advance(sayBound)
	if dropped := <-returned; dropped != 1 {
		t.Fatalf("dropped %d", dropped)
	}
}

func TestOutcomeStatus(t *testing.T) {
	if (Outcome{Code: 7}).Status() != 7 || (Outcome{Signal: unix.SIGTERM}).Status() != 143 {
		t.Fatal("status")
	}
	if Seconds(30*time.Second) != "30 s" || Seconds(300*time.Millisecond) != "0.3 s" {
		t.Fatalf("seconds %q %q", Seconds(30*time.Second), Seconds(300*time.Millisecond))
	}
	if Names([]procs.Process{{PID: 9, Name: "b"}, {PID: 3, Name: "a"}}) != "a[3],b[9]" {
		t.Fatal("names")
	}
}

func TestReadSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions")
	if err := RecordSession(path, 4242, 1_700_000_000_123_456); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".more", []byte("junk\n12 x\n1 5\n77 1700000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ReadSessions(path)
	if len(got) != 1 || got[0] != (RecordedSession{Session: 4242, Start: 1_700_000_000_123_456}) {
		t.Fatalf("read %v", got)
	}
	more := ReadSessions(path + ".more")
	if len(more) != 1 || more[0] != (RecordedSession{Session: 77, Start: 1_700_000_000}) {
		t.Fatalf("read %v", more)
	}
	if ReadSessions(filepath.Join(t.TempDir(), "none")) != nil || RecordSession("", 1, 1) != nil {
		t.Fatal("no file is no sessions")
	}
}

func TestStopSessionsStopsOnlyTheRecordedRunsSessions(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(300, 1, 300, 300, "swift-test")
	w.add(301, 300, 301, 300, "xctest")
	w.add(400, 1, 400, 400, "someone-else") // its id was given to another process: not the recorded run's
	w.add(501, 1, 501, 500, "orphan")       // session 500's leader is gone: its members are still the run's
	path := filepath.Join(t.TempDir(), "sessions")
	lines := fmt.Sprintf("300 %d\n400 99\n500 %d\n", w.processes[300].row.StartMicros, int64(1))
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	var words bytes.Buffer
	survivors, err := stopSessions(path, *w.system(), sayer(&words, "jst lock"))
	if err != nil || len(survivors) != 0 {
		t.Fatalf("survivors %v, err %v", survivors, err)
	}
	table, _ := w.read()
	if _, present := table[400]; !present || len(table) != 1 {
		t.Fatalf("left %v", table)
	}
	if !strings.Contains(words.String(), "session 300 still has swift-test[300],xctest[301]") {
		t.Fatalf("said %q", words.String())
	}
}

func TestSessionTreeByItsLeadersStart(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(300, 1, 300, 300, "leader")
	table, _ := w.read()
	if sessionTree(table, RecordedSession{Session: 300, Start: table[300].StartMicros}) == nil {
		t.Fatal("the leader's start does not name it")
	}
	if sessionTree(table, RecordedSession{Session: 300, Start: table[300].Start}) != nil {
		t.Fatal("whole seconds name the leader")
	}
	if sessionTree(table, RecordedSession{Session: 300, Start: 5}) != nil {
		t.Fatal("another start is another process")
	}
}

func TestForwardInAGroupSignalsTheGroupUntilTheReap(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 50, "make").ignoresTerm = true
	w.add(101, 100, 100, 50, "swift").ignoresTerm = true
	run := newFakeRun(w, Group, 100)
	run.forward(unix.SIGINT)
	w.end(100)
	run.forward(unix.SIGTERM) // ended, not reaped: the group is still the run's
	run.reap()
	run.forward(unix.SIGTERM) // reaped: nothing is the run's to signal
	signals := w.sentSignals()
	if len(signals) != 2 || signals[0].target != -100 || signals[0].sig != unix.SIGINT || signals[1].sig != unix.SIGTERM {
		t.Fatalf("sent %v", signals)
	}
}

func TestForwardInASessionReachesMembersOutsideTheGroup(t *testing.T) {
	w := newWorld(newFakeClock())
	w.add(100, 1, 100, 100, "swift-test").ignoresTerm = true
	w.add(101, 100, 101, 100, "xctest").ignoresTerm = true // a group of its own, in the session
	run := newFakeRun(w, Session, 100)
	run.forward(unix.SIGTERM)
	var targets []int
	for _, each := range w.sentSignals() {
		targets = append(targets, each.target)
	}
	if len(targets) != 2 || targets[0] != -100 || targets[1] != 101 {
		t.Fatalf("sent to %v", targets)
	}
}

func TestCloserUndoesLastFirstAndOnce(t *testing.T) {
	var order []int
	closer := &Closer{}
	closer.Add(func() { order = append(order, 1) })
	closer.Add(func() { order = append(order, 2) })
	closer.Close()
	closer.Close()
	if len(order) != 2 || order[0] != 2 || order[1] != 1 {
		t.Fatalf("order %v", order)
	}
}
