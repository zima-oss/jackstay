package golden

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/guard"
	"github.com/zima-oss/jackstay/internal/logs"
	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/review/codex"
	"github.com/zima-oss/jackstay/internal/stats"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/genai"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/telemetry/telemetrytest"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const session = "8a2f0c1e-0000-4000-8000-00000000c1a0"

// --- the scenarios' machinery -----------------------------------------------------------------

// fakeCodex writes a Codex that prints events, writes an answer (when given one) to its -o path
// and exits with status.
func fakeCodex(t *testing.T, events []string, answer *review.Answer, status int) string {
	t.Helper()
	place := filepath.Join(t.TempDir(), "codex")
	var script strings.Builder
	script.WriteString("#!/bin/sh\nout=\"\"\nwhile [ $# -gt 0 ]; do case \"$1\" in -o) out=\"$2\"; shift;; esac; shift; done\ncat >/dev/null\n")
	script.WriteString("cat <<'EVENTS'\n" + strings.Join(events, "\n") + "\nEVENTS\n")
	if answer != nil {
		if answer.Findings == nil {
			answer.Findings = []review.AnswerFinding{}
		}
		if answer.Prior == nil {
			answer.Prior = []review.PriorAnswer{}
		}
		data, _ := json.Marshal(answer)
		script.WriteString("cat > \"$out\" <<'ANSWER'\n" + string(data) + "\nANSWER\n")
	}
	fmt.Fprintf(&script, "exit %d\n", status)
	if err := os.WriteFile(place, []byte(script.String()), 0o700); err != nil {
		t.Fatal(err)
	}
	return place
}

var recordedStream = []string{
	`{"type":"thread.started","thread_id":"0199aaaa-bbbb-7ccc-8ddd-eeeeffff0001"}`,
	`{"type":"turn.started"}`,
	`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"reading"}}`,
	`{"type":"turn.completed","usage":{"input_tokens":1200,"cached_input_tokens":1000,"cache_write_input_tokens":0,"output_tokens":80,"reasoning_output_tokens":40}}`,
	`{"type":"turn.started"}`,
	`{"type":"turn.completed","usage":{"input_tokens":300,"cached_input_tokens":200,"cache_write_input_tokens":5,"output_tokens":20,"reasoning_output_tokens":10}}`,
}

// round is a `jst review` round with the real Codex runner over a fake repository.
func round(t *testing.T, command string, store string) review.Round {
	t.Helper()
	settings := review.Defaults()
	settings.Use = []string{"codex"}
	settings.Reviewers["codex"] = review.ReviewerSettings{Name: "codex", Command: command, Effort: "high", TimeoutMinutes: 1}
	dir := t.TempDir()
	return review.Round{Settings: settings, Repo: newFakeRepo(), Store: review.Store{Root: store},
		Machine: review.Machine{RunReviewer: codex.Run,
			Probe:            func(context.Context, string, string) (string, error) { return "codex-cli 0.0.0", nil },
			CheckEnvironment: func([]review.ReviewerSettings) string { return "" }, RecordLogin: func(string) {},
			SlotsDir: filepath.Join(dir, "slots"), StartLockPath: filepath.Join(dir, "start.lock"),
			Now: time.Now, TreeAlive: func(string) bool { return true }, SlotPoll: 10 * time.Millisecond},
		Out: &bytes.Buffer{}, Errors: &bytes.Buffer{}}
}

// runReview is one `jst review` process: its command span begun, the round run, the span finished.
func runReview(t *testing.T, home string, command, store string, env map[string]string) review.Result {
	t.Helper()
	t.Setenv("JACKSTAY_HOME", home)
	separateProcess(t)
	for name, value := range env {
		t.Setenv(name, value)
	}
	telemetry.Reset()
	telemetry.Begin("review", []string{"--task", "golden"})
	result := round(t, command, store).Run(context.Background(), review.Options{Task: "golden"})
	telemetry.Finish(result.Status)
	return result
}

// hook is one guard process given a call.
func hook(t *testing.T, home string, input string) string {
	t.Helper()
	t.Setenv("JACKSTAY_HOME", home)
	separateProcess(t)
	telemetry.Reset()
	telemetry.Begin("guard", nil)
	var stdout, stderr bytes.Buffer
	status := guard.Hook(strings.NewReader(input), &stdout, &stderr)
	telemetry.Finish(status)
	return stdout.String()
}

