package config

import (
	"strings"
	"testing"
)

func TestPathPatterns(t *testing.T) {
	for pattern, good := range map[string]bool{
		"Generated/**": true, "Generated": true, "build/cache/*": true, "~/Library/Caches/myapp/**": true, "~/Library": true,
		"*": false, "**": false, "./*": false, "~/*": false, "/repo/*": false, "../x/*": false, ".git/hooks/*": false,
		"": false, "a/../b/*": false,
	} {
		if problem := PatternProblem(pattern); (problem == "") != good {
			t.Errorf("%q: problem %q, want good=%v", pattern, problem, good)
		}
	}
}

func TestGuardRulesSection(t *testing.T) {
	good := `{"jackstay_schema": 2, "guard": {"rules": {"zsh-glob": {"level": "refuse"}, "rm-recursive": {"paths": ["Generated/**"]},
		"git-push-force": {"level": "warn"}, "git-reset-hard": {"level": "off"}}}}`
	cfg, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if problems := SectionProblems(cfg).Of("guard"); len(problems) != 0 {
		t.Fatalf("a good section: %v", problems)
	}
	for text, want := range map[string]string{
		`{"no-such-rule": {"level": "off"}}`:                "no built-in rule",
		`{"git-config-alias": {"level": "off"}}`:            "cannot be judged",
		`{"zsh-glob": {"level": "maybe"}}`:                  "must be",
		`{"git-reset-hard": {"paths": ["Generated/**"]}}`:   "only rm-recursive and find-delete",
		`{"rm-recursive": {"paths": ["*"]}}`:                "names no folder",
		`{"rm-recursive": {"level": "off", "other": true}}`: "other",
	} {
		cfg, err := Parse([]byte(`{"jackstay_schema": 2, "guard": {"rules": ` + text + `}}`))
		if err != nil {
			t.Fatal(err)
		}
		problems := strings.Join(SectionProblems(cfg).Of("guard"), "; ")
		if !strings.Contains(problems, want) {
			t.Errorf("%s: problems %q, want %q", text, problems, want)
		}
	}
}
