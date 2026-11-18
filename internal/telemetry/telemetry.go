// Package telemetry is the one place every jackstay command records what it did (docs/design/
// observability.md): OpenTelemetry spans — the command, its phases, its waits, the processes it
// starts — written by the Go tracing SDK (stable 1.x) through journal's exporter into the
// project's journal of OTLP-JSON lines. The context comes in through TRACEPARENT and goes out to
// every child; the Claude session a process runs for is put on each of its spans.
//
// The command span is begun by cli.Main and made lazily: nothing of the SDK runs until a span is
// needed (the guard answers first, section 8), and the span keeps the time Begin was called.
// jackstay's stores stay the source of truth (section 9): every failure here is swallowed.
package telemetry

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/telemetry/mask"
	"github.com/zima-oss/jackstay/internal/version"
)

// Environment variables.
const (
	// TraceparentVariable carries the W3C trace context to a child (the environment carrier).
	TraceparentVariable = "TRACEPARENT"
	// TracestateVariable carries the W3C trace state.
	TracestateVariable = "TRACESTATE"
	// SwitchVariable set to `off` turns telemetry off (the latency measurement, tests).
	SwitchVariable = "JACKSTAY_TELEMETRY"
	// ClaudeSessionVariable is the Claude Code session a Bash child runs for (measured, research
	// section 2; not documented, so best effort).
	ClaudeSessionVariable = "CLAUDE_CODE_SESSION_ID"
)

// ScopeName is the instrumentation scope of jackstay's own spans.
const ScopeName = "jackstay"

// FinishBound is the most Finish waits for a write (the exporter's lock wait is shorter).
const FinishBound = 200 * time.Millisecond

// process is this process's telemetry.
type process struct {
	lock sync.Mutex
	// begun by Begin
	begun     bool
	off       bool
	command   string
	args      []string
	started   time.Time
	parent    trace.SpanContext
	badParent string
	ids       *idSource
	session   string
	pending   []attribute.KeyValue
	// set by the command before the span is made
	project     string
	projectRoot string
	folder      string
	// made by materialize
	provider *sdktrace.TracerProvider
	tracers  map[string]trace.Tracer
	root     trace.Span
	rootCtx  context.Context
	open     []*Span
	finished bool
	exporter *journal.Exporter
	// set by Fail: the command span's error, whatever its exit status
	failure, failureMessage string
	// outcomeGiven: the command said its outcome (Finish does not write "ok" over it)
	outcomeGiven bool
	// testing
	now func() time.Time
}

var current = &process{}

// Begin starts this process's command span: its time, its parent (TRACEPARENT), its ids, and
// TRACEPARENT for every child — and nothing of the SDK yet. cli.Main calls it once.
func Begin(command string, args []string) {
	state := current
	state.lock.Lock()
	defer state.lock.Unlock()
	if state.begun {
		return
	}
	state.begun = true
	state.command, state.args = command, append([]string(nil), args...)
	state.started = state.clock()
	if os.Getenv(SwitchVariable) == "off" {
		state.off = true
		return
	}
	state.session = os.Getenv(ClaudeSessionVariable)
	carrier := propagation.MapCarrier{}
	if given := os.Getenv(TraceparentVariable); given != "" {
		carrier[strings.ToLower(TraceparentVariable)] = given
		if state := os.Getenv(TracestateVariable); state != "" {
			carrier[strings.ToLower(TracestateVariable)] = state
		}
		parent := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(context.Background(), carrier))
		if parent.IsValid() {
			state.parent = parent
		} else {
			state.badParent = given
		}
	}
	state.ids = newIDSource(state.parent)
	_ = os.Setenv(TraceparentVariable, traceparent(state.ids.firstTrace, state.ids.firstSpan))
}

func (state *process) clock() time.Time {
	if state.now != nil {
		return state.now()
	}
	return time.Now()
}

func traceparent(traceID trace.TraceID, spanID trace.SpanID) string {
	return "00-" + traceID.String() + "-" + spanID.String() + "-01"
}

