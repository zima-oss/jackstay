package scopes

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/receipts"
	"github.com/zima-oss/jackstay/internal/supervise"
)

func gateSection() map[string]any {
	return map[string]any{"judge": "exit", "timeout": 600,
		"scopes": map[string]any{
			"full":     map[string]any{"run": counted("full", `cat "$SIDE/pkg-filtered-xctest.log"`), "judge": "swift-test"},
			"gate":     map[string]any{"steps": argv("cheap", "middle", "full")},
			"x-nested": map[string]any{"steps": argv("cheap", "x-inner")},
			"x-inner":  map[string]any{"steps": argv("middle", "last")},
			"x-base":   map[string]any{"steps": argv("based")},
		},
		"steps": map[string]any{
			"cheap":  map[string]any{"run": counted("cheap", `test ! -f "$SIDE/cheap-fails"`)},
			"middle": map[string]any{"run": counted("middle", `test ! -f "$SIDE/middle-fails"`)},
			"last":   map[string]any{"run": counted("last", "true")},
			"based":  map[string]any{"run": argv("sh", "-c", `echo run >> "$SIDE/based"; echo "$1"`, "sh", "{base}"), "base": "base-ref"},
		}}
}

func with(section map[string]any, kind, name string, entry map[string]any) map[string]any {
	copied := map[string]any{}
	for key, value := range section {
		copied[key] = value
	}
	table := map[string]any{}
	for key, value := range objectOf(section[kind]) {
		table[key] = value
	}
	table[name] = entry
	copied[kind] = table
	return copied
}

// newGateWorld is a gate's world with a base branch.
func newGateWorld(t *testing.T) *world {
	w := newWorld(t, gateSection(), nil)
	w.repo.Git("branch", "base-ref")
	return w
}

func (w *world) counters(names ...string) []int {
	var found []int
	for _, name := range names {
		found = append(found, w.counter(name))
	}
	return found
}

func (w *world) expectCounters(names string, wanted ...int) {
	w.t.Helper()
	got := w.counters(strings.Fields(names)...)
	for index := range wanted {
		if got[index] != wanted[index] {
			w.t.Fatalf("%s ran %v times, want %v; stderr:\n%s", names, got, wanted, w.stderr.String())
		}
	}
}

func TestStepsRunInOrderAndStopAtTheFirstFailure(t *testing.T) {
	w := newGateWorld(t)
	w.flag("middle-fails", true)
	w.expectStatus(w.jst("gate"), 1)
	w.expectLast("gate failed at middle")
	w.expectCounters("cheap middle full", 1, 1, 0)
	var steps []receipts.Record
	for _, record := range w.records("run") {
		if record.Text("gate_run_id") != "" {
			steps = append(steps, record)
		}
	}
	if len(steps) != 2 || steps[0].Text("step") != "cheap" || steps[1].Text("step") != "middle" {
		t.Fatalf("the steps' lines: %v", steps)
	}
	if _, present := steps[0]["scope"]; present {
		t.Fatalf("a step that is no scope names one: %v", steps[0])
	}
	gate := w.lastRecord("gate")
	outcomes := []string{}
	for _, raw := range gate["steps"].([]any) {
		outcomes = append(outcomes, raw.(map[string]any)["outcome"].(string))
	}
	if strings.Join(outcomes, ",") != "exited-0,failed,not-run,not-run" || gate.Text("gate_run_id") != steps[0].Text("gate_run_id") {
		t.Fatalf("the gate's line: %v", gate)
	}
	if start := w.lastRecord("gate-start"); start.Text("gate_run_id") != gate.Text("gate_run_id") {
		t.Fatalf("the gate-start line: %v", start)
	}
	w.jst("gate", "--keep-going")
	w.expectCounters("full", 1)
	w.expectLast("failed at middle")
	var out, errs bytes.Buffer
	receipts.LastCommand([]string{"--scope", "gate"}, nil, &out, &errs)
	if !strings.Contains(out.String(), "middle: failed") {
		t.Fatalf("jst test last --scope gate:\n%s%s", out.String(), errs.String())
	}
}

