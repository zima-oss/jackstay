package status

import (
	"testing"

	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
)

// Spans of both generations name their stage: `jst gh` (before 0.6.0) and `jst checks`/`jst land`.
func TestStagesOfGitHubCommandsOfBothGenerations(t *testing.T) {
	cases := map[[2]string]string{
		{"gh", "ci-status"}: "github.ci-status", {"gh", "pr-land"}: "github.pr-land", {"gh", "other"}: "github.command",
		{"checks", "wait"}: "checks.wait", {"checks", "run-wait"}: "checks.run-wait",
		{"land", ""}: "land.command", {"land", "find"}: "land.find",
	}
	for given, want := range cases {
		span := journal.Span{Name: "jst", Attributes: map[string]any{telemetry.AttrCommand: given[0], telemetry.AttrGitHubOperation: given[1]}}
		if got := stageOf(span); got != want {
			t.Errorf("%v: %q, want %q", given, got, want)
		}
	}
}

// A gate recorded before 0.6.0 removed `--resume` keeps its `resumed` outcome in the history.
func TestAResumedGateFromBeforeTheUpgradeKeepsItsOutcome(t *testing.T) {
	span := journal.Span{Name: "test.gate", Attributes: map[string]any{telemetry.AttrOutcome: "resumed"}}
	if got := statusOf(span); got != "resumed" {
		t.Fatalf("status %q, want resumed", got)
	}
}
