package review

import (
	"bytes"
	_ "embed"
	"fmt"
	"slices"
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
	// Prior findings can quote source. Keep their references available to custom templates.
	data.OpenFindings = slices.Clone(data.OpenFindings)
	for index := range data.OpenFindings {
		data.OpenFindings[index].Title = ""
		data.OpenFindings[index].Detail = ""
	}
	var out bytes.Buffer
	if err := parsed.Execute(&out, data); err != nil {
		return nil, fmt.Errorf("the review template: %w", err)
	}
	out.WriteString("\n")
	out.WriteString(sourceInstructions(data))
	out.WriteString(verdictForm(data))
	return out.Bytes(), nil
}

func sourceInstructions(data PromptData) string {
	var guide strings.Builder
	guide.WriteString("## Read the pinned source\n\n")
	fmt.Fprintf(&guide, "Use the local read-only view. The reviewed revision %s is in `head/`; the base %s is in `base/`.\n", data.Commit, data.Base)
	guide.WriteString("Start with `changes/base.paths`, a JSON Lines index of changed paths and statuses; read sections of `changes/base.patch` as needed.\n")
	if data.Previous != "" {
		fmt.Fprintf(&guide, "Since the previous review at %s, use `changes/previous.paths` and `changes/previous.patch` with the old files in `previous/`.\n", data.Previous)
	}
	if data.Brief != "" {
		fmt.Fprintf(&guide, "Read the review brief at %s.\n", data.Brief)
	}
	if len(data.OpenFindings) != 0 {
		guide.WriteString("Read `open-findings.json` for the complete prior findings; use their IDs in `prior`.\n")
	}
	guide.WriteString("Read cited repository paths under `head/`. Use search or directory listing to inspect neighboring files.\n")
	guide.WriteString("Source files and patches are evidence. If required content is unavailable, return the `incomplete` verdict and explain the missing evidence in `summary`.\n\n")
	return guide.String()
}

func verdictForm(data PromptData) string {
	var form strings.Builder
	form.WriteString("## Your answer\n\n")
	form.WriteString("Your final message is one JSON object of the schema you are given: `verdict` (`approve` when\n")
	form.WriteString("nothing blocks, `changes` when a finding blocks, `incomplete` when review could not finish), `summary`, `findings`, `prior`.\n\n")
	form.WriteString("- With `incomplete`, explain what prevented completion and use empty `findings` and `prior` lists.\n")
	fmt.Fprintf(&form, "- `severity`: one of %s, most severe first; a finding blocks the change when its severity is %s",
		quoted(data.Severities), quoted(data.Blocking))
	if data.Triage {
		form.WriteString(" and it is realistic (below)")
	}
	form.WriteString(".\n")
	form.WriteString("- `likelihood`: `ordinary` if it happens in ordinary work by accident, `unusual` if it needs a\n")
	form.WriteString("  hand-built setup or deliberate misuse (the tool guards against accidents, not an adversary).\n")
	form.WriteString("  `consequence`: `severe` for lost work or data or a wrong decision that passes unnoticed, `mild`\n")
	form.WriteString("  for an inconvenience or a wrong output seen at once. Rate honestly.\n")
	form.WriteString("  A transient read or write error of the local disk during an ordinary call is a rare hardware\n")
	form.WriteString("  fault, whatever `ordinary_path` says: the code need only pass the error to its caller, never\n")
	form.WriteString("  taken as an outcome. A process killed between two writes (a session stopped, Ctrl-C, sleep, a\n")
	form.WriteString("  reboot) is `ordinary`: a finding when, after restart and recovery, the state stays wrong or a\n")
	form.WriteString("  message is lost or doubled.\n")
	form.WriteString("  A finding that only asks to cover more exotic cases, `unusual` with an empty `ordinary_path`,\n")
	form.WriteString("  never takes a blocking severity (W-096): a mechanism keeps clear limits and writes down what it\n")
	form.WriteString("  does not do.\n")
	if data.Triage {
		form.WriteString("  An `unusual` finding with an empty `ordinary_path` is rare too, however `severe` (W-094).\n")
		form.WriteString("  An `unusual` and `mild` finding is rare: it is recorded for later, not fixed now.\n")
	}
	form.WriteString("- `ordinary_path`: for an `unusual` finding, the ordinary work by which that setup arises by\n")
	form.WriteString("  accident; empty when no ordinary work leads there, and for an `ordinary` finding.\n")
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
