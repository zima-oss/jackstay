package background

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/procs"
)

type lookHarness struct {
	world  *world
	clock  *fakeClock
	place  store
	look   *looker
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

// newLookHarness is a `jst procs` run under a Claude Code process noted as session (none: "").
func newLookHarness(t *testing.T, session string) *lookHarness {
	t.Helper()
	_, place := testHome(t)
	fake, clock := newWorld(), newFakeClock()
	withCaller(fake)
	if session != "" {
		noteSession(t, place, claudePID, 1000, session)
	}
	h := &lookHarness{world: fake, clock: clock, place: place, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	h.look = &looker{host: fakeMachine(fake, clock), place: place, stdout: h.stdout, stderr: h.stderr}
	return h
}

// supervised adds a jst bg (pid supervisor) and its group (leader group) to the world and lists it.
func (h *lookHarness) supervised(t *testing.T, supervisor, group int, name, session string, deadlineIn time.Duration) Entry {
	t.Helper()
	h.world.add(supervisor, shellPID, shellPID, shellPID, int64(supervisor)*10, "jst")
	h.world.add(group, supervisor, group, group, int64(group)*10, "sleep")
	return h.listed(t, supervisor, group, name, session, deadlineIn)
}

// listed writes an entry for a supervisor and group as jst bg would have.
func (h *lookHarness) listed(t *testing.T, supervisor, group int, name, session string, deadlineIn time.Duration) Entry {
	t.Helper()
	now := epoch(h.clock.Now())
	entry := Entry{ID: "1800000000-" + strconv.Itoa(supervisor), Supervisor: supervisor, SupervisorStart: pointer(int64(supervisor) * 10),
		Group: group, Started: now - 60, LeaderStart: pointer(int64(group) * 10), Cwd: "/work", Command: []string{"sleep", "300"},
		Status: "running"}
	if name != "" {
		entry.Name = &name
	}
	if session != "" {
		entry.Session = &session
	}
	if deadlineIn == 0 {
		entry.Keep = true
	} else {
		entry.Deadline = pointer(now + deadlineIn.Seconds())
	}
	if !h.place.write(entry) {
		t.Fatal("the entry was not written")
	}
	return entry
}

func (h *lookHarness) listJSON(t *testing.T) []map[string]any {
	t.Helper()
	h.stdout.Reset()
	if code := h.look.list(true, true); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var listed []map[string]any
	if err := json.Unmarshal(h.stdout.Bytes(), &listed); err != nil {
		t.Fatalf("%v: %q", err, h.stdout.String())
	}
	return listed
}

func TestListShowsThisSessionsEntriesAndCountsTheOthers(t *testing.T) {
	h := newLookHarness(t, "mine")
	h.supervised(t, 600, 610, "server", "mine", 10*time.Minute)
	h.supervised(t, 601, 611, "server", "other", 0)
	if code := h.look.list(false, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	text := h.stdout.String()
	if !strings.Contains(text, "1800000000-600  server  running  age 1m00s  deadline in 10m00s  session mine  /work  sleep 300") ||
		strings.Contains(text, "1800000000-601") || !strings.Contains(text, "(1 more in other sessions: jst procs --all)") {
		t.Fatalf("listed %q", text)
	}
	listed := h.listJSON(t)
	if len(listed) != 2 || listed[0]["state"] != "running" || listed[1]["deadline"] != nil || listed[1]["keep"] != true {
		t.Fatalf("--all --json %v", listed)
	}
}

func TestListWithoutAnySaysSo(t *testing.T) {
	h := newLookHarness(t, "mine")
	h.look.list(false, false)
	if h.stdout.String() != "nothing supervised in this session\n" {
		t.Fatalf("%q", h.stdout.String())
	}
	h.stdout.Reset()
	if h.look.list(true, true); strings.TrimSpace(h.stdout.String()) != "[]" {
		t.Fatalf("%q", h.stdout.String())
	}
}

func TestAnEntryWhoseSupervisorAndGroupAreGoneIsCleaned(t *testing.T) {
	h := newLookHarness(t, "")
	h.listed(t, 620, 621, "", "", time.Minute)
	if listed := h.listJSON(t); len(listed) != 0 {
		t.Fatalf("listed %v", listed)
	}
	if h.place.listed("1800000000-620") {
		t.Fatal("the entry stays")
	}
	if got := eventPairs(t, h.place); !reflect.DeepEqual(got, [][2]string{{"cleaned", ""}}) {
		t.Fatalf("events %v", got)
	}
}

func TestAnUnreadableProcessTableDoesNotRemoveTheEntry(t *testing.T) {
	h := newLookHarness(t, "")
	entry := h.listed(t, 620, 621, "", "", time.Minute)
	h.world.readFails = true
	if listed := h.listJSON(t); len(listed) != 1 || listed[0]["state"] != stateUnknown {
		t.Fatalf("unverified entry was omitted: %v", listed)
	}
	if !h.place.listed(entry.ID) {
		t.Fatal("unverified entry was removed")
	}
}

func TestAReusedProcessIdIsNotTheSupervisor(t *testing.T) {
	h := newLookHarness(t, "")
	entry := h.supervised(t, 630, 631, "", "", time.Minute)
	alive := h.look.host.supervisorAlive(h.look.host.table(), entry)
	entry.SupervisorStart = pointer(*entry.SupervisorStart - 1000)
	if !alive || h.look.host.supervisorAlive(h.look.host.table(), entry) {
		t.Fatal("a process of the supervisor's id with another start counts as the supervisor")
	}
}

func TestAnOrphanedGroupIsShownWithTheWayToStopIt(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 640, 641, "left", "", 0)
	h.world.remove(640) // the supervisor died outright
	h.look.list(true, false)
	if text := h.stdout.String(); !strings.Contains(text, "left  orphaned") ||
		!strings.Contains(text, "its supervisor is gone; `jst procs stop 1800000000-640` stops group 641") {
		t.Fatalf("listed %q", text)
	}
	if len(h.world.signalsSent()) != 0 {
		t.Fatal("a kept orphan was signalled by a look")
	}
}

func TestReviewC4AnOrphanPastItsDeadlineIsStoppedByTheNextLook(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 650, 651, "late", "", time.Minute)
	h.world.remove(650)
	h.clock.advance(2 * time.Minute)
	if listed := h.listJSON(t); len(listed) != 0 {
		t.Fatalf("listed %v", listed)
	}
	if h.world.has(651) || h.place.listed("1800000000-650") {
		t.Fatal("the group or its entry stays")
	}
	stop := events(t, h.place)[0]
	if stop["event"] != "stop" || stop["reason"] != orphanedDeadline || stop["gone"] != true || stop["looked"] != true {
		t.Fatalf("stop %v", stop)
	}
}

func TestAnOrphanWritingAFileIsHeldNotStopped(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 660, 661, "writer", "", time.Minute)
	h.world.remove(660)
	h.world.setHeld([]string{"/work/out.bin"}, false)
	h.clock.advance(2 * time.Minute)
	listed := h.listJSON(t)
	if len(listed) != 1 || listed[0]["state"] != "orphaned" || !reflect.DeepEqual(listed[0]["held"], []any{"/work/out.bin"}) {
		t.Fatalf("listed %v", listed)
	}
	if !h.world.has(661) {
		t.Fatal("the writer was stopped")
	}
	h.listJSON(t)
	if got := eventPairs(t, h.place); !reflect.DeepEqual(got, [][2]string{{"held", orphanedDeadline}}) {
		t.Fatalf("events %v (held once while the files are the same)", got)
	}
	h.stdout.Reset()
	h.look.list(true, false)
	if !strings.Contains(h.stdout.String(), "held open for writing: /work/out.bin") {
		t.Fatalf("%q", h.stdout.String())
	}
}

