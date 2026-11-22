package review

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func testSettings() Settings {
	s := Defaults()
	s.Use = []string{"codex"}
	s.RepeatSeverities = allButLast(s.Severities)
	return s
}

func finding(id, place, severity, likelihood, consequence string, repeats ...string) AnswerFinding {
	return AnswerFinding{ID: id, Place: place, Severity: severity, Likelihood: likelihood, Consequence: consequence,
		Title: "t " + id, Detail: "d " + id, Repeats: repeats}
}

func TestLedgerOverAChain(t *testing.T) {
	s := testSettings()
	r1 := Advance(s, nil, nil, map[string]Answer{"codex": {Verdict: "changes", Findings: []AnswerFinding{
		finding("F1", "a.go: run", "blocker", "ordinary", "severe"),
		finding("F1b", "b.go: x", "major", "unusual", "mild"), // rare: deferred
	}}}, 1, "c1")
	if !r1.Blocks || len(r1.New) != 2 || r1.Ledger[0].ID != "r1-codex-F1" || r1.Ledger[1].Status != StatusDeferred {
		t.Fatalf("round 1: %+v", r1)
	}
	if strings.Join(r1.Hits, ",") != "a.go: run,b.go: x" || len(r1.RedesignDue) != 0 || len(r1.Disagree) != 0 {
		t.Fatalf("round 1 hits: %+v", r1)
	}
	// Round 2: nothing new, but the blocker is confirmed open: still blocks and hits again.
	r2 := Advance(s, r1.Ledger, r1.Streaks, map[string]Answer{"codex": {Verdict: "approve",
		Prior: []PriorAnswer{{ID: "r1-codex-F1", Status: "open"}}}}, 2, "c2")
	if !r2.Blocks || slices.Compare(r2.Reopened, []string{"r1-codex-F1"}) != 0 {
		t.Fatalf("round 2: %+v", r2)
	}
	if slices.Compare(r2.RedesignDue, []string{"a.go: run"}) != 0 || r2.Streaks["a.go: run"] != 2 || r2.Streaks["b.go: x"] != 0 {
		t.Fatalf("round 2 redesign: %+v", r2)
	}
	if slices.Compare(r2.Disagree, []string{"codex"}) != 0 {
		t.Fatalf("approve while blocking is a disagreement: %+v", r2.Disagree)
	}
	// Round 3: fixed; a new finding repeating it takes its place.
	r3 := Advance(s, r2.Ledger, r2.Streaks, map[string]Answer{"codex": {Verdict: "changes",
		Prior:    []PriorAnswer{{ID: "r1-codex-F1", Status: "fixed"}},
		Findings: []AnswerFinding{finding("F1", "elsewhere", "minor", "ordinary", "mild", "r1-codex-F1")}}}, 3, "c3")
	if r3.Blocks || r3.Ledger[0].Status != StatusFixed || r3.Ledger[0].FixedRound != 3 {
		t.Fatalf("round 3: %+v", r3)
	}
	if added := r3.Ledger[2]; added.ID != "r3-codex-F1" || added.PlaceKey != "a.go: run" {
		t.Fatalf("repeats takes the place: %+v", added)
	}
	if len(r3.Hits) != 0 || len(r3.RedesignDue) != 0 { // a minor does not hit
		t.Fatalf("round 3 hits: %+v", r3)
	}
}

func TestEmptyAnswerKeepsABlocker(t *testing.T) {
	s := testSettings()
	r1 := Advance(s, nil, nil, map[string]Answer{"codex": {Findings: []AnswerFinding{finding("F1", "p", "blocker", "ordinary", "mild")}}}, 1, "c")
	r2 := Advance(s, r1.Ledger, r1.Streaks, map[string]Answer{"codex": {Verdict: "approve", Prior: []PriorAnswer{{ID: "r1-codex-F1", Status: "open"}}}}, 2, "c2")
	if !r2.Blocks {
		t.Fatal("an open blocker must still block")
	}
}

func TestFixedOnlyWhenEveryReviewerSaysSo(t *testing.T) {
	s := testSettings()
	r1 := Advance(s, nil, nil, map[string]Answer{"codex": {Findings: []AnswerFinding{finding("F1", "p", "blocker", "ordinary", "mild")}}}, 1, "c")
	r2 := Advance(s, r1.Ledger, r1.Streaks, map[string]Answer{
		"codex": {Prior: []PriorAnswer{{ID: "r1-codex-F1", Status: "fixed"}}},
		"other": {Prior: []PriorAnswer{{ID: "r1-codex-F1", Status: "open"}}},
	}, 2, "c2")
	if r2.Ledger[0].Status != StatusOpen || !r2.Blocks {
		t.Fatalf("%+v", r2)
	}
}

