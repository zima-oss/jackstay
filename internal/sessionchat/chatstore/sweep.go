package chatstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// Retention and the sweep (section 7.4).
const (
	sweepEvery       = 10 * time.Minute
	staleSending     = 60 * time.Second
	keepDone         = 7 * 24 * time.Hour
	keepDoneEvent    = 24 * time.Hour
	keepAfterExpiry  = 7 * 24 * time.Hour
	keepGoneSessions = 7 * 24 * time.Hour
)

// Sweep runs the sweep now, whenever it last ran.
func (store *Store) Sweep(ctx context.Context) error {
	return store.update(ctx, func(transaction *transaction) error {
		if transaction.swept {
			return nil
		}
		return transaction.sweep()
	})
}

// sweptRecord is swept.json: when the sweep last ran. Losing it only makes the next operation sweep.
type sweptRecord struct {
	V  int    `json:"v"`
	At string `json:"at"`
}

func (place layout) swept() string { return filepath.Join(place.root, "swept.json") }

// sweepIfDue runs the sweep when it has not run for 10 minutes; every operation calls it.
func (transaction *transaction) sweepIfDue() error {
	var record sweptRecord
	if err := readRecord(transaction.place.swept(), 256, &record); err == nil {
		if last, ok := parseStamp(record.At); ok && transaction.now.Sub(last) < sweepEvery && !last.After(transaction.now) {
			return nil
		}
	}
	return transaction.sweep()
}

// sweep moves what nobody touched (a stale `sending`, an overdue delivery or request, a sending
// notice whose pusher died, an idle subscription past its `until`), then removes the messages and
// sessions past their retention. Each change is single-record and idempotent.
func (transaction *transaction) sweep() error {
	ids, err := listNames(transaction.place.commits())
	if err != nil {
		return err
	}
	for _, name := range ids {
		id := name[:len(name)-len(".json")]
		if !messageIDPattern.MatchString(id) {
			continue
		}
		found, err := transaction.message(id)
		if err != nil {
			continue
		}
		if err := transaction.sweepMessage(found); err != nil {
			return err
		}
	}
	sessions, err := transaction.listSessions()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		since, ok := parseStamp(session.Since)
		if session.State == StateGone && ok && transaction.now.Sub(since) >= keepGoneSessions {
			if err := transaction.removeSession(session); err != nil {
				return err
			}
			continue
		}
		if _, err := transaction.reconcileWatchers(hashKey(session.Key)); err != nil {
			return err
		}
	}
	transaction.swept = true
	return writeRecord(transaction.place.swept(), sweptRecord{V: 1, At: stamp(transaction.now)}, 256)
}

// sweepMessage applies the clock to a message's deliveries and request, then removes it when its
// retention is over.
func (transaction *transaction) sweepMessage(found message) error {
	lastChange, _ := parseStamp(found.envelope.Created)
	var deliveries []deliverystate.Delivery
	var whole []bool
	for _, hash := range found.envelope.Recipients {
		var delivery Delivery
		if err := readRecord(transaction.place.delivery(found.envelope.ID, hash), deliveryBound, &delivery); err != nil {
			continue
		}
		entered, _ := parseStamp(delivery.At)
		if delivery.State == string(deliverystate.Sending) && transaction.now.Sub(entered) >= staleSending {
			stale, err := deliverystate.Apply(delivery.table(found.envelope.Request != nil), deliverystate.Event{Kind: deliverystate.Stale})
			if err == nil {
				if delivery, err = transaction.saveStepped(found.envelope, hash, delivery, stale); err != nil {
					return err
				}
			}
		}
		settled, err := transaction.settleDeadline(&found, hash, delivery)
		if err != nil {
			return err
		}
		if found.envelope.Request != nil {
			if refreshed, err := transaction.message(found.envelope.ID); err == nil {
				found = refreshed
			}
		}
		if at, ok := parseStamp(settled.At); ok && at.After(lastChange) {
			lastChange = at
		}
		deliveries = append(deliveries, settled.table(found.envelope.Request != nil))
		whole = append(whole, settled.Whole)
	}
	if found.envelope.Request != nil && len(found.envelope.Recipients) == 0 && transaction.overdue(found.envelope) {
		if found.commit.RequestState == string(deliverystate.Open) {
			found.commit.RequestState, found.commit.Notice, found.commit.Closed = string(deliverystate.Closed), string(deliverystate.NoticeOwed), stamp(transaction.now)
			if err := writeRecord(transaction.place.commit(found.envelope.ID), found.commit, commitBound); err != nil {
				return err
			}
		}
	}
	if closed, ok := parseStamp(found.commit.Closed); ok && closed.After(lastChange) {
		lastChange = closed
	}
	request := deliverystate.Request{}
	if found.envelope.Request != nil {
		request = found.commit.request()
	}
	expires, expiresKnown := parseStamp(found.envelope.ExpiresAt)
	switch {
	case deliverystate.MessageDone(deliveries, whole, request) && !transaction.heldByMailbox(found.envelope):
		keep := keepDone
		if found.envelope.Kind == "event" {
			keep = keepDoneEvent
		}
		if transaction.now.Sub(lastChange) >= keep {
			_, err := transaction.removeMessage(found.envelope)
			return err
		}
	case expiresKnown && transaction.now.Sub(expires) >= keepAfterExpiry:
		transaction.store.log("removed-not-done", found.envelope.ID)
		_, err := transaction.removeMessage(found.envelope)
		return err
	}
	return nil
}