// For an orphan: the look covers the session's members in other groups,
// which the deadline's stop signals too.
func TestAnOrphansLookCoversItsSessionsOtherGroups(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 665, 666, "writer", "", time.Minute)
	h.world.add(667, 666, 667, 666, 6670, "writer") // a group of its own in the session
	h.world.remove(665)
	h.world.setHeld([]string{"/work/out.bin"}, false)
	h.clock.advance(2 * time.Minute)
	h.listJSON(t)
	if !reflect.DeepEqual(h.world.heldOthers, []int{667}) || !h.world.has(667) {
		t.Fatalf("asked about %v", h.world.heldOthers)
	}
}

func TestAnOrphanWhoseLeaderIsNotProvenIsNotSignalledByALook(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 670, 671, "", "", time.Minute)
	h.world.remove(670)
	h.world.add(671, 1, 671, 671, 9999, "other") // the group's id given to another process
	h.clock.advance(2 * time.Minute)
	if listed := h.listJSON(t); len(listed) != 1 || listed[0]["state"] != "orphaned" {
		t.Fatalf("listed %v", listed)
	}
	if len(h.world.signalsSent()) != 0 {
		t.Fatalf("signalled %v", h.world.signalsSent())
	}
}

func TestAnOrphanTheSandboxCannotSignalIsNamedNotSignalled(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 680, 681, "", "", time.Minute)
	h.world.remove(680)
	h.world.denied[681] = true
	h.clock.advance(2 * time.Minute)
	h.look.list(true, false)
	if !strings.Contains(h.stdout.String(), "run outside the sandbox") || len(h.world.signalsSent()) != 0 {
		t.Fatalf("%q %v", h.stdout.String(), h.world.signalsSent())
	}
}