func TestResumeSkipsPassedStepsOnTheSameTree(t *testing.T) {
	w := newGateWorld(t)
	w.expectStatus(w.jst("gate"), 0)
	w.expectLast("outcome=passed")
	w.expectStatus(w.jst("gate", "--resume"), 0)
	w.expectLast("outcome=resumed")
	w.expectCounters("cheap middle full", 1, 1, 1)
	if start := w.lastRecord("gate-start"); start["resume"] != true {
		t.Fatalf("the gate-start line: %v", start)
	}
	// Without --resume everything runs again.
	w.jst("gate")
	w.expectCounters("full", 2)
	// A new commit is a new tree.
	w.repo.Write(map[string]string{"Sources/b.swift": "let b = 2\n"})
	w.commit()
	w.expectStatus(w.jst("gate", "--resume"), 0)
	w.expectLast("outcome=passed")
	w.expectCounters("full", 3)
}

func TestAChangedCheckoutIsNotResumed(t *testing.T) {
	w := newGateWorld(t)
	w.jst("gate")
	w.repo.Write(map[string]string{"Sources/a.swift": "let a = 2\n"})
	w.jst("gate", "--resume")
	w.expectSaid("only a clean checkout is resumed")
	w.expectCounters("cheap", 2)
	w.repo.Write(map[string]string{"untracked.txt": "x\n"})
	w.repo.Git("checkout", "Sources/a.swift")
	w.jst("gate", "--resume")
	w.expectCounters("cheap", 3)
}

func TestAnEnvironmentVariableTheKeyReadsIsPartOfIt(t *testing.T) {
	w := newGateWorld(t)
	w.jst("gate")
	w.env["DEMO_FLAG"] = "1"
	w.jst("gate", "--resume")
	w.expectCounters("cheap", 2)
}

func TestStepKeyUsesTheStepDirectoryAndEnvironmentForToolchain(t *testing.T) {
	w := newGateWorld(t)
	proj, err := w.tester().open(options{})
	if err != nil {
		t.Fatal(err)
	}
	proj.receipts["toolchain"] = []any{argv("/bin/sh", "-c", `test -f a.swift && test "$SDK_MODE" = pinned`)}
	chosen := leaf{name: "cheap", kind: "step", cwd: "Sources", env: map[string]string{"SDK_MODE": "pinned"}}
	key, why := proj.stepKey(chosen, []string{"tests"}, "", "Sources")
	if key == "" {
		t.Fatalf("step key refused: %s", why)
	}
}

func TestAStepThatRewritesAFileGetsNoMark(t *testing.T) {
	w := newGateWorld(t)
	w.configure(with(gateSection(), "steps", "cheap", map[string]any{"run": counted("cheap", "cp Sources/a.swift x.tmp; mv x.tmp Sources/a.swift")}), nil)
	w.commit()
	w.jst("gate")
	w.expectSaid("no mark (a file of the checkout was rewritten")
	w.jst("gate", "--resume")
	w.expectCounters("cheap middle", 2, 1)
}

func TestAFailedStepIsNotRunAgainByResume(t *testing.T) {
	w := newGateWorld(t)
	w.flag("middle-fails", true)
	w.jst("gate")
	w.flag("middle-fails", false) // outside the checkout: the tree is the same
	w.expectStatus(w.jst("gate", "--resume"), 2)
	w.expectSaid("nothing has changed since this step failed")
	w.expectLast("outcome=refused")
	w.expectCounters("middle", 1)
	w.expectStatus(w.jst("gate"), 0)
	w.expectSaid("step middle: no mark (this step failed on this same tree before; its bar stands)")
	// It passed now, and its bar stays: --resume still refuses on this tree.
	w.expectStatus(w.jst("gate", "--resume"), 2)
}

func TestResumeFalseAlwaysRuns(t *testing.T) {
	w := newGateWorld(t)
	cheap := map[string]any{"run": counted("cheap", `test ! -f "$SIDE/cheap-fails"`), "resume": false}
	w.configure(with(gateSection(), "steps", "cheap", cheap), nil)
	w.commit()
	w.jst("gate")
	w.jst("gate", "--resume")
	w.expectSaid(`step cheap: runs ("resume": false)`)
	w.expectCounters("cheap middle", 2, 1)
}

