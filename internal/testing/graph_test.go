package scopes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/receipts"
)

// counting is a scope that counts its runs and one test, so a run of it is `passed`.
func counting(name string, more map[string]any) map[string]any {
	entry := map[string]any{"run": counted(name, `test ! -f "$SIDE/`+name+`-fails" && echo ran 1 tests`), "judge": "steps",
		"evidence": "ran ([0-9]+) tests"}
	for key, value := range more {
		entry[key] = value
	}
	return entry
}

// whereSection is the example of docs/design/land.md section 7.1: x-go and x-docs in Linux CI,
// x-ios on macOS only, both over a build that runs anywhere.
func whereSection() map[string]any {
	return map[string]any{"timeout": 600,
		"selection": map[string]any{"order": argv("x-docs", "gate"),
			"rules": []any{map[string]any{"glob": "docs/**", "scope": "x-docs"}}},
		"scopes": map[string]any{
			"x-build": map[string]any{"run": counted("x-build", "true"), "judge": "exit"},
			"x-go":    counting("x-go", map[string]any{"deps": argv("x-build"), "ci": argv("linux")}),
			"x-docs":  counting("x-docs", map[string]any{"ci": argv("linux")}),
			"x-ios":   counting("x-ios", map[string]any{"deps": argv("x-build"), "systems": argv("darwin")}),
			"gate":    map[string]any{"deps": argv("x-docs", "x-go", "x-ios")},
		}}
}

func newWhereWorld(t *testing.T, system string) *world {
	w := newWorld(t, whereSection(), nil)
	w.system = system
	return w
}

func TestTheWalkFollowsTheDeclarationsAndTheSystem(t *testing.T) {
	section := whereSection()
	for _, each := range []struct {
		system, asked, want string
	}{
		{"darwin", "gate", "x-docs,x-build,x-go,x-ios,gate"},
		// On Linux x-ios is a node, skipped; x-build enters through x-go.
		{"linux", "gate", "x-docs,x-build,x-go,x-ios,gate"},
		{"linux", "x-ios", "x-ios"},
		{"darwin", "x-ios x-go", "x-build,x-ios,x-go"},
	} {
		if got := strings.Join(newGraph(section, each.system).walk(strings.Fields(each.asked)), ","); got != each.want {
			t.Errorf("%s on %s: %s, want %s", each.asked, each.system, got, each.want)
		}
	}
	diamond := map[string]any{"scopes": map[string]any{
		"x-a": map[string]any{"deps": argv("x-b", "x-c")}, "x-b": map[string]any{"run": argv("b"), "deps": argv("x-d")},
		"x-c": map[string]any{"run": argv("c"), "deps": argv("x-d")}, "x-d": map[string]any{"run": argv("d")}}}
	if got := strings.Join(newGraph(diamond, "linux").walk(argv2("x-a")), ","); got != "x-d,x-b,x-c,x-a" {
		t.Errorf("diamond: %s", got)
	}
	if got := strings.Join(newGraph(diamond, "linux").walk(argv2("x-c", "x-b")), ","); got != "x-d,x-c,x-b" {
		t.Errorf("two scopes asked for: %s", got)
	}
	// A gate over a darwin-only test whose build could run anywhere: on Linux the test is skipped
	// and the build is not visited through it.
	only := map[string]any{"scopes": map[string]any{
		"gate":    map[string]any{"deps": argv("x-ios", "x-lint")},
		"x-lint":  map[string]any{"run": argv("lint")},
		"x-ios":   map[string]any{"run": argv("ios"), "deps": argv("x-build"), "systems": argv("darwin")},
		"x-build": map[string]any{"run": argv("build")}}}
	linux := newGraph(only, "linux")
	if got := strings.Join(linux.walk(argv2("gate")), ","); got != "x-ios,x-lint,gate" || linux.eligible("x-ios") || !linux.eligible("x-build") {
		t.Errorf("darwin-only gate on Linux: %s", got)
	}
}

func argv2(items ...string) []string { return items }

