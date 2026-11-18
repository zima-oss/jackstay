//go:build integration

package scopes

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/procs"
)

// guard only ends a broken test; no outcome is decided by it (W-013).
const guard = 120 * time.Second

// scopeTimeout is the timeout of the scope that hangs: the gated clock fires it when the test says.
const scopeTimeout = 7777 * time.Second

// gatedClock is the machine's clock, except that the scope's timeout fires when the test closes
// fire: the timeout is an event of the test (the command is ready), not a race with the machine.
type gatedClock struct {
	fire chan time.Time
}

func (clock gatedClock) Now() time.Time            { return time.Now() }
func (clock gatedClock) Sleep(pause time.Duration) { time.Sleep(pause) }
func (clock gatedClock) After(pause time.Duration) <-chan time.Time {
	if pause == scopeTimeout {
		return clock.fire
	}
	return time.After(pause)
}

// realWorld is a world whose commands really run under supervise, in sessions of their own.
func realWorld(t *testing.T, test map[string]any) (*world, gatedClock) {
	w := newWorld(t, test, nil)
	clock := gatedClock{fire: make(chan time.Time, 1)}
	w.launcher, w.machineClock = superviseLaunch, clock
	return w, clock
}

// waitFor waits for a file to appear (the command's acknowledgement), the guard ending a broken test.
func waitFor(t *testing.T, place string) string {
	t.Helper()
	deadline := time.Now().Add(guard)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(place); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("waited %s for %s", guard, place)
	return ""
}

// gone says a process no longer runs (it may linger as a zombie no one reaps yet).
func gone(pid int) bool {
	row, found, err := procs.Row(pid)
	if err != nil {
		return unix.Kill(pid, 0) == unix.ESRCH
	}
	return !found || row.Zombie
}

func TestIntegrationAScopePassesWithTheStepsJudge(t *testing.T) {
	w, _ := realWorld(t, map[string]any{"timeout": 600, "scopes": map[string]any{
		"changed": map[string]any{"run": argv("sh", "-c", `echo "ran 3 tests"; echo "$JACKSTAY_RUN_ID" > "$SIDE/id"`), "judge": "steps",
			"evidence": "ran ([0-9]+) tests"}}})
	w.expectStatus(w.jst("changed"), 0)
	w.expectLast("outcome=passed", "counted=3")
	run := w.lastRecord("run")
	if run.Text("run_id") != w.read("id") || run.Text("outcome") != "passed" {
		t.Fatalf("the record: %v", run)
	}
	if output := run.Text("output"); output == "" {
		t.Fatal("the output was not kept")
	} else if data, err := os.ReadFile(output); err != nil || !strings.Contains(string(data), "ran 3 tests") {
		t.Fatalf("the kept output: %q %v", data, err)
	}
}

// A veto at the end of a line longer than 1 MiB fails the scope: the line is not cut off.
func TestIntegrationAVetoBeyondOneMiBFails(t *testing.T) {
	w, _ := realWorld(t, map[string]any{"timeout": 600, "scopes": map[string]any{
		"changed": map[string]any{"run": argv("sh", "-c", `echo "ran 3 tests"; head -c 1048600 /dev/zero | tr '\0' x; echo FATAL`),
			"judge": "steps", "evidence": "ran ([0-9]+) tests", "veto": "FATAL"}}})
	w.expectStatus(w.jst("changed"), 1)
	w.expectLast("outcome=failed", "FATAL")
}

