package review

import (
	"strings"
	"testing"
)

func TestPromptFreshAndDelta(t *testing.T) {
	data := PromptData{Commit: "c2", Subject: "s", Base: "b", DiffStat: " a.go | 2 +-", Cite: []string{"AGENTS.md"},
		Round: 1, Triage: true, Severities: []string{"blocker", "major", "minor"}, Blocking: []string{"blocker"}}
	fresh, err := BuildPrompt("", data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"git diff b c2", "`AGENTS.md`", "English", "## Your answer", "realistic"} {
		if !strings.Contains(string(fresh), want) {
			t.Errorf("fresh prompt lacks %q", want)
		}
	}
	data.Round, data.Previous, data.Focus = 2, "c1", "the lock"
	data.OpenFindings = []LedgerFinding{{ID: "r1-codex-F1", Place: "a.go: run", Severity: "blocker", Title: "title", Detail: "the whole detail",
		OpenedRound: 1, OpenedCommit: "c1"}}
	delta, err := BuildPrompt("", data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"git diff c1 c2", "r1-codex-F1", "the whole detail", "Look hardest at: the lock"} {
		if !strings.Contains(string(delta), want) {
			t.Errorf("delta prompt lacks %q", want)
		}
	}
}

func TestTemplateCannotDropTheForm(t *testing.T) {
	out, err := BuildPrompt("Only {{.Commit}}.", PromptData{Commit: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "Only abc.") || !strings.Contains(string(out), "## Your answer") {
		t.Fatal(string(out))
	}
	if _, err := BuildPrompt("{{.Nothing}}", PromptData{}); err == nil {
		t.Fatal("an unknown field must be refused")
	}
}
