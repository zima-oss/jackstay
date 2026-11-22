package sessionchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/chatevent"
	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
)

type clock struct{ now time.Time }

func (c *clock) read() time.Time { return c.now }

// chatRepository is a Git repository whose jackstay.json enables chat, with the account's store in a
// fresh home and a clock the test moves.
func chatRepository(t *testing.T) (string, *clock) {
	t.Helper()
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	t.Setenv("JACKSTAY_CHAT_SESSION_TOKEN", "")
	t.Setenv("CLAUDE_PID", "")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "")
	t.Setenv("CLAUDE_CODE_EXECPATH", "")
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "jackstay.json"), []byte(`{"jackstay_schema":3,"project":"alpha","chat":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."},
		{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	moment := &clock{now: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)}
	folder := filepath.Join(os.Getenv("JACKSTAY_HOME"), "store")
	previousStore, previousClaude, previousLaunch := openStore, findClaude, launchPusher
	openStore = func() (*chatstore.Store, error) {
		return chatstore.Open(folder, chatstore.Options{Now: moment.read}), nil
	}
	findClaude = func() (procs.Process, bool) { return procs.Process{}, false }
	launchPusher = func(...string) error { return nil }
	t.Cleanup(func() { openStore, findClaude, launchPusher = previousStore, previousClaude, previousLaunch })
	return root, moment
}

// tokenServer is a server for a session with its own token key.
func tokenServer(t *testing.T, root, name, token string) *Server {
	t.Helper()
	chat, err := localChat(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{chat: chat, name: name, token: "tk:" + token}
}

// call runs one tool through a whole MCP exchange and returns its structured result.
func call(t *testing.T, server *Server, tool string, arguments map[string]any) map[string]any {
	t.Helper()
	var input bytes.Buffer
	for _, request := range []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}},
		{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": arguments}},
	} {
		encoded, _ := json.Marshal(request)
		input.Write(append(encoded, '\n'))
	}
	var output, diagnostics bytes.Buffer
	if code := server.Serve(&input, &output, &diagnostics); code != 0 {
		t.Fatalf("serve: %d %s", code, diagnostics.String())
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	var response struct {
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
			Content           []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.IsError {
		t.Fatalf("%s failed: %+v", tool, response.Result.Content)
	}
	return response.Result.StructuredContent
}

func TestTwoSessionsTalkThroughServe(t *testing.T) {
	root, _ := chatRepository(t)
	alice := tokenServer(t, root, "alice", strings.Repeat("a", 64))
	bob := tokenServer(t, root, "bob", strings.Repeat("b", 64))
	call(t, bob, "chat.whoami", nil)
	sent := call(t, alice, "chat.send", map[string]any{"to": "bob", "message": "hello bob", "idempotency_key": "greeting-1"})
	if sent["outcome"] != "accepted" {
		t.Fatalf("send: %+v", sent)
	}
	again := call(t, alice, "chat.send", map[string]any{"to": "bob", "message": "hello bob", "idempotency_key": "greeting-1"})
	if again["outcome"] != "duplicate" || again["message_id"] != sent["message_id"] {
		t.Fatalf("a retry is a duplicate: %+v", again)
	}
	inbox := bob.call(json.RawMessage(`{"name":"chat.inbox","arguments":{}}`)).(map[string]any)
	if text := inbox["content"].([]map[string]any)[0]["text"].(string); !strings.Contains(text, "hello bob") {
		t.Fatalf("inbox: %s", text)
	}
	// The 0.5.x names (section 5.5).
	reply := call(t, bob, "chat.reply", map[string]any{"message_id": sent["message_id"], "body": "hello alice"})
	if reply["outcome"] != "accepted" {
		t.Fatalf("reply: %+v", reply)
	}
	acked := call(t, bob, "chat.ack", map[string]any{"message_id": sent["message_id"]})
	if acked["state"] != "acted" {
		t.Fatalf("chat.ack means acted: %+v", acked)
	}
	if self := call(t, alice, "chat.self", nil); self["name"] != "alice" || self["key_source"] != "token" {
		t.Fatalf("chat.self is chat.whoami: %+v", self)
	}
	refused := call(t, alice, "chat.send", map[string]any{"to": "nobody", "message": "x"})
	if refused["outcome"] != "refused" || refused["reason"] != "unknown_recipient" {
		t.Fatalf("a refusal is an answer: %+v", refused)
	}
}

// failingWriter takes the first write (the initialization's answer) and refuses the rest, as a client
// gone before the inbox's response.
type failingWriter struct{ writes int }

func (writer *failingWriter) Write(data []byte) (int, error) {
	writer.writes++
	if writer.writes > 1 {
		return 0, errors.New("the client is gone")
	}
	return len(data), nil
}

// A notice is marked shown only once the response carrying it is written (section 8.5).
func TestServeMarksNoticesOnlyAfterTheResponseIsWritten(t *testing.T) {
	root, moment := chatRepository(t)
	alice := tokenServer(t, root, "alice", strings.Repeat("a", 64))
	bob := tokenServer(t, root, "bob", strings.Repeat("b", 64))
	call(t, bob, "chat.whoami", nil)
	call(t, alice, "chat.send", map[string]any{"to": "bob", "message": "short-lived", "expires_in_seconds": 60})
	moment.now = moment.now.Add(2 * time.Minute)
	request := func() *bytes.Buffer {
		var input bytes.Buffer
		input.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n")
		input.WriteString(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"chat.inbox","arguments":{}}}` + "\n")
		return &input
	}
	var diagnostics bytes.Buffer
	if code := alice.Serve(request(), &failingWriter{}, &diagnostics); code != 2 {
		t.Fatalf("a failed write ends the connection: %d", code)
	}
	if _, fields := inbox(t, alice); len(fields["notices"].([]any)) != 1 {
		t.Fatalf("a notice whose response was never written comes again: %+v", fields)
	}
	if _, fields := inbox(t, alice); fields["notices"] != nil && len(fields["notices"].([]any)) != 0 {
		t.Fatalf("once written, it is marked: %+v", fields)
	}
}

