// Package supervise starts a command and lives beside it until nothing of it is left
// (docs/design/implementation.md section 5.2). There are two ways to start, and
// only two: a Session of its own without a terminal (`jst test <scope>`, `jst bg`), stopped as a
// session tree; and a Group of its own given the terminal (`jst lock`, `jst test --`), stopped as the
// group plus every session recorded in JACKSTAY_LOCK_SESSIONS_FILE. One signal loop (Signals) passes
// every request to stop on; the first starts the wait, and the command is killed with what it
// started when the grace from that request is over. Output goes through pipes, read line by line;
// the command is reaped by Process.Wait; after the stop the readers have a bound to reach the end.
//
// The authority over the command is its unreaped leader: until Process.Wait reaps it, its id — and
// so its group's and its session's — cannot be given to another process, so the group is signalled
// by its id and the table filtered by that id names its members without any guess by start times.
// The end is learnt without reaping (kqueue's NOTE_EXIT on darwin, waitid with WNOWAIT on linux);
// the leader is reaped only once the table shows nothing of its group (and session) left but its
// zombie, or after the last KILL and its wait, the survivors named — always last, never before a
// signal by its ids; nothing is signalled after it. A process outside the group is signalled
// only by an id from a table read in the same step. Without a table the whole stop (TERM, the
// grace, KILL, the wait) goes to the group by its id, and the outcome says it is unconfirmed. No
// lock that signalling needs is held across a reap, a write of supervise's words (a queue of
// their own) or a table read.
package supervise

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/proctree"
	"github.com/zima-oss/jackstay/internal/telemetry"
)

// Mode is how a command is started.
type Mode int

const (
	// Session is a session of its own without a terminal; it is stopped as a session tree
	// (descendants found by their session even when reparented, and by parent links).
	Session Mode = iota + 1
	// Group is a process group of its own, given jackstay's terminal when jackstay is in front of one; it is
	// stopped as the group (what left the group is not caught).
	Group
)

// Defaults of a Spec's durations left zero.
const (
	// DefaultGrace is the lock's and `jst test --`'s GROUP_WAIT.
	DefaultGrace = 30 * time.Second
	// DefaultKillWait is what is given to the killed to be gone.
	DefaultKillWait = 5 * time.Second
	// DefaultSettle is how long a command's last processes may take to end after it ended by
	// itself before they count as left behind.
	DefaultSettle = 300 * time.Millisecond
	// DefaultReadBound is how long the readers have after the stop to reach the output's end.
	DefaultReadBound = 10 * time.Second
	// DefaultSampleEvery is how often a session's tree is read while its command runs.
	DefaultSampleEvery = time.Second
	// DefaultPoll is the pause between two reads of the table while waiting for a tree to end.
	DefaultPoll = 50 * time.Millisecond
)

// Spec is what to run and how.
type Spec struct {
	Argv []string
	// TraceArgv is the argv kept in telemetry when the real one may hold a credential; nil keeps Argv.
	TraceArgv []string
	// Env is the command's environment; nil is jackstay's own.
	Env  []string
	Dir  string
	Mode Mode
	// Terminal gives a Group the terminal when jackstay is in front of one.
	Terminal bool
	// Stdin is the command's standard input; nil is Input, or /dev/null without Input.
	Stdin *os.File
	// Input, when Stdin is nil, is given to the command on its standard input through a pipe,
	// which is then closed; what the command does not read is dropped when it ends.
	Input []byte
	// Output hears both streams line by line through pipes; nil leaves the command writing to
	// StdoutTo and StderrTo, else Stdout and Stderr (nil: jackstay's own) directly.
	Output         Sink
	Stdout, Stderr *os.File
	// StdoutTo and StderrTo, when Output is nil, are given the stream's bytes as they come through a
	// pipe, unchanged and under the same bound as Output; each is written from one goroutine, so
	// one writer given both must take its own lock. A writer's first error is a problem of the
	// outcome, and the rest of its stream is read and dropped.
	StdoutTo, StderrTo io.Writer
	// Context, when set, stops the command at its end as Control.Stop does (SIGTERM, SIGKILL after
	// the grace); a context ended before the start starts nothing.
	Context context.Context
	// Trace is the context whose span the command's span goes under (nil: Context's, else the
	// innermost open span); it changes nothing of the run.
	Trace context.Context
	// Grace runs from the first request to stop (or from the stop of what was left) to SIGKILL.
	Grace time.Duration
	// KillWait, Settle, ReadBound, SampleEvery: see the defaults.
	KillWait    time.Duration
	Settle      time.Duration
	ReadBound   time.Duration
	SampleEvery time.Duration
	// SessionsFile (Group) names the sessions to stop after any end (SessionsFileVariable).
	SessionsFile string
	// RecordSessionIn (Session) is a sessions file this command's session is recorded in.
	RecordSessionIn string
	// Started hears the command's process (its group's and session's id) once it exists.
	Started func(pid int)
	// Control, when set, is given the run's Control before Started: a way to stop the command from
	// inside jackstay (jst bg's deadline) and to name what such a stop would signal.
	Control func(*Control)
	// Say is where supervise's own words go, each line beginning with Who.
	Say io.Writer
	Who string
	// Clock and System are the machine's when zero; tests give their own.
	Clock  Clock
	System *proctree.System
}

