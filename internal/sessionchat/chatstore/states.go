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
	// ObserveEnded is SessionEnd: a hint for listings and status, which changes nothing else. There
	// is no liveness: what waits for an ended session stays, and a resumed conversation reads it.
	ObserveEnded = "ended"
)

// Kinds of a session's notices (section 6.1).
const (
	NoticeIdle                = "idle"
	NoticeNotDelivered        = "not_delivered"
	NoticeSubscriptionExpired = "subscription_expired"
	idleNoticeKeep            = 24 * time.Hour
)

// Observe records a session's state. A person's prompt (person true, with ObserveBusy) resets the
// session's loop counts (section 4.8). Idle fires the armed idle subscriptions on the session
// (section 3.3); an end is only shown.
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
		// What already arrived is armed at the generation it arrived in before this idle advances
		// it: a receipt written just before a killed process never makes the subscription miss
		// this idle.
		reconciled, err := transaction.reconcileWatchers(hashKey(session.Key))
		if err != nil {
			return session, err
		}
		session.IdleWatchers = reconciled.IdleWatchers
		session.Notices = reconciled.Notices
		if session.State != StateIdle {
			session.State, session.Since = StateIdle, moment
		}
		session.IdleGeneration++
		session.IdleAt = moment
		session.StopBlocks = 0
	case ObserveEnded:
		if session.State != StateEnded {
			session.State, session.Since = StateEnded, moment
		}
	default:
		return session, refuse(ReasonInvalid, "unknown observation %q", observed)
	}
	if observed != ObserveIdle {
		// Busy and an end fire nothing: a plain write.
		if err := transaction.writeSession(session); err != nil {
			return session, err
		}
		return transaction.reconcileWatchers(hashKey(session.Key))
	}
	// An idle is journalled with the work it triggers (the subscriptions it fires), so a crash
	// between the state and that work is finished by the next recovery, before any later
	// observation can overwrite the state.
	entry := journalEntry{Op: opObserve, ID: "observe-" + hashKey(session.Key)[:32],
		Observed: &observation{Session: hashKey(session.Key), State: session.State, Since: session.Since,
			Generation: session.IdleGeneration, IdleAt: session.IdleAt}}
	if err := transaction.publishJournal(entry); err != nil && !errors.Is(err, os.ErrExist) {
		return session, err
	}
	if err := transaction.writeSession(session); err != nil {
		return session, err
	}
	result, err := transaction.applyObservation(*entry.Observed)
	if err != nil {
		return result, err
	}
	return result, transaction.removeJournal(entry.ID)
}

// observation is an idle in progress (an opObserve journal entry).
type observation struct {
	Session    string `json:"session"`
	State      string `json:"state"`
	Since      string `json:"since"`
	Generation int    `json:"generation"`
	IdleAt     string `json:"idle_at,omitempty"`
}

// applyObservation finishes an idle: the state is on the session's record (a replay that finds an
// older one writes it), and the subscriptions are reconciled. Every part is idempotent.
func (transaction *transaction) applyObservation(observed observation) (Session, error) {
	session, err := transaction.readSession(observed.Session)
	if errors.Is(err, os.ErrNotExist) {
		return session, nil
	}
	if err != nil {
		return session, err
	}
	if session.IdleGeneration < observed.Generation {
		session.State, session.Since = observed.State, observed.Since
		if observed.Generation > session.IdleGeneration {
			session.IdleAt = observed.IdleAt
			session.StopBlocks = 0 // an idle ends the turn the Stop count belongs to
		}
		session.IdleGeneration = max(session.IdleGeneration, observed.Generation)
		if err := transaction.writeSession(session); err != nil {
			return session, err
		}
	}
	return transaction.reconcileWatchers(observed.Session)
}

