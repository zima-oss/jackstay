package sessionchat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
)

func chatFixture(t *testing.T) (Store, Store) {
	t.Helper()
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	common := filepath.Join(t.TempDir(), ".git")
	if err := os.Mkdir(common, 0o700); err != nil {
		t.Fatal(err)
	}
	policy := config.ChatPolicy{Enabled: true, MaxMessageBytes: 4096, MaxPending: 128, MaxSessions: 32, RetentionHours: 72}
	first, err := StoreFor(common, policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StoreFor(common, policy)
	if err != nil {
		t.Fatal(err)
	}
	return first, second
}
func TestDurableDeliveryAndRecipientAuthority(t *testing.T) {
	senderStore, recipientStore := chatFixture(t)
	sender, err := senderStore.Register("implementer", strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := recipientStore.Register("reviewer", strings.Repeat("r", 32))
	if err != nil {
		t.Fatal(err)
	}
	intruder, err := recipientStore.Register("other", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = senderStore.Inbox(sender.ID, 100); err != nil {
		t.Fatal(err)
	}
	message, duplicate, err := senderStore.Send(sender.ID, recipient.ID, "request", "review this change", "request-1", "")
	if err != nil || duplicate {
		t.Fatalf("send: %+v %v %v", message, duplicate, err)
	}
	replay, replayed, err := senderStore.Send(sender.ID, recipient.ID, "request", "review this change", "request-1", "")
	if err != nil || !replayed || replay.ID != message.ID {
		t.Fatalf("retry: %+v %v %v", replay, replayed, err)
	}
	if err = recipientStore.Acknowledge(intruder.ID, message.ID); err == nil {
		t.Fatal("intruder acknowledged message")
	}
	if _, _, err = recipientStore.Reply(intruder.ID, message.ID, "forged", "reply-1"); err == nil {
		t.Fatal("intruder replied")
	}
	inbox, err := recipientStore.Inbox(recipient.ID, 100)
	if err != nil || len(inbox) != 1 || inbox[0].ID != message.ID {
		t.Fatalf("inbox: %+v %v", inbox, err)
	}
	reply, _, err := recipientStore.Reply(recipient.ID, message.ID, "done", "reply-1")
	if err != nil || reply.To != sender.ID || reply.ReplyTo != message.ID {
		t.Fatalf("reply: %+v %v", reply, err)
	}
	if err = recipientStore.Acknowledge(recipient.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	replay, replayed, err = senderStore.Send(sender.ID, recipient.ID, "request", "review this change", "request-1", "")
	if err != nil || !replayed || replay.ID != message.ID || !replay.Acknowledged {
		t.Fatalf("retry after acknowledgment: %+v %v %v", replay, replayed, err)
	}
	if err = recipientStore.Acknowledge(recipient.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := senderStore.Register("implementer", strings.Repeat("s", 32))
	if err != nil || resumed.ID != sender.ID {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	received, err := senderStore.Inbox(resumed.ID, 100)
	if err != nil || len(received) != 1 || received[0].ID != reply.ID {
		t.Fatalf("reply inbox: %+v %v", received, err)
	}
}

func TestDifferentRepositoriesKeepSeparateNamespaces(t *testing.T) {
	first, _ := chatFixture(t)
	otherCommon := filepath.Join(t.TempDir(), ".git")
	if err := os.Mkdir(otherCommon, 0o700); err != nil {
		t.Fatal(err)
	}
	other, err := StoreFor(otherCommon, first.policy)
	if err != nil {
		t.Fatal(err)
	}
	if first.path == other.path {
		t.Fatal("separate repositories share a chat state")
	}
	sharedToken := strings.Repeat("t", 32)
	firstSession, err := first.Register("same", sharedToken)
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := other.Register("same", sharedToken)
	if err != nil {
		t.Fatal(err)
	}
	if firstSession.ID == otherSession.ID {
		t.Fatal("stable capability correlates separate repositories")
	}
}

func TestReplacementGitDirectoryGetsFreshNamespaceWithoutChangingOldState(t *testing.T) {
	first, _ := chatFixture(t)
	oldSession, err := first.Register("original", strings.Repeat("t", 32))
	if err != nil {
		t.Fatal(err)
	}
	oldData, err := os.ReadFile(first.path)
	if err != nil {
		t.Fatal(err)
	}
	oldDirectory := first.commonDirectory + "-old"
	if err := os.Rename(first.commonDirectory, oldDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(first.commonDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement, err := StoreFor(first.commonDirectory, first.policy)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.path == first.path {
		t.Fatal("replacement repository reused the old chat namespace")
	}
	if sessions, err := replacement.Sessions(); err != nil || len(sessions) != 0 {
		t.Fatalf("replacement discovered old sessions: %+v, %v", sessions, err)
	}
	newSession, err := replacement.Register("replacement", strings.Repeat("t", 32))
	if err != nil || newSession.ID == oldSession.ID {
		t.Fatalf("replacement identity: %+v, %v", newSession, err)
	}
	kept, err := os.ReadFile(first.path)
	if err != nil || !bytes.Equal(kept, oldData) {
		t.Fatalf("legacy state was changed: %v", err)
	}
}

func TestExistingEmptyChatStateIsNotReinitialized(t *testing.T) {
	store, _ := chatFixture(t)
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register("recipient", ""); err == nil {
		t.Fatal("existing empty state was accepted")
	}
	data, err := os.ReadFile(store.path)
	if err != nil || len(data) != 0 {
		t.Fatalf("existing state was overwritten: %v, %q", err, data)
	}
}

func TestExistingOversizeChatStateIsNotReinitialized(t *testing.T) {
	store, _ := chatFixture(t)
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("x"), 12<<20+1)
	if err := os.WriteFile(store.path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register("recipient", ""); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("oversize state was accepted: %v", err)
	}
	info, err := os.Stat(store.path)
	if err != nil || info.Size() != int64(len(data)) {
		t.Fatalf("oversize state was overwritten: %v, %+v", err, info)
	}
}
func TestConcurrentSendAndCapacity(t *testing.T) {
	first, second := chatFixture(t)
	first.policy.MaxPending = 10
	second.policy.MaxPending = 10
	sender, err := first.Register("sender", "")
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := second.Register("recipient", "")
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	var accepted int
	var mutex sync.Mutex
	for index := 0; index < 30; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			store := first
			if index%2 == 0 {
				store = second
			}
			_, _, err := store.Send(sender.ID, recipient.ID, "message", "hello", fmt.Sprintf("key-%d", index), "")
			if err == nil {
				mutex.Lock()
				accepted++
				mutex.Unlock()
			}
		}(index)
	}
	group.Wait()
	inbox, err := second.Inbox(recipient.ID, 100)
	if err != nil || len(inbox) != 10 || accepted != 10 {
		t.Fatalf("accepted %d, inbox %d, error %v", accepted, len(inbox), err)
	}
	if _, _, err = first.Send(sender.ID, "unknown", "message", "hello", "unknown", ""); err == nil {
		t.Fatal("accepted unknown recipient")
	}
}
func TestMCPToolsAndOversizeRequest(t *testing.T) {
	first, second := chatFixture(t)
	sender, err := first.Register("sender", "")
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := second.Register("recipient", "")
	if err != nil {
		t.Fatal(err)
	}
	server := Server{store: first, session: sender}
	request := func(id int, method string, params any) string {
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		return string(data) + "\n"
	}
	calls := request(1, "initialize", map[string]any{"protocolVersion": "2025-11-25"}) + request(2, "tools/list", map[string]any{}) + request(3, "tools/call", map[string]any{"name": "chat.send", "_meta": map[string]any{"progressToken": 3}, "arguments": map[string]any{"to": recipient.ID, "kind": "message", "body": "hello", "idempotency_key": "one"}})
	var output, diagnostics bytes.Buffer
	if status := server.Serve(strings.NewReader(calls), &output, &diagnostics); status != 0 {
		t.Fatalf("status %d: %s", status, diagnostics.String())
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[2], "structuredContent") {
		t.Fatalf("responses: %s", output.String())
	}
	inbox, err := second.Inbox(recipient.ID, 10)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("inbox: %+v %v", inbox, err)
	}
	if status := server.Serve(strings.NewReader(strings.Repeat("x", maxRequestBytes+1)), &output, &diagnostics); status != 2 {
		t.Fatal("oversize request accepted")
	}
}

func TestToolSchemasAndLargestEscapedMessage(t *testing.T) {
	for _, tool := range toolDefinitions() {
		schema := tool["inputSchema"].(map[string]any)
		if schema["required"] == nil {
			t.Fatalf("%s has a null required list", tool["name"])
		}
	}
	first, second := chatFixture(t)
	first.policy.MaxMessageBytes = 16384
	second.policy.MaxMessageBytes = 16384
	sender, err := first.Register("sender", "")
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := second.Register("recipient", "")
	if err != nil {
		t.Fatal(err)
	}
	server := Server{store: first, session: sender}
	body := strings.Repeat("\x01", 16384)
	params, err := json.Marshal(map[string]any{"name": "chat.send", "arguments": map[string]any{"to": recipient.ID, "kind": "message", "body": body, "idempotency_key": "large"}})
	if err != nil {
		t.Fatal(err)
	}
	result := server.call(params).(map[string]any)
	if result["isError"] == true {
		t.Fatalf("large send refused: %+v", result)
	}
	receiver := Server{store: second, session: recipient}
	result = receiver.call([]byte(`{"name":"chat.inbox","arguments":{"limit":1}}`)).(map[string]any)
	if result["isError"] == true {
		t.Fatalf("large inbox refused: %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxResponseBytes {
		t.Fatalf("large result size %d: %v", len(encoded), err)
	}
	result = receiver.call([]byte(`{"name":"chat.inbox","arguments":{"to":"forbidden"}}`)).(map[string]any)
	if result["isError"] != true {
		t.Fatal("irrelevant argument accepted")
	}
}
func TestRetentionPrunesAcknowledgedMessages(t *testing.T) {
	first, _ := chatFixture(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	first.now = func() time.Time { return now }
	first.policy.RetentionHours = 1
	sender, err := first.Register("sender", "")
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := first.Register("recipient", "")
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := first.Send(sender.ID, recipient.ID, "message", "hello", "key", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Acknowledge(recipient.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	first.now = func() time.Time { return now.Add(2 * time.Hour) }
	_, err = first.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(first.path)
	if err != nil {
		t.Fatal(err)
	}
	var state snapshot
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) != 0 {
		t.Fatalf("retained expired messages: %+v", state.Messages)
	}
}

func TestPerRecipientPendingCapLeavesSpaceForOtherSessions(t *testing.T) {
	store, _ := chatFixture(t)
	store.policy.MaxPending = 4
	store.policy.MaxPendingPerSession = 1
	owner, err := store.Register("sender", "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Register("first", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Register("second", "")
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := store.Send(owner.ID, first.ID, "request", "fixture", "first", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Send(owner.ID, first.ID, "message", "full", "blocked", ""); err == nil {
		t.Fatal("accepted a message beyond the recipient quota")
	}
	if _, _, err := store.Send(owner.ID, second.ID, "message", "room", "other", ""); err != nil {
		t.Fatalf("a saturated inbox blocked a different recipient: %v", err)
	}
	if err := store.Acknowledge(first.ID, message.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Send(owner.ID, first.ID, "message", "available", "after", ""); err != nil {
		t.Fatalf("acknowledgment did not free the slot: %v", err)
	}
}
