package judge

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestJUnitReadsPytestCasesAndEmptySelection(t *testing.T) {
	cases := []struct {
		name, xml string
		count     int
		skipped   int
		outcome   Outcome
	}{
		{"pass and skip", `<?xml version="1.0" encoding="utf-8"?>
<testsuites><testsuite name="pytest" errors="0" failures="0" skipped="1" tests="2">
<testcase classname="test_sample" name="test_pass" time="0.000" />
<testcase classname="test_sample" name="test_skip" time="0.000">
<skipped type="pytest.skip" message="example">example</skipped></testcase>
</testsuite></testsuites>`, 1, 1, Passed},
		{"no tests collected", `<?xml version="1.0" encoding="utf-8"?>
<testsuites><testsuite name="pytest" errors="0" failures="0" skipped="0" tests="0" /></testsuites>`,
			0, 0, NothingRun},
		{"all skipped", `<testsuite name="pytest" tests="1" skipped="1">
<testcase classname="x" name="skip"><skipped /></testcase></testsuite>`, 0, 1, NothingRun},
		{"failure and setup error", `<testsuites tests="2" failures="1" errors="1">
<testsuite name="pytest" tests="2" failures="1" errors="1">
<testcase name="f"><failure message="assertion"/></testcase>
<testcase name="e"><error message="setup"/></testcase>
</testsuite></testsuites>`, 2, 0, Failed},
		{"nested suites", `<testsuite name="outer" tests="2" skipped="1">
<testsuite name="a" tests="1"><testcase classname="C" name="same"/></testsuite>
<testsuite name="b" tests="1" skipped="1"><testcase classname="C" name="same"><skipped/></testcase></testsuite>
</testsuite>`, 1, 1, Passed},
	}
	for _, sample := range cases {
		t.Run(sample.name, func(t *testing.T) {
			facts := readJUnit(strings.NewReader(sample.xml))
			if len(facts.Unjudged) != 0 || facts.Kind != JUnit ||
				!facts.Counts.Counting || facts.Counts.Counted != sample.count ||
				facts.Counts.Skipped != sample.skipped {
				t.Fatalf("facts %+v", facts)
			}
			verdict := Evaluate(facts, End{}, ScopePolicy)
			if verdict.Outcome != sample.outcome {
				t.Fatalf("verdict %+v", verdict)
			}
		})
	}
}

func TestJUnitRefusesAmbiguousOrIncompleteReports(t *testing.T) {
	invalid := map[string]string{
		"empty":                 "",
		"truncated":             `<testsuites><testsuite name="a"><testcase name="x"/>`,
		"unsupported root":      `<coverage><testcase name="x"/></coverage>`,
		"second root":           `<testsuite name="a"/><testsuite name="b"/>`,
		"doctype":               `<!DOCTYPE testsuite [<!ENTITY x "test">]><testsuite name="x"/>`,
		"entity":                `<testsuite name="x"><testcase name="&unknown;"/></testsuite>`,
		"count mismatch":        `<testsuite name="x" tests="2"><testcase name="a"/></testsuite>`,
		"invalid count":         `<testsuite name="x" tests="-1"/>`,
		"duplicate count":       `<testsuite tests="0" tests="0"/>`,
		"duplicate case":        `<testsuite name="x"><testcase name="a"/><testcase name="a"/></testsuite>`,
		"conflicting outcome":   `<testsuite name="x"><testcase name="a"><failure/><skipped/></testcase></testsuite>`,
		"unknown case outcome":  `<testsuite name="x"><testcase name="a"><xfail/></testcase></testsuite>`,
		"missing case name":     `<testsuite name="x"><testcase/></testsuite>`,
		"unexpected case child": `<testsuite name="x"><testcase name="a"><testcase name="b"/></testcase></testsuite>`,
		"unexpected text":       `<testsuite name="x">surprise</testsuite>`,
		"namespaced root":       `<x:testsuite xmlns:x="urn:other"/>`,
		"processing instruction": `<?xml version="1.0"?>
<testsuite name="x"><?other processing?></testsuite>`,
	}
	for name, report := range invalid {
		t.Run(name, func(t *testing.T) {
			facts := readJUnit(strings.NewReader(report))
			if len(facts.Unjudged) == 0 || facts.Failure != "" ||
				facts.Counts.Counted != 0 ||
				Evaluate(facts, End{}, ScopePolicy).Outcome != Unjudged {
				t.Fatalf("invalid report accepted: %+v", facts)
			}
		})
	}
}

func TestJUnitBoundsSizeDepthAndNumberOfCases(t *testing.T) {
	deep := strings.Repeat("<testsuite>", junitDepthLimit+1) +
		strings.Repeat("</testsuite>", junitDepthLimit+1)
	overLimit := "<testsuite>" + strings.Repeat(" ", junitReportLimit) + "</testsuite>"
	var many strings.Builder
	many.WriteString("<testsuite>")
	for index := 0; index <= junitCaseLimit; index++ {
		many.WriteString(`<testcase name="case-`)
		many.WriteString(strconv.Itoa(index))
		many.WriteString(`"/>`)
	}
	many.WriteString("</testsuite>")
	for name, report := range map[string]string{
		"depth": deep, "bytes": overLimit, "cases": many.String(),
	} {
		t.Run(name, func(t *testing.T) {
			facts := readJUnit(strings.NewReader(report))
			if len(facts.Unjudged) == 0 || facts.Counts.Counted != 0 {
				t.Fatalf("limit not enforced: %+v", facts)
			}
		})
	}
}

type failingJUnitReader struct{}

func (failingJUnitReader) Read([]byte) (int, error) {
	return 0, errors.New("sensitive data from report path")
}

func TestJUnitDoesNotExposeReadDiagnostics(t *testing.T) {
	facts := readJUnit(failingJUnitReader{})
	if len(facts.Unjudged) == 0 || strings.Contains(facts.Unjudged[0], "sensitive") {
		t.Fatalf("read error leaked report diagnostics: %+v", facts)
	}
	if verdict := Evaluate(facts, End{Status: 1}, ScopePolicy); verdict.Outcome != Failed {
		t.Fatalf("process failure precedence changed: %+v", verdict)
	}
	if verdict := Evaluate(facts, End{Signal: "SIGTERM"}, ScopePolicy); verdict.Outcome != Stopped {
		t.Fatalf("stop precedence changed: %+v", verdict)
	}
}

var _ io.Reader = failingJUnitReader{}
