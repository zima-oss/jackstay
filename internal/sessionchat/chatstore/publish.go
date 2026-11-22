package chatstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/zima-oss/jackstay/internal/chatevent"
)

// Publish stores one of jackstay's own events (section 10): a message of kind `event` from the sender
// `jackstay`, to the sessions in the event's Recipients. A recipient the store does not know, or one
// that is gone, is skipped; an event with none left is not stored. The event's Key makes a repeat a
// duplicate, which stores nothing new. Events are stored only while the store stays under 90% of
// each bound (section 7.4); one that does not fit is refused as `capacity`.
//
// Not yet (jackstay 1): finding the producing command's session through the process tree (Origin),
// and the channel-wide events such as `land.merged`.
func (store *Store) Publish(ctx context.Context, event chatevent.Event) error {
	if len(event.Recipients) == 0 {
		return nil
	}
	body := eventBody(event)
	key := eventKey(event.Key)
	return store.update(ctx, func(transaction *transaction) error {
		if _, found, err := transaction.duplicateOf(SystemKey, key); found || err != nil {
			return err
		}
		sessions, err := transaction.listSessions()
		if err != nil {
			return err
		}
		var recipients []Session
		for _, session := range sessions {
			if session.State != StateGone && containsKey(event.Recipients, session.Key) {
				recipients = append(recipients, session)
			}
		}
		if len(recipients) == 0 {
			return nil
		}
		address := fullAddress(recipients[0])
		if len(recipients) > 1 {
			address = SystemKey
		}
		plan := planned{
			sender: Session{Key: SystemKey, Name: SystemKey}, recipients: recipients, kind: "event", body: body,
			summary: summaryOf(trimmed(event.Summary), body), priority: PriorityNext, expiry: DefaultExpirySeconds,
			to: address, meta: boundedMeta(event.Meta), idempotencyKey: key, sessions: sessions,
		}
		_, err = transaction.commitMessage(plan)
		return err
	})
}

// eventBody is the event's text: its summary, its body and its attachments, at most MaxBodyBytes.
func eventBody(event chatevent.Event) string {
	lines := []string{strings.TrimSpace(event.Summary)}
	if event.Body != "" {
		lines = append(lines, event.Body)
	}
	for _, attachment := range event.Attachments {
		lines = append(lines, "attached: "+attachment.URI)
	}
	lines = append(lines, "event: "+event.Kind)
	text := strings.ToValidUTF8(strings.Join(lines, "\n"), "")
	if len(text) <= MaxBodyBytes {
		return text
	}
	text = text[:MaxBodyBytes]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}

// eventKey is an idempotency key the store accepts for an event's key of any length.
func eventKey(key string) string {
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(key))
	return "event-" + hex.EncodeToString(digest[:24])
}

// boundedMeta keeps the meta entries the store accepts, so an event is never refused for them.
func boundedMeta(meta map[string]string) map[string]string {
	if checkMeta(meta) == nil {
		return meta
	}
	return nil
}

func containsKey(keys []string, key string) bool {
	for _, each := range keys {
		if each == key {
			return true
		}
	}
	return false
}
