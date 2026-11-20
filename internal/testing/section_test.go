package scopes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/receipts"
)

// sectionProblems are a `test` section's problems as jackstay.json's reader and the meaning checks find
// them (the types first: a section of the wrong types is not read further).
func sectionProblems(t *testing.T, test, receiptsSection map[string]any) []string {
	t.Helper()
	if receiptsSection == nil {
		receiptsSection = map[string]any{"toolchain": []any{}}
	}
	data, err := json.Marshal(map[string]any{"jackstay_schema": 3, "test": test, "receipts": receiptsSection})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := config.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if found := config.SectionProblems(parsed).Of("test"); len(found) > 0 {
		return found
	}
	return Problems(parsed.Section("test"))
}

func goodSection() map[string]any {
	return map[string]any{
		"judge": "swift-test", "timeout": 1800,
		"leftovers": []any{map[string]any{"glob": "~/Library/Preferences/ExampleTests.*.plist", "fail": true},
			map[string]any{"glob": "$TMPDIR/{run_id}-*", "fail": false}},
		"scopes": map[string]any{
			"focused": map[string]any{"run": argv("swift", "test", "--filter", "{tests}"), "timeout": 600, "when": "while working"},
			"changed": map[string]any{"run": argv("python3", "-I", "Scripts/select.py", "--run", "--base", "{base}"), "base": "origin/main",
				"judge": "steps", "evidence": "Executed ([0-9]+) tests?", "when": "before every code commit"},
			"quick":   map[string]any{"run": argv("swift", "test", "--skip", "{slow}"), "slow": map[string]any{"class_seconds": 20}},
			"full":    map[string]any{"run": argv("swift", "test", "--skip", `^ExampleTests\.Snapshot`), "timeout": 2400},
			"display": map[string]any{"run": argv("swift", "test", "--filter", `^ExampleTests\.Snapshot`), "requires": []any{map[string]any{"run": argv("Scripts/screen-ok.sh"), "why": "needs an unlocked 2x screen"}}},
			"gate":    map[string]any{"steps": argv("format", "docs", "build", "full"), "when": "before every pull request"},
		},
		"steps": map[string]any{
			"format": map[string]any{"run": argv("swift", "format", "lint", "--strict", "-r", "Sources"), "judge": "exit"},
			"docs":   map[string]any{"run": argv("python3", "-I", "Scripts/check_docs.py"), "judge": "exit"},
			"build":  map[string]any{"run": argv("swift", "build"), "judge": "exit"},
		},
	}
}

func TestAGoodSection(t *testing.T) {
	if found := sectionProblems(t, goodSection(), nil); len(found) != 0 {
		t.Fatalf("problems: %v", found)
	}
}

