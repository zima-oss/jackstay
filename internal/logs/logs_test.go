package logs

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
)

func TestParse(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	options, err := parse([]string{"--since", "2h", "--command", "test"}, now)
	if err != nil || !options.Since.Equal(now.Add(-2*time.Hour)) || options.Scope.All || options.Command != "test" {
		t.Fatalf("%+v %v", options, err)
	}
	options, err = parse([]string{"--session", "s"}, now)
	if err != nil || !options.Scope.All || !options.Since.Equal(now.AddDate(0, 0, -30)) {
		t.Fatalf("a session reads every project for 30 days: %+v %v", options, err)
	}
	options, err = parse([]string{"--trace", "abc"}, now)
	if err != nil || !options.Tree {
		t.Fatalf("a trace is a tree: %+v", options)
	}
	for _, bad := range [][]string{{"--since", "soon"}, {"extra"}, {"--nope"}} {
		if _, err := parse(bad, now); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
}

func spanOf(trace, parent, session string, start time.Time, guardCommand, toolUse string) journal.Span {
	name, command := "jst review", "review"
	attributes := map[string]any{telemetry.AttrCommand: command, telemetry.AttrClaudeSession: session}
	if toolUse != "" {
		name = "jst guard"
		attributes[telemetry.AttrCommand] = "guard"
		attributes[telemetry.AttrClaudeToolUse] = toolUse
		attributes[telemetry.AttrGuardCommand] = guardCommand
	}
	return journal.Span{TraceID: trace, SpanID: name + start.String(), ParentID: parent, Name: name, Start: start,
		End: start.Add(time.Millisecond), Attributes: attributes}
}

func TestToolCallExactAndCandidates(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	spans := []journal.Span{
		spanOf("t1", "toolA", "s", now, "jst review", "call-a"),
		spanOf("t1", "toolB", "s", now.Add(time.Second), "jst review", "call-b"),
		spanOf("t1", "toolB", "s", now.Add(2*time.Second), "", ""), // call-b's command, by trace
		spanOf("t1", "toolA", "s", now.Add(3*time.Second), "", ""), // call-a's command, by trace
	}
	answer := FindToolCall(spans, "call-a")
	if len(answer.Exact) != 1 || !answer.Exact[0].Start.Equal(now.Add(3*time.Second)) || len(answer.Candidates) != 0 {
		t.Fatalf("exact %+v candidates %+v", answer.Exact, answer.Candidates)
	}
	unrelated := []journal.Span{
		spanOf("t2", "", "s", now, "jst review", "call-c"),
		spanOf("t3", "", "s", now.Add(time.Second), "", ""),
		spanOf("t4", "", "other", now.Add(time.Second), "", ""),
		spanOf("t5", "", "s", now.Add(time.Minute), "", ""),
	}
	answer = FindToolCall(unrelated, "call-c")
	if len(answer.Exact) != 0 || len(answer.Candidates) != 1 || answer.Candidates[0].TraceID != "t3" {
		t.Fatalf("candidates %+v", answer.Candidates)
	}
	var out bytes.Buffer
	Print(&out, append(spans, unrelated...), Options{ToolCall: "call-a", JSON: true})
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 2 {
		t.Fatalf("--json --tool-call printed %d spans:\n%s", len(lines), out.String())
	}
}
