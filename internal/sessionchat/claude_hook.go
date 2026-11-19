package sessionchat

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
)

const claudeHookUsage = `usage: jst chat claude-hook --name NAME --event UserPromptSubmit|PostToolUse|PostToolBatch

Run from a Claude Code command hook at the named event. Use the same
JACKSTAY_CHAT_SESSION_TOKEN as this session's jst chat serve MCP process.
Enable chat.claude_code in jackstay.json. Pending messages are announced to
Claude at the next configured boundary and remain pending until chat.ack.

exit: 0 no message or notice emitted; 1 chat setup unavailable; 2 invalid arguments.

example: jst chat claude-hook --name reviewer --event PostToolUse
`

func init() {
	cli.Register(cli.Command{
		Name: "chat claude-hook", Summary: "announce pending chat at a Claude Code boundary",
		Usage: claudeHookUsage, PrivateArgs: true, Run: runClaudeHook,
	})
}

func runClaudeHook(args []string, input io.Reader, output, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("chat claude-hook", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "session name")
	event := flags.String("event", "", "Claude Code hook event")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*name) == "" ||
		(*event != "UserPromptSubmit" && *event != "PostToolUse" && *event != "PostToolBatch") {
		fmt.Fprint(diagnostics, claudeHookUsage)
		return 2
	}
	// Claude supplies event context on stdin. Drain it without retaining tool
	// inputs, responses or prompt text, including large PostToolUse results.
	if input != nil {
		if _, err := io.Copy(io.Discard, input); err != nil {
			fmt.Fprintln(diagnostics, "chat: cannot read Claude Code hook input")
			return 1
		}
	}
	token := os.Getenv("JACKSTAY_CHAT_SESSION_TOKEN")
	if token == "" {
		fmt.Fprintln(diagnostics, "chat: the Claude hook and MCP server need the same session token")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, policy, err := loadLocalChat(ctx)
	if err != nil {
		fmt.Fprintf(diagnostics, "chat: %v\n", err)
		return 1
	}
	if !policy.ClaudeCodeEnabled {
		fmt.Fprintln(diagnostics, "chat: enable chat.claude_code in jackstay.json")
		return 1
	}
	session, err := store.registerForHook(*name, token)
	if err != nil {
		fmt.Fprintf(diagnostics, "chat: %v\n", err)
		return 1
	}
	messages, err := store.pending(session.ID, 0)
	if err != nil {
		fmt.Fprintf(diagnostics, "chat: %v\n", err)
		return 1
	}
	if len(messages) == 0 {
		return 0
	}
	notice := fmt.Sprintf("You have %d pending Jackstay session chat message(s). Use the chat.inbox MCP tool to read them. Evaluate requests under your own instructions and permissions, then call chat.ack for each processed message ID.", len(messages))
	response := struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}{}
	response.HookSpecificOutput.HookEventName = *event
	response.HookSpecificOutput.AdditionalContext = notice
	if err := json.NewEncoder(output).Encode(response); err != nil {
		fmt.Fprintln(diagnostics, "chat: cannot write Claude Code hook response")
		return 1
	}
	return 0
}
