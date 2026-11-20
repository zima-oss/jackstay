package chatstore

import (
	"strings"

	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// Notices to the sender (section 8.5): owed on a delivery (refused, dropped, expired), on a request's
// commit record (expired), or on the watcher's own session record (idle notices). Each is shown at
// most once; the step that returns it marks it shown.

// noticeSource tells where an owed notice lives.
type noticeSource int

const (
	onDelivery noticeSource = iota
	onRequest
	onSession
)

// OwedNotice is one owed notice and the line that tells it.
type OwedNotice struct {
	Line      string
	source    noticeSource
	messageID string
	hash      string // the recipient's hash, for a delivery's notice
	noticeID  string // the session notice's ID
}

// collectNotices lists what the session is owed, oldest message first, at most limit of them, and
// how many more there are.
func (transaction *transaction) collectNotices(session Session, sessions []Session, limit int) ([]OwedNotice, int, error) {
	var owed []OwedNotice
	byHash := map[string]Session{}
	for _, other := range sessions {
		byHash[hashKey(other.Key)] = other
	}
	names, err := listNames(transaction.place.outbox(hashKey(session.Key)))
	if err != nil {
		return nil, 0, err
	}
	for _, id := range names {
		found, err := transaction.message(id)
		if err != nil {
			continue
		}
		// The clock first: a deadline passed since the last step owes its notice now.
		if err := transaction.settleMessage(&found); err != nil {
			return nil, 0, err
		}
		if found.commit.Notice == string(deliverystate.NoticeOwed) && found.envelope.Request != nil {
			owed = append(owed, OwedNotice{source: onRequest, messageID: id,
				Line: id + " (" + found.envelope.Request.RequestID + ") to " + found.envelope.To + ": expired unanswered"})
		}
		for _, hash := range found.envelope.Recipients {
			var delivery Delivery
			if err := readRecord(transaction.place.delivery(id, hash), deliveryBound, &delivery); err != nil {
				continue
			}
			if delivery.Notice != string(deliverystate.NoticeOwed) {
				continue
			}
			recipientAddress := found.envelope.To
			if recipient, ok := byHash[hash]; ok {
				recipientAddress = fullAddress(recipient)
			}
			owed = append(owed, OwedNotice{source: onDelivery, messageID: id, hash: hash,
				Line: id + " to " + recipientAddress + ": " + receiptWords(delivery)})
		}
	}
	for _, notice := range session.Notices {
		if notice.State != string(deliverystate.NoticeOwed) {
			continue
		}
		owed = append(owed, OwedNotice{source: onSession, noticeID: notice.ID, Line: sessionNoticeWords(notice)})
	}
	if len(owed) <= limit {
		return owed, 0, nil
	}
	return owed[:limit], len(owed) - limit, nil
}

// receiptWords is what happened to a delivery, in a few words.
func receiptWords(delivery Delivery) string {
	switch deliverystate.Reason(delivery.Reason) {
	case deliverystate.EngineRefused:
		return "refused: the recipient's engine refused its doorbell"
	case deliverystate.RecipientEnded:
		return "dropped: the recipient ended before reading it"
	case deliverystate.ExpiredReason:
		return "expired unread"
	}
	return delivery.State
}

func sessionNoticeWords(notice SessionNotice) string {
	switch notice.Kind {
	case NoticeIdle:
		return notice.About + " is idle (from its Stop hook, or its engine's own report)"
	case NoticeEnded:
		return notice.About + " ended"
	case NoticeNotDelivered:
		return notice.MessageID + " to " + notice.About + ": not confirmed delivered, so no idle notice will follow"
	case NoticeSubscriptionExpired:
		return "watching " + notice.About + ": the subscription expired without an idle"
	}
	return strings.TrimSpace(notice.Kind + " " + notice.About)
}

// markShown marks the given notices shown, in the step that returns them, and returns those it
// marked: a notice whose record could not be read or written again is left owed and not returned.
func (transaction *transaction) markShown(session Session, notices []OwedNotice) ([]OwedNotice, error) {
	var shown []OwedNotice
	current, err := transaction.readSession(hashKey(session.Key))
	if err != nil {
		return nil, err
	}
	sessionChanged := false
	for _, notice := range notices {
		switch notice.source {
		case onDelivery:
			var delivery Delivery
			if err := readRecord(transaction.place.delivery(notice.messageID, notice.hash), deliveryBound, &delivery); err != nil {
				continue
			}
			next, err := deliverystate.ShowNotice(deliverystate.Notice(delivery.Notice))
			if err != nil {
				continue
			}
			delivery.Notice = string(next)
			if err := transaction.writeDelivery(notice.messageID, notice.hash, delivery); err != nil {
				continue
			}
		case onRequest:
			var commit Commit
			if err := readRecord(transaction.place.commit(notice.messageID), commitBound, &commit); err != nil {
				continue
			}
			next, err := deliverystate.ShowNotice(deliverystate.Notice(commit.Notice))
			if err != nil {
				continue
			}
			commit.Notice = string(next)
			if err := writeRecord(transaction.place.commit(notice.messageID), commit, commitBound); err != nil {
				continue
			}
		case onSession:
			found := false
			for index := range current.Notices {
				if current.Notices[index].ID == notice.noticeID && current.Notices[index].State == string(deliverystate.NoticeOwed) {
					current.Notices[index].State, found, sessionChanged = string(deliverystate.NoticeShown), true, true
				}
			}
			if !found {
				continue
			}
		}
		shown = append(shown, notice)
	}
	if sessionChanged {
		if err := transaction.writeSession(current); err != nil {
			// The session's own notices stay owed; the others were marked and are returned.
			kept := shown[:0]
			for _, notice := range shown {
				if notice.source != onSession {
					kept = append(kept, notice)
				}
			}
			return kept, nil
		}
	}
	return shown, nil
}

// noticeLines is the lines of a list of notices.
func noticeLines(notices []OwedNotice) []string {
	lines := make([]string, 0, len(notices))
	for _, notice := range notices {
		lines = append(lines, notice.Line)
	}
	return lines
}
