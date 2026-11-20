package scopes

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/proctree"
	"github.com/zima-oss/jackstay/internal/receipts"
	"github.com/zima-oss/jackstay/internal/supervise"
)

func scopesSection() map[string]any {
	return map[string]any{
		"judge": "swift-test", "timeout": 600,
		"scopes": map[string]any{
			"focused": map[string]any{"run": argv("sh", "-c", `echo "filter=$1" > "$SIDE/filter"; cat "$SIDE/pkg-filtered-xctest.log"`, "sh", "{tests}")},
			"changed": map[string]any{"run": argv("sh", "-c", `echo "base=$1" > "$SIDE/base"; echo "ran 4 tests"`, "sh", "{base}"),
				"base": "origin/main", "judge": "steps", "evidence": "ran ([0-9]+) tests"},
			"full": map[string]any{"run": argv("sh", "-c", `env > "$SIDE/env"; pwd > "$SIDE/pwd"; cat "$SIDE/pkg-filtered-xctest.log"`),
				"env": map[string]any{"DEMO_MODE": "strict", "EMPTY": ""}, "when": "after merges on main", "cwd": "Sources"},
			"display": map[string]any{"run": argv("sh", "-c", `echo ran >> "$SIDE/display"; cat "$SIDE/pkg-filtered-xctest.log"`),
				"requires": []any{map[string]any{"run": argv("sh", "-c", "exit 3"), "why": "needs a screen"}}},
			"x-st":       map[string]any{"run": cat("pkg-swift-testing-only"), "expect": argv("swift-testing")},
			"x-st-wrong": map[string]any{"run": cat("pkg-swift-testing-only")},
			"x-mixed":    map[string]any{"run": cat("pkg-mixed-failed")},
			"x-exit":     map[string]any{"run": argv("true"), "judge": "exit"},
			"x-none":     map[string]any{"run": argv("true")},
			"x-veto": map[string]any{"run": argv("sh", "-c", `cat "$SIDE/pkg-filtered-xctest.log"; echo "RECOGNIZER-INVARIANT broken"`),
				"veto": "RECOGNIZER-INVARIANT"},
			"x-check-ok": map[string]any{"run": argv("true"), "judge": "exit",
				"requires": []any{map[string]any{"run": argv("true"), "why": "a check that passes"}}},
			"x-hang-check": map[string]any{"run": argv("true"), "judge": "exit",
				"requires": []any{map[string]any{"run": argv("hang-check"), "why": "a check that hangs", "timeout": 1}}},
			"x-shell": map[string]any{"run": argv("sh", "-c", `echo "${HOME:+home} {2}" > "$SIDE/literal"; cat "$SIDE/pkg-filtered-xctest.log"`)},
		},
	}
}

func TestAScopeRunsAndIsJudgedAndRecorded(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("full"), 0)
	w.expectLast("outcome=passed", "counted=2 bundles=2/2")
	run := w.lastRecord("run")
	if run.Text("scope") != "full" || run.Text("judge") != "swift-test" || run.Text("bundles") != "2/2" || run.Text("outcome") != "passed" {
		t.Fatalf("the record: %v", run)
	}
	if counted, _ := run.Number("counted"); counted != 2 {
		t.Fatalf("counted: %v", run["counted"])
	}
	env, _ := run["env"].(map[string]any)
	if env["DEMO_MODE"] != "strict" || env["EMPTY"] != "" {
		t.Fatalf("env: %v", run["env"])
	}
	seen := map[string]string{}
	for _, line := range strings.Split(w.read("env"), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			seen[name] = value
		}
	}
	if seen["DEMO_MODE"] != "strict" || seen[RunIDVariable] != run.Text("run_id") || seen[supervise.RunIDVariable] != run.Text("run_id") {
		t.Fatalf("the command's environment: %v", seen)
	}
	if !strings.HasPrefix(seen[RunTmpVariable], w.tmp+"/") {
		t.Fatalf("JACKSTAY_RUN_TMP %q is not under TMPDIR %s", seen[RunTmpVariable], w.tmp)
	}
	if _, err := os.Stat(seen[RunTmpVariable]); !os.IsNotExist(err) {
		t.Fatal("the run's own folder is removed when it holds no leftovers")
	}
	if pwd, _ := filepath.EvalSymlinks(w.read("pwd")); pwd != filepath.Join(w.repo.Root, "Sources") {
		t.Fatalf("ran in %s", pwd)
	}
	if start := w.lastRecord("start"); start.Text("run_id") != run.Text("run_id") || start.Text("scope") != "full" {
		t.Fatalf("the start line: %v", start)
	}
	if _, present := run["lock_waited_seconds"]; !present {
		t.Fatal("no lock_waited_seconds")
	}
	var out, errs bytes.Buffer
	if status := receipts.LastCommand([]string{"--scope", "full"}, nil, &out, &errs); status != 0 {
		t.Fatalf("jst test last: %d %s", status, errs.String())
	}
	if !strings.Contains(out.String(), "outcome=passed") || !strings.Contains(out.String(), "scope=full") {
		t.Fatalf("jst test last said:\n%s", out.String())
	}
}

