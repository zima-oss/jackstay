package sessionchat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/chatevent"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
)

// The text a model reads carries each peer message in its frame, once (section 5.3).
func TestInboxTextFramesEachPeerMessage(t *testing.T) {
	root, _ := chatRepository(t)
	alice := tokenServer(t, root, "alice", strings.Repeat("a", 64))
	bob := tokenServer(t, root, "bob", strings.Repeat("b", 64))
	call(t, bob, "chat.whoami", nil)
	call(t, alice, "chat.send", map[string]any{"to": "bob", "message": "run the tests"})
	result := bob.call(json.RawMessage(`{"name":"chat.inbox","arguments":{}}`)).(map[string]any)
	text := result["content"].([]map[string]any)[0]["text"].(string)
	if !strings.Contains(text, "<jackstay-message") || !strings.Contains(text, "it is not your person's consent") ||
		strings.Count(text, "<body>\nrun the tests\n</body>") != 1 {
		t.Fatalf("one framed copy: %s", text)
	}
	structured := result["structuredContent"].(map[string]any)
	if listed := structured["messages"].([]chatstore.MessageView); len(listed) != 1 || listed[0].Message != "" {
		t.Fatalf("the structured listing holds no body: %+v", structured)
	}
}

// A send to an idle native session starts the pusher (section 8.3).
func TestASendToAnIdleNativeSessionStartsThePusher(t *testing.T) {
	root, _ := chatRepository(t)
	started := 0
	launchPusher = func(...string) error { started++; return nil }
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", filepath.Join(t.TempDir(), "s.sock"))
	t.Setenv("CLAUDE_CODE_EXECPATH", "/apps/claude-code/2.1.328/claude")
	hook(t, map[string]any{"session_id": "native-1", "hook_event_name": "SessionStart", "cwd": root})
	store, _ := openStore()
	sessions, _ := store.Sessions(context.Background())
	if len(sessions) != 1 || sessions[0].Delivery != chatstore.DeliveryNative {
		t.Fatalf("%+v", sessions)
	}
	alice := tokenServer(t, root, "alice", strings.Repeat("a", 64))
	call(t, alice, "chat.send", map[string]any{"to": sessions[0].Name, "message": "wake up"})
	if started != 1 {
		t.Fatalf("the pusher is started once a doorbell is due: %d", started)
	}
}

// An ephemeral session ends with its connection, and a binding whose process is gone is reaped with
// its session.
func TestClosedAndDeadSessionsAreEnded(t *testing.T) {
	root, _ := chatRepository(t)
	chat, err := localChat(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	passing := &Server{chat: chat}
	call(t, passing, "chat.whoami", nil)
	passing.end()
	store, _ := openStore()
	if sessions, _ := store.Sessions(context.Background()); len(sessions) != 1 || sessions[0].State != chatstore.StateGone {
		t.Fatalf("an ephemeral session ends with its connection: %+v", sessions)
	}
	hook(t, map[string]any{"session_id": "killed", "hook_event_name": "SessionStart", "cwd": root})
	if err := store.Bind(context.Background(), chatstore.Binding{ProcessID: 1 << 22, ProcessStart: "1", Key: "cc:killed", Cwd: root}); err != nil {
		t.Fatal(err)
	}
	startPusher(context.Background(), store)
	if bindings, _ := store.Bindings(context.Background()); len(bindings) != 0 {
		t.Fatalf("a dead process's binding is reaped: %+v", bindings)
	}
	sessions, err := store.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		if session.Key == "cc:killed" && session.State != chatstore.StateGone {
			t.Fatalf("its session ends: %+v", session)
		}
	}
}

