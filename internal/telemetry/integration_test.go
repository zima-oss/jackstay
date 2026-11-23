//go:build integration

package telemetry_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/telemetry/telemetrytest"
)

// binary is jackstay built once for these tests.
var binary string

func TestMain(m *testing.M) {
	folder, err := os.MkdirTemp("", "jackstay-telemetry-")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(folder, "jst")
	root, _ := filepath.Abs("../..")
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/jst")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		panic(string(output))
	}
	status := m.Run()
	_ = os.RemoveAll(folder)
	os.Exit(status)
}

// project is a folder with a jackstay.json, a jackstay home and the environment a process run there gets.
func project(t *testing.T) (folder, home string, env []string) {
	t.Helper()
	folder, home = t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(folder, "jackstay.json"), []byte(`{"jackstay_schema": 2, "project": "demo"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, pair := range os.Environ() {
		if !strings.HasPrefix(pair, "TRACEPARENT=") && !strings.HasPrefix(pair, "JACKSTAY_TELEMETRY=") &&
			!strings.HasPrefix(pair, "CLAUDE_CODE_SESSION_ID=") {
			env = append(env, pair)
		}
	}
	return folder, home, append(env, "JACKSTAY_HOME="+home, "CLAUDE_CODE_SESSION_ID=integration-session",
		telemetry.FinishWaitVariable+"="+telemetrytest.GenerousFinishWait)
}

// A jackstay command run by `jst test --` is one trace: the command, the run, the child, the inner jackstay.
func TestOneTraceAcrossProcesses(t *testing.T) {
	folder, home, env := project(t)
	command := exec.Command(binary, "test", "--", binary, "version")
	command.Dir, command.Env = folder, env
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	spans := telemetrytest.Spans(t, home)
	names := []string{}
	for _, span := range spans {
		names = append(names, span.Name)
		if span.TraceID != spans[0].TraceID {
			t.Fatalf("two traces: %+v", spans)
		}
		if span.Text(telemetry.AttrClaudeSession) != "integration-session" {
			t.Fatalf("%s has no session", span.Name)
		}
	}
	if strings.Join(names, ",") != "jst test,test.run,exec jst,jst version" {
		t.Fatalf("spans %v", names)
	}
	if spans[3].ParentID != spans[2].SpanID || spans[2].ParentID != spans[1].SpanID {
		t.Fatal("not one tree")
	}
}

// A stopped `jst test` ends its spans as stopped, and still dies by the signal.
func TestStoppedBySignal(t *testing.T) {
	folder, home, env := project(t)
	// A child that never ends by itself: only the signal ends the run.
	command := exec.Command(binary, "test", "--", "sleep", "3600")
	command.Dir, command.Env = folder, env
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill() }()
	// The child running means jackstay listens for the stop and has begun the run.
	deadline := time.Now().Add(60 * time.Second)
	for exec.Command("pgrep", "-P", strconv.Itoa(command.Process.Pid), "-x", "sleep").Run() != nil {
		if time.Now().After(deadline) {
			t.Fatal("the child sleep did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = command.Process.Signal(syscall.SIGTERM)
	err := command.Wait()
	status, _ := err.(*exec.ExitError)
	if status == nil || status.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
		t.Fatalf("jackstay did not die by SIGTERM: %v", err)
	}
	spans := telemetrytest.Spans(t, home)
	root := telemetrytest.Named(spans, "jst test")
	if len(root) != 1 || root[0].Text(telemetry.AttrErrorType) != "signal_SIGTERM" || root[0].Text(telemetry.AttrOutcome) != "stopped" ||
		root[0].StatusCode != journal.StatusError {
		t.Fatalf("command span %+v", root)
	}
}

// The guard given an empty standard input names its caller.
func TestGuardNamesItsCaller(t *testing.T) {
	folder, home, env := project(t)
	command := exec.Command("sh", "-c", binary+" guard </dev/null; echo caller-marker >/dev/null")
	command.Dir, command.Env = folder, env
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
	spans := telemetrytest.Spans(t, home)
	guardSpans := telemetrytest.Named(spans, "jst guard")
	if len(guardSpans) != 1 || guardSpans[0].Text(telemetry.AttrGuardOutcome) != "failed_open" {
		t.Fatalf("%+v", spans)
	}
	caller := strings.Join(guardSpans[0].Texts(telemetry.AttrCallerArgv), " ")
	if !strings.Contains(caller, "sh -c") || !strings.Contains(caller, "guard </dev/null") {
		t.Fatalf("caller %q", caller)
	}
}