func TestAnUndeclaredScopeIsRefusedWithTheDeclaredOnes(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("quick"), 2)
	w.expectSaid("is not a scope of demo")
	w.expectSaid("focused")
	w.expectLast("outcome=not-started")
	if len(w.records("")) != 0 {
		t.Fatal("a refused scope wrote to the record")
	}
	w.expectStatus(w.jst(), 2)
	w.expectSaid("which scope?")
	w.expectStatus(w.jst("full", "--frobnicate"), 2)
	w.expectSaid("unknown option --frobnicate")
	w.expectStatus(w.jst("full", "--wait", "soon"), 2)
	w.expectStatus(w.jst("full", "--resume"), 2)
	w.expectSaid("--resume and --keep-going are for a scope with deps")
}

func TestFocusedNeedsNamesAndOthersTakeNone(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("focused"), 2)
	w.expectSaid("needs the tests to run")
	w.expectStatus(w.jst("full", "Some.Test"), 2)
	w.expectSaid("takes no names")
	w.expectStatus(w.jst("focused", "Target.Class", "Other"), 0)
	if got := w.read("filter"); got != "filter="+testsPattern([]string{"Target.Class", "Other"}) {
		t.Fatalf("the filter: %s", got)
	}
}

func TestBaseFromTheScopeAndFromTheCommandLine(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("changed"), 0)
	if got := w.read("base"); got != "base=origin/main" {
		t.Fatalf("base: %s", got)
	}
	w.expectStatus(w.jst("changed", "--base", "HEAD~0"), 0)
	w.expectLast("outcome=passed", "counted=4")
	if got := w.read("base"); got != "base=HEAD~0" {
		t.Fatalf("base: %s", got)
	}
}

func TestBracesThatAreNoPlaceholderStay(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("x-shell"), 0)
	if got := w.read("literal"); got != "home {2}" {
		t.Fatalf("the shell saw %q", got)
	}
}

func TestAFailingRequirementRunsNothing(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("display"), 2)
	w.expectLast("outcome=not-started", "needs a screen", "the check ended 3")
	if w.counter("display") != 0 {
		t.Fatal("the scope ran")
	}
	run := w.lastRecord("run")
	if run.Text("outcome") != "not-started" || len(w.records("start")) != 0 {
		t.Fatalf("the record: %v", w.records(""))
	}
}

