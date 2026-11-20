package review

import (
	"fmt"
	"slices"
	"strings"
)

// Report is a round's `report.md` (section 5).
func Report(settings Settings, record RoundRecord) string {
	var out strings.Builder
	status, next := Disposition(settings, record)
	fmt.Fprintf(&out, "# Review of %s, round %d\n\n", record.Task, record.Round)
	fmt.Fprintf(&out, "**%s** (exit %d)\n\n", next, status)
	fmt.Fprintf(&out, "- commit `%s`, base `%s`, chain %d", record.Commit, record.Base, record.Chain)
	if record.Follows > 0 {
		fmt.Fprintf(&out, ", follows round %d", record.Follows)
	}
	out.WriteString("\n")
	if record.Focus != "" {
		fmt.Fprintf(&out, "- focus: %s\n", record.Focus)
	}
	for _, run := range record.Runs {
		fmt.Fprintf(&out, "- %s: %s, model %q, effort %q, %.0f s\n", run.Reviewer, run.Version, run.Model, run.Effort, run.Seconds)
	}
	if record.OverLimit {
		fmt.Fprintf(&out, "- past the round limit of %d without the owner's word\n", settings.RoundLimit)
	}
	if len(record.Outcome.Disagree) > 0 {
		fmt.Fprintf(&out, "- the reviewer's own verdict differs from jackstay's: %s\n", strings.Join(record.Outcome.Disagree, ", "))
	}
	byID := map[string]LedgerFinding{}
	for _, finding := range record.Outcome.Ledger {
		byID[finding.ID] = finding
	}
	if len(record.Outcome.RedesignDue) > 0 {
		out.WriteString("\n## To be redesigned, not patched (W-019)\n\n")
		for _, place := range record.Outcome.RedesignDue {
			fmt.Fprintf(&out, "- `%s`: hit %d rounds running\n", place, record.Outcome.Streaks[place])
		}
	}
	var fresh, rare []LedgerFinding
	for _, id := range record.Outcome.New {
		if finding := byID[id]; finding.Status == StatusDeferred {
			rare = append(rare, finding)
		} else {
			fresh = append(fresh, finding)
		}
	}
	writeFindings(&out, "New findings", fresh, settings)
	var stillOpen []LedgerFinding
	for _, id := range record.Outcome.Reopened {
		stillOpen = append(stillOpen, byID[id])
	}
	writeFindings(&out, "Still open from earlier rounds", stillOpen, settings)
	if len(record.Outcome.NewlyFixed) > 0 {
		out.WriteString("\n## Fixed since the round before\n\n")
		for _, id := range record.Outcome.NewlyFixed {
			fmt.Fprintf(&out, "- %s — %s\n", id, byID[id].Title)
		}
	}
	if len(rare) > 0 {
		out.WriteString("\n## Rare: for the project's later-findings card (W-048)\n\n")
		out.WriteString("| found | place | finding | likelihood | consequence | why it waits |\n|---|---|---|---|---|---|\n")
		for _, finding := range rare {
			why := ""
			if finding.Consequence == "severe" {
				why = "no ordinary-work path named (W-094)"
			}
			fmt.Fprintf(&out, "| %s, round %d | `%s` | %s | unusual | %s | %s |\n", record.Task, record.Round,
				cell(finding.Place), cell(finding.Title), finding.Consequence, why)
		}
	}
	out.WriteString("\n## Summaries\n")
	for _, name := range sortedNames(record.Summaries) {
		fmt.Fprintf(&out, "\n**%s**: %s\n", name, record.Summaries[name])
	}
	return out.String()
}

func writeFindings(out *strings.Builder, heading string, findings []LedgerFinding, settings Settings) {
	if len(findings) == 0 {
		return
	}
	slices.SortStableFunc(findings, func(first, second LedgerFinding) int { return strings.Compare(first.PlaceKey, second.PlaceKey) })
	fmt.Fprintf(out, "\n## %s\n", heading)
	for _, finding := range findings {
		blocks := ""
		if settings.Blocks(finding.Severity, finding.Likelihood, finding.Consequence, finding.OrdinaryPath) {
			blocks = ", blocks"
		}
		fmt.Fprintf(out, "\n### %s — `%s`\n\n%s (%s, %s, %s%s)\n\n%s\n", finding.ID, finding.Place, finding.Title,
			finding.Severity, finding.Likelihood, finding.Consequence, blocks, finding.Detail)
	}
}

func cell(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "|", "\\|"), "\n", " ")
}

// FailureReport is the `report.md` of an attempt that did not become a round.
func FailureReport(task string, attempt int, runs []ReviewerRun) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Review of %s, attempt %d: no round taken\n\n", task, attempt)
	for _, run := range runs {
		fmt.Fprintf(&out, "- %s: **%s** — %s\n", run.Reviewer, run.Outcome, run.Reason)
	}
	out.WriteString("\nThe files of each reviewer are beside this report. jackstay does not retry: rerun when the reason is gone.\n")
	return out.String()
}