func TestTheNotesOfGoneProcessesArePruned(t *testing.T) {
	h := newLookHarness(t, "mine")
	noteSession(t, h.place, 4242, 1, "gone")
	noteSession(t, h.place, claudePID, 999, "an earlier process of the id")
	h.look.list(true, true)
	found, _ := os.ReadDir(filepath.Join(h.place.folder, "sessions"))
	if len(found) != 1 || found[0].Name() != "200-1000" {
		t.Fatalf("notes %v", found)
	}
}

func TestAnEntryWrittenBy043IsListed(t *testing.T) {
	h := newLookHarness(t, "")
	data, err := os.ReadFile(filepath.Join("testdata", "1763300000-70001.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.place.folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.place.entryPath("1763300000-70001"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	h.world.add(70001, shellPID, shellPID, shellPID, 1763299990, "Python")
	h.world.add(70002, 70001, 70002, 70002, 1763300001, "sh")
	listed := h.listJSON(t)
	want := map[string]any{"id": "1763300000-70001", "name": "watcher", "supervisor": float64(70001),
		"supervisor_start": float64(1763299990), "group": float64(70002), "started": 1763300000.123456,
		"leader_start": float64(1763300001), "deadline": 1763301800.123456, "keep": false, "session": "s-1",
		"watched": map[string]any{"pid": float64(70003), "start": float64(1763200000), "name": "claude"},
		"cwd":     "/work/project", "command": []any{"sh", "-c", "tail -f log"}, "status": "running", "state": "running"}
	if len(listed) != 1 || !reflect.DeepEqual(listed[0], want) {
		t.Fatalf("listed %v", listed)
	}
}

func TestAnEntryWhoseIdIsNotItsFileIsNotRead(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 690, 691, "", "", time.Minute)
	data, _ := os.ReadFile(h.place.entryPath("1800000000-690"))
	if err := os.WriteFile(filepath.Join(h.place.folder, "1800000000-1.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if entries := h.place.entries(); len(entries) != 1 || entries[0].ID != "1800000000-690" {
		t.Fatalf("entries %v", entries)
	}
}

func TestStopAsksALivingSupervisorThroughItsFile(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 700, 701, "server", "", 0)
	h.clock.onSleep = func() {
		if h.place.stopAsked("1800000000-700") {
			h.place.remove("1800000000-700") // the supervisor answers: its group stopped, its entry removed
		}
	}
	if code := h.look.stop([]string{"1800000000-700"}, false); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if h.stdout.String() != "jst procs: 1800000000-700 (server) stopped\n" || len(h.world.signalsSent()) != 0 {
		t.Fatalf("%q %v", h.stdout.String(), h.world.signalsSent())
	}
}

func TestStopReportsIncompleteCleanupAndKeepsItsEntry(t *testing.T) {
	h := newLookHarness(t, "")
	entry := h.supervised(t, 700, 701, "server", "", 0)
	h.clock.onSleep = func() {
		if h.place.stopAsked(entry.ID) {
			entry.Status = stateCleanupIncomplete
			if !h.place.write(entry) {
				t.Fatal("could not record incomplete cleanup")
			}
		}
	}
	if code := h.look.stop([]string{entry.ID}, false); code != 1 ||
		!strings.Contains(h.stderr.String(), "cleanup incomplete") || h.stdout.Len() != 0 {
		t.Fatalf("exit %d: stdout %q, stderr %q", code, h.stdout.String(), h.stderr.String())
	}
	h.world.remove(entry.Supervisor)
	h.world.remove(entry.Group)
	if listed := h.listJSON(t); len(listed) != 1 || listed[0]["state"] != stateCleanupIncomplete {
		t.Fatalf("the incomplete cleanup is missing from the list: %v", listed)
	}
	if h.place.stopAsked(entry.ID) {
		t.Fatal("completed stop request is still pending")
	}
}

// An unanswered request was taken back at the bound and the group
// signalled from here while its supervisor lived. A living supervisor keeps the request, and
// nothing is signalled from here.
func TestALivingSupervisorKeepsAnUnansweredRequest(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 710, 711, "", "", 0)
	if code := h.look.stop([]string{"1800000000-710"}, false); code != 1 || !strings.Contains(h.stderr.String(), "the request stays") {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if !h.world.has(711) || !h.place.stopAsked("1800000000-710") || !h.place.listed("1800000000-710") ||
		len(h.world.signalsSent()) != 0 {
		t.Fatalf("signalled %v, or the request or the entry was taken away", h.world.signalsSent())
	}
}

func TestASupervisorProvenGoneWhileAskedIsPassedOverForItsProvenGroup(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 715, 716, "", "", 0)
	h.clock.onSleep = func() { h.world.remove(715) } // the supervisor dies without answering
	if code := h.look.stop([]string{"1800000000-715"}, false); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if h.world.has(716) || h.place.stopAsked("1800000000-715") || h.place.listed("1800000000-715") {
		t.Fatal("the group stays, or the request or the entry")
	}
}

