// Package background is `jst bg` and `jst procs` (docs/design/implementation.md
// section 5.1): a long-lived command started in a session of its own, stopped at a
// deadline or when the Claude Code process it was started under is gone, listed in
// `~/.jackstay/.all/procs/<id>.json`, and stopped on request through `<id>.stop`.
//
// Exit codes of `jst bg`: the command's own (128+N for a signal); 2 a bad request; 126 the command
// could not start; 127 it was not found; asked to stop by a signal, jst bg ends by that signal.
// `jst procs`: 0; 1 a name or id not stopped (`stop`); 2 a bad request.
package background

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// The supervision's times (docs/design/implementation.md section 5.1).
const (
	// checkEvery is how often the session is checked and a held group asked again.
	checkEvery = 30 * time.Second
	// slice is how soon a request to stop through the stop file is answered.
	slice = 250 * time.Millisecond
	// stopGrace is what a group is given between TERM and KILL.
	stopGrace = 5 * time.Second
	// defaultMinutes and maxMinutes stand where jackstay.json has no `procs` section.
	defaultMinutes = 30.0
	maxMinutes     = 240.0
	// tellBound is how long jst bg's last lines may take to be written before it ends without them.
	tellBound = time.Second
)

// Statuses of jst bg's own.
const (
	statusUsage       = 2
	statusCannotStart = 126
	statusNotFound    = 127
)

const bgUsage = "usage: jst bg [--for MINUTES] [--name NAME] [--keep] -- COMMAND ..."

func init() {
	cli.Register(
		cli.Command{Name: "bg", Summary: "run a long-lived command with a deadline, stopped when its session is gone", Usage: bgHelp, Run: bgCommand},
		cli.Command{Name: "procs", Summary: "what jst bg supervises: this session's, or --all", Usage: procsHelp, Run: listCommand},
		cli.Command{Name: "procs stop", Summary: "stop what jst bg supervises, by ID or by this session's NAME", Usage: stopHelp, Run: stopCommand},
	)
}

// request is what `jst bg` was asked.
type request struct {
	minutes float64
	timed   bool
	name    *string
	keep    bool
	// watch and every are for the tests: the process to watch instead of the session's, and how
	// often it is checked.
	watch int
	every time.Duration
	argv  []string
}

// parseBg reads the options before `--`; false when the words cannot be used.
func parseBg(args []string) (request, bool) {
	asked := request{every: checkEvery}
	cut := -1
	for index, word := range args {
		if word == "--" {
			cut = index
			break
		}
	}
	if cut < 0 || cut == len(args)-1 {
		return asked, false
	}
	head := args[:cut]
	asked.argv = args[cut+1:]
	for index := 0; index < len(head); index++ {
		option := head[index]
		if option == "--keep" {
			asked.keep = true
			continue
		}
		if index+1 >= len(head) {
			return asked, false
		}
		index++
		value := head[index]
		switch option {
		case "--for":
			minutes, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(minutes) || minutes <= 0 {
				return asked, false
			}
			asked.minutes, asked.timed = minutes, true
		case "--name":
			name := value
			asked.name = &name
		case "--watch":
			pid, err := strconv.Atoi(value)
			if err != nil || pid <= 1 {
				return asked, false
			}
			asked.watch = pid
		case "--check-every":
			seconds, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
				return asked, false
			}
			asked.every = time.Duration(seconds * float64(time.Second))
		default:
			return asked, false
		}
	}
	return asked, true
}

