package chatstore

import (
	"context"
	"errors"
	"os"
	"slices"
	"unicode/utf8"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// RespondInput is chat.respond's input after its schema (section 5.2).
type RespondInput struct {
	RequestID      string
	Decision       string
	Choice         string
	Reason         string
	Message        string
	Attachments    []Attachment
	Meta           map[string]string
	IdempotencyKey string
}

// Respond answers a request addressed to the session of responderKey (section 4.4): the response is
// a message to the requester in the request's thread, and in one commit the request becomes
// answered and its delivery acted, unless that is final. The related list never stops it.
func (store *Store) Respond(ctx context.Context, responderKey string, input RespondInput) (SendResult, error) {
	if err := checkRespondInput(input); err != nil {
		return SendResult{}, err
	}
	if !requestIDPattern.MatchString(input.RequestID) {
		return SendResult{}, refuse(ReasonInvalid, "request_id is req_ and 26 characters")
	}
	var result SendResult
	err := store.update(ctx, func(transaction *transaction) error {
		responder, err := transaction.sessionOf(responderKey)
		if err != nil {
			return err
		}
		if duplicate, found, err := transaction.duplicateOf(responderKey, input.IdempotencyKey); found || err != nil {
			result = duplicate
			return err
		}
		envelope, commit, delivery, err := transaction.requestOf(responder, input.RequestID)
		if err != nil {
			return err
		}
		if err := transaction.admitResponse(envelope, &commit, &delivery, hashKey(responder.Key)); err != nil {
			return err
		}
		if err := checkDecision(*envelope.Request, input); err != nil {
			return err
		}
		sessions, err := transaction.listSessions()
		if err != nil {
			return err
		}
		requester := Session{Key: envelope.From, Name: envelope.FromName, ChannelID: envelope.FromChannelID, State: StateGone}
		requesterHash := hashKey(envelope.From)
		for _, session := range sessions {
			if session.Key == envelope.From {
				requester = session
			}
		}
		if requester.Ref == "" {
			requester.Ref = requesterHash[:6]
		}
		summary := summaryOf("", input.Message)
		if summary == "" {
			summary = summaryOf("", input.Decision+": "+input.Reason)
		}
		plan := planned{
			sender: responder, recipients: []Session{requester}, gone: map[string]bool{requesterHash: requester.State == StateGone},
			kind: "response", body: input.Message, summary: trimmed(summary), priority: PriorityNext,
			expiry: DefaultExpirySeconds, threadID: envelope.ThreadID, replyTo: envelope.ID, to: fullAddress(requester),
			response:    &Response{RequestID: input.RequestID, Decision: input.Decision, Choice: input.Choice, Reason: input.Reason},
			attachments: input.Attachments, meta: input.Meta, idempotencyKey: input.IdempotencyKey, sessions: sessions,
			answers: envelope.ID, responder: hashKey(responder.Key),
		}
		if err := transaction.checkLimits(plan, true); err != nil {
			return err
		}
		result, err = transaction.commitMessage(plan)
		return err
	})
	return result, err
}

func checkRespondInput(input RespondInput) error {
	if err := checkBody(input.Message); err != nil {
		return err
	}
	if utf8.RuneCountInString(input.Reason) > 2000 || !utf8.ValidString(input.Reason) {
		return refuse(ReasonInvalid, "a reason is at most 2,000 characters")
	}
	if !oneLine(input.Choice, 64) {
		return refuse(ReasonInvalid, "a choice is at most 64 characters on one line")
	}
	if err := checkAttachments(input.Attachments); err != nil {
		return err
	}
	if err := checkMeta(input.Meta); err != nil {
		return err
	}
	return checkIdempotencyKey(input.IdempotencyKey)
}

// requestOf is a committed request, found by its own ID, and the responder's delivery of it. The
// request ID is evidence of a shown frame (section 4.10), so it names a native delivery whose body
// is not yet visible.
func (transaction *transaction) requestOf(responder Session, requestID string) (Envelope, Commit, Delivery, error) {
	var envelope Envelope
	var commit Commit
	var delivery Delivery
	unknown := refuse(ReasonUnknownMessage, "no request %s", requestID)
	data, err := durable.ReadLimited(transaction.place.requestIndex(requestID), 64)
	if errors.Is(err, os.ErrNotExist) {
		return envelope, commit, delivery, unknown
	}
	if err != nil {
		return envelope, commit, delivery, err
	}
	id := string(data)
	if !messageIDPattern.MatchString(id) {
		return envelope, commit, delivery, unknown
	}
	if err := readRecord(transaction.place.commit(id), commitBound, &commit); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return envelope, commit, delivery, unknown
		}
		return envelope, commit, delivery, err
	}
	if err := readRecord(transaction.place.envelope(id), envelopeBound, &envelope); err != nil {
		return envelope, commit, delivery, err
	}
	if envelope.Request == nil || envelope.Request.RequestID != requestID {
		return envelope, commit, delivery, unknown
	}
	hash := hashKey(responder.Key)
	if !slices.Contains(envelope.Recipients, hash) {
		return envelope, commit, delivery, refuse(ReasonNotRecipient, "only the session the request was sent to may answer it")
	}
	err = readRecord(transaction.place.delivery(id, hash), deliveryBound, &delivery)
	return envelope, commit, delivery, err
}

