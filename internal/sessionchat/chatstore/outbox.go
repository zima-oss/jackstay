package chatstore

import (
	"context"
	"slices"
	"sort"
)

// OutboxInput is chat.outbox's input.
type OutboxInput struct {
	MessageID     string
	States        []string
	RequestStates []string
	ThreadID      string
	Limit         int
	Cursor        string
}

// RecipientRow is one recipient of a sent message, with its receipt and its live state (section 5.2).
type RecipientRow struct {
	Session   SessionRef `json:"session"`
	State     string     `json:"state"`
	At        string     `json:"at"`
	Route     string     `json:"route"`
	Reason    string     `json:"reason,omitempty"`
	Note      string     `json:"note,omitempty"`
	LiveState string     `json:"live_state"`
}

// OutboxRequest is a sent request's state, with the answer's decision.
type OutboxRequest struct {
	RequestID string `json:"request_id"`
	State     string `json:"state"`
	Decision  string `json:"decision,omitempty"`
}

// OutboxMessage is one sent message in the outbox.
type OutboxMessage struct {
	ID         string         `json:"id"`
	ThreadID   string         `json:"thread_id"`
	To         string         `json:"to"`
	Summary    string         `json:"summary"`
	Kind       string         `json:"kind"`
	Created    string         `json:"created"`
	ExpiresAt  string         `json:"expires_at"`
	Request    *OutboxRequest `json:"request,omitempty"`
	Counts     map[string]int `json:"counts"`
	Recipients []RecipientRow `json:"recipients"`
}