// hook runs `jst hook claude` with the given input and returns what it printed.
func hook(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	encoded, _ := json.Marshal(input)
	var output, diagnostics bytes.Buffer
	if code := runHook(nil, bytes.NewReader(encoded), &output, &diagnostics); code != 0 {
		t.Fatalf("the hook exits 0: %d", code)
	}
	if diagnostics.Len() > 0 {
		t.Fatalf("hook: %s", diagnostics.String())
	}
	if output.Len() == 0 {
		return nil
	}
	var printed map[string]any
	if err := json.Unmarshal(output.Bytes(), &printed); err != nil {
		t.Fatal(err)
	}
	return printed
}

func TestTheHookRingsAndHoldsATurnForUrgentWork(t *testing.T) {
	root, moment := chatRepository(t)
	session := map[string]any{"session_id": "s-1", "cwd": root}
	with := func(event string, more map[string]any) map[string]any {
		input := map[string]any{"hook_event_name": event}
		for key, value := range session {
			input[key] = value
		}
		for key, value := range more {
			input[key] = value
		}
		return input
	}
	hook(t, with("SessionStart", nil))
	store, _ := openStore()
	sessions, err := store.Sessions(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].Delivery != chatstore.DeliveryHooks || sessions[0].State != chatstore.StateIdle {
		t.Fatalf("SessionStart registers an idle hooks session: %+v %v", sessions, err)
	}
	alice := tokenServer(t, root, "alice", strings.Repeat("a", 64))
	call(t, alice, "chat.send", map[string]any{"to": sessions[0].Name, "message": "secret body"})
	prompt := hook(t, with("UserPromptSubmit", map[string]any{"prompt": "please go on"}))
	told, _ := prompt["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(told, "1 new message") || strings.Contains(told, "secret") {
		t.Fatalf("the prompt rings with no text: %+v", prompt)
	}
	moment.now = moment.now.Add(3 * time.Second)
	call(t, alice, "chat.send", map[string]any{"to": sessions[0].Name, "message": "now please", "priority": "now"})
	if batch := hook(t, with("PostToolBatch", nil)); batch == nil {
		t.Fatal("a tool batch rings for a new message")
	}
	if batch := hook(t, with("PostToolBatch", nil)); batch != nil {
		t.Fatalf("once told and handed over, not again: %+v", batch)
	}
	moment.now = moment.now.Add(3 * time.Second)
	call(t, alice, "chat.send", map[string]any{"to": sessions[0].Name, "message": "stop and read", "priority": "now"})
	if stop := hook(t, with("Stop", nil)); stop["decision"] != "block" {
		t.Fatalf("a Stop holds the turn for an urgent message: %+v", stop)
	}
	hook(t, with("SessionEnd", nil))
	if sessions, _ := store.Sessions(context.Background()); sessions[0].State != chatstore.StateGone {
		t.Fatalf("SessionEnd: %+v", sessions[0])
	}
}