func TestARequirementThatHangsTimesOutAndIsStopped(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.overrides["hang-check"] = func(order launch) *launched {
		if order.timeout != time.Second {
			t.Errorf("the check's timeout: %v", order.timeout)
		}
		if order.spec.Mode != supervise.Session || order.spec.Grace != 10*time.Second {
			t.Errorf("the check is not run as a session stopped with 10 s of grace: %+v", order.spec)
		}
		return &launched{outcome: supervise.Outcome{PID: 31, Signal: syscall.SIGTERM}, timedOut: true, killed: "sh[31],sleep[32]"}
	}
	w.expectStatus(w.jst("x-hang-check"), 2)
	w.expectLast("outcome=not-started", "timed out after 1 s")
	run := w.lastRecord("run")
	if session, _ := run.Number("requires_session"); session != 31 || run.Text("requires_killed") != "sh[31],sleep[32]" {
		t.Fatalf("the record: %v", run)
	}
	// One that ends 0 when its timeout's stop asks it to is no pass either.
	w.overrides["hang-check"] = func(launch) *launched {
		return &launched{outcome: supervise.Outcome{PID: 31}, timedOut: true, killed: "sh[31]"}
	}
	w.expectStatus(w.jst("x-hang-check"), 2)
	w.expectLast("timed out")
	// One that ends and leaves what cannot be stopped does not pass.
	w.overrides["hang-check"] = func(launch) *launched {
		return &launched{outcome: supervise.Outcome{PID: 31, Survivors: []proctree.Survivor{{Member: proctree.Member{PID: 40}, Name: "stuck"}}}}
	}
	w.expectStatus(w.jst("x-hang-check"), 2)
	w.expectLast("could not be stopped: stuck[40]")
	// A check that timed out and whose stop is not confirmed says what still runs.
	w.overrides["hang-check"] = func(launch) *launched {
		return &launched{outcome: supervise.Outcome{PID: 31, Survivors: []proctree.Survivor{{Member: proctree.Member{PID: 40}, Name: "stuck"}}},
			timedOut: true}
	}
	w.expectStatus(w.jst("x-hang-check"), 2)
	w.expectLast("its stop is not confirmed, still running: stuck[40]")
}

func TestARequirementWhoseEndCannotBeConfirmedIsNoPass(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("x-check-ok"), 0)
	w.unreadable = true
	w.expectStatus(w.jst("x-check-ok"), 2)
	w.expectLast("cannot be confirmed")
}

func TestSwiftTestingOnlyPassesWhenExpected(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("x-st"), 0)
	w.expectLast("outcome=passed", "swift-testing=3")
	w.expectStatus(w.jst("x-st-wrong"), 77)
	w.expectLast("outcome=unjudged")
}

func TestAFailureLineFailsARunThatEnded0(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("x-mixed"), 1)
	w.expectLast("outcome=failed", "BetaTests testFails")
	w.expectSaid("what the output says went wrong")
}

func TestExitJudgeIsNeverPassed(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("x-exit"), 0)
	w.expectLast("outcome=exited-0")
	if run := w.lastRecord("run"); run["stored"] != false {
		t.Fatalf("an exited-0 run stored a receipt: %v", run)
	}
}

func TestNothingCountedIsNothingRun(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("x-none"), 77)
	w.expectLast("outcome=unjudged") // an expected engine without markers
	w.configure(map[string]any{"timeout": 60, "scopes": map[string]any{
		"changed": map[string]any{"run": argv("echo", "ran 0 tests"), "judge": "steps", "evidence": "ran ([0-9]+) tests"}}}, nil)
	w.expectStatus(w.jst("changed"), 76)
	w.expectLast("outcome=nothing-run (exit 76")
}

func TestNegativeEvidenceCannotCreateAScopePass(t *testing.T) {
	w := newWorld(t, map[string]any{"scopes": map[string]any{
		"full": map[string]any{"run": argv("sh", "-c", "echo 'ran 4 tests'; echo 'ran -1 tests'"),
			"judge": "steps", "evidence": `ran (-?[0-9]+) tests`},
	}}, nil)
	w.expectStatus(w.jst("full"), receipts.StatusUnjudged)
	w.expectLast("outcome=unjudged", "counted=4")
	if run := w.lastRecord("run"); run.Text("outcome") != receipts.OutcomeUnjudged ||
		!strings.Contains(run.Text("why"), "not a number") || run["stored"] == true {
		t.Fatalf("negative evidence produced a pass or receipt: %v", run)
	}
}

func TestAVetoFails(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("x-veto"), 1)
	w.expectLast("RECOGNIZER-INVARIANT")
}

