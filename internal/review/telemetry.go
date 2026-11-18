package review

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"go.opentelemetry.io/otel/attribute"

	"github.com/zima-oss/jackstay/internal/review/codex"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/genai"
)

// startAttempt is the span of an attempt (docs/design/observability.md, section 2): under it each
// reviewer's agent span.
func startAttempt(ctx context.Context, planned plan, folder string) (context.Context, *telemetry.Span) {
	return telemetry.Start(ctx, "review.attempt", telemetry.Options{Attributes: []attribute.KeyValue{
		attribute.String(telemetry.AttrReviewTask, planned.task), attribute.Int(telemetry.AttrReviewRound, planned.number),
		attribute.Int(telemetry.AttrReviewAttempt, planned.attempt), attribute.String(telemetry.AttrReviewStore, folder),
		attribute.String(telemetry.AttrHead, planned.commit)}})
}

// endAttempt records the round's verdict (or why there is none) and ends the span.
func endAttempt(span *telemetry.Span, record *RoundRecord, failure string, status int, folder string) {
	span.Set(attribute.String(telemetry.AttrReviewStore, folder), attribute.Int(telemetry.AttrExitCode, status))
	if record == nil {
		span.Fail("no_round", failure)
		span.End("no-round")
		return
	}
	verdict := "clear"
	switch {
	case len(record.Outcome.RedesignDue) > 0:
		verdict = "redesign-due"
	case record.Outcome.Blocks:
		verdict = "blocks"
	}
	attributes := []attribute.KeyValue{attribute.String(telemetry.AttrReviewOutcome, verdict),
		attribute.Int(telemetry.AttrReviewRound, record.Round), attribute.Int(telemetry.AttrReviewDisposition, status)}
	counts := map[string]int{}
	for _, finding := range record.Outcome.Ledger {
		if finding.Status == StatusOpen {
			counts[finding.Severity]++
		}
	}
	for severity, count := range counts {
		attributes = append(attributes, attribute.Int(telemetry.AttrReviewFindings+severity, count))
	}
	span.Event(telemetry.EventReviewVerdict, attributes...)
	span.End(verdict)
}

// startAgent is a reviewer's agent span; the run's process span goes under it.
func startAgent(ctx context.Context, name string) (context.Context, *telemetry.Span) {
	agentCtx, span := genai.StartAgentRun(ctx, name)
	span.Set(attribute.String(telemetry.AttrReviewReviewer, name))
	return agentCtx, span
}

// endAgent annotates the agent span from what the run left in jackstay's store (its events.jsonl) and
// its outcome, then ends it — after the run is over, so nothing is lost.
func endAgent(span *telemetry.Span, run ReviewerRun, folder string) {
	agentRun := codexRunOf(filepath.Join(folder, "events.jsonl"))
	agentRun.Provider, agentRun.Agent, agentRun.Model, agentRun.Effort = "openai", run.Reviewer, run.Model, run.Effort
	genai.FinishAgentRun(span, agentRun)
	span.Set(attribute.String(telemetry.AttrReviewStore, folder))
	if run.Outcome != codex.OutcomeDone {
		errorType := run.Outcome
		if run.Outcome == codex.OutcomeFailed && run.Exit > 0 {
			errorType = fmt.Sprintf("exit_%d", run.Exit)
		}
		span.Fail(errorType, run.Reason)
	}
	span.End(run.Outcome)
}

// codexEvent is what is read of `codex exec --json`'s lines.
type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Usage    *struct {
		Input           int64 `json:"input_tokens"`
		CachedInput     int64 `json:"cached_input_tokens"`
		CacheWriteInput int64 `json:"cache_write_input_tokens"`
		Output          int64 `json:"output_tokens"`
		ReasoningOutput int64 `json:"reasoning_output_tokens"`
	} `json:"usage"`
}

// codexRunOf reads a run's thread and its turns' usage; a file that is missing or torn gives what
// could be read.
func codexRunOf(place string) genai.AgentRun {
	run := genai.AgentRun{}
	data, err := os.ReadFile(place) // #nosec G304 -- the round's own events
	if err != nil {
		return run
	}
	lines := bufio.NewScanner(bytes.NewReader(data))
	lines.Buffer(make([]byte, 64<<10), 16<<20)
	for lines.Scan() {
		var seen codexEvent
		if json.Unmarshal(lines.Bytes(), &seen) != nil {
			continue
		}
		switch seen.Type {
		case "thread.started":
			run.ThreadID = seen.ThreadID
		case "turn.completed", "turn.failed":
			usage := genai.Usage{}
			if seen.Usage != nil {
				usage = genai.Usage{Input: seen.Usage.Input, CachedInput: seen.Usage.CachedInput,
					CacheWrite: seen.Usage.CacheWriteInput, Output: seen.Usage.Output, ReasoningOutput: seen.Usage.ReasoningOutput}
			}
			run.Turns = append(run.Turns, usage)
			run.TurnFailed = append(run.TurnFailed, seen.Type == "turn.failed")
		}
	}
	return run
}
