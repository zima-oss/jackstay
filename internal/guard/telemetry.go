package guard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/genai"
)

// The guard's outcomes in its span (docs/design/observability.md, section 2).
const (
	outcomeAllowed     = "allowed"
	outcomeRefused     = "refused"
	outcomeWarned      = "warned"
	outcomeFailedOpen  = "failed_open"
	outcomeIgnoredTool = "ignored_tool"
)

// judged is what the guard's span says of a call.
type judged struct {
	outcome    string
	rules      []string
	warnings   []string
	command    string
	background bool // run_in_background, not through jst bg
	// configError is why the project's jackstay.json could not be used ("" when it could).
	configError string
	why         string
}

// record puts the decision on the guard's command span, after the answer (section 8): the
// project and session the call is of, the tool call it judged, the decision as an event.
func record(given call, projectName, projectRoot string, decided judged) {
	telemetry.SetProject(projectName, projectRoot)
	telemetry.SetSession(given.text("session_id"))
	attributes := []attribute.KeyValue{attribute.String(telemetry.AttrGuardOutcome, decided.outcome),
		attribute.String(telemetry.AttrOutcome, decided.outcome)}
	if tool := given.text("tool_name"); tool != "" {
		attributes = append(attributes, attribute.String(telemetry.AttrGuardTool, tool))
		attributes = append(attributes, genai.ToolCallAttributes(genai.ToolCall{Name: tool, ID: given.text("tool_use_id"),
			AgentType: given.text("agent_type"), AgentID: given.text("agent_id")})...)
	}
	for key, name := range map[string]string{"prompt_id": telemetry.AttrClaudePrompt, "transcript_path": telemetry.AttrClaudeTranscript,
		"cwd": telemetry.AttrCwd} {
		if value := given.text(key); value != "" {
			attributes = append(attributes, attribute.String(name, value))
		}
	}
	if decided.command != "" {
		digest := sha256.Sum256([]byte(decided.command))
		attributes = append(attributes, attribute.String(telemetry.AttrGuardCommand, decided.command),
			attribute.String(telemetry.AttrGuardCommandID, hex.EncodeToString(digest[:])))
	}
	telemetry.Annotate(attributes...)
	event := []attribute.KeyValue{attribute.String(telemetry.AttrGuardOutcome, decided.outcome)}
	if len(decided.rules) > 0 {
		event = append(event, attribute.StringSlice(telemetry.AttrGuardRules, decided.rules))
		telemetry.Annotate(attribute.StringSlice(telemetry.AttrGuardRules, decided.rules))
	}
	if len(decided.warnings) > 0 {
		event = append(event, attribute.StringSlice(telemetry.AttrGuardWarnings, decided.warnings))
		telemetry.Annotate(attribute.StringSlice(telemetry.AttrGuardWarnings, decided.warnings))
	}
	if decided.why != "" {
		event = append(event, attribute.String(telemetry.AttrReason, decided.why))
	}
	if decided.configError != "" {
		telemetry.Annotate(attribute.String(telemetry.AttrGuardConfigError, decided.configError))
	}
	telemetry.Note(telemetry.EventGuardDecision, event...)
	if decided.background {
		telemetry.Note(telemetry.EventBgRequestOutside, attribute.String(telemetry.AttrGuardOutcome, decided.outcome))
	}
	if decided.outcome == outcomeFailedOpen {
		// Who called the guard with what it could not read (the 2026-11-16 case, research section 1).
		telemetry.NoteCaller()
		telemetry.Fail(outcomeFailedOpen, decided.why)
	}
}

// recordFault puts a recovered panic on the guard's span, with its stack.
func recordFault(fault any, stack []byte) {
	telemetry.Annotate(attribute.String(telemetry.AttrGuardOutcome, outcomeFailedOpen),
		attribute.String(telemetry.AttrOutcome, outcomeFailedOpen))
	telemetry.Note(telemetry.EventException, attribute.String(telemetry.AttrExceptionType, fmt.Sprintf("%T", fault)),
		attribute.String(telemetry.AttrExceptionMessage, fmt.Sprint(fault)),
		attribute.String(telemetry.AttrExceptionStack, string(stack)))
	telemetry.NoteCaller()
	telemetry.Fail("panic", fmt.Sprint(fault))
}

// recordAnswerTime is how long the call took to answer, from the hook's start.
func recordAnswerTime(took time.Duration) {
	telemetry.Annotate(attribute.Float64(telemetry.AttrGuardMillis, float64(took.Microseconds())/1000))
}