func TestEligibilityIsOneRecursiveRule(t *testing.T) {
	section := map[string]any{"scopes": map[string]any{
		"x-ios":  map[string]any{"run": argv("ios"), "systems": argv("darwin")},
		"x-mac":  map[string]any{"deps": argv("x-ios")},
		"x-docs": map[string]any{"run": argv("docs")},
		"x-all":  map[string]any{"deps": argv("x-mac", "x-docs"), "ci": argv("linux")},
		"x-pack": map[string]any{"run": argv("pack"), "deps": argv("x-mac")},
	}}
	linux := newGraph(section, "linux")
	if !linux.eligible("x-all") || linux.eligible("x-mac") || linux.eligible("x-pack") {
		t.Fatal("an unrestricted composite over a darwin-only one is Linux-eligible through its other deps; the darwin-only one and a scope with run over it are not")
	}
	if problems := graphProblems(section); len(problems) != 0 {
		t.Fatalf("ci: [linux] on x-all: %v", problems)
	}
	scopes := objectOf(section["scopes"])
	scopes["x-mac"] = map[string]any{"deps": argv("x-ios"), "ci": argv("linux")}
	problems := strings.Join(graphProblems(section), "; ")
	if !strings.Contains(problems, "`test.scopes.x-mac.ci` names linux, where it cannot run: x-ios runs on darwin only") {
		t.Fatalf("ci: [linux] on the darwin-only composite: %s", problems)
	}
}

func TestTheGraphsProblemsAreRefused(t *testing.T) {
	for _, each := range []struct {
		test   map[string]any
		wanted string
	}{
		{map[string]any{"scopes": map[string]any{"gate": map[string]any{"deps": argv("x-a")},
			"x-a": map[string]any{"run": argv("a"), "deps": argv("gate")}}}, "circle: gate → x-a → gate"},
		{map[string]any{"scopes": map[string]any{"gate": map[string]any{"deps": argv("nope")}}}, "neither a step nor a scope"},
		{map[string]any{"scopes": map[string]any{"gate": map[string]any{"deps": argv("full"), "steps": argv("full")},
			"full": map[string]any{"run": argv("x")}}}, "both `steps` and `deps`"},
		{map[string]any{"scopes": map[string]any{"x-go": map[string]any{"run": argv("x"), "ci": argv("linux"), "systems": argv("darwin")}}},
			"names linux, where it cannot run"},
		{map[string]any{"scopes": map[string]any{"gate": map[string]any{"deps": argv("x-one")},
			"x-one": map[string]any{"run": argv("go", "test", "{test_args}"), "selector": "go-test", "judge": "go-json"}}},
			"whose words need test names"},
		{map[string]any{"scopes": map[string]any{"gate": map[string]any{"deps": argv("full"), "requires": []any{map[string]any{"run": argv("y"), "why": "z"}}},
			"full": map[string]any{"run": argv("x")}}}, "move the check to the dep that needs it"},
		{map[string]any{"scopes": map[string]any{"full": map[string]any{"run": argv("x"), "systems": argv("windows")}}}, "'windows' is not a system"},
		{map[string]any{"scopes": map[string]any{"gate": map[string]any{"when": "always"}}}, "names no `deps`"},
	} {
		if found := strings.Join(sectionProblems(t, each.test, nil), "\n"); !strings.Contains(found, each.wanted) {
			t.Errorf("%v: %s does not say %q", each.test, found, each.wanted)
		}
	}
	// Each refusal is also `not-started` before anything runs.
	w := newWorld(t, map[string]any{"scopes": map[string]any{"gate": map[string]any{"deps": argv("x-a")},
		"x-a": map[string]any{"run": counted("x-a", "true"), "deps": argv("gate")}}}, nil)
	w.expectStatus(w.jst("gate"), 2)
	w.expectLast("outcome=not-started")
	w.expectCounters("x-a", 0)
}

func TestASharedDepRunsOnceAfterWhatItNeeds(t *testing.T) {
	section := map[string]any{"judge": "exit", "scopes": map[string]any{
		"gate":  map[string]any{"deps": argv("x-a", "x-b")},
		"x-a":   map[string]any{"run": counted("x-a", "true"), "deps": argv("x-lib")},
		"x-b":   map[string]any{"run": counted("x-b", "true"), "deps": argv("x-lib")},
		"x-lib": map[string]any{"run": counted("x-lib", "true")},
	}}
	w := newWorld(t, section, nil)
	w.expectStatus(w.jst("gate"), 0)
	w.expectCounters("x-lib x-a x-b", 1, 1, 1)
	w.expectLast("outcome=passed (gate, ", "x-lib=exited-0, x-a=exited-0, x-b=exited-0")
	// A scope with run and deps runs its deps first.
	w.expectStatus(w.jst("x-a"), 0)
	w.expectCounters("x-lib x-a x-b", 2, 2, 1)
	w.expectLast("outcome=exited-0 (x-a, ")
	w.expectStatus(w.jst("x-a", "Some.Test"), 2)
	w.expectSaid("has deps (or lists steps) and takes no names")
}

