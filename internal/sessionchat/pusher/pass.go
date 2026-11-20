// Package pusher is one pass of the native doorbell (docs/design/chat.md section 8.3, W-095): it
// lists the sessions whose doorbell is due, rings each one's socket outside the store's lock, and
// records what became of the ring. The ringer that writes the socket's line is given to it, so the
// pass knows nothing of the line's format.
package pusher

import (
	"context"

	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
)

// Ringer rings one session's doorbell on its socket.
type Ringer interface {
	Ring(ctx context.Context, socket, text string) chatstore.RingOutcome
}

// Pass is one pass of the doorbell.
type Pass struct {
	Store  *chatstore.Store
	Ringer Ringer
	// Alive reports whether a binding's process still runs (process ID and start).
	Alive func(chatstore.Binding) bool
}

// Run rings every due doorbell once; it returns how many rings it recorded.
func (pass Pass) Run(ctx context.Context) (int, error) {
	due, err := pass.Store.DueRings(ctx)
	if err != nil || len(due) == 0 {
		return 0, err
	}
	bindings, err := pass.Store.Bindings(ctx)
	if err != nil {
		bindings = nil // every ring then fails, confirmed: nothing was written
	}
	for _, ring := range due {
		outcome := chatstore.RingFailed
		if binding, found := socketOf(ring.Recipient, bindings, pass.Alive); found {
			outcome = pass.Ringer.Ring(ctx, binding.Socket, ring.Text)
		}
		if err := pass.Store.RecordRing(ctx, ring, outcome); err != nil {
			return 0, err
		}
	}
	return len(due), nil
}

// socketOf is the live binding with a socket of the session's key; the newest when several.
func socketOf(key string, bindings []chatstore.Binding, alive func(chatstore.Binding) bool) (chatstore.Binding, bool) {
	var best chatstore.Binding
	found := false
	for _, binding := range bindings {
		if binding.Key != key || binding.Socket == "" || (alive != nil && !alive(binding)) {
			continue
		}
		if !found || binding.Written > best.Written {
			best, found = binding, true
		}
	}
	return best, found
}
