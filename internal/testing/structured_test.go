package scopes

import (
	"os"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/receipts"
)

func TestJUnitNeedsEveryDeclaredReport(t *testing.T) {
	const first = `<testsuite name="one" tests="1"><testcase name="first" time="0.125"/></testsuite>`
	const second = `<testsuite name="two" tests="1"><testcase name="second" time="0.25"/></testsuite>`
	scope := map[string]any{"judge": "junit", "reports": argv("One.xml", "Two.xml"), "durations": true,
		"run": argv("sh", "-c", `test "$1" = "$JACKSTAY_TEST_REPORT_DIR" &&
			printf '%s' "$2" > "$1/One.xml" &&
			{ test -f "$SIDE/missing" || {
				if test -f "$SIDE/overlap"; then printf '%s' "$2"; else printf '%s' "$3"; fi
			} > "$1/Two.xml"; }`, "runner", "{report_dir}", first, second)}
	world := newWorld(t, map[string]any{"scopes": map[string]any{"x-multi": scope}}, nil)
	world.expectStatus(world.jst("x-multi"), 0)
	world.expectLast("outcome=passed", "counted=2")
	samples := items(world.lastRecord(receipts.EventRun)["case_durations"])
	if len(samples) != 2 || objectOf(samples[0])["test"] != "first" || objectOf(samples[1])["test"] != "second" {
		t.Fatalf("expected both report timings, got %v", samples)
	}
	world.flag("overlap", true)
	world.expectStatus(world.jst("x-multi"), receipts.StatusUnjudged)
	world.expectLast("declared JUnit reports repeat a test case")
	if _, exists := world.lastRecord(receipts.EventRun)["case_durations"]; exists {
		t.Fatal("an unjudged set recorded timings")
	}
	world.flag("missing", true)
	world.expectStatus(world.jst("x-multi"), receipts.StatusUnjudged)
	world.expectLast("a declared test report is missing")
}

func TestStructuredScopeReusesWithoutRegexAndCleanCopyRunsAfresh(t *testing.T) {
	command := counted("report", `printf '%s' '<testsuite tests="1"><testcase name="case"/></testsuite>' > "$JACKSTAY_TEST_REPORT"`)
	section := map[string]any{"scopes": map[string]any{"full": map[string]any{
		"judge": "junit", "report": "tests.xml", "run": command,
	}}}
	more := map[string]any{"receipts": map[string]any{
		"inputs": argv("Sources"), "toolchain": []any{}, "env": []any{},
		"commands": []any{map[string]any{"argv": command, "judge": "junit", "report": "tests.xml", "reuse": true}},
	}}
	world := newWorld(t, section, more)
	world.commit()
	world.expectStatus(world.jst("full"), 0)
	world.expectStatus(world.jst("full"), 0)
	world.expectLast("outcome=reused")
	world.expectCounters("report", 1)
	world.expectStatus(world.jst("full", "--commit", "HEAD"), 0)
	world.expectLast("outcome=passed")
	world.expectCounters("report", 2)
}

func TestStructuredScopesRecordVerdictsAndUsePrivateReportPaths(t *testing.T) {
	const passing = `<testsuite tests="2" skipped="1" failures="0" errors="0"><testcase name="pass"/><testcase name="skip"><skipped/></testcase></testsuite>`
	const empty = `<testsuite tests="0" skipped="0" failures="0" errors="0"/>`
	scopes := map[string]any{
		"x-go-json": map[string]any{"judge": "go-json", "run": argv("sh", "-c", `printf '%s\n' "$@"`, "runner",
			`{"Action":"start","Package":"demo"}`, `{"Action":"run","Package":"demo","Test":"TestIt"}`,
			`{"Action":"pass","Package":"demo","Test":"TestIt"}`, `{"Action":"pass","Package":"demo"}`)},
	}
	for _, each := range []struct{ name, script, report string }{
		{"x-junit", `test "$1" = "$JACKSTAY_TEST_REPORT" || exit 9; printf '%s\n' "$1" >> "$SIDE/attempts"; printf '%s' "$2" > "$1"`, passing},
		{"x-empty", `printf '%s' "$2" > "$1"; exit 5`, empty},
		{"x-missing", `:`, "unused"},
		{"x-broken", `printf '%s' "$2" > "$1"`, "<testsuite>"},
		{"x-failed", `printf '%s' "$2" > "$1"`, `<testsuite tests="1"><testcase name="fails"><failure/></testcase></testsuite>`},
	} {
		scopes[each.name] = map[string]any{"judge": "junit", "report": "report.xml", "empty_exit": 5,
			"run": argv("sh", "-c", each.script, "runner", "{report}", each.report)}
	}
	world := newWorld(t, map[string]any{"scopes": scopes}, nil)
	for _, each := range []struct {
		name    string
		status  int
		outcome string
		count   float64
	}{
		{"x-go-json", 0, receipts.OutcomePassed, 1},
		{"x-junit", 0, receipts.OutcomePassed, 1},
		{"x-empty", receipts.StatusNothingRun, receipts.OutcomeNothingRun, 0},
		{"x-missing", receipts.StatusUnjudged, receipts.OutcomeUnjudged, 0},
		{"x-broken", receipts.StatusUnjudged, receipts.OutcomeUnjudged, 0},
		{"x-failed", 1, receipts.OutcomeFailed, 1},
	} {
		world.expectStatus(world.jst(each.name), each.status)
		record := world.lastRecord(receipts.EventRun)
		world.expectLast("outcome=" + each.outcome)
		if counted, _ := record.Number("counted"); record.Text("outcome") != each.outcome || counted != each.count {
			t.Fatalf("%s: %v", each.name, record)
		}
	}
	world.expectStatus(world.jst("x-junit", "--no-reuse"), 0)
	paths := strings.Fields(world.read("attempts"))
	if len(paths) != 2 || paths[0] == paths[1] {
		t.Fatalf("report paths %q", paths)
	}
	for _, name := range paths {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("report path survived the attempt: %s (%v)", name, err)
		}
	}
}

func TestAScopePassedOnRetryIsNoPassAndFailsTheGate(t *testing.T) {
	// The shape of Surefire's report for a test that failed once and passed on its rerun.
	const flaky = `<testsuite name="s" tests="1" failures="0" errors="0" skipped="0">` +
		`<testcase name="steady" classname="c"/>` +
		`<testcase name="flaky" classname="c"><flakyFailure message="first attempt"><stackTrace>trace</stackTrace></flakyFailure></testcase>` +
		`</testsuite>`
	run := argv("sh", "-c", `printf '%s' "$2" > "$1"`, "runner", "{report}", flaky)
	section := map[string]any{
		"scopes": map[string]any{
			"full": map[string]any{"judge": "junit", "report": "tests.xml", "run": run},
			"gate": map[string]any{"steps": argv("producer")},
		},
		"steps": map[string]any{"producer": map[string]any{"judge": "junit", "report": "tests.xml", "run": run}},
	}
	world := newWorld(t, section, nil)
	world.commit()
	world.expectStatus(world.jst("full"), receipts.StatusPassedOnRetry)
	world.expectLast("outcome=passed-on-retry", "1 test passed only on retry", "counted=2", "no receipt")
	if got := world.lastRecord(receipts.EventRun).Text("outcome"); got != receipts.OutcomePassedOnRetry {
		t.Fatalf("recorded outcome %q", got)
	}
	world.expectStatus(world.jst("full"), receipts.StatusPassedOnRetry)
	world.expectLast("outcome=passed-on-retry")
	world.expectStatus(world.jst("gate"), receipts.StatusPassedOnRetry)
	world.expectLast("passed-on-retry")
}