func TestACommandThatCannotStartFails(t *testing.T) {
	w := newWorld(t, map[string]any{"timeout": 60, "scopes": map[string]any{"full": map[string]any{"run": argv("/no/such/command")}}}, nil)
	w.expectStatus(w.jst("full"), 127)
	w.expectLast("outcome=failed (exit 127")
}

func TestListAndPlan(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	w.expectStatus(w.jst("list"), 0)
	for _, wanted := range []string{"when: after merges on main", "[timeout 600 s]", "not declared: quick, gate", "x-veto"} {
		if !strings.Contains(w.stdout.String(), wanted) {
			t.Fatalf("list does not say %q:\n%s", wanted, w.stdout.String())
		}
	}
	w.expectStatus(w.jst("plan", "changed", "--base", "main"), 0)
	for _, wanted := range []string{"changed: sh -c", "base=main", "judge=steps", "timeout=600"} {
		if !strings.Contains(w.stdout.String(), wanted) {
			t.Fatalf("plan does not say %q:\n%s", wanted, w.stdout.String())
		}
	}
	w.expectStatus(w.jst("plan", "display"), 0)
	if !strings.Contains(w.stdout.String(), "requires: sh -c exit 3  (needs a screen)") {
		t.Fatalf("plan:\n%s", w.stdout.String())
	}
	w.expectStatus(w.jst("plan", "focused"), 0)
	if !strings.Contains(w.stdout.String(), "focused: cannot run as asked") {
		t.Fatalf("plan:\n%s", w.stdout.String())
	}
	w.expectStatus(w.jst("plan", "nope"), 2)
	if len(w.records("")) != 0 {
		t.Fatal("list and plan wrote to the record")
	}
	w.configure(map[string]any{"scopes": map[string]any{"full": map[string]any{"run": argv("true")}}}, nil)
	w.expectStatus(w.jst("list"), 0)
	if !strings.Contains(w.stdout.String(), "[no timeout: runs unbounded]") {
		t.Fatalf("list:\n%s", w.stdout.String())
	}
}

func TestABrokenTestSectionStopsScopesAndNotTheRawForm(t *testing.T) {
	w := newWorld(t, map[string]any{"scopes": map[string]any{"fast": map[string]any{"run": argv("true")}}}, nil)
	w.expectStatus(w.jst("full"), 2)
	w.expectSaid("a scope is one of")
	w.expectLast("outcome=not-started")
	// `jst test --` reads only `lock` and `receipts`.
	if _, _, err := config.Load(w.repo.Root, receipts.Needs); err != nil {
		t.Fatal(err)
	}
	w.configure(map[string]any{"scopes": map[string]any{"full": map[string]any{"run": argv("true"), "bogus": 1}}}, nil)
	w.expectStatus(w.jst("full"), 2)
	w.expectSaid("unknown key")
	if _, _, err := config.Load(w.repo.Root, receipts.Needs); err != nil {
		t.Fatal(err)
	}
	w.configure(nil, nil)
	w.expectStatus(w.jst("full"), 2)
	w.expectSaid("declares no scopes")
}

func TestTheRegistrationHandsScopesOver(t *testing.T) {
	w := newWorld(t, scopesSection(), nil)
	var out, errs bytes.Buffer
	if status := receipts.TestCommand([]string{"nope"}, nil, &out, &errs); status != 2 || !strings.Contains(errs.String(), "is not a scope of demo") {
		t.Fatalf("jst test nope: %d %s", status, errs.String())
	}
	_ = w
}

func lockSection() map[string]any {
	return map[string]any{"judge": "exit", "timeout": 600, "scopes": map[string]any{
		"focused": map[string]any{"run": argv("sh", "-c", `echo "$JACKSTAY_LOCK_HELD" > "$SIDE/held"`, "sh", "{tests}")},
		"full": map[string]any{"run": argv("sh", "-c",
			`echo "$JACKSTAY_LOCK_HELD" > "$SIDE/held"; echo "${JACKSTAY_LOCK_SESSIONS_FILE:-none}" > "$SIDE/seen"`)},
		"x-own": map[string]any{"run": argv("sh", "-c", `echo "$JACKSTAY_LOCK_HELD" > "$SIDE/held"`), "lock": false}}}
}

