package sessionchat

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/zima-oss/jackstay/internal/config"
)

type fakeCodex struct {
	listener    net.Listener
	mu          sync.Mutex
	active      bool
	accepted    map[string]string
	lossOnce    bool
	stale       bool
	steers      int
	hideHistory bool
	currentTurn string
}

func startFakeCodex(t *testing.T) (*fakeCodex, string) {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "jcb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "codex.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeCodex{listener: listener, active: true, accepted: make(map[string]string), currentTurn: "turn-1"}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		_ = http.Serve(listener, websocket.Server{
			Handshake: func(*websocket.Config, *http.Request) error { return nil },
			Handler:   websocket.Handler(fake.serve),
		})
	}()
	return fake, path
}

func (fake *fakeCodex) serve(conn *websocket.Conn) {
	defer func() { _ = conn.Close() }()
	for {
		var line string
		if websocket.Message.Receive(conn, &line) != nil {
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal([]byte(line), &request) != nil || len(request.ID) == 0 {
			continue
		}
		var result any
		var failure any
		switch request.Method {
		case "initialize":
			result = map[string]any{"userAgent": "fake"}
		case "thread/turns/list":
			fake.mu.Lock()
			items := []map[string]any{}
			if !fake.hideHistory {
				for id, turnID := range fake.accepted {
					if turnID == fake.currentTurn {
						items = append(items, map[string]any{"type": "userMessage", "clientId": id, "id": "item-" + id, "content": []any{}})
					}
				}
			}
			status := "completed"
			if fake.active {
				status = "inProgress"
			}
			result = map[string]any{"data": []any{map[string]any{"id": fake.currentTurn, "status": status, "items": items}}}
			fake.mu.Unlock()
		case "turn/steer":
			var params struct {
				ExpectedTurnID      string `json:"expectedTurnId"`
				ClientUserMessageID string `json:"clientUserMessageId"`
			}
			_ = json.Unmarshal(request.Params, &params)
			fake.mu.Lock()
			if fake.stale || params.ExpectedTurnID != fake.currentTurn {
				failure = map[string]any{"code": -32602, "message": "stale turn"}
			} else {
				fake.steers++
				fake.accepted[params.ClientUserMessageID] = fake.currentTurn
				result = map[string]string{"turnId": fake.currentTurn}
			}
			lost := fake.lossOnce && failure == nil
			fake.lossOnce = false
			fake.mu.Unlock()
			if lost {
				return // server accepted before connection failure
			}
		default:
			failure = map[string]any{"code": -32601, "message": "unknown"}
		}
		response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result, "error": failure})
		_ = websocket.Message.Send(conn, string(response))
	}
}

func bridgeFixture(t *testing.T, socket string) (CodexBridge, Message) {
	t.Helper()
	store := Store{path: filepath.Join(t.TempDir(), "state.json"), namespace: "test", now: time.Now,
		policy: config.ChatPolicy{Enabled: true, MaxMessageBytes: 4096, MaxPending: 32, MaxSessions: 8, RetentionHours: 72}}
	sender, err := store.Register("sender", "")
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := store.Register("recipient", "")
	if err != nil {
		t.Fatal(err)
	}
	message, _, err := store.Send(sender.ID, recipient.ID, "request", "Can you check this?", "key-1", "")
	if err != nil {
		t.Fatal(err)
	}
	return CodexBridge{Store: store, SessionID: recipient.ID, SocketPath: socket, ThreadID: "thread-1"}, message
}