// limits are procs.default_minutes and procs.max_minutes of the project here; the defaults
// without one, or (said) when jackstay.json's procs section cannot be used.
func limits(here string, stderr io.Writer) (fallback, ceiling float64) {
	fallback, ceiling = defaultMinutes, maxMinutes
	if here == "" {
		return fallback, ceiling
	}
	_, loaded, err := config.Load(here, []string{"procs"})
	if err != nil {
		fmt.Fprintf(stderr, "jst bg: jackstay.json cannot be used (%v); the default deadline applies\n", err)
		return fallback, ceiling
	}
	section := loaded.Section("procs")
	if number, ok := section["default_minutes"].(json.Number); ok {
		if value, err := number.Float64(); err == nil {
			fallback = value
		}
	}
	if number, ok := section["max_minutes"].(json.Number); ok {
		if value, err := number.Float64(); err == nil {
			ceiling = value
		}
	}
	return fallback, ceiling
}

func bgCommand(args []string, _ io.Reader, _, stderr io.Writer) int {
	asked, ok := parseBg(args)
	if !ok {
		fmt.Fprintln(stderr, bgUsage)
		return statusUsage
	}
	here, err := os.Getwd()
	if err != nil {
		here = ""
	}
	fallback, ceiling := limits(here, stderr)
	place, err := openStore()
	if err != nil {
		fmt.Fprintf(stderr, "jst bg: %v\n", err)
		return statusUsage
	}
	run := &backgroundRun{asked: asked, host: realMachine(), place: place, here: here, stderr: stderr}
	run.plan(fallback, ceiling)
	return run.run()
}

// backgroundRun is one `jst bg -- COMMAND`.
type backgroundRun struct {
	asked  request
	host   machine
	place  store
	here   string
	stderr io.Writer

	minutes float64
	capped  bool
}

// plan settles the minutes: --for, else the project's default, never above its ceiling.
func (run *backgroundRun) plan(fallback, ceiling float64) {
	run.minutes = fallback
	if run.asked.timed {
		run.minutes = run.asked.minutes
	}
	if run.minutes > ceiling {
		run.minutes, run.capped = ceiling, true
	}
}

func (run *backgroundRun) run() int {
	// The handlers come first: a stop asked for while the command starts must reach its session.
	signals := supervise.Listen(context.Background(), run.host.clock)
	defer signals.Close()
	say := newTeller(run.stderr)
	table := run.host.table()
	watched := run.watched(table)
	started := run.host.clock.Now()
	id := fmt.Sprintf("%d-%d", started.Unix(), run.host.pid)
	if first, _, asked := signals.First(); asked {
		say.close(run.host.clock.After(tellBound))
		signals.Close()
		return supervise.EndBy(first)
	}
	ended := make(chan struct{})
	var loop errgroup.Group
	var watcher *watcher
	var control *supervise.Control
	outcome, err := supervise.Run(signals, supervise.Spec{
		Argv: run.asked.argv, Mode: supervise.Session, Stdin: os.Stdin, Grace: stopGrace,
		Say: say, Who: "jst bg", Clock: run.host.clock,
		Control: func(given *supervise.Control) { control = given },
		Started: func(pid int) {
			watcher = run.begin(id, pid, control, started, watched, table, say)
			loop.Go(func() error {
				watcher.watch(ended)
				return nil
			})
		},
	})
	close(ended)
	_ = loop.Wait()
	var startErr *supervise.StartError
	if errors.As(err, &startErr) {
		code := statusCannotStart
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			say.tell(fmt.Sprintf("jst bg: %s: command not found; nothing was started", run.asked.argv[0]))
			code = statusNotFound
		} else {
			say.tell(fmt.Sprintf("jst bg: %s could not start (%v); nothing was started", run.asked.argv[0], startErr.Err))
		}
		say.close(run.host.clock.After(tellBound))
		return code
	}
	if err != nil || watcher == nil {
		say.tell(fmt.Sprintf("jst bg: %v", err))
		say.close(run.host.clock.After(tellBound))
		return statusCannotStart
	}
	for _, problem := range outcome.Problems {
		say.tell(fmt.Sprintf("jst bg: %s: %v", id, problem))
	}
	reason := watcher.stoppedFor()
	if reason == "" {
		reason = "ended"
		if outcome.StoppedBy != 0 {
			reason = "signal"
		}
	}
	status := outcome.Status()
	note("end", map[string]any{"id": id, "status": status, "reason": reason})
	run.place.remove(id)
	say.close(run.host.clock.After(tellBound))
	if outcome.StoppedBy != 0 {
		signals.Close()
		return supervise.EndBy(outcome.StoppedBy)
	}
	return status
}

