package review

import (
	"fmt"
	"slices"
	"strings"
)

// Ledger statuses (section 4).
const (
	StatusOpen     = "open"
	StatusFixed    = "fixed"
	StatusDeferred = "deferred"
)

// LedgerFinding is a finding as the chain keeps it: everything the reviewer gave, jackstay's id, and
// its history.
type LedgerFinding struct {
	ID          string `json:"id"`
	Reviewer    string `json:"reviewer"`
	AnswerID    string `json:"answer_id"`
	Place       string `json:"place"`
	PlaceKey    string `json:"place_key"`
	Severity    string `json:"severity"`
	Likelihood  string `json:"likelihood"`
	Consequence string `json:"consequence"`
	Title       string `json:"title"`
	Detail      string `json:"detail"`
	// OrdinaryPath is the ordinary work by which an unusual setup arises by accident (W-094).
	OrdinaryPath string   `json:"ordinary_path,omitempty"`
	Repeats      []string `json:"repeats"`
	Status       string   `json:"status"`
	OpenedRound  int      `json:"opened_round"`
	OpenedCommit string   `json:"opened_commit"`
	FixedRound   int      `json:"fixed_round,omitempty"`
}

// Outcome of a round's computation from its answers.
type Outcome struct {
	Ledger      []LedgerFinding `json:"ledger"`
	New         []string        `json:"new"`         // ledger ids found in this round
	Reopened    []string        `json:"reopened"`    // ledger ids answered open in this round
	NewlyFixed  []string        `json:"newly_fixed"` // ledger ids answered fixed in this round
	Hits        []string        `json:"hits"`        // place keys hit in this round
	Streaks     map[string]int  `json:"streaks"`     // place key: rounds running hit, along the chain
	RedesignDue []string        `json:"redesign_due"`
	Blocks      bool            `json:"blocks"`
	Disagree    []string        `json:"disagree"` // reviewers whose verdict differs from the computed one
}

// PlaceKey is a place compared without case, its spaces trimmed and runs of them made one.
func PlaceKey(place string) string {
	return strings.ToLower(strings.Join(strings.Fields(place), " "))
}

// LedgerID is jackstay's id of a finding: always the round's prefix and the answer's own id, so ids
// unique in an answer are unique in the chain (a reviewer that copies the ledger's form gets a
// longer id, never a shared one).
func LedgerID(round int, reviewer, answerID string) string {
	return fmt.Sprintf("r%d-%s-%s", round, reviewer, answerID)
}

// Advance computes a round of a chain: the ledger before it (empty for a chain's first round),
// the streaks before it, and each reviewer's checked answer.
func Advance(settings Settings, before []LedgerFinding, streaks map[string]int, answers map[string]Answer,
	round int, commit string) Outcome {
	ledger := make([]LedgerFinding, len(before))
	copy(ledger, before)
	index := map[string]int{}
	for position, finding := range ledger {
		index[finding.ID] = position
	}
	reviewers := sortedNames(answers)
	outcome := Outcome{Streaks: map[string]int{}}
	hit := map[string]bool{}

	// Every open finding: fixed only when every reviewer says fixed.
	stillOpen := map[string]bool{}
	for _, reviewer := range reviewers {
		for _, prior := range answers[reviewer].Prior {
			if prior.Status == StatusOpen {
				stillOpen[prior.ID] = true
			}
		}
	}
	for position := range ledger {
		finding := &ledger[position]
		if finding.Status != StatusOpen {
			continue
		}
		if stillOpen[finding.ID] {
			outcome.Reopened = append(outcome.Reopened, finding.ID)
			if slices.Contains(settings.RepeatSeverities, finding.Severity) {
				hit[finding.PlaceKey] = true
			}
			continue
		}
		finding.Status = StatusFixed
		finding.FixedRound = round
		outcome.NewlyFixed = append(outcome.NewlyFixed, finding.ID)
	}

	for _, reviewer := range reviewers {
		for _, written := range answers[reviewer].Findings {
			added := LedgerFinding{
				ID: LedgerID(round, reviewer, written.ID), Reviewer: reviewer, AnswerID: written.ID,
				Place: strings.TrimSpace(written.Place), PlaceKey: PlaceKey(written.Place),
				Severity: written.Severity, Likelihood: written.Likelihood, Consequence: written.Consequence,
				Title: written.Title, Detail: written.Detail, OrdinaryPath: written.OrdinaryPath, Repeats: append([]string{}, written.Repeats...),
				Status: StatusOpen, OpenedRound: round, OpenedCommit: commit,
			}
			if len(written.Repeats) > 0 {
				if position, ok := index[written.Repeats[0]]; ok {
					added.Place, added.PlaceKey = ledger[position].Place, ledger[position].PlaceKey
				}
			}
			if settings.Triage && IsRare(added.Likelihood, added.Consequence, added.OrdinaryPath) {
				added.Status = StatusDeferred
			}
			if slices.Contains(settings.RepeatSeverities, added.Severity) {
				hit[added.PlaceKey] = true
			}
			index[added.ID] = len(ledger)
			ledger = append(ledger, added)
			outcome.New = append(outcome.New, added.ID)
		}
	}

	for place := range hit {
		outcome.Hits = append(outcome.Hits, place)
		outcome.Streaks[place] = streaks[place] + 1
		if outcome.Streaks[place] >= settings.RepeatRounds {
			outcome.RedesignDue = append(outcome.RedesignDue, place)
		}
	}
	slices.Sort(outcome.Hits)
	slices.Sort(outcome.RedesignDue)

	for _, finding := range ledger {
		if finding.Status == StatusOpen && settings.Blocks(finding.Severity, finding.Likelihood, finding.Consequence, finding.OrdinaryPath) {
			outcome.Blocks = true
		}
	}
	for _, reviewer := range reviewers {
		if (answers[reviewer].Verdict == "approve") == outcome.Blocks {
			outcome.Disagree = append(outcome.Disagree, reviewer)
		}
	}
	outcome.Ledger = ledger
	return outcome
}

// Open is the ledger's open findings, in order.
func Open(ledger []LedgerFinding) []LedgerFinding {
	var open []LedgerFinding
	for _, finding := range ledger {
		if finding.Status == StatusOpen {
			open = append(open, finding)
		}
	}
	return open
}

// ledgerBlocks says whether an open finding of ledger blocks, by today's predicate.
func ledgerBlocks(settings Settings, ledger []LedgerFinding) bool {
	for _, finding := range Open(ledger) {
		if settings.Blocks(finding.Severity, finding.Likelihood, finding.Consequence, finding.OrdinaryPath) {
			return true
		}
	}
	return false
}
