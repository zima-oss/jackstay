package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fixtureDefects(t *testing.T) Defects {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "defects.json"))
	if err != nil {
		t.Fatal(err)
	}
	var defects Defects
	if err := json.Unmarshal(data, &defects); err != nil {
		t.Fatal(err)
	}
	return defects
}

func TestTheMatcherProposesByMechanismInPlaceAndTitle(t *testing.T) {
	defects := fixtureDefects(t)
	if len(defects.Defects) != 10 || len(defects.Ranges) != 3 {
		t.Fatalf("the fixture has %d defects over %d ranges", len(defects.Defects), len(defects.Ranges))
	}
	measurement := bench{defects: defects}
	run := Run{Range: "cache"}
	for finding, want := range map[Finding]string{
		{Place: "cache.go: Get", Title: "Get reads the map without the mutex"}:                                      "B1",
		{Place: "cache.go: Cache.Get", Title: "The expiry comparison is inverted"}:                                  "B2",
		{Place: "stock.go: Release", Title: "Release can drive Reserved negative"}:                                  "B3",
		{Place: "README.md", Title: "Wording"}:                                                                      "",
		{Place: "order.go: ParseLine", Title: "index out of range"}:                                                 "",
		{Place: "cache.go: NewCache / Put", Title: "Inserting into a zero-value Cache panics", Detail: "nil clock"}: "E13",
		{Place: "cache.go: Get", Title: "Expired cache entries accumulate without bound"}:                           "E12",
		{Place: "cache.go: Get", Title: "Another problem", Detail: "the comparison is inverted"}:                    "",
	} {
		if got := measurement.propose(run, finding); got != want {
			t.Errorf("%q proposed %q, want %q (another range's defect, a detail-only word, clock for lock earn nothing)", finding.Title, got, want)
		}
	}
}

func TestOnlyAPersonsLabelsCount(t *testing.T) {
	measurement := bench{defects: fixtureDefects(t)}
	run := Run{Arm: "codex-jst", Range: "cache", Iteration: 1, Findings: []Finding{
		{Place: "cache.go: Get", Title: "Get reads the map without the mutex"},
		{Place: "cache.go: Get", Title: "Another race in Get"},
		{Place: "cache.go: Put", Title: "Expired entries are never evicted"},
		{Place: "cache.go: Get", Title: "Something that is not so"},
		{Place: "stock.go: Release", Title: "Not looked at yet"},
	}}
	measurement.labels = map[string]string{
		labelKey(run, 0, run.Findings[0]): "B1", labelKey(run, 1, run.Findings[1]): "B1",
		labelKey(run, 2, run.Findings[2]): "E12", labelKey(run, 3, run.Findings[3]): "false",
	}
	if err := measurement.checkLabels([]Run{run}); err != nil {
		t.Fatal(err)
	}
	measurement.score(&run)
	if !slices.Equal(run.Found, []string{"B1"}) || run.Duplicates != 1 || run.Extras != 1 || run.False != 1 || run.Unjudged != 1 {
		t.Fatalf("score: %+v", run)
	}
	for _, wrong := range []string{"B22", "A1", "", "E99"} {
		measurement.labels[labelKey(run, 4, run.Findings[4])] = wrong
		if err := measurement.checkLabels([]Run{run}); err == nil {
			t.Errorf("label %q was taken for a cache finding", wrong)
		}
	}
	twin := Run{Arm: "codex-jst", Range: "cache", Iteration: 1, Findings: []Finding{run.Findings[0], run.Findings[0]}}
	if labelKey(twin, 0, twin.Findings[0]) == labelKey(twin, 1, twin.Findings[1]) {
		t.Fatal("two findings with one place and title share a label")
	}
}

func TestADirectAnswerIsReadAsTheWrappedArmReadsIt(t *testing.T) {
	if _, err := complete([]byte(`{"verdict":"approve","summary":"clear","findings":null,"prior":[]}`)); err == nil {
		t.Fatal("findings null was taken")
	}
	if _, err := complete([]byte(`{"verdict":"incomplete","summary":"no view","findings":[],"prior":[]}`)); err == nil {
		t.Fatal("an incomplete verdict was taken")
	}
	if _, err := complete([]byte(`{"verdict":"approve","summary":"clear","findings":[],"prior":[]}`)); err != nil {
		t.Fatalf("a complete answer was refused: %v", err)
	}
}

func TestEveryRangeOfTheFixtureHasItsFiles(t *testing.T) {
	defects := fixtureDefects(t)
	for _, defect := range append(defects.Defects, defects.Extras...) {
		step := ""
		for _, part := range defects.Ranges {
			if part.Name == defect.Range {
				step = part.Step
			}
		}
		source, err := os.ReadFile(filepath.Join("testdata", "fixture", step, defect.File))
		if err != nil {
			source, err = os.ReadFile(filepath.Join("testdata", "fixture", "step1", defect.File))
		}
		if err != nil || !strings.Contains(string(source), defect.Symbol) {
			t.Errorf("%s: %s of %s does not hold %s (%v)", defect.ID, defect.File, step, defect.Symbol, err)
		}
	}
}

func TestProposalsNeverCountUntilAPersonMovesThem(t *testing.T) {
	folder := t.TempDir()
	run := Run{Arm: "codex-jst", Range: "cache", Iteration: 1, Outcome: "answered", Findings: []Finding{
		{Place: "cache.go: Get", Title: "Get reads the map without the mutex"}}}
	line, _ := json.Marshal(run)
	path := filepath.Join(folder, "runs.jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	measurement := bench{log: io.Discard, proposeLabels: true}
	inRepositoryRoot(t)
	if err := measurement.rescore(path, []string{"codex-jst"}); err != nil {
		t.Fatal(err)
	}
	proposed, _ := os.ReadFile(filepath.Join(folder, "runs.proposed.json"))
	report, _ := os.ReadFile(filepath.Join(folder, "runs.md"))
	if !strings.Contains(string(proposed), `"B1"`) || !strings.Contains(string(report), "| codex-jst | 1/1 | 1 |") {
		t.Fatalf("a proposal counted, or was not written:\n%s\n%s", proposed, report)
	}
}

// inRepositoryRoot moves the test into the repository's root, where rescore reads the fixture.
func inRepositoryRoot(t *testing.T) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
}

func TestTheDirectClaudeArmGetsNoEffortButTheOneAsked(t *testing.T) {
	kept := directClaudeEnvironment([]string{"PATH=/bin", "CLAUDE_EFFORT=low", "CLAUDE_CODE_EFFORT_LEVEL=high", "ANTHROPIC_MODEL=x"})
	if !slices.Equal(kept, []string{"PATH=/bin", "ANTHROPIC_MODEL=x"}) {
		t.Fatalf("environment: %v", kept)
	}
}
