package sessionchat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/zima-oss/jackstay/internal/chatevent"
	"github.com/zima-oss/jackstay/internal/review"
)

// systemSender is the sender of jackstay's own events; no client session can have it as an ID.
const systemSender = "jackstay"

func init() {
	chatevent.Register(publisher{})
	chatevent.RegisterIdentifier(identify)
}

// publisher stores jackstay's events in the chat of the event's repository, one message per
// recipient, keyed by the event's key so a repeat stores nothing new.
type publisher struct{}

func (publisher) Publish(ctx context.Context, event chatevent.Event) error {
	if len(event.Recipients) == 0 {
		return nil
	}
	store, _, err := loadChatAt(ctx, event.Worktree)
	if err != nil {
		return err
	}
	return store.publish(event)
}

func (store Store) publish(event chatevent.Event) error {
	lines := []string{event.Summary}
	if event.Body != "" {
		lines = append(lines, event.Body)
	}
	for _, attachment := range event.Attachments {
		lines = append(lines, "attached: "+attachment.URI)
	}
	ending := "event: " + event.Kind + "; call chat.ack once read."
	body := boundMessage(lines, ending, store.policy.MaxMessageBytes)
	var failures []error
	for _, recipient := range event.Recipients {
		key := event.Key
		if len(event.Recipients) > 1 || len(key) > 128 {
			digest := sha256.Sum256([]byte(event.Key + "\x00" + recipient))
			key = "event/" + hex.EncodeToString(digest[:24])
		}
		if _, _, err := store.send(systemSender, recipient, "message", body, key, "", true); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// identify is the chat session of this process in worktree: the one its session token names, once
// that session has registered (through `jst chat serve` or the Claude Code hook).
func identify(ctx context.Context, worktree string) (string, error) {
	token := os.Getenv("JACKSTAY_CHAT_SESSION_TOKEN")
	if token == "" {
		return "", nil
	}
	store, _, err := loadChatAt(ctx, worktree)
	if err != nil {
		return "", nil // chat is not enabled here: no session to tell
	}
	identity, err := store.tokenID(token)
	if err != nil {
		return "", err
	}
	sessions, err := store.Sessions()
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(sessions, func(session Session) bool { return session.ID == identity }) {
		return "", errors.New("this session's chat identity has not registered yet")
	}
	return identity, nil
}

// syncReviewNotifications publishes the review results still owed to the session: a result whose
// publication failed when its review ended is pulled here, when the session next looks.
func syncReviewNotifications(ctx context.Context, store Store, sessionID string) error {
	here, err := os.Getwd()
	if err != nil {
		return err
	}
	background, err := review.BackgroundAt(here, "")
	if err != nil {
		return nil // no review here, so nothing is owed here
	}
	return background.DeliverOwed(ctx, sessionID, chatStore{store})
}

// chatStore publishes into an already opened store.
type chatStore struct{ store Store }

func (target chatStore) Publish(_ context.Context, event chatevent.Event) error {
	return target.store.publish(event)
}

// reviewNotificationSyncError is a failure to publish owed review results, apart from the chat's
// own delivery: the Codex bridge retries it without treating the chat as broken.
type reviewNotificationSyncError struct{ cause error }

func (failure reviewNotificationSyncError) Error() string {
	return "review notification sync: " + failure.cause.Error()
}
func (failure reviewNotificationSyncError) Unwrap() error { return failure.cause }

// boundMessage joins lines and the ending into at most limit bytes: the first line (the summary)
// and the ending always, the rest cut at a UTF-8 boundary, so an event is never refused for its
// size every time it is retried.
func boundMessage(lines []string, ending string, limit int) string {
	whole := strings.Join(append(slices.Clone(lines), ending), "\n")
	if limit <= 0 || len(whole) <= limit {
		return whole
	}
	const marker = "\n…\n"
	room := limit - len(ending) - len(marker)
	head := strings.Join(lines, "\n")
	if room <= 0 {
		return strings.ToValidUTF8(whole[:limit], "")
	}
	cut := head[:min(room, len(head))]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + marker + ending
}