// SetProject names the project the process's spans belong to, before the first span is made (the
// guard's project is its call's, not its folder's). root is its jackstay.json's folder ("" if none).
func SetProject(name, root string) {
	state := current
	state.lock.Lock()
	defer state.lock.Unlock()
	if state.provider == nil {
		state.project, state.projectRoot = name, root
	}
}

// SetSession names the Claude session the process runs for (the guard's, from its input).
func SetSession(sessionID string) {
	state := current
	state.lock.Lock()
	defer state.lock.Unlock()
	if sessionID != "" {
		state.session = sessionID
	}
}

// Annotate adds attributes to the command span (kept until it is made).
func Annotate(attributes ...attribute.KeyValue) {
	state := current
	state.lock.Lock()
	defer state.lock.Unlock()
	if state.off || !state.begun {
		return
	}
	for _, pair := range attributes {
		if string(pair.Key) == AttrOutcome {
			state.outcomeGiven = true
		}
	}
	if state.root == nil {
		state.pending = append(state.pending, attributes...)
		return
	}
	state.root.SetAttributes(attributes...)
}

// Context is the command span's context: the parent of whatever starts without one.
func Context() context.Context {
	state := current
	state.lock.Lock()
	defer state.lock.Unlock()
	if !state.materialize() {
		return context.Background()
	}
	return state.rootCtx
}

// materialize makes the provider and the command span; false when telemetry is off or not begun.
// The lock is held.
func (state *process) materialize() bool {
	if state.off || !state.begun || state.finished {
		return false
	}
	if state.provider != nil {
		return true
	}
	settings := resolveProject(state.project, state.projectRoot)
	state.project, state.folder = settings.name, settings.folder
	modeOfCommands := settings.commands
	state.exporter = &journal.Exporter{
		Folder:     settings.folder,
		Reduce:     reducer(modeOfCommands),
		ReduceText: mask.Text,
		Upkeep: func(folder string) {
			journal.Upkeep(folder, settings.retention, time.Now())
		},
	}
	attributes := []attribute.KeyValue{
		attribute.String(AttrServiceName, "jackstay"),
		attribute.String(AttrServiceVersion, version.Version),
		attribute.String(AttrBuildCommit, version.BuildCommit()),
		attribute.Int(AttrProcessPID, os.Getpid()),
		attribute.Int(AttrProcessParentPID, os.Getppid()),
	}
	if state.project != "" {
		attributes = append(attributes, attribute.String(AttrProject, state.project))
	}
	state.provider = sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(state.exporter),
		sdktrace.WithResource(resource.NewSchemaless(attributes...)),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithIDGenerator(state.ids),
	)
	state.tracers = map[string]trace.Tracer{}
	parentCtx := context.Background()
	kind := trace.SpanKindInternal
	if state.parent.IsValid() {
		parentCtx = trace.ContextWithRemoteSpanContext(parentCtx, state.parent)
		kind = trace.SpanKindServer
	}
	rootAttributes := []attribute.KeyValue{attribute.String(AttrCommand, state.command)}
	// The whole argv (`jst lint pr --x`), so the mask reads it as one command's words.
	argv := append(append([]string{"jst"}, strings.Fields(state.command)...), state.args...)
	rootAttributes = append(rootAttributes, attribute.StringSlice(AttrArgs, argv))
	if folder, err := os.Getwd(); err == nil {
		rootAttributes = append(rootAttributes, attribute.String(AttrCwd, folder))
	}
	if state.badParent != "" {
		rootAttributes = append(rootAttributes, attribute.String(AttrTraceparentBad, state.badParent))
	}
	rootAttributes = append(rootAttributes, state.sessionAttributes()...)
	rootAttributes = append(rootAttributes, state.pending...)
	state.pending = nil
	state.rootCtx, state.root = state.tracer("").Start(parentCtx, "jst "+state.command, trace.WithSpanKind(kind),
		trace.WithTimestamp(state.started), trace.WithAttributes(rootAttributes...))
	return true
}