func TestASupervisorThatCannotBeToldIsNotPassedOver(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 717, 718, "", "", 0)
	h.world.readFails = true
	h.world.denied[717] = true // kill(717, 0) answers EPERM: it may run
	h.clock.onSleep = func() { h.world.remove(717); h.world.add(717, 1, 717, 717, 1, "other") }
	code := h.look.stop([]string{"1800000000-717"}, false)
	if code != 1 || len(h.world.signalsSent()) != 0 || !h.world.has(718) {
		t.Fatalf("exit %d, sent %v: %s", code, h.world.signalsSent(), h.stderr.String())
	}
}

func TestAnOrphanIsStoppedByIdWhenItsLeaderIsProven(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 720, 721, "left", "", 0)
	h.world.remove(720)
	if code := h.look.stop([]string{"1800000000-720"}, false); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if h.world.has(721) || h.place.listed("1800000000-720") {
		t.Fatal("the group or the entry stays")
	}
	stop := events(t, h.place)[0]
	if stop["event"] != "stop" || stop["reason"] != "asked" || stop["gone"] != true {
		t.Fatalf("stop %v", stop)
	}
	sent := h.world.signalsSent()
	if len(sent) == 0 || sent[0] != (sentSignal{target: -721, sig: unix.SIGTERM}) {
		t.Fatalf("sent %v", sent)
	}
}

func TestAnOrphanWhoseLeaderIsNotProvenIsNotSignalled(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 730, 731, "", "", 0)
	h.world.remove(730)
	h.world.remove(731)
	h.world.add(731, 1, 731, 731, 1, "other")
	if code := h.look.stop([]string{"1800000000-730"}, false); code != 1 || !strings.Contains(h.stderr.String(), "not signalled") {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if len(h.world.signalsSent()) != 0 {
		t.Fatalf("sent %v", h.world.signalsSent())
	}
}