// An Outcome is how a run ended and what it left.
type Outcome struct {
	PID int
	// Started is when the command was started; Ended when its end was learnt (before the reap,
	// what it left cleared and its output read: the command's own end), both by the run's clock.
	Started time.Time
	Ended   time.Time
	// Code is the command's exit status when it exited; Signal the signal it died by.
	Code   int
	Signal syscall.Signal
	// StoppedBy is the first signal jackstay was asked to stop with (0: none).
	StoppedBy syscall.Signal
	// Killed says the command still ran when the grace after the request was over, and was killed.
	Killed bool
	// Left is what the command left running after it ended by itself.
	Left []procs.Process
	// Survivors still ran after SIGKILL; Refused are signals the kernel refused; Foreign are
	// processes of another account that were not waited for.
	Survivors []proctree.Survivor
	Refused   []*proctree.SignalError
	Foreign   []procs.Process
	// Unread says why the output was not read to its end ("" when it was).
	Unread string
	// Unconfirmed says the table could not be read when the clearing ended: the group was
	// stopped by its id, and whether anything of it is left cannot be told (tree_unconfirmed).
	Unconfirmed bool
	// Problems are what could not be done on the way (the table unreadable, a sink's error, words
	// of supervise's own dropped because nobody read them).
	Problems []error
}

// Status is the command's status as a shell gives it: the exit code, or 128 and the signal.
func (outcome Outcome) Status() int {
	if outcome.Signal != 0 {
		return 128 + int(outcome.Signal)
	}
	return outcome.Code
}

// A StartError says the command could not be started: it never ran.
type StartError struct {
	Command string
	Err     error
}

func (problem *StartError) Error() string {
	return fmt.Sprintf("cannot start %s: %v", problem.Command, problem.Err)
}

func (problem *StartError) Unwrap() error { return problem.Err }

func withDefaults(spec Spec) Spec {
	if spec.Grace <= 0 {
		spec.Grace = DefaultGrace
	}
	if spec.KillWait <= 0 {
		spec.KillWait = DefaultKillWait
	}
	if spec.Settle <= 0 {
		spec.Settle = DefaultSettle
	}
	if spec.ReadBound <= 0 {
		spec.ReadBound = DefaultReadBound
	}
	if spec.SampleEvery <= 0 {
		spec.SampleEvery = DefaultSampleEvery
	}
	if spec.Clock == nil {
		spec.Clock = RealClock()
	}
	if spec.Who == "" {
		spec.Who = "jst"
	}
	if spec.System == nil {
		system := proctree.Real()
		system.Poll = DefaultPoll
		spec.System = &system
	}
	system := *spec.System
	system.Clock = spec.Clock
	spec.System = &system
	return spec
}

// exitSeen is what was learnt of the command's end before its reap.
type exitSeen struct {
	// signaled says it died by a signal; known says the wait status was seen at all.
	signaled bool
	known    bool
}

