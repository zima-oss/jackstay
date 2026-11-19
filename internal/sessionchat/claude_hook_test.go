package sessionchat

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
)

func TestClaudeHookNoticesPendingAndWaitsForRecipientAcknowledgment(t *testing.T) {
	project := t.TempDir()
	command := exec.Command("git", "init", "--quiet", project)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	configFile := `{"jackstay_schema":2,"project":"chat-fixture","chat":{"enabled":true,"claude_code":{"enabled":true}}}`
	if err := os.WriteFile(filepath.Join(project, "jackstay.json"), []byte(configFile), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	token := strings.Repeat("r", 32)
	t.Setenv("JACKSTAY_CHAT_SESSION_TOKEN", token)
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	common, err := (gitcli.Git{Dir: project}).CommonDir(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := StoreFor(common, config.ChatSettings(map[string]any{"enabled": true}))
	if err != nil {
		t.Fatal(err)
	}
	sender, err := store.Register("sender", strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := store.Register("recipient", token)
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := store.Send(sender.ID, recipient.ID, "request", "private body", "test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"UserPromptSubmit", "PostToolUse", "PostToolBatch"} {
		var output, diagnostics bytes.Buffer
		arguments := []string{"--name", "recipient", "--event", event}
		if status := runClaudeHook(arguments, strings.NewReader(`{"tool_response":"private tool output"}`), &output, &diagnostics); status != 0 {
			t.Fatalf("%s: exit %d: %s", event, status, diagnostics.String())
		}
		var response struct {
			HookSpecificOutput struct {
				HookEventName     string `json:"hookEventName"`
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(output.Bytes(), &response); err != nil || response.HookSpecificOutput.HookEventName != event ||
			!strings.Contains(response.HookSpecificOutput.AdditionalContext, "1 pending") ||
			strings.Contains(output.String(), message.Body) || strings.Contains(output.String(), "private tool output") {
			t.Fatalf("invalid private notice for %s: %s, %v", event, output.String(), err)
		}
		inbox, err := store.Inbox(recipient.ID, 10)
		if err != nil || len(inbox) != 1 {
			t.Fatalf("hook acknowledged message before agent: %+v %v", inbox, err)
		}
		after, err := os.ReadFile(store.path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("hook rewrote state at %s: %v", event, err)
		}
	}
	var wrongName, wrongDiagnostics bytes.Buffer
	if status := runClaudeHook([]string{"--name", "someone-else", "--event", "PostToolUse"}, nil, &wrongName, &wrongDiagnostics); status != 1 || wrongName.Len() != 0 {
		t.Fatalf("hook accepted a different name for the MCP recipient: %d, %s", status, wrongDiagnostics.String())
	}
	if err := store.Acknowledge(recipient.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	if status := runClaudeHook([]string{"--name", "recipient", "--event", "PostToolUse"}, nil, &output, &diagnostics); status != 0 || output.Len() != 0 {
		t.Fatalf("acknowledged message still announced: exit %d, %s, %s", status, output.String(), diagnostics.String())
	}
	if err := os.WriteFile(filepath.Join(project, "jackstay.json"), []byte(`{"jackstay_schema":2,"project":"chat-fixture","chat":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	diagnostics.Reset()
	if status := runClaudeHook([]string{"--name", "recipient", "--event", "PostToolUse"}, nil, &output, &diagnostics); status != 1 || !strings.Contains(diagnostics.String(), "chat.claude_code") {
		t.Fatalf("hook ignored project opt-in: exit %d, %s", status, diagnostics.String())
	}
}

func TestClaudeHookRejectsUnsupportedEventAndKeepsStableIdentity(t *testing.T) {
	store, _ := chatFixture(t)
	recipient, err := store.Register("recipient", strings.Repeat("r", 32))
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := store.Register("recipient", strings.Repeat("r", 32))
	if err != nil || resumed.ID != recipient.ID {
		t.Fatalf("recipient identity changed across hook invocations: %+v, %v", resumed, err)
	}
	var output, diagnostics bytes.Buffer
	if status := runClaudeHook([]string{"--name", "recipient", "--event", "Stop"}, nil, &output, &diagnostics); status != 2 {
		t.Fatalf("unsupported event exit = %d", status)
	}
}
