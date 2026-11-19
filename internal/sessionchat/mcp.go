package sessionchat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/reviewpool"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

const maxRequestBytes = 128 << 10
const maxResponseBytes = 1 << 20

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server binds one MCP stdio connection to a registered session identity.
type Server struct {
	store   Store
	session Session
}

func init() {
	cli.Register(cli.Command{Name: "chat serve", Summary: "serve repository-local agent session chat over MCP", Usage: "usage: jst chat serve --name NAME\n\nServe a local stdio MCP session. Set JACKSTAY_CHAT_SESSION_TOKEN to a random 32+ character secret in the host launcher to resume an identity. The project must enable chat in jackstay.json. Messages are delivered when recipients poll their inbox.\n\nexit: 0 end of input; 2 invalid configuration, arguments or transport.\n\nexample: jst chat serve --name reviewer\n", PrivateArgs: true, Run: Run})
}

// Run starts the chat MCP server from an explicitly enabled project.
func Run(args []string, input io.Reader, output, diagnostics io.Writer) int {
	if len(args) != 2 || args[0] != "--name" {
		fmt.Fprintln(diagnostics, "usage: jst chat serve --name NAME")
		return 2
	}
	contextWithDeadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, _, err := loadLocalChat(contextWithDeadline)
	if err != nil {
		fmt.Fprintf(diagnostics, "chat: %v\n", err)
		return 2
	}
	session, err := store.Register(args[1], os.Getenv("JACKSTAY_CHAT_SESSION_TOKEN"))
	if err != nil {
		fmt.Fprintln(diagnostics, "chat: "+err.Error())
		return 2
	}
	return (&Server{store: store, session: session}).Serve(input, output, diagnostics)
}

