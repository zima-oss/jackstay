//go:build integration

package supervise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/procs"
)

// The test binary is also each helper process these tests need, chosen by helperVariable.
const (
	helperVariable = "JACKSTAY_SUPERVISE_HELPER"
	fileVariable   = "JACKSTAY_SUPERVISE_HELPER_FILE"
	goVariable     = "JACKSTAY_SUPERVISE_HELPER_GO"
)

// outerTimeout only ends a broken test; no outcome is decided by it (W-013).
const outerTimeout = 90 * time.Second

func TestMain(m *testing.M) {
	switch os.Getenv(helperVariable) {
	case "":
		os.Exit(m.Run())
	case "setsid-sleeper":
		// Leaves its parent's session, says so, and waits to be stopped.
		_, _ = unix.Setsid()
		_ = os.WriteFile(os.Getenv(fileVariable), []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(time.Hour)
		os.Exit(0)
	case "hup-check":
		// Says it is ready, waits for the word to go, and tells whether SIGHUP is ignored in it.
		_ = os.WriteFile(os.Getenv(fileVariable), []byte("ready"), 0o600)
		awaitFile(os.Getenv(goVariable))
		if signal.Ignored(unix.SIGHUP) {
			os.Exit(0)
		}
		os.Exit(3)
	case "run-group":
		// A supervisor as `jst lock` is one: the command after its own arguments, in a group of its own.
		signals := Listen(context.Background(), RealClock())
		outcome, err := Run(signals, Spec{Argv: os.Args[1:], Mode: Group, Terminal: true, Stdin: os.Stdin})
		if err != nil {
			os.Exit(127)
		}
		if outcome.StoppedBy != 0 {
			signals.Close()
			os.Exit(EndBy(outcome.StoppedBy))
		}
		os.Exit(outcome.Status())
	case "end-by":
		number, _ := strconv.Atoi(os.Args[1])
		signals := Listen(context.Background(), RealClock())
		signals.Close()
		os.Exit(EndBy(syscall.Signal(number)))
	case "tty-front":
		os.Exit(inFront())
	case "tty-session":
		// A session leader with a pseudo-terminal: the command's group must have it while it runs,
		// and the leader after.
		outcome, err := Run(nil, Spec{Argv: []string{os.Args[0]}, Env: helperEnv("tty-front"),
			Mode: Group, Terminal: true})
		lent := -1
		if err == nil {
			lent = outcome.Status()
		}
		_ = os.WriteFile(os.Getenv(fileVariable), fmt.Appendf(nil, "lent=%d back=%d", lent, inFront()), 0o600)
		os.Exit(0)
	}
	os.Exit(99)
}

// helperEnv is this process's environment with the helper's mode replaced (a variable named twice
// would be read as its first: a helper starting itself for ever).
func helperEnv(mode string, more ...string) []string {
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, helperVariable+"=") {
			env = append(env, entry)
		}
	}
	return append(append(env, helperVariable+"="+mode), more...)
}

// inFront is 0 when this process's group is its terminal's, else 3.
func inFront() int {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 4
	}
	defer tty.Close()
	front, err := unix.IoctlGetInt(int(tty.Fd()), unix.TIOCGPGRP)
	if err != nil || front != unix.Getpgrp() {
		return 3
	}
	return 0
}

func awaitFile(path string) {
	deadline := time.Now().Add(outerTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
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

func readPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	waitFor(t, "a pid in "+path, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && pid > 0
	})
	return pid
}

// gone says the process no longer runs.
func gone(pid int) bool {
	row, found, err := procs.Row(pid)
	return err == nil && (!found || row.Zombie)
}

func killLater(t *testing.T, pid int) {
	t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
}

type lines struct {
	mutex sync.Mutex
	got   []string
}

func (sink *lines) Line(stream Stream, text string) error {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	sink.got = append(sink.got, stream.String()+":"+text)
	return nil
}

