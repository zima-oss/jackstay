package judge

import (
	"os"
	"path/filepath"
	"testing"
)

// The fixtures preserve the structure emitted by Vitest 5.1.1's JUnit reporter.
// Timestamp, host and durations are fixed because they are unrelated to the verdict.
func TestJUnitReadsVitestReports(t *testing.T) {
	for _, sample := range []struct {
		name    string
		status  int
		counted int
		skipped int
		outcome Outcome
	}{
		{name: "pass", counted: 1, skipped: 1, outcome: Passed},
		{name: "fail", status: 1, counted: 1, outcome: Failed},
		{name: "empty", counted: 0, skipped: 2, outcome: NothingRun},
		{name: "zero", counted: 0, skipped: 0, outcome: NothingRun},
	} {
		t.Run(sample.name, func(t *testing.T) {
			report, err := os.Open(filepath.Join("testdata", "vitest", sample.name+".xml"))
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