func TestBadSections(t *testing.T) {
	scope := func(name string, entry map[string]any) map[string]any {
		return map[string]any{"scopes": map[string]any{name: entry}}
	}
	cases := []struct {
		test   map[string]any
		wanted string
	}{
		{scope("fast", map[string]any{"run": argv("x")}), "a scope is one of"},
		{scope("x-cover", map[string]any{"run": argv("x")}), ""},
		{scope("full", map[string]any{"run": argv("x"), "steps": argv("a")}), "exactly one of"},
		{scope("full", map[string]any{"run": argv("x", "{what}")}), "{what}"},
		{scope("full", map[string]any{"run": argv("sh", "-c", "echo ${HOME} {X}", "--filter", "^A{2,3}$")}), ""},
		{scope("quick", map[string]any{"run": argv("x", "{slow}")}), "no `slow` rule"},
		{scope("quick", map[string]any{"run": argv("x"), "slow": map[string]any{"class_seconds": 2}}), "which its words do not hold"},
		{scope("changed", map[string]any{"run": argv("x"), "base": "main"}), "{base}, which its words do not hold"},
		{scope("full", map[string]any{"run": argv("swift", "test", "--parallel"), "judge": "swift-test"}), "--parallel"},
		{scope("full", map[string]any{"run": argv("swift", "test", "--parallel"), "judge": "exit"}), ""},
		{scope("changed", map[string]any{"run": argv("x"), "judge": "steps"}), "no `evidence`"},
		{scope("changed", map[string]any{"run": argv("x"), "judge": "steps", "evidence": "ran tests"}), "no group"},
		{scope("full", map[string]any{"run": argv("x"), "judge": "pytest"}), "must be one of"},
		{scope("full", map[string]any{"run": argv("x"), "judge": "junit"}), "single portable file name"},
		{scope("full", map[string]any{"run": argv("x", "{report}"), "judge": "junit", "report": "tests.xml", "empty_exit": 5}), ""},
		{scope("full", map[string]any{"run": argv("x"), "judge": "junit", "report": "../tests.xml"}), "single portable file name"},
		{scope("full", map[string]any{"run": argv("x"), "judge": "junit", "report": "tests.xml", "empty_exit": 126}), "from 1 to 125"},
		{scope("full", map[string]any{"run": argv("x", "{report}"), "judge": "go-json"}), "only in arguments to a file-based producer"},
		{scope("full", map[string]any{"run": argv("x"), "judge": "go-json", "evidence": "ran ([0-9]+)"}), "cannot be combined"},
		{map[string]any{"scopes": map[string]any{"gate": map[string]any{"steps": argv("a")}},
			"steps": map[string]any{"a": map[string]any{"steps": argv("gate")}}}, "circle"},
		{scope("gate", map[string]any{"steps": argv("nope")}), "neither a step nor a scope"},
		{map[string]any{"scopes": map[string]any{"gate": map[string]any{"steps": argv("focused")},
			"focused": map[string]any{"run": argv("t", "{tests}")}}}, "{tests}"},
		{map[string]any{"scopes": map[string]any{"full": map[string]any{"run": argv("x")}},
			"steps": map[string]any{"full": map[string]any{"run": argv("y")}}}, "both a scope and a step"},
		{map[string]any{"steps": map[string]any{"bad name": map[string]any{"run": argv("y")}}}, "a step's name"},
		{map[string]any{"leftovers": []any{map[string]any{"glob": "/tmp/{who}", "fail": true}}}, "{who}"},
		{map[string]any{"leftovers": []any{map[string]any{"glob": "/tmp/x"}}}, "fail` is missing"},
		{map[string]any{"leftovers": []any{map[string]any{"fail": true}}}, "glob` is missing"},
		{scope("full", map[string]any{"run": argv("x"), "requires": []any{map[string]any{"run": argv("y")}}}), "why` is missing"},
		{scope("full", map[string]any{"run": argv("x"), "requires": []any{map[string]any{"why": "y"}}}), "run` is missing"},
		{scope("quick", map[string]any{"run": argv("x"), "judge": "swift-test", "durations": true}), "part of a class"},
		{scope("full", map[string]any{"run": argv("x"), "judge": "swift-test", "expect": argv("junit")}), "not an engine"},
		{scope("full", map[string]any{"run": argv("x"), "expect": argv("xctest")}), "is for the `swift-test` judge"},
		{map[string]any{"expect": []any{}, "scopes": map[string]any{}}, "names no engine"},
		{scope("full", map[string]any{"run": argv("x"), "env": map[string]any{"JACKSTAY_RUN_ID": "1"}}), "jackstay's own"},
		{scope("full", map[string]any{"run": argv("x"), "env": map[string]any{"1BAD": "1"}}), "not the name of an environment variable"},
		{scope("full", map[string]any{"run": argv("x"), "cwd": "../other"}), "inside the checkout"},
		{scope("full", map[string]any{"run": argv("x"), "unknown": 1}), "unknown key"},
		{scope("full", map[string]any{"run": argv("x"), "timeout": 0}), "above 0"},
		{scope("full", map[string]any{"run": []any{}}), "names no command"},
		{scope("gate", map[string]any{"steps": []any{}}), "names no step"},
		{map[string]any{"scopes": map[string]any{"gate": map[string]any{"steps": argv("full"), "requires": []any{map[string]any{"run": argv("y")}}},
			"full": map[string]any{"run": argv("x")}}}, "why` is missing"},
		{map[string]any{"scopes": map[string]any{"gate": map[string]any{"steps": argv("full"), "timeout": 5},
			"full": map[string]any{"run": argv("x")}}}, "each step has its own"},
		{map[string]any{"judge": "pytest", "scopes": map[string]any{}}, "`test.judge` must be one of"},
	}
	for _, each := range cases {
		found := sectionProblems(t, each.test, nil)
		if each.wanted == "" {
			if len(found) > 0 {
				t.Errorf("%v: %v", each.test, found)
			}
			continue
		}
		if !strings.Contains(strings.Join(found, "\n"), each.wanted) {
			t.Errorf("%v: %v does not say %q", each.test, found, each.wanted)
		}
	}
}

func TestAGateNeedsTheToolchainWrittenOut(t *testing.T) {
	if found := sectionProblems(t, goodSection(), map[string]any{}); !strings.Contains(strings.Join(found, " "), "receipts.toolchain") {
		t.Fatalf("problems: %v", found)
	}
	if found := sectionProblems(t, map[string]any{"scopes": map[string]any{"full": map[string]any{"run": argv("x")}}}, map[string]any{}); len(found) != 0 {
		t.Fatalf("problems: %v", found)
	}
}

