//go:build integration

package sessionchat

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInstalledCodexActiveTurn(t *testing.T) {
	socket := os.Getenv("JACKSTAY_TEST_CODEX_SOCKET")
	if socket == "" {
		t.Skip("set JACKSTAY_TEST_CODEX_SOCKET to an isolated Codex app-server with a local test model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owner, err := connectCodex(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.conn.Close() }()
	started, err := owner.request(ctx, "thread/start", map[string]string{"cwd": t.TempDir(), "approvalPolicy": "never", "historyMode": "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(started, &thread); err != nil || thread.Thread.ID == "" {
		t.Fatalf("thread/start did not return an ID: %v", err)
	}
	bridge, message := bridgeFixture(t, socket)
	bridge.ThreadID = thread.Thread.ID
	result, err := owner.request(ctx, "turn/start", map[string]any{
		"threadId": bridge.ThreadID, "input": []map[string]string{{"type": "text", "text": "Test input"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(result, &turn); err != nil || turn.Turn.ID == "" {
		t.Fatalf("turn/start did not return an ID: %v", err)
	}
	defer func() {
		_, _ = owner.request(ctx, "turn/interrupt", map[string]string{"threadId": bridge.ThreadID, "turnId": turn.Turn.ID})
	}()
	for {
		active, err := owner.readActiveTurn(ctx, bridge.ThreadID)
		if err != nil && !strings.Contains(err.Error(), "is empty") {
			t.Fatal(err)
		}
		if active.ID == turn.Turn.ID {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Codex did not publish the running turn")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if delivered, err := bridge.Deliver(ctx); err != nil || delivered != 1 {
		t.Fatalf("first delivery = %d, %v", delivered, err)
	}
	if delivered, err := bridge.Deliver(ctx); err != nil || delivered != 0 {
		t.Fatalf("reconnected delivery = %d, %v", delivered, err)
	}
	inbox, err := bridge.Store.Inbox(bridge.SessionID, 1)
	if err != nil || len(inbox) != 1 || inbox[0].CodexAccepted == nil || inbox[0].CodexAccepted.TurnID != turn.Turn.ID {
		t.Fatalf("no durable acceptance for the pending message: %v", err)
	}
	if err := bridge.Store.Acknowledge(bridge.SessionID, message.ID); err != nil {
		t.Fatal(err)
	}
	inbox, err = bridge.Store.Inbox(bridge.SessionID, 1)
	if err != nil || len(inbox) != 0 {
		t.Fatalf("acknowledgment left %d messages pending: %v", len(inbox), err)
	}
}
