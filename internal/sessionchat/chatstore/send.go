package chatstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// Limits of section 4.8 and the account's message bound of section 6.4.
const (
	sendsPerMinute       = 30
	sendWindow           = 60 * time.Second
	queuePerRecipient    = 50
	exchangeLimit        = 40
	maxLiveMessages      = 4096
	idleSubscriptionLife = 12 * time.Hour
	maxInlineRecipients  = 20
	maxRecipients        = 256
)

// Outcomes of a send (section 4.2); a refusal is returned as a *Refusal.
const (
	OutcomeAccepted   = "accepted"
	OutcomeDuplicate  = "duplicate"
	OutcomeSubscribed = "subscribed"
	OutcomeUncertain  = "uncertain"
)

// SendInput is chat.send's input after its schema (section 5.2).
type SendInput struct {
	To               string
	Message          string
	Summary          string
	NotifyWhenIdle   bool
	ThreadID         string
	ReplyTo          string
	Request          *RequestInput
	Priority         string
	ExpiresInSeconds int
	Attachments      []Attachment
	Meta             map[string]string
	IdempotencyKey   string
}

// RecipientState is one recipient in a send's result.
type RecipientState struct {
	Session string `json:"session"`
	State   string `json:"state"`
}

// SendResult is a send's or a response's result (section 5.2).
type SendResult struct {
	Outcome        string           `json:"outcome"`
	MessageID      string           `json:"message_id,omitempty"`
	ThreadID       string           `json:"thread_id,omitempty"`
	RequestID      string           `json:"request_id,omitempty"`
	Recipients     []RecipientState `json:"recipients,omitempty"`
	RecipientsMore int              `json:"recipients_more,omitempty"`
	Watching       string           `json:"watching,omitempty"`
	Until          string           `json:"until,omitempty"`
	Fired          bool             `json:"fired,omitempty"`
	FiredAt        string           `json:"fired_at,omitempty"`
}