func TestCompositeStepsExpandIntoLeaves(t *testing.T) {
	w := newGateWorld(t)
	w.expectStatus(w.jst("x-nested"), 0)
	var names []string
	for _, raw := range w.lastRecord("gate")["steps"].([]any) {
		if raw.(map[string]any)["composite"] == nil {
			names = append(names, raw.(map[string]any)["step"].(string))
		}
	}
	if strings.Join(names, ",") != "cheap,middle,last" {
		t.Fatalf("steps: %v", names)
	}
	w.jst("x-nested", "--resume")
	w.expectCounters("last", 1)
}

func TestACommandsOwnRefsAreInTheKey(t *testing.T) {
	w := newGateWorld(t)
	lastWords := argv("sh", "-c", `echo run >> "$SIDE/last"`)
	w.configure(with(gateSection(), "steps", "last", map[string]any{"run": lastWords}),
		map[string]any{"receipts": map[string]any{"inputs": argv("."), "toolchain": []any{}, "env": []any{},
			"commands": []any{map[string]any{"argv": lastWords, "refs": argv("base-ref")}}}})
	w.commit()
	w.repo.Git("branch", "-f", "base-ref", "HEAD")
	w.jst("x-nested")
	w.jst("x-nested", "--resume")
	w.expectCounters("last", 1)
	w.repo.Git("branch", "-f", "base-ref", "HEAD~1")
	w.jst("x-nested", "--resume")
	w.expectCounters("last", 2)
	w.repo.Git("branch", "-D", "base-ref")
	w.jst("x-nested", "--resume")
	w.expectSaid("the ref base-ref cannot be resolved")
	w.jst("x-nested", "--resume")
	w.expectCounters("last", 4)
}

// A schema-2 list's own `requires` run once, before any node of a run whose graph holds the list,
// and gate the whole run; a run whose graph lacks the list does not run them.
func TestANestedSchema2ListsRequiresGateTheWholeRun(t *testing.T) {
	w := newGateWorld(t)
	inner := map[string]any{"steps": argv("middle", "last"), "requires": []any{map[string]any{"run": counted("check", "exit 4"), "why": "needs a screen"}}}
	section := with(gateSection(), "scopes", "x-inner", inner)
	section = with(section, "scopes", "x-other", map[string]any{"steps": argv("x-inner", "cheap")})
	w.configure(section, map[string]any{"jackstay_schema": 2})
	w.commit()
	w.expectStatus(w.jst("x-nested"), 2)
	w.expectSaid("x-inner: needs a screen")
	w.expectLast("outcome=not-started (x-nested failed at x-inner")
	w.expectCounters("cheap middle last check", 0, 0, 0, 1)
	// Reached directly in one list and through a nested one, the list is checked once.
	w.expectStatus(w.jst("x-other"), 2)
	w.expectCounters("cheap check", 0, 2)
	w.expectStatus(w.jst("gate"), 0)
	w.expectCounters("cheap check", 1, 2)
	// In schema 3 a composite carries no `requires`.
	w.configure(section, nil)
	w.expectStatus(w.jst("gate"), 2)
	w.expectSaid("`test.scopes.x-inner.requires`: a scope with no `run`")
}

func TestAChangeHiddenByInfoExcludeIsNotResumed(t *testing.T) {
	w := newGateWorld(t)
	w.expectStatus(w.jst("gate"), 0)
	exclude := filepath.Join(w.repo.Root, ".git", "info", "exclude")
	appendTo := func(text string) {
		file, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.WriteString(text)
		_ = file.Close()
	}
	appendTo("local.swift\n")
	w.repo.Write(map[string]string{"local.swift": "let x = 1\n"})
	w.jst("gate", "--resume")
	w.expectSaid("only a rule outside the committed .gitignore files hides")
	w.expectCounters("cheap", 2)
	// Agents' own folders, hidden the same way, hold no source: no reason not to resume.
	_ = os.Remove(filepath.Join(w.repo.Root, "local.swift"))
	appendTo(".omx/\n")
	w.jst("gate")
	w.repo.Write(map[string]string{".omx/logs/now.jsonl": "{}\n"})
	w.jst("gate", "--resume")
	w.expectLast("outcome=resumed")
}

func TestASkipWorktreeEntryIsNotResumed(t *testing.T) {
	w := newGateWorld(t)
	w.jst("gate")
	w.repo.Git("update-index", "--skip-worktree", "Sources/a.swift")
	w.jst("gate", "--resume")
	w.expectSaid("git is told not to look at Sources/a.swift")
	w.expectCounters("cheap", 2)
}