func (state *process) tracer(schemaURL string) trace.Tracer {
	if found, ok := state.tracers[schemaURL]; ok {
		return found
	}
	options := []trace.TracerOption{trace.WithInstrumentationVersion(version.Version)}
	if schemaURL != "" {
		options = append(options, trace.WithSchemaURL(schemaURL))
	}
	made := state.provider.Tracer(ScopeName, options...)
	state.tracers[schemaURL] = made
	return made
}

// sessionAttributes are what every span of a process that knows its Claude session carries.
func (state *process) sessionAttributes() []attribute.KeyValue {
	if state.session == "" {
		return nil
	}
	found := []attribute.KeyValue{attribute.String(AttrClaudeSession, state.session)}
	for _, annotate := range sessionAnnotators {
		found = append(found, annotate(state.session)...)
	}
	return found
}

var sessionAnnotators []func(sessionID string) []attribute.KeyValue

// OnSession registers attributes every span of a Claude session carries (the GenAI adapter's
// conversation id); called from an init.
func OnSession(annotate func(sessionID string) []attribute.KeyValue) {
	sessionAnnotators = append(sessionAnnotators, annotate)
}

// Options of a span.
type Options struct {
	Kind trace.SpanKind
	// SchemaURL makes the span with a tracer of that schema (the GenAI adapter's).
	SchemaURL  string
	Attributes []attribute.KeyValue
	// StartAt is when the span began, if before now (a wait known to be one only later).
	StartAt time.Time
}

// Span is a span of jackstay's: ended once, with an outcome.
type Span struct {
	inner      trace.Span
	context    context.Context
	gitCalls   int
	gitSeconds float64
	ended      bool
}

var noSpan = noop.NewTracerProvider().Tracer("")

// Start makes a span under ctx's span, or, when ctx has none (nil is no context), under the
// innermost open span or the command span. With telemetry off it is a span that records nothing.
func Start(ctx context.Context, name string, options Options) (context.Context, *Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	state := current
	state.lock.Lock()
	defer state.lock.Unlock()
	if !state.materialize() {
		inner := noSpan
		started, span := inner.Start(ctx, name)
		return started, &Span{inner: span, context: started}
	}
	if !trace.SpanContextFromContext(ctx).IsValid() {
		// No parent given: the innermost open span, else the command span.
		ctx = state.rootCtx
		if len(state.open) > 0 {
			ctx = state.open[len(state.open)-1].context
		}
	}
	kind := options.Kind
	if kind == trace.SpanKindUnspecified {
		kind = trace.SpanKindInternal
	}
	attributes := append(state.sessionAttributes(), options.Attributes...)
	startOptions := []trace.SpanStartOption{trace.WithSpanKind(kind), trace.WithAttributes(attributes...)}
	if !options.StartAt.IsZero() {
		startOptions = append(startOptions, trace.WithTimestamp(options.StartAt))
	}
	started, inner := state.tracer(options.SchemaURL).Start(ctx, name, startOptions...)
	span := &Span{inner: inner, context: started}
	state.open = append(state.open, span)
	return started, span
}

// Context is the span's context, for spans started under it.
func (span *Span) Context() context.Context { return span.context }

// Traceparent is the W3C header of the span, for a child's environment.
func (span *Span) Traceparent() string {
	context := span.inner.SpanContext()
	if !context.IsValid() {
		return ""
	}
	return traceparent(context.TraceID(), context.SpanID())
}

// Set adds attributes.
func (span *Span) Set(attributes ...attribute.KeyValue) { span.inner.SetAttributes(attributes...) }

// Rename changes the span's name (the GenAI adapter names an agent run).
func (span *Span) Rename(name string) { span.inner.SetName(name) }

// Event adds an event now.
func (span *Span) Event(name string, attributes ...attribute.KeyValue) {
	span.inner.AddEvent(name, trace.WithAttributes(attributes...))
}

