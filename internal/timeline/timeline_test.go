package timeline

import (
	"io"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
)

func TestWindowIsBounded(t *testing.T) {
	now := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	for _, args := range [][]string{
		{"--since", "0"}, {"--since", "721h"}, {"--until", "2026-11-18T08:00:00Z"},
		{"--until", "2026-10-18T08:00:00Z"}, {"--limit", "0"}, {"--limit", "1001"}, {"extra"},
	} {
		if _, err := readWindow(args, now, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	asked, err := readWindow([]string{"--until", "2026-11-17T07:30:00Z", "--since", "5m", "--limit", "2"}, now, io.Discard)
	if err != nil || asked.limit != 2 || asked.until.Sub(asked.from) != 5*time.Minute {
		t.Fatalf("window %+v: %v", asked, err)
	}
}

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
