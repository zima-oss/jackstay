// Package deliverystate is the transition table of the agent chat's deliveries
// (docs/design/chat.md, sections 4.2, 4.10, 7.3 and 8). It holds no files and no clock: the store
// calls Apply under its lock with what it observed (the deadline, the pusher's answer, what the
// caller named) and writes back the delivery it returns. The model in docs/design/chat-model and the
// property tests of this package check the same rules.
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
	Held      State = "held"
	Read      State = "read"
	Acted     State = "acted"
	Refused   State = "refused"
	Dropped   State = "dropped"
	Expired   State = "expired"
)

// States lists every delivery state.
var States = []State{Pending, Sending, Delivered, Held, Read, Acted, Refused, Dropped, Expired}

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
	NoReason             Reason = ""
	Unconfirmed          Reason = "unconfirmed"
	EngineRefused        Reason = "engine_refused"
	EngineQueueFull      Reason = "engine_queue_full"
	RecipientEnded       Reason = "recipient_ended"
	ExpiredReason        Reason = "expired"
	EngineHeldUnresolved Reason = "engine_held_unresolved"
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

// MaxAttempts is the number of confirmed failures after which the native route gives the
// delivery to the hooks, and the bridge stops steering it (sections 8.3 and 9.2).
const MaxAttempts = 4

// Delivery is the part of a delivery record the table reads and writes.
type Delivery struct {
	Route    Route
	State    State
	Reason   Reason
	Pusher   string // the pass that set `sending`; empty otherwise
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
	// Claim is a native or bridge pass setting `sending` before it pushes (section 8.3 step 1).
	Claim Kind = "claim"
	// Announce is a hook putting the delivery in its output (section 8.4).
	Announce Kind = "announce"
	// Answer is the engine's (or app-server's) answer to a push, recorded by its pass.
	Answer Kind = "answer"
	// Failure is a confirmed failure: nothing reached the engine.
	Failure Kind = "failure"
	// Stale is the sweep finding `sending` older than 60 s.
	Stale Kind = "stale"
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
	// Confirm is chat.inbox naming the delivery in `ids`.
	Confirm Kind = "confirm"
	// Freeze is a review mailbox's freeze reading what was sent to it (section 9.1).
	Freeze Kind = "freeze"
	// CloseRequest is a request closed before its deadline (a freeze of the mailbox that asked it):
	// its delivery, if not yet pushed or still held, expires with it.
	CloseRequest Kind = "close-request"
)

// EngineAnswer is what a push's answer said.
type EngineAnswer string

// The answers a pass records.
const (
	Accepted  EngineAnswer = "accepted"
	HeldBy    EngineAnswer = "held"
	RefusedBy EngineAnswer = "refused"
	QueueFull EngineAnswer = "queue_full"
	// Lost is a line written with no answer, or one jackstay cannot parse.
	Lost EngineAnswer = "lost"
)

// Naming is what a recipient's call named the message by.
type Naming string

// How a call names a delivery.
const (
	// ByNothing: the call does not name it (chat.inbox without ids).
	ByNothing Naming = ""
	// ByID: the bare message ID, which other messages' `thread` and `reply_to` also carry.
	ByID Naming = "id"
	// ByHandle: the delivery's handle, or its request ID, which only a frame the engine showed carries.
	ByHandle Naming = "handle"
)

// Event is one thing that happened to a delivery, with what the store observed when it happened.
type Event struct {
	Kind Kind
	// Pusher is the pass that claims (Claim) or records (Answer, Failure).
	Pusher string
	// Answer is the push's answer (Answer).
	Answer EngineAnswer
	// Overdue is true when `expires_at` has passed, checked under the same lock (section 4.10).
	Overdue bool
	// Named is how the recipient's call named the delivery (ReadWhole, AckRead, AckActed, Confirm).
	Named Naming
}

// Errors of Apply. The delivery returned with them is the one given, unchanged.
var (
	// ErrNotAllowed: the event has no transition from this state on this route.
	ErrNotAllowed = errors.New("deliverystate: transition not allowed")
	// ErrSuperseded: a pass recorded an outcome for a delivery it no longer owns (compare-and-set);
	// the outcome is dropped and logged.
	ErrSuperseded = errors.New("deliverystate: the delivery moved on; the outcome is dropped")
	// ErrNotVisible: the recipient named a native delivery its engine has not shown, by a name
	// that does not prove the engine showed it.
	ErrNotVisible = errors.New("deliverystate: not visible to the recipient")
)

// Final reports whether a state is never left.
func Final(state State) bool {
	return state == Acted || state == Refused || state == Dropped || state == Expired
}

// Unread reports whether a delivery keeps its inbox marker (section 7.1).
func Unread(state State) bool {
	return state == Pending || state == Sending || state == Delivered || state == Held
}

