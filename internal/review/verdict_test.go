package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAnswer(t *testing.T) {
	dir := t.TempDir()
	write := func(text string) string {
		path := filepath.Join(dir, "answer.json")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if _, outcome, _ := ReadAnswer(filepath.Join(dir, "missing")); outcome != "no-answer" {
		t.Fatal(outcome)
	}
	if _, outcome, _ := ReadAnswer(write("  \n")); outcome != "no-answer" {
		t.Fatal(outcome)
	}
	for _, bad := range []string{`not json`, `{"verdict":"approve","summary":"","findings":[],"prior":[],"extra":1}`,
		`{"verdict":"approve","verdict":"changes","summary":"","findings":[],"prior":[]}`, `{} {}`,
		`{"verdict":"approve"}`, `{"verdict":"approve","summary":"","findings":null,"prior":[]}`,
		`{"verdict":"approve","summary":"","findings":[{"id":"F1"}],"prior":[]}`,
		`{"verdict":"approve","summary":"","findings":[],"prior":[{"id":"x"}]}`,
		`{"verdict":"approve","summary":null,"findings":[],"prior":[]}`} {
		if _, outcome, reason := ReadAnswer(write(bad)); outcome != "unparsable" || reason == "" {
			t.Errorf("%s: %s", bad, outcome)
		}
	}
	answer, outcome, _ := ReadAnswer(write(`{"verdict":"approve","summary":"s","findings":[],"prior":[]}`))
	if outcome != "done" || answer.Summary != "s" {
		t.Fatal(outcome)
	}
}

func TestCheckAnswer(t *testing.T) {
	s := testSettings()
	ledger := []LedgerFinding{{ID: "r1-codex-F1", Status: StatusOpen}, {ID: "r1-codex-F2", Status: StatusDeferred}}
	good := Answer{Verdict: "changes", Findings: []AnswerFinding{finding("F1", "p", "major", "ordinary", "mild", "r1-codex-F2")},
		Prior: []PriorAnswer{{ID: "r1-codex-F1", Status: "fixed"}}}
	if problem := CheckAnswer(good, s, ledger); problem != "" {
		t.Fatal(problem)
	}
	cases := map[string]Answer{
		"twice":           {Verdict: "approve", Findings: []AnswerFinding{finding("F1", "p", "major", "ordinary", "mild"), finding("F1", "q", "major", "ordinary", "mild")}, Prior: good.Prior},
		"severity":        {Verdict: "approve", Findings: []AnswerFinding{finding("F1", "p", "fatal", "ordinary", "mild")}, Prior: good.Prior},
		"not in the":      {Verdict: "approve", Findings: []AnswerFinding{finding("F1", "p", "major", "ordinary", "mild", "F9")}, Prior: good.Prior},
		"does not answer": {Verdict: "approve"},
		"not open":        {Verdict: "approve", Prior: []PriorAnswer{{ID: "r1-codex-F1", Status: "fixed"}, {ID: "r1-codex-F2", Status: "open"}}},
		"no place":        {Verdict: "approve", Findings: []AnswerFinding{finding("F1", " ", "major", "ordinary", "mild")}, Prior: good.Prior},
	}
	for want, answer := range cases {
		if problem := CheckAnswer(answer, s, ledger); !strings.Contains(problem, want) {
			t.Errorf("%s: %q", want, problem)
		}
	}
}

func TestSchemaIsStrict(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(Schema([]string{"high", "low"}), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false || len(schema["required"].([]any)) != 4 {
		t.Fatalf("%v", schema)
	}
	if !strings.Contains(string(Schema([]string{"high", "low"})), `"high"`) {
		t.Fatal("severities not in the schema")
	}
}
