//go:build integration

package status

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

func TestStatusJoinsMixedDeliveryStagesWithoutPrivateFields(t *testing.T) {
	const settings = `{"jackstay_schema":2,"project":"delivery"}`
	repository := testrepo.New(t, map[string]string{"jackstay.json": settings})
	linked := filepath.Join(filepath.Dir(repository.Root), "linked")
	repository.Git("worktree", "add", "-q", "-b", "linked", linked)
	nested := filepath.Join(repository.Root, "independent")
	repository.Git("init", "-q", "-b", "main", nested)
	if err := os.WriteFile(filepath.Join(nested, "jackstay.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	until := time.Now().Truncate(time.Second).Add(-5 * time.Minute)
	from := until.Add(-10 * time.Minute)
	point := func(minutes int) time.Time { return from.Add(time.Duration(minutes) * time.Minute) }
	const private = "private-fixture-prompt-and-arguments"
	head := strings.Repeat("a", 40)
	journals := map[string]*bytes.Buffer{}
	add := func(span journal.Span) {
		t.Helper()
		date := span.End.In(time.Local).Format(time.DateOnly)
		if journals[date] == nil {
			journals[date] = new(bytes.Buffer)
		}
		writeSpan(t, journals[date], span)
	}
	span := func(name string, trace, key, parent int, start, end time.Time, attrs map[string]any, events ...journal.Event) journal.Span {
		seen := journal.Span{Name: name, TraceID: fmt.Sprintf("%032x", trace), SpanID: fmt.Sprintf("%016x", key),
			Start: start, End: end, Attributes: attrs, Events: events}
		if parent > 0 {
			seen.ParentID = fmt.Sprintf("%016x", parent)
		}
		return seen
	}

	// Keep records in arbitrary append order. The most recent rows should win the limit.
	add(span("review.attempt", 1, 3, 2, point(5), point(6), map[string]any{
		telemetry.AttrOutcome: "clear", telemetry.AttrHead: head, telemetry.AttrReviewTask: private,
	}))
	add(span("jst checks", 1, 1, 0, from.Add(-time.Second), from, map[string]any{
		telemetry.AttrCommand: "checks", telemetry.AttrGitHubOperation: "status", telemetry.AttrCwd: repository.Root,
		telemetry.AttrGitHubRepository: "Example/Remote", telemetry.AttrGitHubPull: 24, telemetry.AttrOutcome: "ok",
		telemetry.AttrHead: head, telemetry.AttrArgs: private,
	}))
	add(span("test.run", 1, 2, 1, point(1), point(4), map[string]any{
		telemetry.AttrOutcome: "passed", telemetry.AttrTestHead: head, telemetry.AttrTestRunID: "1a2f-987",
		telemetry.AttrTestOutput: private,
	}))
	add(span("invoke_agent codex", 1, 4, 3, point(6), point(7), map[string]any{
		"gen_ai.operation.name": "invoke_agent", "gen_ai.usage.input_tokens": 123, "gen_ai.usage.output_tokens": 45,
		telemetry.AttrReviewReviewer: "codex", telemetry.AttrOutcome: "done", "gen_ai.request.model": private,
	}))
	add(span("jst bg", 2, 1, 0, point(5), until.Add(time.Second), map[string]any{
		telemetry.AttrCommand: "bg", telemetry.AttrCwd: linked, telemetry.AttrArgs: private,
	}, journal.Event{Name: telemetry.EventBgStarted, Time: point(8), Attributes: map[string]any{
		telemetry.AttrBgID: "1760000000-123", telemetry.AttrBgName: private,
	}}, journal.Event{Name: telemetry.EventBgEnded, Time: until}))
	add(span("test.run", 1, 5, 1, point(2), point(3), map[string]any{
		telemetry.AttrWorktree: nested, telemetry.AttrTestRunID: "2def-111",
	}))
	add(span("review.attempt", 3, 1, 0, point(5), point(9), map[string]any{
		telemetry.AttrReviewTask: "no-location", telemetry.AttrOutcome: "clear",
	}))
	add(span("test.run", 4, 1, 0, point(9), until, map[string]any{
		telemetry.AttrWorktree: repository.Root, telemetry.AttrTestRunID: "3abc-111",
	}))
	// A machine-wide stop has a caller's path, but lacks the stopped group's location.
	add(span("jst procs stop", 5, 1, 0, point(8), point(9), map[string]any{telemetry.AttrCwd: linked},
		journal.Event{Name: telemetry.EventBgStopped, Time: point(8)}))

	folder := filepath.Join(repository.Home, homedir.MarkerName("delivery"), telemetry.FolderName)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	for date, data := range journals {
		if err := os.WriteFile(filepath.Join(folder, journal.FileName(date)), data.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(limit int) Repository {
		t.Helper()
		report := Read(context.Background(), []string{repository.Root}, window{from: from, until: until, limit: limit}, true, time.Now())
		events, err := json.Marshal(report.Repositories[0].Events)
		if err != nil {
			t.Fatal(err)
		}
		for _, hidden := range []string{private, nested, repository.Root, linked, "Example/Remote", "2def-111", "3abc-111"} {
			if strings.Contains(string(events), hidden) {
				t.Fatalf("events leaked %q: %s", hidden, events)
			}
		}
		return report.Repositories[0]
	}

	got := read(10)
	stages := []string{}
	for _, entry := range got.Events {
		stages = append(stages, entry.Stage)
	}
	want := []string{"checks.status", "test.run", "review.attempt", "review.agent", "background.started"}
	if got.RepositoryID == "" || !slices.Equal(stages, want) || got.Counts.OmittedEvents != 0 {
		t.Fatalf("timeline %+v", got)
	}
	pr, test, review, agent, background := got.Events[0], got.Events[1], got.Events[2], got.Events[3], got.Events[4]
	if pr.Pull != pullID("example/remote", 24) || !pr.At.Equal(from) || pr.DurationMS != 1000 ||
		test.ParentSpanID != pr.SpanID || test.TraceID != pr.TraceID || test.WorktreeID != pr.WorktreeID ||
		test.Head != pr.Head || test.Status != "passed" || test.TestRunID != "1a2f-987" ||
		review.ParentSpanID != test.SpanID || agent.ParentSpanID != review.SpanID ||
		agent.Tokens == nil || agent.Tokens.Input != 123 || agent.Tokens.Output != 45 ||
		background.At.Equal(until) || background.WorktreeID == test.WorktreeID || background.BackgroundID != "1760000000-123" {
		t.Fatalf("stages did not correlate: %+v", got.Events)
	}
	clipped := read(2)
	if len(clipped.Events) != 2 || clipped.Counts.OmittedEvents != 3 ||
		clipped.Events[0].Stage != "review.agent" || clipped.Events[1].Stage != "background.started" {
		t.Fatalf("output limit: %+v", clipped)
	}
}

func writeSpan(t *testing.T, destination io.Writer, span journal.Span) {
	t.Helper()
	fields := func(attrs map[string]any) []map[string]any {
		reduced := make([]map[string]any, 0, len(attrs))
		for key, value := range attrs {
			one := map[string]any{"key": key}
			switch typed := value.(type) {
			case string:
				one["value"] = map[string]any{"stringValue": typed}
			case int:
				one["value"] = map[string]any{"intValue": strconv.Itoa(typed)}
			default:
				t.Fatalf("unknown fixture type %T", value)
			}
			reduced = append(reduced, one)
		}
		return reduced
	}
	events := make([]map[string]any, 0, len(span.Events))
	for _, event := range span.Events {
		events = append(events, map[string]any{"name": event.Name, "timeUnixNano": strconv.FormatInt(event.Time.UnixNano(), 10),
			"attributes": fields(event.Attributes)})
	}
	entry := map[string]any{"traceId": span.TraceID, "spanId": span.SpanID, "parentSpanId": span.ParentID, "name": span.Name,
		"startTimeUnixNano": strconv.FormatInt(span.Start.UnixNano(), 10), "endTimeUnixNano": strconv.FormatInt(span.End.UnixNano(), 10),
		"attributes": fields(span.Attributes), "events": events}
	data := map[string]any{"resourceSpans": []any{map[string]any{"scopeSpans": []any{map[string]any{"spans": []any{entry}}}}}}
	if err := json.NewEncoder(destination).Encode(data); err != nil {
		t.Fatal(err)
	}
}