// reconcileWatchers brings the idle subscriptions on a session in line with what the store holds
// (section 4.9), and is the only place they move (W-019). A subscription written with a message
// arms once that message's delivery shows it arrived (delivered without unconfirmed, read, acted) and
// ends with a not_delivered notice once it shows it did not (held, refused, dropped, expired,
// unconfirmed); only an armed one, or a standalone one, then fires: on an idle past its generation,
// or at its `until`. Each notice is written first under a name fixed for its
// subscription, and the subscription removed after, so a crash between them, or a second call,
// fires nothing twice.
func (transaction *transaction) reconcileWatchers(hash string) (Session, error) {
	session, err := transaction.readSession(hash)
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, nil // an evicted session holds no subscriptions
	}
	if err != nil || len(session.IdleWatchers) == 0 {
		return session, err
	}
	about := "[" + session.Ref + "]"
	armedAny := false
	var firings []firing
	for index, watcher := range session.IdleWatchers {
		until, ok := parseStamp(watcher.Until)
		// Each outcome is judged by when its own event happened, never by when this runs: the idle
		// that advanced the generation (idle_at), the delivery's final state (its at). An event at or after until counts for nothing: the subscription expired.
		before := func(moment time.Time, known bool) bool { return ok && known && moment.Before(until) }
		kind := ""
		if !watcher.Armed && watcher.MessageID != "" {
			outcome, when, err := transaction.arrival(watcher.MessageID, hash, session)
			if err != nil {
				return session, err // a delivery it cannot read leaves the subscription waiting
			}
			switch {
			case outcome == arrived:
				session.IdleWatchers[index].Armed, session.IdleWatchers[index].ArmedGeneration = true, session.IdleGeneration
				watcher, armedAny = session.IdleWatchers[index], true
			case outcome == notArrived && before(when, true):
				kind = NoticeNotDelivered
			case outcome == notArrived:
				kind = NoticeSubscriptionExpired
			}
		}
		armed := watcher.Armed || watcher.MessageID == ""
		idleAt, idleKnown := parseStamp(session.IdleAt)
		switch {
		case kind != "":
		case armed && session.IdleGeneration > watcher.ArmedGeneration && before(idleAt, idleKnown):
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

// arrival reads what the watched message's delivery says, and when it came to say it. A record that
// cannot be read is an error, never an outcome.
func (transaction *transaction) arrival(id, hash string, session Session) (int, time.Time, error) {
	var delivery Delivery
	err := readRecord(transaction.place.delivery(id, hash), deliveryBound, &delivery)
	if errors.Is(err, os.ErrNotExist) {
		return notArrived, transaction.now, nil // its message was removed: it never arrived
	}
	if err != nil {
		return waiting, time.Time{}, err
	}
	var commit Commit
	if err := readRecord(transaction.place.commit(id), commitBound, &commit); err != nil && !errors.Is(err, os.ErrNotExist) {
		return waiting, time.Time{}, err
	}
	eventAt, _ := parseStamp(delivery.At)
	state := deliverystate.State(delivery.State)
	switch {
	case commit.RequestState == string(deliverystate.Closed) && deliverystate.Expirable(state):
		closed, _ := parseStamp(commit.Closed)
		return notArrived, closed, nil // a closure cut short before its delivery's write (section 4.4)
	case delivery.Reason == string(deliverystate.Unconfirmed):
		return notArrived, eventAt, nil
	case state == deliverystate.Delivered || state == deliverystate.Read || state == deliverystate.Acted:
		return arrived, eventAt, nil
	case deliverystate.Final(state):
		return notArrived, eventAt, nil
	}
	return waiting, time.Time{}, nil
}

// owe writes a notice on a session's record, once per ID.
func (transaction *transaction) owe(key string, notice SessionNotice) error {
	watcher, err := transaction.readSession(hashKey(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil // the watcher's record is gone: nobody is left to tell
	}
	if err != nil {
		return err // kept for a later try: a firing's entry stays until its notice is written
	}
	if slices.ContainsFunc(watcher.Notices, func(existing SessionNotice) bool { return existing.ID == notice.ID }) {
		return nil
	}
	notice.At, notice.State = stamp(transaction.now), string(deliverystate.NoticeOwed)
	watcher.Notices = transaction.addNotice(watcher.Notices, notice)
	return transaction.writeSession(watcher)
}
