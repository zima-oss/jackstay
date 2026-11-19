package changes

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		typed    bool
		problems int
		check    func(t *testing.T, e Entry)
	}{
		{name: "untyped", text: "- a fix\n", typed: false},
		{name: "fix", text: "---\n{\"type\": \"fix\", \"audience\": \"consumer\"}\n---\n# A fix\n\nMore.\n", typed: true,
			check: func(t *testing.T, e Entry) {
				if e.Title != "A fix" || e.Body != "More." || e.Bump() != "patch" {
					t.Fatalf("got %+v", e)
				}
			}},
		{name: "breaking with migration", typed: true,
			text: "---\n{\"type\": \"breaking\", \"audience\": \"consumer\", \"card\": \"GLM-1\"}\n---\n# Rows\n\nWhy.\n\n## Migration\n\nDo this.\n",
			check: func(t *testing.T, e Entry) {
				if e.Migration != "Do this." || e.Body != "Why." || e.Card != "GLM-1" || e.Bump() != "major" {
					t.Fatalf("got %+v", e)
				}
			}},
		{name: "breaking without migration", typed: true, problems: 1,
			text: "---\n{\"type\": \"breaking\", \"audience\": \"consumer\"}\n---\n# Rows\n"},
		{name: "empty migration", typed: true, problems: 1,
			text: "---\n{\"type\": \"breaking\", \"audience\": \"consumer\"}\n---\n# Rows\n\n## Migration\n\n## Other\n"},
		{name: "unknown key", typed: true, problems: 1,
			text: "---\n{\"type\": \"fix\", \"audience\": \"consumer\", \"extra\": 1}\n---\n# T\n"},
		{name: "repeated key", typed: true, problems: 1,
			text: "---\n{\"type\": \"fix\", \"type\": \"fix\", \"audience\": \"consumer\"}\n---\n# T\n"},
		{name: "bad type and audience", typed: true, problems: 2,
			text: "---\n{\"type\": \"chore\", \"audience\": \"everyone\"}\n---\n# T\n"},
		{name: "no title", typed: true, problems: 1,
			text: "---\n{\"type\": \"fix\", \"audience\": \"consumer\"}\n---\nNo heading here\n"},
		{name: "yanks on a fix", typed: true, problems: 1,
			text: "---\n{\"type\": \"fix\", \"audience\": \"consumer\", \"yanks\": \"1.0.0\"}\n---\n# T\n"},
		{name: "unclosed", typed: false, problems: 1, text: "---\n{\"type\": \"fix\"}\n# T\n"},
		{name: "carriage returns", typed: true, text: "---\r\n{\"type\": \"note\", \"audience\": \"internal\"}\r\n---\r\n# T\r\n",
			check: func(t *testing.T, e Entry) {
				if e.Bump() != "" || e.Title != "T" {
					t.Fatalf("got %+v", e)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, problems := Parse("x.md", []byte(c.text))
			if e.Typed != c.typed || len(problems) != c.problems {
				t.Fatalf("typed %v problems %q", e.Typed, problems)
			}
			if c.check != nil {
				c.check(t, e)
			}
		})
	}
}

func TestFormatRoundTrip(t *testing.T) {
	e := Entry{Name: "a.md", Typed: true, Type: "breaking", Audience: "consumer", Card: "APP-1", Title: `A "quoted" title`, Body: "Body.", Migration: "Step."}
	back, problems := Parse("a.md", Format(e))
	if len(problems) > 0 || back.Title != e.Title || back.Body != e.Body || back.Migration != e.Migration || back.Card != e.Card {
		t.Fatalf("got %+v %q", back, problems)
	}
}

func TestBodyOrder(t *testing.T) {
	parse := func(name, kind string) Entry {
		e, _ := Parse(name, []byte("---\n{\"type\": \""+kind+"\", \"audience\": \"consumer\"}\n---\n# "+name+"\n"))
		return e
	}
	untyped, _ := Parse("z.md", []byte("- old style\n"))
	body := Body([]Entry{parse("b.md", "fix"), parse("c.md", "feature"), untyped, parse("a.md", "fix")})
	want := "### Features\n\n- **c.md**\n\n### Fixes\n\n- **b.md**\n- **a.md**\n\n- old style"
	if body != want {
		t.Fatalf("got\n%s\nwant\n%s", body, want)
	}
}

func TestInsert(t *testing.T) {
	pattern := HeadingPattern("## {version} — {date}")
	log := "# Changelog\n\nIntro.\n\n## 0.1.0 — 2026-01-01\n\n- old\n"
	first := Insert(log, "## 0.2.0 — 2026-02-02\n\n- new\n", "first", pattern)
	if !strings.HasPrefix(first, "# Changelog\n\nIntro.\n\n## 0.2.0") || !strings.Contains(first, "- new\n\n## 0.1.0") {
		t.Fatalf("first:\n%s", first)
	}
	last := Insert(log, "## 0.2.0 — 2026-02-02\n\n- new\n", "last", pattern)
	if !strings.HasSuffix(last, "- old\n\n## 0.2.0 — 2026-02-02\n\n- new\n") {
		t.Fatalf("last:\n%s", last)
	}
	if got := Insert("", "## 1\n", "first", pattern); got != "## 1\n" {
		t.Fatalf("empty: %q", got)
	}
}

func TestTemplateProblem(t *testing.T) {
	for _, bad := range []string{"no front matter", "---\nnull\n---\n# T\n", "---\n{}\n---garbage\n# T\n", "---\n{bad}\n---\n"} {
		if TemplateProblem([]byte(bad)) == "" {
			t.Errorf("accepted %q", bad)
		}
	}
	if problem := TemplateProblem([]byte("---\n{\"type\": \"\", \"audience\": \"\"}\n---\n# {slug}\n")); problem != "" {
		t.Errorf("refused a good template: %s", problem)
	}
}

func TestYanksMustBeAVersion(t *testing.T) {
	_, problems := Parse("y.md", []byte("---\n{\"type\": \"note\", \"audience\": \"consumer\", \"yanks\": \"garbage\"}\n---\n# Y\n"))
	if len(problems) != 1 {
		t.Fatalf("got %q", problems)
	}
}

func TestMigrationKeepsAFencedComment(t *testing.T) {
	entry, problems := Parse("b.md", []byte("---\n{\"type\": \"breaking\", \"audience\": \"consumer\"}\n---\n# B\n\n## Migration\n\n```sh\n# edit config\nmake it\n```\n"))
	if len(problems) > 0 || !strings.Contains(entry.Migration, "make it") || entry.Body != "" {
		t.Fatalf("got %+v %q", entry, problems)
	}
}

func TestAMigrationHeadingInsideAFenceIsNotTheSection(t *testing.T) {
	_, problems := Parse("b.md", []byte("---\n{\"type\": \"breaking\", \"audience\": \"consumer\"}\n---\n# B\n\n```md\n## Migration\nexample\n```\n"))
	if len(problems) != 1 {
		t.Fatalf("a fenced example counted as the migration section: %q", problems)
	}
}

func TestNestedFencesHideTheMigrationHeading(t *testing.T) {
	_, problems := Parse("b.md", []byte("---\n{\"type\": \"breaking\", \"audience\": \"consumer\"}\n---\n# B\n\n````md\n```\n## Migration\nexample\n```\n````\n"))
	if len(problems) != 1 {
		t.Fatalf("a nested example counted as the migration section: %q", problems)
	}
}
