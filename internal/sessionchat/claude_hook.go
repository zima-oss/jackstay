package sessionchat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
)

// hookBudget is the dispatcher's time budget (section 8.2); the store's lock deadline keeps it
// inside.
const hookBudget = 2 * time.Second

const hookUsage = `usage: jst hook claude

The Claude Code hook dispatcher for the agent chat (docs/design/chat.md section 8.2). Register it
for SessionStart, UserPromptSubmit, PostToolBatch, Stop, StopFailure and SessionEnd; it reads the
hook's JSON from stdin. It rings the chat's doorbell, holds a turn for an urgent message, and
records the session's state. It never fails a turn: an error is reported on stderr and it exits 0.
The project must enable chat in jackstay.json.

exit: 0 always.

example: jst hook claude
`

func init() {
	cli.Register(cli.Command{Name: "hook claude", Summary: "the Claude Code hook for the agent chat", Usage: hookUsage, Run: runHook})
	cli.Register(cli.Command{Name: "chat claude-hook", Summary: "the old name of jst hook claude", Usage: "usage: jst chat claude-hook [--name NAME] [--event EVENT]\n\nThe 0.5.x name of `jst hook claude`, kept through 0.6.x; its flags are ignored, and the event is read from stdin.\n\nexit: 0 always.\n\nexample: jst chat claude-hook\n", PrivateArgs: true, Run: runOldHook})
}

func runOldHook(_ []string, input io.Reader, output, diagnostics io.Writer) int {
	fmt.Fprintln(diagnostics, "jst chat claude-hook is now jst hook claude; its flags are no longer needed")
	return runHook(nil, input, output, diagnostics)
}

// hookInput is what Claude Code writes to a hook's stdin.
type hookInput struct {
	SessionID      string `json:"session_id"`
	Event          string `json:"hook_event_name"`
	Cwd            string `json:"cwd"`
	Prompt         string `json:"prompt"`
	StopHookActive bool   `json:"stop_hook_active"`
}

func runHook(args []string, input io.Reader, output, diagnostics io.Writer) int {
	if len(args) != 0 {
		fmt.Fprint(diagnostics, hookUsage)
		return 0
	}
	data, err := io.ReadAll(io.LimitReader(input, 1<<20))
	var hook hookInput
	if err == nil {
		err = json.Unmarshal(data, &hook)
	}
	if err != nil || hook.SessionID == "" || hook.Event == "" {
		fmt.Fprintln(diagnostics, "jackstay chat: the hook's input names no session or event")
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookBudget)
	defer cancel()
	answer, err := dispatch(ctx, hook)
	if err != nil {
		if err != errChatOff {
			fmt.Fprintf(diagnostics, "jackstay chat: %v\n", err)
		}
		return 0
	}
	if answer.text == "" {
		return 0
	}
	var reply any
	if answer.block {
		reply = map[string]any{"decision": "block", "reason": answer.text}
	} else {
		reply = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": hook.Event, "additionalContext": answer.text}}
	}
	encoded, _ := json.Marshal(reply)
	if _, err := output.Write(append(encoded, '\n')); err != nil {
		return 0
	}
	// What the ring told of is marked only once the answer is handed over (section 8.4).
	if len(answer.told) > 0 {
		marking, cancel := context.WithTimeout(context.Background(), hookBudget)
		defer cancel()
		if err := answer.store.MarkHookRung(marking, answer.key, answer.told); err != nil {
			fmt.Fprintf(diagnostics, "jackstay chat: %v\n", err)
		}
	}
	return 0
}

type hookAnswer struct {
	text  string
	block bool
	told  []string
	store *chatstore.Store
	key   string
}

// dispatch applies one hook event to the chat (section 8.2).
func dispatch(ctx context.Context, hook hookInput) (hookAnswer, error) {
	folder := hook.Cwd
	if folder == "" {
		folder, _ = os.Getwd()
	}
	chat, err := localChat(ctx, folder)
	if err != nil {
		return hookAnswer{}, err
	}
	key, source := "cc:"+hook.SessionID, chatstore.SourceEngine
	if token, err := tokenKey(); err != nil {
		return hookAnswer{}, err
	} else if token != "" {
		key, source = token, chatstore.SourceToken
	}
	socket, version := os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET"), claudeVersion()
	registration := chatstore.Registration{Key: key, KeySource: source, Engine: chatstore.EngineClaudeCode,
		Channel: chat.channel, Worktree: chat.worktree, Branch: chat.branch, Delivery: deliveryOf(socket, version)}
	if hook.Event == "SessionStart" || hook.Event == "StopFailure" {
		registration.State = chatstore.StateIdle
	}
	if _, err := chat.store.Register(ctx, registration); err != nil {
		return hookAnswer{}, err
	}
	answer := hookAnswer{store: chat.store, key: key}
	ring := func(kind string, stopActive bool) error {
		rung, err := chat.store.HookRing(ctx, key, kind, stopActive)
		answer.text, answer.block, answer.told = rung.Text, rung.Block, rung.Told
		return err
	}
	switch hook.Event {
	case "SessionStart":
		if process, found := findClaude(); found {
			err = chat.store.Bind(ctx, chatstore.Binding{ProcessID: process.PID, ProcessStart: processStart(process),
				Key: key, Cwd: chat.worktree, Socket: socket, ClaudeVersion: version})
		}
		startPusher(ctx, chat.store)
	case "UserPromptSubmit":
		pullOwedReviews(ctx, chat.store, key, folder) // a result its review could not publish (section 10)
		if _, err = chat.store.Observe(ctx, key, chatstore.ObserveBusy, personsPrompt(hook.Prompt)); err == nil {
			err = ring(chatstore.HookPrompt, false)
		}
	case "PostToolBatch", "PostToolUse":
		if _, err = chat.store.Observe(ctx, key, chatstore.ObserveBusy, false); err == nil {
			err = ring(chatstore.HookToolBatch, false)
		}
	case "Stop":
		if err = ring(chatstore.HookStop, hook.StopHookActive); err == nil && !answer.block {
			startPusher(ctx, chat.store)
		}
	case "StopFailure":
		startPusher(ctx, chat.store)
	case "SessionEnd":
		_, err = chat.store.Observe(ctx, key, chatstore.ObserveEnded, false) // a hint: it drops nothing
		if process, found := findClaude(); found && err == nil {
			err = chat.store.Unbind(ctx, process.PID, processStart(process))
		}
	}
	return answer, err
}

// personsPrompt reports whether a prompt came from a person: none of the frames a peer's message, a
// scheduled prompt or a background report starts with (section 8.2). In doubt, it is not.
func personsPrompt(prompt string) bool {
	start := strings.TrimSpace(prompt)
	for _, frame := range []string{"<jackstay-message", "<cross-session-message", "<task-notification", "<channel"} {
		if strings.HasPrefix(start, frame) {
			return false
		}
	}
	return start != ""
}