// exitWatch learns the command's end without reaping it: await blocks until the end, ended looks
// once. Tests give their own.
type exitWatch struct {
	await func(pid int) (exitSeen, error)
	ended func(pid int) (bool, error)
}

// running is a command between its start and its reap.
type running struct {
	spec  Spec
	pid   int
	say   func(string, ...any)
	exits exitWatch
	// reapProcess is Process.Wait: called once, after the clearing, never under the mutex.
	reapProcess func() (*os.ProcessState, error)

	// mutex guards what signalling needs: no reap, no write and no table read is made under it.
	mutex sync.Mutex
	// tree remembers, in a session, the descendants found by their parent links (they may leave the
	// session); the group's and the session's members are named by their ids at each read.
	tree     *proctree.Tree
	reaped   bool
	killed   bool
	asked    chan struct{}
	askedAt  time.Time
	askedBy  string
	state    *os.ProcessState
	problems []error
}

func newRunning(spec Spec, pid int) *running {
	run := &running{spec: spec, pid: pid, say: sayer(spec.Say, spec.Who), tree: proctree.New(), asked: make(chan struct{}),
		exits: exitWatch{await: awaitExit, ended: exitedNow}}
	if spec.Mode == Session {
		run.tree.FollowSession(pid)
	}
	return run
}

// A Control stops a running command from inside jackstay and names what such a stop signals.
type Control struct {
	run *running
}

// Stop asks the command to stop as a request by a signal would: SIGTERM to its group (and, in a
// session, to each member outside the group the table shows), and SIGKILL when the grace is over.
// True once the stop was made, or the command had been reaped already (nothing of it is the run's
// to signal then); false when the group could not be signalled — then nothing was asked, and the
// caller asks again.
func (control *Control) Stop() bool {
	run := control.run
	run.request(run.spec.Clock.Now(), "a stop from jackstay")
	table, err := run.spec.System.Read()
	run.mutex.Lock()
	defer run.mutex.Unlock()
	if run.reaped {
		return true
	}
	sent, _ := run.signalAll(table, err == nil, unix.SIGTERM)
	return sent
}

// Members are what a stop signals now: the command's group, and the processes outside it (in a
// session: its members in other groups, and descendants that left it). Without a table the group
// is all a stop can signal, and all that is named.
func (control *Control) Members() (group int, others []int) {
	run := control.run
	table, err := run.spec.System.Read()
	run.mutex.Lock()
	defer run.mutex.Unlock()
	if err != nil || run.reaped {
		return run.pid, nil
	}
	for _, row := range run.members(table) {
		if row.Group != run.pid {
			others = append(others, row.PID)
		}
	}
	return run.pid, others
}

// request keeps the first request to stop (its time and what made it) and lets the overdue kill
// wait for its grace.
func (run *running) request(when time.Time, cause string) {
	run.mutex.Lock()
	defer run.mutex.Unlock()
	if !run.askedAt.IsZero() {
		return
	}
	run.askedAt, run.askedBy = when, cause
	close(run.asked)
}

// firstRequest is when the first request to stop came and what made it (zero: none came).
func (run *running) firstRequest() (time.Time, string) {
	run.mutex.Lock()
	defer run.mutex.Unlock()
	return run.askedAt, run.askedBy
}

func (run *running) problem(err error) {
	run.mutex.Lock()
	defer run.mutex.Unlock()
	run.problems = append(run.problems, err)
}