func TestStatusAndOutputThroughPipes(t *testing.T) {
	sink := &lines{}
	outcome, err := Run(nil, Spec{Mode: Group, Output: sink,
		Argv: []string{"sh", "-c", `printf '\033[31mred\033[0m\nplain'; printf 'err\n' >&2; exit 7`}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(sink.got, "|")
	if outcome.Started.IsZero() || outcome.Ended.Before(outcome.Started) {
		t.Fatalf("started %v, ended %v", outcome.Started, outcome.Ended)
	}
	if outcome.Status() != 7 || outcome.Unread != "" || !strings.Contains(joined, "stdout:red") ||
		!strings.Contains(joined, "stdout:plain") || !strings.Contains(joined, "stderr:err") {
		t.Fatalf("status %d, unread %q, lines %q", outcome.Status(), outcome.Unread, joined)
	}
}

func TestACommandThatCannotStart(t *testing.T) {
	_, err := Run(nil, Spec{Mode: Group, Argv: []string{filepath.Join(t.TempDir(), "no-such-command")}})
	var startErr *StartError
	if !errors.As(err, &startErr) {
		t.Fatalf("error %v", err)
	}
}

func TestWhatTheCommandLeftInItsGroupIsStopped(t *testing.T) {
	left := filepath.Join(t.TempDir(), "left")
	words := &lockedBuffer{}
	outcome, err := Run(nil, Spec{Mode: Group, Say: words, Who: "jst lock",
		Argv: []string{"sh", "-c", `sleep 600 & echo $! > "$0"; exit 7`, left}})
	if err != nil {
		t.Fatal(err)
	}
	sleeper := readPID(t, left)
	killLater(t, sleeper)
	if outcome.Status() != 7 || len(outcome.Left) != 1 || outcome.Left[0].PID != sleeper || len(outcome.Survivors) != 0 {
		t.Fatalf("outcome %+v, said %q", outcome, words.String())
	}
	waitFor(t, "the words are written", func() bool { return strings.Contains(words.String(), "SIGTERM") })
	if !gone(sleeper) || strings.Contains(words.String(), "SIGKILL") {
		t.Fatalf("the sleep runs on (%v), said %q", !gone(sleeper), words.String())
	}
}

// With real processes: a leftover that starts a replacement in the group
// on its way out leaves the group populated; the run ends only once the replacement is gone too.
// The leftover catches TERM, starts the replacement, writes its id (the acknowledgement) and only
// then ends, so the group is never empty before the replacement exists; the replacement dies by
// that TERM sent to it, long before the grace (an outer bound only) could matter (W-013).
func TestAReplacementStartedByALeftoverIsStoppedBeforeTheRunEnds(t *testing.T) {
	folder := t.TempDir()
	ready, replacement := filepath.Join(folder, "ready"), filepath.Join(folder, "replacement")
	leftover := `trap 'sleep 600 & echo $! > "$1"; exit 0' TERM; echo ready > "$0"; while :; do sleep 0.1; done`
	outcome, err := Run(nil, Spec{Mode: Group, Grace: outerTimeout,
		Argv: []string{"sh", "-c", `sh -c "$2" "$0" "$1" & while [ ! -s "$0" ]; do sleep 0.05; done`, ready, replacement, leftover}})
	if err != nil {
		t.Fatal(err)
	}
	spawned := readPID(t, replacement)
	killLater(t, spawned)
	if !gone(spawned) || len(outcome.Survivors) != 0 || len(outcome.Left) == 0 || outcome.Killed {
		t.Fatalf("the replacement runs on (%v): outcome %+v", !gone(spawned), outcome)
	}
}

func TestACommandIgnoringTheStopIsKilledWhenTheGraceIsOver(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	incoming := make(chan os.Signal, 4)
	signals := newSignals(context.Background(), incoming, RealClock())
	go signals.loop()
	defer signals.Close()
	done := make(chan Outcome, 1)
	words := &lockedBuffer{}
	go func() {
		outcome, _ := Run(signals, Spec{Mode: Group, Grace: 500 * time.Millisecond, Say: words, Who: "jst lock",
			Argv: []string{"sh", "-c", `trap "" TERM; echo ready > "$0"; while :; do sleep 0.1; done`, ready}})
		done <- outcome
	}()
	waitFor(t, "the command is ready", func() bool { _, err := os.Stat(ready); return err == nil })
	incoming <- unix.SIGTERM
	incoming <- unix.SIGTERM // a second request is passed on and changes nothing
	var outcome Outcome
	select {
	case outcome = <-done:
	case <-time.After(outerTimeout):
		t.Fatal("the run never ended")
	}
	if !outcome.Killed || outcome.StoppedBy != unix.SIGTERM || outcome.Signal != unix.SIGKILL || len(outcome.Survivors) != 0 {
		t.Fatalf("outcome %+v, said %q", outcome, words.String())
	}
	waitFor(t, "the words are written", func() bool {
		return strings.Contains(words.String(), "did not stop within 0.5 s of being asked to (SIGTERM)")
	})
}

func TestASessionsGrandchildThatLeftTheSessionIsStopped(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild")
	incoming := make(chan os.Signal, 4)
	signals := newSignals(context.Background(), incoming, RealClock())
	go signals.loop()
	defer signals.Close()
	done := make(chan Outcome, 1)
	go func() {
		outcome, _ := Run(signals, Spec{Mode: Session, Grace: 5 * time.Second,
			Env:  helperEnv("setsid-sleeper", fileVariable+"="+pidFile),
			Argv: []string{"sh", "-c", `"$0" & wait`, os.Args[0]}})
		done <- outcome
	}()
	grandchild := readPID(t, pidFile)
	killLater(t, grandchild)
	if session, err := unix.Getsid(grandchild); err != nil || session != grandchild {
		t.Fatalf("the grandchild did not leave the session: %d %v", session, err)
	}
	incoming <- unix.SIGTERM
	select {
	case outcome := <-done:
		if outcome.StoppedBy != unix.SIGTERM || len(outcome.Survivors) != 0 {
			t.Fatalf("outcome %+v", outcome)
		}
	case <-time.After(outerTimeout):
		t.Fatal("the run never ended")
	}
	if !gone(grandchild) {
		t.Fatal("the grandchild that left the session runs on")
	}
}

func TestASessionsReparentedChildIsStoppedAfterANormalEnd(t *testing.T) {
	left := filepath.Join(t.TempDir(), "left")
	outcome, err := Run(nil, Spec{Mode: Session, Argv: []string{"sh", "-c", `sleep 600 & echo $! > "$0"; exit 0`, left}})
	if err != nil {
		t.Fatal(err)
	}
	sleeper := readPID(t, left)
	killLater(t, sleeper)
	if outcome.Status() != 0 || len(outcome.Left) != 1 || !gone(sleeper) {
		t.Fatalf("outcome %+v, gone %v", outcome, gone(sleeper))
	}
}

func TestOutputHeldOpenByWhatLeftTheGroupIsUnreadAfterTheBound(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "holder")
	outcome, err := Run(nil, Spec{Mode: Group, Output: &lines{}, ReadBound: 200 * time.Millisecond,
		Env:  helperEnv("setsid-sleeper", fileVariable+"="+pidFile),
		Argv: []string{"sh", "-c", `"$0" & while [ ! -s "$1" ]; do sleep 0.05; done`, os.Args[0], pidFile}})
	if err != nil {
		t.Fatal(err)
	}
	killLater(t, readPID(t, pidFile))
	if outcome.Status() != 0 || !strings.Contains(outcome.Unread, "not read to its end") {
		t.Fatalf("outcome %+v", outcome)
	}
}

func helper(t *testing.T, mode string, args ...string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], args...)
	command.Env = helperEnv(mode)
	return command
}