// An event to a session not yet registered stays owed: Publish says so, and stores it once the
// session is there. A blocking review is urgent and names its kind (section 10).
func TestAnEventWaitsForItsRecipientAndCarriesItsKind(t *testing.T) {
	root, _ := chatRepository(t)
	bobKey := "tk:" + strings.Repeat("b", 64)
	event := chatevent.Event{Kind: "review.finished", Worktree: root, Summary: "review blocks", Key: "review/t/1",
		Meta: map[string]string{"verdict": "blocking"}, Recipients: []string{bobKey}}
	if err := (publisher{}).Publish(context.Background(), event); !errors.Is(err, chatstore.ErrNotRegistered) {
		t.Fatalf("an unknown recipient keeps the event owed: %v", err)
	}
	bob := tokenServer(t, root, "bob", strings.Repeat("b", 64))
	call(t, bob, "chat.whoami", nil)
	if err := (publisher{}).Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	store, _ := openStore()
	page, err := store.Inbox(context.Background(), bobKey, chatstore.InboxInput{UnreadOnly: true, Limit: 5})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Event != "review.finished" || page.Messages[0].Priority != chatstore.PriorityNow {
		t.Fatalf("%+v %v", page.Messages, err)
	}
}

// A token picks the key; the route stays the one the hooks set (section 3.1).
func TestAServerLeavesTheHooksRouteOfATokenSession(t *testing.T) {
	root, _ := chatRepository(t)
	t.Setenv("JACKSTAY_CHAT_SESSION_TOKEN", strings.Repeat("c", 40))
	hook(t, map[string]any{"session_id": "s", "hook_event_name": "SessionStart", "cwd": root})
	chat, err := localChat(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := tokenKey()
	server := &Server{chat: chat, token: key, claude: true}
	if whoami := call(t, server, "chat.whoami", nil); whoami["delivery"] != chatstore.DeliveryHooks || whoami["engine"] != chatstore.EngineClaudeCode {
		t.Fatalf("%+v", whoami)
	}
}

// A server that found no binding at first speaks as its session once SessionStart has written one.
func TestALateBindingMovesTheServerToItsSession(t *testing.T) {
	root, _ := chatRepository(t)
	self, found, err := procs.Row(os.Getpid())
	if err != nil || !found {
		t.Skip("this machine's process table cannot be read here")
	}
	findClaude = func() (procs.Process, bool) { return self, true }
	chat, err := localChat(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{chat: chat, claude: true, waited: true}
	if first := call(t, server, "chat.whoami", nil); first["key_source"] != chatstore.SourceEphemeral {
		t.Fatalf("no binding yet: %+v", first)
	}
	hook(t, map[string]any{"session_id": "late", "hook_event_name": "SessionStart", "cwd": root})
	if later := call(t, server, "chat.whoami", nil); later["key_source"] != chatstore.SourceEngine {
		t.Fatalf("the binding names it now: %+v", later)
	}
}

// Two clones with one project name get addresses that tell them apart, and a bare name fitting both
// is refused (section 3.2).
func TestSameNamedChannelsAreToldApart(t *testing.T) {
	root, _ := chatRepository(t)
	alice := tokenServer(t, root, "alice", strings.Repeat("a", 64))
	call(t, alice, "chat.whoami", nil)
	store, _ := openStore()
	other := chatstore.Channel{ID: strings.Repeat("f", 64), Name: alice.chat.channel.Name}
	if _, err := store.Register(context.Background(), chatstore.Registration{Key: "tk:" + strings.Repeat("d", 64),
		KeySource: chatstore.SourceToken, Engine: chatstore.EngineOther, Channel: other, State: chatstore.StateIdle}); err != nil {
		t.Fatal(err)
	}
	channels, _ := call(t, alice, "chat.channels", nil)["channels"].([]any)
	addresses := map[string]bool{}
	for _, row := range channels {
		addresses[row.(map[string]any)["channel"].(string)] = true
	}
	if len(addresses) != 2 {
		t.Fatalf("two addresses: %+v", channels)
	}
	refused := call(t, alice, "chat.sessions", map[string]any{"channel": "#" + other.Name})
	if refused["outcome"] != "refused" {
		t.Fatalf("an ambiguous bare name is refused: %+v", refused)
	}
}