func TestTheLockPerScope(t *testing.T) {
	w := newWorld(t, lockSection(), map[string]any{"lock": map[string]any{"enabled": true}})
	t.Setenv(flock.PathVariable, filepath.Join(t.TempDir(), "suite.lock"))
	w.expectStatus(w.jst("full"), 0)
	if w.read("held") == "" {
		t.Fatal("the scope ran without the lock")
	}
	if w.lastRecord("run")["locked"] != true {
		t.Fatalf("the record: %v", w.lastRecord("run"))
	}
	w.expectStatus(w.jst("focused", "A"), 0)
	if w.read("held") != "" {
		t.Fatal("focused took the lock")
	}
	w.expectStatus(w.jst("x-own"), 0)
	if w.read("held") != "" {
		t.Fatal("a scope with lock false took the lock")
	}
	w.expectStatus(w.jst("x-own", "--lock"), 0)
	if w.read("held") == "" {
		t.Fatal("--lock was not taken")
	}
}

func TestTheCommandDoesNotSeeTheSessionsFile(t *testing.T) {
	w := newWorld(t, lockSection(), map[string]any{"lock": map[string]any{"enabled": true}})
	t.Setenv(flock.PathVariable, filepath.Join(t.TempDir(), "suite.lock"))
	sessions := filepath.Join(t.TempDir(), "sessions")
	w.env[supervise.SessionsFileVariable] = sessions
	w.expectStatus(w.jst("full"), 0)
	if got := w.read("seen"); got != "none" {
		t.Fatalf("the command saw %s", got)
	}
	if last := w.launches[len(w.launches)-1].spec; last.RecordSessionIn != sessions || last.Mode != supervise.Session ||
		last.Grace != supervise.SessionGrace || last.Settle != time.Second {
		t.Fatalf("the run's session is not recorded in the outer lock's file, or not stopped as a session: %+v", last)
	}
}

func TestTheLockStaysOffByDefault(t *testing.T) {
	w := newWorld(t, lockSection(), nil)
	w.expectStatus(w.jst("full"), 0)
	if w.read("held") != "" {
		t.Fatal("the lock was taken while lock.enabled is off")
	}
}

func TestAHeldLockGivesUpAfterItsWait(t *testing.T) {
	w := newWorld(t, lockSection(), map[string]any{"lock": map[string]any{"enabled": true}})
	path := filepath.Join(t.TempDir(), "suite.lock")
	t.Setenv(flock.PathVariable, path)
	held, err := flock.Acquire(t.Context(), flock.Request{Lock: path, Words: "the test"})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	w.expectStatus(w.jst("full", "--wait", "0"), 75)
	w.expectLast("outcome=gave-up")
	if run := w.lastRecord("run"); run.Text("outcome") != "gave-up" || run.Text("scope") != "full" {
		t.Fatalf("the record: %v", run)
	}
}

func treeSection() map[string]any {
	return map[string]any{"judge": "swift-test", "scopes": map[string]any{
		"x-hang":  map[string]any{"run": argv("hang", "x"), "timeout": 2},
		"x-run":   map[string]any{"run": argv("run", "x"), "timeout": 600},
		"x-plain": map[string]any{"run": cat("pkg-filtered-xctest"), "timeout": 600},
	}}
}

func passingLines(t *testing.T) []string {
	return strings.Split(strings.TrimSuffix(fixture(t, "pkg-filtered-xctest"), "\n"), "\n")
}