func TestASignalIgnoredAtStartStaysIgnoredInTheSupervisorAndTheCommand(t *testing.T) {
	folder := t.TempDir()
	ready, goFile := filepath.Join(folder, "ready"), filepath.Join(folder, "go")
	command := helper(t, "run-group")
	command.Env = append(command.Env, fileVariable+"="+ready, goVariable+"="+goFile)
	// The command the supervisor runs is the hup-check helper (env sets its mode).
	command.Args = []string{os.Args[0], "/usr/bin/env", helperVariable + "=hup-check", os.Args[0]}
	signal.Ignore(unix.SIGHUP)
	err := command.Start()
	signal.Reset(unix.SIGHUP)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the command is ready", func() bool { _, err := os.Stat(ready); return err == nil })
	_ = command.Process.Signal(unix.SIGHUP)
	if err := os.WriteFile(goFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("the supervisor ignoring SIGHUP ended %v", err)
	}
}

func TestASignalNotIgnoredIsTheDefaultOneInTheCommand(t *testing.T) {
	folder := t.TempDir()
	ready, goFile := filepath.Join(folder, "ready"), filepath.Join(folder, "go")
	command := helper(t, "run-group")
	command.Env = append(command.Env, fileVariable+"="+ready, goVariable+"="+goFile)
	command.Args = []string{os.Args[0], "/usr/bin/env", helperVariable + "=hup-check", os.Args[0]}
	// Caught here, so the helper starts with SIGHUP's default whatever this test inherited.
	caught := make(chan os.Signal, 1)
	signal.Notify(caught, unix.SIGHUP)
	err := command.Start()
	signal.Stop(caught)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the command is ready", func() bool { _, err := os.Stat(ready); return err == nil })
	if err := os.WriteFile(goFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var exited *exec.ExitError
	if err := command.Wait(); !errors.As(err, &exited) || exited.ExitCode() != 3 {
		t.Fatalf("the command's SIGHUP: %v", err)
	}
}

func TestEndByEndsByTheSignalAndQuitExits131(t *testing.T) {
	for _, each := range []struct {
		sig    syscall.Signal
		status int
	}{{unix.SIGTERM, -1}, {unix.SIGINT, -1}, {unix.SIGHUP, -1}, {unix.SIGQUIT, 131}} {
		command := helper(t, "end-by", strconv.Itoa(int(each.sig)))
		// Caught here while the helper starts, so it starts with each signal's default whatever this
		// test inherited (a SIGHUP ignored at start is never caught, so never ended by).
		caught := make(chan os.Signal, 4)
		signal.Notify(caught, unix.SIGHUP, unix.SIGINT, unix.SIGTERM, unix.SIGQUIT)
		err := command.Start()
		signal.Stop(caught)
		if err != nil {
			t.Fatal(err)
		}
		err = command.Wait()
		var exited *exec.ExitError
		if !errors.As(err, &exited) {
			t.Fatalf("%v: %v", each.sig, err)
		}
		status := exited.Sys().(syscall.WaitStatus)
		switch {
		case each.status < 0 && (!status.Signaled() || status.Signal() != each.sig):
			t.Errorf("%v: ended %v", each.sig, status)
		case each.status >= 0 && status.ExitStatus() != each.status:
			t.Errorf("%v: exit %d", each.sig, status.ExitStatus())
		}
	}
}
