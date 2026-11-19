package scopes

import (
	"slices"
	"testing"
)

func TestJestSelectionUsesAFileAndLiteralName(t *testing.T) {
	selected, err := parseJestSelection([]string{"tests/unit file.test.js::group brackets [1]"})
	if err != nil {
		t.Fatal(err)
	}
	got := jestSelectionArgs(selected)
	want := []string{`--testNamePattern=^group brackets \[1\]$`, "--runTestsByPath", "tests/unit file.test.js"}
	if !slices.Equal(got, want) {
		t.Fatalf("args %q, want %q", got, want)
	}
	for _, words := range [][]string{
		nil, {"../bad.test.js::name"}, {"--watch::test"}, {"{report}::name"},
		{"test.js::"}, {"test.js::first\nsecond"}, {"a.test.js::first", "b.test.js::second"},
	} {
		if _, err := parseJestSelection(words); err == nil {
			t.Errorf("accepted invalid ID %q", words)
		}
	}
}