// heldByMailbox reports whether a message sent to or from a review mailbox must stay until that
// mailbox is frozen (section 7.4): a mailbox not yet gone holds it.
func (transaction *transaction) heldByMailbox(envelope Envelope) bool {
	keys := append([]string{hashKey(envelope.From)}, envelope.Recipients...)
	for _, hash := range keys {
		session, err := transaction.readSession(hash)
		if err == nil && session.Mailbox && session.State != StateGone {
			return true
		}
	}
	return false
}

// removeMessage removes a message: its commit record first, its folder fsynced, so it disappears
// at once and a crash cannot bring the record back over files already gone; then everything else
// and its idempotency entry (section 7.3).
func (transaction *transaction) removeMessage(envelope Envelope) (int64, error) {
	place := transaction.place
	id := envelope.ID
	if err := os.Remove(place.commit(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if err := transaction.store.syncFolder(place.commits()); err != nil {
		return 0, err
	}
	freed := deliverystate.CommitCharge + deliverystate.EnvelopeCharge + int64(envelope.BodyBytes)
	for _, hash := range envelope.Recipients {
		if exists(place.delivery(id, hash)) {
			freed += deliverystate.DeliveryCharge
		}
		if removeIfThere(filepath.Join(place.inbox(hash), id)) {
			freed += deliverystate.MarkerCharge
		}
	}
	if removeIfThere(filepath.Join(place.outbox(hashKey(envelope.From)), id)) {
		freed += deliverystate.MarkerCharge
	}
	if envelope.IdempotencyKey != "" {
		path := place.idempotencyEntry(envelope.From, envelope.IdempotencyKey)
		if data, err := os.ReadFile(path); err == nil && string(data) == id { // #nosec G304 -- the store's own entry
			if removeIfThere(path) {
				freed += deliverystate.IdempotencyCharge
			}
		}
	}
	if envelope.Request != nil && removeIfThere(place.requestIndex(envelope.Request.RequestID)) {
		freed += deliverystate.MarkerCharge
	}
	if err := transaction.removeMessageFiles(id); err != nil {
		return 0, err
	}
	return freed, nil
}

func removeIfThere(path string) bool {
	return os.Remove(path) == nil
}

// removeSession removes a session gone for 7 days, with its inbox markers.
func (transaction *transaction) removeSession(session Session) error {
	hash := hashKey(session.Key)
	if err := os.RemoveAll(transaction.place.inbox(hash)); err != nil {
		return err
	}
	if err := os.Remove(transaction.place.session(hash)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// freeRoom removes done messages, oldest first, until charge fits under limit (section 7.4). It
// never removes a message that is not done. Room is measured once from the folders, after the sweep,
// and each removal subtracts what it freed, all under the one lock.
func (transaction *transaction) freeRoom(charge, limit int64, messages int) error {
	if !transaction.swept {
		if err := transaction.sweep(); err != nil {
			return err
		}
	}
	measured, err := transaction.measure()
	if err != nil {
		return err
	}
	fits := func() bool { return measured.Messages < messages && deliverystate.Admit(measured.Bytes, charge, limit) }
	if fits() {
		return nil
	}
	ids, err := listNames(transaction.place.commits())
	if err != nil {
		return err
	}
	sort.Strings(ids)
	for _, name := range ids {
		if fits() {
			return nil
		}
		id := name[:len(name)-len(".json")]
		found, err := transaction.message(id)
		if err != nil {
			continue
		}
		if transaction.done(found) && !transaction.heldByMailbox(found.envelope) {
			freed, err := transaction.removeMessage(found.envelope)
			if err != nil {
				return err
			}
			measured.Bytes, measured.Messages = max(0, measured.Bytes-freed), max(0, measured.Messages-1)
		}
	}
	return nil
}

// done reports whether no delivery, request or notice still needs the message.
func (transaction *transaction) done(found message) bool {
	var deliveries []deliverystate.Delivery
	var whole []bool
	for _, hash := range found.envelope.Recipients {
		var delivery Delivery
		if err := readRecord(transaction.place.delivery(found.envelope.ID, hash), deliveryBound, &delivery); err != nil {
			continue
		}
		deliveries = append(deliveries, delivery.table(found.envelope.Request != nil))
		whole = append(whole, delivery.Whole)
	}
	request := deliverystate.Request{}
	if found.envelope.Request != nil {
		request = found.commit.request()
	}
	return deliverystate.MessageDone(deliveries, whole, request)
}
