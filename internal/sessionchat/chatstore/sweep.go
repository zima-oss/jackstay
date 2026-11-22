package chatstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// Retention and the sweep (section 7.4).
const (
	sweepEvery      = 10 * time.Minute
	staleSending    = 60 * time.Second
	keepDone        = 7 * 24 * time.Hour
	keepDoneEvent   = 24 * time.Hour
	keepAfterExpiry = 7 * 24 * time.Hour
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
	if err := transaction.finishRemovals(); err != nil {
		return err
	}
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
			return nil // a delivery it cannot read may still need the message: leave it for a later sweep
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
	if transaction.heldByMailbox(found.envelope) {
		return nil // what the next round must read stays until its mailbox is frozen (section 7.4)
	}
	switch {
	case deliverystate.MessageDone(deliveries, whole, request):
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
// mailbox is frozen (section 7.4): a mailbox not yet frozen holds it.
func (transaction *transaction) heldByMailbox(envelope Envelope) bool {
	keys := append([]string{hashKey(envelope.From)}, envelope.Recipients...)
	for _, hash := range keys {
		session, err := transaction.readSession(hash)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return true // a record it cannot read may be an open mailbox's: keep the message
		case session.Mailbox && !session.Frozen:
			return true
		}
	}
	return false
}

// removalStep is one file of a message's removal: its path, and what its charge frees.
type removalStep struct {
	path   string
	charge int64
	folder bool // a folder, removed with whatever is left in it
}

// removalSteps are a message's files in removal order (section 7.3): the commit record first, so the
// message disappears at once, then its markers, its deliveries, the request index, the idempotency
// entry if it names this message, the body, and the envelope last. The envelope is the one file the
// sweep finds a stopped removal by, so it goes last.
func (transaction *transaction) removalSteps(envelope Envelope) ([]removalStep, error) {
	place := transaction.place
	id := envelope.ID
	steps := []removalStep{{path: place.commit(id), charge: deliverystate.CommitCharge}}
	for _, hash := range envelope.Recipients {
		steps = append(steps, removalStep{path: filepath.Join(place.inbox(hash), id), charge: deliverystate.MarkerCharge})
	}
	steps = append(steps, removalStep{path: filepath.Join(place.outbox(hashKey(envelope.From)), id), charge: deliverystate.MarkerCharge})
	for _, hash := range envelope.Recipients {
		steps = append(steps, removalStep{path: place.delivery(id, hash), charge: deliverystate.DeliveryCharge})
	}
	steps = append(steps, removalStep{path: place.deliveries(id), folder: true})
	if envelope.Request != nil {
		steps = append(steps, removalStep{path: place.requestIndex(envelope.Request.RequestID), charge: deliverystate.MarkerCharge})
	}
	if envelope.IdempotencyKey != "" {
		path := place.idempotencyEntry(envelope.From, envelope.IdempotencyKey)
		data, err := durable.ReadLimited(path, 64)
		switch {
		case err == nil && string(data) == id:
			steps = append(steps, removalStep{path: path, charge: deliverystate.IdempotencyCharge})
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return nil, err
		}
	}
	return append(steps,
		removalStep{path: place.body(id), charge: int64(envelope.BodyBytes)},
		removalStep{path: place.envelope(id), charge: deliverystate.EnvelopeCharge}), nil
}

// removeMessage removes a message step by step in removalSteps' order. The commit record's unlink
// is fsynced before anything else goes, so a machine crash cannot bring the record back over files
// already gone. Each step is idempotent: a file already gone counts as done, and any other error
// stops the removal, for the next sweep to finish. It returns the charges it freed.
func (transaction *transaction) removeMessage(envelope Envelope) (int64, error) {
	steps, err := transaction.removalSteps(envelope)
	if err != nil {
		return 0, err
	}
	var freed int64
	for index, step := range steps {
		if step.folder {
			if err := os.RemoveAll(step.path); err != nil {
				return freed, err
			}
			continue
		}
		err := os.Remove(step.path)
		switch {
		case err == nil:
			freed += step.charge
		case !errors.Is(err, os.ErrNotExist):
			return freed, err
		}
		if index == 0 {
			if err := transaction.store.syncFolder(transaction.place.commits()); err != nil {
				return freed, err
			}
		}
	}
	return freed, nil
}

// finishRemovals finishes the removals a stop cut short (section 7.3): an envelope whose message has
// neither a commit record nor a journal entry, each known absent (an error that leaves it unknown
// leaves the envelope alone).
func (transaction *transaction) finishRemovals() error {
	names, err := listNames(transaction.place.messages())
	if err != nil {
		return err
	}
	for _, name := range names {
		id := strings.TrimSuffix(name, ".json")
		if !messageIDPattern.MatchString(id) || !absent(transaction.place.commit(id)) ||
			!absent(filepath.Join(transaction.place.journal(), id)) {
			continue
		}
		var envelope Envelope
		if err := readRecord(transaction.place.envelope(id), envelopeBound, &envelope); err != nil {
			continue // an envelope it cannot read is left as it is
		}
		if _, err := transaction.removeMessage(envelope); err != nil {
			return err
		}
		transaction.store.log("removal-finished", id)
	}
	return nil
}

// absent reports whether path is known not to exist.
func absent(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

// evictSession removes the record of the session the session cap evicts (section 6.4), and only
// that: its inbox markers and deliveries stay, under its key, so the same conversation finds its
// unread messages again when it registers anew; what nobody reads expires by its own deadline.
func (transaction *transaction) evictSession(session Session) error {
	if err := os.Remove(transaction.place.session(hashKey(session.Key))); err != nil && !errors.Is(err, os.ErrNotExist) {
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
			return false // a delivery it cannot read may still need the message
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
