// Package deliverystate is the transition table of the agent chat's deliveries
// (docs/design/chat.md, sections 4.2, 4.10, 7.3 and 8). It holds no files and no clock: the store
// calls Apply under its lock with what it observed (the deadline, the bridge's answer, a refused
// doorbell) and writes back the delivery it returns. The model in docs/design/chat-model and the
// property tests of this package check the same rules.
//
// Since W-095 the native and hooks routes carry no content: they ring a doorbell, and a message's
// text reaches the recipient's model only when the recipient reads it from its inbox. Only the
// Codex bridge still pushes a message's text, at most once.
package deliverystate

import (
	"errors"
	"fmt"
)

// State is a delivery's receipt (section 4.2).
type State string

// The delivery states.
const (
	Pending   State = "pending"
	Sending   State = "sending"
	Delivered State = "delivered"
	Read      State = "read"
	Acted     State = "acted"
	Refused   State = "refused"
	Dropped   State = "dropped"
	Expired   State = "expired"
)

// States lists every delivery state.
var States = []State{Pending, Sending, Delivered, Read, Acted, Refused, Dropped, Expired}

// Route is the one way a delivery is pushed (section 8.1).
type Route string

// The routes.
const (
	Native  Route = "native"
	Hooks   Route = "hooks"
	Bridge  Route = "bridge"
	Mailbox Route = "mailbox"
	Poll    Route = "poll"
)

// Routes lists every route.
var Routes = []Route{Native, Hooks, Bridge, Mailbox, Poll}

// Reason qualifies a state (section 5.4).
type Reason string

// The receipt reasons.
const (
	NoReason    Reason = ""
	Unconfirmed Reason = "unconfirmed"
	// EngineRefused: the recipient's engine refused the native doorbell (it closed the connection).
	EngineRefused  Reason = "engine_refused"
	RecipientEnded Reason = "recipient_ended"
	ExpiredReason  Reason = "expired"
)

// Notice is the state of the notice a delivery owes its sender (section 8.5).
type Notice string

// The notice states; NoNotice means none is owed.
const (
	NoNotice      Notice = ""
	NoticeOwed    Notice = "owed"
	NoticeSending Notice = "sending"
	NoticeShown   Notice = "shown"
)

// MaxAttempts is the number of confirmed failures after which the bridge stops steering a delivery,
// which then waits in the recipient's inbox (section 9.2).
const MaxAttempts = 4

// Delivery is the part of a delivery record the table reads and writes.
type Delivery struct {
	Route    Route
	State    State
	Reason   Reason
	Pusher   string // the bridge pass that set `sending`; empty otherwise
	Attempts int
	Notice   Notice
	// Request marks the delivery of a request: its expiry notice is owed on the request's commit
	// record, not here, so the requester is told once (section 4.4).
	Request bool
}

// Kind names what happened to a delivery.
type Kind string

// The events of the table.
const (
	// Claim is the bridge setting `sending` before it steers a message (section 9.2).
	Claim Kind = "claim"
	// Answer is the app-server's answer to a steer, recorded by its pass.
	Answer Kind = "answer"
	// Failure is a confirmed failure of a steer: nothing reached the engine.
	Failure Kind = "failure"
	// Stale is the sweep finding `sending` older than 60 s.
	Stale Kind = "stale"
	// RingRefused is the recipient's engine refusing the native doorbell: it closed the connection
	// before the line was taken (section 8.3). A doorbell it took, or ignored, changes nothing.
	RingRefused Kind = "ring-refused"
	// Expire is the sweep, or another step's deadline check, finding `expires_at` passed.
	Expire Kind = "expire"
	// End is the recipient's end: SessionEnd, a dead process, or a mailbox closed without a freeze.
	End Kind = "end"
	// ReadWhole is chat.inbox emitting the whole body in one response.
	ReadWhole Kind = "read-whole"
	// AckRead is chat.ack with `read`.
	AckRead Kind = "ack-read"
	// AckActed is chat.ack with `acted`, or chat.respond to this request.
	AckActed Kind = "ack-acted"
	// Freeze is a review mailbox's freeze reading what was sent to it (section 9.1).
	Freeze Kind = "freeze"
	// CloseRequest is a request closed before its deadline (a freeze of the mailbox that asked it):
	// its delivery, if not yet read, expires with it.
	CloseRequest Kind = "close-request"
)

// EngineAnswer is what a steer's answer said.
type EngineAnswer string

// The answers a bridge pass records.
const (
	Accepted EngineAnswer = "accepted"
	// Lost is a steer written with no answer, or one jackstay cannot parse.
	Lost EngineAnswer = "lost"
)

