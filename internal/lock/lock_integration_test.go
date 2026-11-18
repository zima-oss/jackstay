//go:build integration

package lock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// The test binary is `jst` (with this package's commands) when asJst is set, and a helper that
// records a session of its own when recordVariable is.
const (
	asJst          = "JACKSTAY_LOCK_TEST_AS_JST"
	recordVariable = "JACKSTAY_LOCK_TEST_RECORD"
)

// outerTimeout only ends a broken test; no outcome is decided by it (W-013).
const outerTimeout = 90 * time.Second

func TestMain(m *testing.M) {
	if os.Getenv(asJst) == "1" {
		os.Exit(cli.Main("jst", os.Args[1:]))
	}
	if path := os.Getenv(recordVariable); path != "" {
		// A run's own session, as `jst test <scope>` starts one, named in the sessions file.
		_, _ = unix.Setsid()
		row, _, _ := procs.Row(os.Getpid())
		_ = supervise.RecordSession(os.Getenv(supervise.SessionsFileVariable), os.Getpid(), row.StartMicros)
		_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fixture struct {
	folder, lock string
	env          []string
}

func newFixture(t *testing.T) fixture {
	folder, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fix := fixture{folder: folder, lock: filepath.Join(folder, "nested", "suite.lock")}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "JACKSTAY_") {
			fix.env = append(fix.env, entry)
		}
	}
	fix.env = append(fix.env, asJst+"=1", flock.PathVariable+"="+fix.lock, "JACKSTAY_HOME="+filepath.Join(folder, "home"))
	return fix
}

func (fix fixture) path(name string) string { return filepath.Join(fix.folder, name) }

func (fix fixture) command(dir string, env []string, args ...string) *exec.Cmd {
	command := exec.Command(os.Args[0], args...)
	command.Env = append(append([]string(nil), fix.env...), env...)
	command.Dir = dir
	if dir == "" {
		command.Dir = fix.folder
	}
	return command
}

type ran struct {
	status int
	stderr string
	stdout string
}

func (fix fixture) run(t *testing.T, env []string, args ...string) ran {
	t.Helper()
	return fix.runIn(t, "", env, args...)
}

func (fix fixture) runIn(t *testing.T, dir string, env []string, args ...string) ran {
	t.Helper()
	command := fix.command(dir, env, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	status := 0
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		status = exited.ExitCode()
		if waited := exited.Sys().(syscall.WaitStatus); waited.Signaled() {
			status = 128 + int(waited.Signal())
		}
	} else if err != nil {
		t.Fatal(err)
	}
	return ran{status: status, stderr: stderr.String(), stdout: stdout.String()}
}

// background starts `jst lock …` with its standard error in a file.
func (fix fixture) background(t *testing.T, name string, env []string, args ...string) *exec.Cmd {
	t.Helper()
	command := fix.command("", env, args...)
	said, err := os.Create(fix.path(name + ".said"))
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = said
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	said.Close()
	t.Cleanup(func() { _ = command.Process.Kill(); _, _ = command.Process.Wait() })
	return command
}

