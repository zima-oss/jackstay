package background

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/proctree"
	"github.com/zima-oss/jackstay/internal/supervise"
)

func TestParseBg(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		ok    bool
		check func(request) bool
	}{
		{"plain", []string{"--", "sleep", "1"}, true, func(asked request) bool {
			return !asked.timed && asked.every == checkEvery && reflect.DeepEqual(asked.argv, []string{"sleep", "1"})
		}},
		{"every option", []string{"--for", "2.5", "--name", "srv", "--keep", "--watch", "42", "--check-every", "0.1", "--", "x"},
			true, func(asked request) bool {
				return asked.timed && asked.minutes == 2.5 && *asked.name == "srv" && asked.keep && asked.watch == 42 &&
					asked.every == 100*time.Millisecond
			}},
		{"a name may be empty", []string{"--name", "", "--", "x"}, true, func(asked request) bool { return *asked.name == "" }},
		{"options after the command are the command's", []string{"--", "x", "--for", "0"}, true, func(asked request) bool {
			return reflect.DeepEqual(asked.argv, []string{"x", "--for", "0"})
		}},
		{"no --", []string{"sleep", "1"}, false, nil},
		{"nothing after --", []string{"--for", "1", "--"}, false, nil},
		{"--for 0", []string{"--for", "0", "--", "true"}, false, nil},
		{"--for negative", []string{"--for", "-1", "--", "true"}, false, nil},
		{"--for nan", []string{"--for", "nan", "--", "true"}, false, nil},
		{"--for a word", []string{"--for", "soon", "--", "true"}, false, nil},
		{"--for without a value", []string{"--for", "--", "true"}, false, nil},
		{"--check-every 0", []string{"--check-every", "0", "--", "true"}, false, nil},
		{"--watch a word", []string{"--watch", "me", "--", "true"}, false, nil},
		{"an unknown option", []string{"--fast", "--", "true"}, false, nil},
	}
	for _, each := range cases {
		t.Run(each.name, func(t *testing.T) {
			asked, ok := parseBg(each.args)
			if ok != each.ok {
				t.Fatalf("ok %v, want %v", ok, each.ok)
			}
			if ok && !each.check(asked) {
				t.Fatalf("parsed %+v", asked)
			}
		})
	}
}

func TestBgRefusesABadRequestWithTwo(t *testing.T) {
	testHome(t)
	var stderr bytes.Buffer
	if code := bgCommand([]string{"--for", "0", "--", "true"}, nil, io.Discard, &stderr); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "usage: jst bg") {
		t.Fatalf("said %q", stderr.String())
	}
}

func TestTheDeadlineIsForElseTheDefaultCappedByTheMaximum(t *testing.T) {
	cases := []struct {
		name    string
		asked   request
		minutes float64
		capped  bool
	}{
		{"the default", request{}, 30, false},
		{"--for", request{minutes: 5, timed: true}, 5, false},
		{"--for above the maximum", request{minutes: 500, timed: true}, 240, true},
	}
	for _, each := range cases {
		run := &backgroundRun{asked: each.asked}
		run.plan(defaultMinutes, maxMinutes)
		if run.minutes != each.minutes || run.capped != each.capped {
			t.Fatalf("%s: %v %v", each.name, run.minutes, run.capped)
		}
	}
}