// Serve handles newline-delimited JSON-RPC messages on one stdio connection.
func (server *Server) Serve(input io.Reader, output, diagnostics io.Writer) int {
	reader := bufio.NewReaderSize(input, maxRequestBytes+1)
	initialized := false
	for {
		line, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) || len(line) > maxRequestBytes {
			fmt.Fprintln(diagnostics, "chat MCP request exceeds 128 KiB")
			return 2
		}
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			return 0
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return 2
		}
		var request rpcRequest
		if strictjson.UnmarshalOpen(bytes.TrimSpace(line), &request) != nil || request.JSONRPC != "2.0" || request.Method == "" {
			if !writeResponse(output, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "Invalid JSON-RPC request"}}) {
				return 2
			}
			if errors.Is(readErr, io.EOF) {
				return 0
			}
			continue
		}
		if len(request.ID) == 0 {
			if errors.Is(readErr, io.EOF) {
				return 0
			}
			continue
		}
		response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
		switch request.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if strictjson.UnmarshalOpen(request.Params, &params) != nil || !supportedVersion(params.ProtocolVersion) {
				response.Error = &rpcError{-32602, "Invalid initialization"}
			} else {
				initialized = true
				response.Result = map[string]any{"protocolVersion": params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "jackstay-chat", "version": "1"}}
			}
		case "ping":
			response.Result = map[string]any{}
		case "tools/list":
			if !initialized {
				response.Error = &rpcError{-32600, "MCP session not initialized"}
			} else {
				response.Result = map[string]any{"tools": toolDefinitions()}
			}
		case "tools/call":
			if !initialized {
				response.Error = &rpcError{-32600, "MCP session not initialized"}
			} else {
				response.Result = server.call(request.Params)
			}
		default:
			response.Error = &rpcError{-32601, "Method not found"}
		}
		if !writeResponse(output, response) {
			return 2
		}
		if errors.Is(readErr, io.EOF) {
			return 0
		}
	}
}
func supportedVersion(version string) bool {
	switch version {
	case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25":
		return true
	}
	return false
}
func writeResponse(output io.Writer, response rpcResponse) bool {
	data, err := json.Marshal(response)
	if err != nil || len(data) > maxResponseBytes {
		return false
	}
	_, err = output.Write(append(data, '\n'))
	return err == nil
}
func toolDefinitions() []map[string]any {
	type field struct {
		Name, Description string
		Properties        map[string]any
		Required          []string
		ReadOnly          bool
	}
	stringField := func(maximum int) map[string]any { return map[string]any{"type": "string", "maxLength": maximum} }
	fields := []field{
		{"chat.self", "Show this connection's session identity and polling capability", map[string]any{}, nil, true},
		{"chat.sessions", "Discover recipient session IDs in this repository", map[string]any{}, nil, true},
		{"chat.send", "Send one direct message or request; retry with the same idempotency key", map[string]any{"to": stringField(64), "kind": map[string]any{"type": "string", "enum": []string{"message", "request"}}, "body": stringField(16384), "idempotency_key": stringField(128)}, []string{"to", "kind", "body", "idempotency_key"}, false},
		{"chat.inbox", "Read pending messages addressed to this session", map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, nil, true},
		{"chat.reply", "Reply to a message addressed to this session", map[string]any{"message_id": stringField(64), "body": stringField(16384), "idempotency_key": stringField(128)}, []string{"message_id", "body", "idempotency_key"}, false},
		{"chat.ack", "Acknowledge one message addressed to this session", map[string]any{"message_id": stringField(64)}, []string{"message_id"}, false},
		{"review.submit", "Queue a review of pinned commits in this repository and subscribe to the outcome", map[string]any{"base": stringField(200), "head": stringField(200), "task": stringField(200), "reviewers": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"codex", "claude-code"}}, "minItems": 1, "maxItems": 2}}, []string{"base", "head", "task", "reviewers"}, false},
		{"review.status", "Read a request submitted by this session in this repository", map[string]any{"job_id": stringField(24)}, []string{"job_id"}, true},
	}
	result := make([]map[string]any, 0, len(fields))
	for _, entry := range fields {
		required := append([]string{}, entry.Required...)
		result = append(result, map[string]any{"name": entry.Name, "description": entry.Description, "inputSchema": map[string]any{"type": "object", "properties": entry.Properties, "required": required, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": entry.ReadOnly, "openWorldHint": false}})
	}
	return result
}
func (server *Server) call(raw json.RawMessage) any {
	var envelope struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Metadata  json.RawMessage `json:"_meta"` // transport metadata, never tool arguments
	}
	if strictjson.Unmarshal(raw, &envelope) != nil {
		return toolFailure("invalid tool call")
	}
	var arguments struct {
		To             string   `json:"to"`
		Kind           string   `json:"kind"`
		Body           string   `json:"body"`
		IdempotencyKey string   `json:"idempotency_key"`
		MessageID      string   `json:"message_id"`
		Limit          int      `json:"limit"`
		Base           string   `json:"base"`
		Head           string   `json:"head"`
		Task           string   `json:"task"`
		Reviewers      []string `json:"reviewers"`
		JobID          string   `json:"job_id"`
	}
	if len(envelope.Arguments) > 0 && strictjson.Unmarshal(envelope.Arguments, &arguments) != nil {
		return toolFailure("invalid arguments")
	}
	allowed := map[string]map[string]bool{
		"chat.self": {}, "chat.sessions": {},
		"chat.send":     {"to": true, "kind": true, "body": true, "idempotency_key": true},
		"chat.inbox":    {"limit": true},
		"chat.reply":    {"message_id": true, "body": true, "idempotency_key": true},
		"chat.ack":      {"message_id": true},
		"review.submit": {"base": true, "head": true, "task": true, "reviewers": true},
		"review.status": {"job_id": true},
	}
	if permitted, found := allowed[envelope.Name]; found && len(envelope.Arguments) > 0 {
		members, err := strictjson.Members(envelope.Arguments)
		if err != nil {
			return toolFailure("invalid arguments")
		}
		for _, member := range members {
			if !permitted[member.Name] {
				return toolFailure("invalid arguments")
			}
		}
	}
	if err := server.store.Touch(server.session.ID); err != nil {
		return toolFailure(err.Error())
	}
	var result any
	var err error
	switch envelope.Name {
	case "chat.self":
		result = map[string]any{"session": server.session, "delivery": "poll-only"}
	case "chat.sessions":
		var sessions []Session
		sessions, err = server.store.Sessions()
		result = map[string]any{"sessions": sessions}
	case "chat.send":
		var message Message
		var duplicate bool
		message, duplicate, err = server.store.Send(server.session.ID, arguments.To, arguments.Kind, arguments.Body, arguments.IdempotencyKey, "")
		message.CodexAccepted = nil
		result = map[string]any{"message": message, "duplicate": duplicate}
	case "chat.inbox":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		notificationErr := syncReviewNotifications(ctx, server.store, server.session.ID)
		cancel()
		if arguments.Limit == 0 {
			arguments.Limit = 50
		}
		var messages []Message
		messages, err = server.store.Inbox(server.session.ID, arguments.Limit)
		if err == nil && len(messages) == 0 {
			err = notificationErr
		}
		for index := range messages {
			messages[index].CodexAccepted = nil
		}
		result = map[string]any{"messages": messages}
	case "chat.reply":
		var message Message
		var duplicate bool
		message, duplicate, err = server.store.Reply(server.session.ID, arguments.MessageID, arguments.Body, arguments.IdempotencyKey)
		message.CodexAccepted = nil
		result = map[string]any{"message": message, "duplicate": duplicate}
	case "chat.ack":
		err = server.store.Acknowledge(server.session.ID, arguments.MessageID)
		result = map[string]any{"acknowledged": err == nil}
	case "review.submit":
		var here string
		here, err = os.Getwd()
		if err != nil {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var common string
		common, err = (gitcli.Git{Dir: here}).CommonDir(ctx)
		if err == nil {
			common, err = filepath.EvalSymlinks(common)
		}
		if err == nil && common != server.store.commonDirectory {
			err = errors.New("MCP session repository changed")
		}
		if err == nil {
			var pool reviewpool.Pool
			pool, err = reviewpool.Default()
			if err == nil {
				var job reviewpool.Job
				var created bool
				job, created, err = pool.EnqueueForSession(ctx, reviewpool.Request{Repository: here, Base: arguments.Base, Head: arguments.Head, Task: arguments.Task, Reviewers: arguments.Reviewers}, server.session.ID, server.store.commonDirectory)
				result = map[string]any{"job": publicReviewJob(job), "created": created}
			}
		}
		cancel()
	case "review.status":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var pool reviewpool.Pool
		pool, err = reviewpool.Default()
		if err == nil {
			var job reviewpool.Job
			job, err = pool.StatusForSession(ctx, arguments.JobID, server.session.ID, server.store.commonDirectory)
			result = map[string]any{"job": publicReviewJob(job)}
		}
		cancel()
	default:
		return toolFailure("unknown tool")
	}
	if err != nil {
		return toolFailure(err.Error())
	}
	encoded, _ := json.Marshal(result)
	if len(encoded) > maxResponseBytes/2 {
		return toolFailure("chat result exceeds its size limit; retry with a smaller inbox limit")
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(encoded)}}, "structuredContent": result}
}

func publicReviewJob(job reviewpool.Job) map[string]any {
	return map[string]any{"id": job.ID, "base": job.Base, "head": job.Head, "task": job.Task,
		"reviewers": job.Reviewers, "status": job.Status, "attempts": job.Attempts,
		"exit": job.Exit, "not_before": job.NotBefore, "failure_class": job.FailureClass, "policy_id": job.PolicyID}
}
func toolFailure(reason string) map[string]any {
	return map[string]any{"content": []map[string]string{{"type": "text", "text": reason}}, "isError": true}
}