func TestAHookOutsideAChatProjectDoesNothing(t *testing.T) {
	folder := t.TempDir()
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	encoded, _ := json.Marshal(map[string]any{"session_id": "s", "hook_event_name": "UserPromptSubmit", "cwd": folder})
	var output, diagnostics bytes.Buffer
	if code := runHook(nil, bytes.NewReader(encoded), &output, &diagnostics); code != 0 || output.Len() != 0 {
		t.Fatalf("%d %q %q", code, output.String(), diagnostics.String())
	}
}

func TestAnEventReachesItsRecipientsOnce(t *testing.T) {
	root, _ := chatRepository(t)
	bob := tokenServer(t, root, "bob", strings.Repeat("b", 64))
	call(t, bob, "chat.whoami", nil)
	event := chatevent.Event{Kind: "review.finished", Worktree: root, Summary: "review clear", Body: "round 2",
		Recipients: []string{"tk:" + strings.Repeat("b", 64), "tk:unknown"}, Key: "review/task/2"}
	for range 2 {
		if err := (publisher{}).Publish(context.Background(), event); !errors.Is(err, chatstore.ErrNotRegistered) {
			t.Fatalf("the unknown recipient keeps it owed: %v", err)
		}
	}
	store, _ := openStore()
	page, err := store.Inbox(context.Background(), "tk:"+strings.Repeat("b", 64), chatstore.InboxInput{UnreadOnly: true, Limit: 5})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Kind != "event" || !strings.Contains(page.Messages[0].Message, "review clear") {
		t.Fatalf("one event, once: %+v %v", page.Messages, err)
	}
}

func TestAPersonsPromptIsToldFromAFramedOne(t *testing.T) {
	for prompt, person := range map[string]bool{
		"fix the test":                          true,
		"<cross-session-message from=x>hi":      false,
		"  <task-notification>done":             false,
		"<jackstay-message>":                    false,
		"":                                      false,
		"please read <cross-session-message> x": true,
	} {
		if personsPrompt(prompt) != person {
			t.Errorf("%q: want %v", prompt, person)
		}
	}
}

func TestTheVersionComesFromClaudeCodesExecutablePath(t *testing.T) {
	for path, version := range map[string]string{
		"/apps/claude-code/2.1.328/abc/claude.app/Contents/MacOS/claude": "2.1.328",
		"/home/u/.local/share/claude/versions/2.1.330":                   "2.1.330",
		"/usr/local/bin/claude":                                          "",
	} {
		t.Setenv("CLAUDE_CODE_EXECPATH", path)
		if got := claudeVersion(); got != version {
			t.Errorf("%s: %q, want %q", path, got, version)
		}
	}
	if deliveryOf("/run/s.sock", "2.1.328") != chatstore.DeliveryNative || deliveryOf("/run/s.sock", "2.1.330") != chatstore.DeliveryHooks ||
		deliveryOf("", "2.1.328") != chatstore.DeliveryHooks {
		t.Fatal("native needs a socket and a verified version")
	}
}

func TestTheCodexBridgeWaitsForJackstayOne(t *testing.T) {
	var diagnostics bytes.Buffer
	command, _, found := cli.Find([]string{"chat", "codex-bridge"})
	if !found {
		t.Fatal("the command is still registered")
	}
	if code := command.Run(nil, strings.NewReader(""), &bytes.Buffer{}, &diagnostics); code != 2 || !strings.Contains(diagnostics.String(), "jackstay 1") {
		t.Fatalf("%d %q", code, diagnostics.String())
	}
}

// inbox runs chat.inbox through a whole MCP exchange and returns its text and its fields.
func inbox(t *testing.T, server *Server) (string, map[string]any) {
	t.Helper()
	var input bytes.Buffer
	input.WriteString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n")
	input.WriteString(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"chat.inbox","arguments":{}}}` + "\n")
	var output, diagnostics bytes.Buffer
	if code := server.Serve(&input, &output, &diagnostics); code != 0 {
		t.Fatalf("serve: %d %s", code, diagnostics.String())
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &response); err != nil || len(response.Result.Content) != 1 {
		t.Fatalf("%v: %s", err, output.String())
	}
	text := response.Result.Content[0].Text
	index := strings.LastIndex(text, "fields: ")
	if index < 0 {
		t.Fatalf("no fields: %s", text)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(text[index+len("fields: "):])), &fields); err != nil {
		t.Fatal(err)
	}
	return text, fields
}