// Run starts the command, passes every request signals hears on, waits for its end, clears what it
// left, stops the recorded sessions and reads the output to its end under the bound. The error is
// a *StartError when the command could not be started; signals may be nil.
func Run(signals *Signals, spec Spec) (Outcome, error) {
	spec = withDefaults(spec)
	if len(spec.Argv) == 0 {
		return Outcome{}, &StartError{Command: "", Err: errors.New("no command")}
	}
	if spec.Context != nil && spec.Context.Err() != nil {
		return Outcome{}, &StartError{Command: spec.Argv[0], Err: context.Cause(spec.Context)}
	}
	closer := &Closer{}
	defer closer.Close()
	if spec.Mode == Group && !signal.Ignored(unix.SIGTSTP) {
		// Ctrl-Z would stop the command, or jackstay, with nobody to continue it: ignored while it runs,
		// by jackstay and (an ignored signal is inherited) by the command.
		signal.Ignore(unix.SIGTSTP)
		closer.Add(func() { signal.Reset(unix.SIGTSTP) })
	}
	startedAt := spec.Clock.Now()
	span := startChildSpan(spec)
	spec.Env = telemetry.ChildEnv(spec.Env, span)
	process, tty, readers, input, err := start(spec)
	if err != nil {
		span.Fail("start_failed", err.Error())
		span.End("not-started")
		return Outcome{}, &StartError{Command: spec.Argv[0], Err: err}
	}
	span.Set(attribute.Int(telemetry.AttrChildPID, process.Pid))
	closer.Add(tty.giveBack)
	closer.Add(input.finish)
	words := newSayQueue(spec.Say)
	sayingSpec := spec
	sayingSpec.Say = words
	run := newRunning(sayingSpec, process.Pid)
	run.reapProcess = process.Wait
	outcome := Outcome{PID: run.pid, Started: startedAt}
	if row, found, rowErr := procs.Row(run.pid); found {
		run.tree.Remember(run.pid, row.StartMicros)
		if spec.Mode == Session {
			if err := RecordSession(spec.RecordSessionIn, run.pid, row.StartMicros); err != nil {
				outcome.Problems = append(outcome.Problems, fmt.Errorf("the session was not recorded: %w", err))
			}
		}
	} else if rowErr != nil {
		outcome.Problems = append(outcome.Problems, rowErr)
	}
	if signals != nil {
		signals.attach(func(sig syscall.Signal) {
			_, when, _ := signals.First()
			run.request(when, SignalName(sig))
			run.forward(sig)
		})
		closer.Add(signals.detach)
	}
	if spec.Control != nil {
		spec.Control(&Control{run: run})
	}
	if spec.Context != nil {
		closer.Add(stopAtEnd(spec.Context, &Control{run: run}))
	}
	if spec.Started != nil {
		spec.Started(run.pid)
	}
	output := startOutput(spec.Output, readers)

	exited := make(chan struct{})
	giveUp := make(chan struct{})
	var seen exitSeen
	var watching errgroup.Group
	watching.Go(func() error {
		defer close(exited)
		seen = run.awaitEnd(giveUp)
		outcome.Ended = spec.Clock.Now()
		return nil
	})
	watching.Go(func() error { return run.sampleWhileRunning(exited) })
	watching.Go(func() error { return run.killWhenOverdue(exited, giveUp) })
	_ = watching.Wait()

	askedAt, _ := run.firstRequest()
	deadline := spec.Clock.Now().Add(spec.Grace)
	if !askedAt.IsZero() {
		deadline = askedAt.Add(spec.Grace)
	}
	if signals != nil {
		if first, _, asked := signals.First(); asked {
			outcome.StoppedBy = first
		}
	}
	run.clearAfterEnd(&outcome, !askedAt.IsZero() || seen.signaled, deadline)
	run.mutex.Lock()
	state, killed := run.state, run.killed
	outcome.Problems = append(outcome.Problems, run.problems...)
	run.mutex.Unlock()
	outcome.Killed = killed
	if state != nil {
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			outcome.Signal = status.Signal()
		} else {
			outcome.Code = state.ExitCode()
		}
	}
	if spec.Mode == Group && spec.SessionsFile != "" {
		survivors, err := stopSessions(spec.SessionsFile, *spec.System, run.say)
		outcome.Survivors = append(outcome.Survivors, survivors...)
		if err != nil {
			outcome.Problems = append(outcome.Problems, err)
		}
	}
	unread, sinkProblem := output.finish(spec.Clock.After(spec.ReadBound), spec.ReadBound)
	outcome.Unread = unread
	if sinkProblem != nil {
		outcome.Problems = append(outcome.Problems, sinkProblem)
	}
	if dropped := words.close(spec.Clock.After(sayBound)); dropped > 0 {
		outcome.Problems = append(outcome.Problems, fmt.Errorf("%d lines of %s's own were not written: nobody read them", dropped, spec.Who))
	}
	endChildSpan(span, outcome)
	return outcome, nil
}