func TestIntegrationATimeoutEndsTheWholeSessionTree(t *testing.T) {
	// The child ignores TERM, so only the KILL after the grace ends it; the shell waits for it.
	w, clock := realWorld(t, map[string]any{"timeout": scopeTimeout.Seconds(), "judge": "exit", "scopes": map[string]any{
		"full": map[string]any{"run": argv("sh", "-c",
			`(trap "" TERM; echo $(exec sh -c 'echo $PPID') > "$SIDE/child.tmp"; mv "$SIDE/child.tmp" "$SIDE/child"; exec sleep 100000) & `+
				`echo $$ > "$SIDE/shell"; wait`)}}})
	statusOf := make(chan int, 1)
	go func() { statusOf <- w.jst("full") }()
	child, err := strconv.Atoi(waitFor(t, w.at("child")))
	if err != nil {
		t.Fatal(err)
	}
	shell, err := strconv.Atoi(waitFor(t, w.at("shell")))
	if err != nil {
		t.Fatal(err)
	}
	clock.fire <- time.Now()
	select {
	case status := <-statusOf:
		if status != StatusTimedOut {
			t.Fatalf("status %d; stderr:\n%s", status, w.stderr.String())
		}
	case <-time.After(guard):
		t.Fatal("jst test did not end")
	}
	w.expectLast("outcome=timed-out (exit 124")
	for _, pid := range []int{child, shell} {
		if !gone(pid) {
			_ = unix.Kill(pid, unix.SIGKILL)
			t.Fatalf("%d still runs after the timeout's stop", pid)
		}
	}
	run := w.lastRecord("run")
	killed := run.Text("killed")
	if run["timed_out"] != true || !strings.Contains(killed, "["+strconv.Itoa(shell)+"]") {
		t.Fatalf("the record: %v", run)
	}
	for _, match := range regexp.MustCompile(`\[(\d+)\]`).FindAllStringSubmatch(killed, -1) {
		if pid, _ := strconv.Atoi(match[1]); !gone(pid) {
			t.Fatalf("%d was named as killed and runs", pid)
		}
	}
}

func TestIntegrationAGateFailsFastAndItsBarRefusesResume(t *testing.T) {
	w, _ := realWorld(t, gateSection())
	w.repo.Git("branch", "base-ref")
	w.flag("middle-fails", true)
	w.expectStatus(w.jst("gate"), 1)
	w.expectLast("gate gate failed at middle")
	w.expectCounters("cheap middle full", 1, 1, 0)
	w.flag("middle-fails", false)
	w.expectStatus(w.jst("gate", "--resume"), 2)
	w.expectSaid("nothing has changed since this step failed")
	w.expectLast("outcome=refused", "middle=refused")
	w.expectCounters("cheap middle full", 1, 1, 0)
	// A change of the tree lifts the bar; then a pass is resumed.
	w.repo.Write(map[string]string{"Sources/b.swift": "let b = 2\n"})
	w.commit()
	w.expectStatus(w.jst("gate", "--resume"), 0)
	w.expectCounters("cheap middle full", 2, 2, 1)
	w.expectStatus(w.jst("gate", "--resume"), 0)
	w.expectLast("outcome=resumed")
	w.expectCounters("cheap middle full", 2, 2, 1)
}

func TestIntegrationALeftoverAttributedByTheRunID(t *testing.T) {
	w, _ := realWorld(t, nil)
	w.configure(map[string]any{"judge": "swift-test", "timeout": 600,
		"scopes": map[string]any{"full": map[string]any{"run": argv("sh", "-c",
			`touch "$TMPDIR/left-$JACKSTAY_RUN_ID.plist"; cat "$SIDE/pkg-filtered-xctest.log"`)}},
		"leftovers": []any{map[string]any{"glob": "$TMPDIR/left-{run_id}.plist", "fail": true}}}, nil)
	w.expectStatus(w.jst("full"), 1)
	w.expectLast("outcome=failed", "left behind")
	run := w.lastRecord("run")
	wanted := filepath.Join(w.tmp, "left-"+run.Text("run_id")+".plist")
	leftovers, _ := run["leftovers"].(map[string]any)
	if failed, _ := leftovers["failed"].([]any); len(failed) != 1 || failed[0] != wanted {
		t.Fatalf("the record's leftovers: %v, want %s", run["leftovers"], wanted)
	}
	if _, err := os.Stat(wanted); err != nil {
		t.Fatal("jackstay deleted the leftover")
	}
}
