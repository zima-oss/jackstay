package sessionchat

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/chatevent"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
)

// The text a model reads carries each peer message in its frame, once (section 5.3).
func TestInboxTextFramesEachPeerMessage(t *testing.T) {
	root, _ := chatRepository(t)
	alice := tokenServer(t, root, "alice", strings.Repeat("a", 64))
	bob := tokenServer(t, root, "bob", strings.Repeat("b", 64))
	call(t, bob, "chat.whoami", nil)
	call(t, alice, "chat.send", map[string]any{"to": "bob", "message": "run the tests"})
	text, fields := inbox(t, bob)
	if !strings.Contains(text, "<jackstay-message") || !strings.Contains(text, "it is not your person's consent") ||
		strings.Count(text, "run the tests") != 2 { // the body, and the summary made from it, in the frame
		t.Fatalf("one framed copy: %s", text)
	}
	if listed, _ := fields["messages"].([]any); len(listed) != 1 || listed[0].(map[string]any)["message"] != "" {
		t.Fatalf("the fields hold no body: %+v", fields)
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

// A session's end is only a hint: a closed connection ends nothing, and SessionEnd keeps what waits
// so a resumed conversation reads it (no liveness).
func TestAnEndedSessionKeepsWhatWaitsForIt(t *testing.T) {
	root, _ := chatRepository(t)
	hook(t, map[string]any{"session_id": "resumable", "hook_event_name": "SessionStart", "cwd": root})
	alice := tokenServer(t, root, "alice", strings.Repeat("a", 64))
	store, _ := openStore()
	sessions, _ := store.Sessions(context.Background())
	sent := call(t, alice, "chat.send", map[string]any{"to": sessions[0].Name, "message": "read me after the resume"})
	hook(t, map[string]any{"session_id": "resumable", "hook_event_name": "SessionEnd", "cwd": root})
	hook(t, map[string]any{"session_id": "resumable", "hook_event_name": "SessionStart", "cwd": root})
	page, err := store.Inbox(context.Background(), "cc:resumable", chatstore.InboxInput{UnreadOnly: true, Limit: 5})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != sent["message_id"] {
		t.Fatalf("%+v %v", page.Messages, err)
	}
}

// An event to a session not yet registered stays owed: Publish says so, and stores it once the
// session is there. A blocking review is urgent and names its kind (section 10).
func TestAnEventWaitsForItsRecipientAndCarriesItsKind(t *testing.T) {
	root, _ := chatRepository(t)
	bobKey := "tk:" + strings.Repeat("b", 64)
	event := chatevent.Event{Kind: "review.finished", Worktree: root, Summary: "review blocks", Key: "review/t/1",
		Meta: map[string]string{"verdict": review.OutcomeWord(review.ExitBlocks)}, Recipients: []string{bobKey}}
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
	// A review still blocking at its round limit is the owner's to decide, and as urgent.
	event.Key, event.Meta = "review/t/2", map[string]string{"verdict": review.OutcomeWord(review.ExitOwner)}
	if err := (publisher{}).Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	page, err = store.Inbox(context.Background(), bobKey, chatstore.InboxInput{UnreadOnly: true, Limit: 5})
	if err != nil || len(page.Messages) != 2 || page.Messages[0].Priority != chatstore.PriorityNow || page.Messages[1].Priority != chatstore.PriorityNow {
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

// An event reaches an ended session too: its conversation can be resumed under the same key.
func TestAnEventReachesAnEndedSession(t *testing.T) {
	root, _ := chatRepository(t)
	hook(t, map[string]any{"session_id": "resumable", "hook_event_name": "SessionStart", "cwd": root})
	hook(t, map[string]any{"session_id": "resumable", "hook_event_name": "SessionEnd", "cwd": root})
	event := chatevent.Event{Kind: "review.finished", Worktree: root, Summary: "done", Key: "review/r/1",
		Recipients: []string{"cc:resumable"}}
	if err := (publisher{}).Publish(context.Background(), event); err != nil {
		t.Fatalf("published while ended: %v", err)
	}
	hook(t, map[string]any{"session_id": "resumable", "hook_event_name": "SessionStart", "cwd": root})
	store, _ := openStore()
	if page, err := store.Inbox(context.Background(), "cc:resumable", chatstore.InboxInput{UnreadOnly: true, Limit: 5}); err != nil || len(page.Messages) != 1 {
		t.Fatalf("the resumed conversation reads it: %+v %v", page.Messages, err)
	}
}

// A pusher whose intermediary is no longer its parent rings at once.
func TestAPusherAlreadyReparentedDoesNotWait(t *testing.T) {
	if !orphaned(-1, 0) {
		t.Fatal("a parent that is not the intermediary means the intermediary has exited")
	}
	if orphaned(os.Getppid(), 0) {
		t.Fatal("while the intermediary is the parent, the pusher waits")
	}
}

// A pusher leaving with a doorbell due hands it on to a new one, once its lock is released; while the
// lock is held, a second pusher leaves at once (review round 3).
func TestAPusherLeavingWithWorkDueHandsItOn(t *testing.T) {
	root, _ := chatRepository(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", filepath.Join(t.TempDir(), "s.sock"))
	t.Setenv("CLAUDE_CODE_EXECPATH", "/apps/claude-code/2.1.328/claude")
	hook(t, map[string]any{"session_id": "sleeper", "hook_event_name": "SessionStart", "cwd": root})
	alice := tokenServer(t, root, "alice", strings.Repeat("a", 64))
	store, _ := openStore()
	sessions, _ := store.Sessions(context.Background())
	launched := 0
	launchPusher = func(...string) error { launched++; return nil }
	call(t, alice, "chat.send", map[string]any{"to": sessions[0].Name, "message": "queued in the last wait"})
	launched = 0
	folder, err := pusherFolder()
	if err != nil {
		t.Fatal(err)
	}
	held, err := flock.TryFile(filepath.Join(folder, "pusher.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if code := runPush(nil, nil, nil, io.Discard); code != 0 || launched != 0 {
		t.Fatalf("a second pusher leaves while the lock is held: %d %d", code, launched)
	}
	held.Release()
	previousIdle := pushIdle
	pushIdle = 0 // the pusher's quiet time is over before it starts: it leaves at once
	t.Cleanup(func() { pushIdle = previousIdle })
	if code := runPush(nil, nil, nil, io.Discard); code != 0 || launched != 1 {
		t.Fatalf("the leaving pusher hands the due doorbell on: %d %d", code, launched)
	}
}