// watchedProcess is the process whose end stops the group, with its start in microseconds.
type watchedProcess struct {
	session string
	pid     int
	start   int64
	micros  int64
	known   bool
	name    string
}

// watched is the process the session check watches: --watch's, else the nearest ancestor with a
// session note, else the nearest that may be a session's own; nil when none can be told.
func (run *backgroundRun) watched(table map[int]procs.Process) *watchedProcess {
	if run.asked.watch > 0 {
		found := &watchedProcess{pid: run.asked.watch, name: "given"}
		if row, present := table[run.asked.watch]; present {
			found.start, found.micros, found.known = row.Start, row.StartMicros, true
		}
		return found
	}
	own := procs.OwnSession(table, run.host.pid)
	if !own.Found {
		return nil
	}
	return &watchedProcess{session: own.Session, pid: own.PID, start: own.Start, micros: table[own.PID].StartMicros,
		known: true, name: own.Name}
}

// begin lists the entry, logs the start and says what supervises the command; control is the
// supervisor's own hold on it, through which every stop goes.
func (run *backgroundRun) begin(id string, group int, control stopper, started time.Time, watched *watchedProcess,
	table map[int]procs.Process, say *teller) *watcher {
	found := &watcher{host: run.host, place: run.place, group: group, control: control, keep: run.asked.keep,
		every: run.asked.every, say: say, nextCheck: started.Add(run.asked.every)}
	if !run.asked.keep {
		found.deadline = started.Add(time.Duration(run.minutes * float64(time.Minute))).Round(0)
		if watched != nil {
			found.watched = watched
		}
	}
	entry := Entry{ID: id, Name: run.asked.name, Supervisor: run.host.pid, Group: group, Started: epoch(started),
		Keep: run.asked.keep, Cwd: run.here, Command: run.asked.argv, Status: "running"}
	if leader, present, _ := run.host.row(group); present {
		entry.LeaderStart = &leader.Start
	}
	if own, present := table[run.host.pid]; present {
		entry.SupervisorStart = &own.Start
	}
	if !run.asked.keep {
		deadline := epoch(found.deadline)
		entry.Deadline = &deadline
	}
	if watched != nil {
		if watched.session != "" {
			session := watched.session
			entry.Session = &session
		}
		if !run.asked.keep {
			entry.Watched = &Watched{PID: watched.pid, Name: watched.name}
			if watched.known {
				start := watched.start
				entry.Watched.Start = &start
			}
		}
	}
	found.entry = entry
	listed := run.place.write(entry)
	note("start", map[string]any{"id": id, "name": entry.Name, "group": group, "deadline": entry.Deadline,
		"keep": entry.Keep, "session": entry.Session, "watched": entry.Watched, "cwd": entry.Cwd,
		"command": entry.Command[:min(len(entry.Command), 20)]})
	say.tell(run.startWords(entry, found, watched, listed))
	return found
}