// startChildSpan is the span of the process a run starts (observability.md, section 2): under
// the span of the run's context, else the innermost open one; its TRACEPARENT is the child's.
func traceArgv(spec Spec) []string {
	if spec.TraceArgv != nil {
		return spec.TraceArgv
	}
	return spec.Argv
}

func startChildSpan(spec Spec) *telemetry.Span {
	parent := spec.Trace
	if parent == nil {
		parent = spec.Context
	}
	_, span := telemetry.Start(parent, "exec "+filepath.Base(spec.Argv[0]), telemetry.Options{
		Kind: trace.SpanKindClient, Attributes: []attribute.KeyValue{attribute.StringSlice(telemetry.AttrChildArgv, traceArgv(spec))}})
	return span
}

func endChildSpan(span *telemetry.Span, outcome Outcome) {
	span.Set(attribute.Int(telemetry.AttrExitCode, outcome.Status()))
	if outcome.StoppedBy != 0 {
		span.Set(attribute.String(telemetry.AttrStoppedBy, SignalName(outcome.StoppedBy)))
	}
	if outcome.Killed {
		span.Set(attribute.Bool(telemetry.AttrKilled, true))
	}
	switch {
	case outcome.Signal != 0:
		span.Set(attribute.String(telemetry.AttrSignal, SignalName(outcome.Signal)))
		span.Fail("signal_"+SignalName(outcome.Signal), "")
		span.End("signalled")
	case outcome.Code != 0:
		span.Fail(fmt.Sprintf("exit_%d", outcome.Code), "")
		span.End("exited")
	default:
		span.End("exited-0")
	}
}

// stopAtEnd stops the command when ctx ends, until the returned step is called (the run is over).
func stopAtEnd(ctx context.Context, control *Control) func() {
	runOver := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
			control.Stop()
		case <-runOver:
		}
	}()
	return func() {
		close(runOver)
		<-watched
	}
}

// executable finds a program in the same directory and PATH its process will use.
// Like exec.LookPath, it refuses an implicit relative executable from PATH.
func executable(program, dir string, env []string) (string, error) {
	if program == "" || program == "." || program == ".." || strings.ContainsRune(program, 0) || filepath.IsAbs(program) {
		return exec.LookPath(program)
	}
	if strings.ContainsRune(program, '/') {
		if dir == "" {
			return exec.LookPath(program)
		}
		absolute, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		// Keep the program's path components for the OS to resolve, including links and "..".
		return exec.LookPath(strings.TrimRight(absolute, "/") + "/" + program)
	}
	pathValue := os.Getenv("PATH")
	if env != nil {
		pathValue = ""
		for _, entry := range env {
			if value, found := strings.CutPrefix(entry, "PATH="); found {
				pathValue = value
				break
			}
		}
	}
	for _, folder := range filepath.SplitList(pathValue) {
		candidate := filepath.Join(folder, program)
		if filepath.IsAbs(candidate) {
			if path, err := exec.LookPath(candidate); err == nil {
				return path, nil
			}
			continue
		}
		absolute, err := filepath.Abs(filepath.Join(dir, candidate))
		if err != nil {
			return "", err
		}
		if _, err := exec.LookPath(absolute); err == nil {
			return candidate, &exec.Error{Name: program, Err: exec.ErrDot}
		}
	}
	return "", &exec.Error{Name: program, Err: exec.ErrNotFound}
}

// start starts the command; at a terminal the start that is refused the terminal is made again
// without it (the command never ran).
func start(spec Spec) (*os.Process, *terminal, []*lineReader, *inputFeed, error) {
	path, err := executable(spec.Argv[0], spec.Dir, spec.Env)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var tty *terminal
	if spec.Mode == Group && spec.Terminal {
		tty = openTerminal()
	}
	for {
		process, readers, input, err := startOnce(spec, path, tty)
		if err != nil && tty != nil && refusedTerminal(err) {
			tty.close()
			tty = nil
			continue
		}
		if err != nil {
			tty.close()
			return nil, nil, nil, nil, err
		}
		return process, tty, readers, input, nil
	}
}