// Send stores one message, request, reply or idle subscription from the session of senderKey.
func (store *Store) Send(ctx context.Context, senderKey string, scope Scope, input SendInput) (SendResult, error) {
	priority, expiry, err := checkSendInput(&input)
	if err != nil {
		return SendResult{}, err
	}
	var result SendResult
	err = store.update(ctx, func(transaction *transaction) error {
		sender, err := transaction.sessionOf(senderKey)
		if err != nil {
			return err
		}
		if duplicate, found, err := transaction.duplicateOf(senderKey, input.IdempotencyKey); found || err != nil {
			result = duplicate
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
		destination, err := resolveTarget(input.To, sender, sessions, channels)
		if err != nil {
			return err
		}
		if destination.channel != nil && (input.Request != nil || input.NotifyWhenIdle) {
			return refuse(ReasonRequestToChannel, "requests and notify_when_idle go to one session, never to a channel")
		}
		replied, threadID, err := transaction.threadOf(sender, input.ThreadID, input.ReplyTo)
		if err != nil {
			return err
		}
		if err := checkScope(scope, sender, destination, replied, channels); err != nil {
			return err
		}
		if input.NotifyWhenIdle {
			if err := checkWatchable(*destination.session); err != nil {
				return err
			}
			if input.Message == "" {
				result, err = transaction.subscribeIdle(sender, *destination.session, sessions)
				return err
			}
		}
		recipients, err := transaction.recipientsOf(sender, destination, sessions)
		if err != nil {
			return err
		}
		plan := planned{
			sender: sender, recipients: recipients, kind: "message", body: input.Message,
			summary: summaryOf(input.Summary, input.Message), priority: priority, expiry: expiry,
			threadID: threadID, replyTo: input.ReplyTo, attachments: input.Attachments, meta: input.Meta,
			idempotencyKey: input.IdempotencyKey, sessions: sessions, channels: channels,
		}
		if destination.channel != nil {
			plan.to, plan.toChannel = channelAddressOf(*destination.channel, channels), destination.channel.ID
		} else {
			plan.to = fullAddress(*destination.session)
		}
		if input.Request != nil {
			plan.kind = "request"
			plan.request = &Request{Expects: input.Request.Expects, Options: input.Request.Options}
		}
		plan.watch = input.NotifyWhenIdle
		if plan.watch && !slices.ContainsFunc(destination.session.IdleWatchers, func(watcher IdleWatcher) bool { return watcher.Key == sender.Key }) &&
			len(destination.session.IdleWatchers) >= maxWatchers {
			return refuse(ReasonCapacity, "%s already has %d idle watchers", fullAddress(*destination.session), maxWatchers)
		}
		if err := transaction.checkLimits(plan, destination.session != nil); err != nil {
			return err
		}
		result, err = transaction.commitMessage(plan)
		return err
	})
	return result, err
}

func checkSendInput(input *SendInput) (string, int, error) {
	if err := checkBody(input.Message); err != nil {
		return "", 0, err
	}
	if input.Message == "" && !input.NotifyWhenIdle {
		return "", 0, refuse(ReasonInvalid, "message is empty; only a notify_when_idle subscription goes without one")
	}
	if input.Message == "" && input.Request != nil {
		return "", 0, refuse(ReasonInvalid, "a request needs a message")
	}
	if err := checkSummary(input.Summary); err != nil {
		return "", 0, err
	}
	if err := checkRequest(input.Request); err != nil {
		return "", 0, err
	}
	if err := checkAttachments(input.Attachments); err != nil {
		return "", 0, err
	}
	if err := checkMeta(input.Meta); err != nil {
		return "", 0, err
	}
	if err := checkIdempotencyKey(input.IdempotencyKey); err != nil {
		return "", 0, err
	}
	for _, reference := range []string{input.ThreadID, input.ReplyTo} {
		if reference != "" && !messageIDPattern.MatchString(reference) {
			return "", 0, refuse(ReasonInvalid, "thread_id and reply_to are message IDs (msg_…)")
		}
	}
	priority, err := checkPriority(input.Priority)
	if err != nil {
		return "", 0, err
	}
	expiry, err := checkExpiry(input.ExpiresInSeconds)
	return priority, expiry, err
}

// checkWatchable is section 4.9's rule for the target of notify_when_idle.
func checkWatchable(recipient Session) error {
	if recipient.State == StateUnknown {
		return refuse(ReasonIdleUnobservable, "%s reports no busy or idle state, so its idle cannot be watched", fullAddress(recipient))
	}
	return nil
}

// threadOf checks reply_to and thread_id (section 4.3) and returns the envelope replied to, if any,
// and the thread of the new message ("" starts a new one).
func (transaction *transaction) threadOf(sender Session, threadID, replyTo string) (*Envelope, string, error) {
	if replyTo == "" && threadID == "" {
		return nil, "", nil
	}
	reference := replyTo
	if reference == "" {
		reference = threadID
	}
	envelope, err := transaction.readableEnvelope(sender, reference)
	if err != nil {
		return nil, "", err
	}
	if replyTo == "" {
		return nil, envelope.ThreadID, nil
	}
	if threadID != "" && threadID != envelope.ThreadID {
		return nil, "", refuse(ReasonInvalid, "thread_id disagrees with the thread of reply_to (%s)", envelope.ThreadID)
	}
	return &envelope, envelope.ThreadID, nil
}

// readableEnvelope is a committed message the session may read in a thread (section 4.3): one it
// sent, one addressed to it, or one sent to a channel.
func (transaction *transaction) readableEnvelope(session Session, id string) (Envelope, error) {
	var envelope Envelope
	if !exists(transaction.place.commit(id)) {
		return envelope, refuse(ReasonUnknownMessage, "no message %s; messages from 0.5.x were not carried over", id)
	}
	if err := readRecord(transaction.place.envelope(id), envelopeBound, &envelope); err != nil {
		return envelope, err
	}
	hash := hashKey(session.Key)
	if envelope.From == session.Key || envelope.ToChannelID != "" || slices.Contains(envelope.Recipients, hash) {
		return envelope, nil
	}
	return envelope, refuse(ReasonUnknownMessage, "no message %s that this session may read", id)
}

// checkScope applies the related list (section 2): a restricted sender reaches its own repository,
// the listed ones and #common only when listed; a reply to a message it received may go back to
// that message's sender.
func checkScope(scope Scope, sender Session, destination target, replied *Envelope, channels []Channel) error {
	if !scope.Restricted {
		return nil
	}
	allowed := allowedChannels(scope, sender.ChannelID, channels)
	if destination.channel != nil {
		if allowed[destination.channel.ID] {
			return nil
		}
		return refuse(ReasonOutOfScope, "#%s is not in this repository's chat.related", destination.channel.Name)
	}
	recipient := *destination.session
	if allowed[recipient.ChannelID] {
		return nil
	}
	if replied != nil && replied.From == recipient.Key && slices.Contains(replied.Recipients, hashKey(sender.Key)) {
		return nil
	}
	return refuse(ReasonOutOfScope, "%s works in a repository outside this repository's chat.related", fullAddress(recipient))
}

// recipientsOf is the sessions given a delivery: the one addressed, or the channel's live
// subscribers but the sender, as they are at the send (section 4.5).
func (transaction *transaction) recipientsOf(sender Session, destination target, sessions []Session) ([]Session, error) {
	if destination.session != nil {
		return []Session{*destination.session}, nil
	}
	var recipients []Session
	for _, session := range sessions {
		if session.Key == sender.Key || !addressable(session) || !slices.Contains(session.Subscriptions, destination.channel.ID) {
			continue
		}
		full, err := transaction.queueFull(hashKey(session.Key))
		if err != nil {
			return nil, err
		}
		if full {
			return nil, refuse(ReasonRecipientFull, "%s, subscribed to the channel, has %d messages waiting; send when it has read them",
				fullAddress(session), queuePerRecipient)
		}
		if len(recipients) == maxRecipients {
			return nil, refuse(ReasonCapacity, "the channel has more than %d subscribers to deliver to", maxRecipients)
		}
		recipients = append(recipients, session)
	}
	return recipients, nil
}

// planned is a message about to be committed.
type planned struct {
	sender         Session
	recipients     []Session
	gone           map[string]bool // recipient hashes whose delivery starts dropped (a response to a requester gone)
	kind           string
	body           string
	summary        string
	priority       string
	expiry         int
	threadID       string
	replyTo        string
	to             string
	toChannel      string
	request        *Request
	response       *Response
	attachments    []Attachment
	meta           map[string]string
	idempotencyKey string
	watch          bool
	sessions       []Session
	channels       []Channel
	// answers is the request a response answers, and responder the hash of its recipient.
	answers   string
	responder string
}

// checkLimits applies section 4.8 to a sender that is not jackstay: the rate, the repeat, and for
// one recipient its queue and the loop count.
func (transaction *transaction) checkLimits(plan planned, direct bool) error {
	if plan.sender.Key == SystemKey {
		return nil
	}
	recent, err := transaction.recentSends(plan.sender.Key)
	if err != nil {
		return err
	}
	if len(recent) >= sendsPerMinute {
		return refuse(ReasonRateLimited, "at most %d sends in 60 seconds; wait a minute", sendsPerMinute)
	}
	bodyHash := sha256.Sum256([]byte(plan.body))
	bodyHex := hex.EncodeToString(bodyHash[:])
	hashes := recipientHashes(plan.recipients)
	for _, earlier := range recent {
		sameTarget := (plan.toChannel != "" && earlier.ToChannelID == plan.toChannel) ||
			(plan.toChannel == "" && earlier.ToChannelID == "" && slices.Equal(earlier.Recipients, hashes))
		sameAnswer := plan.response == nil || (earlier.Response != nil && earlier.Response.RequestID == plan.response.RequestID)
		if sameTarget && sameAnswer && earlier.BodySHA256 == bodyHex && earlier.Kind == plan.kind {
			return refuse(ReasonRepeated, "the same message went to the same recipient within 60 seconds")
		}
	}
	if !direct || len(plan.recipients) != 1 {
		return nil
	}
	recipient := plan.recipients[0]
	if !plan.gone[hashKey(recipient.Key)] {
		full, err := transaction.queueFull(hashKey(recipient.Key))
		if err != nil {
			return err
		}
		if full {
			return refuse(ReasonRecipientFull, "%s has %d messages waiting; wait for it to read them", fullAddress(recipient), queuePerRecipient)
		}
	}
	if exchangeCount(plan.sender, recipient) >= exchangeLimit {
		return refuse(ReasonLoop, "you and %s exchanged %d messages with no prompt from a person; ask your person before going on", fullAddress(recipient), exchangeLimit)
	}
	return nil
}

// exchangeCount is the pair's count: a person's prompt on either side resets that side (section 4.8).
func exchangeCount(one, other Session) int {
	return min(one.Exchange[hashKey(other.Key)], other.Exchange[hashKey(one.Key)])
}

// recentSends is the committed envelopes the sender sent in the last minute, found from its outbox
// markers, whose names carry their time.
func (transaction *transaction) recentSends(senderKey string) ([]Envelope, error) {
	names, err := listNames(transaction.place.outbox(hashKey(senderKey)))
	if err != nil {
		return nil, err
	}
	since := transaction.now.Add(-sendWindow)
	var recent []Envelope
	for _, name := range names {
		created, ok := messageTime(name)
		if !ok || created.Before(since) || !exists(transaction.place.commit(name)) {
			continue
		}
		var envelope Envelope
		if err := readRecord(transaction.place.envelope(name), envelopeBound, &envelope); err != nil {
			continue
		}
		recent = append(recent, envelope)
	}
	return recent, nil
}

// queueFull reports whether a recipient has 50 deliveries pending, sending or held (section 4.8).
func (transaction *transaction) queueFull(hash string) (bool, error) {
	names, err := listNames(transaction.place.inbox(hash))
	if err != nil {
		return false, err
	}
	if len(names) < queuePerRecipient {
		return false, nil
	}
	waiting := 0
	for _, name := range names {
		if !exists(transaction.place.commit(name)) {
			continue
		}
		var delivery Delivery
		if err := readRecord(transaction.place.delivery(name, hash), deliveryBound, &delivery); err != nil {
			continue
		}
		if deliverystate.CountsAgainstQueue(deliverystate.State(delivery.State)) {
			waiting++
		}
	}
	return waiting >= queuePerRecipient, nil
}

func recipientHashes(recipients []Session) []string {
	hashes := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		hashes = append(hashes, hashKey(recipient.Key))
	}
	return hashes
}