func TestTriageOffKeepsRareOpen(t *testing.T) {
	s := testSettings()
	s.Triage = false
	r1 := Advance(s, nil, nil, map[string]Answer{"codex": {Findings: []AnswerFinding{finding("F1", "p", "blocker", "unusual", "mild")}}}, 1, "c")
	if r1.Ledger[0].Status != StatusOpen || !r1.Blocks {
		t.Fatalf("%+v", r1)
	}
}

func TestLedgerIDsStayDistinct(t *testing.T) {
	// F1 and r2-codex-F1 in one answer are two findings and must stay two.
	s := testSettings()
	r2 := Advance(s, nil, nil, map[string]Answer{"codex": {Findings: []AnswerFinding{
		finding("F1", "p", "blocker", "ordinary", "severe"), finding("r2-codex-F1", "q", "blocker", "ordinary", "severe"),
	}}}, 2, "c")
	if len(r2.Ledger) != 2 || r2.Ledger[0].ID == r2.Ledger[1].ID {
		t.Fatalf("%+v", r2.Ledger)
	}
	r3 := Advance(s, r2.Ledger, r2.Streaks, map[string]Answer{"codex": {Prior: []PriorAnswer{
		{ID: r2.Ledger[0].ID, Status: "fixed"}, {ID: r2.Ledger[1].ID, Status: "open"}}}}, 3, "c3")
	if !r3.Blocks {
		t.Fatal("fixing one closed both")
	}
}

func TestPlaceKey(t *testing.T) {
	if PlaceKey("  Internal/Run.go:   Start ") != "internal/run.go: start" {
		t.Fatal(PlaceKey("  Internal/Run.go:   Start "))
	}
}

func TestAnUnusualSevereFindingWithoutAnOrdinaryPathDoesNotHoldARound(t *testing.T) {
	s := testSettings()
	handBuilt := finding("F1", "a.go: run", "blocker", "unusual", "severe")
	accidental := finding("F2", "b.go: run", "blocker", "unusual", "severe")
	accidental.OrdinaryPath = "a rebase in a linked worktree while the review runs"
	outcome := Advance(s, nil, nil, map[string]Answer{"codex": {Verdict: "changes", Findings: []AnswerFinding{handBuilt}}}, s.RoundLimit, "c1")
	if outcome.Blocks || outcome.Ledger[0].Status != StatusDeferred {
		t.Fatalf("a hand-built setup held the round at its limit: %+v", outcome)
	}
	outcome = Advance(s, nil, nil, map[string]Answer{"codex": {Verdict: "changes", Findings: []AnswerFinding{accidental}}}, s.RoundLimit, "c1")
	if !outcome.Blocks || outcome.Ledger[0].OrdinaryPath == "" {
		t.Fatalf("a severe accident with its ordinary path did not block: %+v", outcome)
	}
}

func TestARoundAtTheLimitIsReadByTodaysPredicate(t *testing.T) {
	h := newHarness(t, Answer{Verdict: "approve", Summary: "clear"})
	settings := h.round.Settings
	// A round saved before W-094: at the limit, blocking only by an unusual, severe finding with no path.
	handBuilt := LedgerFinding{ID: "r6-codex-F1", Reviewer: "codex", Place: "a.go: run", PlaceKey: "a.go: run", Severity: "blocker",
		Likelihood: "unusual", Consequence: "severe", Title: "t", Detail: "d", Status: StatusOpen, OpenedRound: settings.RoundLimit}
	record := RoundRecord{Task: "feature/x", Round: settings.RoundLimit, Attempt: settings.RoundLimit, Chain: 1, Base: "c0", Commit: "c1",
		Policy: settings.policyID(), Runs: []ReviewerRun{{Reviewer: "codex", Outcome: "done"}},
		Outcome: Outcome{Blocks: true, Ledger: []LedgerFinding{handBuilt}}}
	if err := writeJSON(filepath.Join(h.round.Store.TaskPath("feature/x"), fmt.Sprintf("round-%d", settings.RoundLimit), RecordName), record); err != nil {
		t.Fatal(err)
	}
	result := h.round.Run(context.Background(), Options{Revision: "c0..c2", Fresh: true})
	if result.Status != ExitClear || result.Record == nil || result.Record.OverLimit {
		t.Fatalf("a saved flag held the task at the limit although nothing blocks by today's rule: %+v", result)
	}
	if reasons, _ := CheckReasons(settings, h.round.Store, "feature/x", "c0", "c2"); len(reasons) != 0 {
		t.Fatalf("the clear follow-up does not pass the check: %v", reasons)
	}
}
