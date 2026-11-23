package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type recordedRow struct {
	Doc   string                     `json:"doc"`
	Need  map[string]json.RawMessage `json:"-"`
	All   recordedDecision           `json:"all"`
	Guard recordedDecision           `json:"guard"`
	Cards recordedDecision           `json:"cards"`
}

type recordedDecision struct {
	Usable   bool     `json:"usable"`
	Sections []string `json:"sections"`
	Project  string   `json:"project"`
}

type recordedCases struct {
	Load []recordedRow `json:"load"`
}

// The recorded decisions (testdata/decisions.json: whether each document is usable, its usable
// sections and its project name; W-036: decisions, not texts).
func cases(t *testing.T) recordedCases {
	t.Helper()
	data, err := os.ReadFile("testdata/decisions.json")
	if err != nil {
		t.Fatal(err)
	}
	var c recordedCases
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// A pattern RE2 does not take (a lookaround): refused (W-036), so the recorded decision is skipped.
var notRE2 = regexp.MustCompile(`\(\?[=!<]`)

// The same files are usable, whole, for the guard and for the Card check, with the same usable
// sections and project name; the sentences are each implementation's own.
func TestLoadMakesTheRecordedDecisions(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "p")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	compared := 0
	for _, row := range cases(t).Load {
		if notRE2.MatchString(row.Doc) {
			continue
		}
		data := []byte(row.Doc)
		if row.Doc == "ÿ" {
			data = []byte{0xff}
		}
		if err := os.WriteFile(filepath.Join(folder, Name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		for name, c := range map[string]struct {
			need []string
			want recordedDecision
		}{"all": {nil, row.All}, "guard": {[]string{"guard"}, row.Guard}, "cards": {[]string{"cards"}, row.Cards}} {
			if name == "all" && strings.Contains(row.Doc, `"test"`) {
				continue // the meaning of `test` is not checked in Go yet (package comment)
			}
			compared++
			root, cfg, err := Load(folder, c.need)
			if (err == nil) != c.want.Usable {
				t.Errorf("%s of %s: usable=%v (%v), recorded usable=%v", name, row.Doc, err == nil, err, c.want.Usable)
				continue
			}
			if err != nil {
				continue
			}
			var got []string
			for k := range cfg {
				if k != "test" {
					got = append(got, k)
				}
			}
			var want []string
			for _, k := range c.want.Sections {
				if k != "test" {
					want = append(want, k)
				}
			}
			sort.Strings(got)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
				t.Errorf("%s of %s: usable sections %v, recorded %v", name, row.Doc, got, want)
			}
			if p := ProjectName(root, cfg, folder); p != c.want.Project {
				t.Errorf("%s of %s: project %q, recorded %q", name, row.Doc, p, c.want.Project)
			}
		}
	}
	if compared < 300 {
		t.Fatalf("only %d decisions compared", compared)
	}
}

func TestRE2IsTheDialect(t *testing.T) {
	cfg, err := Parse([]byte(`{"jackstay_schema": 2, "guard": {"deny": [{"id": "x", "command": "^git tag(?!x)"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(SectionProblems(cfg).Of("guard")) == 0 {
		t.Fatal("a look-ahead is not RE2 and must make the guard section unusable")
	}
}

func TestNotRE2ListsLookArounds(t *testing.T) {
	cfg, err := Parse([]byte(`{"jackstay_schema": 2, "guard": {"deny": [{"id": "x", "command": "^git tag(?!x)", "env": {"A": "1"}}]}, "cards": {"id": "A-[0-9]+"}}`))
	if err != nil {
		t.Fatal(err)
	}
	found := NotRE2(cfg)
	if len(found) != 1 || !strings.Contains(found[0], "guard.deny[0].command") {
		t.Fatalf("got %v", found)
	}
}

// The `github` section (docs/design/implementation.md, section 8.1): checked like every section, and a
// broken one stops only the commands that read it.
func TestGitHubSection(t *testing.T) {
	good := `"account": "octocat", "remote": "origin", "repository": "zima-oss/jackstay", "wait": {"timeout_seconds": 540, "interval_seconds": 20.5, "log_lines": 80}`
	for _, tc := range []struct {
		name    string
		section string
		schema  int
		problem string // "" when the section is usable
	}{
		{"whole", good, 2, ""},
		{"only the account", `"account": "a-b-c"`, 2, ""},
		{"39 characters", `"account": "` + strings.Repeat("a", 39) + `"`, 2, ""},
		{"a remote with a slash", `"account": "a", "remote": "up/stream"`, 2, ""},
		{"no account", `"repository": "o/r"`, 2, "github.account` is missing"},
		{"an empty account", `"account": ""`, 2, "non-empty string"},
		{"40 characters", `"account": "` + strings.Repeat("a", 40) + `"`, 2, "not a GitHub login"},
		{"a leading hyphen", `"account": "-a"`, 2, "not a GitHub login"},
		{"a trailing hyphen", `"account": "a-"`, 2, "not a GitHub login"},
		{"two hyphens", `"account": "a--b"`, 2, "not a GitHub login"},
		{"an underscore", `"account": "a_b"`, 2, "not a GitHub login"},
		{"a number", `"account": 5`, 2, "non-empty string"},
		{"a repository without owner", `"account": "a", "repository": "jackstay"`, 2, "owner/name"},
		{"a repository of three parts", `"account": "a", "repository": "o/r/x"`, 2, "owner/name"},
		{"a repository named ..", `"account": "a", "repository": "o/.."`, 2, "owner/name"},
		{"a remote with a space", `"account": "a", "remote": "my remote"`, 2, "remote's name"},
		{"a remote with ..", `"account": "a", "remote": "a..b"`, 2, "remote's name"},
		{"a remote starting with a hyphen", `"account": "a", "remote": "-x"`, 2, "remote's name"},
		{"a zero timeout", `"account": "a", "wait": {"timeout_seconds": 0}`, 2, "above 0"},
		{"a negative interval", `"account": "a", "wait": {"interval_seconds": -1}`, 2, "above 0"},
		{"fractional log lines", `"account": "a", "wait": {"log_lines": 1.5}`, 2, "whole number"},
		{"zero log lines", `"account": "a", "wait": {"log_lines": 0}`, 2, "log_lines` must be a whole number above 0"},
		{"an unknown key", `"account": "a", "token": "x"`, 2, "unknown key `github.token`"},
		{"an unknown wait key", `"account": "a", "wait": {"lines": 3}`, 2, "unknown key `github.wait.lines`"},
		{"schema 1", `"account": "a"`, 1, "need `\"jackstay_schema\": 2`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			folder := t.TempDir()
			doc := fmt.Sprintf(`{"jackstay_schema": %d, "project": "p", "trash": {"keep_days": 3}, "github": {%s}}`, tc.schema, tc.section)
			if err := os.WriteFile(filepath.Join(folder, Name), []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
			_, cfg, err := Load(folder, []string{"github"})
			if tc.problem == "" {
				if err != nil {
					t.Fatalf("unusable: %v", err)
				}
				if _, ok := cfg.GitHub(); !ok {
					t.Fatal("the section is left out")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.problem) {
				t.Fatalf("got %v, want a problem with %q", err, tc.problem)
			}
			if tc.schema != 2 {
				return
			}
			_, others, err := Load(folder, []string{"trash"})
			if err != nil {
				t.Fatalf("a broken github section stopped a command that does not read it: %v", err)
			}
			if _, ok := others.GitHub(); ok || others.Section("trash") == nil {
				t.Fatalf("usable sections %v", others)
			}
		})
	}
}

func TestGitHubSectionValues(t *testing.T) {
	cfg, err := Parse([]byte(`{"jackstay_schema": 2, "github": {"account": "octocat", "remote": "origin", "repository": "zima-oss/jackstay", "wait": {"timeout_seconds": 540, "interval_seconds": 20, "log_lines": 80}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := cfg.GitHub()
	want := GitHub{Account: "octocat", Remote: "origin", Repository: "zima-oss/jackstay",
		Wait: GitHubWait{TimeoutSeconds: 540, IntervalSeconds: 20, LogLines: 80}}
	if !ok || got != want {
		t.Fatalf("got %+v", got)
	}
	if _, ok := (Config{}).GitHub(); ok {
		t.Fatal("no section must say so")
	}
}

func TestSectionsLeavesOut(t *testing.T) {
	all := Sections()
	without := Sections("github")
	if len(all) != len(without)+1 || contains(without, "github") || !contains(all, "github") || contains(all, "jackstay_schema") {
		t.Fatalf("all %v, without github %v", all, without)
	}
}

func TestProjectNameOfARelativeFolder(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "named")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(folder)
	if got := ProjectName("", Config{}, "."); got != "named" {
		t.Fatalf("got %q", got)
	}
}

func TestCardsRejectsAPatternThatCanMatchEmptyText(t *testing.T) {
	loaded, err := Parse([]byte(`{"jackstay_schema":2,"cards":{"dir":"cards","id":"A*","unit":"pr","since":"2026-01-01"}}`))
	if err != nil {
		t.Fatal(err)
	}
	problems := SectionProblems(loaded).Of("cards")
	if len(problems) != 1 || !strings.Contains(problems[0], "nonempty ID") {
		t.Fatalf("card ID was not rejected: %v", problems)
	}
}

func TestStepKindsAreChecked(t *testing.T) {
	for text, want := range map[string]string{
		`{"jackstay_schema": 3, "test": {"scopes": {"a": {"run": ["true"], "kind": "lint"}}}}`:                     "must be \"test\" or \"check\"",
		`{"jackstay_schema": 3, "test": {"scopes": {"a": {"deps": ["b"], "kind": "check"}, "b": {"run": ["x"]}}}}`: "is for a scope with `run`",
		`{"jackstay_schema": 3, "test": {"scopes": {"a": {"run": ["true"], "kind": "test"}}}}`:                     "which counts none",
		`{"jackstay_schema": 3, "test": {"scopes": {"a": {"run": ["true"], "kind": "check"}}}}`:                    "",
		`{"jackstay_schema": 3, "test": {"scopes": {"a": {"run": ["go"], "judge": "go-json", "kind": "check"}}}}`:  "can only be `exit`",
		`{"jackstay_schema": 3, "test": {"scopes": {"a": {"run": ["go"], "judge": "go-json", "kind": "test"}}}}`:   "",
	} {
		parsed, err := Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(SectionProblems(parsed).found["test"], "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: %q, want %q", text, got, want)
		}
	}
}
