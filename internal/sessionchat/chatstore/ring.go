package chatstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// The doorbell (W-095; docs/design/chat.md sections 8.3 and 8.4): a line that tells a session how
// many messages and notices wait, and never carries a message's text, sender, summary or ID. The
// socket and the hooks ring it; the session reads its inbox.

// Hooks that ring.
const (
	HookPrompt    = "prompt"     // UserPromptSubmit: every unread message and every owed notice
	HookToolBatch = "tool-batch" // PostToolBatch: now and next messages no hook has told of yet
	HookStop      = "stop"       // Stop: now messages no hook has told of yet, and the turn is held
)

// maxStopBlocks is how many times in a row a Stop hook holds a turn (section 8.4).
const maxStopBlocks = 3

// ringRetry is the wait after each confirmed failure to reach a socket (section 8.3).
var ringRetry = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}

// DoorbellLine is the doorbell's whole text.
func DoorbellLine(messages, urgent, notices int) string {
	var parts []string
	switch {
	case messages == 1:
		parts = append(parts, "1 new message")
	case messages > 1:
		parts = append(parts, fmt.Sprintf("%d new messages", messages))
	}
	if urgent > 0 && messages > 0 {
		parts[0] += fmt.Sprintf(" (%d urgent)", urgent)
	}
	switch {
	case notices == 1:
		parts = append(parts, "1 notice")
	case notices > 1:
		parts = append(parts, fmt.Sprintf("%d notices", notices))
	}
	return "jackstay chat: " + strings.Join(parts, " and ") + ". Read them with chat.inbox (or `jst chat inbox`)."
}

// Ring is what a hook returns to Claude Code: the doorbell line, and whether a Stop holds the turn.
type Ring struct {
	Text  string // additionalContext, or the reason of a Stop block; "" when nothing waits
	Block bool
}

// waitingDelivery is one unread delivery a doorbell may count.
type waitingDelivery struct {
	found    message
	hash     string
	delivery Delivery
}

// waiting lists the session's pending deliveries on the doorbell routes, each settled against the
// clock first, so an expired one is never counted.
func (transaction *transaction) waiting(session Session) ([]waitingDelivery, error) {
	hash := hashKey(session.Key)
	names, err := listNames(transaction.place.inbox(hash))
	if err != nil {
		return nil, err
	}
	var result []waitingDelivery
	for _, id := range names {
		found, err := transaction.message(id)
		if errors.Is(err, errNoDelivery) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var delivery Delivery
		if err := readRecord(transaction.place.delivery(id, hash), deliveryBound, &delivery); err != nil {
			continue
		}
		if delivery, err = transaction.settleDeadline(&found, hash, delivery); err != nil {
			return nil, err
		}
		if deliverystate.Rings(delivery.table(found.envelope.Request != nil)) {
			result = append(result, waitingDelivery{found: found, hash: hash, delivery: delivery})
		}
	}
	return result, nil
}

// HookRing rings the doorbell from one of a session's hooks (section 8.4). At a prompt it counts
// every unread message and every owed notice, since a socket's silent ring proves nothing; at a tool
// batch only the now and next messages no hook has told of yet; at Stop it holds the turn for now
// messages no hook has told of yet, at most three times in a row, and a Stop that does not hold makes
// the session idle.
func (store *Store) HookRing(ctx context.Context, key, hook string, stopHookActive bool) (Ring, error) {
	var result Ring
	err := store.update(ctx, func(transaction *transaction) error {
		session, err := transaction.sessionOf(key)
		if err != nil {
			return err
		}
		waiting, err := transaction.waiting(session)
		if err != nil {
			return err
		}
		var told []waitingDelivery
		urgent := 0
		for _, item := range waiting {
			priority := item.found.envelope.Priority
			switch hook {
			case HookPrompt:
			case HookToolBatch:
				if priority == PriorityLater || item.delivery.HookRung {
					continue
				}
			case HookStop:
				if priority != PriorityNow || item.delivery.HookRung {
					continue
				}
			default:
				return refuse(ReasonInvalid, "unknown hook %q", hook)
			}
			told = append(told, item)
			if priority == PriorityNow {
				urgent++
			}
		}
		notices := 0
		if hook == HookPrompt {
			sessions, err := transaction.listSessions()
			if err != nil {
				return err
			}
			owed, more, err := transaction.collectNotices(session, sessions, 50)
			if err != nil {
				return err
			}
			notices = len(owed) + more
		}
		if hook == HookStop && (len(told) == 0 || (stopHookActive && session.StopBlocks >= maxStopBlocks)) {
			_, err := transaction.observe(session, ObserveIdle, false)
			return err
		}
		if len(told) == 0 && notices == 0 {
			return nil
		}
		for _, item := range told {
			if !item.delivery.HookRung {
				item.delivery.HookRung = true
				if err := transaction.writeDelivery(item.found.envelope.ID, item.hash, item.delivery); err != nil {
					return err
				}
			}
		}
		result.Text = DoorbellLine(len(told), urgent, notices)
		if hook != HookStop {
			return nil
		}
		result.Block = true
		current, err := transaction.readSession(hashKey(session.Key))
		if err != nil {
			return err
		}
		current.StopBlocks++
		if current.State != StateBusy {
			current.State, current.Since = StateBusy, stamp(transaction.now)
		}
		current.LastSeen = stamp(transaction.now)
		return transaction.writeSession(current)
	})
	return result, err
}