// A scope with `run` and deps that takes test names gets them; its deps get none, in the run and
// in the plan.
func TestTheScopeAskedForTakesTheTestNamesAndItsDepsNone(t *testing.T) {
	section := map[string]any{"judge": "exit", "scopes": map[string]any{
		"focused": map[string]any{"run": argv("sh", "-c", `echo "$0" >> "$SIDE/focused-names"`, "{tests}"), "deps": argv("x-build")},
		"x-build": map[string]any{"run": counted("x-build", "true")},
	}}
	w := newWorld(t, section, nil)
	w.expectStatus(w.jst("focused", "Some.Test"), 0)
	w.expectCounters("x-build", 1)
	w.expectLast("outcome=exited-0 (focused, ", "x-build=exited-0, focused=exited-0")
	if names, err := os.ReadFile(filepath.Join(w.side, "focused-names")); err != nil || strings.TrimSpace(string(names)) != `^Some\.Test(/|$)` {
		t.Fatalf("the names focused got: %q, %v", names, err)
	}
	w.expectStatus(w.jst("plan", "focused", "Some.Test"), 0)
	if plan := w.stdout.String(); !strings.Contains(plan, "x-build: sh -c") || !strings.Contains(plan, `Some\.Test`) {
		t.Fatalf("the plan:\n%s", plan)
	}
}

func TestAFailureEndsTheRunAndKeepGoingRunsWhatDoesNotDependOnIt(t *testing.T) {
	section := map[string]any{"judge": "exit", "scopes": map[string]any{
		"gate":    map[string]any{"deps": argv("x-a", "x-other")},
		"x-a":     map[string]any{"run": counted("x-a", "true"), "deps": argv("x-lib")},
		"x-lib":   map[string]any{"run": counted("x-lib", `test ! -f "$SIDE/lib-fails"`)},
		"x-other": map[string]any{"run": counted("x-other", "true")},
	}}
	w := newWorld(t, section, nil)
	w.flag("lib-fails", true)
	w.expectStatus(w.jst("gate"), 1)
	w.expectCounters("x-lib x-a x-other", 1, 0, 0)
	w.expectLast("outcome=failed (gate failed at x-lib, exit 1", "x-a=not-run", "x-other=not-run", "gate=not-run")
	w.expectStatus(w.jst("gate", "--keep-going"), 1)
	w.expectCounters("x-lib x-a x-other", 2, 0, 1)
	w.expectLast("outcome=failed (gate failed at x-lib", "x-a=not-run", "x-other=exited-0", "gate=not-run")
}

func TestASkippedDepNeitherStopsTheRunNorFailsAComposite(t *testing.T) {
	w := newWhereWorld(t, "linux")
	w.expectStatus(w.jst("gate"), 0)
	w.expectLast("outcome=passed (gate, ", "x-ios=skipped")
	w.expectCounters("x-docs x-build x-go x-ios", 1, 1, 1, 0)
	w.expectStatus(w.jst("x-ios"), statusSkipped)
	w.expectLast("outcome=skipped")
	// A composite all of whose deps are skipped is skipped.
	w.configure(with(whereSection(), "scopes", "x-mobile", map[string]any{"deps": argv("x-ios")}), nil)
	w.expectStatus(w.jst("x-mobile"), statusSkipped)
	w.expectLast("outcome=skipped", "x-mobile=skipped")
	w.expectCounters("x-build x-ios", 1, 0)
}

