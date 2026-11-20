package chatstore

import (
	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// settleDeadline applies what the clock says to one delivery before a step reads or returns it
// (section 4.10): a request whose deadline passed is closed with its delivery in one step
// (deliverystate.ExpireRequest), a request closed by a crash between its two writes gets its
// delivery closed, and an overdue pending or held delivery expires. It returns the delivery as it
// now is, and found's commit record as it now is.
func (transaction *transaction) settleDeadline(found *message, hash string, delivery Delivery) (Delivery, error) {
	isRequest := found.envelope.Request != nil
	table := delivery.table(isRequest)
	overdue := transaction.overdue(found.envelope)
	moment := stamp(transaction.now)
	if isRequest {
		request := found.commit.request()
		switch {
		case request.State == deliverystate.Open && overdue:
			closedRequest, closedDelivery, err := deliverystate.ExpireRequest(request, table)
			if err != nil {
				return delivery, err
			}
			commit := found.commit
			commit.RequestState, commit.Notice, commit.Closed = string(closedRequest.State), string(closedRequest.Notice), moment
			if err := writeRecord(transaction.place.commit(found.envelope.ID), commit, commitBound); err != nil {
				return delivery, err
			}
			found.commit = commit
			return transaction.saveStepped(found.envelope, hash, delivery, closedDelivery)
		case request.State == deliverystate.Closed && deliverystate.Expirable(table.State):
			closed, err := deliverystate.Apply(table, deliverystate.Event{Kind: deliverystate.CloseRequest})
			if err != nil {
				return delivery, err
			}
			return transaction.saveStepped(found.envelope, hash, delivery, closed)
		}
		return delivery, nil
	}
	if overdue && deliverystate.Expirable(table.State) {
		expired, err := deliverystate.Apply(table, deliverystate.Event{Kind: deliverystate.Expire})
		if err != nil {
			return delivery, err
		}
		return transaction.saveStepped(found.envelope, hash, delivery, expired)
	}
	return delivery, nil
}

// saveStepped is the one writer of a delivery the table moved (W-019): it writes the record and
// reconciles the recipient's idle subscriptions with it, so a subscription waiting on this message
// arms, or ends, at the step that decides it.
func (transaction *transaction) saveStepped(envelope Envelope, hash string, delivery Delivery, next deliverystate.Delivery) (Delivery, error) {
	updated := delivery.withTable(next, stamp(transaction.now))
	if updated != delivery {
		if err := transaction.writeDelivery(envelope.ID, hash, updated); err != nil {
			return delivery, err
		}
	}
	if _, err := transaction.reconcileWatchers(hash); err != nil {
		return updated, err
	}
	return updated, nil
}

// settleMessage applies the clock to every delivery of a message before a read renders it, so the
// request it shows is the request as it now is (section 4.10).
func (transaction *transaction) settleMessage(found *message) error {
	for _, hash := range found.envelope.Recipients {
		var delivery Delivery
		if err := readRecord(transaction.place.delivery(found.envelope.ID, hash), deliveryBound, &delivery); err != nil {
			continue
		}
		if _, err := transaction.settleDeadline(found, hash, delivery); err != nil {
			return err
		}
	}
	if found.envelope.Request != nil && len(found.envelope.Recipients) == 0 && transaction.overdue(found.envelope) &&
		found.commit.RequestState == string(deliverystate.Open) {
		found.commit.RequestState, found.commit.Notice, found.commit.Closed =
			string(deliverystate.Closed), string(deliverystate.NoticeOwed), stamp(transaction.now)
		return writeRecord(transaction.place.commit(found.envelope.ID), found.commit, commitBound)
	}
	return nil
}
