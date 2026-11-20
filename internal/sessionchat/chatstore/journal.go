package chatstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// The journalled writes of a send or a response (section 7.3).

// Journal operations.
const (
	opSend    = "send"
	opRespond = "respond"
)

// journalEntry names an operation in progress and what follows from its commit.
type journalEntry struct {
	V  int    `json:"v"`
	Op string `json:"op"`
	ID string `json:"id"`
	// Sender is the sending session's key: its outbox marker is written at step 4.
	Sender string `json:"sender"`
	// Idempotency is the base name of the idempotency entry the operation writes, "" without a key.
	Idempotency string `json:"idempotency,omitempty"`
	// Subscription is the idle subscription a send with notify_when_idle writes on its recipient's
	// record once committed (section 4.9).
	Subscription *subscriptionPlan `json:"subscription,omitempty"`
	// Request is the request message a response answers.
	Request string `json:"request,omitempty"`
	// Responder is the hash of the responding session: its delivery of the request becomes acted.
	Responder string `json:"responder,omitempty"`
}

type subscriptionPlan struct {
	Recipient string `json:"recipient"`
	Watcher   string `json:"watcher"`
	Until     string `json:"until"`
	MessageID string `json:"message_id"`
}

// publishJournal writes the entry whole to a temporary file, fsyncs it, links it under its final
// name (a link never replaces a name) and fsyncs the folder, so an entry is never seen half written.
func (transaction *transaction) publishJournal(entry journalEntry) error {
	entry.V = 1
	data, err := encodeRecord(entry, journalBound)
	if err != nil {
		return err
	}
	folder := transaction.place.journal()
	if err := makeFolder(folder); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(folder, ".entry.*.partial")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	_, writeErr := temporary.Write(data)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err := os.Link(name, filepath.Join(folder, entry.ID)); err != nil {
		return err
	}
	return durable.SyncFolder(folder)
}

