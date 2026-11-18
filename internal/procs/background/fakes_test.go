package background

import (
	"encoding/json"
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

// fakeClock moves only when slept on or advanced (W-013); After fires once the time reaches it.
type fakeClock struct {
	mutex   sync.Mutex
	now     time.Time
	timers  []fakeTimer
	waiting chan struct{}
	// onSleep runs after each Sleep, outside the lock (a supervisor answering, in a test).
	onSleep func()
}

type fakeTimer struct {
	at      time.Time
	channel chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_800_000_000, 0), waiting: make(chan struct{}, 64)}
}

func (c *fakeClock) Now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(pause time.Duration) {
	c.advance(pause)
	if c.onSleep != nil {
		c.onSleep()
	}
}

func (c *fakeClock) After(pause time.Duration) <-chan time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	channel := make(chan time.Time, 1)
	if pause <= 0 {
		channel <- c.now
		return channel
	}
	c.timers = append(c.timers, fakeTimer{at: c.now.Add(pause), channel: channel})
	select {
	case c.waiting <- struct{}{}:
	default:
	}
	return channel
}

func (c *fakeClock) advance(pause time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.now = c.now.Add(pause)
	kept := c.timers[:0]
	for _, timer := range c.timers {
		if !timer.at.After(c.now) {
			timer.channel <- c.now
			continue
		}
		kept = append(kept, timer)
	}
	c.timers = kept
}

// world is a fake machine's processes and what signals do to them.
type world struct {
	mutex       sync.Mutex
	rows        map[int]procs.Process
	ignoresTerm map[int]bool
	// denied are the processes a signal to is refused with EPERM.
	denied map[int]bool
	sent   []sentSignal
	// heldFiles is what lsof answers; unlooked makes it give no answer.
	heldFiles []string
	unlooked  bool
	heldAsked int
	// heldOthers are the ids the last look was asked about besides the group.
	heldOthers []int
	// heldGate, when set, holds every look until it is closed (a slow lsof); heldEntered hears each.
	heldGate    chan struct{}
	heldEntered chan struct{}
	readFails   bool
}

type sentSignal struct {
	target int
	sig    syscall.Signal
}

func newWorld() *world {
	return &world{rows: map[int]procs.Process{}, ignoresTerm: map[int]bool{}, denied: map[int]bool{}}
}

// add puts a process in the world: its start in whole seconds (micros derived from it).
func (w *world) add(pid, parent, group, session int, start int64, name string) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.rows[pid] = procs.Process{PID: pid, Parent: parent, Group: group, Session: session, UID: 501, Start: start,
		StartMicros: start*1_000_000 + 7, Name: name}
}

func (w *world) remove(pid int) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	delete(w.rows, pid)
}

func (w *world) has(pid int) bool {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	_, present := w.rows[pid]
	return present
}

func (w *world) read() (map[int]procs.Process, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if w.readFails {
		return nil, &procs.TableError{Err: unix.EIO}
	}
	copied := make(map[int]procs.Process, len(w.rows))
	for pid, row := range w.rows {
		copied[pid] = row
	}
	return copied, nil
}

func (w *world) row(pid int) (procs.Process, bool, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if w.readFails {
		return procs.Process{}, false, &procs.TableError{Err: unix.EIO}
	}
	row, present := w.rows[pid]
	return row, present, nil
}

func (w *world) signal(target int, sig syscall.Signal) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	var hit []int
	for pid, row := range w.rows {
		if (target > 0 && pid == target) || (target < 0 && row.Group == -target) {
			hit = append(hit, pid)
		}
	}
	if len(hit) == 0 {
		return unix.ESRCH
	}
	for _, pid := range hit {
		if w.denied[pid] {
			return unix.EPERM
		}
	}
	if sig == 0 {
		return nil
	}
	w.sent = append(w.sent, sentSignal{target: target, sig: sig})
	for _, pid := range hit {
		if sig == unix.SIGKILL || !w.ignoresTerm[pid] {
			delete(w.rows, pid)
		}
	}
	return nil
}