func startOnce(spec Spec, path string, tty *terminal) (*os.Process, []*lineReader, *inputFeed, error) {
	var closeAfter, closeOnFailure []*os.File
	started := false
	defer func() {
		for _, file := range closeAfter {
			_ = file.Close()
		}
		if !started {
			for _, file := range closeOnFailure {
				_ = file.Close()
			}
		}
	}()
	stdin := spec.Stdin
	var inputWrite *os.File
	if stdin == nil && spec.Input != nil {
		inputRead, writeEnd, err := os.Pipe()
		if err != nil {
			return nil, nil, nil, err
		}
		closeAfter = append(closeAfter, inputRead)
		closeOnFailure = append(closeOnFailure, writeEnd)
		stdin, inputWrite = inputRead, writeEnd
	}
	if stdin == nil {
		devNull, err := os.Open(os.DevNull)
		if err != nil {
			return nil, nil, nil, err
		}
		closeAfter = append(closeAfter, devNull)
		stdin = devNull
	}
	stdout, stderr := spec.Stdout, spec.Stderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	var readers []*lineReader
	piped := func(stream Stream, raw io.Writer) (*os.File, error) {
		readEnd, writeEnd, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		closeAfter = append(closeAfter, writeEnd)
		closeOnFailure = append(closeOnFailure, readEnd)
		readers = append(readers, &lineReader{file: readEnd, stream: stream, raw: raw})
		return writeEnd, nil
	}
	var err error
	switch {
	case spec.Output != nil:
		if stdout, err = piped(Stdout, nil); err == nil {
			stderr, err = piped(Stderr, nil)
		}
	default:
		if spec.StdoutTo != nil {
			stdout, err = piped(Stdout, spec.StdoutTo)
		}
		if err == nil && spec.StderrTo != nil {
			stderr, err = piped(Stderr, spec.StderrTo)
		}
	}
	if err != nil {
		return nil, nil, nil, err
	}
	attributes := &syscall.SysProcAttr{Setsid: true}
	if spec.Mode == Group {
		attributes = tty.attributes()
	}
	process, err := os.StartProcess(path, spec.Argv, &os.ProcAttr{Dir: spec.Dir, Env: spec.Env,
		Files: []*os.File{stdin, stdout, stderr}, Sys: attributes})
	if err != nil {
		return nil, nil, nil, err
	}
	started = true
	return process, readers, feedInput(inputWrite, spec.Input), nil
}

// inputFeed writes the command's Input into its standard input's pipe from a goroutine of its
// own (a command that does not read must not hold the run).
type inputFeed struct {
	file    *os.File
	written chan struct{}
}

// feedInput starts writing input to file and closing it; nil file is no feed.
func feedInput(file *os.File, input []byte) *inputFeed {
	if file == nil {
		return nil
	}
	feed := &inputFeed{file: file, written: make(chan struct{})}
	go func() {
		defer close(feed.written)
		_, _ = file.Write(input)
		_ = file.Close()
	}()
	return feed
}

// finish closes the pipe under a write still blocked (the command ended without reading it all;
// the close ends the write) and waits for the writer.
func (feed *inputFeed) finish() {
	if feed == nil {
		return
	}
	_ = feed.file.Close()
	<-feed.written
}

// awaitEnd returns once the command has ended, without reaping it. When the end cannot be awaited
// it is looked for every poll; giveUp (the last KILL and its wait are over) ends the look, and the
// reap then waits for the end itself — no signal is needed any more.
func (run *running) awaitEnd(giveUp <-chan struct{}) exitSeen {
	seen, err := run.exits.await(run.pid)
	if err == nil {
		return seen
	}
	run.problem(fmt.Errorf("the command's end cannot be awaited (%w); it is looked for every %s instead", err,
		Seconds(run.spec.System.Poll)))
	for {
		if ended, err := run.exits.ended(run.pid); err == nil && ended {
			return exitSeen{}
		}
		select {
		case <-giveUp:
			return exitSeen{}
		case <-run.spec.Clock.After(run.spec.System.Poll):
		}
	}
}