func TestTheRunsOutcomeIsTheFirstInTheOrder(t *testing.T) {
	line := func(name, outcome string, status int) step {
		return step{"step": name, "outcome": outcome, "status": status}
	}
	for _, each := range []struct {
		lines  []step
		want   string
		status int
	}{
		{[]step{line("a", "nothing-run", 76), line("b", "failed", 0)}, "failed", 1},
		{[]step{line("a", "failed", 127)}, "failed", 127},
		{[]step{line("a", "timed-out", 124), line("b", "passed-on-retry", 78)}, "passed-on-retry", 78},
		{[]step{line("a", "unjudged", 77), line("b", "timed-out", 124)}, "timed-out", 124},
		{[]step{line("a", "stopped-waiting", 130), line("b", "stopped", 143)}, "stopped", 143},
		{[]step{line("a", "unjudged", 77), line("b", "stopped-waiting", 130)}, "stopped-waiting", 130},
		{[]step{line("a", "nothing-run", 76), line("b", "unjudged", 77)}, "unjudged", 77},
		{[]step{line("a", "gave-up", 75), line("b", "nothing-run", 76)}, "nothing-run", 76},
		{[]step{line("a", "not-started", 2), line("b", "gave-up", 75)}, "gave-up", 75},
		{[]step{line("a", "not-run", 0), line("b", "not-started", 2)}, "not-started", 2},
		{[]step{line("a", "skipped", 0)}, "skipped", statusSkipped},
		{[]step{line("b", "passed", 0), line("a", "exited-0", 0)}, "exited-0", 0},
		{[]step{line("b", "exited-0", 0), line("a", "passed", 0)}, "passed", 0},
	} {
		state := &gateState{steps: each.lines}
		outcomes := map[string]string{}
		for _, entry := range each.lines {
			outcomes[textOf(entry["step"])] = textOf(entry["outcome"])
		}
		state.settle([]string{"a"}, outcomes)
		if state.outcome != each.want || state.status != each.status {
			t.Errorf("%v: %s %d, want %s %d", each.lines, state.outcome, state.status, each.want, each.status)
		}
	}
}

func TestChangedSplitsBetweenHereCIAndElsewhere(t *testing.T) {
	// A path no rule matches selects gate. On a Mac: x-ios runs here, after x-build from the ci group.
	mac := newWhereWorld(t, "darwin")
	mac.repo.Commit("change", map[string]string{"Sources/b.swift": "let b = 2\n"})
	mac.expectStatus(mac.jst("changed", "--base", "main"), 0)
	mac.expectLast("outcome=passed (changed, ", "here: x-build=exited-0, x-ios=passed; ci: x-docs, x-go; elsewhere: none")
	mac.expectCounters("x-build x-ios x-go x-docs", 1, 1, 0, 0)
	// On Linux nothing runs here; x-ios needs another machine.
	linux := newWhereWorld(t, "linux")
	linux.repo.Commit("change", map[string]string{"Sources/b.swift": "let b = 2\n"})
	linux.expectStatus(linux.jst("changed", "--base", "main"), statusNeedsOtherSystem)
	linux.expectLast("outcome=needs-other-system (darwin: x-ios; changed, ", "here: none; ci: x-docs, x-build, x-go")
	linux.expectCounters("x-build x-ios x-go x-docs", 0, 0, 0, 0)
	// A change only to docs/ selects x-docs, which CI covers.
	docs := newWhereWorld(t, "linux")
	docs.repo.Commit("change", map[string]string{"docs/a.md": "a\n"})
	docs.expectStatus(docs.jst("changed", "--base", "main"), 0)
	docs.expectLast("outcome=left-to-ci (changed, ", "here: none; ci: x-docs; elsewhere: none")
	// The plan shows the same split: no command of x-docs, which the run leaves to CI.
	docs.expectStatus(docs.jst("plan", "changed", "--base", "main"), 0)
	if plan := docs.stdout.String(); strings.Contains(plan, "x-docs: sh") || !strings.Contains(plan, "here: none\nleft to CI: x-docs\nelsewhere: none\n") {
		t.Fatalf("the plan:\n%s", plan)
	}
	// On a Mac the plan lists x-ios with its dep x-build, and leaves the rest to CI.
	mac.expectStatus(mac.jst("plan", "changed", "--base", "main"), 0)
	if plan := mac.stdout.String(); !strings.Contains(plan, "x-build: sh") || !strings.Contains(plan, "x-ios: sh") ||
		strings.Contains(plan, "x-go: sh") || !strings.Contains(plan, "here: x-ios\nleft to CI: x-docs, x-go\n") {
		t.Fatalf("the plan on a Mac:\n%s", plan)
	}
	// needs-other-system comes only after here passed: a failure here is the outcome.
	section := with(whereSection(), "scopes", "gate", map[string]any{"deps": argv("x-docs", "x-go", "x-ios", "x-lint")})
	section = with(section, "scopes", "x-lint", counting("x-lint", nil))
	linux.configure(section, nil)
	linux.commit()
	linux.expectStatus(linux.jst("changed", "--base", "main"), statusNeedsOtherSystem)
	linux.expectLast("here: x-lint=passed")
	linux.flag("x-lint-fails", true)
	linux.expectStatus(linux.jst("changed", "--base", "main"), 1)
	linux.expectLast("outcome=failed (changed failed at x-lint")
}

