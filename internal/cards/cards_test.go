package cards

import (
	"slices"
	"strings"
	"testing"
)

func rules(t *testing.T) Rules {
	t.Helper()
	r, err := New("docs/work/tasks", "APP-[0-9]{3,}")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCheck(t *testing.T) {
	r := rules(t)
	known := r.Known([]string{"docs/work/tasks/APP-140-empty-state.md", "docs/work/tasks/APP-141-x.md", "README.md", "docs/work/tasks/APP-1400.md"})
	for _, tc := range []struct {
		name    string
		message string
		rules   []string
	}{
		{"one ID", "Fix it\n\nCard: APP-140\n", nil},
		{"two IDs", "x\n\nCard: APP-140, APP-141", nil},
		{"lines add up", "x\nCard: APP-140\nCard: APP-141", nil},
		{"a card's path", "x\nCard: docs/work/tasks/APP-140-empty-state.md", nil},
		{"backquotes and a full stop", "x\nCard: `APP-140`.", nil},
		{"none with a reason", "x\nCard: none (handoff after the release)", nil},
		{"carriage returns", "x\r\nCard: APP-140\r\n", nil},
		{"after GitHub's paragraph", "x\n\nCard: APP-140\n\nCo-authored-by: a <a@b>", nil},
		{"no line", "x\n\nAPP-140", []string{NoLine}},
		{"not at the first column", "x\n  Card: APP-140", []string{NoLine}},
		{"an ID with no card", "x\nCard: APP-999", []string{Unknown}},
		{"a longer number is another card", "x\nCard: APP-1400", nil},
		{"words after it", "x\nCard: APP-140 and more", []string{Unreadable}},
		{"another project's card", "x\nCard: DEV-270", []string{Unreadable}},
		{"a path outside the folder", "x\nCard: docs/APP-140-x.md", []string{Unreadable}},
		{"empty", "x\nCard: ", []string{Unreadable}},
		{"none without a reason", "x\nCard: none", []string{NoReason}},
		{"none not alone", "x\nCard: none (a), APP-140", []string{NoReason}},
		{"each line judged", "x\nCard: APP-140\nCard: what\nCard: APP-998", []string{Unreadable, Unknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, f := range r.Check(tc.message, known) {
				got = append(got, f.Rule)
			}
			if !slices.Equal(got, tc.rules) {
				t.Fatalf("%q: got %v, want %v", tc.message, got, tc.rules)
			}
		})
	}
}

func TestIDOfFile(t *testing.T) {
	r := rules(t)
	for name, want := range map[string]string{
		"APP-140-a.md": "APP-140", "APP-140.md": "APP-140", "APP-1400-a.md": "APP-1400",
		"APP-14-a.md": "", "README.md": "", "xAPP-140.md": "",
	} {
		if got := r.IDOfFile(name); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

func TestNew(t *testing.T) {
	if _, err := New("d", "A-(?!x)"); err == nil {
		t.Fatal("a pattern RE2 does not take was accepted")
	}
	if _, err := FromSection(map[string]any{"id": "A-[0-9]+"}); err == nil {
		t.Fatal("a section without dir was accepted")
	}
}

func TestCardLineDecisions(t *testing.T) {
	rules, err := New("docs/tasks/", "APP-[0-9]{3,}")
	if err != nil {
		t.Fatal(err)
	}
	known := rules.Known([]string{"docs/tasks/APP-140-x.md", "docs/tasks/APP-141.md", "docs/tasks/APP-142", "other/APP-143-x.md", "docs/tasks/sub/APP-144-x.md"})
	if len(known) != 2 || !known["APP-140"] || !known["APP-141"] {
		t.Errorf("known: %v", known)
	}
	cases := []struct {
		message string
		rules   []string
	}{
		{"Card: APP-140", nil},
		{"Card: none (x).", nil},
		{"Card: none (a (b))", nil},
		{"Card: none()", []string{NoReason}},
		{"Card: none", []string{NoReason}},
		{"Card: ./docs/tasks/APP-140-x.md", nil},
		{"Card: `docs/tasks/APP-140-x.md`.", nil},
		{"Card: ` APP-140 `", nil},
		{"Card: APP-140,, APP-141", []string{Unreadable}},
		{"Card: APP-140, none (x)", []string{NoReason}},
		{"Card: APP-140\nCard: none (x)", []string{NoReason}},
		{"Card: APP-140, APP-140", nil},
		{"Card: APP-999", []string{Unknown}},
		{"Card: other/APP-140-x.md", []string{Unreadable}},
		{"Card: APP-140 and more", []string{Unreadable}},
		{"no line", []string{NoLine}},
	}
	for _, c := range cases {
		var got []string
		for _, finding := range rules.Check(c.message, known) {
			got = append(got, finding.Rule)
		}
		if strings.Join(got, ",") != strings.Join(c.rules, ",") {
			t.Errorf("%q: %v, want %v", c.message, got, c.rules)
		}
	}
	anywhere := rules.Anywhere("APP-140 APP-141,xAPP-142 APP-1430x -APP-144 APP-145.APP-146 APP-140")
	if strings.Join(anywhere, ",") != "APP-140,APP-141,APP-145,APP-146" {
		t.Errorf("anywhere: %v", anywhere)
	}
	if got := rules.Anywhere("backlog: APP-1400 merged, APP-150"); strings.Join(got, ",") != "APP-1400,APP-150" {
		t.Errorf("anywhere longest: %v", got)
	}
}