func TestLeafPathsAndResolve(t *testing.T) {
	section := goodSection()
	paths, err := leafPaths(section, "gate", nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, path := range paths {
		names = append(names, path.name+"/"+path.kind)
	}
	if strings.Join(names, ",") != "format/step,docs/step,build/step,full/scope" {
		t.Fatalf("leaves: %v", names)
	}
	full := resolve(section, "full", kindScope, objectOf(objectOf(section["scopes"])["full"]))
	if full.timeout != 2400 || full.lockWait != 2400 || !full.lock || !full.durations || full.judge != "swift-test" ||
		strings.Join(full.expect, ",") != "xctest" || full.scope != "full" || !full.resume {
		t.Fatalf("full: %+v", full)
	}
	quick := resolve(section, "quick", kindScope, objectOf(objectOf(section["scopes"])["quick"]))
	if quick.lock || quick.durations || quick.timeout != 1800 || !quick.slow.hasClassSeconds || quick.slow.hasCaseSeconds {
		t.Fatalf("quick: %+v", quick)
	}
	format := resolve(section, "format", kindStep, objectOf(objectOf(section["steps"])["format"]))
	if format.scope != "" || format.judge != "exit" || format.durations || !format.lock {
		t.Fatalf("format: %+v", format)
	}
	display := resolve(section, "display", kindScope, objectOf(objectOf(section["scopes"])["display"]))
	if len(display.requires) != 1 || display.requires[0].timeout != 60 {
		t.Fatalf("display: %+v", display)
	}
}

func TestNoDurationsSkipNothing(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	classes, note := slowClasses("demo", slowRule{classSeconds: 1, hasClassSeconds: true})
	if len(classes) != 0 || note != "quick=everything (no durations yet)" || slowPattern(classes) != nothing {
		t.Fatalf("%v %q", classes, note)
	}
}

func TestMediansOverWholeClassesOnly(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	clock := newClock()
	whole := map[string]float64{"t1": 10, "t2": 15}
	for index := range 3 {
		if err := recordDurations("demo", "r"+string(rune('0'+index)), "full", "h",
			map[string]map[string]float64{"T.Slow": whole, "T.Fast": {"t": 0.1}}, clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// A run that held part of the class (a filter) is not counted, however fast it was.
	if err := recordDurations("demo", "part", "full", "h", map[string]map[string]float64{"T.Slow": {"t1": 0.01}}, clock.Now()); err != nil {
		t.Fatal(err)
	}
	runs, err := readDurations("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got := classMedians(runs)["T.Slow"]; got != (classMedian{total: 25, slowest: 15, runs: 3}) {
		t.Fatalf("T.Slow: %+v", got)
	}
	classes, note := slowClasses("demo", slowRule{classSeconds: 20, hasClassSeconds: true})
	if len(classes) != 1 || classes[0].name != "T.Slow" || note != "" {
		t.Fatalf("%v %q", classes, note)
	}
	pattern := regexp.MustCompile(slowPattern(classes))
	if !pattern.MatchString("T.Slow/t1") || pattern.MatchString("T.SlowToo/t1") {
		t.Fatalf("pattern %s", pattern)
	}
	if found, _ := slowClasses("demo", slowRule{caseSeconds: 12, hasCaseSeconds: true}); len(found) != 1 || found[0].name != "T.Slow" {
		t.Fatalf("by case: %v", found)
	}
	if found, _ := slowClasses("demo", slowRule{caseSeconds: 16, hasCaseSeconds: true}); len(found) != 0 {
		t.Fatalf("by case: %v", found)
	}
}

func TestOnlyTheLastRunsAreKept(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	clock := newClock()
	for index := range keepDurationRuns + 5 {
		if err := recordDurations("demo", "r"+strings.Repeat("x", index), "full", "h",
			map[string]map[string]float64{"T.A": {"t": float64(index)}}, clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := readDurations("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != keepDurationRuns || runs[len(runs)-1].RunID != "r"+strings.Repeat("x", keepDurationRuns+4) {
		t.Fatalf("kept %d, the last %s", len(runs), runs[len(runs)-1].RunID)
	}
	place, _ := durationsPath("demo")
	data, _ := os.ReadFile(place)
	if lines := strings.Count(string(data), "\n"); lines != keepDurationRuns {
		t.Fatalf("the file holds %d lines", lines)
	}
	if err := recordDurations("demo", "none", "full", "h", map[string]map[string]float64{"T.A": {}}, clock.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestAnUnreadableDurationsFileSkipsNothingAndSaysSo(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	place, _ := durationsPath("demo")
	if err := os.MkdirAll(place, 0o700); err != nil {
		t.Fatal(err)
	}
	classes, note := slowClasses("demo", slowRule{classSeconds: 1, hasClassSeconds: true})
	if len(classes) != 0 || !strings.Contains(note, "cannot be read") {
		t.Fatalf("%v %q", classes, note)
	}
	if err := recordDurations("demo", "r", "full", "h", map[string]map[string]float64{"T.A": {"t": 1}}, newClock().Now()); err == nil {
		t.Fatal("a durations file that is a folder was written")
	}
}

func quickSection() map[string]any {
	return map[string]any{"judge": "swift-test", "timeout": 600, "scopes": map[string]any{
		"full": map[string]any{"run": cat("pkg-filtered-xctest")},
		"quick": map[string]any{"run": argv("sh", "-c", `echo "$1" > "$SIDE/skip"; cat "$SIDE/pkg-filtered-xctest.log"`, "sh", "{slow}"),
			"slow": map[string]any{"case_seconds": 0.01}},
	}}
}

func TestQuickSkipsWhatFullRunsMeasuredAsSlow(t *testing.T) {
	w := newWorld(t, quickSection(), nil)
	w.expectStatus(w.jst("quick"), 0)
	w.expectLast("quick=everything (no durations yet)")
	if w.read("skip") != nothing {
		t.Fatalf("skipped %s", w.read("skip"))
	}
	place, _ := durationsPath("demo")
	if _, err := os.Stat(place); err == nil {
		t.Fatal("a quick run recorded durations")
	}
	w.expectStatus(w.jst("full"), 0)
	runs, err := readDurations("demo")
	if err != nil || len(runs) != 1 {
		t.Fatalf("%v %v", runs, err)
	}
	if tests := runs[0].Classes["OtherTests.AlphaTests"].Tests; strings.Join(tests, ",") != "testExpected,testOne" || runs[0].Scope != "full" {
		t.Fatalf("the durations: %+v", runs[0])
	}
	w.expectStatus(w.jst("quick"), 0)
	w.expectLast("quick=skipped OtherTests.AlphaTests")
	if w.read("skip") != `^(OtherTests\.AlphaTests)(/|$)` {
		t.Fatalf("skipped %s", w.read("skip"))
	}
	if run := w.lastRecord("run"); run.Text("quick") != "skipped OtherTests.AlphaTests" {
		t.Fatalf("the record: %v", run)
	}
	w.expectStatus(w.jst("plan", "quick"), 0)
	if !strings.Contains(w.stdout.String(), "skipped: OtherTests.AlphaTests") {
		t.Fatalf("plan:\n%s", w.stdout.String())
	}
}

func reuseSection() map[string]any {
	return map[string]any{"judge": "swift-test", "timeout": 600, "scopes": map[string]any{
		"full": map[string]any{"run": counted("full", `cat "$SIDE/pkg-filtered-xctest.log"`)}}}
}

func reuseReceipts() map[string]any {
	return map[string]any{"receipts": map[string]any{"inputs": argv("Sources"), "toolchain": []any{}, "env": []any{},
		"commands": []any{map[string]any{"argv": counted("full", `cat "$SIDE/pkg-filtered-xctest.log"`), "reuse": true,
			"evidence": "Executed ([0-9]+) tests?"}}}}
}

func TestANewLeftoversRuleIsANewKey(t *testing.T) {
	w := newWorld(t, reuseSection(), reuseReceipts())
	w.commit()
	w.expectStatus(w.jst("full"), 0)
	if run := w.lastRecord("run"); run["stored"] != true {
		t.Fatalf("no receipt: %v", run)
	}
	w.expectStatus(w.jst("full"), 0)
	w.expectLast("outcome=reused")
	w.expectCounters("full", 1)
	if reused := w.lastRecord("reused"); reused.Text("lookup") != "under-lock" {
		t.Fatalf("the reused line: %v", reused)
	}
	section := reuseSection()
	section["leftovers"] = []any{map[string]any{"glob": w.at("nothing-*.plist"), "fail": true}}
	w.configure(section, reuseReceipts())
	w.commit() // jackstay.json is not an input here: only the scope's definition can tell the runs apart
	w.expectStatus(w.jst("full"), 0)
	w.expectCounters("full", 2)
	// --no-reuse runs it, and a gate never reuses.
	w.expectStatus(w.jst("full", "--no-reuse"), 0)
	w.expectCounters("full", 3)
}

func TestReuseIsRefusedOnMain(t *testing.T) {
	w := newWorld(t, reuseSection(), reuseReceipts())
	w.commit()
	w.repo.Git("checkout", "-q", "main")
	w.repo.Git("merge", "-q", "--ff-only", "work")
	w.expectStatus(w.jst("full"), 0)
	w.expectStatus(w.jst("full"), 0)
	w.expectCounters("full", 2)
	if hit := w.lastRecord("would-hit"); !strings.Contains(hit.Text("reason"), "the branch is main") {
		t.Fatalf("the would-hit line: %v", hit)
	}
	w.repo.Git("checkout", "-q", "--detach", "main")
	w.expectStatus(w.jst("full"), 0)
	if hit := w.lastRecord("would-hit"); !strings.Contains(hit.Text("reason"), "HEAD is the commit main points at") {
		t.Fatalf("the would-hit line: %v", hit)
	}
	w.repo.Git("checkout", "-q", "work")
	w.env[receipts.NoReuseVariable] = "1"
	w.expectStatus(w.jst("full"), 0)
	w.expectCounters("full", 4)
}

// A forced run (--no-reuse or JACKSTAY_NO_REUSE) still measures the repeat: its would-hit says
// `forced`, it reuses nothing, and the statistics count it apart from the savings reuse would make.
func TestAForcedRunMeasuresTheRepeatAndReusesNothing(t *testing.T) {
	w := newWorld(t, reuseSection(), reuseReceipts())
	w.commit()
	w.expectStatus(w.jst("full"), 0)
	w.expectStatus(w.jst("full", "--no-reuse"), 0)
	w.expectCounters("full", 2)
	if hit := w.lastRecord("would-hit"); hit["forced"] != true || hit.Text("reason") != "--no-reuse" || hit["receipt"] != true {
		t.Fatalf("the would-hit line of --no-reuse: %v", hit)
	}
	w.env[receipts.NoReuseVariable] = "1"
	w.expectStatus(w.jst("full"), 0)
	w.expectCounters("full", 3)
	if hit := w.lastRecord("would-hit"); hit["forced"] != true || !strings.Contains(hit.Text("reason"), receipts.NoReuseVariable) {
		t.Fatalf("the would-hit line of %s: %v", receipts.NoReuseVariable, hit)
	}
	delete(w.env, receipts.NoReuseVariable)
	w.expectStatus(w.jst("full"), 0)
	w.expectLast("outcome=reused")
	w.expectCounters("full", 3)
	tally := receipts.Summarise(w.records(""))["demo"]
	if tally == nil || tally.Forced != 2 || tally.WouldHit != 0 || tally.Reused != 1 {
		t.Fatalf("the statistics: %+v", tally)
	}
}

// A scope whose words are a receipts.commands entry without "reuse": true takes no part in
// receipts: a repeat logs no would-hit (the raw form `jst test --` still does).
func TestAScopeNotOptedInLogsNoWouldHit(t *testing.T) {
	more := reuseReceipts()
	more["receipts"].(map[string]any)["commands"].([]any)[0].(map[string]any)["reuse"] = false
	w := newWorld(t, reuseSection(), more)
	w.commit()
	w.expectStatus(w.jst("full"), 0)
	w.expectStatus(w.jst("full"), 0)
	w.expectCounters("full", 2)
	if hits := w.records("would-hit"); len(hits) != 0 {
		t.Fatalf("would-hit lines: %v", hits)
	}
}

func TestPrerequisiteTimeoutDefaultsToAConstant(t *testing.T) {
	entry := map[string]any{"requires": []any{
		map[string]any{"run": []any{"true"}, "why": "default"},
		map[string]any{"run": []any{"true"}, "why": "override", "timeout": json.Number("7")},
	}}
	checks := checksOf(entry)
	if len(checks) != 2 || checks[0].timeout != config.TestRequiresTimeoutSeconds || checks[1].timeout != 7 {
		t.Fatalf("unexpected timeouts: %+v", checks)
	}
}

// A scope whose project's receipts cannot be written runs nothing, though the run log can be (#59).
func TestAScopeThatCannotRecordItsEvidenceDoesNotStart(t *testing.T) {
	w := newWorld(t, reuseSection(), reuseReceipts())
	w.commit()
	receiptsFolder := filepath.Join(w.repo.Home, homedir.MarkerName("demo"), "receipts")
	if err := os.MkdirAll(receiptsFolder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(receiptsFolder, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(receiptsFolder, 0o700) })
	w.expectStatus(w.jst("full"), statusRefused)
	w.expectSaid("Run it outside the sandbox.")
	w.expectCounters("full", 0)
}