func TestTheLimitsComeFromTheProjectsProcsSection(t *testing.T) {
	testHome(t)
	project := t.TempDir()
	write := func(text string) {
		if err := os.WriteFile(filepath.Join(project, "jackstay.json"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"jackstay_schema": 2, "procs": {"default_minutes": 10, "max_minutes": 60}}`)
	var said bytes.Buffer
	if fallback, ceiling := limits(project, &said); fallback != 10 || ceiling != 60 || said.Len() != 0 {
		t.Fatalf("limits %v %v %q", fallback, ceiling, said.String())
	}
	write(`{"jackstay_schema": 2, "procs": {"default_minutes": 100, "max_minutes": 60}}`)
	fallback, ceiling := limits(project, &said)
	if fallback != defaultMinutes || ceiling != maxMinutes || !strings.Contains(said.String(), "the default deadline applies") {
		t.Fatalf("a broken section: %v %v %q", fallback, ceiling, said.String())
	}
	if fallback, ceiling := limits(t.TempDir(), &said); fallback != defaultMinutes || ceiling != maxMinutes {
		t.Fatalf("no jackstay.json: %v %v", fallback, ceiling)
	}
}

// The command's session in the fake world: leader 500 and a child, started by this command.
const (
	leaderPID = 500
	childPID  = 501
	testID    = "1800000000-900"
)

type harness struct {
	world   *world
	clock   *fakeClock
	place   store
	run     *backgroundRun
	said    *bytes.Buffer
	control *fakeControl
}

func newHarness(t *testing.T, asked request) *harness {
	t.Helper()
	_, place := testHome(t)
	fake, clock := newWorld(), newFakeClock()
	withCaller(fake)
	fake.add(leaderPID, selfPID, leaderPID, leaderPID, 5000, "sh")
	fake.add(childPID, leaderPID, leaderPID, leaderPID, 5001, "sleep")
	if asked.every == 0 {
		asked.every = checkEvery
	}
	if asked.argv == nil {
		asked.argv = []string{"sh", "-c", "sleep 300 & sleep 300"}
	}
	run := &backgroundRun{asked: asked, host: fakeMachine(fake, clock), place: place, here: "/work"}
	run.plan(defaultMinutes, maxMinutes)
	return &harness{world: fake, clock: clock, place: place, run: run, said: &bytes.Buffer{},
		control: &fakeControl{world: fake, group: leaderPID}}
}

// begin starts the supervision as jst bg does once the command runs; the words are kept.
func (h *harness) begin(t *testing.T) *watcher {
	t.Helper()
	table, _ := h.world.read()
	say := newTeller(h.said)
	watching := h.run.begin(testID, leaderPID, h.control, h.clock.Now(), h.run.watched(table), table, say)
	t.Cleanup(func() { say.close(closedBound()) })
	return watching
}

func closedBound() <-chan time.Time {
	channel := make(chan time.Time, 1)
	channel <- time.Time{}
	return channel
}

func (h *harness) entry(t *testing.T) Entry {
	t.Helper()
	data, err := os.ReadFile(h.place.entryPath(testID))
	if err != nil {
		t.Fatal(err)
	}
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestTheEntryHoldsEveryField(t *testing.T) {
	h := newHarness(t, request{name: pointer("server")})
	noteSession(t, h.place, claudePID, 1000, "session-1")
	watching := h.begin(t)
	entry := h.entry(t)
	if entry.ID != testID || entry.nameOf() != "server" || entry.Supervisor != selfPID || *entry.SupervisorStart != 9000 ||
		entry.Group != leaderPID || *entry.LeaderStart != 5000 || entry.Keep || entry.sessionOf() != "session-1" ||
		entry.Cwd != "/work" || entry.Status != "running" || entry.Held != nil {
		t.Fatalf("entry %+v", entry)
	}
	if entry.Started != 1_800_000_000 || *entry.Deadline != 1_800_000_000+30*60 {
		t.Fatalf("times %v %v", entry.Started, *entry.Deadline)
	}
	if entry.Watched == nil || entry.Watched.PID != claudePID || *entry.Watched.Start != 1000 || entry.Watched.Name != "claude" {
		t.Fatalf("watched %+v", entry.Watched)
	}
	if watching.watched.micros != 1000*1_000_000+7 {
		t.Fatal("identity inside is not by microseconds")
	}
	raw, _ := os.ReadFile(h.place.entryPath(testID))
	var keys map[string]any
	_ = json.Unmarshal(raw, &keys)
	for _, key := range []string{"id", "name", "supervisor", "supervisor_start", "group", "started", "leader_start", "deadline",
		"keep", "session", "watched", "cwd", "command", "status"} {
		if _, present := keys[key]; !present {
			t.Fatalf("no %s in %s", key, raw)
		}
	}
	start := events(t, h.place)[0]
	if start["event"] != "start" || start["id"] != testID || start["group"] != float64(leaderPID) || start["session"] != "session-1" {
		t.Fatalf("start %v", start)
	}
}

func TestKeptHasNoDeadlineAndNoSessionCheck(t *testing.T) {
	h := newHarness(t, request{keep: true, name: pointer("server")})
	watching := h.begin(t)
	entry := h.entry(t)
	if !entry.Keep || entry.Deadline != nil || entry.Watched != nil {
		t.Fatalf("entry %+v", entry)
	}
	h.world.remove(claudePID)
	h.clock.advance(1000 * time.Hour)
	if watching.timedCheck() || watching.askedCheck() {
		t.Fatal("a kept group was stopped by a deadline or a session")
	}
	if !h.world.has(leaderPID) {
		t.Fatal("the group was signalled")
	}
}

func TestTheStartWordsSayWhatSupervises(t *testing.T) {
	h := newHarness(t, request{minutes: 500, timed: true})
	words := h.run.startWords(Entry{ID: testID, Group: leaderPID}, &watcher{deadline: h.clock.Now()}, nil, false)
	for _, part := range []string{"jst bg: " + testID + " started, group 500", "240 min, the most procs.max_minutes allows",
		"supervised by the deadline only", "NOT listed"} {
		if !strings.Contains(words, part) {
			t.Fatalf("%q lacks %q", words, part)
		}
	}
	kept := &backgroundRun{asked: request{keep: true}}
	if words := kept.startWords(Entry{ID: testID, Group: 5}, &watcher{}, nil, true); !strings.Contains(words, "kept: no deadline") ||
		!strings.Contains(words, "jst procs stop "+testID) {
		t.Fatalf("kept words %q", words)
	}
}

func TestAnUnwritableFolderStillSupervises(t *testing.T) {
	h := newHarness(t, request{})
	h.run.place = store{folder: "/dev/null/procs"}
	watching := h.begin(t)
	h.clock.advance(31 * time.Minute)
	if !watching.timedCheck() || h.world.has(leaderPID) {
		t.Fatal("the deadline did not hold without a file")
	}
}

func TestAnUnregisteredKeptProcessIsStopped(t *testing.T) {
	h := newHarness(t, request{keep: true})
	h.run.place = store{folder: "/dev/null/procs"}
	h.begin(t)
	if h.world.has(leaderPID) || h.control.stopCount() != 1 {
		t.Fatal("an unregistered kept group was left running")
	}
}

func TestTheRegistryMasksRecordsFromOlderVersionsWhenRead(t *testing.T) {
	const marker = "audit-fixture-value"
	h := newHarness(t, request{})
	h.begin(t)
	legacy := h.entry(t)
	legacy.Command = []string{"tool", "run", "--password", marker}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.place.entryPath(testID), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	entry, ok := h.place.read(testID)
	if !ok || len(entry.Command) == 0 || strings.Contains(strings.Join(entry.Command, " "), marker) || entry.Command[0] != "tool" {
		t.Fatal("the legacy entry leaked an argument or hid the executable")
	}
}

func TestTheRegistryMasksRecordedArguments(t *testing.T) {
	const marker = "audit-fixture-value"
	for _, test := range []struct {
		mode string
		hide string
	}{
		{"masked", marker},
		{"skeleton", "ordinary-argument"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			args := []string{"tool", "run", "--password", marker, "ordinary-argument"}
			h := newHarness(t, request{argv: args})
			h.run.here = t.TempDir()
			config := `{"jackstay_schema":2,"telemetry":{"commands":"` + test.mode + `"}}`
			if err := os.WriteFile(filepath.Join(h.run.here, "jackstay.json"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			h.begin(t)
			raw, err := os.ReadFile(h.place.entryPath(testID))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(test.hide)) || bytes.Contains(raw, []byte(marker)) ||
				h.run.asked.argv[3] != marker {
				t.Fatal("the registry persisted unmasked arguments or changed the launch arguments")
			}
		})
	}
}

func TestTheRegistryKeepsIncompleteCleanup(t *testing.T) {
	cases := map[string]supervise.Outcome{
		"no process table": {Unconfirmed: true},
		"surviving child":  {Survivors: []proctree.Survivor{{Member: proctree.Member{PID: childPID}}}},
		"foreign child":    {Foreign: []procs.Process{{PID: childPID}}},
	}
	for name, outcome := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, request{})
			watching := h.begin(t)
			if err := h.place.askStop(testID); err != nil {
				t.Fatal(err)
			}
			if status := watching.finish(outcome, "asked"); status != 1 {
				t.Fatalf("cleanup failure returned %d", status)
			}
			if entry := h.entry(t); entry.Status != stateCleanupIncomplete || h.place.stopAsked(testID) {
				t.Fatalf("entry %+v, stop pending %v", entry, h.place.stopAsked(testID))
			}
		})
	}
}

func TestConfirmedCleanupRemovesTheRegistryEntry(t *testing.T) {
	h := newHarness(t, request{})
	watching := h.begin(t)
	if status := watching.finish(supervise.Outcome{Code: 42}, "ended"); status != 42 || h.place.listed(testID) {
		t.Fatalf("exit %d, listed %v", status, h.place.listed(testID))
	}
}

func TestTheDeadlineStopsTheGroup(t *testing.T) {
	h := newHarness(t, request{})
	watching := h.begin(t)
	h.clock.advance(29 * time.Minute)
	if watching.timedCheck() {
		t.Fatal("stopped before the deadline")
	}
	h.clock.advance(time.Minute)
	if !watching.timedCheck() {
		t.Fatal("not stopped at the deadline")
	}
	if h.world.has(leaderPID) || h.world.has(childPID) || watching.reason != "deadline" {
		t.Fatalf("group left, reason %q", watching.reason)
	}
	if got := eventPairs(t, h.place); !reflect.DeepEqual(got, [][2]string{{"start", ""}, {"stop", "deadline"}}) {
		t.Fatalf("events %v", got)
	}
	if stop := events(t, h.place)[1]; stop["looked"] != true {
		t.Fatalf("stop %v", stop)
	}
}

func TestTheDeadlineIsWallClockAfterASleep(t *testing.T) {
	h := newHarness(t, request{every: time.Hour})
	watching := h.begin(t)
	h.clock.advance(5 * time.Hour) // the Mac slept through the deadline and the checks
	if !watching.timedCheck() || watching.reason != "deadline" {
		t.Fatal("not stopped at the first check after waking")
	}
}

func TestAGoneSessionStopsTheGroup(t *testing.T) {
	h := newHarness(t, request{every: 100 * time.Millisecond})
	watching := h.begin(t)
	h.clock.advance(100 * time.Millisecond)
	if watching.timedCheck() {
		t.Fatal("stopped while the session lives")
	}
	h.world.remove(claudePID)
	h.clock.advance(50 * time.Millisecond)
	if watching.timedCheck() {
		t.Fatal("checked before its time")
	}
	h.clock.advance(50 * time.Millisecond)
	if !watching.timedCheck() || watching.reason != "session-gone" || h.world.has(leaderPID) {
		t.Fatalf("reason %q", watching.reason)
	}
}

func TestASessionProcessIdGivenAgainIsNotTheSession(t *testing.T) {
	h := newHarness(t, request{every: time.Second})
	watching := h.begin(t)
	h.world.add(claudePID, 1, claudePID, claudePID, 1000, "claude")
	h.world.mutex.Lock()
	row := h.world.rows[claudePID]
	row.StartMicros++ // the same whole second, another process
	h.world.rows[claudePID] = row
	h.world.mutex.Unlock()
	h.clock.advance(time.Second)
	if !watching.timedCheck() || watching.reason != "session-gone" {
		t.Fatal("a later process of the session's id kept the group alive")
	}
}

func TestAWatchedProcessIsGivenByWatch(t *testing.T) {
	h := newHarness(t, request{watch: 777, every: time.Second})
	h.world.add(777, 1, 777, 777, 7000, "sleep")
	watching := h.begin(t)
	if entry := h.entry(t); entry.Watched.PID != 777 || entry.Watched.Name != "given" || *entry.Watched.Start != 7000 ||
		entry.Session != nil {
		t.Fatalf("watched %+v", entry.Watched)
	}
	h.world.remove(777)
	h.clock.advance(time.Second)
	if !watching.timedCheck() || watching.reason != "session-gone" {
		t.Fatal("the watched process's end did not stop the group")
	}
}

func TestTheSessionIsTheNearestNotedAncestorElseTheNearestThatIsNotAShell(t *testing.T) {
	h := newHarness(t, request{})
	table, _ := h.world.read()
	found := h.run.watched(table)
	if found == nil || found.session != "" || found.pid != claudePID || found.name != "claude" {
		t.Fatalf("without a note: %+v", found)
	}
	noteSession(t, h.place, claudePID, 1000, "session-1")
	if found := h.run.watched(table); found.session != "session-1" || found.pid != claudePID {
		t.Fatalf("with a note: %+v", found)
	}
	if found := h.run.watched(nil); found != nil {
		t.Fatalf("without a table: %+v", found)
	}
}

func TestAGroupWritingAFileIsHeldAndAskedAgain(t *testing.T) {
	h := newHarness(t, request{every: time.Minute})
	h.world.setHeld([]string{"/work/data.bin"}, false)
	watching := h.begin(t)
	h.clock.advance(30 * time.Minute)
	if watching.timedCheck() {
		t.Fatal("a writer was stopped")
	}
	if entry := h.entry(t); entry.Status != "held" || !reflect.DeepEqual(entry.Held, []string{"/work/data.bin"}) {
		t.Fatalf("entry %+v", entry)
	}
	h.clock.advance(30 * time.Second)
	if watching.timedCheck() || h.world.heldAsked != 1 {
		t.Fatalf("asked again before the next check (%d)", h.world.heldAsked)
	}
	h.clock.advance(30 * time.Second)
	if watching.timedCheck() || h.world.heldAsked != 2 {
		t.Fatalf("not asked again at the next check (%d)", h.world.heldAsked)
	}
	h.world.setHeld(nil, false)
	h.clock.advance(time.Minute)
	if !watching.timedCheck() || h.world.has(leaderPID) {
		t.Fatal("not stopped once nothing was held")
	}
	if got := eventPairs(t, h.place); !reflect.DeepEqual(got, [][2]string{{"start", ""}, {"held", "deadline"}, {"stop", "deadline"}}) {
		t.Fatalf("events %v (held is logged once while the files are the same)", got)
	}
}

func TestNoAnswerFromLsofStopsAllTheSame(t *testing.T) {
	h := newHarness(t, request{})
	h.world.setHeld(nil, true)
	watching := h.begin(t)
	h.clock.advance(30 * time.Minute)
	if !watching.timedCheck() || h.world.has(leaderPID) {
		t.Fatal("not stopped")
	}
	if stop := events(t, h.place)[1]; stop["looked"] != false {
		t.Fatalf("stop %v", stop)
	}
}

func TestAStopRequestIsAnsweredWhateverTheDeadline(t *testing.T) {
	h := newHarness(t, request{keep: true})
	watching := h.begin(t)
	if err := h.place.askStop(testID); err != nil {
		t.Fatal(err)
	}
	if !watching.askedCheck() || watching.reason != "asked" || h.world.has(leaderPID) {
		t.Fatal("the request was not answered")
	}
	if got := eventPairs(t, h.place); !reflect.DeepEqual(got, [][2]string{{"start", ""}, {"stop", "asked"}}) {
		t.Fatalf("events %v", got)
	}
}

// The deadline went through the table — none on linux, or a failed
// read — and a failed first row of the leader made every later stop a silent no-op that ended
// the watcher. Every stop goes through the supervisor's hold on its unreaped child now.
func TestTheDeadlineStopsWithoutATableOrALeaderRow(t *testing.T) {
	h := newHarness(t, request{})
	h.world.readFails = true // no table and no row from the start
	watching := h.begin(t)
	if entry := h.entry(t); entry.LeaderStart != nil {
		t.Fatalf("leader start %v", *entry.LeaderStart)
	}
	h.clock.advance(30 * time.Minute)
	if !watching.timedCheck() || watching.reason != "deadline" || h.control.stopCount() != 1 || h.world.has(leaderPID) {
		t.Fatalf("reason %q, stops %d", watching.reason, h.control.stopCount())
	}
}

func TestAStopNotMadeKeepsTheWatcherGoing(t *testing.T) {
	h := newHarness(t, request{})
	watching := h.begin(t)
	h.control.refuse = true
	h.clock.advance(30 * time.Minute)
	if watching.timedCheck() || watching.reason != "" {
		t.Fatalf("a stop not made ended the watcher (%q)", watching.reason)
	}
	if got := eventPairs(t, h.place); !reflect.DeepEqual(got, [][2]string{{"start", ""}}) {
		t.Fatalf("a stop not made was logged: %v", got)
	}
	h.control.refuse = false
	h.clock.advance(slice)
	if !watching.timedCheck() || watching.reason != "deadline" || h.world.has(leaderPID) {
		t.Fatalf("not asked again: %q", watching.reason)
	}
	if err := h.place.askStop(testID); err != nil {
		t.Fatal(err)
	}
	h.control.refuse = true
	if watching.askedCheck() {
		t.Fatal("a request not answered ended its loop")
	}
	if !h.place.stopAsked(testID) {
		t.Fatal("the request was taken back")
	}
}

// The look at the open files asked about the group only, while the stop
// signals the session's members in other groups too. The look is asked about exactly that set.
func TestTheLookCoversWhatTheStopSignals(t *testing.T) {
	h := newHarness(t, request{})
	h.control.others = []int{601, 602}
	watching := h.begin(t)
	h.world.setHeld([]string{"/work/written-by-601.bin"}, false)
	h.clock.advance(30 * time.Minute)
	if watching.timedCheck() || !reflect.DeepEqual(h.world.heldOthers, []int{601, 602}) {
		t.Fatalf("asked about %v", h.world.heldOthers)
	}
}

// A slow lsof held the only loop that reads the stop file, so a request
// went unseen. The stop file has a loop of its own: the request is answered while the look waits.
func TestAStopRequestIsAnsweredWhileTheLookIsSlow(t *testing.T) {
	h := newHarness(t, request{})
	watching := h.begin(t)
	h.world.heldGate, h.world.heldEntered = make(chan struct{}), make(chan struct{}, 1)
	ended := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		watching.watch(ended)
		close(finished)
	}()
	<-h.clock.waiting
	<-h.clock.waiting
	h.clock.advance(30 * time.Minute)
	<-h.world.heldEntered // the deadline's look is waiting for lsof
	<-h.clock.waiting     // the stop file's loop waits for its next slice
	if err := h.place.askStop(testID); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(slice)
	waitUntil(t, func() bool { return watching.stoppedFor() != "" })
	close(h.world.heldGate)
	<-finished
	if watching.stoppedFor() != "asked" || h.control.stopCount() != 1 {
		t.Fatalf("reason %q, stops %d", watching.stoppedFor(), h.control.stopCount())
	}
	if got := eventPairs(t, h.place); !reflect.DeepEqual(got, [][2]string{{"start", ""}, {"stop", "asked"}}) {
		t.Fatalf("events %v", got)
	}
}

// waitUntil waits for a condition another goroutine makes true; it decides nothing by time (the
// test's own timeout ends a broken one).
func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	for !condition() {
		runtime.Gosched()
	}
}

func TestAStopRequestIsAnsweredWithinASlice(t *testing.T) {
	h := newHarness(t, request{keep: true})
	watching := h.begin(t)
	ended := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		watching.watch(ended)
		close(finished)
	}()
	<-h.clock.waiting
	if err := h.place.askStop(testID); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(slice)
	<-finished
	if watching.reason != "asked" {
		t.Fatalf("reason %q", watching.reason)
	}
}

func TestTheWatcherEndsWithTheCommand(t *testing.T) {
	h := newHarness(t, request{})
	watching := h.begin(t)
	ended := make(chan struct{})
	close(ended)
	watching.watch(ended)
	if watching.reason != "" {
		t.Fatalf("reason %q", watching.reason)
	}
}

// blockedWriter never returns from a write until released: a standard error nobody reads.
type blockedWriter struct{ release chan struct{} }

func (writer blockedWriter) Write(data []byte) (int, error) {
	<-writer.release
	return len(data), nil
}

func TestAFullStandardErrorDoesNotBlockTheSupervision(t *testing.T) {
	writer := blockedWriter{release: make(chan struct{})}
	say := newTeller(writer)
	for range 1000 {
		say.tell("a line")
		_, _ = say.Write([]byte("supervise's line\n"))
	}
	say.close(closedBound())
	say.tell("after the end")
	close(writer.release)
}

func TestTheTellerWritesItsLinesInOrder(t *testing.T) {
	var out bytes.Buffer
	say := newTeller(&out)
	say.tell("one")
	_, _ = say.Write([]byte("two\n"))
	say.close(make(chan time.Time))
	if out.String() != "one\ntwo\n" {
		t.Fatalf("wrote %q", out.String())
	}
}

func TestAStartRefusedBeforeTheCommandWritesNothing(t *testing.T) {
	_, place := testHome(t)
	h := &backgroundRun{asked: request{argv: []string{"no-such-command-anywhere-jst"}, every: checkEvery}, host: realMachine(),
		place: place, stderr: io.Discard}
	h.plan(defaultMinutes, maxMinutes)
	if code := h.run(); code != statusNotFound {
		t.Fatalf("exit %d", code)
	}
	if found, _ := os.ReadDir(place.folder); len(found) != 0 {
		t.Fatalf("left %v", found)
	}
}
