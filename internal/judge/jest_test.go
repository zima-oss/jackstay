package judge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJestReports(t *testing.T) {
	for _, sample := range []struct {
		name             string
		want             Outcome
		counted, skipped int
	}{
		{"pass", Passed, 1, 2},
		{"fail", Failed, 1, 0},
		{"zero", NothingRun, 0, 3},
		{"collect", Unjudged, 0, 0},
	} {
		t.Run(sample.name, func(t *testing.T) {
			report, err := os.Open(filepath.Join("testdata", "jest", sample.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			defer report.Close()
			verdict := Evaluate(readJest(report), End{}, ScopePolicy)
			if verdict.Outcome != sample.want || verdict.Counted != sample.counted || verdict.Skipped != sample.skipped {
				t.Fatalf("got %+v, want %s (%d/%d)", verdict, sample.want, sample.counted, sample.skipped)
			}
		})
	}
}

func TestJestRejectsIncompleteAndContradictoryReports(t *testing.T) {
	passed, err := os.ReadFile(filepath.Join("testdata", "jest", "pass.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		"{}", "[]", string(passed[:len(passed)/2]),
		strings.Replace(string(passed), `"numPassedTests": 1`, `"numPassedTests": 2`, 1),
		strings.Replace(string(passed), `"success": true`, `"success": true, "success": true`, 1),
	} {
		if outcome := Evaluate(readJest(strings.NewReader(input)), End{}, ScopePolicy).Outcome; outcome != Unjudged {
			t.Errorf("got %s for an invalid report", outcome)
		}
	}
}

func TestJestRetriedAssertionIsPassedOnRetry(t *testing.T) {
	for _, sample := range []struct {
		file   string
		status string
		want   Outcome
	}{
		{file: "pass", status: "passed", want: PassedOnRetry},
		{file: "pass", status: "pending", want: Unjudged},
		{file: "fail", status: "failed", want: Failed},
	} {
		content, err := os.ReadFile(filepath.Join("testdata", "jest", sample.file+".json"))
		if err != nil {
			t.Fatal(err)
		}
		input := strings.Replace(string(content), `"status": "`+sample.status+`"`,
			`"status": "`+sample.status+`", "invocations": 2`, 1)
		if input == string(content) {
			t.Fatalf("%s fixture lacks %s assertion", sample.file, sample.status)
		}
		verdict := Evaluate(readJest(strings.NewReader(input)), End{}, ScopePolicy)
		if verdict.Outcome != sample.want {
			t.Fatalf("%s retry: %+v, want %s", sample.file, verdict, sample.want)
		}
	}
}

func TestJestSelectionWitnessesExactFileAndAssertion(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "jest", "pass.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Replace(string(fixture), `"assertionResults":`, `"name": "/work/run.test.js", "assertionResults":`, 1)
	raw = strings.Replace(raw, `"status": "passed"`, `"status": "passed", "fullName": "suite literal [1]"`, 1)
	for _, each := range []struct {
		selection JestSelection
		want      Outcome
	}{
		{JestSelection{File: "/work/run.test.js", Name: "suite literal [1]"}, Passed},
		{JestSelection{File: "/work/elsewhere.test.js", Name: "suite literal [1]"}, Unjudged},
		{JestSelection{File: "/work/run.test.js", Name: "suite literal 1"}, Unjudged},
	} {
		if verdict := Evaluate(readJestSelected(strings.NewReader(raw), &each.selection), End{}, ScopePolicy); verdict.Outcome != each.want {
			t.Errorf("selection %+v: %s, want %s", each.selection, verdict.Outcome, each.want)
		}
	}
	selected := JestSelection{File: "/work/run.test.js", Name: "suite literal [1]"}
	duplicate := strings.Replace(raw, `"status": "pending"`, `"status": "pending", "fullName": "suite literal [1]"`, 1)
	if verdict := Evaluate(readJestSelected(strings.NewReader(duplicate), &selected), End{}, ScopePolicy); verdict.Outcome != Unjudged {
		t.Fatalf("ambiguous assertion: %+v", verdict)
	}
}
