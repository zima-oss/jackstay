package chatstore

import (
	"context"
	"errors"
	"os"
	"slices"
	"unicode/utf8"

	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// AckResult is chat.ack's result.
type AckResult struct {
	MessageID string `json:"message_id"`
	State     string `json:"state"`
	Changed   bool   `json:"changed"`
	Detail    string `json:"detail,omitempty"`
}

// Ack declares a message read or handled by its recipient (section 5.2). States move only forward;
// an ack of a final delivery is accepted, changes nothing and says so.
func (store *Store) Ack(ctx context.Context, key, reference, state, note string) (AckResult, error) {
	id := reference
	if !messageIDPattern.MatchString(id) {
		return AckResult{}, refuse(ReasonInvalid, "message_id is a message ID (msg_…)")
	}
	kind := deliverystate.AckActed
	switch state {
	case "", "acted":
	case "read":
		kind = deliverystate.AckRead
	default:
		return AckResult{}, refuse(ReasonInvalid, "state is read or acted")
	}
	if !utf8.ValidString(note) || utf8.RuneCountInString(note) > 200 {
		return AckResult{}, refuse(ReasonInvalid, "a note is at most 200 characters")
	}
	var result AckResult
	err := store.update(ctx, func(transaction *transaction) error {
		session, err := transaction.sessionOf(key)
		if err != nil {
			return err
		}
		hash := hashKey(session.Key)
		found, err := transaction.message(id)
		if errors.Is(err, errNoDelivery) {
			return refuse(ReasonUnknownMessage, "no message %s; messages from 0.5.x were not carried over", id)
		}
		if err != nil {
			return err
		}
		if !slices.Contains(found.envelope.Recipients, hash) {
			return refuse(ReasonNotRecipient, "only the recipient of %s may acknowledge it", id)
		}
		var delivery Delivery
		if err := readRecord(transaction.place.delivery(id, hash), deliveryBound, &delivery); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return refuse(ReasonNotRecipient, "only the recipient of %s may acknowledge it", id)
			}
			return err
		}
		before := delivery.State
		updated, stepErr := transaction.stepLoaded(found, hash, delivery, deliverystate.Event{Kind: kind})
		if stepErr != nil && !errors.Is(stepErr, deliverystate.ErrNotAllowed) {
			return stepErr
		}
		if note != "" && updated.Note != note && !deliverystate.Final(deliverystate.State(before)) {
			updated.Note = note
			if err := transaction.writeDelivery(id, hash, updated); err != nil {
				return err
			}
		}
		result = AckResult{MessageID: id, State: updated.State, Changed: updated.State != before}
		switch updated.State {
		case string(deliverystate.Refused), string(deliverystate.Dropped), string(deliverystate.Expired):
			result.Detail = "this delivery is " + updated.State + "; the ack changes nothing"
		}
		return nil
	})
	return result, err
}
