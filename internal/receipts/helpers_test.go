package receipts

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

// fakeClock moves a millisecond each time it is read, from a fixed start (W-013: no real time).
type fakeClock struct {
	mutex sync.Mutex
	now   time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
}

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

func (c *fakeClock) Sleep(d time.Duration) { c.Advance(d) }

func (c *fakeClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

// fakeRun is what the fake command does: its output, how it ends, and anything it does meanwhile.
type fakeRun struct {
	stdout, stderr []string
	outcome        supervise.Outcome
	err            error
	during         func()
	calls          int
	spec           supervise.Spec
}

func (f *fakeRun) run(_ *supervise.Signals, spec supervise.Spec) (supervise.Outcome, error) {
	f.calls++
	f.spec = spec
	if f.err != nil {
		return supervise.Outcome{}, f.err
	}
	if spec.Started != nil {
		spec.Started(4242)
	}
	for _, line := range f.stdout {
		_ = spec.Output.Line(supervise.Stdout, line)
	}
	for _, line := range f.stderr {
		_ = spec.Output.Line(supervise.Stderr, line)
	}
	if f.during != nil {
		f.during()
	}
	out := f.outcome
	out.PID = 4242
	return out, nil
}

// world is a repository with a jackstay.json, a fresh jackstay home, and a tester whose command is faked.
type world struct {
	t      *testing.T
	repo   *testrepo.Repo
	clock  *fakeClock
	env    map[string]string
	fake   *fakeRun
	stdout bytes.Buffer
	stderr bytes.Buffer
	ended  syscall.Signal
	held   func(context.Context, flock.Request) (*flock.Held, error)
}

// configFile opts `make test` out of reuse, so a test runs it each time; reuseConfig opts it in.
const configFile = `{"jackstay_schema": 3, "project": "demo", "receipts": {"inputs": ["src"], "env": [], "toolchain": [],
  "commands": [{"argv": ["make", "test"], "reuse": false, "evidence": "Executed ([0-9]+) tests?", "veto": "FAILED"},
               {"argv_prefix": ["make", "check"], "reuse": false, "evidence": "Executed ([0-9]+) tests?"}]}}`

func newWorld(t *testing.T, config string) *world {
	t.Helper()
	repo := testrepo.New(t, map[string]string{"jackstay.json": config, "src/a.swift": "let a = 1\n", ".gitignore": "build/\n"})
	repo.Git("checkout", "-q", "-b", "feature")
	repo.Write(map[string]string{"feature.txt": "f"})
	repo.Git("add", "feature.txt")
	repo.Git("commit", "-q", "-m", "feature")
	return &world{t: t, repo: repo, clock: newClock(), env: map[string]string{}, fake: &fakeRun{}}
}

func (w *world) tester() *tester {
	environ := []string{"PATH=" + os.Getenv("PATH")}
	for name, value := range w.env {
		environ = append(environ, name+"="+value)
	}
	w.stdout.Reset()
	w.stderr.Reset()
	acquire := w.held
	if acquire == nil {
		acquire = flock.Acquire
	}
	return &tester{stdout: &w.stdout, stderr: &w.stderr, clock: w.clock, getenv: func(name string) string { return w.env[name] },
		environ: environ, here: w.repo.Root, pid: 777, run: w.fake.run, listen: supervise.Listen,
		acquire: acquire, endBy: func(sig syscall.Signal) int { w.ended = sig; return 128 + int(sig) }}
}

// test runs `jst test ARGS` in the world.
func (w *world) test(args ...string) int {
	w.t.Helper()
	asked, ok := parseRaw(args)
	if !ok {
		w.t.Fatalf("cannot parse %q", args)
	}
	return w.tester().raw(asked)
}

// lastLine is the last line jst test said.
func (w *world) lastLine() string {
	lines := strings.Split(strings.TrimRight(w.stderr.String(), "\n"), "\n")
	return lines[len(lines)-1]
}

// records are the run log's lines.
func (w *world) records() []Record {
	path, err := RunLogPath()
	if err != nil {
		w.t.Fatal(err)
	}
	return ReadRecords([]string{path})
}

func (w *world) lastRecord(event string) Record {
	w.t.Helper()
	var found Record
	for _, record := range w.records() {
		if record.Text("event") == event {
			found = record
		}
	}
	if found == nil {
		w.t.Fatalf("no %s line in %v", event, w.records())
	}
	return found
}

func (w *world) storeFolder() string {
	folder, err := StateFolder("demo", "receipts")
	if err != nil {
		w.t.Fatal(err)
	}
	return folder
}

func (w *world) receipts() []string {
	matches, _ := filepath.Glob(filepath.Join(w.storeFolder(), "*.json"))
	return matches
}

func (w *world) notes() []string {
	matches, _ := filepath.Glob(filepath.Join(w.storeFolder(), "*.failed.*"))
	return matches
}

func passing(count string) *fakeRun {
	return &fakeRun{stdout: []string{"Test Suite 'All tests' started", "Executed " + count + " tests, with 0 failures"}}
}

var reuseConfig = strings.Replace(configFile, `"reuse": false, "evidence": "Executed ([0-9]+) tests?", "veto"`, `"reuse": true, "evidence": "Executed ([0-9]+) tests?", "veto"`, 1)
