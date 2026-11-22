package scopes

import (
	"bytes"
	"os"
	"strings"
	"syscall"
	"testing"

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

func TestResumeIsRefusedAndARerunRunsEveryNode(t *testing.T) {
	w := newGateWorld(t)
	w.expectStatus(w.jst("gate", "--resume"), 2)
	w.expectSaid("--resume was removed in jackstay 0.6.0")
	w.expectSaid("reuse of passes comes with the cache of jackstay 1")
	w.expectLast("outcome=not-started")
	w.expectCounters("cheap middle full", 0, 0, 0)
	// A gate stopped by a failure and run again runs every node again, the ones that passed too.
	w.flag("middle-fails", true)
	w.expectStatus(w.jst("gate"), 1)
	w.expectCounters("cheap middle full", 1, 1, 0)
	w.flag("middle-fails", false)
	w.expectStatus(w.jst("gate"), 0)
	w.expectCounters("cheap middle full", 2, 2, 1)
	w.expectStatus(w.jst("gate"), 0)
	w.expectCounters("cheap middle full", 3, 3, 2)
	for _, gone := range []string{"resumed", "refused"} {
		if strings.Contains(w.stderr.String(), "="+gone) {
			t.Fatalf("a removed outcome %s:\n%s", gone, w.stderr.String())
		}
	}
	if start := w.lastRecord("gate-start"); start["resume"] != nil {
		t.Fatalf("the gate-start line still says resume: %v", start)
	}
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

func TestAFailingRequiresKeepsTheStepFromStarting(t *testing.T) {
	w := newGateWorld(t)
	screen := []any{map[string]any{"run": argv("sh", "-c", `test ! -f "$SIDE/no-screen"`), "why": "needs a screen"}}
	section := with(gateSection(), "steps", "cheap", map[string]any{"run": counted("cheap", "true"), "requires": screen})
	w.configure(section, nil)
	w.commit()
	w.expectStatus(w.jst("x-nested"), 0)
	w.flag("no-screen", true)
	w.expectStatus(w.jst("x-nested"), 2)
	w.expectSaid("needs a screen")
	// --keep-going runs what does not depend on the failure: x-inner's steps run.
	w.expectStatus(w.jst("x-nested", "--keep-going"), 2)
	w.expectLast("cheap=not-started", "x-nested=not-run")
	w.expectCounters("cheap", 1)
}

func TestATimedOutStepRunsAgain(t *testing.T) {
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
	w.expectStatus(w.jst("gate"), 0)
	w.expectCounters("middle", 1) // the override answered for the hung run; this one really ran
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
