package review

import (
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/config"
)

func settingsFrom(t *testing.T, text string) (Settings, []string) {
	t.Helper()
	parsed, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if problems := config.SectionProblems(parsed).Of("review"); len(problems) > 0 {
		t.Fatalf("types: %v", problems)
	}
	return SettingsOf(parsed)
}

func TestDefaults(t *testing.T) {
	s, problems := settingsFrom(t, `{"jackstay_schema": 2, "project": "x"}`)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	codex := s.Reviewers["codex"]
	if len(s.Use) != 0 || s.Reviewers["claude-code"].Command != "claude" {
		t.Fatalf("reviewer built-ins and opt-in defaults: %+v", s)
	}
	if codex.Effort != "medium" || codex.TimeoutMinutes != 20 || codex.Command != "codex" || len(codex.Isolation) != len(DefaultIsolation) {
		t.Fatalf("codex defaults: %+v", codex)
	}
	if strings.Join(s.RepeatSeverities, ",") != "blocker,major" || s.RoundLimit != 6 || s.MaxConcurrent != 4 ||
		s.QueueMinutes != 15 || s.KeepDays != 30 || !s.Triage || s.Cite != nil {
		t.Fatalf("defaults: %+v", s)
	}
	if !s.Pool.Accepting || s.Pool.HistoryDays != 30 {
		t.Fatalf("pool defaults: %+v", s.Pool)
	}
	if s.Budget([]string{"codex"}) != 37 {
		t.Fatalf("budget %d", s.Budget([]string{"codex"}))
	}
}

