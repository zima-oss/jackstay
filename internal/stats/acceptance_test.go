package stats_test

// The questions the journal must answer (docs/design/observability.md, section 11), each over a
// journal written through jackstay's real seams — the guard hook, receipts' run log, flock, supervise,
// procs — in a jackstay home of the test's own. A3, A4, A8, A10, A13 are in internal/telemetry/golden;
// A14 in internal/telemetry.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/guard"
	"github.com/zima-oss/jackstay/internal/logs"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/receipts"
	"github.com/zima-oss/jackstay/internal/stats"
	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/telemetry/telemetrytest"
)

type clock struct{}

func (clock) Now() time.Time { return time.Now() }

// process runs body as one jackstay process of command (its span begun and finished).
func process(t *testing.T, home, command string, env map[string]string, body func() int) {
	t.Helper()
	t.Setenv("JACKSTAY_HOME", home)
	t.Setenv(telemetry.TraceparentVariable, "")
	_ = os.Unsetenv(telemetry.TraceparentVariable)
	for name, value := range env {
		t.Setenv(name, value)
	}
	telemetry.Reset()
	if processClock != nil {
		telemetry.SetClock(processClock)
	}
	telemetry.Begin(command, nil)
	telemetry.Finish(body())
}

// processClock, when set, is the clock of each simulated process: a test whose report depends on
// the time between calls gives them fixed times, not the wall clock.
var processClock func() time.Time

func guardCall(t *testing.T, home, session, command string, background bool) {
	t.Helper()
	call := map[string]any{"session_id": session, "tool_name": "Bash", "tool_use_id": "toolu_" + command,
		"cwd": filepath.Join(home, "demo"), "tool_input": map[string]any{"command": command, "run_in_background": background}}
	_ = os.MkdirAll(filepath.Join(home, "demo"), 0o700)
	data, _ := json.Marshal(call)
	process(t, home, "guard", nil, func() int {
		return guard.HookWithin(2*time.Minute, bytes.NewReader(data), &bytes.Buffer{}, &bytes.Buffer{}) // decisions, not speed
	})
}

func home(t *testing.T) string {
	t.Helper()
	found := telemetrytest.Begin(t, "acceptance", nil, nil)
	telemetry.Reset()
	return found
}

func wide() (time.Time, time.Time) { return time.Now().Add(-time.Hour), time.Now().Add(time.Hour) }

// A5: refusals per rule, retried within two minutes or given up.
func TestRefusalsRetriedAndGivenUp(t *testing.T) {
	folder := home(t)
	// Each call a second after the one before, whatever the wall clock does (a laptop may sleep);
	// anchored at the test's start so the journal reader's window still holds them.
	start := time.Now()
	t.Cleanup(func() { processClock = nil })
	for index, call := range [][2]string{{"session-a", "rm -rf src"}, {"session-a", "jst trash src"},
		{"session-b", "rm -rf build-output"}, {"session-b", "rm -rf build-output"}} {
		at := start.Add(time.Duration(index) * time.Second)
		processClock = func() time.Time { return at }
		guardCall(t, folder, call[0], call[1], false)
	}
	spans := telemetrytest.Spans(t, folder)
	report, _ := stats.Compute("refusals", stats.Input{Spans: spans})
	if len(report.Rows) != 1 {
		t.Fatalf("%v", report.Rows)
	}
	row := report.Rows[0]
	if row[0] != "rm-recursive" || row[2] != 3 || row[3] != 100.0 || row[4] != 1 || row[6] != 2 {
		t.Fatalf("rule, refusals, share, retried, given up: %v", row)
	}
}

func testRun(t *testing.T, folder, key, head, outcome string) {
	t.Helper()
	process(t, folder, "test", nil, func() int {
		log := receipts.RunLog{Path: filepath.Join(t.TempDir(), "runs.jsonl"), Project: "demo", Command: []string{"just", "go"},
			Where: receipts.Where{Head: head, Branch: "main"}, Clock: clock{}}
		log.Write(receipts.EventStart, key, map[string]any{"run_id": key + outcome})
		status := 0
		if outcome != "passed" {
			status = 1
		}
		log.Write(receipts.EventRun, key, map[string]any{"run_id": key + outcome, "outcome": outcome, "status": status,
			"evidence_count": 3})
		return status
	})
}