// OutboxResult is chat.outbox's result.
type OutboxResult struct {
	Messages   []OutboxMessage `json:"messages,omitempty"`
	Recipients []RecipientRow  `json:"recipients,omitempty"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// Outbox lists what the session sent and what became of it (section 5.2). Rows are never split.
func (store *Store) Outbox(ctx context.Context, key string, input OutboxInput) (OutboxResult, error) {
	if input.Limit <= 0 || input.Limit > 100 {
		input.Limit = 20
	}
	filters := filterDigest("outbox", input.MessageID, input.States, input.RequestStates, input.ThreadID)
	position, err := decodeCursor(input.Cursor, filters)
	if err != nil {
		return OutboxResult{}, err
	}
	var result OutboxResult
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
		byHash := map[string]Session{}
		for _, other := range sessions {
			byHash[hashKey(other.Key)] = other
		}
		if input.MessageID != "" {
			found, err := transaction.message(input.MessageID)
			if err != nil || found.envelope.From != session.Key {
				return refuse(ReasonUnknownMessage, "no message %s sent by this session", input.MessageID)
			}
			all, err := transaction.recipientRows(&found, byHash, sessions, channels)
			if err != nil {
				return err
			}
			// The cursor holds a position in the recipient list, which does not change; the state
			// filter applies after it, so a receipt that moved between pages hides no other row.
			from := 0
			if position.ID != "" {
				from = position.Offset
			}
			rows := filterRows(all[min(from, len(all)):], input.States)
			start := 0
			budget := ResultBound - 256
			for index := start; index < len(rows) && len(result.Recipients) < input.Limit; index++ {
				trial := append(append([]RecipientRow(nil), result.Recipients...), rows[index])
				if len(marshalCompact(OutboxResult{Recipients: trial})) > budget && len(result.Recipients) > 0 {
					break
				}
				result.Recipients = trial
			}
			if returned := start + len(result.Recipients); returned < len(rows) {
				last := result.Recipients[len(result.Recipients)-1]
				position := from
				for index := from; index < len(all); index++ {
					if all[index].Session.Ref == last.Session.Ref && all[index].At == last.At {
						position = index + 1
						break
					}
				}
				result.NextCursor = encodeCursor(cursor{ID: "recipients", Offset: position, Filters: filters})
			}
			return nil
		}
		ids, err := listNames(transaction.place.outbox(hashKey(session.Key)))
		if err != nil {
			return err
		}
		sort.Sort(sort.Reverse(sort.StringSlice(ids)))
		budget := ResultBound - 256
		for _, id := range ids {
			if position.ID != "" && id >= position.ID {
				continue
			}
			found, err := transaction.message(id)
			if err != nil || (input.ThreadID != "" && found.envelope.ThreadID != input.ThreadID) {
				continue
			}
			rows, err := transaction.recipientRows(&found, byHash, sessions, channels)
			if err != nil {
				return err
			}
			if len(input.RequestStates) > 0 && (found.envelope.Request == nil || !slices.Contains(input.RequestStates, found.commit.RequestState)) {
				continue
			}
			counts := map[string]int{}
			for _, row := range rows {
				counts[row.State]++
			}
			if len(input.States) > 0 && len(filterRows(rows, input.States)) == 0 {
				continue
			}
			item := OutboxMessage{ID: id, ThreadID: found.envelope.ThreadID, To: found.envelope.To, Summary: found.envelope.Summary,
				Kind: found.envelope.Kind, Created: found.envelope.Created, ExpiresAt: found.envelope.ExpiresAt, Counts: counts,
				Recipients: rows[:min(len(rows), 8)]}
			if item.Recipients == nil {
				item.Recipients = []RecipientRow{}
			}
			if request := found.envelope.Request; request != nil {
				item.Request = &OutboxRequest{RequestID: request.RequestID, State: found.commit.RequestState}
				if found.commit.ResponseID != "" {
					if answer, err := transaction.message(found.commit.ResponseID); err == nil && answer.envelope.Response != nil {
						item.Request.Decision = answer.envelope.Response.Decision
					}
				}
			}
			if len(result.Messages) == input.Limit {
				result.NextCursor = encodeCursor(cursor{ID: result.Messages[len(result.Messages)-1].ID, Filters: filters})
				break
			}
			trial := append(append([]OutboxMessage(nil), result.Messages...), item)
			if len(marshalCompact(OutboxResult{Messages: trial})) > budget && len(result.Messages) > 0 {
				result.NextCursor = encodeCursor(cursor{ID: result.Messages[len(result.Messages)-1].ID, Filters: filters})
				break
			}
			result.Messages = trial
		}
		return nil
	})
	return result, err
}

// recipientRows are a message's recipients with their receipts, deadlines applied first.
func (transaction *transaction) recipientRows(found *message, byHash map[string]Session, sessions []Session, channels []Channel) ([]RecipientRow, error) {
	var rows []RecipientRow
	for _, hash := range found.envelope.Recipients {
		var delivery Delivery
		if err := readRecord(transaction.place.delivery(found.envelope.ID, hash), deliveryBound, &delivery); err != nil {
			continue
		}
		delivery, err := transaction.settleDeadline(found, hash, delivery)
		if err != nil {
			return nil, err
		}
		row := RecipientRow{State: delivery.State, At: delivery.At, Route: delivery.Route, Reason: delivery.Reason,
			Note: delivery.Note, LiveState: StateUnknown}
		if recipient, ok := byHash[hash]; ok {
			row.Session = SessionRef{Address: sessionAddress(recipient, sessions), Name: recipient.Name, Ref: recipient.Ref, Engine: recipient.Engine}
			for _, channel := range channels {
				if channel.ID == recipient.ChannelID {
					row.Session.Repository = channelAddressOf(channel, channels)
				}
			}
			row.LiveState = recipient.State
		} else {
			row.Session = SessionRef{Address: "[" + hash[:6] + "]", Name: "", Ref: hash[:6], Engine: EngineOther}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func filterRows(rows []RecipientRow, states []string) []RecipientRow {
	if len(states) == 0 {
		return rows
	}
	return slices.DeleteFunc(slices.Clone(rows), func(row RecipientRow) bool {
		return !slices.Contains(states, row.State)
	})
}