// Event is one thing that happened to a delivery, with what the store observed when it happened.
type Event struct {
	Kind Kind
	// Pusher is the bridge pass that claims (Claim) or records (Answer, Failure).
	Pusher string
	// Answer is the steer's answer (Answer).
	Answer EngineAnswer
	// Overdue is true when `expires_at` has passed, checked under the same lock (section 4.10).
	Overdue bool
}

// Errors of Apply. The delivery returned with them is the one given, unchanged.
var (
	// ErrNotAllowed: the event has no transition from this state on this route.
	ErrNotAllowed = errors.New("deliverystate: transition not allowed")
	// ErrSuperseded: a pass recorded an outcome for a delivery it no longer owns (compare-and-set);
	// the outcome is dropped and logged.
	ErrSuperseded = errors.New("deliverystate: the delivery moved on; the outcome is dropped")
	// ErrNotVisible: the recipient asked for a delivery whose body it may no longer read.
	ErrNotVisible = errors.New("deliverystate: not visible to the recipient")
)

// Final reports whether a state is never left.
func Final(state State) bool {
	return state == Acted || state == Refused || state == Dropped || state == Expired
}

// Unread reports whether a delivery keeps its inbox marker (section 7.1).
func Unread(state State) bool {
	return state == Pending || state == Sending || state == Delivered
}

// CountsAgainstQueue reports whether a delivery counts towards the recipient's 50 (section 4.8).
func CountsAgainstQueue(state State) bool {
	return state == Pending || state == Sending
}

// Expirable reports whether a deadline moves the state to expired (section 4.10): a message the
// recipient has not read, and that no steer is carrying.
func Expirable(state State) bool { return state == Pending }

// Visible reports whether the body is visible to the recipient (section 4.10): on every route, until
// the delivery is refused, dropped or expired. No route shows a body anywhere but the inbox read,
// except the bridge's steer, which is the same text the inbox returns.
func Visible(delivery Delivery) bool {
	switch delivery.State {
	case Refused, Dropped, Expired:
		return false
	}
	return true
}

// Due reports whether a bridge pass may claim the delivery now, before priority and next_attempt.
func Due(delivery Delivery) bool {
	return delivery.State == Pending && delivery.Route == Bridge && delivery.Attempts < MaxAttempts
}

// Rings reports whether the recipient's doorbell may ring for the delivery: it waits unread on the
// native or hooks route (sections 8.3 and 8.4). Ringing changes nothing, so a repeated ring is
// harmless.
func Rings(delivery Delivery) bool {
	return delivery.State == Pending && (delivery.Route == Native || delivery.Route == Hooks)
}

// Apply returns the delivery after the event, or the delivery unchanged and an error.
func Apply(delivery Delivery, event Event) (Delivery, error) {
	next, err := apply(delivery, event)
	if err != nil {
		return delivery, err
	}
	if next.State != delivery.State && !Allowed(delivery.State, next.State, event.Kind) {
		panic(fmt.Sprintf("deliverystate: %s took %s to %s, which the table does not list", event.Kind, delivery.State, next.State))
	}
	return next, nil
}

func apply(delivery Delivery, event Event) (Delivery, error) {
	// Deadlines first (section 4.10): a step that would steer, read, ack or answer an overdue
	// pending delivery expires it instead, and goes on as if it had expired already.
	if event.Overdue && Expirable(delivery.State) && checksDeadline(event.Kind) {
		return expire(delivery), nil
	}
	switch event.Kind {
	case Claim:
		if !Due(delivery) {
			return delivery, ErrNotAllowed
		}
		delivery.State, delivery.Pusher = Sending, event.Pusher
		return delivery, nil
	case Answer, Failure:
		if delivery.State != Sending || delivery.Pusher != event.Pusher {
			return delivery, ErrSuperseded
		}
		delivery.Pusher = ""
		if event.Kind == Failure {
			delivery.State = Pending
			delivery.Attempts++
			return delivery, nil
		}
		switch event.Answer {
		case Accepted:
			delivery.State = Delivered
		case Lost:
			delivery.State, delivery.Reason = Delivered, Unconfirmed
		default:
			return delivery, ErrNotAllowed
		}
		return delivery, nil
	case Stale:
		if delivery.State != Sending {
			return delivery, ErrNotAllowed
		}
		delivery.State, delivery.Reason, delivery.Pusher = Delivered, Unconfirmed, ""
		return delivery, nil
	case RingRefused:
		if delivery.Route != Native || delivery.State != Pending {
			return delivery, ErrNotAllowed
		}
		delivery.State, delivery.Reason, delivery.Notice = Refused, EngineRefused, NoticeOwed
		return delivery, nil
	case Expire, CloseRequest:
		if !Expirable(delivery.State) {
			return delivery, ErrNotAllowed
		}
		return expire(delivery), nil
	case End:
		if delivery.State != Pending && delivery.State != Sending {
			return delivery, ErrNotAllowed
		}
		delivery.State, delivery.Reason, delivery.Pusher = Dropped, RecipientEnded, ""
		delivery.Notice = NoticeOwed
		return delivery, nil
	case ReadWhole:
		if delivery.State != Pending && delivery.State != Delivered {
			return delivery, ErrNotVisible
		}
		return confirmedRead(delivery), nil
	case AckRead, AckActed:
		if Final(delivery.State) {
			// Accepted, changes nothing, and says so (section 5.2, chat.ack).
			return delivery, nil
		}
		if event.Kind == AckRead {
			if delivery.State == Pending || delivery.State == Delivered {
				return confirmedRead(delivery), nil
			}
			return delivery, nil // `read` already, or being steered: changes nothing
		}
		delivery.State, delivery.Pusher = Acted, ""
		if delivery.Reason == Unconfirmed {
			delivery.Reason = NoReason
		}
		return delivery, nil
	case Freeze:
		if delivery.Route != Mailbox || delivery.State != Pending {
			return delivery, ErrNotAllowed
		}
		delivery.State = Read
		return delivery, nil
	}
	return delivery, ErrNotAllowed
}

