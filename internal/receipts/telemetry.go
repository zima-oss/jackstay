package receipts

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"

	"github.com/zima-oss/jackstay/internal/telemetry"
)

// observe turns a run-log line into spans (docs/design/observability.md, section 13): the run log
// is the seam through which `jst test` and the gate are observed, so internal/testing is not edited.
// A `start` line opens a run's span, its `run` line ends it with the verdict; `gate-start` and
// `gate` do the same for a gate; `reused` and a run that never started are verdicts of a moment.
// The log line is written first and stays the record (section 9).
func observe(log RunLog, event, key string, fields map[string]any) {
	switch event {
	case EventStart:
		runID := text(fields["run_id"])
		_, span := telemetry.Start(context.Background(), "test.run", telemetry.Options{Attributes: append(log.attributes(key),
			attribute.String(telemetry.AttrTestRunID, runID))})
		openSpans.put("run:"+runID, span)
	case EventRun, EventReused, EventWouldHit:
		span := openSpans.take("run:" + text(fields["run_id"]))
		if span == nil {
			_, span = telemetry.Start(context.Background(), "test.run", telemetry.Options{Attributes: log.attributes(key)})
		}
		outcome := text(fields["outcome"])
		if event == EventReused {
			outcome = OutcomeReused
		}
		if event == EventWouldHit {
			outcome = "would-hit"
		}
		endWithVerdict(span, outcome, fields)
	case "gate-start":
		_, span := telemetry.Start(context.Background(), "test.gate", telemetry.Options{Attributes: append(log.attributes(key),
			attribute.String(telemetry.AttrTestRunID, text(fields["gate_run_id"])),
			attribute.String(telemetry.AttrTestScope, text(fields["scope"])))})
		openSpans.put("gate:"+text(fields["gate_run_id"]), span)
	case EventGate:
		span := openSpans.take("gate:" + text(fields["gate_run_id"]))
		if span == nil {
			_, span = telemetry.Start(context.Background(), "test.gate", telemetry.Options{Attributes: log.attributes(key)})
		}
		endWithVerdict(span, text(fields["outcome"]), fields)
	}
}

func (log RunLog) attributes(key string) []attribute.KeyValue {
	found := []attribute.KeyValue{}
	if key != "" {
		found = append(found, attribute.String(telemetry.AttrTestKey, key))
	}
	if log.Where.Head != "" {
		found = append(found, attribute.String(telemetry.AttrTestHead, log.Where.Head))
	}
	if log.Where.Branch != "" {
		found = append(found, attribute.String(telemetry.AttrBranch, log.Where.Branch))
	}
	if log.Where.Worktree != "" {
		found = append(found, attribute.String(telemetry.AttrWorktree, log.Where.Worktree))
	}
	if len(log.Command) > 0 {
		found = append(found, attribute.StringSlice(telemetry.AttrChildArgv, log.Command))
	}
	return found
}

// notFailures are the outcomes that are not failures of the run.
var notFailures = map[string]bool{OutcomePassed: true, OutcomeReused: true, "exited-0": true, "would-hit": true}

func endWithVerdict(span *telemetry.Span, outcome string, fields map[string]any) {
	verdict := []attribute.KeyValue{attribute.String(telemetry.AttrTestOutcome, outcome)}
	// The run log's count of tests (`evidence_count`; absent or null: nothing counted them).
	if number, isNumber := wholeNumber(fields["evidence_count"]); isNumber {
		verdict = append(verdict, attribute.Int64(telemetry.AttrTestCount, number))
	}
	if output := text(fields["output"]); output != "" {
		span.Set(attribute.String(telemetry.AttrTestOutput, output))
	}
	if status, isNumber := wholeNumber(fields["status"]); isNumber {
		span.Set(attribute.Int64(telemetry.AttrExitCode, status))
	}
	span.Event(telemetry.EventTestVerdict, verdict...)
	if !notFailures[outcome] {
		span.Fail(strings.ReplaceAll(outcome, "-", "_"), "")
	}
	span.End(outcome)
}

func text(value any) string {
	if value == nil {
		return ""
	}
	if found, ok := value.(string); ok {
		return found
	}
	return fmt.Sprint(value)
}

func wholeNumber(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	}
	return 0, false
}

// spanTable holds the spans opened by a start line until their end line.
type spanTable struct {
	lock  sync.Mutex
	spans map[string]*telemetry.Span
}

var openSpans = &spanTable{spans: map[string]*telemetry.Span{}}

func (table *spanTable) put(name string, span *telemetry.Span) {
	table.lock.Lock()
	defer table.lock.Unlock()
	table.spans[name] = span
}

func (table *spanTable) take(name string) *telemetry.Span {
	table.lock.Lock()
	defer table.lock.Unlock()
	span := table.spans[name]
	delete(table.spans, name)
	return span
}