// routeOf is the route a new delivery to the session takes (section 8.1).
func routeOf(session Session) deliverystate.Route {
	if session.Mailbox {
		return deliverystate.Mailbox
	}
	switch session.Delivery {
	case DeliveryNative:
		return deliverystate.Native
	case DeliveryHooks:
		return deliverystate.Hooks
	case DeliveryBridge:
		return deliverystate.Bridge
	}
	return deliverystate.Poll
}

// commitMessage admits a planned message against the store's bounds and writes it as section 7.3
// orders: journal, body, envelope, deliveries, commit record, what follows from it, derived files.
func (transaction *transaction) commitMessage(plan planned) (SendResult, error) {
	id, err := newMessageID(transaction.now, transaction.store.random)
	if err != nil {
		return SendResult{}, err
	}
	moment := stamp(transaction.now)
	threadID := plan.threadID
	if threadID == "" {
		threadID = id
	}
	bodyHash := sha256.Sum256([]byte(plan.body))
	envelope := Envelope{
		V: 1, ID: id, ThreadID: threadID, ReplyTo: plan.replyTo, Kind: plan.kind, From: plan.sender.Key,
		FromName: plan.sender.Name, FromChannelID: plan.sender.ChannelID, To: plan.to, ToChannelID: plan.toChannel,
		Summary: plan.summary, Priority: plan.priority, Created: moment,
		ExpiresAt: stamp(transaction.now.Add(time.Duration(plan.expiry) * time.Second)),
		BodyBytes: len(plan.body), BodySHA256: hex.EncodeToString(bodyHash[:]),
		Recipients: recipientHashes(plan.recipients), Response: plan.response, Attachments: plan.attachments,
		Meta: plan.meta, IdempotencyKey: plan.idempotencyKey,
	}
	if plan.request != nil {
		request := *plan.request
		if request.RequestID, err = newRequestID(transaction.store.random); err != nil {
			return SendResult{}, err
		}
		envelope.Request = &request
	}
	if renderedSize(envelope) > renderedBound {
		return SendResult{}, refuse(ReasonTooLarge, "everything but the body must fit in 12 KiB as shown; shorten the summary, attachments or meta")
	}
	envelopeData, err := encodeRecord(envelope, envelopeBound)
	if err != nil {
		return SendResult{}, refuse(ReasonTooLarge, "the message's record would pass 48 KiB")
	}
	deliveries := make([]Delivery, len(plan.recipients))
	for index, recipient := range plan.recipients {
		hash := envelope.Recipients[index]
		state := deliverystate.New(routeOf(recipient), plan.request != nil, plan.gone[hash])
		delivery := Delivery{V: 1, ID: id, Key: recipient.Key, At: moment}.withTable(state, moment)
		delivery.At = moment
		deliveries[index] = delivery
	}
	charge := deliverystate.SendCharge(len(plan.body), len(deliveries), plan.idempotencyKey != "")
	if envelope.Request != nil {
		charge += deliverystate.MarkerCharge // the request's index entry
	}
	if err := transaction.makeRoom(plan, charge); err != nil {
		return SendResult{}, err
	}
	entry := journalEntry{Op: opSend, ID: id, Sender: plan.sender.Key, Request: plan.answers, Responder: plan.responder}
	if plan.answers != "" {
		entry.Op = opRespond
	}
	if plan.idempotencyKey != "" {
		entry.Idempotency = filepath.Base(transaction.place.idempotencyEntry(plan.sender.Key, plan.idempotencyKey))
	}
	if envelope.Request != nil {
		entry.Asks = envelope.Request.RequestID
	}
	if plan.watch {
		entry.Subscription = &subscriptionPlan{
			Recipient: envelope.Recipients[0], Watcher: plan.sender.Key, MessageID: id,
			Until: stamp(transaction.now.Add(idleSubscriptionLife)),
		}
	}
	if err := transaction.publishJournal(entry); err != nil {
		return SendResult{}, err
	}
	if err := transaction.writeBeforeCommit(plan.body, envelopeData, envelope, deliveries); err != nil {
		if rollErr := transaction.rollBack(entry); rollErr != nil {
			transaction.store.log("rollback-failed", id)
		}
		return SendResult{}, err
	}
	commit := Commit{V: 1, ID: id, Committed: moment}
	if plan.request != nil {
		commit.RequestState = string(deliverystate.Open)
	}
	if durableErr := transaction.writeCommit(commit); durableErr != nil {
		if errors.Is(durableErr, durable.ErrNotSynced) {
			// The commit may still be undone by a crash of the machine: stop here, keep the entry,
			// and let the store stay stopped until a recovery makes it durable (section 7.2).
			transaction.store.log("commit-not-durable", id)
			return SendResult{Outcome: OutcomeUncertain, MessageID: id, ThreadID: threadID}, nil
		}
		_ = transaction.rollBack(entry)
		return SendResult{}, durableErr
	}
	// Past the commit the message exists: a failure from here on leaves the entry for recovery to
	// finish, and the sender is told uncertain with the IDs, never a bare error it might retry blind.
	uncertain := func(err error) (SendResult, error) {
		transaction.store.log("after-commit-failure", id+": "+err.Error())
		result := SendResult{Outcome: OutcomeUncertain, MessageID: id, ThreadID: threadID}
		if envelope.Request != nil {
			result.RequestID = envelope.Request.RequestID
		}
		return result, nil
	}
	if err := transaction.applyCommitted(entry, envelope); err != nil {
		return uncertain(err)
	}
	if err := transaction.writeDerived(entry, envelope); err != nil {
		return uncertain(err)
	}
	if err := transaction.removeJournal(id); err != nil {
		return uncertain(err)
	}
	transaction.countExchange(plan)
	result := SendResult{Outcome: OutcomeAccepted, MessageID: id, ThreadID: threadID}
	if envelope.Request != nil {
		result.RequestID = envelope.Request.RequestID
	}
	for index, recipient := range plan.recipients {
		if index == maxInlineRecipients {
			result.RecipientsMore = len(plan.recipients) - index
			break
		}
		result.Recipients = append(result.Recipients, RecipientState{Session: sessionAddress(recipient, plan.sessions), State: deliveries[index].State})
	}
	return result, nil
}