func TestATimeoutStopsTheWholeTree(t *testing.T) {
	w := newWorld(t, treeSection(), nil)
	w.overrides["hang"] = func(order launch) *launched {
		if order.timeout != 2*time.Second {
			t.Errorf("the timeout: %v", order.timeout)
		}
		feed(order.spec.Output, strings.Join(passingLines(t), "\n"), "")
		order.stopping()
		return &launched{outcome: supervise.Outcome{PID: 50, Signal: syscall.SIGTERM}, timedOut: true, killed: "sh[50],sleep[51]"}
	}
	w.expectStatus(w.jst("x-hang"), 124)
	w.expectLast("outcome=timed-out", "killed=sh[50],sleep[51]", "no receipt")
	w.expectSaid("reached its timeout of 2 s")
	run := w.lastRecord("run")
	if run.Text("outcome") != "timed-out" || run["timed_out"] != true || run.Text("killed") != "sh[50],sleep[51]" {
		t.Fatalf("the record: %v", run)
	}
	if _, stored := run["stored"]; stored {
		t.Fatal("a timed-out run speaks of a receipt")
	}
	// Survivors are named.
	w.overrides["hang"] = func(order launch) *launched {
		order.stopping()
		return &launched{outcome: supervise.Outcome{PID: 50, Signal: syscall.SIGKILL, Killed: true,
			Survivors: []proctree.Survivor{{Member: proctree.Member{PID: 51}, Name: "xctest"}}}, timedOut: true}
	}
	w.expectStatus(w.jst("x-hang"), 124)
	w.expectLast("killed=nothing", "survivors=xctest[51]")
}

func TestAFailureSeenBeforeAStopIsAFailure(t *testing.T) {
	w := newWorld(t, treeSection(), nil)
	w.overrides["run"] = func(order launch) *launched {
		feed(order.spec.Output, "Test Case '-[A.B testC]' failed (0.001 seconds).", "")
		return &launched{outcome: supervise.Outcome{PID: 50, Signal: syscall.SIGTERM, StoppedBy: syscall.SIGTERM}}
	}
	w.expectStatus(w.jst("x-run"), 128+int(syscall.SIGTERM))
	w.expectLast("outcome=failed (a failure was seen before the stop", "SIGTERM after")
	if w.ended != syscall.SIGTERM {
		t.Fatalf("jackstay ended by %v", w.ended)
	}
	if run := w.lastRecord("run"); run.Text("signal") != "SIGTERM" || run.Text("outcome") != "failed" {
		t.Fatalf("the record: %v", run)
	}
	// A failure in a timeout's run is a failure too.
	w.overrides["run"] = func(order launch) *launched {
		feed(order.spec.Output, "Test Case '-[A.B testC]' failed (0.001 seconds).", "")
		order.stopping()
		return &launched{outcome: supervise.Outcome{PID: 50, Signal: syscall.SIGTERM}, timedOut: true}
	}
	w.expectStatus(w.jst("x-run"), 1)
	w.expectLast("outcome=failed (exit 1")
}

func TestAStopAfterTheCommandFailedByItselfIsAFailure(t *testing.T) {
	w := newWorld(t, treeSection(), nil)
	ended := time.Date(2026, 1, 2, 11, 0, 0, 0, time.UTC)
	w.overrides["run"] = func(launch) *launched {
		return &launched{outcome: supervise.Outcome{PID: 50, Code: 3, Ended: ended, StoppedBy: syscall.SIGTERM}}
	}
	w.firstStop = func(*supervise.Signals) (syscall.Signal, time.Time, bool) {
		return syscall.SIGTERM, ended.Add(time.Second), true
	}
	w.jst("x-run")
	w.expectLast("outcome=failed", "exit 3 (the command had ended by itself)")
	if w.ended != syscall.SIGTERM {
		t.Fatal("jackstay did not end by the signal")
	}
}

func TestWhatIsLeftAfterANormalEndIsStoppedAndTheRunIsNoPass(t *testing.T) {
	w := newWorld(t, treeSection(), nil)
	w.overrides["run"] = func(order launch) *launched {
		feed(order.spec.Output, strings.Join(passingLines(t), "\n"), "")
		return &launched{outcome: supervise.Outcome{PID: 50, Left: []procs.Process{{PID: 51, Name: "sleep"}}}}
	}
	w.expectStatus(w.jst("x-run"), 77)
	w.expectLast("outcome=unjudged", "left processes running")
	if run := w.lastRecord("run"); run.Text("left_running") != "sleep[51]" {
		t.Fatalf("the record: %v", run)
	}
}

