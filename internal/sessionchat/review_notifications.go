package sessionchat

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/zima-oss/jackstay/internal/reviewpool"
)

type reviewNotificationSyncError struct{ cause error }

func (failure reviewNotificationSyncError) Error() string {
	return "review notification sync: " + failure.cause.Error()
}
func (failure reviewNotificationSyncError) Unwrap() error { return failure.cause }

// syncReviewNotifications moves durable queue events into the recipient's chat
// inbox. The queue marks each event only after chat accepts it; repeating after
// an interrupted write uses the same chat idempotency key.
func syncReviewNotifications(ctx context.Context, store Store, sessionID string) error {
	if store.commonDirectory == "" {
		return nil
	}
	pool, err := reviewpool.Default()
	if err != nil {
		return err
	}
	events, err := pool.Undelivered(ctx, sessionID, store.commonDirectory)
	if err != nil {
		return err
	}
	for _, event := range events {
		body := fmt.Sprintf("Review %s attempt %d %s (exit %d). Call review.status for details, then chat.ack.",
			event.JobID, event.Attempt, event.Status, event.Exit)
		key := fmt.Sprintf("review-pool/%s/%d/%s/%s", event.JobID, event.Attempt, event.Status, sessionID)
		if event.Generation != "" {
			digest := sha256.Sum256([]byte(key + "/" + event.Generation))
			key = fmt.Sprintf("review-pool/v2/%x", digest)
		}
		if _, _, err := store.notifyReview(sessionID, body, key); err != nil {
			return err
		}
		if err := pool.MarkDelivered(ctx, event, sessionID); err != nil {
			return err
		}
	}
	return nil
}
