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
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

const maxRequestBytes = 128 << 10
const maxResponseBytes = 1 << 20

// bindingWait is how long a tool call waits for the hook's binding (section 3.1).
const bindingWait = 2 * time.Second

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

// Server is one MCP stdio connection of `jst chat serve` (section 5).
type Server struct {
	chat local
	// name is the --name a launcher gave; "" keeps the session's own.
	name string
	// fixed is the key of a token or ephemeral session; "" when the key comes from the binding.
	fixed, source string
	// afterWrite runs once a response is written: what a tool marks only after its handoff.
	afterWrite func()
}

const serveUsage = `usage: jst chat serve [--name NAME]

Serve the agent chat to one session over stdio MCP (docs/design/chat.md section 5). A Claude Code
session is found through the binding its jst hook claude wrote; JACKSTAY_CHAT_SESSION_TOKEN gives
a session of its own; without either the session is ephemeral. The project must enable chat in
jackstay.json.

exit: 0 end of input; 2 invalid configuration, arguments or transport.

example: jst chat serve
`

func init() {
	cli.Register(cli.Command{Name: "chat serve", Summary: "serve the agent chat to one session over MCP", Usage: serveUsage, PrivateArgs: true, Run: Run})
}

// Run starts the chat MCP server from an enabled project.
func Run(args []string, input io.Reader, output, diagnostics io.Writer) int {
	name := ""
	switch {
	case len(args) == 0:
	case len(args) == 2 && args[0] == "--name" && chatstore.ValidName(args[1]):
		name = args[1]
	default:
		fmt.Fprint(diagnostics, serveUsage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	here, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(diagnostics, "chat: cannot read the working directory")
		return 2
	}
	chat, err := localChat(ctx, here)
	if err != nil {
		fmt.Fprintf(diagnostics, "chat: %v\n", err)
		return 2
	}
	server := &Server{chat: chat, name: name}
	key, err := tokenKey()
	if err != nil {
		fmt.Fprintf(diagnostics, "chat: %v\n", err)
		return 2
	}
	switch {
	case key != "":
		server.fixed, server.source = key, chatstore.SourceToken
	default:
		if _, found := claudeProcess(); !found {
			server.fixed, server.source = ephemeralKey(), chatstore.SourceEphemeral
		}
	}
	return server.Serve(input, output, diagnostics)
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
				response.Result = map[string]any{"protocolVersion": params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "jackstay-chat", "version": "2"}}
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
		if server.afterWrite != nil {
			server.afterWrite()
			server.afterWrite = nil
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

// session is the session this call speaks as: registered (or refreshed) in the repository's
// channel. A Claude Code session's key comes from its binding, read on every call so a /clear moves
// the server to the new key; without a binding after the wait the server is ephemeral.
func (server *Server) session(ctx context.Context) (chatstore.Session, error) {
	key, source, engine, delivery := server.fixed, server.source, chatstore.EngineOther, chatstore.DeliveryPoll
	if key == "" {
		bound, found := boundKey(ctx, server.chat.store, bindingWait)
		if found {
			key, source, engine, delivery = bound, chatstore.SourceEngine, chatstore.EngineClaudeCode, ""
		} else {
			server.fixed, server.source = ephemeralKey(), chatstore.SourceEphemeral
			key, source = server.fixed, server.source
		}
	}
	registration := chatstore.Registration{Key: key, KeySource: source, Engine: engine, Name: server.name,
		Channel: server.chat.channel, Worktree: server.chat.worktree, Branch: server.chat.branch, Delivery: delivery}
	return server.chat.store.Register(ctx, registration)
}

func toolFailure(reason string) map[string]any {
	return map[string]any{"content": []map[string]string{{"type": "text", "text": reason}}, "isError": true}
}

// toolResult is a tool's answer, its JSON as text and as structured content.
func toolResult(result any) map[string]any {
	encoded, err := json.Marshal(result)
	if err != nil {
		return toolFailure("the result could not be encoded")
	}
	if len(encoded) > maxResponseBytes/2 {
		return toolFailure("the result exceeds its size limit; ask for fewer")
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(encoded)}}, "structuredContent": result}
}

// refusalResult is a refusal of section 5.4 as a tool's answer.
func refusalResult(err error) map[string]any {
	var refusal *chatstore.Refusal
	if errors.As(err, &refusal) {
		return toolResult(map[string]any{"outcome": "refused", "reason": refusal.Reason, "detail": refusal.Detail})
	}
	return toolFailure(err.Error())
}
