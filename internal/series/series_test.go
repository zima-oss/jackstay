package series

import (
	"reflect"
	"regexp"
	"testing"
)

func TestTaken(t *testing.T) {
	cases := []struct {
		pattern, path string
		number        string
	}{
		{"docs/work/tasks/APP-{n}-*.md", "docs/work/tasks/APP-170-x.md", "170"},
		{"docs/work/tasks/APP-{n}-*.md", "docs/work/tasks/APP-170.md", "170"},
		{"docs/work/tasks/APP-{n}-*.md", "docs/work/tasks/APP-170", "170"},
		{"docs/work/tasks/APP-{n}-*.md", "docs/work/tasks/APP-170-x.md.orig", "170"},
		{"docs/work/tasks/APP-{n}-*.md", "docs/work/tasks/APP-17a", "17"},
		{"docs/work/tasks/APP-{n}-*.md", "docs/work/tasks/sub/APP-170.md", ""},
		{"docs/work/tasks/APP-{n}-*.md", "docs/work/APP-170.md", ""},
		{"docs/work/tasks/APP-{n}-*.md", "docs/work/tasks/XAPP-170.md", ""},
		{"docs/*-{n}-*.md", "docs/Q-12-x.md", "12"},
		{"Q-{n}.md", "Q-3.md", "3"},
		{"./Q-{n}.md", "Q-3.md", "3"},
		{"./docs//notes/./Q-{n}.md", "docs/notes/Q-3.md", "3"},
		{"a.b/Q-{n}.md", "aXb/Q-3.md", ""},
	}
	for _, c := range cases {
		taken, err := Taken(c.pattern)
		if err != nil {
			t.Fatal(err)
		}
		match := taken.FindStringSubmatch(c.path)
		got := ""
		if match != nil {
			got = match[1]
		}
		if got != c.number {
			t.Errorf("%s on %s: %q, want %q", c.pattern, c.path, got, c.number)
		}
	}
	for _, bad := range []string{"docs/x.md", "docs/{n}/{n}.md", "{n}/x-{n}.md"} {
		if _, err := Taken(bad); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestFormatNumberMentions(t *testing.T) {
	if got := Format("APP", 3, 7); got != "APP-007" {
		t.Error(got)
	}
	if got := Format("Q", 0, 12); got != "Q-12" {
		t.Error(got)
	}
	prefix, number, ok := NumberOf("APP-0140")
	if !ok || prefix != "APP-" || number != 140 {
		t.Error(prefix, number, ok)
	}
	if _, _, ok := NumberOf("APP-"); ok {
		t.Error("no number")
	}
	got, _ := Mentions("CHK", "| CHK-3 | x-CHK-9 | CHK-12a | CHK-40x | ALC-5 | CHK-77 |")
	if !reflect.DeepEqual(got, []int{3, 12, 40, 77}) {
		t.Error(got)
	}
	if plain, _ := Mentions("CHK", "CHK-1 CHK-22"); !reflect.DeepEqual(plain, []int{1, 22}) {
		t.Error("plain")
	}
}

func TestFrontMatter(t *testing.T) {
	matter, err := ReadFrontMatter("---\r\n{\"id\": \"\", \"tags\": [\"a\", 1.50], \"n\": {\"k\": true}}\r\n---\r\n# Body\nmore")
	if err != nil {
		t.Fatal(err)
	}
	if !matter.OneLine || matter.Body != "# Body\nmore" {
		t.Errorf("%+v", matter)
	}
	if err := matter.Set("id", "CHK-001"); err != nil {
		t.Fatal(err)
	}
	if err := matter.Set("card", "APP-1 <&>"); err != nil {
		t.Fatal(err)
	}
	want := "---\n{\"id\": \"CHK-001\", \"tags\": [\"a\", 1.50], \"n\": {\"k\": true}, \"card\": \"APP-1 <&>\"}\n---\n# Body\nmore"
	if got := matter.String(); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if value, ok := matter.Get("tags"); !ok || len(value.([]any)) != 2 {
		t.Error(value)
	}
	multi, err := ReadFrontMatter("---\n{\n  \"id\": \"x\"\n}\n---\nb")
	if err != nil || multi.OneLine {
		t.Fatal(err, multi.OneLine)
	}
	if got := multi.String(); got != "---\n{\n  \"id\": \"x\"\n}\n---\nb" {
		t.Error(got)
	}
	for _, bad := range []string{"", "no", "---\n[1]\n---\n", "---\n{\"a\": 1, \"a\": 2}\n---\n", "---\n{\"a\": 1} {}\n---\n", "---\n{\"a\": 1}\n"} {
		if _, err := ReadFrontMatter(bad); err == nil {
			t.Errorf("%q read", bad)
		}
	}
}

func TestFrontMatterStrict(t *testing.T) {
	for _, bad := range []string{"---\n{\"a\": 1} garbage\n---\n", "---\n{\"a\": 1}}\n---\n", "---\n{\"a\": 1} {}\n---\n"} {
		if _, err := ReadFrontMatter(bad); err == nil {
			t.Errorf("%q read", bad)
		}
	}
	if _, err := Numbers([]string{"d/A-2000000000000.md"}, regexp.MustCompile(`^d/A-([0-9]+)\.md$`)); err == nil {
		t.Error("a number past the bound counted")
	}
	if _, err := Mentions("A", "A-99999999999999999999"); err == nil {
		t.Error("a mention past the bound counted")
	}
}
