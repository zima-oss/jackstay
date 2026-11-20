package scopes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/receipts"
	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

// fakeClock moves a millisecond each time it is read (W-013: no real time); Set moves it.
type fakeClock struct {
	mutex sync.Mutex
	now   time.Time
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) Set(moment time.Time) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.now = moment
}

func (c *fakeClock) Sleep(d time.Duration) { c.Advance(d) }

func (c *fakeClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

// fixtures is the recorded outputs' folder, found before any test moves into its repository.
var fixtures = func() string {
	here, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Join(here, "..", "..", "tests", "fixtures", "swift-test")
}()

// fixture is an output of `swift test` in tests/fixtures/swift-test (the shape of real runs, with
// neutral names), read as it is: only the judge's tests expand `@passing` lines.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtures, name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\n@passing ") {
		t.Fatalf("%s has `@passing` lines, which only the judge's tests expand", name)
	}
	return string(data)
}

// override is what a test makes of one launch instead of running it: nil runs it.
type override func(order launch) *launched

// world is a repository with a jackstay.json, a fresh jackstay home, a side folder the commands write to,
// and a tester whose commands run without supervise (exec, both streams read whole), unless an
// override says how a launch ends.
type world struct {
	t          *testing.T
	repo       *testrepo.Repo
	side       string
	tmp        string
	clock      *fakeClock
	env        map[string]string
	stdout     bytes.Buffer
	stderr     bytes.Buffer
	ended      syscall.Signal
	overrides  map[string]override
	launches   []launch
	alive      func(int) bool
	firstStop  func(*supervise.Signals) (syscall.Signal, time.Time, bool)
	git        func(ctx context.Context, dir string, args ...string) ([]byte, error)
	unreadable bool
	mutex      sync.Mutex
	// launcher and machineClock replace the exec launch and the fake clock (integration tests).
	launcher     func(*supervise.Signals, launch) (launched, error)
	machineClock supervise.Clock
}

