package judge

import (
	"encoding/json"
	"strings"
	"testing"
)

func goEvent(action, pkg, test, output string) string {
	data, err := json.Marshal(goJSONEvent{Action: action, Package: pkg, Test: test, Output: output})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func readGoEvents(events ...string) Facts {
	var reader goJSONReader
	for _, event := range events {
		reader.line(event)
	}
	return reader.facts()
}

func TestGoJSONCountsExecutedLeavesAcrossInterleavedPackagesAndRepeatRuns(t *testing.T) {
	facts := readGoEvents(
		goEvent("start", "one", "", ""),
		goEvent("start", "two", "", ""),
		goEvent("run", "one", "TestParent", ""),
		goEvent("run", "two", "TestOther", ""),
		goEvent("run", "one", "TestParent/child", ""),
		goEvent("pass", "one", "TestParent/child", ""),
		goEvent("pass", "two", "TestOther", ""),
		goEvent("pass", "one", "TestParent", ""),
		goEvent("run", "one", "TestParent", ""),
		goEvent("run", "one", "TestParent/child", ""),
		goEvent("pass", "one", "TestParent/child", ""),
		goEvent("pass", "one", "TestParent", ""),
		goEvent("pass", "two", "", ""),
		goEvent("pass", "one", "", ""),
	)
	if facts.Counts.Counted != 3 || facts.Failure != "" || len(facts.Unjudged) != 0 {
		t.Fatalf("interleaved/repeated cases: %+v", facts)
	}
	if got := Evaluate(facts, End{}, ScopePolicy).Outcome; got != Passed {
		t.Fatalf("outcome %s", got)
	}
}

func TestGoJSONRefusesCacheAndIncompleteOrContradictoryEvidence(t *testing.T) {
	cases := []struct {
		name   string
		events []string
		want   Outcome
		reason string
	}{
		{"empty", []string{goEvent("start", "one", "", ""), goEvent("pass", "one", "", "")}, NothingRun, ""},
		{"skipped", []string{goEvent("start", "one", "", ""), goEvent("run", "one", "TestSkipped", ""),
			goEvent("skip", "one", "TestSkipped", ""), goEvent("pass", "one", "", "")}, NothingRun, ""},
		{"cached replay", []string{goEvent("start", "one", "", ""), goEvent("run", "one", "TestCase", ""),
			goEvent("pass", "one", "TestCase", ""), goEvent("output", "one", "", "ok  \tone\t(cached)\n"),
			goEvent("pass", "one", "", "")}, Unjudged, "cache"},
		{"unfinished package", []string{goEvent("start", "one", "", ""), goEvent("run", "one", "TestCase", ""),
			goEvent("pass", "one", "TestCase", "")}, Unjudged, "incomplete"},
		{"unfinished test", []string{goEvent("start", "one", "", ""), goEvent("run", "one", "TestCase", ""),
			goEvent("pass", "one", "", "")}, Unjudged, "unfinished"},
		{"malformed", []string{goEvent("start", "one", "", ""), `{"Action":"pass"`}, Unjudged, "decoded"},
		{"duplicate field", []string{goEvent("start", "one", "", ""), `{"Action":"pass","Action":"fail","Package":"one"}`}, Unjudged, "decoded"},
		{"unknown action", []string{goEvent("start", "one", "", ""), goEvent("surprise", "one", "", ""),
			goEvent("pass", "one", "", "")}, Unjudged, "unknown"},
		{"missing start", []string{goEvent("pass", "one", "", "")}, Unjudged, "outside"},
		{"skip contradicts executed tests", []string{goEvent("start", "one", "", ""),
			goEvent("run", "one", "TestCase", ""), goEvent("pass", "one", "TestCase", ""),
			goEvent("skip", "one", "", "")}, Unjudged, "skipped Go package"},
		{"failed case", []string{goEvent("start", "one", "", ""), goEvent("run", "one", "TestBad", ""),
			goEvent("fail", "one", "TestBad", ""), goEvent("fail", "one", "", "")}, Failed, ""},
		{"failed package", []string{goEvent("start", "one", "", ""), goEvent("fail", "one", "", "")}, Failed, ""},
		{"failed build", []string{`{"Action":"build-fail","ImportPath":"one"}`,
			goEvent("start", "one", "", ""), goEvent("fail", "one", "", "")}, Failed, ""},
		{"benchmark only", []string{goEvent("start", "one", "", ""), goEvent("bench", "one", "BenchmarkFast", ""),
			goEvent("pass", "one", "", "")}, NothingRun, ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			facts := readGoEvents(test.events...)
			verdict := Evaluate(facts, End{}, ScopePolicy)
			if verdict.Outcome != test.want || !strings.Contains(verdict.Why, test.reason) {
				t.Fatalf("facts %+v, verdict %+v", facts, verdict)
			}
		})
	}
}

func TestGoJSONBoundsStreamAndNames(t *testing.T) {
	reader := goJSONReader{bytes: maxGoJSONBytes}
	reader.line(goEvent("start", "one", "", ""))
	if !strings.Contains(strings.Join(reader.facts().Unjudged, " "), "limit") {
		t.Fatal("oversized stream was accepted")
	}
	facts := readGoEvents(goEvent("start", strings.Repeat("a", maxGoJSONName+1), "", ""))
	if !strings.Contains(strings.Join(facts.Unjudged, " "), "identity") {
		t.Fatal("oversized name was accepted")
	}
}