func confirmedRead(delivery Delivery) Delivery {
	delivery.State = Read
	if delivery.Reason == Unconfirmed {
		delivery.Reason = NoReason
	}
	return delivery
}

func expire(delivery Delivery) Delivery {
	delivery.State, delivery.Reason, delivery.Pusher = Expired, ExpiredReason, ""
	if !delivery.Request {
		delivery.Notice = NoticeOwed
	}
	return delivery
}

func checksDeadline(kind Kind) bool {
	switch kind {
	case Claim, RingRefused, ReadWhole, AckRead, AckActed:
		return true
	}
	return false
}

// Edge is one allowed transition of section 4.2's table.
type Edge struct {
	From, To State
	Kind     Kind
}

// Table lists every allowed transition, and nothing else (section 4.2).
var Table = []Edge{
	{Pending, Sending, Claim},
	{Pending, Expired, Claim},
	{Sending, Delivered, Answer},
	{Sending, Pending, Failure},
	{Sending, Delivered, Stale},
	{Pending, Refused, RingRefused},
	{Pending, Expired, RingRefused},
	{Pending, Expired, Expire},
	{Pending, Expired, CloseRequest},
	{Pending, Dropped, End},
	{Sending, Dropped, End},
	{Pending, Read, ReadWhole},
	{Delivered, Read, ReadWhole},
	{Pending, Expired, ReadWhole},
	{Pending, Read, AckRead},
	{Delivered, Read, AckRead},
	{Pending, Expired, AckRead},
	{Pending, Acted, AckActed},
	{Sending, Acted, AckActed},
	{Delivered, Acted, AckActed},
	{Read, Acted, AckActed},
	{Pending, Expired, AckActed},
	{Pending, Read, Freeze},
}

// Allowed reports whether the table lists the transition.
func Allowed(from, target State, kind Kind) bool {
	for _, edge := range Table {
		if edge.From == from && edge.To == target && edge.Kind == kind {
			return true
		}
	}
	return false
}

// RequestState is a request's own state, apart from its delivery (section 4.4).
type RequestState string

// The request states.
const (
	Open     RequestState = "open"
	Answered RequestState = "answered"
	Closed   RequestState = "expired"
)

// Request is the part of a request's commit record the table reads and writes.
type Request struct {
	State  RequestState
	Notice Notice
}

// ExpireRequest closes an open request at its deadline, or at its mailbox's freeze, in one step
// with its delivery: the delivery expires too when it is still pending, and the requester is owed
// one notice, on the request's commit record, whatever became of the delivery.
func ExpireRequest(request Request, delivery Delivery) (Request, Delivery, error) {
	if request.State != Open {
		return request, delivery, ErrNotAllowed
	}
	request.State, request.Notice = Closed, NoticeOwed
	if Expirable(delivery.State) {
		next, err := Apply(delivery, Event{Kind: CloseRequest})
		if err != nil {
			return request, delivery, err
		}
		delivery = next
	}
	return request, delivery, nil
}

