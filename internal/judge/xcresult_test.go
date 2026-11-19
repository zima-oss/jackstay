package judge

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/testreport"
)

func xcresultFixture(t *testing.T, name string) ([]byte, []byte) {
	t.Helper()
	read := func(part string) []byte {
		contents, err := os.ReadFile(filepath.Join("testdata", "xcresult", name+"-"+part+".json"))
		if err != nil {
			t.Fatal(err)
		}
		return contents
	}
	return read("summary"), read("tests")
}

func TestXCResultCountsFailedParameterizedArgument(t *testing.T) {
	summaryJSON, testsJSON := xcresultFixture(t, "pass")
	var summary xcresultSummary
	var tree xcresultTree
	if err := json.Unmarshal(summaryJSON, &summary); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(testsJSON, &tree); err != nil {
		t.Fatal(err)
	}
	one, four, five := 1, 4, 5
	summary.Result = "Failed"
	summary.Passed, summary.Failed = &four, &one
	summary.DevicesAndConfigurations[0].Passed = &five
	summary.DevicesAndConfigurations[0].Failed = &one
	plan := &tree.TestNodes[0]
	modern := &plan.Children[0]
	parameterized := &modern.Children[2]
	plan.Result, modern.Result, parameterized.Result = "Failed", "Failed", "Failed"
	parameterized.Children[0].Result = "Failed"
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	testsJSON, err = json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	facts, _, _ := ReadXCResult(bytes.NewReader(summaryJSON), bytes.NewReader(testsJSON))
	if verdict := Evaluate(facts, End{}, ScopePolicy); verdict.Outcome != Failed || verdict.Counted != 5 || verdict.Skipped != 2 {
		t.Fatalf("parameterized failure verdict: %+v", verdict)
	}
}

func TestXCResultMatchesBothSwiftEngines(t *testing.T) {
	for _, test := range []struct {
		name string
		want Outcome
	}{
		{"pass", Passed},
		{"failed", Failed},
	} {
		t.Run(test.name, func(t *testing.T) {
			summary, tests := xcresultFixture(t, test.name)
			facts, durations, truncated := ReadXCResult(bytes.NewReader(summary), bytes.NewReader(tests))
			verdict := Evaluate(facts, End{}, ScopePolicy)
			if verdict.Outcome != test.want || verdict.Counted != 5 || verdict.Skipped != 2 || len(durations) != 5 || truncated {
				t.Fatalf("verdict %+v, duration count %d, truncated %v", verdict, len(durations), truncated)
			}
			for _, sample := range durations {
				if !strings.HasPrefix(sample.Test, "test://") || sample.Seconds < 0 {
					t.Fatalf("unusable sample %+v", sample)
				}
			}
		})
	}
}

func TestXCResultRefusesIncompleteOrContradictoryEvidence(t *testing.T) {
	summary, tests := xcresultFixture(t, "pass")
	changedArgument := regexp.MustCompile(`("nodeType": "Arguments",\s*"result": )"Passed"`).ReplaceAllString(string(tests), `$1"Failed"`)
	for _, test := range []struct {
		name, summary, tests string
		required             []string
	}{
		{"missing tree", string(summary), "", nil},
		{"changed total", strings.Replace(string(summary), `"totalTestCount": 7`, `"totalTestCount": 8`, 1), string(tests), nil},
		{"missing engine", string(summary), strings.ReplaceAll(string(tests), `"name": "LegacyTests",`, `"name": "RemovedTests",`), []string{"ModernTests", "LegacyTests"}},
		{"truncated tree", string(summary), string(tests[:len(tests)/2]), nil},
		{"duplicate case", string(summary), strings.Replace(string(tests), "LegacyTests/LegacyTests/testPass", "LegacyTests/LegacyTests/testOptionalFailure", 1), nil},
		{"parameter failed without parent", string(summary), changedArgument, nil},
		{"oversized document", string(summary), strings.Repeat(" ", xcresultDocumentLimit+1), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			facts, durations, _ := readXCResultForBundles(strings.NewReader(test.summary), strings.NewReader(test.tests), test.required)
			if len(facts.Unjudged) == 0 || len(durations) != 0 {
				t.Fatalf("accepted inconsistent Xcode result: %+v, %d durations", facts, len(durations))
			}
		})
	}
}

func TestXCResultReaderRequiresBothReportsAndBundles(t *testing.T) {
	summary, tests := xcresultFixture(t, "pass")
	folder := t.TempDir()
	report, err := testreport.OpenReports(folder, []string{"summary.json", "tests.json"})
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	if err := os.WriteFile(filepath.Join(folder, "summary.json"), summary, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := New(Options{Kind: XCResultJSON, XCResultBundles: []string{"ModernTests", "LegacyTests"}, XCResultDurations: true})
	if err != nil {
		t.Fatal(err)
	}
	reader.ReadReport(report)
	if verdict := reader.Verdict(End{}); verdict.Outcome != Unjudged {
		t.Fatalf("missing test tree: %+v", verdict)
	}
	if err := os.WriteFile(filepath.Join(folder, "tests.json"), tests, 0o600); err != nil {
		t.Fatal(err)
	}
	reader.ReadReport(report)
	if verdict := reader.Verdict(End{}); verdict.Outcome != Passed || verdict.Counted != 5 || verdict.Skipped != 2 {
		t.Fatalf("complete result: %+v", verdict)
	}
	if samples, _ := reader.XCResultDurations(); len(samples) != 5 {
		t.Fatalf("recorded %d timings, want 5", len(samples))
	}
	reader, err = New(Options{Kind: XCResultJSON, XCResultBundles: []string{"MissingTests"}})
	if err != nil {
		t.Fatal(err)
	}
	reader.ReadReport(report)
	if verdict := reader.Verdict(End{}); verdict.Outcome != Unjudged {
		t.Fatalf("missing bundle: %+v", verdict)
	}
}

func TestXCResultRequiresExpectedBundles(t *testing.T) {
	for _, options := range []Options{
		{Kind: XCResultJSON},
		{Kind: XCResultJSON, XCResultBundles: []string{"ModernTests", "ModernTests"}},
		{Kind: JUnit, XCResultBundles: []string{"ModernTests"}},
	} {
		if _, err := New(options); err == nil {
			t.Errorf("accepted unsupported options: %+v", options)
		}
	}
}
