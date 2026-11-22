package chatstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/zima-oss/jackstay/internal/chatevent"
)

// eventExpirySeconds is an event's expiry (section 10): 24 hours.
const eventExpirySeconds = 24 * 60 * 60

// ErrNotRegistered is a recipient the store does not know yet: the event stays owed to it, to be
// published again once it has registered.
var ErrNotRegistered = errors.New("the recipient has not registered with the chat yet")

// Publish stores one of jackstay's own events (section 10): a message of kind `event` from the sender
// `jackstay`, one to each session in the event's Recipients. A recipient that is gone is skipped: it
// will not read. One the store does not know yet makes Publish return ErrNotRegistered for it, so a
// producer keeps the event owed to it. The event's Key, with the recipient, makes a repeat a
// duplicate, which stores nothing new. Events are stored only while the store stays under 90% of
// each bound (section 7.4); one that does not fit is refused as `capacity`.
//
// Not yet (jackstay 1): finding the producing command's session through the process tree (Origin),
// and the channel-wide events such as `land.merged`.
func (store *Store) Publish(ctx context.Context, event chatevent.Event) error {
	body := eventBody(event)
	priority := PriorityLater
	if event.Kind == "gate.failed" || (event.Kind == "review.finished" && event.Meta["verdict"] == "blocking") {
		priority = PriorityNow
	}
	var failures []error
	for _, recipient := range event.Recipients {
		key := eventKey(event.Key, recipient)
		err := store.update(ctx, func(transaction *transaction) error {
			if _, found, err := transaction.duplicateOf(SystemKey, key); found || err != nil {
				return err
			}
			session, err := transaction.readSession(hashKey(recipient))
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%s: %w", recipient, ErrNotRegistered)
			}
			if err != nil {
				return err
			}
			if session.State == StateGone {
				return nil
			}
			sessions, err := transaction.listSessions()
			if err != nil {
				return err
			}
			plan := planned{
				sender: Session{Key: SystemKey, Name: SystemKey}, recipients: []Session{session}, kind: "event",
				event: event.Kind, body: body, summary: summaryOf(trimmed(event.Summary), body), priority: priority,
				expiry: eventExpirySeconds, to: fullAddress(session), meta: boundedMeta(event.Meta),
				idempotencyKey: key, sessions: sessions,
			}
			_, err = transaction.commitMessage(plan)
			return err
		})
		failures = append(failures, err)
	}
	return errors.Join(failures...)
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

// eventKey is an idempotency key the store accepts, one per event and recipient.
func eventKey(key, recipient string) string {
	if key == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(key + "\x00" + recipient))
	return "event-" + hex.EncodeToString(digest[:24])
}

// boundedMeta keeps the meta entries the store accepts, so an event is never refused for them.
func boundedMeta(meta map[string]string) map[string]string {
	if checkMeta(meta) == nil {
		return meta
	}
	return nil
}
