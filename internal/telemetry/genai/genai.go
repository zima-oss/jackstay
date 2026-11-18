// Package genai is the only place jackstay knows a name of OpenTelemetry's GenAI semantic conventions
// (docs/design/observability.md, section 6). They are *development*, so they are pinned here and
// confined: the instrumentation calls the writing half, the queries call the reading half, and
// neither names a `gen_ai.*` attribute. Every value is also kept under jackstay's own `jackstay.*` name, so a
// journal written under an older revision still answers.
//
// Updating the revision (or otel, or proto-otlp) is one commit: move Revision and SchemaURL,
// change the names below where the conventions changed them, run the golden tests
// (golden_test.go: a model call, a tool call, a retry, an error, a nested agent — the content and
// the query answers). The golden files change only where a name changed; the answers must not.
package genai

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
)

// The pinned revision of open-telemetry/semantic-conventions-genai (it has no tags).
const (
	Revision  = "e6658f14c9d9ea0760825f029cb82e7673f48fab"
	SchemaURL = "https://opentelemetry.io/schemas/gen-ai-dev/1.42.0-dev"
)

// The names of the pinned revision that jackstay writes.
const (
	operationName       = "gen_ai.operation.name"
	providerName        = "gen_ai.provider.name"
	agentName           = "gen_ai.agent.name"
	agentID             = "gen_ai.agent.id"
	conversationID      = "gen_ai.conversation.id"
	requestModel        = "gen_ai.request.model"
	reasoningLevel      = "gen_ai.request.reasoning.level"
	toolName            = "gen_ai.tool.name"
	toolCallID          = "gen_ai.tool.call.id"
	toolType            = "gen_ai.tool.type"
	usageInput          = "gen_ai.usage.input_tokens"
	usageOutput         = "gen_ai.usage.output_tokens"
	usageCacheRead      = "gen_ai.usage.cache_read.input_tokens"
	usageCacheWrite     = "gen_ai.usage.cache_write.input_tokens"
	usageReasoningOuput = "gen_ai.usage.reasoning.output_tokens"

	invokeAgent = "invoke_agent"
)

func init() {
	telemetry.Declare(map[string]telemetry.Kind{
		operationName: telemetry.Plain, providerName: telemetry.Plain, agentName: telemetry.Plain,
		agentID: telemetry.Identity, conversationID: telemetry.Identity, requestModel: telemetry.Plain,
		reasoningLevel: telemetry.Plain, toolName: telemetry.Plain, toolCallID: telemetry.Identity,
		toolType: telemetry.Plain, usageInput: telemetry.Plain, usageOutput: telemetry.Plain,
		usageCacheRead: telemetry.Plain, usageCacheWrite: telemetry.Plain, usageReasoningOuput: telemetry.Plain,
	})
	telemetry.OnSession(func(sessionID string) []attribute.KeyValue {
		return []attribute.KeyValue{attribute.String(conversationID, sessionID)}
	})
}

// --- writing ---------------------------------------------------------------------------------

// ToolCall is a Claude tool call the guard judged.
type ToolCall struct {
	Name      string // "Bash"
	ID        string // tool_use_id
	AgentType string // a subagent's type ("" for the main agent)
	AgentID   string
}

// ToolCallAttributes annotate the guard's span: the call it judged. It sets no operation name —
// the guard does not execute the tool (section 6).
func ToolCallAttributes(call ToolCall) []attribute.KeyValue {
	found := []attribute.KeyValue{attribute.String(toolName, call.Name), attribute.String(toolType, "function")}
	if call.ID != "" {
		found = append(found, attribute.String(toolCallID, call.ID), attribute.String(telemetry.AttrClaudeToolUse, call.ID))
	}
	if call.AgentType != "" {
		found = append(found, attribute.String(agentName, call.AgentType),
			attribute.String(telemetry.AttrClaudeAgentType, call.AgentType))
	}
	if call.AgentID != "" {
		found = append(found, attribute.String(agentID, call.AgentID), attribute.String(telemetry.AttrClaudeAgentID, call.AgentID))
	}
	return found
}

// Usage is a model's token counts.
type Usage struct {
	Input, CachedInput, CacheWrite, Output, ReasoningOutput int64
}

// Add sums two usages.
func (usage Usage) Add(other Usage) Usage {
	return Usage{Input: usage.Input + other.Input, CachedInput: usage.CachedInput + other.CachedInput,
		CacheWrite: usage.CacheWrite + other.CacheWrite, Output: usage.Output + other.Output,
		ReasoningOutput: usage.ReasoningOutput + other.ReasoningOutput}
}

