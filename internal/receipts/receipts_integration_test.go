//go:build integration

package receipts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// The test binary is `jst` when asJst is set, and a process that leaves the command's group (a
// session of its own) and holds its output open when holdVariable names a file for its pid.
const (
	asJst        = "JACKSTAY_RECEIPTS_TEST_AS_JST"
	holdVariable = "JACKSTAY_RECEIPTS_TEST_HOLD"
)

// outerTimeout only ends a broken test; no outcome is decided by it (W-013).
const outerTimeout = 60 * time.Second

func TestMain(m *testing.M) {
	if os.Getenv(asJst) == "1" {
		os.Exit(cli.Main("jst", os.Args[1:]))
	}
	if place := os.Getenv(holdVariable); place != "" {
		_, _ = unix.Setsid()
		_ = os.WriteFile(place, []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const shJSON = `{"jackstay_schema": 2, "project": "demo", "receipts": {"inputs": ["src"], "env": [], "toolchain": [],
  "commands": [{"argv_prefix": ["sh"], "reuse": false, "evidence": "Executed ([0-9]+) tests?", "veto": "FAILED"}]}}`

// realWorld is a world whose command really runs, under supervise.
func realWorld(t *testing.T) *world {
	return newWorld(t, shJSON)
}

func (w *world) realTest(extraEnv []string, readBound time.Duration, args ...string) int {
	w.t.Helper()
	asked, ok := parseRaw(args)
	if !ok {
		w.t.Fatalf("cannot parse %q", args)
	}
	runner := w.tester()
	runner.run = supervise.Run
	runner.clock = supervise.RealClock()
	runner.environ = append(runner.environ, extraEnv...)
	runner.readBound = readBound
	return runner.raw(asked)
}

func TestRealAPassWithEvidence(t *testing.T) {
	w := realWorld(t)
	if status := w.realTest(nil, 0, "--", "sh", "-c", "echo 'Executed 3 tests'; echo 'Executed 2 tests' >&2"); status != 0 {
		t.Fatalf("status %d\n%s", status, w.stderr.String())
	}
	run := w.lastRecord(EventRun)
	if run.Text("outcome") != OutcomePassed || run["evidence_count"] != 5.0 || run["stored"] != true || len(w.receipts()) != 1 {
		t.Fatalf("run %v", run)
	}
	data, err := os.ReadFile(run.Text("output"))
	if err != nil || !strings.Contains(string(data), "Executed 3 tests") || !strings.Contains(string(data), "Executed 2 tests") {
		t.Fatalf("kept output %q %v", data, err)
	}
	if !strings.Contains(w.stdout.String(), "Executed 3 tests") || !strings.Contains(w.stderr.String(), "Executed 2 tests") {
		t.Fatal("the streams were not passed on")
	}
}

func TestRealAVeto(t *testing.T) {
	w := realWorld(t)
	if status := w.realTest(nil, 0, "--", "sh", "-c", "echo 'the suite FAILED'; echo 'Executed 1 test'"); status != StatusVeto {
		t.Fatalf("status %d\n%s", status, w.stderr.String())
	}
	if run := w.lastRecord(EventRun); run.Text("veto_line") != "the suite FAILED" || len(w.receipts()) != 0 {
		t.Fatalf("run %v", run)
	}
}

// A veto at the end of a line longer than 1 MiB is still seen; a long line without one leaves the
// run unjudged, since a pattern across the pieces it was read in is not seen.
func TestRealALineBeyondOneMiB(t *testing.T) {
	long := `head -c 1048600 /dev/zero | tr '\0' x`
	cases := []struct {
		name, script string
		status       int
		outcome      string
	}{
		{"a veto beyond the bound fails", "echo 'Executed 3 tests'; " + long + "; echo FAILED", StatusVeto, OutcomeFailed},
		{"no veto: unjudged", "echo 'Executed 3 tests'; " + long + "; echo", StatusUnjudged, OutcomeUnjudged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := realWorld(t)
			if status := w.realTest(nil, 0, "--", "sh", "-c", c.script); status != c.status {
				t.Fatalf("status %d, want %d\n%s", status, c.status, w.lastLine())
			}
			if run := w.lastRecord(EventRun); run.Text("outcome") != c.outcome || len(w.receipts()) != 0 {
				t.Fatalf("outcome %s, receipts %v", run.Text("outcome"), w.receipts())
			}
		})
	}
}

func TestRealNothingRun(t *testing.T) {
	w := realWorld(t)
	if status := w.realTest(nil, 0, "--", "sh", "-c", "echo 'Executed 0 tests'"); status != StatusNothingRun {
		t.Fatalf("status %d\n%s", status, w.stderr.String())
	}
}

func TestRealAFailureKeepsItsStatus(t *testing.T) {
	w := realWorld(t)
	if status := w.realTest(nil, 0, "--", "sh", "-c", "echo 'x: error: broken' >&2; exit 76"); status != 76 {
		t.Fatalf("status %d", status)
	}
	if !strings.HasPrefix(w.lastLine(), "jst test: outcome=failed (exit 76, ") ||
		!strings.Contains(w.stderr.String(), "| x: error: broken") {
		t.Fatalf("said %s", w.stderr.String())
	}
}

func TestRealUnjudgedWhileALeftoverHoldsTheOutput(t *testing.T) {
	w := realWorld(t)
	pidFile := filepath.Join(t.TempDir(), "holder.pid")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	script := "'" + self + "' & while [ ! -s '" + pidFile + "' ]; do sleep 0.01; done; echo 'Executed 1 test'"
	status := w.realTest([]string{holdVariable + "=" + pidFile}, time.Second, "--", "sh", "-c", script)
	if status != StatusUnjudged {
		t.Fatalf("status %d\n%s", status, w.stderr.String())
	}
	run := w.lastRecord(EventRun)
	if run.Text("outcome") != OutcomeUnjudged || !strings.Contains(run.Text("why"), "not read to its end") || len(w.receipts()) != 0 {
		t.Fatalf("run %v", run)
	}
}

func TestRealALeftoverInItsGroupIsStopped(t *testing.T) {
	w := realWorld(t)
	if status := w.realTest(nil, 0, "--", "sh", "-c", "sleep 600 & echo 'Executed 1 test'"); status != 0 {
		t.Fatalf("status %d\n%s", status, w.stderr.String())
	}
	run := w.lastRecord(EventRun)
	if run.Text("outcome") != OutcomePassed || !strings.Contains(run.Text("left_running"), "sleep") {
		t.Fatalf("run %v", run)
	}
}

func TestRealAStopBySignalIsRecordedStopped(t *testing.T) {
	w := realWorld(t)
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(os.Args[0], "test", "--", "sh", "-c", "echo up > '"+ready+"'; exec sleep 600")
	command.Dir = w.repo.Root
	command.Env = append(os.Environ(), asJst+"=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(outerTimeout)
	for {
		if data, err := os.ReadFile(ready); err == nil && len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			t.Fatal("the command never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := command.Wait()
	status, _ := command.ProcessState.Sys().(syscall.WaitStatus)
	if err == nil || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("jst test did not end by SIGTERM: %v", err)
	}
	run := w.lastRecord(EventRun)
	if run.Text("outcome") != OutcomeStopped || run.Text("signal") != "SIGTERM" || run["status"] != 143.0 {
		t.Fatalf("run %v", run)
	}
	if len(w.notes()) != 1 {
		t.Fatalf("notes %v", w.notes())
	}
}
