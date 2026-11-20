package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestScoringMatchesAFindingToTheDefectItNames(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "defects.json"))
	if err != nil {
		t.Fatal(err)
	}
	var defects Defects
	if err := json.Unmarshal(data, &defects); err != nil {
		t.Fatal(err)
	}
	if len(defects.Defects) != 10 || len(defects.Ranges) != 3 {
		t.Fatalf("the fixture has %d defects over %d ranges", len(defects.Defects), len(defects.Ranges))
	}
	measurement := bench{defects: defects}
	run := Run{Range: "cache", Findings: []Finding{
		{Place: "cache.go: Get", Title: "Get reads the map without the mutex", Detail: "a data race with Put"},
		{Place: "cache.go: Cache.Get", Title: "The expiry comparison is inverted", Detail: "expired entries are returned"},
		{Place: "cache.go: Get", Title: "Another race in Get", Detail: "concurrent map read without lock"},
		{Place: "stock.go: Release", Title: "Release can drive Reserved negative", Detail: "no check of count"},
		{Place: "README.md", Title: "Wording", Detail: "a typo"},
		{Place: "order.go: ParseLine", Title: "index out of range", Detail: "off-by-one"},
		{Place: "stock.go: ReserveLines", Title: "Reserving lines races with Release", Detail: "no lock"},
	}}
	measurement.score(&run)
	if !slices.Equal(run.Found, []string{"B1", "B2", "B3"}) || run.Duplicates != 1 || run.Unmatched != 2 || run.Extras != 1 {
		t.Fatalf("found %v, %d duplicates, %d unmatched (a defect of another range does not count)", run.Found, run.Duplicates, run.Unmatched)
	}
}

func TestEveryRangeOfTheFixtureHasItsFiles(t *testing.T) {
	data, _ := os.ReadFile(filepath.Join("testdata", "defects.json"))
	var defects Defects
	if err := json.Unmarshal(data, &defects); err != nil {
		t.Fatal(err)
	}
	for _, defect := range defects.Defects {
		step := ""
		for _, part := range defects.Ranges {
			if part.Name == defect.Range {
				step = part.Step
			}
		}
		source, err := os.ReadFile(filepath.Join("testdata", "fixture", step, defect.File))
		if err != nil || !strings.Contains(string(source), defect.Symbol) {
			t.Errorf("%s: %s of %s does not hold %s (%v)", defect.ID, defect.File, step, defect.Symbol, err)
		}
	}
}

func TestAFindingAboutAnotherIssueOfTheSameCodeEarnsNoSeededDefect(t *testing.T) {
	data, _ := os.ReadFile(filepath.Join("testdata", "defects.json"))
	var defects Defects
	if err := json.Unmarshal(data, &defects); err != nil {
		t.Fatal(err)
	}
	measurement := bench{defects: defects}
	run := Run{Range: "cache", Findings: []Finding{
		{Place: "cache.go: NewCache / Put", Title: "Inserting into a zero-value Cache panics", Detail: "a nil clock and a nil map"},
		{Place: "cache.go: Get", Title: "Expired cache entries accumulate without bound", Detail: "nothing evicts expired entries"},
	}}
	measurement.score(&run)
	if len(run.Found) != 0 || run.Extras != 2 || run.Findings[0].Match != "E13" || run.Findings[1].Match != "E12" {
		t.Fatalf("found %v, matches %+v (clock is not lock, eviction is not the inverted test)", run.Found, run.Findings)
	}
	measurement.labels = map[string]string{labelKey(run, run.Findings[1]): "false"}
	measurement.score(&run)
	if run.False != 1 || run.Extras != 1 {
		t.Fatalf("a person's label did not override the matcher: %+v", run)
	}
}
