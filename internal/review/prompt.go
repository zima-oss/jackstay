package review

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed prompt.tmpl
var defaultTemplate string

// PromptData are the fields a template receives (section 3.2).
type PromptData struct {
	Brief        string
	Commit       string
	Subject      string
	Base         string
	DiffStat     string
	Tree         string
	Cite         []string
	Round        int
	Previous     string
	OpenFindings []LedgerFinding
	Triage       bool
	Severities   []string
	Blocking     []string
	Focus        string
}

// BuildPrompt writes the prompt from a template ("" for jackstay's own) and appends the verdict form,
// which no template can drop.
func BuildPrompt(templateText string, data PromptData) ([]byte, error) {
	if templateText == "" {
		templateText = defaultTemplate
	}
	parsed, err := template.New("review").Option("missingkey=error").Parse(templateText)
	if err != nil {
		return nil, fmt.Errorf("the review template: %w", err)
	}
	var out bytes.Buffer
	if err := parsed.Execute(&out, data); err != nil {
		return nil, fmt.Errorf("the review template: %w", err)
	}
	out.WriteString("\n")
	out.WriteString(verdictForm(data))
	return out.Bytes(), nil
}

func verdictForm(data PromptData) string {
	var form strings.Builder
	form.WriteString("## Your answer\n\n")
	form.WriteString("Your final message is one JSON object of the schema you are given: `verdict` (`approve` when\n")
	form.WriteString("nothing blocks, else `changes`), `summary`, `findings`, `prior`.\n\n")
	fmt.Fprintf(&form, "- `severity`: one of %s, most severe first; a finding blocks the change when its severity is %s",
		quoted(data.Severities), quoted(data.Blocking))
	if data.Triage {
		form.WriteString(" and it is realistic (below)")
	}
	form.WriteString(".\n")
	form.WriteString("- `likelihood`: `ordinary` if it happens in ordinary work by accident, `unusual` if it needs a\n")
	form.WriteString("  deliberate or unusual setup. `consequence`: `severe` for lost work or data or a wrong decision\n")
	form.WriteString("  that passes unnoticed, `mild` for an inconvenience or a wrong output seen at once. Rate honestly.\n")
	if data.Triage {
		form.WriteString("  An `unusual` and `mild` finding is rare: it is recorded for later, not fixed now.\n")
	}
	form.WriteString("- `id`: a short id of your own for each new finding (`F1`, `F2`, ...); jackstay gives it its ledger id.\n")
	form.WriteString("- `place`: `path: function or section`. When a finding is about the same place as an open finding\n")
	form.WriteString("  above, write that finding's place exactly and name its id in `repeats`.\n")
	form.WriteString("- `detail`: what is wrong, where, why, and the smallest fix. No style preferences.\n")
	form.WriteString("- `prior`: every open finding above, by id, `fixed` or `open` (an empty list when there are none).\n")
	return form.String()
}

func quoted(words []string) string {
	parts := make([]string, len(words))
	for index, word := range words {
		parts[index] = "`" + word + "`"
	}
	return strings.Join(parts, ", ")
}