// startWords is the line that says what supervises the command.
func (run *backgroundRun) startWords(entry Entry, found *watcher, watched *watchedProcess, listed bool) string {
	said := []string{fmt.Sprintf("jst bg: %s%s started, group %d", entry.ID, inBrackets(entry.nameOf()), entry.Group)}
	switch {
	case run.asked.keep:
		handle := entry.nameOf()
		if handle == "" {
			handle = entry.ID
		}
		said = append(said, "kept: no deadline and no session check; `jst procs stop "+handle+"` ends it")
	default:
		limit := ""
		if run.capped {
			limit = ", the most procs.max_minutes allows"
		}
		said = append(said, fmt.Sprintf("deadline %s (%s min%s)", found.deadline.Local().Format("15:04"),
			strconv.FormatFloat(run.minutes, 'g', -1, 64), limit))
		if watched == nil {
			said = append(said, "no session process can be told here: supervised by the deadline only")
		} else {
			session := watched.session
			if session == "" {
				session = "unknown"
			}
			said = append(said, fmt.Sprintf("stopped too when %s %d (session %s) is gone", watched.name, watched.pid, session))
		}
	}
	if !listed {
		said = append(said, "NOT listed: "+run.place.folder+" cannot be written; the deadline and the session check still hold")
	}
	return strings.Join(said, "; ")
}

func inBrackets(name string) string {
	if name == "" {
		return ""
	}
	return " (" + name + ")"
}

// epoch is a time as seconds since 1970, the unit of the entry's times.
func epoch(moment time.Time) float64 {
	return float64(moment.UnixNano()) / 1e9
}

// stopper is the supervisor's hold on its command (supervise.Control): a stop goes through it,
// so it needs no process table, and it names what such a stop signals.
type stopper interface {
	Stop() bool
	Members() (group int, others []int)
}

// watcher stops the group when asked through the stop file, at the deadline and when the watched
// process is gone; a group holding a file open for writing is held, not stopped, and asked again.
// The stop file has a loop of its own, so a slow look at the open files never delays a request.
type watcher struct {
	host    machine
	place   store
	entry   Entry
	group   int
	control stopper
	keep    bool
	// deadline is wall-clock (no monotonic reading): a Mac asleep past it stops the group at the
	// first check after waking.
	deadline        time.Time
	watched         *watchedProcess
	every           time.Duration
	nextCheck       time.Time
	deadlineChecked bool
	heldSaid        []string
	say             *teller

	mutex sync.Mutex
	// reason is why the watcher stopped the group ("" while it has not).
	reason string
}

// watch runs the two loops until the command has ended or the group was stopped: the stop file
// every slice, and the deadline and the session at their times.
func (watching *watcher) watch(ended <-chan struct{}) {
	stopped := make(chan struct{})
	var once sync.Once
	end := func() { once.Do(func() { close(stopped) }) }
	var loops sync.WaitGroup
	loop := func(check func() bool) {
		defer loops.Done()
		for {
			select {
			case <-ended:
				return
			case <-stopped:
				return
			case <-watching.host.clock.After(slice):
			}
			if watching.stoppedFor() != "" {
				end()
				return
			}
			if check() {
				end()
				return
			}
		}
	}
	loops.Add(1)
	go loop(watching.askedCheck)
	if !watching.keep {
		loops.Add(1)
		go loop(watching.timedCheck)
	}
	loops.Wait()
}

// stoppedFor is why the group was stopped ("" while it was not).
func (watching *watcher) stoppedFor() string {
	watching.mutex.Lock()
	defer watching.mutex.Unlock()
	return watching.reason
}

// askedCheck answers a request through the stop file. The request stays until the stop was made;
// it is removed with the entry once the command has ended. True when the group was stopped.
func (watching *watcher) askedCheck() bool {
	id := watching.entry.ID
	if !watching.place.stopAsked(id) {
		return false
	}
	return watching.stop("asked", "", map[string]any{"id": id, "reason": "asked"})
}