// ApplyMessage is Apply for a delivery together with its message's request (the zero Request for a
// message that is not one). A deadline found by any step closes an open request, whatever its
// delivery's state, and expires the delivery with it when that is still pending (ExpireRequest),
// before the event applies; so a pass's or a reader's deadline check never expires one without the
// other, and a read past the deadline of a delivered request still closes the request.
func ApplyMessage(request Request, delivery Delivery, event Event) (Request, Delivery, error) {
	// A request's closure writes two files, its commit record and then its delivery; a crash
	// between them leaves the request closed and its delivery pending. Every step repairs that
	// first, so a closed question is never claimed.
	if delivery.Request && request.State == Closed && Expirable(delivery.State) {
		repaired, err := Apply(delivery, Event{Kind: CloseRequest})
		if err != nil {
			return request, delivery, err
		}
		if next, err := Apply(repaired, event); err == nil {
			repaired = next
		}
		return request, repaired, nil
	}
	if delivery.Request && request.State == Open && event.Overdue && checksDeadline(event.Kind) {
		closedRequest, closedDelivery, err := ExpireRequest(request, delivery)
		if err != nil {
			return request, delivery, err
		}
		// The event goes on as if the deadline had been applied already (section 4.10); what is
		// left of it, if anything, is the expiry itself.
		if next, err := Apply(closedDelivery, event); err == nil {
			closedDelivery = next
		}
		return closedRequest, closedDelivery, nil
	}
	next, err := Apply(delivery, event)
	if err != nil {
		return request, delivery, err
	}
	// A request closed early (its mailbox froze) while its delivery was `sending`: a confirmed
	// failure must not leave a closed question waiting to be steered.
	if delivery.Request && request.State == Closed && Expirable(next.State) {
		if closed, err := Apply(next, Event{Kind: CloseRequest}); err == nil {
			next = closed
		}
	}
	return request, next, nil
}

// Respond answers an open request in one commit with its response (section 4.4): the request becomes
// answered and its delivery acted, unless that delivery is final. An overdue request is closed
// instead and the response refused (request_closed).
func Respond(request Request, delivery Delivery, overdue bool) (Request, Delivery, error) {
	if request.State == Answered {
		return request, delivery, ErrAlreadyAnswered
	}
	if request.State == Closed {
		return request, delivery, ErrRequestClosed
	}
	if overdue {
		closedRequest, closedDelivery, err := ExpireRequest(request, delivery)
		if err != nil {
			return request, delivery, err
		}
		return closedRequest, closedDelivery, ErrRequestClosed
	}
	next, err := Apply(delivery, Event{Kind: AckActed})
	if err != nil {
		return request, delivery, err
	}
	request.State = Answered
	return request, next, nil
}

// Errors of Respond.
var (
	ErrAlreadyAnswered = errors.New("deliverystate: already_answered")
	ErrRequestClosed   = errors.New("deliverystate: request_closed")
)

// Notice transitions (section 8.5): owed → shown when chat.inbox or a hook includes it. A doorbell
// says only that notices wait; it shows none.

// ShowNotice marks an owed notice shown by a hook or chat.inbox, in the step that returns it.
func ShowNotice(notice Notice) (Notice, error) {
	if notice != NoticeOwed {
		return notice, ErrNotAllowed
	}
	return NoticeShown, nil
}

// MessageDone reports whether retention and pressure may remove a message (section 7.4): none of its
// deliveries needs it, its request is not open, and no notice is owed on the request's commit
// record (the only notice of a request that expired).
func MessageDone(deliveries []Delivery, whole []bool, request Request) bool {
	if request.State == Open || request.Notice == NoticeOwed || request.Notice == NoticeSending {
		return false
	}
	for index, delivery := range deliveries {
		if NeedsMessage(delivery, index < len(whole) && whole[index]) {
			return false
		}
	}
	return true
}

// NeedsMessage reports whether a delivery keeps its message from retention and pressure
// (section 7.4). Whole is true when a push carried the whole body.
func NeedsMessage(delivery Delivery, whole bool) bool {
	switch {
	case delivery.State == Pending || delivery.State == Sending:
		return true
	case delivery.State == Delivered && (delivery.Reason == Unconfirmed || !whole):
		return true
	case delivery.State == Delivered && delivery.Route == Bridge:
		return true
	case delivery.Notice == NoticeOwed || delivery.Notice == NoticeSending:
		return true
	}
	return false
}

// New is a delivery as a send creates it: `pending` on its route, or, for a response to a requester
// already gone, `dropped` (recipient_ended) with its notice owed like any other dropped delivery.
func New(route Route, request, recipientGone bool) Delivery {
	if recipientGone {
		return Delivery{Route: route, State: Dropped, Reason: RecipientEnded, Notice: NoticeOwed, Request: request}
	}
	return Delivery{Route: route, State: Pending, Request: request}
}
