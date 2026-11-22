package chatstore

import (
	"context"
	"errors"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// ThreadInput is chat.thread's input: exactly one of ThreadID and Channel.
type ThreadInput struct {
	ThreadID string
	Channel  string
	Limit    int
	Cursor   string
}

// ThreadResult is chat.thread's result.
type ThreadResult struct {
	Messages   []MessageView `json:"messages"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// Thread reads a thread, or a channel's history, as the session may see it (sections 4.3 and 4.10):
// what it sent, what was addressed to it as far as its own delivery makes the body visible, and
// every channel message it has no delivery of. Reading a thread changes no receipt.
func (store *Store) Thread(ctx context.Context, key string, input ThreadInput) (ThreadResult, error) {
	if (input.ThreadID == "") == (input.Channel == "") {
		return ThreadResult{}, refuse(ReasonInvalid, "give exactly one of thread_id and channel")
	}
	if input.ThreadID != "" && !messageIDPattern.MatchString(input.ThreadID) {
		return ThreadResult{}, refuse(ReasonInvalid, "thread_id is a message ID")
	}
	if input.Limit <= 0 || input.Limit > 100 {
		input.Limit = 50
	}
	filters := filterDigest("thread", input.ThreadID, input.Channel)
	position, err := decodeCursor(input.Cursor, filters)
	if err != nil {
		return ThreadResult{}, err
	}
	var result ThreadResult
	err = store.update(ctx, func(transaction *transaction) error {
		session, err := transaction.sessionOf(key)
		if err != nil {
			return err
		}
		sessions, err := transaction.listSessions()
		if err != nil {
			return err
		}
		channels, err := transaction.listChannels()
		if err != nil {
			return err
		}
		channelID := ""
		if input.Channel != "" {
			parsed, err := parseAddress(input.Channel)
			if err != nil || !parsed.channel {
				return refuse(ReasonInvalid, "%q is not a channel address", input.Channel)
			}
			channel, err := resolveChannel(parsed, channels)
			if err != nil {
				return err
			}
			channelID = channel.ID
		}
		ids, err := transaction.threadMessages(input.ThreadID, channelID)
		if err != nil {
			return err
		}
		if input.ThreadID != "" && len(ids) == 0 {
			return refuse(ReasonUnknownMessage, "no thread %s", input.ThreadID)
		}
		hash := hashKey(session.Key)
		page := pager{budget: ResultBound}
		inbox := InboxResult{}
		for _, id := range ids {
			// Resume after the cursor's message by the order of the listing, so a message removed
			// since never hides the rest; inside its body only while it still exists.
			if position.ID != "" {
				after := id > position.ID
				if channelID != "" {
					after = id < position.ID
				}
				insideBody := id == position.ID && position.Offset > 0
				if !after && !insideBody {
					continue
				}
			}
			if len(inbox.Messages) == input.Limit {
				last := inbox.Messages[len(inbox.Messages)-1]
				result.NextCursor = encodeCursor(cursor{ID: last.ID, Filters: filters})
				break
			}
			found, err := transaction.message(id)
			if err != nil {
				continue
			}
			if err := transaction.settleMessage(&found); err != nil {
				return err
			}
			view, visible, err := transaction.threadView(found, session, hash, sessions, channels)
			if err != nil {
				return err
			}
			if !visible {
				if view.ID != "" {
					trial := inbox
					trial.Messages = append(append([]MessageView(nil), inbox.Messages...), view)
					trial.NextCursor = strings.Repeat("x", 128)
					if len(marshalCompact(trial)) > ResultBound && len(inbox.Messages) > 0 {
						last := inbox.Messages[len(inbox.Messages)-1]
						result.NextCursor = encodeCursor(cursor{ID: last.ID, Filters: filters})
						break
					}
					inbox.Messages = append(inbox.Messages, view)
				}
				continue
			}
			body, err := transaction.bodyOf(found.envelope)
			if err != nil {
				return err
			}
			offset := 0
			if id == position.ID {
				offset = position.Offset
			}
			piece, complete, fits := page.fit(&inbox, view, body, offset)
			if !fits {
				last := inbox.Messages[len(inbox.Messages)-1]
				result.NextCursor = encodeCursor(cursor{ID: last.ID, Filters: filters})
				break
			}
			view.Message, view.Offset, view.Complete = piece, offset, complete
			inbox.Messages = append(inbox.Messages, view)
			if !complete {
				result.NextCursor = encodeCursor(cursor{ID: id, Offset: offset + len(piece), Filters: filters})
				break
			}
		}
		result.Messages = inbox.Messages
		return nil
	})
	return result, err
}

// threadMessages lists a thread's committed messages oldest first, or a channel's newest first.
func (transaction *transaction) threadMessages(threadID, channelID string) ([]string, error) {
	names, err := listNames(transaction.place.messages())
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, name := range names {
		id := name[:len(name)-len(".json")]
		if !messageIDPattern.MatchString(id) || !exists(transaction.place.commit(id)) {
			continue
		}
		var envelope Envelope
		if err := readRecord(transaction.place.envelope(id), envelopeBound, &envelope); err != nil {
			continue
		}
		if (threadID != "" && envelope.ThreadID == threadID) || (channelID != "" && envelope.ToChannelID == channelID) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if channelID != "" {
		slices.Reverse(ids)
	}
	return ids, nil
}

// threadView is a message of a thread as the session may see it: visible, a bare placeholder when
// it has a delivery whose body it may not see yet, or nothing at all when it may not read it.
func (transaction *transaction) threadView(found message, session Session, hash string, sessions []Session, channels []Channel) (MessageView, bool, error) {
	envelope := found.envelope
	view := transaction.view(found, sessions, channels)
	if slices.Contains(envelope.Recipients, hash) {
		var delivery Delivery
		if err := readRecord(transaction.place.delivery(envelope.ID, hash), deliveryBound, &delivery); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return MessageView{}, false, nil
			}
			return MessageView{}, false, err
		}
		if !deliverystate.Visible(delivery.table(envelope.Request != nil)) {
			return MessageView{ID: envelope.ID, ThreadID: envelope.ThreadID, Hidden: true}, false, nil
		}
		return view, true, nil
	}
	if envelope.From == session.Key || envelope.ToChannelID != "" {
		return view, true, nil
	}
	return MessageView{}, false, nil
}