// Fail marks the span an error of class errorType; message is free text (masked when written).
func (span *Span) Fail(errorType, message string) {
	span.inner.SetAttributes(attribute.String(AttrErrorType, errorType))
	span.inner.SetStatus(codes.Error, message)
}

// End ends the span with an outcome word ("" leaves it unset). Only the first End counts.
func (span *Span) End(outcome string) {
	state := current
	state.lock.Lock()
	defer state.lock.Unlock()
	span.endLocked(outcome, time.Time{})
}

func (span *Span) endLocked(outcome string, endedAt time.Time) {
	if span.ended {
		return
	}
	span.ended = true
	if outcome != "" {
		span.inner.SetAttributes(attribute.String(AttrOutcome, outcome))
	}
	if span.gitCalls > 0 {
		span.inner.SetAttributes(attribute.Int(AttrGitCalls, span.gitCalls), attribute.Float64(AttrGitSeconds, span.gitSeconds))
	}
	for index, open := range current.open {
		if open == span {
			current.open = append(current.open[:index], current.open[index+1:]...)
			break
		}
	}
	if endedAt.IsZero() {
		span.inner.End()
	} else {
		span.inner.End(trace.WithTimestamp(endedAt))
	}
}

// NoteGit counts a git call against the innermost open span (they are not spans, section 2).
func NoteGit(took time.Duration) {
	state := current
	state.lock.Lock()
	defer state.lock.Unlock()
	if len(state.open) == 0 {
		return
	}
	innermost := state.open[len(state.open)-1]
	innermost.gitCalls++
	innermost.gitSeconds += took.Seconds()
}

// Finish ends every open span, innermost first, and the command span with the exit status; it is
// idempotent. cli.Main calls it when the command returns; supervise.EndBy before it re-raises a
// signal (FinishBySignal).
func Finish(exitCode int) { current.finish(exitCode, "", "") }

// FinishBySignal ends the process's spans as stopped by a signal, before jackstay dies by it.
func FinishBySignal(sig syscall.Signal) {
	current.finish(128+int(sig), "signal_"+signalName(sig), "stopped")
}

// Panicked records a panic and its stack on the command span and finishes it (cli.Main panics
// again after it: the crash is unchanged).
func Panicked(fault any, stack []byte) {
	state := current
	state.lock.Lock()
	if state.materialize() {
		state.root.AddEvent(EventException, trace.WithAttributes(
			attribute.String(AttrExceptionType, fmt.Sprintf("%T", fault)),
			attribute.String(AttrExceptionMessage, fmt.Sprint(fault)),
			attribute.String(AttrExceptionStack, string(stack))))
	}
	state.lock.Unlock()
	state.finish(2, "panic", "crashed")
}

func (state *process) finish(exitCode int, errorType, outcome string) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }() // telemetry is never worth failing an exit for
		state.lock.Lock()
		defer state.lock.Unlock()
		if !state.materialize() {
			state.finished = true
			return
		}
		now := state.clock()
		for len(state.open) > 0 {
			innermost := state.open[len(state.open)-1]
			if errorType != "" {
				innermost.inner.SetAttributes(attribute.String(AttrErrorType, errorType))
				innermost.inner.SetStatus(codes.Error, "")
			}
			innermost.endLocked("stopped", now)
		}
		state.root.SetAttributes(attribute.Int(AttrExitCode, exitCode))
		switch {
		case outcome != "":
			state.root.SetAttributes(attribute.String(AttrOutcome, outcome))
		case exitCode == 0 && !state.outcomeGiven:
			state.root.SetAttributes(attribute.String(AttrOutcome, "ok"))
		}
		if errorType == "" && state.failure != "" {
			errorType = state.failure
			state.root.SetStatus(codes.Error, state.failureMessage)
		}
		if errorType == "" && exitCode != 0 {
			errorType = fmt.Sprintf("exit_%d", exitCode)
		}
		if errorType != "" {
			state.root.SetAttributes(attribute.String(AttrErrorType, errorType))
			if state.failure == "" {
				state.root.SetStatus(codes.Error, "")
			}
		}
		state.root.End(trace.WithTimestamp(now))
		state.finished = true
	}()
	select {
	case <-done:
	case <-time.After(FinishBound):
	}
}

