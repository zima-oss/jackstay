package telemetry_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"go.opentelemetry.io/otel/attribute"

	"github.com/zima-oss/jackstay/internal/telemetry"
	_ "github.com/zima-oss/jackstay/internal/telemetry/genai"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/telemetry/journal/otlpcheck"
	"github.com/zima-oss/jackstay/internal/telemetry/telemetrytest"
)

func TestCommandPhaseAndChild(t *testing.T) {
	home := telemetrytest.Begin(t, "test", []string{"gate"}, map[string]string{
		telemetry.ClaudeSessionVariable: "session-1"})
	header := os.Getenv(telemetry.TraceparentVariable)
	if !strings.HasPrefix(header, "00-") || len(header) != 55 {
		t.Fatalf("TRACEPARENT for children = %q", header)
	}
	ctx, phase := telemetry.Start(context.Background(), "test.step", telemetry.Options{})
	_, child := telemetry.Start(ctx, "exec just", telemetry.Options{Attributes: []attribute.KeyValue{
		attribute.StringSlice(telemetry.AttrChildArgv, []string{"just", "gate", "--password", "hunter2"})}})
	env := telemetry.ChildEnv([]string{"A=1", "TRACEPARENT=old"}, child)
	if env[0] != "A=1" || env[1] != "TRACEPARENT="+child.Traceparent() {
		t.Fatalf("child env %q", env)
	}
	child.End("exited-0")
	phase.End("passed")
	telemetry.Finish(0)

	spans := telemetrytest.Spans(t, home)
	if len(spans) != 3 {
		t.Fatalf("%d spans", len(spans))
	}
	root, step, exec := spans[0], spans[1], spans[2]
	if root.Name != "jst test" || root.ParentID != "" || root.Number(telemetry.AttrExitCode) != 0 || root.Text(telemetry.AttrOutcome) != "ok" {
		t.Fatalf("root %+v", root)
	}
	if !strings.Contains(header, root.SpanID) || !strings.Contains(header, root.TraceID) {
		t.Fatalf("TRACEPARENT %s does not name the command span %s/%s", header, root.TraceID, root.SpanID)
	}
	if step.ParentID != root.SpanID || exec.ParentID != step.SpanID || exec.TraceID != root.TraceID {
		t.Fatalf("tree: %s<-%s<-%s", root.SpanID, step.ParentID, exec.ParentID)
	}
	if got := exec.Texts(telemetry.AttrChildArgv); strings.Join(got, " ") != "just gate --password ***" {
		t.Fatalf("argv not masked: %q", got)
	}
	for _, span := range spans {
		if span.Text(telemetry.AttrClaudeSession) != "session-1" || span.Text("gen_ai.conversation.id") != "session-1" {
			t.Fatalf("%s has no session: %v", span.Name, span.Attributes)
		}
	}
	if got := root.Texts(telemetry.AttrArgs); strings.Join(got, " ") != "jst test gate" {
		t.Fatalf("args %q", got)
	}
}

func TestInheritedParentAndBadParent(t *testing.T) {
	parent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	home := telemetrytest.Begin(t, "check", nil, map[string]string{telemetry.TraceparentVariable: parent})
	telemetry.Finish(1)
	spans := telemetrytest.Spans(t, home)
	if len(spans) != 1 || spans[0].TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || spans[0].ParentID != "00f067aa0ba902b7" {
		t.Fatalf("not a child of the inherited parent (unsampled flag ignored): %+v", spans)
	}
	if spans[0].Kind != 2 || spans[0].StatusCode != journal.StatusError || spans[0].Text(telemetry.AttrErrorType) != "exit_1" {
		t.Fatalf("kind/status %+v", spans[0])
	}

	home = telemetrytest.Begin(t, "check", nil, map[string]string{telemetry.TraceparentVariable: "garbage"})
	telemetry.Finish(0)
	spans = telemetrytest.Spans(t, home)
	if spans[0].ParentID != "" || spans[0].Text(telemetry.AttrTraceparentBad) != "garbage" {
		t.Fatalf("bad parent %+v", spans[0])
	}
}