func TestAStopOfJstTestStopsTheWholeTree(t *testing.T) {
	w := newWorld(t, treeSection(), nil)
	w.overrides["run"] = func(order launch) *launched {
		return &launched{outcome: supervise.Outcome{PID: 50, Signal: syscall.SIGTERM, StoppedBy: syscall.SIGTERM}}
	}
	w.expectStatus(w.jst("x-run"), 143)
	w.expectSaid("outcome=stopped (SIGTERM after")
	if w.lastRecord("run").Text("outcome") != "stopped" || w.ended != syscall.SIGTERM {
		t.Fatalf("the record: %v", w.lastRecord("run"))
	}
	left, _ := os.ReadDir(w.tmp)
	if len(left) != 0 {
		t.Fatalf("a stopped run left files of its own in $TMPDIR: %v", left)
	}
}

func TestAnUnreadableProcessTableIsNoPass(t *testing.T) {
	w := newWorld(t, treeSection(), nil)
	w.unreadable = true
	w.expectStatus(w.jst("x-plain"), 77)
	w.expectLast("outcome=unjudged", "cannot be confirmed")
	if run := w.lastRecord("run"); run["tree_unconfirmed"] != true {
		t.Fatalf("the record: %v", run)
	}
	// What supervise could not do on the way is no pass either.
	w.unreadable = false
	w.overrides["run"] = func(order launch) *launched {
		feed(order.spec.Output, strings.Join(passingLines(t), "\n"), "")
		return &launched{outcome: supervise.Outcome{PID: 50, Problems: []error{os.ErrPermission}}}
	}
	w.expectStatus(w.jst("x-run"), 77)
	w.expectLast("cannot be confirmed")
	// Nor is an output not read to its end.
	w.overrides["run"] = func(order launch) *launched {
		feed(order.spec.Output, strings.Join(passingLines(t), "\n"), "")
		return &launched{outcome: supervise.Outcome{PID: 50, Unread: "the output was not read to its end"}}
	}
	w.expectStatus(w.jst("x-run"), 77)
	w.expectLast("not read to its end")
}

func TestPatternsOfTests(t *testing.T) {
	cases := []struct {
		names         []string
		match, differ []string
	}{
		{[]string{"Target.Class/testA"}, []string{"Target.Class/testA"}, []string{"Target.Class/testAB"}},
		{[]string{"Target.Class"}, []string{"Target.Class/testA"}, []string{"Target.ClassTwo/testA"}},
		{[]string{"Class"}, []string{"Other.Class/x"}, []string{"Other.SubClass/x"}},
		{[]string{"A.B", "C"}, []string{"A.B/t", "X.C/t"}, []string{"A.BC/t"}},
	}
	for _, each := range cases {
		pattern := regexp.MustCompile(testsPattern(each.names))
		for _, name := range each.match {
			if !pattern.MatchString(name) {
				t.Errorf("%v does not match %s", each.names, name)
			}
		}
		for _, name := range each.differ {
			if pattern.MatchString(name) {
				t.Errorf("%v matches %s", each.names, name)
			}
		}
	}
	if got := escapePattern("a.b-c d"); got != `a\.b\-c\ d` {
		t.Fatalf("escaped as %s", got)
	}
}

func TestPlaceholders(t *testing.T) {
	if got := placeholdersOf([]string{"echo ${HOME} {X}", "^A{2,3}$", "{tests}", "x{base}y", "${slow}"}); strings.Join(got, ",") != "tests,base" {
		t.Fatalf("found %v", got)
	}
	if got := replacePlaceholders("a{base}b${base}{unknown}", map[string]string{"base": "B"}); got != "aBb${base}{unknown}" {
		t.Fatalf("replaced as %s", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellWords([]string{"sh", "-c", "echo 'x'", ""}); got != `sh -c 'echo '"'"'x'"'"'' ''` {
		t.Fatalf("quoted as %s", got)
	}
}
