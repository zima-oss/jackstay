package chatstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// ResultBound is the most a chat tool's result text may take (section 5.2).
const ResultBound = 48 << 10

// InboxInput is chat.inbox's input with its defaults applied (section 5.2).
type InboxInput struct {
	IDs        []string
	UnreadOnly bool
	Limit      int
	Cursor     string
	MarkRead   bool
}

// InboxResult is chat.inbox's result.
type InboxResult struct {
	Messages    []MessageView `json:"messages"`
	Notices     []string      `json:"notices,omitempty"`
	NoticesMore int           `json:"notices_more,omitempty"`
	More        int           `json:"more,omitempty"`
	NextCursor  string        `json:"next_cursor,omitempty"`
}

// cursor is a page's position: after (rank, id), or inside the body of id from offset. Filters is a
// digest of the call's filters, so a cursor used with others is refused.
type cursor struct {
	Rank    int    `json:"r"`
	ID      string `json:"i"`
	Offset  int    `json:"o,omitempty"`
	Filters string `json:"f"`
}

func encodeCursor(position cursor) string {
	return base64.RawURLEncoding.EncodeToString(marshalCompact(position))
}

func decodeCursor(text, filters string) (cursor, error) {
	var position cursor
	if text == "" {
		return position, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil || len(text) > 512 || json.Unmarshal(data, &position) != nil || position.Filters != filters || position.Offset < 0 {
		return cursor{}, refuse(ReasonInvalid, "the cursor does not belong to these filters; start again without it")
	}
	return position, nil
}

func filterDigest(parts ...any) string {
	digest := sha256.Sum256(marshalCompact(parts))
	return hex.EncodeToString(digest[:6])
}

func marshalCompact(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

// candidate is one delivery of the reading session.
type candidate struct {
	found    message
	delivery Delivery
}

func rankOf(priority string) int {
	if priority == PriorityNow {
		return 0
	}
	return 1
}

// Inbox reads the messages addressed to the session (section 5.2) under the visibility rule of
// section 4.10, and returns the notices it owes (section 8.5).
func (store *Store) Inbox(ctx context.Context, key string, input InboxInput) (InboxResult, error) {
	if input.Limit <= 0 || input.Limit > 50 {
		input.Limit = 20
	}
	if len(input.IDs) > 50 {
		return InboxResult{}, refuse(ReasonInvalid, "ids holds at most 50 references")
	}
	filters := filterDigest("inbox", input.IDs, input.UnreadOnly, input.MarkRead)
	position, err := decodeCursor(input.Cursor, filters)
	if err != nil {
		return InboxResult{}, err
	}
	var result InboxResult
	err = store.update(ctx, func(transaction *transaction) error {
		session, err := transaction.sessionOf(key)
		if err != nil {
			return err
		}
		hash := hashKey(session.Key)
		candidates, err := transaction.inboxCandidates(hash, input)
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
		var visible []candidate
		for _, item := range candidates {
			state := deliverystate.State(item.delivery.State)
			waiting := state == deliverystate.Pending || state == deliverystate.Sending || state == deliverystate.Delivered
			if (input.UnreadOnly && !waiting) || !deliverystate.Visible(item.delivery.table(item.found.envelope.Request != nil)) {
				continue
			}
			visible = append(visible, item)
		}
		sort.SliceStable(visible, func(left, right int) bool {
			leftRank, rightRank := rankOf(visible[left].found.envelope.Priority), rankOf(visible[right].found.envelope.Priority)
			if leftRank != rightRank {
				return leftRank < rightRank
			}
			return visible[left].found.envelope.ID < visible[right].found.envelope.ID
		})
		notices, noticesMore, err := transaction.collectNotices(session, sessions, 50)
		if err != nil {
			return err
		}
		// Sized with every collected notice; finish keeps only those it could mark shown.
		result.Notices, result.NoticesMore = noticeLines(notices), noticesMore
		page := pager{budget: ResultBound, base: marshalCompact(result)}
		start := 0
		if position.ID != "" {
			start = len(visible)
			for index, item := range visible {
				rank, id := rankOf(item.found.envelope.Priority), item.found.envelope.ID
				if rank > position.Rank || (rank == position.Rank && id > position.ID) ||
					(position.Offset > 0 && id == position.ID) {
					start = index
					break
				}
			}
		}
		// The page is built whole first; only then are its receipts written and its notices marked,
		// so a body that fails to load marks nothing, and a receipt that fails to be written leaves
		// its row returned but its delivery unread.
		type pageItem struct {
			item candidate
			row  int
		}
		var readable []pageItem
		finish := func() error {
			// Everything that can fail without changing anything comes first.
			current, err := transaction.readSession(hash)
			if err != nil {
				return err
			}
			for _, entry := range readable {
				updated, stepErr := transaction.stepLoaded(entry.item.found, hash, entry.item.delivery,
					deliverystate.Event{Kind: deliverystate.ReadWhole})
				if stepErr == nil {
					result.Messages[entry.row].State = updated.State
				}
			}
			shown, err := transaction.markShown(current, notices)
			if err != nil {
				return err
			}
			result.Notices = noticeLines(shown)
			result.NoticesMore += len(notices) - len(shown)
			return nil
		}
		for index := start; index < len(visible) && len(result.Messages) < input.Limit; index++ {
			item := visible[index]
			offset := 0
			if item.found.envelope.ID == position.ID {
				offset = position.Offset
			}
			body, err := transaction.bodyOf(item.found.envelope)
			if err != nil {
				return err
			}
			view := transaction.view(item.found, sessions, channels)
			piece, complete, fits := page.fit(&result, view, body, offset)
			if !fits {
				break
			}
			if complete && offset == 0 && input.MarkRead {
				readable = append(readable, pageItem{item: item, row: len(result.Messages)})
			}
			view.Message, view.Offset, view.Complete, view.State = piece, offset, complete, item.delivery.State
			result.Messages = append(result.Messages, view)
			if !complete {
				result.NextCursor = encodeCursor(cursor{Rank: rankOf(view.Priority), ID: view.ID, Offset: offset + len(piece), Filters: filters})
				result.More = len(visible) - index - 1
				return finish()
			}
		}
		returned := start + len(result.Messages)
		result.More = len(visible) - returned
		if result.More > 0 && len(result.Messages) > 0 {
			last := result.Messages[len(result.Messages)-1]
			result.NextCursor = encodeCursor(cursor{Rank: rankOf(last.Priority), ID: last.ID, Filters: filters})
		}
		return finish()
	})
	return result, err
}

// inboxCandidates is the session's deliveries a read considers: those it names, or those its
// inbox markers point at (every one of its deliveries when it asks for read ones too). Each is
// settled against the clock first, and a marker whose delivery is no longer unread is removed.
func (transaction *transaction) inboxCandidates(hash string, input InboxInput) ([]candidate, error) {
	var references []string
	switch {
	case len(input.IDs) > 0:
		seen := map[string]bool{}
		for _, id := range input.IDs {
			if !messageIDPattern.MatchString(id) {
				return nil, refuse(ReasonInvalid, "%q is not a message ID", id)
			}
			if !seen[id] {
				seen[id] = true
				references = append(references, id)
			}
		}
	case input.UnreadOnly:
		names, err := listNames(transaction.place.inbox(hash))
		if err != nil {
			return nil, err
		}
		references = names
	default:
		groups, err := listNames(filepath.Join(transaction.place.root, "deliveries"))
		if err != nil {
			return nil, err
		}
		for _, id := range groups {
			if exists(transaction.place.delivery(id, hash)) {
				references = append(references, id)
			}
		}
	}
	var candidates []candidate
	for _, id := range references {
		found, err := transaction.message(id)
		if errors.Is(err, errNoDelivery) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var delivery Delivery
		if err := readRecord(transaction.place.delivery(id, hash), deliveryBound, &delivery); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		// Every read repairs a closure cut short and applies the deadline before it looks (Peek).
		delivery, err = transaction.settleDeadline(&found, hash, delivery)
		if err != nil {
			return nil, err
		}
		if !deliverystate.Unread(deliverystate.State(delivery.State)) {
			if err := os.Remove(filepath.Join(transaction.place.inbox(hash), id)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		candidates = append(candidates, candidate{found: found, delivery: delivery})
	}
	return candidates, nil
}

func (transaction *transaction) bodyOf(envelope Envelope) ([]byte, error) {
	return durable.ReadLimited(transaction.place.body(envelope.ID), MaxBodyBytes)
}

// view is a committed message as a reader sees it, without its body.
func (transaction *transaction) view(found message, sessions []Session, channels []Channel) MessageView {
	envelope := found.envelope
	view := MessageView{
		ID: envelope.ID, ThreadID: envelope.ThreadID, ReplyTo: envelope.ReplyTo, Kind: envelope.Kind,
		Event: envelope.Event, From: refOf(envelope, sessions, channels), To: envelope.To, Summary: envelope.Summary,
		Priority: envelope.Priority, Created: envelope.Created, ExpiresAt: envelope.ExpiresAt,
		Response: envelope.Response, Attachments: envelope.Attachments, Meta: envelope.Meta,
	}
	if envelope.Request != nil {
		state := found.commit.RequestState
		view.Request = &RequestView{RequestID: envelope.Request.RequestID, Expects: envelope.Request.Expects,
			Options: envelope.Request.Options, State: state}
	}
	return view
}

// refOf names a message's sender as it is now, or as the envelope recorded it once it is gone.
func refOf(envelope Envelope, sessions []Session, channels []Channel) SessionRef {
	if envelope.From == SystemKey {
		return SessionRef{Address: SystemKey, Name: SystemKey, Ref: SystemKey, Engine: SystemKey}
	}
	ref := SessionRef{Name: envelope.FromName, Ref: hashKey(envelope.From)[:6], Engine: EngineOther}
	for _, session := range sessions {
		if session.Key == envelope.From {
			ref = SessionRef{Name: session.Name, Ref: session.Ref, Engine: session.Engine}
			ref.Address = sessionAddress(session, sessions)
		}
	}
	if ref.Address == "" {
		ref.Address = ref.Name + " [" + ref.Ref + "]"
	}
	for _, channel := range channels {
		if channel.ID == envelope.FromChannelID {
			ref.Repository = channelAddressOf(channel, channels)
		}
	}
	return ref
}

// pager fits messages into one result under the size bound (section 5.2).
type pager struct {
	budget int
	base   []byte
}

// fit returns the part of body from offset that fits the result with view added: the whole rest
// (complete) when it fits, otherwise as much as fits on a character boundary. A result that holds
// no message yet always takes at least one character, so following the cursor always ends.
func (page pager) fit(result *InboxResult, view MessageView, body []byte, offset int) (string, bool, bool) {
	if offset > len(body) {
		offset = len(body)
	}
	rest := body[offset:]
	size := func(piece []byte, complete bool) int {
		trial := *result
		candidate := view
		candidate.Message, candidate.Offset, candidate.Complete = string(piece), offset, complete
		trial.Messages = append(append([]MessageView(nil), result.Messages...), candidate)
		trial.NextCursor = strings.Repeat("x", 128)
		return len(marshalCompact(trial))
	}
	if size(rest, true) <= page.budget {
		return string(rest), true, true
	}
	low, high := 0, len(rest)
	for low < high {
		middle := (low + high + 1) / 2
		if size(rest[:middle], false) <= page.budget {
			low = middle
		} else {
			high = middle - 1
		}
	}
	for low > 0 && !utf8.Valid(rest[:low]) {
		low--
	}
	if low == 0 {
		if len(result.Messages) > 0 {
			return "", false, false
		}
		_, width := utf8.DecodeRune(rest)
		low = max(width, 1)
	}
	return string(rest[:low]), false, true
}