// AwaitExit returns once the child pid has ended, without reaping it (kqueue's NOTE_EXIT on darwin,
// waitid with WNOWAIT on linux): until it is reaped its id, and so its group's, is still its own
// and may be signalled. For a runner that reaps with exec's Wait (internal/gitcli).
func AwaitExit(pid int) error {
	_, err := awaitExit(pid)
	return err
}

// sampleWhileRunning reads a session's tree now and then while its command runs, so a descendant
// that leaves the session is known by its parent before that parent ends.
func (run *running) sampleWhileRunning(exited <-chan struct{}) error {
	if run.spec.Mode != Session {
		return nil
	}
	for {
		select {
		case <-exited:
			return nil
		case <-run.spec.Clock.After(run.spec.SampleEvery):
		}
		table, err := run.spec.System.Read()
		if err != nil {
			continue
		}
		run.mutex.Lock()
		if !run.reaped {
			run.observe(table)
		}
		run.mutex.Unlock()
	}
}

// killWhenOverdue kills the command with its group (and its session's members) when the grace from
// the first request to stop is over and it still runs: one that ignores the stop cannot hang jackstay.
// When the kill's wait is over too, giveUp is closed.
func (run *running) killWhenOverdue(exited <-chan struct{}, giveUp chan<- struct{}) error {
	select {
	case <-exited:
		return nil
	case <-run.asked:
	}
	askedAt, askedBy := run.firstRequest()
	remaining := askedAt.Add(run.spec.Grace).Sub(run.spec.Clock.Now())
	select {
	case <-exited:
		return nil
	case <-run.spec.Clock.After(max(remaining, 0)):
	}
	table, err := run.spec.System.Read()
	run.mutex.Lock()
	if run.reaped {
		run.mutex.Unlock()
		return nil
	}
	run.killed = true
	run.signalAll(table, err == nil, unix.SIGKILL)
	run.mutex.Unlock()
	run.say("the command did not stop within %s of being asked to (%s); killing its process group %d (SIGKILL).",
		Seconds(run.spec.Grace), askedBy, run.pid)
	select {
	case <-exited:
	case <-run.spec.Clock.After(run.spec.KillWait):
		close(giveUp)
	}
	return nil
}

// forward passes a request to stop on while the command is unreaped: to its group, and in a
// session to each member outside the group too. After the reap nothing is the run's to signal.
func (run *running) forward(sig syscall.Signal) {
	table, err := run.spec.System.Read()
	run.mutex.Lock()
	defer run.mutex.Unlock()
	if run.reaped {
		return
	}
	run.signalAll(table, err == nil, sig)
}

// observe remembers, in a session, the members the table shows by the session's and the group's
// id, and the descendants found by parent links; the caller holds the mutex and the leader is
// unreaped.
func (run *running) observe(table map[int]procs.Process) {
	if run.spec.Mode != Session {
		return
	}
	for pid, row := range table {
		if row.Zombie || pid == run.pid {
			continue
		}
		if row.Session == run.pid || row.Group == run.pid {
			run.tree.Remember(pid, row.StartMicros)
		}
	}
	run.tree.Sample(table)
}

// members are the living processes of the command's group (and in a session, of its session and
// the descendants that left it) as the table shows them, the leader left out, by id; the caller
// holds the mutex and the leader is unreaped, so the group's and the session's ids are its own.
func (run *running) members(table map[int]procs.Process) []procs.Process {
	var found []procs.Process
	if run.spec.Mode == Group {
		for pid, row := range table {
			if !row.Zombie && pid != run.pid && row.Group == run.pid {
				found = append(found, row)
			}
		}
		sort.Slice(found, func(left, right int) bool { return found[left].PID < found[right].PID })
		return found
	}
	run.observe(table)
	for _, row := range run.tree.Living(table) {
		if row.PID != run.pid {
			found = append(found, row)
		}
	}
	return found
}