// timedCheck is one look at the deadline and the session at their times. True when the group was
// stopped.
func (watching *watcher) timedCheck() bool {
	if watching.keep {
		return false
	}
	id := watching.entry.ID
	now := watching.host.clock.Now().Round(0)
	pastDeadline := !now.Before(watching.deadline)
	if now.Before(watching.nextCheck) && (!pastDeadline || watching.deadlineChecked) {
		return false
	}
	watching.nextCheck = now.Add(watching.every)
	why := ""
	switch {
	case pastDeadline:
		why, watching.deadlineChecked = "deadline", true
	case watching.watched != nil && watching.watchedGone():
		why = "session-gone"
	default:
		return false
	}
	// The look covers exactly what the stop signals: the group, and the session's members outside it.
	group, others := watching.control.Members()
	files, looked := watching.host.held(group, others)
	if watching.stoppedFor() != "" {
		return true
	}
	if looked && len(files) > 0 {
		if !equalTexts(watching.heldSaid, files) {
			watching.heldSaid = files
			watching.entry.Status, watching.entry.Held = "held", files
			watching.place.write(watching.entry)
			note("held", map[string]any{"id": id, "reason": why, "files": files[:min(len(files), 20)]})
			watching.say.tell(fmt.Sprintf("jst bg: %s: %s, but the group has files open for writing (%s): not stopped; "+
				"asked again at the next check", id, why, strings.Join(files[:min(len(files), 5)], ", ")))
		}
		return false
	}
	unlooked := ""
	if !looked {
		unlooked = " (lsof gave no answer: stopped all the same)"
	}
	if !watching.stop(why, fmt.Sprintf("jst bg: %s: %s%s; the group was stopped (TERM, then KILL)", id, why, unlooked),
		map[string]any{"id": id, "reason": why, "looked": looked}) {
		// Not made: asked again at the next slice, the deadline's check with it.
		watching.deadlineChecked, watching.nextCheck = false, now
		return false
	}
	return true
}

// watchedGone says the watched process is no longer the one watched; a table that cannot be read
// says nothing.
func (watching *watcher) watchedGone() bool {
	row, found, err := watching.host.row(watching.watched.pid)
	if err != nil {
		return false
	}
	if !found || row.Zombie {
		return true
	}
	return watching.watched.known && row.StartMicros != watching.watched.micros
}

// stop stops the command through its supervisor: TERM to its group (the unreaped leader's, so no
// table is needed) and the session's members, KILL after the grace. False when the stop could not
// be made: nothing is logged or said, and the watcher goes on to ask again. The first stop's reason
// is kept, logged and said.
func (watching *watcher) stop(why, words string, logged map[string]any) bool {
	if !watching.control.Stop() {
		return false
	}
	watching.mutex.Lock()
	first := watching.reason == ""
	if first {
		watching.reason = why
	}
	watching.mutex.Unlock()
	if first {
		note("stop", logged)
		if words != "" {
			watching.say.tell(words)
		}
	}
	return true
}

func equalTexts(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// teller writes jst bg's own lines from a goroutine of its own: the command shares the standard
// error, and when nobody reads it any more a write blocks, which must not stop the supervision. A
// line that cannot be written (the queue full, the stream blocked or closed) is lost.
type teller struct {
	mutex  sync.Mutex
	lines  chan string
	closed bool
	done   chan struct{}
}

func newTeller(out io.Writer) *teller {
	say := &teller{lines: make(chan string, 64), done: make(chan struct{})}
	go func() {
		defer close(say.done)
		for line := range say.lines {
			_, _ = io.WriteString(out, line)
		}
	}()
	return say
}

// tell queues one line.
func (say *teller) tell(text string) { say.send(text + "\n") }

// Write queues what supervise says (one line per call).
func (say *teller) Write(data []byte) (int, error) {
	say.send(string(data))
	return len(data), nil
}

func (say *teller) send(text string) {
	say.mutex.Lock()
	defer say.mutex.Unlock()
	if say.closed {
		return
	}
	select {
	case say.lines <- text:
	default:
	}
}

// close lets the queued lines be written until bound fires; later lines are dropped.
func (say *teller) close(bound <-chan time.Time) {
	say.mutex.Lock()
	if !say.closed {
		say.closed = true
		close(say.lines)
	}
	say.mutex.Unlock()
	select {
	case <-say.done:
	case <-bound:
	}
}
