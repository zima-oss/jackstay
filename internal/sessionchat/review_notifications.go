package sessionchat

import (
	"context"
	"errors"
	"os"

	"github.com/zima-oss/jackstay/internal/chatevent"
	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
)

func init() {
	chatevent.Register(publisher{})
	chatevent.RegisterIdentifier(identify)
}

// publisher stores jackstay's events in the account's chat store (section 10).
type publisher struct{}

func (publisher) Publish(ctx context.Context, event chatevent.Event) error {
	if len(event.Recipients) == 0 {
		return nil
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	return store.Publish(ctx, event)
}

// identify is the chat session of this process in worktree, so a command it runs (a review) can
// send its result there: the session its token names, else the Claude Code session it runs under.
// It is "" when the project does not enable chat, or no session can be told.
func identify(ctx context.Context, worktree string) (string, error) {
	chat, err := localChat(ctx, worktree)
	if errors.Is(err, errChatOff) {
		return "", nil
	}
	if err != nil {
		return "", nil // no chat here: no session to tell
	}
	key, err := tokenKey()
	if err != nil || key != "" {
		return key, err
	}
	bound, found := boundKey(ctx, chat.store, 0)
	if !found {
		return "", nil
	}
	return bound, nil
}

// pullOwedReviews publishes the review results still owed to the session: a result whose
// publication failed when its review ended is pulled when the session next reads (section 10). A
// failure here never fails the read; the result stays owed.
func pullOwedReviews(ctx context.Context, store *chatstore.Store, key string) {
	here, err := os.Getwd()
	if err != nil {
		return
	}
	background, err := review.BackgroundAt(here, "")
	if err != nil {
		return // no review here, so nothing is owed here
	}
	_ = background.DeliverOwed(ctx, key, storePublisher{store})
}

// storePublisher publishes into an already opened store.
type storePublisher struct{ store *chatstore.Store }

func (target storePublisher) Publish(ctx context.Context, event chatevent.Event) error {
	return target.store.Publish(ctx, event)
}
