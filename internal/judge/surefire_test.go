package judge

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixtures preserve Surefire 3.5.4's TestNG XML structure. Host properties,
// durations and failure stack details are reduced to stable representative values.
func TestJUnitReadsSurefireReports(t *testing.T) {
	for _, sample := range []struct {
		name    string
		status  int
		counted int
		skipped int
		outcome Outcome
	}{
		{name: "pass", counted: 1, skipped: 1, outcome: Passed},
		{name: "fail", status: 1, counted: 1, outcome: Failed},
		{name: "skipped", skipped: 1, outcome: NothingRun},
	} {
		t.Run(sample.name, func(t *testing.T) {
			report, err := os.Open(filepath.Join("testdata", "surefire", sample.name+".xml"))
			if err != nil {
				t.Fatal(err)
			}
			defer report.Close()
			facts := readJUnit(report)
			if len(facts.Unjudged) != 0 || !facts.Counts.Counting ||
				facts.Counts.Counted != sample.counted || facts.Counts.Skipped != sample.skipped {
				t.Fatalf("facts %+v", facts)
			}
			if verdict := Evaluate(facts, End{Status: sample.status}, ScopePolicy); verdict.Outcome != sample.outcome {
				t.Fatalf("verdict %+v", verdict)
			}
		})
	}
}