// separateProcess clears what an earlier simulated process left in the environment: each begins as
// a process Claude started (no TRACEPARENT unless a scenario gives one).
func separateProcess(t *testing.T) {
	t.Helper()
	t.Setenv(telemetry.TraceparentVariable, "")
	_ = os.Unsetenv(telemetry.TraceparentVariable)
}

func bashCall(command, toolUseID, agentType, cwd string) string {
	call := map[string]any{"session_id": session, "tool_name": "Bash", "tool_use_id": toolUseID, "cwd": cwd,
		"hook_event_name": "PreToolUse", "tool_input": map[string]any{"command": command}}
	if agentType != "" {
		call["agent_type"], call["agent_id"] = agentType, "agent-1"
	}
	data, _ := json.Marshal(call)
	return string(data)
}

func freshHome(t *testing.T) string {
	t.Helper()
	home := telemetrytest.Begin(t, "golden", nil, nil)
	telemetry.Reset()
	return home
}

// --- normalisation and golden files ------------------------------------------------------------

// volatile are values that differ between runs and machines; they are written as "<x>".
var volatile = map[string]bool{"process.pid": true, "process.parent_pid": true, "jackstay.child.pid": true,
	"jackstay.build.commit": true, "service.version": true, "jackstay.guard.answer_ms": true, "jackstay.git.calls": true,
	"jackstay.git.seconds": true, "jackstay.caller.argv": true, "jackstay.test.run_id": true, "exception.stacktrace": true,
	"jackstay.cwd": true, "jackstay.review.store": true, "jackstay.child.argv": true, "jackstay.lock.path": true, "jackstay.args": true}

type normalSpan struct {
	Name       string         `json:"name"`
	Kind       int            `json:"kind"`
	Parent     string         `json:"parent"`
	Status     int            `json:"status"`
	Scope      string         `json:"scope"`
	SchemaURL  string         `json:"schema_url,omitempty"`
	Attributes map[string]any `json:"attributes"`
	Events     []normalEvent  `json:"events,omitempty"`
}

