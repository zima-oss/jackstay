package pusher

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
)

type recorder struct {
	sockets []string
	texts   []string
	outcome chatstore.RingOutcome
}

func (ringer *recorder) Ring(_ context.Context, socket, text string) chatstore.RingOutcome {
	ringer.sockets = append(ringer.sockets, socket)
	ringer.texts = append(ringer.texts, text)
	return ringer.outcome
}

func TestAPassRingsEachDueSessionOnceAndNeverCarriesText(t *testing.T) {
	ctx := context.Background()
	moment := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	store := chatstore.Open(filepath.Join(t.TempDir(), "v1"), chatstore.Options{Now: func() time.Time { return moment }})
	channel := chatstore.Channel{ID: strings.Repeat("a", 64), Name: "alpha"}
	for _, registration := range []chatstore.Registration{
		{Key: "cc:a", Name: "a", Delivery: chatstore.DeliveryHooks},
		{Key: "cc:n", Name: "n", Delivery: chatstore.DeliveryNative},
		{Key: "cc:quiet", Name: "quiet", Delivery: chatstore.DeliveryNative},
	} {
		registration.KeySource, registration.Engine, registration.Channel, registration.State =
			chatstore.SourceEngine, chatstore.EngineClaudeCode, channel, chatstore.StateIdle
		if _, err := store.Register(ctx, registration); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Bind(ctx, chatstore.Binding{ProcessID: 42, ProcessStart: "1", Key: "cc:n", Cwd: "/work",
		Socket: "/sockets/n.sock", ClaudeVersion: "2.1.328"}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"n", "quiet"} {
		if _, err := store.Send(ctx, "cc:a", chatstore.Scope{}, chatstore.SendInput{To: to, Message: "secret text for " + to}); err != nil {
			t.Fatal(err)
		}
	}
	ringer := &recorder{outcome: chatstore.RingTaken}
	pass := Pass{Store: store, Ringer: ringer, Alive: func(chatstore.Binding) bool { return true }}
	count, err := pass.Run(ctx)
	if err != nil || count != 2 || len(ringer.sockets) != 1 || ringer.sockets[0] != "/sockets/n.sock" {
		t.Fatalf("%d %v %+v", count, err, ringer)
	}
	if strings.Contains(ringer.texts[0], "secret") || !strings.Contains(ringer.texts[0], "1 new message") {
		t.Fatalf("the doorbell carries a count and no text: %q", ringer.texts[0])
	}
	if count, err := pass.Run(ctx); err != nil || count != 0 || len(ringer.sockets) != 1 {
		t.Fatalf("nothing is rung twice; a session with no socket waits for its next try: %d %v", count, err)
	}
}