// A6: a key and head with a pass and a failure is flaky; failed runs' time per project.
func TestFlakyAndTests(t *testing.T) {
	folder := home(t)
	testRun(t, folder, "key-1", "head-1", "passed")
	testRun(t, folder, "key-1", "head-1", "failed")
	testRun(t, folder, "key-2", "head-1", "passed")
	spans := telemetrytest.Spans(t, folder)
	from, to := wide()
	flaky, _ := stats.Compute("flaky", stats.Input{Spans: spans, From: from, To: to})
	if len(flaky.Rows) != 1 || flaky.Rows[0][1] != "key-1" || flaky.Rows[0][3] != 1 || flaky.Rows[0][4] != 1 {
		t.Fatalf("flaky %v", flaky.Rows)
	}
	for _, span := range telemetrytest.Named(spans, "test.run") {
		if span.Events[0].Number(telemetry.AttrTestCount) != 3 {
			t.Fatalf("no count of tests on %+v", span.Events)
		}
	}
	tests, _ := stats.Compute("tests", stats.Input{Spans: spans, From: from, To: to})
	if len(tests.Rows) != 1 || tests.Rows[0][2] != 3 || tests.Rows[0][3] != 2 || tests.Rows[0][4] != 1 {
		t.Fatalf("tests %v", tests.Rows)
	}
}

// A7 and A2: phases and lock waits; a run's tree with its child process and exit code.
func TestPhasesWaitsAndTree(t *testing.T) {
	folder := home(t)
	lockPath := filepath.Join(t.TempDir(), "test.lock")
	holder, err := flock.TryFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	process(t, folder, "test", nil, func() int {
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel()
		_, _ = flock.WaitFile(ctx, lockPath)
		holder.Release()
		if _, err := flock.WaitFile(context.Background(), lockPath); err != nil {
			t.Fatal(err)
		}
		_, phase := telemetry.Start(context.Background(), "test.step", telemetry.Options{})
		outcome, err := supervise.Run(nil, supervise.Spec{Argv: []string{"sh", "-c", "exit 3"}, Mode: supervise.Session})
		if err != nil || outcome.Code != 3 {
			t.Fatalf("%+v %v", outcome, err)
		}
		phase.End("failed")
		return 1
	})
	spans := telemetrytest.Spans(t, folder)
	report, _ := stats.Compute("phases", stats.Input{Spans: spans})
	var waits, steps []any
	for _, row := range report.Rows {
		switch {
		case row[0] == "wait" && row[1] == "test.lock":
			waits = row
		case row[0] == "phase" && row[1] == "test.step":
			steps = row
		}
	}
	if waits == nil || waits[2] != 1 || steps == nil || steps[2] != 1 {
		t.Fatalf("phases %v", report.Rows)
	}
	var printed bytes.Buffer
	logs.Print(&printed, spans, logs.Options{Trace: spans[0].TraceID, Tree: true})
	text := printed.String()
	for _, want := range []string{"jst test", "  wait test.lock", "cancelled", "  test.step", "    exec sh: sh -c", "ERROR exit_3"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the tree has no %q:\n%s", want, text)
		}
	}
}

// A9: background asked for outside jst bg (with the guard's answer), beside jst bg's own.
func TestBackground(t *testing.T) {
	folder := home(t)
	guardCall(t, folder, "session-c", "npm run dev", true)
	guardCall(t, folder, "session-c", "jst bg --for 5 -- npm run dev", true)
	process(t, folder, "bg", nil, func() int {
		deadline := 1794912150.5 // as jst bg's entry has it: a pointer
		procs.Note("start", map[string]any{"id": "bg-1", "name": "dev", "deadline": &deadline})
		var none *float64
		procs.Note("start", map[string]any{"id": "bg-2", "name": "dev", "deadline": none})
		procs.Note("end", map[string]any{"id": "bg-1", "status": 0})
		return 0
	})
	spans := telemetrytest.Spans(t, folder)
	for _, span := range telemetrytest.Named(spans, "jst bg") {
		for _, event := range span.Events {
			if deadline := event.Text(telemetry.AttrBgDeadline); event.Name != telemetry.EventBgStarted {
				continue
			} else if event.Text(telemetry.AttrBgID) == "bg-1" && deadline != "1.7949121505e+09" ||
				event.Text(telemetry.AttrBgID) == "bg-2" && deadline != "" {
				t.Fatalf("deadline %q of %s", deadline, event.Text(telemetry.AttrBgID))
			}
		}
	}
	report, _ := stats.Compute("background", stats.Input{Spans: spans})
	kinds := map[string]int{}
	for _, row := range report.Rows {
		kinds[row[2].(string)] += row[3].(int)
	}
	if kinds["asked outside jst bg"] != 1 || kinds["jst bg started"] != 2 || kinds["jst bg ended"] != 1 {
		t.Fatalf("background %v", report.Rows)
	}
}

