package chatstore

import (
	"errors"
	"os"

	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// errNoDelivery is a delivery that does not exist, or whose message is not committed.
var errNoDelivery = errors.New("no such delivery")

// message is what a step reads of a committed message.
type message struct {
	envelope Envelope
	commit   Commit
}

func (transaction *transaction) message(id string) (message, error) {
	var found message
	if err := readRecord(transaction.place.commit(id), commitBound, &found.commit); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return found, errNoDelivery
		}
		return found, err
	}
	if err := readRecord(transaction.place.envelope(id), envelopeBound, &found.envelope); err != nil {
		return found, err
	}
	return found, nil
}

func (transaction *transaction) overdue(envelope Envelope) bool {
	deadline, ok := parseStamp(envelope.ExpiresAt)
	return !ok || !transaction.now.Before(deadline)
}

// step applies one event to one delivery of a committed message under the store's lock, as the table
// orders (deliverystate.ApplyMessage: the deadline and a closed request first), and writes what
// changed: the request's commit record first, the delivery second (section 4.4). It settles the
// sender's idle subscription that waited on this delivery (section 4.9). The error is the table's.
func (transaction *transaction) step(id, hash string, event deliverystate.Event) (Delivery, error) {
	found, err := transaction.message(id)
	if err != nil {
		return Delivery{}, err
	}
	var delivery Delivery
	if err := readRecord(transaction.place.delivery(id, hash), deliveryBound, &delivery); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Delivery{}, errNoDelivery
		}
		return Delivery{}, err
	}
	return transaction.stepLoaded(found, hash, delivery, event)
}

func (transaction *transaction) stepLoaded(found message, hash string, delivery Delivery, event deliverystate.Event) (Delivery, error) {
	isRequest := found.envelope.Request != nil
	event.Overdue = event.Overdue || transaction.overdue(found.envelope)
	request := deliverystate.Request{}
	if isRequest {
		request = found.commit.request()
	}
	nextRequest, nextDelivery, err := deliverystate.ApplyMessage(request, delivery.table(isRequest), event)
	moment := stamp(transaction.now)
	if isRequest && nextRequest != request {
		commit := found.commit
		commit.RequestState, commit.Notice = string(nextRequest.State), string(nextRequest.Notice)
		if nextRequest.State == deliverystate.Closed && commit.Closed == "" {
			commit.Closed = moment
		}
		if writeErr := writeRecord(transaction.place.commit(found.envelope.ID), commit, commitBound); writeErr != nil {
			return delivery, writeErr
		}
	}
	updated, saveErr := transaction.saveStepped(found.envelope, hash, delivery, nextDelivery)
	if saveErr != nil {
		return updated, saveErr
	}
	return updated, err
}
