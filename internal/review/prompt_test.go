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
	for _, want := range []string{"changes/base.patch", "`AGENTS.md`", "English", "## Your answer", "realistic"} {
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
	for _, want := range []string{"changes/previous.patch", "r1-codex-F1", "open-findings.json", "Look hardest at: the lock"} {
		if !strings.Contains(string(delta), want) {
			t.Errorf("delta prompt lacks %q", want)
		}
	}
	custom, err := BuildPrompt("{{range .OpenFindings}}{{.Title}} {{.Detail}}{{end}}", data)
	if err != nil || strings.Contains(string(delta)+string(custom), "the whole detail") || data.OpenFindings[0].Detail != "the whole detail" {
		t.Fatal("finding text belongs in the local source view")
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

func TestAnUnusualAndSevereFindingMustNameItsOrdinaryPath(t *testing.T) {
	form := verdictForm(PromptData{Triage: true, Severities: []string{"blocker", "major", "minor"}, Blocking: []string{"blocker"}})
	for _, want := range []string{"hand-built setup or deliberate misuse", "`ordinary_path`", "however `severe` (W-094)"} {
		if !strings.Contains(form, want) {
			t.Errorf("the verdict form lacks %q:\n%s", want, form)
		}
	}
	settings := testSettings()
	_, next := Disposition(settings, RoundRecord{Round: 1, Outcome: Outcome{Blocks: true}})
	_, owner := Disposition(settings, RoundRecord{Round: settings.RoundLimit, Outcome: Outcome{Blocks: true}})
	for _, guidance := range []string{next, owner} {
		if !strings.Contains(guidance, "W-094") {
			t.Errorf("the session is not told to re-check the ratings: %s", guidance)
		}
	}
	settings.Triage = false
	if _, plain := Disposition(settings, RoundRecord{Round: 1, Outcome: Outcome{Blocks: true}}); strings.Contains(plain, "W-094") {
		t.Errorf("with triage off a rating decides nothing, yet the session is told to re-check it: %s", plain)
	}
	if form := verdictForm(PromptData{Severities: []string{"blocker"}, Blocking: []string{"blocker"}}); strings.Contains(form, "W-094") {
		t.Error("with triage off the form still says an unusual finding without a path is rare")
	}
}