// signalAll sends sig to the command's group by its id and, in a session with a table, to each
// member outside the group; sent says the group took it (or had nothing left). The caller holds
// the mutex and the leader is unreaped.
func (run *running) signalAll(table map[int]procs.Process, haveTable bool, sig syscall.Signal) (bool, []*proctree.SignalError) {
	var refused []*proctree.SignalError
	err := run.spec.System.Signal(-run.pid, sig)
	sent := err == nil || errors.Is(err, unix.ESRCH)
	if !sent {
		refused = append(refused, refusal(-run.pid, run.spec.System.UID, run.spec.System.UID, err))
	}
	if run.spec.Mode == Session && haveTable {
		for _, row := range run.members(table) {
			if row.Group == run.pid {
				continue
			}
			if err := run.spec.System.Signal(row.PID, sig); err != nil && !errors.Is(err, unix.ESRCH) {
				refused = append(refused, refusal(row.PID, row.UID, run.spec.System.UID, err))
			}
		}
	}
	return sent, refused
}

// refusal is a signal the kernel refused, with why.
func refusal(target, owner, uid int, err error) *proctree.SignalError {
	problem := &proctree.SignalError{PID: target, Kind: proctree.OtherFailure, Err: err}
	if target < 0 {
		problem.PID, problem.Group = -target, true
	}
	if errors.Is(err, unix.EPERM) {
		problem.Kind = proctree.NotThisAccount
		if owner == uid {
			problem.Kind = proctree.SandboxForbids
		}
	}
	return problem
}

// reap reaps the command: no signal goes to its ids after this. The mutex is not held across the
// wait.
func (run *running) reap() {
	run.mutex.Lock()
	if run.reaped {
		run.mutex.Unlock()
		return
	}
	run.reaped = true
	run.mutex.Unlock()
	state, err := run.reapProcess()
	run.mutex.Lock()
	defer run.mutex.Unlock()
	run.state = state
	if err != nil {
		run.problems = append(run.problems, fmt.Errorf("reaping the command: %w", err))
	}
}

// output is the readers of a run's output.
type output struct {
	readers  []*lineReader
	sink     *lockedSink
	readDone chan struct{}
	readErr  error
}

func startOutput(sink Sink, readers []*lineReader) *output {
	reading := &output{readers: readers, sink: &lockedSink{sink: sink}, readDone: make(chan struct{})}
	if len(readers) == 0 {
		close(reading.readDone)
		return reading
	}
	var group errgroup.Group
	for _, reader := range readers {
		reader.sink = reading.sink
		group.Go(reader.read)
	}
	go func() {
		reading.readErr = group.Wait()
		close(reading.readDone)
	}()
	return reading
}

// OverlongLine is why output with a line longer than 1 MiB is not wholly read: the line was judged
// in pieces, and a pattern that spans two of them is not seen.
const OverlongLine = "a line of the output was longer than 1 MiB: it was read in pieces, so a pattern that spans two of them was not seen"

// finish waits, until bound fires, for the readers to reach the output's end; then the pipes are
// closed under them and they are joined (a closed pipe ends a read), and the reason is returned.
// The sink's first error comes with it.
func (reading *output) finish(bound <-chan time.Time, within time.Duration) (string, error) {
	if len(reading.readers) == 0 {
		return "", nil
	}
	defer func() {
		for _, reader := range reading.readers {
			_ = reader.file.Close()
		}
	}()
	select {
	case <-reading.readDone:
		if reading.readErr != nil {
			return "the output could not be read: " + reading.readErr.Error(), reading.sink.firstProblem()
		}
		for _, reader := range reading.readers {
			if reader.overlong {
				return OverlongLine, reading.sink.firstProblem()
			}
		}
		return "", reading.sink.firstProblem()
	case <-bound:
	}
	for _, reader := range reading.readers {
		_ = reader.file.Close()
	}
	<-reading.readDone // closed under them: the error says only that
	return "the output was not read to its end within " + Seconds(within) +
		" after the stop: something still holds it open", reading.sink.firstProblem()
}