func newWorld(t *testing.T, test map[string]any, more map[string]any) *world {
	t.Helper()
	base := t.TempDir()
	side, tmp := filepath.Join(base, "side"), filepath.Join(base, "tmp")
	for _, folder := range []string{side, tmp} {
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"pkg-filtered-xctest", "pkg-mixed-failed", "pkg-swift-testing-only"} {
		if err := os.WriteFile(filepath.Join(side, name+".log"), []byte(fixture(t, name)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repo := testrepo.New(t, map[string]string{"jackstay.json": configText(t, test, more), "Sources/a.swift": "let a = 1\n",
		".gitignore": ".build/\n"})
	repo.Git("checkout", "-q", "-b", "work")
	realSide, err := filepath.EvalSymlinks(side)
	if err != nil {
		t.Fatal(err)
	}
	realTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, repo: repo, side: realSide, tmp: realTmp, clock: newClock(), env: map[string]string{},
		overrides: map[string]override{}, alive: func(int) bool { return false }}
}

// configText is a jackstay.json with the test section and the other sections given.
func configText(t *testing.T, test map[string]any, more map[string]any) string {
	t.Helper()
	config := map[string]any{"jackstay_schema": 3, "project": "demo",
		"receipts": map[string]any{"inputs": []any{"."}, "toolchain": []any{}, "env": []any{"DEMO_*"}}}
	if test != nil {
		config["test"] = test
	}
	for name, value := range more {
		config[name] = value
	}
	data, err := json.MarshalIndent(config, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// configure rewrites jackstay.json (not committed).
func (w *world) configure(test map[string]any, more map[string]any) {
	w.repo.Write(map[string]string{"jackstay.json": configText(w.t, test, more)})
}

func (w *world) commit() {
	w.repo.Git("add", "-A")
	w.repo.Git("commit", "-q", "--allow-empty", "-m", "more")
}

func (w *world) environ() []string {
	environ := []string{"PATH=" + os.Getenv("PATH"), "SIDE=" + w.side, "TMPDIR=" + w.tmp + "/", "HOME=" + os.Getenv("HOME")}
	for name, value := range w.env {
		environ = append(environ, name+"="+value)
	}
	return environ
}

func (w *world) tester() *tester {
	w.stdout.Reset()
	w.stderr.Reset()
	stdout, stderr := guarded(&w.stdout, &w.stderr)
	values := map[string]string{}
	for _, pair := range w.environ() {
		name, value, _ := strings.Cut(pair, "=")
		values[name] = value
	}
	firstStop := w.firstStop
	if firstStop == nil {
		firstStop = (*supervise.Signals).First
	}
	git := w.git
	if git == nil {
		git = runGit
	}
	var clock supervise.Clock = w.clock
	if w.machineClock != nil {
		clock = w.machineClock
	}
	launcher := w.launch
	if w.launcher != nil {
		launcher = w.launcher
	}
	base := &tester{stdout: stdout, stderr: stderr, clock: clock, getenv: func(name string) string { return values[name] },
		environ: w.environ(), here: w.repo.Root, pid: 777, tmpdir: w.tmp + "/", launch: launcher,
		listen: supervise.Listen, acquire: flock.Acquire, firstStop: firstStop, git: git, alive: w.alive,
		logPaths: receipts.RunLogPaths,
		endBy:    func(sig syscall.Signal) int { w.ended = sig; return 128 + int(sig) }}
	if w.unreadable {
		base.system = unreadableSystem()
	}
	return base
}

// launch runs the command with exec (no session, no tree) unless an override for one of its
// words says how it ends.
func (w *world) launch(_ *supervise.Signals, order launch) (launched, error) {
	w.mutex.Lock()
	w.launches = append(w.launches, order)
	w.mutex.Unlock()
	for _, word := range order.spec.Argv {
		for marker, how := range w.overrides {
			if strings.Contains(word, marker) {
				if result := how(order); result != nil {
					if result.outcome.Ended.IsZero() {
						result.outcome.Ended = order.spec.Clock.Now()
					}
					return *result, nil
				}
			}
		}
	}
	command := exec.Command(order.spec.Argv[0], order.spec.Argv[1:]...)
	command.Dir, command.Env = order.spec.Dir, order.spec.Env
	var out, errs bytes.Buffer
	command.Stdout, command.Stderr = &out, &errs
	err := command.Run()
	var exitErr *exec.ExitError
	outcome := supervise.Outcome{PID: 4242}
	switch {
	case errors.As(err, &exitErr):
		outcome.Code = exitErr.ExitCode()
	case err != nil:
		return launched{}, &supervise.StartError{Command: order.spec.Argv[0], Err: err}
	}
	feed(order.spec.Output, out.String(), errs.String())
	outcome.Ended = order.spec.Clock.Now()
	return launched{outcome: outcome}, nil
}

// feed gives both streams' lines to a sink.
func feed(sink supervise.Sink, stdout, stderr string) {
	for _, pair := range []struct {
		stream supervise.Stream
		text   string
	}{{supervise.Stdout, stdout}, {supervise.Stderr, stderr}} {
		text := strings.TrimSuffix(pair.text, "\n")
		if text == "" {
			continue
		}
		for _, line := range strings.Split(text, "\n") {
			_ = sink.Line(pair.stream, line)
		}
	}
}

// jackstay runs `jst test ARGS` (`list` and `plan` as their commands) in the world.
func (w *world) jst(args ...string) int {
	w.t.Helper()
	runner := w.tester()
	switch {
	case len(args) > 0 && args[0] == "list":
		return runner.list(args[1:])
	case len(args) > 0 && args[0] == "plan":
		return runner.plan(args[1:])
	}
	return runner.scope(args)
}

// lastLine is the last `jst test:` line said.
func (w *world) lastLine() string {
	var last string
	for _, line := range strings.Split(w.stderr.String(), "\n") {
		if strings.HasPrefix(line, "jst test: ") {
			last = line
		}
	}
	return last
}

func (w *world) records(event string) []receipts.Record {
	path, err := receipts.RunLogPath()
	if err != nil {
		w.t.Fatal(err)
	}
	var found []receipts.Record
	for _, record := range receipts.ReadRecords([]string{path}) {
		if event == "" || record.Text("event") == event {
			found = append(found, record)
		}
	}
	return found
}

func (w *world) lastRecord(event string) receipts.Record {
	w.t.Helper()
	found := w.records(event)
	if len(found) == 0 {
		w.t.Fatalf("no %s line; stderr:\n%s", event, w.stderr.String())
	}
	return found[len(found)-1]
}

// counter is how many times a counted step ran.
func (w *world) counter(name string) int {
	data, err := os.ReadFile(filepath.Join(w.side, name))
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(data)))
}

func (w *world) at(name string) string { return filepath.Join(w.side, name) }

func (w *world) read(name string) string {
	w.t.Helper()
	data, err := os.ReadFile(w.at(name))
	if err != nil {
		w.t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func (w *world) flag(name string, on bool) {
	if on {
		_ = os.WriteFile(w.at(name), nil, 0o600)
		return
	}
	_ = os.Remove(w.at(name))
}

// expectStatus fails the test when the status is not wanted, with what jackstay said.
func (w *world) expectStatus(status, wanted int) {
	w.t.Helper()
	if status != wanted {
		w.t.Fatalf("status %d, want %d; stderr:\n%s", status, wanted, w.stderr.String())
	}
}

func (w *world) expectLast(wanted ...string) {
	w.t.Helper()
	for _, text := range wanted {
		if !strings.Contains(w.lastLine(), text) {
			w.t.Fatalf("the last line %q does not hold %q; stderr:\n%s", w.lastLine(), text, w.stderr.String())
		}
	}
}

func (w *world) expectSaid(wanted string) {
	w.t.Helper()
	if !strings.Contains(w.stderr.String(), wanted) {
		w.t.Fatalf("jackstay did not say %q; stderr:\n%s", wanted, w.stderr.String())
	}
}

func cat(name string) []any { return []any{"sh", "-c", `cat "$SIDE/` + name + `.log"`} }

// counted is a step that counts its runs in $SIDE/<name> and then runs `then` (shell).
func counted(name, then string) []any {
	return []any{"sh", "-c", `echo run >> "$SIDE/` + name + `"; ` + then}
}

func argv(items ...string) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}