// DueRing is a native doorbell a pass may ring now: the session, the line, and what the ring tells
// of (the deliveries to mark, the newest notice).
type DueRing struct {
	Recipient  string
	Text       string
	Deliveries []string
	NoticesAt  string
}

// RingOutcome is what became of a native ring.
type RingOutcome string

// The outcomes of a ring (section 8.3).
const (
	// RingTaken: the line was written and the connection stayed open, or was answered.
	RingTaken RingOutcome = "taken"
	// RingClosed: the session closed the connection before the 2 s: its engine refused the doorbell.
	RingClosed RingOutcome = "closed"
	// RingFailed: nothing reached the socket (none, or a refused connection): a confirmed failure.
	RingFailed RingOutcome = "failed"
)

// DueRings lists the native doorbells due now (section 8.3): an idle session on the native route,
// past its ring_next, with a pending native delivery or an owed notice no ring has told it of yet.
func (store *Store) DueRings(ctx context.Context) ([]DueRing, error) {
	var due []DueRing
	err := store.update(ctx, func(transaction *transaction) error {
		sessions, err := transaction.listSessions()
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if session.State != StateIdle || session.Delivery != DeliveryNative {
				continue
			}
			if next, ok := parseStamp(session.RingNext); ok && transaction.now.Before(next) {
				continue
			}
			waiting, err := transaction.waiting(session)
			if err != nil {
				return err
			}
			ring := DueRing{Recipient: session.Key}
			messages, urgent := 0, 0
			for _, item := range waiting {
				if item.delivery.Route != string(deliverystate.Native) {
					continue
				}
				messages++
				if item.found.envelope.Priority == PriorityNow {
					urgent++
				}
				if !item.delivery.Rung {
					ring.Deliveries = append(ring.Deliveries, item.found.envelope.ID)
				}
			}
			owed, more, err := transaction.collectNotices(session, sessions, 50)
			if err != nil {
				return err
			}
			newNotices := false
			for _, notice := range owed {
				if notice.At > session.NoticesRungAt {
					newNotices = true
				}
				if notice.At > ring.NoticesAt {
					ring.NoticesAt = notice.At
				}
			}
			if len(ring.Deliveries) == 0 && !newNotices {
				continue
			}
			ring.Text = DoorbellLine(messages, urgent, len(owed)+more)
			due = append(due, ring)
		}
		return nil
	})
	return due, err
}

// RecordRing records what became of a native ring (section 8.3). A ring that reached the socket told
// of everything in it, whatever came back: its deliveries are marked rung and notices_rung_at moves
// on; a closed connection also refuses those still pending. A confirmed failure reached nothing, so
// nothing is marked, and the next ring waits 5 s, 30 s, then 2 min.
func (store *Store) RecordRing(ctx context.Context, ring DueRing, outcome RingOutcome) error {
	return store.update(ctx, func(transaction *transaction) error {
		hash := hashKey(ring.Recipient)
		session, err := transaction.readSession(hash)
		if err != nil {
			return nil // the session is gone: nothing to record
		}
		if outcome == RingFailed {
			session.RingNext = stamp(transaction.now.Add(ringRetry[min(session.RingFailures, len(ringRetry)-1)]))
			session.RingFailures++
			return transaction.writeSession(session)
		}
		for _, id := range ring.Deliveries {
			found, err := transaction.message(id)
			if err != nil {
				continue
			}
			var delivery Delivery
			if err := readRecord(transaction.place.delivery(id, hash), deliveryBound, &delivery); err != nil {
				continue
			}
			if outcome == RingClosed {
				refused, stepErr := transaction.stepLoaded(found, hash, delivery, deliverystate.Event{Kind: deliverystate.RingRefused})
				if stepErr == nil {
					delivery = refused
				}
			}
			if !delivery.Rung {
				delivery.Rung = true
				if err := transaction.writeDelivery(id, hash, delivery); err != nil {
					return err
				}
			}
		}
		session, err = transaction.readSession(hash)
		if err != nil {
			return err
		}
		if ring.NoticesAt > session.NoticesRungAt {
			session.NoticesRungAt = ring.NoticesAt
		}
		session.RingFailures, session.RingNext = 0, ""
		return transaction.writeSession(session)
	})
}