func TestALatestRunWithNoVerdictTakesTheMarkAway(t *testing.T) {
	w := newGateWorld(t)
	w.expectStatus(w.jst("gate"), 0)
	w.overrides["pkg-filtered-xctest"] = func(order launch) *launched {
		if _, err := os.Stat(w.at("leave")); err != nil {
			return nil
		}
		feed(order.spec.Output, strings.Join(passingLines(t), "\n"), "")
		return &launched{outcome: supervise.Outcome{PID: 70, Left: []procs.Process{{PID: 71, Name: "sleep"}}}}
	}
	w.flag("leave", true)
	w.expectStatus(w.jst("full", "--no-reuse"), 77)
	w.flag("leave", false)
	w.jst("gate", "--resume")
	w.expectCounters("full", 2)
}

func TestRequiresAreCheckedBeforeAStepIsResumed(t *testing.T) {
	w := newGateWorld(t)
	screen := []any{map[string]any{"run": argv("sh", "-c", `test ! -f "$SIDE/no-screen"`), "why": "needs a screen"}}
	section := with(gateSection(), "steps", "cheap", map[string]any{"run": counted("cheap", "true"), "requires": screen})
	w.configure(section, nil)
	w.commit()
	w.expectStatus(w.jst("x-nested"), 0)
	w.flag("no-screen", true)
	w.expectStatus(w.jst("x-nested", "--resume"), 2)
	w.expectSaid("needs a screen")
	if strings.Contains(w.lastLine(), "resumed") {
		t.Fatalf("a step was resumed: %s", w.lastLine())
	}
	// --keep-going runs what does not depend on the failure: x-inner's steps are resumed.
	w.expectStatus(w.jst("x-nested", "--resume", "--keep-going"), 2)
	w.expectLast("cheap=not-started", "middle=resumed", "last=resumed", "x-nested=not-run")
}

func TestAMovedBaseIsANewKey(t *testing.T) {
	w := newGateWorld(t)
	w.jst("x-base")
	w.jst("x-base", "--resume")
	w.expectCounters("based", 1)
	w.repo.Write(map[string]string{"Sources/c.swift": "x\n"})
	w.commit()
	w.repo.Git("branch", "-f", "base-ref", "HEAD")
	w.repo.Git("reset", "-q", "--hard", "HEAD~1")
	w.jst("x-base", "--resume")
	w.expectCounters("based", 2)
}

func TestAStepStoppedOnARerunIsNotResumedOnItsOldPass(t *testing.T) {
	w := newGateWorld(t)
	w.expectStatus(w.jst("gate"), 0) // marks for every step
	w.overrides["middle"] = func(launch) *launched {
		if _, err := os.Stat(w.at("hang")); err != nil {
			return nil
		}
		return &launched{outcome: supervise.Outcome{PID: 80, Signal: syscall.SIGTERM, StoppedBy: syscall.SIGTERM}}
	}
	w.flag("hang", true)
	w.expectStatus(w.jst("gate"), 143) // runs again, and is stopped in `middle`
	if w.ended != syscall.SIGTERM || w.lastRecord("run").Text("outcome") != "stopped" {
		t.Fatalf("ended by %v; %v", w.ended, w.lastRecord("run"))
	}
	if gates := w.records("gate"); len(gates) != 1 {
		t.Fatalf("a stopped gate wrote its end: %v", gates)
	}
	if starts := w.records("gate-start"); len(starts) != 2 {
		t.Fatalf("gate-start lines: %v", starts)
	}
	w.flag("hang", false)
	w.expectStatus(w.jst("gate", "--resume"), 0)
	if strings.Contains(w.stderr.String(), "step middle: resumed") {
		t.Fatal("middle was resumed on its old pass")
	}
	w.expectCounters("middle", 2)
}

func TestADirectRunThatFailedAndLeftAFileStillBars(t *testing.T) {
	w := newGateWorld(t)
	fullWords := `test ! -f "$SIDE/full-fails" && cat "$SIDE/pkg-filtered-xctest.log" || { touch report.txt; exit 1; }`
	w.configure(with(gateSection(), "scopes", "full", map[string]any{"run": counted("full", fullWords), "judge": "swift-test"}), nil)
	w.commit()
	w.expectStatus(w.jst("gate"), 0)
	w.flag("full-fails", true)
	w.expectStatus(w.jst("full", "--no-reuse"), 1)
	report := filepath.Join(w.repo.Root, "report.txt")
	if _, err := os.Stat(report); err != nil {
		t.Fatal("the failed run left no file")
	}
	_ = os.Remove(report)
	w.flag("full-fails", false)
	w.expectStatus(w.jst("gate", "--resume"), 2)
	w.expectLast("full=refused")
}