// Only `changed` splits: `auto` runs the scope it selects, also one that CI covers.
func TestAutoRunsTheSelectedScopeWithoutTheSplit(t *testing.T) {
	w := newWhereWorld(t, "linux")
	w.repo.Commit("change", map[string]string{"docs/a.md": "a\n"})
	w.expectStatus(w.jst("auto", "--base", "main"), 0)
	w.expectLast("outcome=passed")
	w.expectCounters("x-docs", 1)
	if last := w.lastLine(); strings.Contains(last, "left-to-ci") {
		t.Fatalf("auto split: %s", last)
	}
}

// A selected composite that cannot run here still leads to its tests: one that only another
// system runs, and no CI covers, needs another machine; it is never left to CI.
func TestChangedFindsWhatOnlyAnotherSystemRunsBehindACompositeThatCannotRunHere(t *testing.T) {
	section := with(whereSection(), "scopes", "x-mobile", map[string]any{"deps": argv("x-ios"), "systems": argv("darwin")})
	section["selection"] = map[string]any{"order": argv("x-mobile", "x-docs", "gate"),
		"rules": []any{map[string]any{"glob": "ios/**", "scope": "x-mobile"}, map[string]any{"glob": "docs/**", "scope": "x-docs"}}}
	linux := newWorld(t, section, nil)
	linux.system = "linux"
	linux.repo.Commit("change", map[string]string{"ios/a.swift": "let a = 1\n"})
	linux.expectStatus(linux.jst("changed", "--base", "main"), statusNeedsOtherSystem)
	linux.expectLast("outcome=needs-other-system (darwin: x-ios; changed, ", "here: none; ci: none")
	linux.expectCounters("x-build x-ios", 0, 0)
	linux.expectStatus(linux.jst("plan", "changed", "--base", "main"), 0)
	if plan := linux.stdout.String(); !strings.Contains(plan, "here: none\nleft to CI: none\nelsewhere: darwin: x-ios\n") {
		t.Fatalf("the plan:\n%s", plan)
	}
}

// A portable test that only a selected macOS-only composite reaches is not run here by that
// selection: it needs the system whose walk reaches it.
func TestChangedSendsAPortableTestReachedOnlyThroughAnotherSystemElsewhere(t *testing.T) {
	section := with(whereSection(), "scopes", "x-mac", map[string]any{"deps": argv("x-smoke"), "systems": argv("darwin")})
	section = with(section, "scopes", "x-smoke", counting("x-smoke", nil))
	section["selection"] = map[string]any{"order": argv("x-mac", "x-docs", "gate"),
		"rules": []any{map[string]any{"glob": "mac/**", "scope": "x-mac"}, map[string]any{"glob": "docs/**", "scope": "x-docs"}}}
	linux := newWorld(t, section, nil)
	linux.system = "linux"
	linux.repo.Commit("change", map[string]string{"mac/a.swift": "let a = 1\n"})
	linux.expectStatus(linux.jst("changed", "--base", "main"), statusNeedsOtherSystem)
	linux.expectLast("outcome=needs-other-system (darwin: x-smoke; changed, ", "here: none; ci: none")
	linux.expectCounters("x-smoke", 0)
}