func TestFinishBySignalEndsOpenSpans(t *testing.T) {
	home := telemetrytest.Begin(t, "test", []string{"--", "sleep", "30"}, nil)
	ctx, phase := telemetry.Start(context.Background(), "test.step", telemetry.Options{})
	_, _ = telemetry.Start(ctx, "exec sleep", telemetry.Options{})
	telemetry.FinishBySignal(syscall.SIGTERM)
	telemetry.Finish(0) // idempotent
	phase.End("passed") // after the finish: ignored
	spans := telemetrytest.Spans(t, home)
	if len(spans) != 3 {
		t.Fatalf("%d spans", len(spans))
	}
	for _, span := range spans {
		if span.StatusCode != journal.StatusError || span.Text(telemetry.AttrErrorType) != "signal_SIGTERM" {
			t.Fatalf("%s: %v", span.Name, span.Attributes)
		}
		if span.Text(telemetry.AttrOutcome) != "stopped" {
			t.Fatalf("%s outcome %q", span.Name, span.Text(telemetry.AttrOutcome))
		}
	}
	if spans[0].Number(telemetry.AttrExitCode) != 143 {
		t.Fatalf("exit %d", spans[0].Number(telemetry.AttrExitCode))
	}
}

func TestPanicRecordsStack(t *testing.T) {
	home := telemetrytest.Begin(t, "boom", nil, nil)
	telemetry.Panicked("broken", []byte("goroutine 1 [running]:\nmain.main()\n\t/x/main.go:3"))
	spans := telemetrytest.Spans(t, home)
	if len(spans[0].Events) != 1 || spans[0].Events[0].Name != "exception" ||
		!strings.Contains(spans[0].Events[0].Text(telemetry.AttrExceptionStack), "main.main") {
		t.Fatalf("%+v", spans[0])
	}
	if spans[0].Text(telemetry.AttrErrorType) != "panic" || spans[0].Text(telemetry.AttrOutcome) != "crashed" {
		t.Fatalf("%v", spans[0].Attributes)
	}
}

func TestOffWritesNothing(t *testing.T) {
	home := telemetrytest.Begin(t, "guard", nil, map[string]string{telemetry.SwitchVariable: "off"})
	_, span := telemetry.Start(context.Background(), "x", telemetry.Options{})
	span.End("ok")
	telemetry.Finish(0)
	if found, _ := filepath.Glob(filepath.Join(home, "*", "telemetry", "*")); len(found) != 0 {
		t.Fatalf("written: %v", found)
	}
}

func TestUndeclaredAttributeIsAProgrammingError(t *testing.T) {
	telemetrytest.Begin(t, "x", nil, nil)
	_, span := telemetry.Start(context.Background(), "x", telemetry.Options{Attributes: []attribute.KeyValue{attribute.String("jackstay.nobody", "v")}})
	defer func() {
		if recover() == nil {
			t.Fatal("no panic for an undeclared attribute")
		}
		telemetry.Reset()
	}()
	span.End("ok")
}

// A14: every line is OTLP that protojson reads back, once the ids are base64 again.
func TestLinesAreOTLP(t *testing.T) {
	home := telemetrytest.Begin(t, "test", nil, map[string]string{telemetry.ClaudeSessionVariable: "s"})
	_, phase := telemetry.Start(context.Background(), "phase", telemetry.Options{})
	phase.Event(telemetry.EventTestVerdict, attribute.String(telemetry.AttrTestOutcome, "passed"))
	phase.End("passed")
	telemetry.Finish(0)
	files, _ := filepath.Glob(filepath.Join(home, "*", "telemetry", "*.otlp.jsonl"))
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	data, _ := os.ReadFile(files[0])
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines", len(lines))
	}
	for _, line := range lines {
		message, err := otlpcheck.Decode([]byte(line))
		if err != nil {
			t.Fatalf("not OTLP: %v\n%s", err, line)
		}
		again, err := otlpcheck.Encode(message)
		if err != nil {
			t.Fatal(err)
		}
		written, _ := otlpcheck.Tree([]byte(line))
		if !reflect.DeepEqual(again, written) {
			t.Fatalf("the proto reads it differently:\n%s\n%v", line, again)
		}
		if !strings.Contains(line, `"traceId":"`) || strings.Contains(line, "==") {
			t.Fatalf("ids not hex: %s", line)
		}
	}
}

// A journal that cannot be written changes nothing of the command (section 9).
func TestUnwritableHomeChangesNothing(t *testing.T) {
	home := telemetrytest.Begin(t, "test", nil, nil)
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(home, 0o700) }()
	_, span := telemetry.Start(context.Background(), "test.step", telemetry.Options{})
	span.End("passed")
	telemetry.Finish(0)
	if found, _ := filepath.Glob(filepath.Join(home, "*")); len(found) != 0 {
		t.Fatalf("written: %v", found)
	}
}
