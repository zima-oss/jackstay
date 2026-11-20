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
	b := bench{defects: defects}
	run := Run{Range: "cache", Findings: []Finding{
		{Place: "cache.go: Get", Title: "Get reads the map without the mutex", Detail: "a data race with Put"},
		{Place: "cache.go: Cache.Get", Title: "Expired entries are returned", Detail: "the comparison is inverted"},
		{Place: "cache.go: Get", Title: "Another race in Get", Detail: "concurrent map read without lock"},
		{Place: "stock.go: Release", Title: "Release can drive Reserved negative", Detail: "no check of count"},
		{Place: "README.md", Title: "Wording", Detail: "a typo"},
		{Place: "order.go: ParseLine", Title: "index out of range", Detail: "off-by-one"},
	}}
	b.score(&run)
	if !slices.Equal(run.Found, []string{"B1", "B2", "B3"}) || run.Duplicates != 1 || run.Unmatched != 2 {
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