// removeJournal removes an operation's entry once it is resolved.
func (transaction *transaction) removeJournal(id string) error {
	if err := os.Remove(filepath.Join(transaction.place.journal(), id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return durable.SyncFolder(transaction.place.journal())
}

// recover resolves every journal entry before the caller's own work: it returns how many it
// resolved and how many it had to keep (the store is then stopped).
func (transaction *transaction) recover() (resolved, kept int, err error) {
	names, err := listNames(transaction.place.journal())
	if err != nil {
		return 0, 0, err
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(transaction.place.journal(), name)
		var entry journalEntry
		if readErr := readRecord(path, journalBound, &entry); readErr != nil || entry.ID != name || entry.Op == "" {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			transaction.moveBroken(name)
			resolved++
			continue
		}
		decision, err := transaction.decide(entry)
		if err != nil {
			return resolved, kept, err
		}
		switch decision {
		case deliverystate.KeepEntry:
			kept++
		case deliverystate.RollBack:
			if err := transaction.rollBack(entry); err != nil {
				return resolved, kept, err
			}
			resolved++
		case deliverystate.RollForward:
			if err := transaction.rollForward(entry); err != nil {
				return resolved, kept, err
			}
			resolved++
		}
	}
	return resolved, kept, nil
}

// decide is section 7.3's choice for an entry: back without a commit record, forward once its
// folder is fsynced, kept while it cannot be.
func (transaction *transaction) decide(entry journalEntry) (deliverystate.Recovery, error) {
	if !exists(transaction.place.commit(entry.ID)) {
		return deliverystate.Recover(deliverystate.NoCommit, false), nil
	}
	synced := transaction.store.syncFolder(transaction.place.commits()) == nil
	return deliverystate.Recover(deliverystate.CommitWritten, synced), nil
}

// moveBroken moves an entry that cannot be parsed to journal/broken/, so it never blocks the store.
func (transaction *transaction) moveBroken(name string) {
	if err := makeFolder(transaction.place.broken()); err == nil {
		_ = os.Rename(filepath.Join(transaction.place.journal(), name), filepath.Join(transaction.place.broken(), name))
		_ = durable.SyncFolder(transaction.place.journal())
	}
	transaction.store.log("journal-broken", name)
}

// rollBack removes the files of the operation's message ID and the idempotency entry it names
// (when that names this message); none of them was served.
func (transaction *transaction) rollBack(entry journalEntry) error {
	if err := transaction.removeMessageFiles(entry.ID); err != nil {
		return err
	}
	if entry.Idempotency != "" {
		path := filepath.Join(transaction.place.idempotency(), entry.Idempotency)
		if data, err := durable.ReadLimited(path, 64); err == nil && string(data) == entry.ID {
			_ = os.Remove(path)
		}
	}
	return transaction.removeJournal(entry.ID)
}

// removeMessageFiles removes everything of a message but its commit record, which it never has
// when rolled back.
func (transaction *transaction) removeMessageFiles(id string) error {
	place := transaction.place
	for _, path := range []string{place.body(id), place.envelope(id)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.RemoveAll(place.deliveries(id)); err != nil {
		return err
	}
	return nil
}

// rollForward applies what follows from a durable commit (the rest of step 3), writes the derived
// files (step 4) and removes the entry (step 5). Every part is idempotent.
func (transaction *transaction) rollForward(entry journalEntry) error {
	var envelope Envelope
	if err := readRecord(transaction.place.envelope(entry.ID), envelopeBound, &envelope); err != nil {
		return fmt.Errorf("roll %s forward: %w", entry.ID, err)
	}
	if err := transaction.applyCommitted(entry, envelope); err != nil {
		return err
	}
	if err := transaction.writeDerived(entry, envelope); err != nil {
		return err
	}
	return transaction.removeJournal(entry.ID)
}

// applyCommitted is step 3 after the commit record: the subscription named in the entry, and for a
// response, the request answered and its delivery acted unless final.
func (transaction *transaction) applyCommitted(entry journalEntry, envelope Envelope) error {
	if plan := entry.Subscription; plan != nil {
		if err := transaction.armSubscription(*plan); err != nil {
			return err
		}
	}
	if entry.Op != opRespond {
		return nil
	}
	var request Commit
	if err := readRecord(transaction.place.commit(entry.Request), commitBound, &request); err != nil {
		return fmt.Errorf("the request of %s: %w", entry.ID, err)
	}
	if request.RequestState == string(deliverystate.Open) {
		request.RequestState, request.ResponseID = string(deliverystate.Answered), entry.ID
		if err := writeRecord(transaction.place.commit(entry.Request), request, commitBound); err != nil {
			return err
		}
	}
	path := transaction.place.delivery(entry.Request, entry.Responder)
	var delivery Delivery
	if err := readRecord(path, deliveryBound, &delivery); err != nil {
		return fmt.Errorf("the request's delivery of %s: %w", entry.ID, err)
	}
	next, err := deliverystate.Apply(delivery.table(true), deliverystate.Event{Kind: deliverystate.AckActed, Named: deliverystate.ByHandle})
	if err != nil || string(next.State) == delivery.State {
		return nil
	}
	return transaction.writeDelivery(entry.Request, entry.Responder, delivery.withTable(next, stamp(transaction.now)))
}

// armSubscription writes an idle subscription on its recipient's record, replacing an earlier one
// of the same watcher (section 4.9).
func (transaction *transaction) armSubscription(plan subscriptionPlan) error {
	session, err := transaction.readSession(plan.Recipient)
	if err != nil {
		return err
	}
	watchers := session.IdleWatchers[:0:0]
	for _, watcher := range session.IdleWatchers {
		if watcher.Key != plan.Watcher {
			watchers = append(watchers, watcher)
		}
	}
	watchers = append(watchers, IdleWatcher{Key: plan.Watcher, Until: plan.Until, MessageID: plan.MessageID})
	if len(watchers) > maxWatchers {
		watchers = watchers[len(watchers)-maxWatchers:]
	}
	session.IdleWatchers = watchers
	return transaction.writeSession(session)
}

// writeDerived is step 4: the inbox markers of unread deliveries, the outbox marker and the
// idempotency entry. Each is created only when missing.
func (transaction *transaction) writeDerived(entry journalEntry, envelope Envelope) error {
	place := transaction.place
	for _, recipient := range envelope.Recipients {
		var delivery Delivery
		if err := readRecord(place.delivery(entry.ID, recipient), deliveryBound, &delivery); err != nil {
			return err
		}
		if deliverystate.Unread(deliverystate.State(delivery.State)) {
			if err := createIfMissing(filepath.Join(place.inbox(recipient), entry.ID), nil); err != nil {
				return err
			}
		}
	}
	if err := createIfMissing(filepath.Join(place.outbox(hashKey(entry.Sender)), entry.ID), nil); err != nil {
		return err
	}
	if entry.Idempotency != "" {
		if err := createIfMissing(filepath.Join(place.idempotency(), entry.Idempotency), []byte(entry.ID)); err != nil {
			return err
		}
	}
	return nil
}

// createIfMissing is createExclusive that takes an existing name as done.
func createIfMissing(path string, data []byte) error {
	if err := createExclusive(path, data); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}