func span(project, name, version string, start time.Time, length time.Duration) journal.Span {
	return journal.Span{TraceID: name + start.String(), SpanID: "s", Name: name, Start: start, End: start.Add(length),
		Attributes: map[string]any{telemetry.AttrCommand: strings.TrimPrefix(name, "jst ")},
		Resource:   map[string]any{telemetry.AttrProject: project, telemetry.AttrServiceVersion: version}}
}

// A11: busy time per project and day, clipped to the range; the most commands at once.
func TestLoad(t *testing.T) {
	day := time.Date(2026, 11, 17, 0, 0, 0, 0, time.Local)
	spans := []journal.Span{
		span("demo", "jst test", "0.5.0", day.Add(10*time.Hour), time.Hour),
		span("demo", "jst review", "0.5.0", day.Add(10*time.Hour+30*time.Minute), time.Hour),
		span("demo", "jst bg", "0.5.0", day.Add(-30*time.Minute), time.Hour), // crosses midnight
	}
	report, _ := stats.Compute("load", stats.Input{Spans: spans, From: day, To: day.AddDate(0, 0, 1)})
	if len(report.Rows) != 1 || report.Rows[0][2] != 3 || report.Rows[0][3] != 9000.0 || report.Rows[0][5] != 2 {
		t.Fatalf("load %v", report.Rows)
	}
	// Over two days, the command crossing midnight is counted once, on the day it ended.
	twoDays, _ := stats.Compute("load", stats.Input{Spans: spans, From: day.AddDate(0, 0, -1), To: day.AddDate(0, 0, 1)})
	commands := 0
	for _, row := range twoDays.Rows {
		commands += row[2].(int)
	}
	if len(twoDays.Rows) != 2 || commands != 3 {
		t.Fatalf("load over two days %v", twoDays.Rows)
	}
	// A command that ends exactly at midnight counts on the day that begins there.
	atMidnight := span("demo", "jst test", "0.5.0", day.Add(-time.Hour), time.Hour)
	edge, _ := stats.Compute("load", stats.Input{Spans: []journal.Span{atMidnight}, From: day.AddDate(0, 0, -1), To: day.AddDate(0, 0, 1)})
	counted := 0
	for _, row := range edge.Rows {
		counted += row[2].(int)
	}
	if counted != 1 {
		t.Fatalf("a command ending at midnight counted %d times: %v", counted, edge.Rows)
	}
}

// A12: a report split by the version that wrote the spans.
func TestGroupingRejectsUnknownReportsEvenWithNoSpans(t *testing.T) {
	for _, group := range []string{"day", "project", "version"} {
		_, err := stats.By("no-such-report", group, stats.Input{})
		if err == nil || !strings.Contains(err.Error(), "no-such-report") {
			t.Fatalf("grouping %s did not report the unknown name: %v", group, err)
		}
	}
}

func TestByVersion(t *testing.T) {
	now := time.Now()
	guardSpan := func(version, outcome string) journal.Span {
		found := span("demo", "jst guard", version, now, time.Millisecond)
		found.Attributes[telemetry.AttrGuardOutcome] = outcome
		return found
	}
	spans := []journal.Span{guardSpan("0.5.0", "allowed"), guardSpan("0.5.0", "refused"), guardSpan("0.5.1", "allowed")}
	report, err := stats.By("guard", "version", stats.Input{Spans: spans})
	if err != nil || len(report.Rows) != 2 || report.Columns[0] != "version" || report.Rows[0][2] != 2 || report.Rows[1][2] != 1 {
		t.Fatalf("by version %v %v", report.Rows, err)
	}
	// Days are the local calendar's even when the range is given in UTC.
	zone := time.FixedZone("UTC+2", 2*3600)
	previous := time.Local
	time.Local = zone
	defer func() { time.Local = previous }()
	local := span("demo", "jst test", "0.5.0", time.Date(2026, 11, 17, 1, 0, 0, 0, zone), 45*time.Minute)
	byDay, _ := stats.By("load", "day", stats.Input{Spans: []journal.Span{local},
		From: time.Date(2026, 11, 16, 23, 30, 0, 0, time.UTC), To: time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)})
	if len(byDay.Rows) != 1 || byDay.Rows[0][0] != "2026-11-17" {
		t.Fatalf("by day %v", byDay.Rows)
	}
	if _, err := stats.By("guard", "colour", stats.Input{}); err == nil {
		t.Fatal("an unknown key is taken")
	}
}