func TestADirectRunThatFailedBarsTheGatesMark(t *testing.T) {
	w := newGateWorld(t)
	fullWords := `test ! -f "$SIDE/full-fails" && cat "$SIDE/pkg-filtered-xctest.log"`
	w.configure(with(gateSection(), "scopes", "full", map[string]any{"run": counted("full", fullWords), "judge": "swift-test"}), nil)
	w.commit()
	w.expectStatus(w.jst("gate"), 0) // the gate never saw `full` fail: its mark is written
	bars, _ := receipts.StateFolder("demo", "gate-bars")
	if _, err := os.Stat(bars); err == nil {
		t.Fatal("bars were written for a gate that passed")
	}
	w.flag("full-fails", true)
	w.expectStatus(w.jst("full"), 1)
	w.flag("full-fails", false)
	w.expectStatus(w.jst("gate", "--resume"), 2)
	w.expectLast("full=refused")
}

func TestATimedOutStepRunsAgainAndBarsNothing(t *testing.T) {
	w := newGateWorld(t)
	w.overrides["middle"] = func(order launch) *launched {
		if _, err := os.Stat(w.at("hang")); err != nil {
			return nil
		}
		order.stopping()
		return &launched{outcome: supervise.Outcome{PID: 80, Signal: syscall.SIGTERM}, timedOut: true}
	}
	w.flag("hang", true)
	w.expectStatus(w.jst("gate"), 124)
	w.expectLast("outcome=timed-out (gate failed at middle, exit 124")
	w.flag("hang", false)
	w.expectStatus(w.jst("gate", "--resume"), 0)
	w.expectCounters("middle", 1) // the override answered for the hung run; this one really ran
}

func TestMarksAndBarsAge(t *testing.T) {
	w := newGateWorld(t)
	w.expectStatus(w.jst("gate"), 0)
	// A mark older than receipts.max_age_hours is not used.
	w.clock.Advance(73 * time.Hour)
	w.jst("gate", "--resume")
	w.expectLast("outcome=passed")
	w.expectCounters("cheap", 2)
	proj, err := w.tester().open(options{})
	if err != nil {
		t.Fatal(err)
	}
	folder := proj.stateFolder("gate-bars")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	young, old := filepath.Join(folder, "young.1"), filepath.Join(folder, "old.1")
	for _, place := range []string{young, old} {
		if err := os.WriteFile(place, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := w.clock.Now()
	// A bar lives receipts.max_age_hours and a day more: one 90 h old stays, one 97 h old goes.
	_ = os.Chtimes(young, now.Add(-90*time.Hour), now.Add(-90*time.Hour))
	_ = os.Chtimes(old, now.Add(-97*time.Hour), now.Add(-97*time.Hour))
	proj.writeBar("key", "step", result{outcome: "failed"})
	if _, err := os.Stat(young); err != nil {
		t.Fatal("a bar younger than max_age_hours and a day was pruned")
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("a bar older than max_age_hours and a day was kept")
	}
	if found := proj.barOf("key"); found == nil || found.Outcome != "failed" {
		t.Fatalf("the bar: %+v", found)
	}
}

func TestGitThatFailsIsNoCleanCheckout(t *testing.T) {
	w := newGateWorld(t)
	w.git = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no git") }
	w.jst("gate", "--resume")
	w.expectSaid("git status could not be read")
	w.expectLast("outcome=passed")
}

func TestStepsThatCannotStartEndTheGate(t *testing.T) {
	w := newGateWorld(t)
	w.repo.Git("branch", "-D", "base-ref")
	section := with(gateSection(), "steps", "based", map[string]any{"run": argv("sh", "-c", "echo $1", "sh", "{base}")})
	w.configure(section, nil)
	w.expectStatus(w.jst("x-base"), 2)
	w.expectSaid("needs a base")
	w.expectLast("outcome=not-started (x-base failed at based")
}