func TestInsideTheSandboxAnOrphanIsNamedRunOutsideTheSandbox(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 740, 741, "", "", 0)
	h.world.remove(740)
	h.world.denied[741] = true
	code := h.look.stop([]string{"1800000000-740"}, false)
	if code != 1 || !strings.Contains(h.stderr.String(), "run outside the sandbox") || len(h.world.signalsSent()) != 0 {
		t.Fatalf("exit %d: %s %v", code, h.stderr.String(), h.world.signalsSent())
	}
}

func TestAnOrphanThatEndedIsRemoved(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 750, 751, "", "", 0)
	h.world.remove(750)
	h.world.remove(751)
	if code := h.look.stop([]string{"1800000000-750"}, false); code != 0 || !strings.Contains(h.stdout.String(), "had ended already") {
		t.Fatalf("exit %d: %s", code, h.stdout.String())
	}
	if h.place.listed("1800000000-750") {
		t.Fatal("the entry stays")
	}
}

func TestReviewC4ANameStopsOnlyThisSessionsEntries(t *testing.T) {
	h := newLookHarness(t, "this-session")
	h.supervised(t, 760, 761, "server", "another-session", 0)
	h.world.remove(760)
	if code := h.look.stop([]string{"server"}, false); code != 1 || !strings.Contains(h.stderr.String(), "other sessions only") {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if !h.world.has(761) {
		t.Fatal("another session's entry was stopped")
	}
	if code := h.look.stop([]string{"server"}, true); code != 0 || h.world.has(761) {
		t.Fatalf("--all: exit %d: %s", code, h.stderr.String())
	}
}

func TestANameStopsThisSessionsEntry(t *testing.T) {
	h := newLookHarness(t, "this-session")
	h.supervised(t, 770, 771, "server", "this-session", 0)
	h.world.remove(770)
	if code := h.look.stop([]string{"server"}, false); code != 0 || h.world.has(771) {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
}

func TestANameNeedsAnIdOrAllWhenTheSessionIsNotKnown(t *testing.T) {
	h := newLookHarness(t, "")
	h.supervised(t, 780, 781, "server", "", 0)
	h.supervised(t, 782, 783, "server", "", 0)
	h.world.remove(780)
	h.world.remove(782)
	if code := h.look.stop([]string{"server"}, false); code != 1 || !strings.Contains(h.stderr.String(), "not known to jackstay") {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if !h.world.has(781) || !h.world.has(783) {
		t.Fatal("something was stopped")
	}
	if code := h.look.stop([]string{"server"}, true); code != 0 || h.world.has(781) || h.world.has(783) {
		t.Fatalf("--all: exit %d: %s", code, h.stderr.String())
	}
}

func TestStopOfNothingSoCalledIsOne(t *testing.T) {
	h := newLookHarness(t, "mine")
	if code := h.look.stop([]string{"server"}, false); code != 1 ||
		h.stderr.String() != "jst procs: nothing supervised is called server\n" {
		t.Fatalf("exit %d: %q", code, h.stderr.String())
	}
}

func TestProcsRefusesBadWords(t *testing.T) {
	testHome(t)
	for _, call := range []struct {
		run  func([]string, *bytes.Buffer) int
		args []string
	}{
		{func(args []string, stderr *bytes.Buffer) int { return listCommand(args, nil, &bytes.Buffer{}, stderr) }, []string{"--fast"}},
		{func(args []string, stderr *bytes.Buffer) int { return stopCommand(args, nil, &bytes.Buffer{}, stderr) }, nil},
		{func(args []string, stderr *bytes.Buffer) int { return stopCommand(args, nil, &bytes.Buffer{}, stderr) }, []string{"--all"}},
	} {
		var stderr bytes.Buffer
		if code := call.run(call.args, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: jst procs") {
			t.Fatalf("%v: exit %d %q", call.args, code, stderr.String())
		}
	}
}

func TestAFileOpenForWritingIsFound(t *testing.T) {
	output := strings.Join([]string{
		"p4242",
		"f1", "aw", "tREG", "n/work/out.log", // standard output: a watcher's output goes there
		"f3", "aw", "tREG", "n/work/data.bin",
		"f4", "au", "tREG", "n/work/both.db",
		"f5", "ar", "tREG", "n/work/read.txt",
		"f6", "aw", "tCHR", "n/dev/ttys001",
		"f7", "aw", "tREG", "n/private/var/folders/x/T/scratch",
		"f8", "aw", "tREG", "n/Users/me/tmp/own",
		"fcwd", "a ", "tDIR", "n/work",
		"p4243",
		"f3", "aw", "tREG", "n/work/data.bin",
	}, "\n")
	files := parseHeld(output, temporaryPrefixes("/Users/me/tmp/"))
	if !reflect.DeepEqual(files, []string{"/work/both.db", "/work/data.bin"}) {
		t.Fatalf("held %v", files)
	}
	if files := parseHeld(output, nil); !reflect.DeepEqual(files,
		[]string{"/Users/me/tmp/own", "/private/var/folders/x/T/scratch", "/work/both.db", "/work/data.bin"}) {
		t.Fatalf("without temporary folders %v", files)
	}
}

func TestAgeReadsAsPeopleWriteIt(t *testing.T) {
	for seconds, want := range map[float64]string{-5: "0m00s", 249: "4m09s", 3900: "1h05m"} {
		if got := age(seconds); got != want {
			t.Fatalf("age(%v) = %q", seconds, got)
		}
	}
	if cutRunes("ééé", 2) != "éé" {
		t.Fatal("cut inside a character")
	}
}

// A supervisor that answers between the stop's look at its entry and its look at the process table
// has stopped the group, removed its entry and ended: that is a stop, not a group that "had ended
// already" (#63, #20).
func TestASupervisorThatAnswersBetweenTwoLooksIsAStop(t *testing.T) {
	h := newLookHarness(t, "")
	entry := h.supervised(t, 700, 701, "server", "", 0)
	read := h.look.host.read
	h.look.host.read = func() (map[int]procs.Process, error) {
		if h.place.stopAsked(entry.ID) && h.place.listed(entry.ID) {
			// The supervisor's whole answer lands here: group stopped, entry removed, supervisor ended.
			h.world.remove(entry.Group)
			h.place.remove(entry.ID)
			h.world.remove(entry.Supervisor)
		}
		return read()
	}
	if code := h.look.stop([]string{entry.ID}, false); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if h.stdout.String() != "jst procs: 1800000000-700 (server) stopped\n" || len(h.world.signalsSent()) != 0 {
		t.Fatalf("%q %v", h.stdout.String(), h.world.signalsSent())
	}
}

// The other answer a supervisor gives, an incomplete cleanup, landing between the same two looks is
// reported as such and its entry kept, never removed as a group that "had ended already".
func TestASupervisorThatLeavesCleanupIncompleteBetweenTwoLooksKeepsItsEntry(t *testing.T) {
	h := newLookHarness(t, "")
	entry := h.supervised(t, 700, 701, "server", "", 0)
	read := h.look.host.read
	h.look.host.read = func() (map[int]procs.Process, error) {
		if h.place.stopAsked(entry.ID) {
			if latest, ok := h.place.read(entry.ID); ok && latest.Status != stateCleanupIncomplete {
				h.world.remove(entry.Group)
				latest.Status = stateCleanupIncomplete
				if !h.place.write(latest) {
					t.Fatal("could not record incomplete cleanup")
				}
				h.world.remove(entry.Supervisor)
			}
		}
		return read()
	}
	if code := h.look.stop([]string{entry.ID}, false); code != 1 || !strings.Contains(h.stderr.String(), "cleanup incomplete") ||
		strings.Contains(h.stdout.String(), "had ended already") {
		t.Fatalf("exit %d: stdout %q, stderr %q", code, h.stdout.String(), h.stderr.String())
	}
	if !h.place.listed(entry.ID) {
		t.Fatal("the incomplete cleanup's entry was removed")
	}
}