func TestUncoveredRunsWhatCIDoesNotCoverOnThisSystemOnACleanCopy(t *testing.T) {
	// The focused scope needs test names: it is left out.
	section := with(whereSection(), "scopes", "focused", map[string]any{"run": argv("sh", "-c", "echo {tests}")})
	mac := newWorld(t, section, nil)
	mac.system = "darwin"
	mac.expectStatus(mac.jst(uncoveredCommand), 0)
	mac.expectSaid("clean copy of")
	mac.expectLast("outcome=passed (uncovered, ", "here: x-docs=passed, x-build=exited-0, x-go=passed, x-ios=passed; ci: none")
	if record := mac.lastRecord("gate"); record.Text("pinned_candidate") != mac.repo.Git("rev-parse", "HEAD") || record.Text("system") != "darwin" {
		t.Fatalf("the record: %v", record)
	}
	if err := UncoveredPassed(mac.repo.Root, "HEAD", "darwin"); err != nil {
		t.Fatalf("install after a passed uncovered: %v", err)
	}
	linux := newWorld(t, section, nil)
	linux.system = "linux"
	linux.expectStatus(linux.jst(uncoveredCommand), 0)
	linux.expectLast("outcome=left-to-ci (uncovered, ", "here: none; ci: x-docs, x-build, x-go; elsewhere: darwin: x-ios")
	linux.expectCounters("x-docs x-build x-go x-ios", 0, 0, 0, 0)
	if record := linux.lastRecord("gate"); record.Text("outcome") != outcomeLeftToCI || record.Text("pinned_candidate") == "" {
		t.Fatalf("the record: %v", record)
	}
	if err := UncoveredPassed(linux.repo.Root, "HEAD", "linux"); err != nil {
		t.Fatalf("install after left-to-ci: %v", err)
	}
	linux.repo.Commit("later", map[string]string{"Sources/c.swift": "let c = 3\n"})
	if err := UncoveredPassed(linux.repo.Root, "HEAD", "linux"); err == nil || !strings.Contains(err.Error(), "jst test uncovered") {
		t.Fatalf("install of a commit with no record: %v", err)
	}
}

// A node that needs a --base, which `jst test uncovered` takes none of, is left out also when a
// selected composite reaches it as a dep, and so is a node with `run` over it.
func TestUncoveredLeavesOutADepThatNeedsABase(t *testing.T) {
	section := with(whereSection(), "scopes", "x-based", map[string]any{"run": counted("x-based", "echo {base}"), "judge": "exit"})
	section = with(section, "scopes", "x-after", map[string]any{"run": counted("x-after", "true"), "judge": "exit",
		"deps": argv("x-based")})
	section = with(section, "scopes", "gate", map[string]any{"deps": argv("x-docs", "x-go", "x-ios", "x-based", "x-after")})
	w := newWorld(t, section, nil)
	w.system = "darwin"
	w.expectStatus(w.jst(uncoveredCommand), 0)
	w.expectLast("outcome=passed (uncovered, ", "here: x-docs=passed, x-build=exited-0, x-go=passed, x-ios=passed; ci: none")
	w.expectCounters("x-based x-after", 0, 0)
}

func TestCIRunsEveryScopeCoveredOnThisSystemOnceInGraphOrder(t *testing.T) {
	w := newWhereWorld(t, "linux")
	w.expectStatus(w.jst(ciCommand), 0)
	w.expectCounters("x-docs x-build x-go x-ios", 1, 1, 1, 0)
	w.expectLast("outcome=passed (ci, ", "x-docs=passed, x-build=exited-0, x-go=passed")
	if gate := w.lastRecord("gate"); gate.Text("scope") != ciCommand || gate.Text("system") != "linux" {
		t.Fatalf("the record: %v", gate)
	}
	mac := newWhereWorld(t, "darwin")
	mac.expectStatus(mac.jst(ciCommand), 2)
	mac.expectSaid("nothing to run here")
	if CIJobName("linux") != "jst test ci (linux)" {
		t.Fatal(CIJobName("linux"))
	}
}

// A project that says nothing of where its scopes run keeps `jst test changed` as it was.
func TestChangedWithoutCIOrSystemsRunsTheSelectedScope(t *testing.T) {
	w := newWorld(t, selectionSection(), nil)
	w.repo.Commit("change", map[string]string{"Sources/a.swift": "let a = 2\n"})
	w.expectStatus(w.jst("changed", "--base", "main"), 0)
	if got := w.lastRecord("run").Text("scope"); got != "full" || len(w.records(receipts.EventGate)) != 0 {
		t.Fatalf("ran %q", got)
	}
}