func TestCodexBridgeAcceptsOnlyActiveTurnAndWaitsForAgentAck(t *testing.T) {
	fake, path := startFakeCodex(t)
	bridge, message := bridgeFixture(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	delivered, err := bridge.Deliver(ctx)
	if err != nil || delivered != 1 {
		t.Fatalf("deliver = %d, %v", delivered, err)
	}
	if inbox, err := bridge.Store.Inbox(bridge.SessionID, 10); err != nil || len(inbox) != 1 || inbox[0].ID != message.ID {
		t.Fatalf("transport acceptance acknowledged message: %+v, %v", inbox, err)
	}
	delivered, err = bridge.Deliver(ctx)
	if err != nil || delivered != 0 || fake.steers != 1 {
		t.Fatalf("replay = %d, %v; steers = %d", delivered, err, fake.steers)
	}
	if err := bridge.Store.Acknowledge(bridge.SessionID, message.ID); err != nil {
		t.Fatal(err)
	}
	delivered, err = bridge.Deliver(ctx)
	if err != nil || delivered != 0 {
		t.Fatalf("after ack = %d, %v", delivered, err)
	}
}

func TestCodexBridgeRecoversLostResponseWithoutDuplicateSteer(t *testing.T) {
	fake, path := startFakeCodex(t)
	fake.lossOnce = true
	bridge, _ := bridgeFixture(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := bridge.Deliver(ctx); err == nil {
		t.Fatal("lost response reported as success")
	}
	delivered, err := bridge.Deliver(ctx)
	if err != nil || delivered != 0 || fake.steers != 1 {
		t.Fatalf("recovery = %d, %v; steers = %d", delivered, err, fake.steers)
	}
}

func TestCodexBridgePersistsPositiveAcceptanceBeforeHistoryCatchesUp(t *testing.T) {
	fake, path := startFakeCodex(t)
	fake.hideHistory = true
	bridge, _ := bridgeFixture(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if delivered, err := bridge.Deliver(ctx); err != nil || delivered != 1 {
		t.Fatalf("first acceptance = %d, %v", delivered, err)
	}
	// A fresh bridge process has no memory; persisted acceptance prevents a
	// duplicate steer while the app-server history has not exposed clientId.
	restarted := CodexBridge{Store: bridge.Store, SessionID: bridge.SessionID, SocketPath: path, ThreadID: bridge.ThreadID}
	if delivered, err := restarted.Deliver(ctx); err != nil || delivered != 0 {
		t.Fatalf("restart = %d, %v", delivered, err)
	}
	fake.mu.Lock()
	steers := fake.steers
	fake.mu.Unlock()
	if steers != 1 {
		t.Fatalf("accepted message steered %d times", steers)
	}
}

func TestCodexBridgeReplaysUnackedMessageInLaterTurn(t *testing.T) {
	fake, path := startFakeCodex(t)
	bridge, message := bridgeFixture(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if delivered, err := bridge.Deliver(ctx); err != nil || delivered != 1 {
		t.Fatalf("first turn = %d, %v", delivered, err)
	}
	if delivered, err := bridge.Deliver(ctx); err != nil || delivered != 0 {
		t.Fatalf("same turn = %d, %v", delivered, err)
	}
	fake.mu.Lock()
	fake.currentTurn = "turn-2"
	fake.mu.Unlock()
	if delivered, err := bridge.Deliver(ctx); err != nil || delivered != 1 {
		t.Fatalf("later turn = %d, %v", delivered, err)
	}
	if inbox, err := bridge.Store.Inbox(bridge.SessionID, 10); err != nil || len(inbox) != 1 || inbox[0].CodexAccepted == nil || inbox[0].CodexAccepted.TurnID != "turn-2" {
		t.Fatalf("later turn acceptance = %+v, %v", inbox, err)
	}
	if err := bridge.Store.Acknowledge(bridge.SessionID, message.ID); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.currentTurn = "turn-3"
	fake.mu.Unlock()
	if delivered, err := bridge.Deliver(ctx); err != nil || delivered != 0 {
		t.Fatalf("acknowledged message in later turn = %d, %v", delivered, err)
	}
}

func TestCodexBridgeLeavesStaleOrIdleTurnPending(t *testing.T) {
	fake, path := startFakeCodex(t)
	bridge, _ := bridgeFixture(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	fake.stale = true
	if _, err := bridge.Deliver(ctx); err == nil || !strings.Contains(err.Error(), "stale turn") {
		t.Fatalf("stale turn = %v", err)
	}
	fake.mu.Lock()
	fake.stale = false
	fake.active = false
	fake.mu.Unlock()
	delivered, err := bridge.Deliver(ctx)
	if err != nil || delivered != 0 || fake.steers != 0 {
		t.Fatalf("idle turn = %d, %v; steers = %d", delivered, err, fake.steers)
	}
}

func TestCodexAcceptanceRequiresRecipientAndDoesNotAcknowledge(t *testing.T) {
	bridge, message := bridgeFixture(t, filepath.Join(t.TempDir(), "unused.sock"))
	if err := bridge.Store.MarkCodexAccepted(message.From, message.ID, "thread-1", "turn-1"); err == nil {
		t.Fatal("sender forged recipient transport acceptance")
	}
	if err := bridge.Store.MarkCodexAccepted(bridge.SessionID, message.ID, "thread-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	inbox, err := bridge.Store.Inbox(bridge.SessionID, 10)
	if err != nil || len(inbox) != 1 || inbox[0].Acknowledged || inbox[0].CodexAccepted == nil {
		t.Fatalf("accepted message must remain pending: %+v, %v", inbox, err)
	}
}