// The JSON form of a report is one object with its rows.
func TestJSONForm(t *testing.T) {
	var out bytes.Buffer
	stats.Write(&out, stats.Report{Name: "guard", Columns: []string{"a"}}, time.Now(), time.Now(), true)
	var parsed map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil || parsed["report"] != "guard" || parsed["rows"] == nil {
		t.Fatalf("%s %v", out.String(), err)
	}
}

// A review attempt before midnight whose Codex run ends after it: by day, the run keeps its task
// and its minutes are split between the days; a range clips them.
func TestOvernightReviewByDay(t *testing.T) {
	midnight := time.Date(2026, 11, 17, 0, 0, 0, 0, time.Local)
	attempt := journal.Span{TraceID: "t", SpanID: "a", Name: "review.attempt", Start: midnight.Add(-20 * time.Minute),
		End: midnight.Add(20 * time.Minute), Attributes: map[string]any{telemetry.AttrReviewTask: "night",
			telemetry.AttrReviewAttempt: int64(1)}, Resource: map[string]any{telemetry.AttrProject: "demo"}}
	agent := journal.Span{TraceID: "t", SpanID: "b", ParentID: "a", Name: "invoke_agent codex",
		Start: midnight.Add(-10 * time.Minute), End: midnight.Add(10 * time.Minute),
		Attributes: map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.usage.input_tokens": int64(7)},
		Resource:   map[string]any{telemetry.AttrProject: "demo"}}
	report, err := stats.By("reviews", "day", stats.Input{Spans: []journal.Span{attempt, agent}})
	if err != nil || len(report.Rows) != 2 {
		t.Fatalf("%v %v", report.Rows, err)
	}
	tokens := int64(0)
	for _, row := range report.Rows {
		if row[2] != "night" || row[11] != 10.0 {
			t.Fatalf("a day's row %v", row)
		}
		tokens += row[7].(int64)
	}
	if tokens != 7 || report.Rows[0][5] != 0 || report.Rows[1][5] != 1 {
		t.Fatalf("the run's tokens counted %d times over the days, runs %v / %v", tokens, report.Rows[0][5], report.Rows[1][5])
	}
	clipped, _ := stats.Compute("reviews", stats.Input{Spans: []journal.Span{attempt, agent},
		From: midnight.Add(-5 * time.Minute), To: midnight.Add(5 * time.Minute)})
	if clipped.Rows[0][10] != 10.0 {
		t.Fatalf("not clipped: %v", clipped.Rows)
	}
}

// Two projects with the same task, round and attempt are two rows.
func TestReviewsPerProject(t *testing.T) {
	now := time.Now()
	attempt := func(project string) journal.Span {
		return journal.Span{TraceID: project, SpanID: "a", Name: "review.attempt", Start: now, End: now.Add(time.Minute),
			Attributes: map[string]any{telemetry.AttrReviewTask: "step1", telemetry.AttrReviewAttempt: int64(1)},
			Resource:   map[string]any{telemetry.AttrProject: project}}
	}
	report, _ := stats.Compute("reviews", stats.Input{Spans: []journal.Span{attempt("one"), attempt("two")}})
	if len(report.Rows) != 2 {
		t.Fatalf("%v", report.Rows)
	}
}

// Only the hook's decisions are guard calls: `jst guard check` or `--help` used by hand writes a
// guard span with no outcome, and one that ends in an error is not a call let through (0.5.6: two
// such spans of a subagent's `jst guard check --help` were reported as failed open).
func TestTheGuardUsedByHandIsNotACallLetThrough(t *testing.T) {
	folder := home(t)
	guardCall(t, folder, "session-a", "ls", false)
	process(t, folder, "guard", nil, func() int { return 2 }) // `jst guard check --help`-like: no outcome, exit 2
	spans := telemetrytest.Spans(t, folder)
	errored := 0
	for _, found := range spans {
		if found.Attributes[telemetry.AttrCommand] == "guard" && found.StatusCode == journal.StatusError {
			errored++
		}
	}
	if errored != 1 {
		t.Fatalf("the by-hand use is not in the journal as an error: %d", errored)
	}
	report, known := stats.Compute("guard", stats.Input{Spans: spans})
	if !known || len(report.Rows) != 1 || report.Rows[0][1] != 1 || report.Rows[0][2] != 1 || report.Rows[0][5] != 0 ||
		len(report.Notes) != 0 {
		t.Fatalf("calls, allowed, failed open: %v %v", report.Rows, report.Notes)
	}
}
