package main

import (
	"encoding/json"
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
		{Place: "cache.go: Get", Title: "Get reads the map without the mutex"}:                                    "B1",
		{Place: "cache.go: Cache.Get", Title: "The expiry comparison is inverted"}:                                "B2",
		{Place: "stock.go: Release", Title: "Release can drive Reserved negative"}:                                "B3",
		{Place: "README.md", Title: "Wording"}:                                                                     "",
		{Place: "order.go: ParseLine", Title: "index out of range"}:                                                "",
		{Place: "cache.go: NewCache / Put", Title: "Inserting into a zero-value Cache panics", Detail: "nil clock"}: "E13",
		{Place: "cache.go: Get", Title: "Expired cache entries accumulate without bound"}:                         "E12",
		{Place: "cache.go: Get", Title: "Another problem", Detail: "the comparison is inverted"}:                  "",
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
		labelKey(run, run.Findings[0]): "B1", labelKey(run, run.Findings[1]): "B1",
		labelKey(run, run.Findings[2]): "E12", labelKey(run, run.Findings[3]): "false",
	}
	measurement.score(&run)
	if !slices.Equal(run.Found, []string{"B1"}) || run.Duplicates != 1 || run.Extras != 1 || run.False != 1 || run.Unjudged != 1 {
		t.Fatalf("score: %+v", run)
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