func (usage Usage) attributes() []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Int64(usageInput, usage.Input), attribute.Int64(usageOutput, usage.Output),
		attribute.Int64(usageCacheRead, usage.CachedInput), attribute.Int64(usageCacheWrite, usage.CacheWrite),
		attribute.Int64(usageReasoningOuput, usage.ReasoningOutput),
	}
}

// AgentRun is a run of an agent jackstay started (Codex in `jst review`).
type AgentRun struct {
	Provider string // "openai"
	Agent    string // "codex"
	Model    string
	Effort   string
	ThreadID string
	Turns    []Usage
	// TurnFailed marks turns that failed (parallel to Turns; may be shorter).
	TurnFailed []bool
}

// StartAgentRun starts the span of an agent run under ctx's span: named, CLIENT, from the tracer
// of the pinned schema (the schema URL belongs to the tracer, section 6).
func StartAgentRun(ctx context.Context, agent string) (context.Context, *telemetry.Span) {
	return telemetry.Start(ctx, invokeAgent+" "+agent, telemetry.Options{Kind: trace.SpanKindClient,
		SchemaURL: SchemaURL, Attributes: []attribute.KeyValue{attribute.String(operationName, invokeAgent),
			attribute.String(agentName, agent)}})
}

// FinishAgentRun annotates the span with what the run was (its usage summed, each turn an event).
func FinishAgentRun(span *telemetry.Span, run AgentRun) {
	attributes := []attribute.KeyValue{attribute.String(providerName, run.Provider), attribute.String(agentName, run.Agent)}
	if run.Model != "" {
		attributes = append(attributes, attribute.String(requestModel, run.Model))
	}
	if run.Effort != "" {
		attributes = append(attributes, attribute.String(reasoningLevel, run.Effort))
	}
	if run.ThreadID != "" {
		attributes = append(attributes, attribute.String(conversationID, run.ThreadID),
			attribute.String(telemetry.AttrCodexThread, run.ThreadID))
	}
	total := Usage{}
	for index, turn := range run.Turns {
		total = total.Add(turn)
		eventAttributes := append(turn.attributes(), attribute.Int(telemetry.AttrCodexTurnIndex, index+1))
		if index < len(run.TurnFailed) && run.TurnFailed[index] {
			eventAttributes = append(eventAttributes, attribute.Bool(telemetry.AttrCodexTurnFailed, true))
		}
		span.Event(telemetry.EventCodexTurn, eventAttributes...)
	}
	span.Set(append(attributes, total.attributes()...)...)
}

// --- reading ---------------------------------------------------------------------------------

// ConversationOf is the conversation (Claude session, Codex thread) a span belongs to.
func ConversationOf(span journal.Span) string {
	if found := span.Text(conversationID); found != "" {
		return found
	}
	if found := span.Text(telemetry.AttrClaudeSession); found != "" {
		return found
	}
	return span.Text(telemetry.AttrCodexThread)
}

// SessionOf is the Claude session of a span (never a Codex thread).
func SessionOf(span journal.Span) string {
	if found := span.Text(telemetry.AttrClaudeSession); found != "" {
		return found
	}
	if !IsAgentRun(span) {
		return span.Text(conversationID)
	}
	return ""
}

// ToolCallOf is the tool call a span judged, if any.
func ToolCallOf(span journal.Span) (ToolCall, bool) {
	call := ToolCall{Name: span.Text(toolName), ID: span.Text(toolCallID), AgentType: span.Text(agentName),
		AgentID: span.Text(agentID)}
	if call.ID == "" {
		call.ID = span.Text(telemetry.AttrClaudeToolUse)
	}
	if call.AgentType == "" {
		call.AgentType = span.Text(telemetry.AttrClaudeAgentType)
	}
	if call.AgentID == "" {
		call.AgentID = span.Text(telemetry.AttrClaudeAgentID)
	}
	return call, call.ID != "" || call.Name != ""
}

// IsAgentRun says a span is an agent run.
func IsAgentRun(span journal.Span) bool { return span.Text(operationName) == invokeAgent }

// AgentRunOf reads an agent run's span; its Turns hold the total only.
func AgentRunOf(span journal.Span) (AgentRun, bool) {
	if !IsAgentRun(span) {
		return AgentRun{}, false
	}
	thread := span.Text(telemetry.AttrCodexThread)
	if thread == "" {
		thread = span.Text(conversationID)
	}
	return AgentRun{Provider: span.Text(providerName), Agent: span.Text(agentName), Model: span.Text(requestModel),
		Effort: span.Text(reasoningLevel), ThreadID: thread,
		Turns: []Usage{{Input: span.Number(usageInput), CachedInput: span.Number(usageCacheRead),
			CacheWrite: span.Number(usageCacheWrite), Output: span.Number(usageOutput),
			ReasoningOutput: span.Number(usageReasoningOuput)}}}, true
}