func TestPoolAdmissionAndHistorySettings(t *testing.T) {
	settings, problems := settingsFrom(t, `{"jackstay_schema":2,"review":{"pool":{"accepting":false,"history_days":90}}}`)
	if len(problems) != 0 || settings.Pool.Accepting || settings.Pool.HistoryDays != 90 {
		t.Fatalf("pool settings: %+v, problems %v", settings.Pool, problems)
	}
	for _, value := range []string{"0", "3651"} {
		parsed, err := config.Parse([]byte(`{"jackstay_schema":2,"review":{"pool":{"history_days":` + value + `}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if problems := config.SectionProblems(parsed).Of("review"); len(problems) == 0 {
			t.Fatalf("invalid history_days %s was accepted by configuration validation", value)
		}
	}
}

func TestSettingsRefused(t *testing.T) {
	cases := map[string]string{
		`"use": ["unknown"]`:          "unknown reviewer",
		`"use": ["codex", "codex"]`:   "twice",
		`"reviewers": {"claude": {}}`: "knows how to run only",
		`"reviewers": {"codex": {"isolation": ["--dangerously-bypass-approvals-and-sandbox"]}}`: "undo read-only",
		`"reviewers": {"codex": {"isolation": ["-c", "sandbox_mode=\"danger-full-access\""]}}`:  "sets `sandbox_mode`",
		`"reviewers": {"codex": {"isolation": ["review this"]}}`:                                "not an option",
		`"reviewers": {"codex": {"isolation": ["-o"]}}`:                                         "jackstay sets itself",
		`"reviewers": {"codex": {"isolation": ["-c"]}}`:                                         "no value",
		`"reviewers": {"codex": {"effort": "huge"}}`:                                            "effort",
		`"reviewers": {"codex": {"timeout_minutes": 0}}`:                                        "timeout_minutes",
		`"blocking": ["fatal"]`:                                                                 "not in `review.severities`",
		`"blocking": []`:                                                                        "no finding could ever block",
		`"repeat_rounds": 1`:                                                                    "repeat_rounds",
		`"max_concurrent": 17`:                                                                  "max_concurrent",
		`"store": "relative/path"`:                                                              "absolute",
		`"cite": ["../outside"]`:                                                                "inside the repository",
		`"severities": ["a", "a"]`:                                                              "twice",
	}
	for body, want := range cases {
		_, problems := settingsFrom(t, `{"jackstay_schema": 2, "review": {`+body+`}}`)
		if !strings.Contains(strings.Join(problems, "\n"), want) {
			t.Errorf("%s: want %q in %v", body, want, problems)
		}
	}
}

func TestReviewerConfigurationOnlyOverridesNamedProviders(t *testing.T) {
	settings, problems := settingsFrom(t, `{"jackstay_schema":2,"review":{
		"reviewers":{"claude-code":{"model":"example"}},"use":[]}}`)
	if len(problems) != 0 || len(settings.Use) != 0 ||
		settings.Reviewers["codex"].Command != "codex" || settings.Reviewers["claude-code"].Model != "example" {
		t.Fatalf("optional reviewers: %+v, problems %v", settings, problems)
	}
}

func TestSpecExampleIsUsable(t *testing.T) {
	_, problems := settingsFrom(t, `{"jackstay_schema": 2, "review": {
		"reviewers": {"codex": {"command": "codex", "model": "", "effort": "medium", "timeout_minutes": 20,
			"isolation": ["--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check",
				"-c", "mcp_servers={}", "-c", "features.apps=false", "-c", "shell_environment_policy.inherit=none"]}},
		"use": ["codex"], "brief": "docs/review-brief.md", "template": "", "cite": ["AGENTS.md", "docs/decisions/", "docs/design/"],
		"severities": ["blocker", "major", "minor"], "blocking": ["blocker"], "triage": true, "repeat_rounds": 2,
		"repeat_severities": ["blocker", "major"], "round_limit": 6, "max_concurrent": 4, "queue_minutes": 15,
		"store": "", "keep_days": 30}}`)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
}

func TestReviewNeedsSchema2(t *testing.T) {
	parsed, err := config.Parse([]byte(`{"jackstay_schema": 1, "review": {}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.SectionProblems(parsed).Sections()) == 0 {
		t.Fatal("a review section under jackstay_schema 1 must be refused")
	}
}

func TestSettingsTaken(t *testing.T) {
	s, problems := settingsFrom(t, `{"jackstay_schema": 2, "review": {"severities": ["high", "low"], "blocking": ["high"], "triage": false,
		"reviewers": {"codex": {"model": "m", "effort": "", "isolation": ["--ephemeral", "-c", "features.apps=false"]}},
		"cite": [], "store": "~/reviews", "keep_days": 3}}`)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	if strings.Join(s.RepeatSeverities, ",") != "high" || s.Triage || s.Cite == nil || len(s.Cite) != 0 ||
		s.Reviewers["codex"].Model != "m" || s.Reviewers["codex"].Effort != "" || s.KeepDays != 3 {
		t.Fatalf("%+v", s)
	}
}

func TestBlocks(t *testing.T) {
	s := Defaults()
	s.RepeatSeverities = allButLast(s.Severities)
	if !s.Blocks("blocker", "ordinary", "mild") || !s.Blocks("blocker", "unusual", "severe") || s.Blocks("blocker", "unusual", "mild") ||
		s.Blocks("major", "ordinary", "severe") {
		t.Fatal("triage on")
	}
	s.Triage = false
	if !s.Blocks("blocker", "unusual", "mild") {
		t.Fatal("triage off")
	}
}

func TestClaudeCodeIsAnOptInReviewerWithFixedIsolation(t *testing.T) {
	settings, problems := settingsFrom(t, `{"jackstay_schema":2,"review":{
		"reviewers":{"codex":{},"claude-code":{"effort":"max"}},"use":["claude-code"]}}`)
	claude := settings.Reviewers["claude-code"]
	if len(problems) > 0 || claude.Command != "claude" || claude.Effort != "max" || claude.TimeoutMinutes != 20 {
		t.Fatalf("Claude settings %+v, problems %v", claude, problems)
	}
	_, problems = settingsFrom(t, `{"jackstay_schema":2,"review":{
		"reviewers":{"claude-code":{"isolation":["--dangerously-skip-permissions"],"effort":"minimal"}},
		"use":["claude-code"]}}`)
	if words := strings.Join(problems, "\n"); !strings.Contains(words, "isolation") || !strings.Contains(words, "effort") {
		t.Fatalf("unsafe or invalid Claude CLI settings accepted: %v", problems)
	}
}
