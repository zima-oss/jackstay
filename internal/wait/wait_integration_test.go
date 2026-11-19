//go:build integration

package wait

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/procs"
)

// brokenTest ends a test that hangs; no outcome depends on it.
const brokenTest = 2 * time.Minute

func startShell(t *testing.T, script string, env ...string) *exec.Cmd {
	t.Helper()
	child := exec.Command("sh", "-c", script)
	child.Env = append(os.Environ(), env...)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	return child
}

func until(t *testing.T, what string, holds func() bool) {
	t.Helper()
	give := time.Now().Add(brokenTest)
	for !holds() {
		if time.Now().After(give) {
			t.Fatalf("%s never came", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAZombieCountsAsEnded(t *testing.T) {
	child := startShell(t, "exit 0")
	defer func() { _ = child.Wait() }()
	pid := child.Process.Pid
	until(t, "the zombie", func() bool {
		row, found, err := procs.Row(pid)
		return err == nil && found && row.Zombie
	})
	if (realWorld{}).Process(pid).running {
		t.Fatal("a zombie runs")
	}
	var stdout, stderr bytes.Buffer
	if code := command([]string{"--pid", strconv.Itoa(pid), "--timeout", "0"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("%d %q %q", code, stdout.String(), stderr.String())
	}
}

func TestARunningProcessRuns(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	child := startShell(t, `echo ready > "$READY"; exec sleep 600`, "READY="+ready)
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	until(t, "ready", func() bool { _, err := os.Stat(ready); return err == nil })
	first := (realWorld{}).Process(child.Process.Pid)
	second := (realWorld{}).Process(child.Process.Pid)
	if !first.running || first.start == 0 || first != second {
		t.Fatalf("a running child has no stable identity: %+v then %+v", first, second)
	}
	if !(realWorld{}).Process(1).running {
		t.Fatal("launchd does not run")
	}
	if (realWorld{}).Process(99_999_999).running {
		t.Fatal("no process runs")
	}
}

func TestAWriterWhoseLastLineIsNotEnded(t *testing.T) {
	folder := t.TempDir()
	log := filepath.Join(folder, "out.log")
	release := filepath.Join(folder, "release")
	child := startShell(t, `printf half > "$LOG"; while [ ! -f "$RELEASE" ]; do sleep 0.02; done; printf ' all done' >> "$LOG"`,
		"LOG="+log, "RELEASE="+release)
	reaped := make(chan struct{})
	go func() { _ = child.Wait(); close(reaped) }()
	until(t, "the first words", func() bool { data, _ := os.ReadFile(log); return len(data) > 0 })
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := command([]string{"--line", "all done", "--in", log, "--while-pid", strconv.Itoa(child.Process.Pid),
		"--timeout", strconv.Itoa(int(brokenTest.Seconds()))}, nil, &stdout, &stderr)
	<-reaped
	if code != 0 || !strings.HasPrefix(stdout.String(), "half all done\n") {
		t.Fatalf("%d %q %q", code, stdout.String(), stderr.String())
	}
}