// makeRoom admits a send's charge and the account's message count (section 7.4), measured from the
// folders now.
func (transaction *transaction) makeRoom(plan planned, charge int64) error {
	limit := transaction.store.writeLimit
	messages := maxLiveMessages
	if plan.kind == "event" {
		limit, messages = transaction.store.eventLimit, maxLiveMessages/10*9
	}
	measured, err := transaction.measure()
	if err != nil {
		return err
	}
	if measured.Messages < messages && deliverystate.Admit(measured.Bytes, charge, limit) {
		return nil
	}
	return refuse(ReasonCapacity, "the chat store is full; wait for its sweep to free room")
}

// writeBeforeCommit is step 2: the body, the envelope and every delivery. A folder that will not
// sync fails the operation, which is then rolled back: nothing of it may be committed over files a
// crash could still lose.
func (transaction *transaction) writeBeforeCommit(body string, envelopeData []byte, envelope Envelope, deliveries []Delivery) error {
	place := transaction.place
	for _, folder := range []string{place.bodies(), place.messages(), place.deliveries(envelope.ID)} {
		if err := makeFolder(folder); err != nil {
			return err
		}
	}
	if err := writeSynced(place.body(envelope.ID), []byte(body), MaxBodyBytes); err != nil {
		return err
	}
	if err := writeSynced(place.envelope(envelope.ID), envelopeData, envelopeBound); err != nil {
		return err
	}
	if envelope.Request != nil {
		if err := publishFile(place.requests(), envelope.Request.RequestID, []byte(envelope.ID)); err != nil {
			return err
		}
	}
	for index, delivery := range deliveries {
		data, err := encodeRecord(delivery, deliveryBound)
		if err != nil {
			return err
		}
		if err := writeSynced(place.delivery(envelope.ID, envelope.Recipients[index]), data, deliveryBound); err != nil {
			return err
		}
	}
	return nil
}