func (fix fixture) said(name string) string {
	data, _ := os.ReadFile(fix.path(name + ".said"))
	return string(data)
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(outerTimeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("never happened: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// loop waits for a file to exist: a holder's command that ends when the test says so.
const loop = `while [ ! -e "$0" ]; do sleep 0.05; done`

func (fix fixture) holder(t *testing.T, name string, options ...string) *exec.Cmd {
	t.Helper()
	args := append(append([]string{"lock"}, options...), "--", "sh", "-c", loop, fix.path(name+".go"))
	holder := fix.background(t, name, nil, args...)
	waitFor(t, "the holder's command runs", func() bool {
		note := flock.HolderNote(fix.lock)
		return strings.Contains(note, fmt.Sprintf("(the lock's %d)", holder.Process.Pid)) && strings.Contains(note, "process group")
	})
	return holder
}

func (fix fixture) release(t *testing.T, name string, holder *exec.Cmd) {
	t.Helper()
	os.WriteFile(fix.path(name+".go"), nil, 0o600)
	if err := holder.Wait(); err != nil {
		t.Fatalf("the holder %s ended %v: %s", name, err, fix.said(name))
	}
}

func (fix fixture) inLine() int {
	return len(flock.Waiting(fix.lock))
}

func TestTheCommandsStatusAndWhatItIsTold(t *testing.T) {
	fix := newFixture(t)
	if done := fix.run(t, nil, "lock", "--", "sh", "-c", "exit 7"); done.status != 7 || done.stderr != "" {
		t.Fatalf("%+v", done)
	}
	info, _ := os.Stat(filepath.Dir(fix.lock))
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("folder %v", info.Mode().Perm())
	}
	check := `case "$JACKSTAY_LOCK_HELD" in "$0":[0-9]*) ;; *) exit 1;; esac; test -z "$JACKSTAY_LOCK_OUTCOME_FILE$JACKSTAY_LOCK_NOTE_WORDS"`
	if done := fix.run(t, []string{OutcomeFileVariable + "=" + fix.path("outcome"), NoteWordsVariable + "=words"},
		"lock", "--", "sh", "-c", check, fix.lock); done.status != 0 {
		t.Fatalf("%+v", done)
	}
	inner := fix.path("inner")
	if done := fix.run(t, nil, "lock", "--", os.Args[0], "lock", "--", "sh", "-c", `: > "$0"`, inner); done.status != 0 ||
		done.stderr != "" {
		t.Fatalf("a nested lock: %+v", done)
	}
	if _, err := os.Stat(inner); err != nil {
		t.Fatal("the nested command did not run")
	}
}

func TestAWaitingRunSaysWhomItWaitsForAndGivesUp(t *testing.T) {
	fix := newFixture(t)
	holder := fix.holder(t, "head", "--project", "demo")
	never := fix.path("never")
	outcome := fix.path("outcome")
	gaveUp := fix.run(t, []string{OutcomeFileVariable + "=" + outcome}, "lock", "--wait", "0.3", "--", "sh", "-c", `: > "$0"`, never)
	data, _ := os.ReadFile(outcome)
	if gaveUp.status != 75 || !strings.Contains(gaveUp.stderr, "Nothing was run") || !strings.Contains(gaveUp.stderr,
		fmt.Sprintf("(the lock's %d)", holder.Process.Pid)) || string(data) != "gave-up\n" {
		t.Fatalf("%+v, outcome %q", gaveUp, data)
	}
	if _, err := os.Stat(never); err == nil {
		t.Fatal("a run that gave up ran")
	}
	// Naming the holder without being under it excuses nothing.
	claimed := fix.run(t, []string{flock.HeldVariable + "=" + fix.lock + ":" + strconv.Itoa(holder.Process.Pid)},
		"lock", "--wait", "0.2", "--", "true")
	if claimed.status != 75 {
		t.Fatalf("a claim was believed: %+v", claimed)
	}
	waiter := fix.background(t, "waiter", nil, "lock", "--project", "alpha", "--note-words", "the person's command", "--", "true")
	waitFor(t, "a run waits", func() bool { return fix.inLine() == 1 })
	status := fix.run(t, nil, "lock", "status", "--json")
	var report map[string]any
	if err := json.Unmarshal([]byte(status.stdout), &report); err != nil {
		t.Fatalf("%v: %q", err, status.stdout)
	}
	waiting, _ := report["waiting"].([]any)
	holderNote, _ := report["holder"].(string)
	if report["held"] != true || !strings.HasPrefix(holderNote, "project demo: sh -c ") || len(waiting) != 1 ||
		!strings.HasPrefix(waiting[0].(string), "project alpha: the person's command in ") {
		t.Fatalf("status %q", status.stdout)
	}
	plain := fix.run(t, nil, "lock", "status")
	if !strings.Contains(plain.stdout, "running: project demo: ") || !strings.Contains(plain.stdout, "  1. project alpha: ") {
		t.Fatalf("status %q", plain.stdout)
	}
	fix.release(t, "head", holder)
	if err := waiter.Wait(); err != nil || !strings.Contains(fix.said("waiter"), "waiting for another heavy run") ||
		!strings.Contains(fix.said("waiter"), "starting.") {
		t.Fatalf("%v: %q", err, fix.said("waiter"))
	}
	if done := fix.run(t, nil, "lock", "--wait", "30", "--", "true"); done.status != 0 || done.stderr != "" {
		t.Fatalf("%+v", done)
	}
}

func TestOutcomeWords(t *testing.T) {
	fix := newFixture(t)
	outcome := fix.path("outcome")
	for _, each := range []struct {
		status int
		word   string
		args   []string
	}{
		{75, "ran", []string{"--", "sh", "-c", "exit 75"}},
		{2, "ran", []string{"--", "sh", "-c", "exit 2"}},
		{127, "ran", []string{"--", "sh", "-c", "exit 127"}},
		{127, "cannot-start", []string{"--", fix.path("no-such-command")}},
		{2, "unusable", []string{"--also-hold", "relative.lock=X", "--", "true"}},
		{2, "unusable", []string{"--also-hold", fix.lock + "=X", "--", "true"}},
		{2, "unusable", []string{"--no-such-option", "--", "true"}},
	} {
		os.WriteFile(outcome, []byte("left from before\n"), 0o600)
		done := fix.run(t, []string{OutcomeFileVariable + "=" + outcome}, append([]string{"lock"}, each.args...)...)
		data, _ := os.ReadFile(outcome)
		if done.status != each.status || string(data) != each.word+"\n" {
			t.Errorf("%q: %d %q (%s)", each.args, done.status, data, done.stderr)
		}
	}
	os.WriteFile(outcome, []byte("left from before\n"), 0o600)
	if done := fix.run(t, []string{OutcomeFileVariable + "=" + outcome}, "lock", "status"); done.status != 0 {
		t.Fatal(done)
	}
	if data, _ := os.ReadFile(outcome); string(data) != "left from before\n" {
		t.Fatalf("status touched the outcome: %q", data)
	}
	if done := fix.run(t, []string{flock.PathVariable + "=relative.lock"}, "lock", "--", "true"); done.status != 2 ||
		!strings.Contains(done.stderr, "must be absolute") {
		t.Fatalf("%+v", done)
	}
}

func TestOlderLocks(t *testing.T) {
	fix := newFixture(t)
	older := fix.path("older/whole-suite.lock")
	seen := fix.path("seen")
	done := fix.run(t, nil, "lock", "--also-hold", older+"=A", "--also-hold", older+"=B", "--", "sh", "-c", `echo "$A|$B" > "$0"`, seen)
	data, _ := os.ReadFile(seen)
	if done.status != 0 || strings.Count(string(data), older+":") != 2 {
		t.Fatalf("%+v %q", done, data)
	}
	link := fix.path("link.lock")
	os.MkdirAll(filepath.Dir(fix.lock), 0o700)
	os.WriteFile(fix.lock, nil, 0o600)
	os.Symlink(fix.lock, link)
	if done := fix.run(t, nil, "lock", "--also-hold", link+"=X", "--", "true"); done.status != 2 || !strings.Contains(done.stderr, "very file") {
		t.Fatalf("%+v", done)
	}
	project := fix.path("project")
	os.Mkdir(project, 0o700)
	os.WriteFile(filepath.Join(project, "jackstay.json"), fmt.Appendf(nil,
		`{"jackstay_schema": 1, "project": "from-file", "lock": {"also_hold": [{"path": %q, "held_env": "OLDER_HELD"}]}}`, older), 0o600)
	done = fix.runIn(t, project, nil, "lock", "--", "sh", "-c", `echo "$OLDER_HELD" > "$0"`, seen)
	data, _ = os.ReadFile(seen)
	if done.status != 0 || !strings.HasPrefix(string(data), older+":") || !strings.HasPrefix(flock.HolderNote(older), "project from-file: ") ||
		!strings.HasPrefix(flock.HolderNote(fix.lock), "project from-file: ") {
		t.Fatalf("%+v %q %q", done, data, flock.HolderNote(older))
	}
	if told := fix.runIn(t, project, nil, "lock", "status"); !strings.Contains(told.stdout, "older lock "+older+": free") {
		t.Fatalf("%q", told.stdout)
	}
	bad := fix.path("bad")
	os.Mkdir(bad, 0o700)
	os.WriteFile(filepath.Join(bad, "jackstay.json"), []byte(`{"jackstay_schema": 1, "project": "bad", "lock": {"wait": 5}}`), 0o600)
	if done := fix.runIn(t, bad, nil, "lock", "--", "true"); done.status != 2 || !strings.Contains(done.stderr, "wait") {
		t.Fatalf("%+v", done)
	}
	if told := fix.runIn(t, bad, nil, "lock", "status"); told.status != 2 {
		t.Fatalf("%+v", told)
	}
}

func TestAnOlderLockHeldElsewhereIsWaitedForWithoutTheSharedOne(t *testing.T) {
	fix := newFixture(t)
	older := fix.path("older/whole-suite.lock")
	oldRun := fix.background(t, "old", []string{flock.PathVariable + "=" + older}, "lock", "--", "sh", "-c", loop, fix.path("old.go"))
	waitFor(t, "the earlier script's run holds the older lock", func() bool {
		return strings.Contains(flock.HolderNote(older), fmt.Sprintf("(the lock's %d)", oldRun.Process.Pid))
	})
	seen := fix.path("seen")
	both := fix.background(t, "both", nil, "lock", "--also-hold", older+"=OLDER_HELD", "--", "sh", "-c",
		`echo "$OLDER_HELD|$JACKSTAY_LOCK_HELD" > "$0"`, seen)
	waitFor(t, "it waits for the older lock", func() bool { return strings.Contains(fix.said("both"), "earlier lock "+older) })
	if flock.IsHeld(fix.lock) {
		t.Fatal("the shared lock is held while the older one is waited for")
	}
	if through := fix.run(t, nil, "lock", "--wait", "0", "--", "true"); through.status != 0 {
		t.Fatalf("another project's run did not go through: %+v", through)
	}
	fix.release(t, "old", oldRun)
	if err := both.Wait(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(seen)
	pid := both.Process.Pid
	if strings.TrimSpace(string(data)) != fmt.Sprintf("%s:%d|%s:%d", older, pid, fix.lock, pid) {
		t.Fatalf("told %q", data)
	}
}

func TestWhatTheCommandLeftIsStoppedBeforeTheLockIsFree(t *testing.T) {
	fix := newFixture(t)
	left := fix.path("left")
	done := fix.run(t, nil, "lock", "--", "sh", "-c", `sleep 120 & echo $! > "$0"; exit 7`, left)
	data, _ := os.ReadFile(left)
	sleeper, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	t.Cleanup(func() { _ = unix.Kill(sleeper, unix.SIGKILL) })
	row, found, _ := procs.Row(sleeper)
	if done.status != 7 || !strings.Contains(done.stderr, "left ") || !strings.Contains(done.stderr, "SIGTERM") ||
		strings.Contains(done.stderr, "SIGKILL") || (found && !row.Zombie) {
		t.Fatalf("%+v, sleep found %v", done, found)
	}
}

func TestARecordedSessionIsStoppedAfterTheEnd(t *testing.T) {
	fix := newFixture(t)
	sessions, ready := fix.path("sessions"), fix.path("recorded")
	os.WriteFile(sessions, nil, 0o600)
	done := fix.run(t, []string{supervise.SessionsFileVariable + "=" + sessions}, "lock", "--", "sh", "-c",
		`/usr/bin/env -u `+asJst+` `+recordVariable+`="$1" "$0" & while [ ! -s "$1" ]; do sleep 0.05; done`, os.Args[0], ready)
	data, _ := os.ReadFile(ready)
	recorded, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	t.Cleanup(func() { _ = unix.Kill(recorded, unix.SIGKILL) })
	row, found, _ := procs.Row(recorded)
	if done.status != 0 || recorded == 0 || (found && !row.Zombie) || !strings.Contains(done.stderr, "session") {
		t.Fatalf("%+v, the recorded session's process runs on: %v", done, found)
	}
}

// startCaught starts a command with the stop signals' default dispositions, whatever this test inherited.
func startCaught(t *testing.T, command *exec.Cmd) {
	t.Helper()
	caught := make(chan os.Signal, 4)
	signal.Notify(caught, unix.SIGHUP, unix.SIGINT, unix.SIGTERM, unix.SIGQUIT)
	err := command.Start()
	signal.Stop(caught)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _, _ = command.Process.Wait() })
}

func TestACommandIgnoringTheStopIsKilledAndTheLockEndsByTheSignal(t *testing.T) {
	fix := newFixture(t)
	mark := fix.path("deaf")
	command := fix.command("", []string{GroupWaitVariable + "=0.5"}, "lock", "--", "sh", "-c",
		`trap "" TERM; echo $$ > "$0"; while :; do sleep 0.1; done`, mark)
	var said bytes.Buffer
	command.Stderr = &said
	startCaught(t, command)
	waitFor(t, "the command runs", func() bool { data, _ := os.ReadFile(mark); return len(data) > 0 })
	_ = command.Process.Signal(unix.SIGTERM)
	_ = command.Process.Signal(unix.SIGTERM) // a second request is passed on and changes nothing
	err := command.Wait()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || !exited.Sys().(syscall.WaitStatus).Signaled() || exited.Sys().(syscall.WaitStatus).Signal() != unix.SIGTERM {
		t.Fatalf("ended %v: %s", err, said.String())
	}
	if !strings.Contains(said.String(), "did not stop within 0.5 s of being asked to (SIGTERM)") || strings.Contains(said.String(), "STILL RUNNING") {
		t.Fatalf("said %q", said.String())
	}
	if flock.IsHeld(fix.lock) {
		t.Fatal("the lock is held after the run")
	}
}

func TestAHolderAskedToStopEndsByThatSignalWithWhatItStarted(t *testing.T) {
	fix := newFixture(t)
	alive := fix.path("alive")
	command := fix.command("", nil, "lock", "--", "sh", "-c", `sh -c 'while :; do sleep 0.05; done' & echo $! > "$0"; wait`, alive)
	startCaught(t, command)
	var grandchild int
	waitFor(t, "the grandchild runs", func() bool {
		data, _ := os.ReadFile(alive)
		grandchild, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return grandchild > 0
	})
	t.Cleanup(func() { _ = unix.Kill(grandchild, unix.SIGKILL) })
	// TERM, not INT: a shell's background job ignores SIGINT and SIGQUIT (POSIX), so INT would leave the
	// grandchild to the 30 s kill.
	_ = command.Process.Signal(unix.SIGTERM)
	err := command.Wait()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.Sys().(syscall.WaitStatus).Signal() != unix.SIGTERM {
		t.Fatalf("ended %v", err)
	}
	if row, found, _ := procs.Row(grandchild); found && !row.Zombie {
		t.Fatal("what the command started runs on")
	}
}

func TestARunInAFolderThatIsGone(t *testing.T) {
	fix := newFixture(t)
	vanished := fix.path("vanished")
	os.Mkdir(vanished, 0o700)
	command := exec.Command("sh", "-c", `cd "$0" && rmdir "$0" && exec "$@"`, vanished, os.Args[0], "lock", "--", "sh", "-c", "exit 5")
	command.Env = fix.env
	err := command.Run()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 5 || !strings.Contains(flock.HolderNote(fix.lock), "a folder that is gone") {
		t.Fatalf("%v, note %q", err, flock.HolderNote(fix.lock))
	}
}

func TestTheCallersWordsInTheNote(t *testing.T) {
	fix := newFixture(t)
	if done := fix.run(t, nil, "lock", "--project", "demo", "--note-words", "make test\n  (through jst test)", "--", "true"); done.status != 0 ||
		!strings.HasPrefix(flock.HolderNote(fix.lock), "project demo: make test (through jst test) in ") {
		t.Fatalf("%+v %q", done, flock.HolderNote(fix.lock))
	}
	if done := fix.run(t, []string{NoteWordsVariable + "=just gate"}, "lock", "--project", "demo", "--", "true"); done.status != 0 ||
		!strings.HasPrefix(flock.HolderNote(fix.lock), "project demo: just gate in ") {
		t.Fatalf("%+v %q", done, flock.HolderNote(fix.lock))
	}
	if done := fix.run(t, nil, "lock"); done.status != 2 || !strings.Contains(done.stderr, "usage") {
		t.Fatalf("%+v", done)
	}
}