// CountsAgainstQueue reports whether a delivery counts towards the recipient's 50 (section 4.8).
func CountsAgainstQueue(state State) bool {
	return state == Pending || state == Sending || state == Held
}

// Expirable reports whether a deadline moves the state to expired (section 4.10).
func Expirable(state State) bool { return state == Pending || state == Held }

// Visible reports whether the body is visible to the recipient (section 4.10), given how the
// reading call names it.
func Visible(delivery Delivery, named Naming) bool {
	switch delivery.State {
	case Held, Refused, Dropped, Expired:
		if delivery.Route != Native || delivery.State != Held {
			return false
		}
		return named == ByHandle
	}
	if delivery.Route != Native {
		return true
	}
	switch delivery.State {
	case Read, Acted:
		return true
	case Delivered:
		return delivery.Reason != Unconfirmed || named == ByHandle
	default: // pending, sending: the engine has not answered
		return named == ByHandle
	}
}

// Due reports whether a pass may claim the delivery now, before priority and next_attempt.
func Due(delivery Delivery) bool {
	return delivery.State == Pending &&
		(delivery.Route == Native || delivery.Route == Bridge) && delivery.Attempts < MaxAttempts
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
	// Deadlines first (section 4.10): a step that would push, read, ack or answer an overdue
	// pending or held delivery expires it instead, and goes on as if it had expired already.
	if event.Overdue && Expirable(delivery.State) && checksDeadline(event.Kind) &&
		(delivery.State == Pending || namesTheDelivery(event.Kind)) {
		return expire(delivery), nil
	}
	switch event.Kind {
	case Claim:
		if !Due(delivery) {
			return delivery, ErrNotAllowed
		}
		delivery.State, delivery.Pusher = Sending, event.Pusher
		return delivery, nil
	case Announce:
		if delivery.Route != Hooks || delivery.State != Pending {
			return delivery, ErrNotAllowed
		}
		delivery.State = Delivered
		return delivery, nil
	case Answer, Failure:
		if delivery.State != Sending || delivery.Pusher != event.Pusher {
			return delivery, ErrSuperseded
		}
		delivery.Pusher = ""
		if event.Kind == Failure {
			delivery.State = Pending
			delivery.Attempts++
			if delivery.Attempts >= MaxAttempts && delivery.Route == Native {
				delivery.Route = Hooks
			}
			return delivery, nil
		}
		switch event.Answer {
		case Accepted:
			delivery.State = Delivered
		case Lost:
			delivery.State, delivery.Reason = Delivered, Unconfirmed
		case HeldBy:
			delivery.State = Held
		case RefusedBy:
			delivery.State, delivery.Reason = Refused, EngineRefused
			delivery.Notice = NoticeOwed
		case QueueFull:
			delivery.State, delivery.Reason = Dropped, EngineQueueFull
			delivery.Notice = NoticeOwed
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
	case Expire, CloseRequest:
		if !Expirable(delivery.State) {
			return delivery, ErrNotAllowed
		}
		return expire(delivery), nil
	case End:
		if delivery.State != Pending && delivery.State != Sending && delivery.State != Held {
			return delivery, ErrNotAllowed
		}
		delivery.State, delivery.Reason, delivery.Pusher = Dropped, RecipientEnded, ""
		delivery.Notice = NoticeOwed
		return delivery, nil
	case ReadWhole:
		if (delivery.State != Pending && delivery.State != Delivered) || !Visible(delivery, event.Named) {
			return delivery, ErrNotVisible
		}
		return confirmedRead(delivery), nil
	case AckRead, AckActed, Confirm:
		return recipientNames(delivery, event)
	case Freeze:
		if delivery.Route != Mailbox || delivery.State != Pending {
			return delivery, ErrNotAllowed
		}
		delivery.State = Read
		return delivery, nil
	}
	return delivery, ErrNotAllowed
}

// recipientNames applies the recipient's own calls that name a delivery.
func recipientNames(delivery Delivery, event Event) (Delivery, error) {
	if Final(delivery.State) {
		// Accepted, changes nothing, and says so (section 5.2, chat.ack).
		return delivery, nil
	}
	// A native delivery the engine has not shown is named only by its handle: a bare ID may have
	// come from another message's `thread` or `reply_to`.
	if !Visible(delivery, event.Named) {
		return delivery, ErrNotVisible
	}
	switch event.Kind {
	case Confirm:
		if delivery.State == Held || (delivery.State == Delivered && delivery.Reason == Unconfirmed) {
			delivery.State, delivery.Reason = Delivered, NoReason
		}
		return delivery, nil
	case AckRead:
		switch delivery.State {
		case Pending, Delivered, Held:
			return confirmedRead(delivery), nil
		}
		return delivery, nil // `read` already, or passed: changes nothing
	default: // AckActed
		delivery.State, delivery.Pusher = Acted, ""
		if delivery.Reason == Unconfirmed {
			delivery.Reason = NoReason
		}
		return delivery, nil
	}
}

func confirmedRead(delivery Delivery) Delivery {
	delivery.State = Read
	if delivery.Reason == Unconfirmed {
		delivery.Reason = NoReason
	}
	return delivery
}

func expire(delivery Delivery) Delivery {
	if delivery.State == Held {
		delivery.Reason = EngineHeldUnresolved
	} else {
		delivery.Reason = ExpiredReason
	}
	delivery.State, delivery.Pusher = Expired, ""
	if !delivery.Request {
		delivery.Notice = NoticeOwed
	}
	return delivery
}

// namesTheDelivery reports whether the event is a recipient's call naming the delivery, which may
// find it held; a pass, a hook and an inbox read only ever take a pending one.
func namesTheDelivery(kind Kind) bool {
	return kind == AckRead || kind == AckActed || kind == Confirm
}

func checksDeadline(kind Kind) bool {
	switch kind {
	case Claim, Announce, ReadWhole, AckRead, AckActed, Confirm:
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
	{Pending, Delivered, Announce},
	{Pending, Expired, Announce},
	{Sending, Delivered, Answer},
	{Sending, Held, Answer},
	{Sending, Refused, Answer},
	{Sending, Dropped, Answer},
	{Sending, Pending, Failure},
	{Sending, Delivered, Stale},
	{Pending, Expired, Expire},
	{Held, Expired, Expire},
	{Pending, Expired, CloseRequest},
	{Held, Expired, CloseRequest},
	{Pending, Dropped, End},
	{Sending, Dropped, End},
	{Held, Dropped, End},
	{Pending, Read, ReadWhole},
	{Delivered, Read, ReadWhole},
	{Pending, Expired, ReadWhole},
	{Pending, Read, AckRead},
	{Delivered, Read, AckRead},
	{Held, Read, AckRead},
	{Pending, Expired, AckRead},
	{Held, Expired, AckRead},
	{Pending, Acted, AckActed},
	{Sending, Acted, AckActed},
	{Delivered, Acted, AckActed},
	{Held, Acted, AckActed},
	{Read, Acted, AckActed},
	{Pending, Expired, AckActed},
	{Held, Expired, AckActed},
	{Held, Delivered, Confirm},
	{Held, Expired, Confirm},
	{Pending, Expired, Confirm},
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
// with its delivery: the delivery expires too when it is pending or held, and the requester is owed
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
// delivery's state, and expires the delivery with it when that is pending or held
// (ExpireRequest), before the event applies; so a pusher's or a reader's deadline check never
// expires one without the other, and a read past the deadline of a delivered request still closes
// the request.
func ApplyMessage(request Request, delivery Delivery, event Event) (Request, Delivery, error) {
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
	// failure, or a hold, must not leave a closed question waiting to be pushed.
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
func Respond(request Request, delivery Delivery, overdue bool, named Naming) (Request, Delivery, error) {
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
	if !Final(delivery.State) && !Visible(delivery, named) {
		return request, delivery, ErrNotVisible
	}
	next, err := Apply(delivery, Event{Kind: AckActed, Named: named})
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

// Notice transitions (section 8.5): owed → sending (a native push claims it) → shown, or back to
// owed on a confirmed failure; owed → shown directly when a hook or chat.inbox includes it.

// ShowNotice marks an owed notice shown by a hook or chat.inbox, in the step that returns it.
func ShowNotice(notice Notice) (Notice, error) {
	if notice != NoticeOwed {
		return notice, ErrNotAllowed
	}
	return NoticeShown, nil
}

// ClaimNotice marks an owed notice `sending` before a native push carries it.
func ClaimNotice(notice Notice) (Notice, error) {
	if notice != NoticeOwed {
		return notice, ErrNotAllowed
	}
	return NoticeSending, nil
}

// SettleNotice records a native push's outcome for a `sending` notice: a confirmed failure returns
// it to owed; any answer, a lost one, or 60 s without an outcome makes it shown, never pushed again.
func SettleNotice(notice Notice, confirmedFailure bool) (Notice, error) {
	if notice != NoticeSending {
		return notice, ErrNotAllowed
	}
	if confirmedFailure {
		return NoticeOwed, nil
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
	case delivery.State == Pending || delivery.State == Sending || delivery.State == Held:
		return true
	case delivery.State == Delivered && (delivery.Reason == Unconfirmed || !whole):
		return true
	case delivery.State == Delivered && (delivery.Route == Hooks || delivery.Route == Bridge):
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
