package judge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures preserve Surefire 3.5.4's TestNG XML structure. Host properties,
// durations and failure stack details are reduced to stable representative values. The flaky
// and rerun fixtures are Surefire 3.5.4's JUnit 5 reports with `surefire.rerunFailingTestsCount`
// (1, and 2 for rerun-mixed), whose suite totals count only the last rerun.
func TestJUnitReadsSurefireReports(t *testing.T) {
	for _, sample := range []struct {
		name    string
		status  int
		counted int
		skipped int
		retried int
		outcome Outcome
	}{
		{name: "pass", counted: 1, skipped: 1, outcome: Passed},
		{name: "fail", status: 1, counted: 1, outcome: Failed},
		{name: "skipped", skipped: 1, outcome: NothingRun},
		{name: "flaky", counted: 2, retried: 1, outcome: PassedOnRetry},
		{name: "rerun-fail", status: 1, counted: 1, outcome: Failed},
		{name: "rerun-mixed", status: 1, counted: 4, skipped: 1, retried: 1, outcome: Failed},
	} {
		t.Run(sample.name, func(t *testing.T) {
			report, err := os.Open(filepath.Join("testdata", "surefire", sample.name+".xml"))
			if err != nil {
				t.Fatal(err)
			}
			defer report.Close()
			facts := readJUnit(report)
			if len(facts.Unjudged) != 0 || !facts.Counts.Counting ||
				facts.Counts.Counted != sample.counted || facts.Counts.Skipped != sample.skipped || facts.Retried != sample.retried {
				t.Fatalf("facts %+v", facts)
			}
			if verdict := Evaluate(facts, End{Status: sample.status}, ScopePolicy); verdict.Outcome != sample.outcome {
				t.Fatalf("verdict %+v", verdict)
			}
		})
	}
}

func TestJUnitRefusesContradictorySurefireRerunRecords(t *testing.T) {
	report := func(records string) string {
		return `<testsuite name="s" tests="1" failures="0" errors="0" skipped="0"><testcase name="t" classname="c">` +
			records + `</testcase></testsuite>`
	}
	for name, sample := range map[string]struct {
		input, why string
	}{
		"flaky and failed":       {report(`<failure/><flakyFailure><stackTrace>x</stackTrace></flakyFailure>`), "conflicting"},
		"flaky and skipped":      {report(`<skipped/><flakyError/>`), "conflicting"},
		"flaky and rerun":        {report(`<flakyFailure/><rerunFailure/>`), "conflicting"},
		"rerun without failure":  {report(`<rerunError><stackTrace>x</stackTrace></rerunError>`), "did not fail"},
		"unknown record content": {report(`<flakyFailure><failure/></flakyFailure>`), "unsupported element"},
		"record outside a case":  {`<testsuite name="s"><flakyFailure/></testsuite>`, "unsupported element"},
	} {
		t.Run(name, func(t *testing.T) {
			facts := readJUnit(strings.NewReader(sample.input))
			verdict := Evaluate(facts, End{}, ScopePolicy)
			if verdict.Outcome != Unjudged || !strings.Contains(verdict.Why, sample.why) {
				t.Fatalf("verdict %+v", verdict)
			}
		})
	}
}

func TestJUnitKeepsCheckingTotalsOfSuitesWithoutReruns(t *testing.T) {
	input := `<testsuites><testsuite name="reran" tests="0"><testcase name="t" classname="c"><flakyFailure/></testcase></testsuite>` +
		`<testsuite name="plain" tests="2"><testcase name="u" classname="c"/></testsuite></testsuites>`
	if verdict := Evaluate(readJUnit(strings.NewReader(input)), End{}, ScopePolicy); verdict.Outcome != Unjudged ||
		!strings.Contains(verdict.Why, "totals disagree") {
		t.Fatalf("verdict %+v", verdict)
	}
}