// writeSynced is durable.WriteAtomic that tries the folder's fsync once more before it gives up.
func writeSynced(path string, data []byte, limit int) error {
	err := durable.WriteAtomic(path, data, 0o600, limit)
	if errors.Is(err, durable.ErrNotSynced) {
		if durable.SyncFolder(filepath.Dir(path)) == nil {
			return nil
		}
		return refuse(ReasonStoreBusy, "the chat store could not make a write durable; retry")
	}
	return err
}

// writeCommit writes the commit record; durable.ErrNotSynced when its folder will not sync even
// on a second try (section 7.2).
func (transaction *transaction) writeCommit(commit Commit) error {
	data, err := encodeRecord(commit, commitBound)
	if err != nil {
		return err
	}
	if err := makeFolder(transaction.place.commits()); err != nil {
		return err
	}
	err = transaction.store.writeCommitFile(transaction.place.commit(commit.ID), data, 0o600, commitBound)
	if errors.Is(err, durable.ErrNotSynced) && transaction.store.syncFolder(transaction.place.commits()) == nil {
		return nil
	}
	return err
}

// writeDelivery replaces a delivery record and, when it left the unread states, removes its inbox
// marker after (section 7.3).
func (transaction *transaction) writeDelivery(id, hash string, delivery Delivery) error {
	delivery.V = 1
	if err := writeRecord(transaction.place.delivery(id, hash), delivery, deliveryBound); err != nil {
		return err
	}
	if !deliverystate.Unread(deliverystate.State(delivery.State)) {
		if err := os.Remove(filepath.Join(transaction.place.inbox(hash), id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// countExchange adds one to the loop count of a direct message's pair, on both sides (section 4.8).
// A crash before it loses one count, never a message.
func (transaction *transaction) countExchange(plan planned) {
	if plan.toChannel != "" || len(plan.recipients) != 1 || plan.sender.Key == SystemKey {
		return
	}
	one, other := plan.sender, plan.recipients[0]
	for _, pair := range [][2]Session{{one, other}, {other, one}} {
		session, err := transaction.readSession(hashKey(pair[0].Key))
		if err != nil {
			continue
		}
		if session.Exchange == nil {
			session.Exchange = map[string]int{}
		}
		peer := hashKey(pair[1].Key)
		if _, known := session.Exchange[peer]; !known && len(session.Exchange) >= 64 {
			continue
		}
		session.Exchange[peer] = min(session.Exchange[peer]+1, exchangeLimit)
		if session.Key == plan.sender.Key {
			session.LastSeen = stamp(transaction.now)
		}
		if err := transaction.writeSession(session); err != nil {
			transaction.store.log("exchange-not-counted", session.Ref)
		}
	}
}

// duplicateOf answers a send whose idempotency key was used: duplicate when its message still has
// its commit record; a leftover entry is removed and the send is fresh (section 7.3).
func (transaction *transaction) duplicateOf(senderKey, key string) (SendResult, bool, error) {
	if key == "" {
		return SendResult{}, false, nil
	}
	path := transaction.place.idempotencyEntry(senderKey, key)
	data, err := durable.ReadLimited(path, 64)
	if errors.Is(err, os.ErrNotExist) {
		return SendResult{}, false, nil
	}
	if err != nil {
		return SendResult{}, false, err
	}
	id := string(data)
	committed := false
	if messageIDPattern.MatchString(id) {
		// Only a commit record known to be absent makes the entry a leftover; any other answer of
		// the disk stops the send, so a retry is never taken for fresh by mistake.
		_, lookupErr := os.Lstat(transaction.place.commit(id))
		switch {
		case lookupErr == nil:
			committed = true
		case !errors.Is(lookupErr, os.ErrNotExist):
			return SendResult{}, false, lookupErr
		}
	}
	if deliverystate.ResolveRetry(true, committed) != deliverystate.Duplicate {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return SendResult{}, false, err
		}
		return SendResult{}, false, nil
	}
	var envelope Envelope
	if err := readRecord(transaction.place.envelope(id), envelopeBound, &envelope); err != nil {
		return SendResult{}, false, err
	}
	result := SendResult{Outcome: OutcomeDuplicate, MessageID: id, ThreadID: envelope.ThreadID}
	if envelope.Request != nil {
		result.RequestID = envelope.Request.RequestID
	}
	sessions, err := transaction.listSessions()
	if err != nil {
		return SendResult{}, false, err
	}
	byHash := map[string]Session{}
	for _, session := range sessions {
		byHash[hashKey(session.Key)] = session
	}
	for index, hash := range envelope.Recipients {
		if index == maxInlineRecipients {
			result.RecipientsMore = len(envelope.Recipients) - index
			break
		}
		var delivery Delivery
		if err := readRecord(transaction.place.delivery(id, hash), deliveryBound, &delivery); err != nil {
			continue
		}
		name := hash[:6]
		if session, ok := byHash[hash]; ok {
			name = sessionAddress(session, sessions)
		}
		result.Recipients = append(result.Recipients, RecipientState{Session: name, State: delivery.State})
	}
	return result, true, nil
}

// subscribeIdle is a standalone notify_when_idle (section 4.9): nothing is stored but the
// subscription on the recipient's record, or, when the recipient is idle now, the notice at once.
func (transaction *transaction) subscribeIdle(watcher, recipient Session, sessions []Session) (SendResult, error) {
	about := "[" + recipient.Ref + "]"
	result := SendResult{Outcome: OutcomeSubscribed, Watching: sessionAddress(recipient, sessions)}
	for _, notice := range watcher.Notices {
		moment, ok := parseStamp(notice.At)
		if notice.Kind == "idle" && notice.About == about && ok && !moment.Before(transaction.now.Add(-time.Minute)) {
			result.Fired, result.FiredAt = true, notice.At
			return result, nil
		}
	}
	if recipient.State == StateIdle {
		moment := stamp(transaction.now)
		notice := SessionNotice{Kind: "idle", About: about, At: moment, State: string(deliverystate.NoticeOwed)}
		notice.ID = "idle-" + recipient.Ref + "-" + moment
		watcher.Notices = transaction.addNotice(watcher.Notices, notice)
		result.Fired, result.FiredAt, result.Until = true, moment, moment
		return result, transaction.writeSession(watcher)
	}
	until := stamp(transaction.now.Add(idleSubscriptionLife))
	watchers := slices.DeleteFunc(slices.Clone(recipient.IdleWatchers), func(existing IdleWatcher) bool {
		return existing.Key == watcher.Key
	})
	watchers = append(watchers, IdleWatcher{Key: watcher.Key, Until: until, Armed: true, ArmedGeneration: recipient.IdleGeneration})
	if len(watchers) > maxWatchers {
		return SendResult{}, refuse(ReasonCapacity, "%s already has %d idle watchers", fullAddress(recipient), maxWatchers)
	}
	recipient.IdleWatchers = watchers
	result.Until = until
	return result, transaction.writeSession(recipient)
}

// addNotice adds an owed notice to a session's list (section 6.1): shown ones older than 24 h go
// first, then the oldest shown one; when all 32 are owed, the oldest owed is dropped and logged.
func (transaction *transaction) addNotice(notices []SessionNotice, notice SessionNotice) []SessionNotice {
	cutoff := transaction.now.Add(-24 * time.Hour)
	kept := slices.DeleteFunc(slices.Clone(notices), func(existing SessionNotice) bool {
		moment, ok := parseStamp(existing.At)
		return existing.State == string(deliverystate.NoticeShown) && (!ok || moment.Before(cutoff))
	})
	for len(kept) >= maxWatchers {
		index := slices.IndexFunc(kept, func(existing SessionNotice) bool { return existing.State == string(deliverystate.NoticeShown) })
		if index < 0 {
			index = 0
			transaction.store.log("notice-dropped", kept[0].ID)
		}
		kept = slices.Delete(kept, index, index+1)
	}
	return append(kept, notice)
}

// trimmed returns text with no leading or trailing space, for summaries built from a reason.
func trimmed(text string) string { return strings.TrimSpace(text) }