type normalEvent struct {
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

func normalise(spans []journal.Span, temp []string) []normalSpan {
	names := map[string]string{}
	for index, span := range spans {
		names[span.TraceID+"/"+span.SpanID] = fmt.Sprintf("#%d %s", index, span.Name)
	}
	clean := func(attributes map[string]any) map[string]any {
		found := map[string]any{}
		for key, value := range attributes {
			if volatile[key] {
				value = "<x>"
			}
			if text, ok := value.(string); ok {
				for _, folder := range temp {
					text = strings.ReplaceAll(text, folder, "<tmp>")
				}
				value = text
			}
			found[key] = value
		}
		return found
	}
	var normal []normalSpan
	for _, span := range spans {
		parent := ""
		if span.ParentID != "" {
			parent = names[span.TraceID+"/"+span.ParentID]
			if parent == "" {
				parent = "<outside>"
			}
		}
		entry := normalSpan{Name: span.Name, Kind: span.Kind, Parent: parent, Status: span.StatusCode, Scope: span.Scope,
			SchemaURL: span.SchemaURL, Attributes: clean(span.Attributes)}
		for _, event := range span.Events {
			entry.Events = append(entry.Events, normalEvent{Name: event.Name, Attributes: clean(event.Attributes)})
		}
		normal = append(normal, entry)
	}
	return normal
}

// order makes the spans' order independent of goroutine timing: by trace order of command, then by
// name within a parent.
func order(spans []journal.Span) []journal.Span {
	sort.SliceStable(spans, func(left, right int) bool {
		if !spans[left].Start.Equal(spans[right].Start) {
			return spans[left].Start.Before(spans[right].Start)
		}
		return spans[left].Name < spans[right].Name
	})
	return spans
}

func checkGolden(t *testing.T, name string, spans []journal.Span, temp ...string) {
	t.Helper()
	temp = append(temp, os.TempDir())
	sort.Slice(temp, func(left, right int) bool { return len(temp[left]) > len(temp[right]) })
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(normalise(spans, temp)); err != nil {
		t.Fatal(err)
	}
	data := buffer.Bytes()
	data = regexp.MustCompile(`"<tmp>[^"]*"`).ReplaceAll(data, []byte(`"<tmp>"`))
	place := filepath.Join("testdata", name+".json")
	if *update {
		if err := os.MkdirAll("testdata", 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(place, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(place)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if strings.TrimSpace(string(want)) != strings.TrimSpace(string(data)) {
		t.Fatalf("%s differs from its golden file (-update rewrites it; the diff is the review):\n%s", name, data)
	}
}

// --- the five scenarios ------------------------------------------------------------------------

func TestModelCall(t *testing.T) {
	home := freshHome(t)
	store := t.TempDir()
	codexPath := fakeCodex(t, recordedStream, &review.Answer{Verdict: "approve", Summary: "fine"}, 0)
	result := runReview(t, home, codexPath, store, nil)
	if result.Status != review.ExitClear {
		t.Fatalf("review: %+v", result)
	}
	spans := order(telemetrytest.Spans(t, home))
	checkGolden(t, "model-call", spans, store, home, filepath.Dir(codexPath))

	agents := telemetrytest.Named(spans, "invoke_agent codex")
	if len(agents) != 1 {
		t.Fatalf("%d agent spans", len(agents))
	}
	run, _ := genai.AgentRunOf(agents[0])
	if run.ThreadID != "0199aaaa-bbbb-7ccc-8ddd-eeeeffff0001" || run.Turns[0].Input != 1500 || run.Turns[0].Output != 100 ||
		run.Turns[0].CachedInput != 1200 || run.Turns[0].ReasoningOutput != 50 || agents[0].SchemaURL != genai.SchemaURL {
		t.Fatalf("agent run %+v (schema %q)", run, agents[0].SchemaURL)
	}
	report, _ := stats.Compute("reviews", stats.Input{Spans: spans, From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if len(report.Rows) != 1 {
		t.Fatalf("reviews: %+v", report)
	}
	row := report.Rows[0]
	row = row[1:] // the project
	if row[0] != "golden" || row[1] != 1 || row[2] != 1 || row[3] != 1 || row[4] != "0" || row[5] != int64(1500) ||
		row[7] != int64(100) || row[10] != "clear" {
		t.Fatalf("reviews row %v", row)
	}
}

func TestToolCall(t *testing.T) {
	home := freshHome(t)
	folder := t.TempDir()
	answer := hook(t, home, bashCall("rm -rf src", "toolu_golden_1", "", folder))
	if !strings.Contains(answer, `"permissionDecision":"deny"`) {
		t.Fatalf("not refused: %s", answer)
	}
	hook(t, home, bashCall("git status --short", "toolu_golden_2", "", folder))
	spans := order(telemetrytest.Spans(t, home))
	checkGolden(t, "tool-call", spans, folder, home)

	found := logs.FindToolCall(spans, "toolu_golden_1")
	if len(found.Decisions) != 1 || found.Decisions[0].Text(telemetry.AttrGuardOutcome) != "refused" {
		t.Fatalf("tool call: %+v", found)
	}
	report, _ := stats.Compute("refusals", stats.Input{Spans: spans})
	if len(report.Rows) != 1 || report.Rows[0][0] != "rm-recursive" || report.Rows[0][2] != 1 || report.Rows[0][4] != 1 {
		t.Fatalf("refusals: %+v", report.Rows)
	}
}

func TestRetry(t *testing.T) {
	home := freshHome(t)
	store := t.TempDir()
	failing := fakeCodex(t, recordedStream[:4], nil, 1)
	if result := runReview(t, home, failing, store, nil); result.Status != review.ExitReviewer {
		t.Fatalf("first attempt: %+v", result)
	}
	answering := fakeCodex(t, recordedStream, &review.Answer{Verdict: "approve", Summary: "fine"}, 0)
	if result := runReview(t, home, answering, store, nil); result.Status != review.ExitClear {
		t.Fatalf("second attempt: %+v", result)
	}
	spans := order(telemetrytest.Spans(t, home))
	checkGolden(t, "retry", spans, store, home, filepath.Dir(failing), filepath.Dir(answering))

	agents := telemetrytest.Named(spans, "invoke_agent codex")
	if len(agents) != 2 || agents[0].StatusCode != journal.StatusError || agents[0].Text(telemetry.AttrErrorType) != "exit_1" ||
		agents[1].StatusCode == journal.StatusError {
		t.Fatalf("agents %+v", agents)
	}
	report, _ := stats.Compute("reviews", stats.Input{Spans: spans})
	row := report.Rows[0][1:]
	if row[1] != 1 || row[2] != 2 || row[3] != 2 || row[4] != "exit_1×1" || row[5] != int64(1200+1500) {
		t.Fatalf("reviews row %v", row)
	}
}

var registerBoom sync.Once

func TestError(t *testing.T) {
	home := freshHome(t)
	// The guard given an empty standard input: it fails open and names its caller.
	answer := hook(t, home, "")
	if !strings.Contains(answer, "did NOT check") {
		t.Fatalf("not failed open: %s", answer)
	}
	// A command that panics: its span keeps the stack, and the panic goes on.
	registerBoom.Do(func() {
		cli.Register(cli.Command{Name: "golden-boom", Summary: "panics (a test's)", Run: func([]string, io.Reader, io.Writer, io.Writer) int {
			panic(errors.New("golden fault"))
		}})
	})
	separateProcess(t)
	telemetry.Reset()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic was swallowed")
			}
		}()
		cli.Main("jst", []string{"golden-boom"})
	}()
	spans := order(telemetrytest.Spans(t, home))
	checkGolden(t, "error", spans, home)

	guardReport, _ := stats.Compute("guard", stats.Input{Spans: spans})
	if len(guardReport.Notes) != 1 || !strings.Contains(guardReport.Notes[0], "failed open") ||
		!strings.Contains(guardReport.Notes[0], "caller pid") || strings.Contains(guardReport.Notes[0], ": unknown") {
		t.Fatalf("guard notes %q", guardReport.Notes)
	}
	var printed bytes.Buffer
	logs.Print(&printed, spans, logs.Options{Outcome: "error", Tree: true})
	if !strings.Contains(printed.String(), "golden fault") || !strings.Contains(printed.String(), "goroutine") {
		t.Fatalf("the stack is not shown:\n%s", printed.String())
	}
}

func TestNestedAgent(t *testing.T) {
	home := freshHome(t)
	folder := t.TempDir()
	hook(t, home, bashCall("jst review --task golden", "toolu_golden_3", "Explore", folder))
	store := t.TempDir()
	codexPath := fakeCodex(t, recordedStream, &review.Answer{Verdict: "approve", Summary: "fine"}, 0)
	parent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	runReview(t, home, codexPath, store, map[string]string{telemetry.TraceparentVariable: parent,
		telemetry.ClaudeSessionVariable: session})
	spans := order(telemetrytest.Spans(t, home))
	checkGolden(t, "nested-agent", spans, folder, store, home, filepath.Dir(codexPath))

	guardSpan := telemetrytest.Named(spans, "jst guard")[0]
	if call, _ := genai.ToolCallOf(guardSpan); call.AgentType != "Explore" {
		t.Fatalf("subagent %+v", call)
	}
	reviewSpan := telemetrytest.Named(spans, "jst review")[0]
	if reviewSpan.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || reviewSpan.ParentID != "00f067aa0ba902b7" {
		t.Fatalf("not under the inherited parent: %+v", reviewSpan)
	}
	for _, span := range spans {
		if genai.SessionOf(span) != session && !genai.IsAgentRun(span) {
			t.Fatalf("%s has no session", span.Name)
		}
	}
	selected := logs.Select(spans, logs.Options{Session: session, Tree: true})
	if len(selected) != len(spans) {
		t.Fatalf("the session's log has %d of %d spans", len(selected), len(spans))
	}
	found := logs.FindToolCall(spans, "toolu_golden_3")
	if len(found.Exact) != 0 || len(found.Candidates) != 1 || found.Candidates[0].Name != "jst review" {
		t.Fatalf("the call's command: %+v", found)
	}
	report, _ := stats.Compute("sessions", stats.Input{Spans: spans, From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)})
	if len(report.Rows) != 1 || report.Rows[0][0] != session || report.Rows[0][2] != 1 || report.Rows[0][9] != int64(1600) {
		t.Fatalf("sessions %v", report.Rows)
	}
}