func (w *world) signalsSent() []sentSignal {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return append([]sentSignal(nil), w.sent...)
}

func (w *world) held(_ int, others []int) ([]string, bool) {
	w.mutex.Lock()
	gate, entered := w.heldGate, w.heldEntered
	w.mutex.Unlock()
	if gate != nil {
		entered <- struct{}{}
		<-gate
	}
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.heldAsked++
	w.heldOthers = append([]int(nil), others...)
	if w.unlooked {
		return nil, false
	}
	return append([]string(nil), w.heldFiles...), true
}

func (w *world) setHeld(files []string, unlooked bool) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.heldFiles, w.unlooked = files, unlooked
}

// The fake machine's ids: this command, its Claude Code process and its shell.
const (
	selfPID   = 900
	claudePID = 200
	shellPID  = 300
)

func fakeMachine(w *world, clock *fakeClock) machine {
	return machine{clock: clock, read: w.read, row: w.row, signal: w.signal, held: w.held, uid: 501, pid: selfPID,
		poll: 50 * time.Millisecond}
}

// withCaller puts this command under a shell under a Claude Code process.
func withCaller(w *world) {
	w.add(claudePID, 1, claudePID, claudePID, 1000, "claude")
	w.add(shellPID, claudePID, shellPID, shellPID, 3000, "zsh")
	w.add(selfPID, shellPID, shellPID, shellPID, 9000, "jst")
}

// testHome is a JACKSTAY_HOME of the test's own, every inherited JACKSTAY_ variable cleared.
func testHome(t *testing.T) (string, store) {
	t.Helper()
	for _, entry := range os.Environ() {
		if name, _, _ := strings.Cut(entry, "="); strings.HasPrefix(name, "JACKSTAY_") {
			t.Setenv(name, "")
		}
	}
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	noted = nil
	note = func(event string, fields map[string]any) {
		record := map[string]any{"event": event}
		for key, value := range fields {
			record[key] = value
		}
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		noted = append(noted, decoded)
	}
	t.Cleanup(func() { note, noted = procs.Note, nil })
	place, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	return home, place
}

// noteSession writes what the guard hook writes for a process.
func noteSession(t *testing.T, place store, pid int, start int64, session string) {
	t.Helper()
	folder := filepath.Join(place.folder, "sessions")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	text, _ := json.Marshal(map[string]any{"session": session, "pid": pid, "start": start, "name": "claude"})
	if err := os.WriteFile(filepath.Join(folder, fmt.Sprintf("%d-%d", pid, start)), text, 0o600); err != nil {
		t.Fatal(err)
	}
}

// noted are the events an in-process run noted, in order (testHome sets note to collect them).
var noted []map[string]any

// events are the events this test's in-process runs noted.
func events(t *testing.T, _ store) []map[string]any {
	t.Helper()
	return noted
}

// eventPairs are the noted (event, reason) pairs.
func eventPairs(t *testing.T, place store) [][2]string {
	t.Helper()
	var pairs [][2]string
	for _, record := range events(t, place) {
		event, _ := record["event"].(string)
		reason, _ := record["reason"].(string)
		pairs = append(pairs, [2]string{event, reason})
	}
	return pairs
}

func pointer[Value any](value Value) *Value { return &value }

// fakeControl is the supervisor's hold on its command in the fake world: a stop is TERM to the
// group, needing no table; refuse makes a stop not made.
type fakeControl struct {
	mutex  sync.Mutex
	world  *world
	group  int
	others []int
	refuse bool
	stops  int
}

func (control *fakeControl) Stop() bool {
	control.mutex.Lock()
	defer control.mutex.Unlock()
	control.stops++
	if control.refuse {
		return false
	}
	_ = control.world.signal(-control.group, unix.SIGTERM)
	return true
}

func (control *fakeControl) Members() (int, []int) {
	control.mutex.Lock()
	defer control.mutex.Unlock()
	return control.group, append([]int(nil), control.others...)
}

func (control *fakeControl) stopCount() int {
	control.mutex.Lock()
	defer control.mutex.Unlock()
	return control.stops
}
