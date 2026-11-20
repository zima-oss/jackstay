package chatstore

import (
	"context"
	"errors"
	"os"
	"slices"
	"time"

	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// What a hook, a bridge or a launcher observed of a session (sections 3.3 and 8.2).
const (
	ObserveBusy = "busy" // UserPromptSubmit, PostToolBatch, a Codex turn started
	ObserveIdle = "idle" // a Stop that lets the turn end, StopFailure, SessionStart, a Codex turn completed
	ObserveGone = "gone" // SessionEnd, a dead process, a bridge that ended
)

// Kinds of a session's notices (section 6.1).
const (
	NoticeIdle                = "idle"
	NoticeEnded               = "ended"
	NoticeNotDelivered        = "not_delivered"
	NoticeSubscriptionExpired = "subscription_expired"
	idleNoticeKeep            = 24 * time.Hour
)

// Observe records a session's state. A person's prompt (person true, with ObserveBusy) resets the
// session's loop counts (section 4.8). Idle fires the armed idle subscriptions on the session;
// gone fires them all and drops the deliveries still waiting for it (section 3.3).
func (store *Store) Observe(ctx context.Context, key, observed string, person bool) (Session, error) {
	var result Session
	err := store.update(ctx, func(transaction *transaction) error {
		session, err := transaction.sessionOf(key)
		if err != nil {
			return err
		}
		session, err = transaction.observe(session, observed, person)
		result = session
		return err
	})
	return result, err
}

func (transaction *transaction) observe(session Session, observed string, person bool) (Session, error) {
	moment := stamp(transaction.now)
	session.LastSeen = moment
	switch observed {
	case ObserveBusy:
		if person {
			session.Exchange = nil
		}
		if session.State != StateBusy {
			session.State, session.Since = StateBusy, moment
		}
	case ObserveIdle:
		if session.State != StateIdle {
			session.State, session.Since = StateIdle, moment
		}
		session.IdleGeneration++
		session.StopBlocks = 0
	case ObserveGone:
		if session.State != StateGone {
			session.State, session.Since = StateGone, moment
		}
	default:
		return session, refuse(ReasonInvalid, "unknown observation %q", observed)
	}
	if err := transaction.writeSession(session); err != nil {
		return session, err
	}
	if observed == ObserveGone {
		if err := transaction.dropWaiting(session); err != nil {
			return session, err
		}
	}
	return transaction.reconcileWatchers(hashKey(session.Key))
}

// dropWaiting moves the deliveries to a session that ended from pending or sending to
// dropped (recipient_ended), each owing its sender one notice (section 4.2).
func (transaction *transaction) dropWaiting(session Session) error {
	hash := hashKey(session.Key)
	names, err := listNames(transaction.place.inbox(hash))
	if err != nil {
		return err
	}
	for _, id := range names {
		_, err := transaction.step(id, hash, deliverystate.Event{Kind: deliverystate.End})
		switch {
		case err == nil, errors.Is(err, errNoDelivery), errors.Is(err, deliverystate.ErrNotAllowed):
			// delivered, read or final already: an end leaves it as it is
		default:
			return err
		}
	}
	return nil
}

// reconcileWatchers brings the idle subscriptions on a session in line with what the store holds
// (section 4.9), and is the only place they move (W-019). A subscription written with a message
// arms once that message's delivery shows it arrived (delivered without unconfirmed, read, acted) and
// ends with a not_delivered notice once it shows it did not (held, refused, dropped, expired,
// unconfirmed); only an armed one, or a standalone one, then fires: on the session's end, on an idle
// past its generation, or at its `until`. Each notice is written first under a name fixed for its
// subscription, and the subscription removed after, so a crash between them, or a second call,
// fires nothing twice.
func (transaction *transaction) reconcileWatchers(hash string) (Session, error) {
	session, err := transaction.readSession(hash)
	if err != nil || len(session.IdleWatchers) == 0 {
		return session, err
	}
	about := "[" + session.Ref + "]"
	armedAny := false
	var firings []firing
	for index, watcher := range session.IdleWatchers {
		kind := ""
		if !watcher.Armed && watcher.MessageID != "" {
			switch transaction.arrival(watcher.MessageID, hash, session.State == StateGone) {
			case arrived:
				session.IdleWatchers[index].Armed, session.IdleWatchers[index].ArmedGeneration = true, session.IdleGeneration
				watcher, armedAny = session.IdleWatchers[index], true
			case notArrived:
				kind = NoticeNotDelivered
			}
		}
		until, ok := parseStamp(watcher.Until)
		armed := watcher.Armed || watcher.MessageID == ""
		switch {
		case kind != "":
		case armed && session.State == StateGone:
			kind = NoticeEnded
		case armed && session.State == StateIdle && session.IdleGeneration > watcher.ArmedGeneration:
			kind = NoticeIdle
		case !ok || !transaction.now.Before(until):
			kind = NoticeSubscriptionExpired
		}
		if kind != "" {
			id := subscriptionID(session, watcher)
			firings = append(firings, firing{Recipient: hash, Watcher: watcher.Key, Subscription: id,
				Notice: SessionNotice{ID: id, Kind: kind, About: about, MessageID: watcher.MessageID}})
		}
	}
	if armedAny {
		if err := transaction.writeSession(session); err != nil {
			return session, err
		}
	}
	for _, fire := range firings {
		if err := transaction.fire(fire); err != nil {
			return session, err
		}
	}
	return transaction.readSession(hash)
}

// firing is one subscription's outcome: the notice owed to its watcher, and the subscription removed
// from its recipient (section 7.3's `journal/fire-…` entry, always rolled forward).
type firing struct {
	Recipient    string        `json:"recipient"`
	Watcher      string        `json:"watcher"`
	Subscription string        `json:"subscription"`
	Notice       SessionNotice `json:"notice"`
}

// fire journals a firing, applies it, then removes its entry: a crash anywhere in between is finished
// by the next recovery before anything else runs, so a subscription has one outcome and is removed.
func (transaction *transaction) fire(fire firing) error {
	entry := journalEntry{Op: opFire, ID: "fire-" + hashOf(fire.Recipient, fire.Subscription)[:32], Firing: &fire}
	if err := transaction.publishJournal(entry); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := transaction.applyFiring(fire); err != nil {
		return err
	}
	return transaction.removeJournal(entry.ID)
}

// applyFiring writes the notice (once per subscription) and removes the subscription; both are
// idempotent.
func (transaction *transaction) applyFiring(fire firing) error {
	if err := transaction.owe(fire.Watcher, fire.Notice); err != nil {
		return err
	}
	recipient, err := transaction.readSession(fire.Recipient)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(slices.Clone(recipient.IdleWatchers), func(watcher IdleWatcher) bool {
		return subscriptionID(recipient, watcher) == fire.Subscription
	})
	if len(kept) == len(recipient.IdleWatchers) {
		return nil
	}
	recipient.IdleWatchers = kept
	return transaction.writeSession(recipient)
}

// subscriptionID is the one identity of a subscription's outcome: whichever outcome is written first
// is its only one, so a crash between the notice and the removal, or a later call that would judge
// it otherwise, owes nothing more (W-019).
func subscriptionID(recipient Session, watcher IdleWatcher) string {
	return "sub-" + recipient.Ref + "-" + hashOf(watcher.Key, watcher.MessageID, watcher.Until)[:16]
}

// arrivals of a watched message.
const (
	waiting = iota
	arrived
	notArrived
)

// arrival reads what the watched message's delivery says. A recipient that ended with the message
// still waiting has not received it.
func (transaction *transaction) arrival(id, hash string, gone bool) int {
	var delivery Delivery
	if err := readRecord(transaction.place.delivery(id, hash), deliveryBound, &delivery); err != nil {
		return notArrived
	}
	var commit Commit
	closed := readRecord(transaction.place.commit(id), commitBound, &commit) == nil &&
		commit.RequestState == string(deliverystate.Closed)
	switch state := deliverystate.State(delivery.State); {
	case closed && deliverystate.Expirable(state):
		return notArrived // a closure cut short before its delivery's write: the repair rule of 4.4
	case delivery.Reason == string(deliverystate.Unconfirmed):
		return notArrived
	case state == deliverystate.Delivered || state == deliverystate.Read || state == deliverystate.Acted:
		return arrived
	case deliverystate.Final(state) || gone:
		return notArrived
	}
	return waiting
}

// owe writes a notice on a session's record, once per ID.
func (transaction *transaction) owe(key string, notice SessionNotice) error {
	watcher, err := transaction.readSession(hashKey(key))
	if err != nil {
		return nil // the watcher's record is gone: nobody is left to tell
	}
	if slices.ContainsFunc(watcher.Notices, func(existing SessionNotice) bool { return existing.ID == notice.ID }) {
		return nil
	}
	notice.At, notice.State = stamp(transaction.now), string(deliverystate.NoticeOwed)
	watcher.Notices = transaction.addNotice(watcher.Notices, notice)
	return transaction.writeSession(watcher)
}