// Note adds an event to the innermost open span, else the command span.
func Note(name string, attributes ...attribute.KeyValue) {
	state := current
	state.lock.Lock()
	defer state.lock.Unlock()
	if !state.materialize() {
		return
	}
	target := state.root
	if len(state.open) > 0 {
		target = state.open[len(state.open)-1].inner
	}
	target.AddEvent(name, trace.WithAttributes(attributes...))
}

// Fail marks the command span an error of class errorType whatever its exit status (the guard
// exits 0 when it fails open); message is free text.
func Fail(errorType, message string) {
	state := current
	state.lock.Lock()
	defer state.lock.Unlock()
	state.failure, state.failureMessage = errorType, message
}

// Outcome sets the command span's outcome word (a command's own: passed, refused, …).
func Outcome(word string) { Annotate(attribute.String(AttrOutcome, word)) }

// ChildEnv is env (nil: this process's) with TRACEPARENT set to span's context, for a child.
func ChildEnv(env []string, span *Span) []string {
	if env == nil {
		env = os.Environ()
	}
	header := span.Traceparent()
	if header == "" {
		return env
	}
	result := make([]string, 0, len(env)+1)
	for _, pair := range env {
		if !strings.HasPrefix(pair, TraceparentVariable+"=") {
			result = append(result, pair)
		}
	}
	return append(result, TraceparentVariable+"="+header)
}

// Folder is the journal folder of this process ("" until a span is made).
func Folder() string {
	state := current
	state.lock.Lock()
	defer state.lock.Unlock()
	return state.folder
}

// idSource gives the command span the ids Begin chose (so a child's TRACEPARENT names a span
// before the SDK runs), then random ones.
type idSource struct {
	lock       sync.Mutex
	firstTrace trace.TraceID
	firstSpan  trace.SpanID
	used       bool
	random     *rand.Rand
}

func newIDSource(parent trace.SpanContext) *idSource {
	var seed [32]byte
	_, _ = crand.Read(seed[:])
	source := &idSource{random: rand.New(rand.NewChaCha8(seed))} // #nosec G404 -- ids, not secrets
	if parent.IsValid() {
		source.firstTrace = parent.TraceID()
	} else {
		binary.BigEndian.PutUint64(source.firstTrace[:8], source.random.Uint64())
		binary.BigEndian.PutUint64(source.firstTrace[8:], source.random.Uint64())
	}
	binary.BigEndian.PutUint64(source.firstSpan[:], source.random.Uint64()|1)
	return source
}

// NewIDs gives a new trace's ids.
func (source *idSource) NewIDs(context.Context) (trace.TraceID, trace.SpanID) {
	source.lock.Lock()
	defer source.lock.Unlock()
	if !source.used {
		source.used = true
		return source.firstTrace, source.firstSpan
	}
	var traceID trace.TraceID
	binary.BigEndian.PutUint64(traceID[:8], source.random.Uint64())
	binary.BigEndian.PutUint64(traceID[8:], source.random.Uint64())
	return traceID, source.spanID()
}

// NewSpanID gives a span id in a trace.
func (source *idSource) NewSpanID(_ context.Context, traceID trace.TraceID) trace.SpanID {
	source.lock.Lock()
	defer source.lock.Unlock()
	if !source.used && traceID == source.firstTrace {
		source.used = true
		return source.firstSpan
	}
	return source.spanID()
}

func (source *idSource) spanID() trace.SpanID {
	var spanID trace.SpanID
	binary.BigEndian.PutUint64(spanID[:], source.random.Uint64()|1)
	return spanID
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	}
	return fmt.Sprintf("SIG%d", int(sig))
}

// Reset forgets this process's telemetry: a test runs many commands in one process.
func Reset() { current = &process{} }

// SetClock gives the clock spans of this process start and end by (tests).
func SetClock(now func() time.Time) { current.now = now }
