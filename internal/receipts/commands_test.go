package receipts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheKeyCommandRunsNothing(t *testing.T) {
	w := newWorld(t, configFile)
	var out, errs strings.Builder
	runner := w.tester()
	runner.stdout, runner.stderr = &out, &errs
	if status := runner.showKey([]string{"make", "test"}); status != 0 {
		t.Fatalf("%d %s", status, errs.String())
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out.String()), &report); err != nil {
		t.Fatal(err)
	}
	if report["key"] == "" || report["reuse_allowed"] != false || report["reuse_refused_because"] != "the command is not opted in (reuse is not true)" ||
		report["no_receipt_because"] != "no receipt" || w.fake.calls != 0 {
		t.Fatalf("%v", report)
	}
	material, _ := report["material"].(map[string]any)
	if material["schema"] != 3.0 {
		t.Fatalf("material %v", material)
	}
	broken := newWorld(t, `{"jackstay_schema": 3, "receipts": {"inputs": ["nowhere"]}}`)
	runner = broken.tester()
	runner.stdout, runner.stderr = &out, &errs
	if status := runner.showKey([]string{"make"}); status != 1 || !strings.Contains(errs.String(), "jst receipts: no key:") {
		t.Fatalf("%d %s", status, errs.String())
	}
	if keyCommand([]string{"make"}, nil, &out, &errs) != StatusUnusable {
		t.Fatal("usage")
	}
}

func TestTheListCommand(t *testing.T) {
	w := newWorld(t, configFile)
	w.fake = passing("3")
	w.test("--", "make", "test")
	var out, errs strings.Builder
	if status := listCommand(nil, nil, &out, &errs); status != 0 {
		t.Fatal(status)
	}
	if !strings.HasPrefix(out.String(), "demo  ") || !strings.Contains(out.String(), "  make test  ") {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	listCommand([]string{"--project", "demo"}, nil, &out, &errs)
	if strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("%q", out.String())
	}
	if listCommand([]string{"--json"}, nil, &out, &errs) != StatusUnusable {
		t.Fatal("list takes no --json")
	}
}

func TestTestLast(t *testing.T) {
	w := newWorld(t, configFile)
	var out, errs strings.Builder
	if status := LastCommand(nil, nil, &out, &errs); status != 1 || !strings.Contains(errs.String(), "no run of demo is in the record") {
		t.Fatalf("%d %s", status, errs.String())
	}
	w.fake = passing("7")
	w.test("--", "make", "test")
	out.Reset()
	if status := LastCommand([]string{"--json"}, nil, &out, &errs); status != 0 {
		t.Fatal(status)
	}
	var shown map[string]any
	if err := json.Unmarshal([]byte(out.String()), &shown); err != nil {
		t.Fatal(err)
	}
	if shown["output"] == nil || len(shown["notable"].([]any)) != 0 {
		t.Fatalf("%v", shown)
	}
	out.Reset()
	LastCommand([]string{"--project", "demo"}, nil, &out, &errs)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.HasPrefix(lines[0], "demo: ") || !strings.HasSuffix(lines[0], "  make test") ||
		lines[len(lines)-1] != "outcome=passed (0 s, tests counted: 7)" || strings.Contains(out.String(), "went wrong") {
		t.Fatalf("%s", out.String())
	}
	// Its veto is this checkout's, for its own project only.
	w.fake = &fakeRun{stdout: []string{"the build FAILED quietly", "Executed 2 tests"}}
	w.test("--", "make", "test")
	out.Reset()
	LastCommand(nil, nil, &out, &errs)
	if !strings.Contains(out.String(), "veto line: the build FAILED quietly") ||
		!strings.Contains(out.String(), "| the build FAILED quietly") {
		t.Fatalf("%s", out.String())
	}
	if LastCommand([]string{"--frob"}, nil, &out, &errs) != StatusUnusable {
		t.Fatal("usage")
	}
}

func TestTestLastShowsAGateAndAScope(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	logPath, _ := RunLogPath()
	data, err := os.ReadFile(fixtures[0])
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	if err := os.WriteFile(logPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errs strings.Builder
	if status := LastCommand([]string{"--project", "alpha", "--scope", "gate"}, nil, &out, &errs); status != 0 {
		t.Fatal(errs.String())
	}
	if out.String() != "alpha: 2026-11-15T09:14:00+0200  gate gate\n  unit: passed (60 s)  run r6-106\noutcome=passed (180 s)\n" {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	LastCommand([]string{"--project", "alpha", "--scope", "unit"}, nil, &out, &errs)
	if !strings.Contains(out.String(), "scope: scope=unit step=unit gate_run_id=g1 judge=swift-test counted=40") ||
		!strings.Contains(out.String(), "the output was not kept") {
		t.Fatalf("%s", out.String())
	}
	out.Reset()
	// Without --scope the latest `run` line by its end, the stopped and gave-up ones skipped.
	LastCommand([]string{"--project", "alpha"}, nil, &out, &errs)
	if !strings.HasSuffix(strings.TrimSpace(out.String()), "outcome=exited-0 (60 s, tests counted: 40)") {
		t.Fatalf("%s", out.String())
	}
}