// admitResponse applies the table's rule for an answer (deliverystate.Respond): a second answer is
// refused, and one past the deadline closes the request with its delivery and is refused. A request
// closed by a crash between its two writes gets its delivery closed first.
func (transaction *transaction) admitResponse(envelope Envelope, commit *Commit, delivery *Delivery, hash string) error {
	moment := stamp(transaction.now)
	request := commit.request()
	table := delivery.table(true)
	if request.State == deliverystate.Closed && deliverystate.Expirable(table.State) {
		repaired, err := deliverystate.Apply(table, deliverystate.Event{Kind: deliverystate.CloseRequest})
		if err == nil {
			if err := transaction.writeDelivery(envelope.ID, hash, delivery.withTable(repaired, moment)); err != nil {
				return err
			}
		}
	}
	deadline, _ := parseStamp(envelope.ExpiresAt)
	overdue := !transaction.now.Before(deadline)
	nextRequest, nextDelivery, err := deliverystate.Respond(request, table, overdue)
	switch {
	case errors.Is(err, deliverystate.ErrAlreadyAnswered):
		return refuse(ReasonAlreadyAnswered, "%s is already answered", envelope.Request.RequestID)
	case errors.Is(err, deliverystate.ErrRequestClosed):
		if nextRequest.State != request.State {
			if err := transaction.closeRequest(envelope.ID, commit, delivery, hash, nextRequest, nextDelivery); err != nil {
				return err
			}
		}
		return refuse(ReasonRequestClosed, "%s closed before it was answered", envelope.Request.RequestID)
	case err != nil:
		return refuse(ReasonUnknownMessage, "no request %s that this session can answer now", envelope.Request.RequestID)
	}
	return nil
}

// closeRequest writes a request's closure: its commit record first, then its delivery (section 4.4),
// so a crash between them leaves a closed request whose delivery every step expires first.
func (transaction *transaction) closeRequest(id string, commit *Commit, delivery *Delivery, hash string,
	request deliverystate.Request, next deliverystate.Delivery) error {
	moment := stamp(transaction.now)
	commit.RequestState, commit.Notice, commit.Closed = string(request.State), string(request.Notice), moment
	if err := writeRecord(transaction.place.commit(id), *commit, commitBound); err != nil {
		return err
	}
	*delivery = delivery.withTable(next, moment)
	return transaction.writeDelivery(id, hash, *delivery)
}

// checkDecision is section 5.2's rule: approve or reject for a decision, answer for an answer, and
// a choice among the options when there are some.
func checkDecision(request Request, input RespondInput) error {
	switch input.Decision {
	case "approve", "reject":
		if request.Expects != "decision" {
			return refuse(ReasonInvalid, "this request expects an answer: decision is answer")
		}
		if input.Choice != "" {
			return refuse(ReasonInvalid, "a decision takes no choice")
		}
	case "answer":
		if request.Expects != "answer" {
			return refuse(ReasonInvalid, "this request expects a decision: approve or reject")
		}
		if len(request.Options) > 0 && !slices.Contains(request.Options, input.Choice) {
			return refuse(ReasonInvalid, "choice is one of the request's options")
		}
		if len(request.Options) == 0 && input.Choice != "" {
			return refuse(ReasonInvalid, "this request has no options")
		}
	default:
		return refuse(ReasonInvalid, "decision is approve, reject or answer")
	}
	return nil
}
