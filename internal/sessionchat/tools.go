package sessionchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

// toolTimeout bounds one tool call, the binding's wait included.
const toolTimeout = 10 * time.Second

// toolDeadline is the server's bound for one tool call: toolTimeout unless a test gave its own.
func (server *Server) toolDeadline() time.Duration {
	if server.toolBudget > 0 {
		return server.toolBudget
	}
	return toolTimeout
}

type tool struct {
	name, description string
	properties        map[string]any
	required          []string
	readOnly          bool
}

func stringField(maximum int) map[string]any {
	return map[string]any{"type": "string", "maxLength": maximum}
}
func booleanField() map[string]any { return map[string]any{"type": "boolean"} }
func integerField(minimum, maximum int) map[string]any {
	return map[string]any{"type": "integer", "minimum": minimum, "maximum": maximum}
}
func listField(items map[string]any, maximum int) map[string]any {
	return map[string]any{"type": "array", "items": items, "maxItems": maximum}
}
func objectField() map[string]any { return map[string]any{"type": "object"} }

// tools are the tools of section 5.2, the 0.5.x names of section 5.5, and the review tools.
var tools = []tool{
	{"chat.whoami", "This session's identity; with name, renames it", map[string]any{"name": stringField(64)}, nil, false},
	{"chat.sessions", "Who can be addressed", map[string]any{"channel": stringField(140), "limit": integerField(1, 100)}, nil, true},
	{"chat.channels", "The known channels", map[string]any{"cursor": stringField(512)}, nil, true},
	{"chat.subscribe", "Subscribe to or leave one channel", map[string]any{"channel": stringField(140), "subscribed": booleanField()}, []string{"channel", "subscribed"}, false},
	{"chat.send", "Send one message, request, reply or idle subscription; retry with the same idempotency_key", map[string]any{
		"to": stringField(140), "message": stringField(32768), "summary": stringField(200), "notify_when_idle": booleanField(),
		"thread_id": stringField(64), "reply_to": stringField(64), "request": objectField(),
		"priority":           map[string]any{"type": "string", "enum": []string{chatstore.PriorityNow, chatstore.PriorityNext, chatstore.PriorityLater}},
		"expires_in_seconds": integerField(60, 604800), "attachments": listField(objectField(), 8), "meta": objectField(),
		"idempotency_key": stringField(128), "body": stringField(32768), "kind": map[string]any{"type": "string", "enum": []string{"message", "request"}}},
		[]string{"to"}, false},
	{"chat.inbox", "Read messages addressed to this session, and the notices owed to it", map[string]any{
		"ids": listField(stringField(64), 50), "unread_only": booleanField(), "limit": integerField(1, 50),
		"cursor": stringField(512), "mark_read": booleanField()}, nil, false},
	{"chat.ack", "Declare a message read or handled", map[string]any{"message_id": stringField(64),
		"state": map[string]any{"type": "string", "enum": []string{"read", "acted"}}, "note": stringField(200)}, []string{"message_id"}, false},
	{"chat.respond", "Answer a request addressed to this session", map[string]any{"request_id": stringField(32),
		"decision": map[string]any{"type": "string", "enum": []string{"approve", "reject", "answer"}}, "choice": stringField(64),
		"reason": stringField(2000), "message": stringField(32768), "attachments": listField(objectField(), 8), "meta": objectField(),
		"idempotency_key": stringField(128)}, []string{"request_id", "decision"}, false},
	{"chat.outbox", "What this session sent, and what became of it", map[string]any{"message_id": stringField(64),
		"states": listField(stringField(16), 8), "request_states": listField(stringField(16), 4), "thread_id": stringField(64),
		"limit": integerField(1, 50), "cursor": stringField(512)}, nil, true},
	{"chat.thread", "A thread, or a channel's history, as this session may see it", map[string]any{"thread_id": stringField(64),
		"channel": stringField(140), "limit": integerField(1, 50), "cursor": stringField(512)}, nil, false},
	{"chat.self", "The same as chat.whoami (0.5.x name)", map[string]any{}, nil, true},
	{"chat.reply", "Reply to a message addressed to this session (0.5.x name; chat.send with reply_to)", map[string]any{
		"message_id": stringField(64), "body": stringField(32768), "idempotency_key": stringField(128)}, []string{"message_id", "body"}, false},
	{"review.submit", "Start `jst review BASE..HEAD` in the background on pinned commits of this repository; the result comes to this session as a review.finished chat event. An equal review already running is joined", map[string]any{
		"base": stringField(200), "head": stringField(200), "task": stringField(200),
		"reviewers": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"codex", "claude-code"}}, "minItems": 1, "maxItems": 2}},
		[]string{"base", "head", "task", "reviewers"}, false},
	{"review.status", "Where a task's background reviews in this repository stand: the one running, and how the latest ended (outcome, round, report)", map[string]any{"task": stringField(200)}, []string{"task"}, true},
}

func toolDefinitions() []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, entry := range tools {
		required := append([]string{}, entry.required...)
		result = append(result, map[string]any{"name": entry.name, "description": entry.description,
			"inputSchema": map[string]any{"type": "object", "properties": entry.properties, "required": required, "additionalProperties": false},
			"annotations": map[string]any{"readOnlyHint": entry.readOnly, "openWorldHint": false}})
	}
	return result
}

// arguments are every tool's arguments; strictjson refuses a member no tool has, and each tool
// takes only its own (allowedArguments).
type arguments struct {
	Name             string                 `json:"name"`
	Channel          string                 `json:"channel"`
	Limit            int                    `json:"limit"`
	Cursor           string                 `json:"cursor"`
	Subscribed       *bool                  `json:"subscribed"`
	To               string                 `json:"to"`
	Message          string                 `json:"message"`
	Summary          string                 `json:"summary"`
	NotifyWhenIdle   bool                   `json:"notify_when_idle"`
	ThreadID         string                 `json:"thread_id"`
	ReplyTo          string                 `json:"reply_to"`
	Request          *requestArguments      `json:"request"`
	Priority         string                 `json:"priority"`
	ExpiresInSeconds int                    `json:"expires_in_seconds"`
	Attachments      []chatstore.Attachment `json:"attachments"`
	Meta             map[string]string      `json:"meta"`
	IdempotencyKey   string                 `json:"idempotency_key"`
	Body             string                 `json:"body"`
	Kind             string                 `json:"kind"`
	IDs              []string               `json:"ids"`
	UnreadOnly       *bool                  `json:"unread_only"`
	MarkRead         *bool                  `json:"mark_read"`
	MessageID        string                 `json:"message_id"`
	State            string                 `json:"state"`
	Note             string                 `json:"note"`
	RequestID        string                 `json:"request_id"`
	Decision         string                 `json:"decision"`
	Choice           string                 `json:"choice"`
	Reason           string                 `json:"reason"`
	States           []string               `json:"states"`
	RequestStates    []string               `json:"request_states"`
	Base             string                 `json:"base"`
	Head             string                 `json:"head"`
	Task             string                 `json:"task"`
	Reviewers        []string               `json:"reviewers"`
}

type requestArguments struct {
	Expects string   `json:"expects"`
	Options []string `json:"options"`
}

func (server *Server) call(raw json.RawMessage) any {
	var envelope struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Metadata  json.RawMessage `json:"_meta"` // transport metadata, never tool arguments
	}
	if strictjson.Unmarshal(raw, &envelope) != nil {
		return toolFailure("invalid tool call")
	}
	var known *tool
	for index := range tools {
		if tools[index].name == envelope.Name {
			known = &tools[index]
		}
	}
	if known == nil {
		return toolFailure("unknown tool")
	}
	var input arguments
	if given := bytes.TrimSpace(envelope.Arguments); len(given) > 0 && !bytes.Equal(given, []byte("null")) {
		if strictjson.Unmarshal(envelope.Arguments, &input) != nil {
			return refusalResult(&chatstore.Refusal{Reason: chatstore.ReasonInvalid, Detail: "arguments do not match the tool's schema"})
		}
		members, err := strictjson.Members(envelope.Arguments)
		if err != nil {
			return toolFailure("invalid arguments")
		}
		for _, member := range members {
			if _, allowed := known.properties[member.Name]; !allowed {
				return refusalResult(&chatstore.Refusal{Reason: chatstore.ReasonInvalid, Detail: member.Name + " is not an argument of " + known.name})
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), server.toolDeadline())
	defer cancel()
	session, err := server.session(ctx)
	if err != nil {
		return refusalResult(err)
	}
	result, err := server.run(ctx, known.name, session, input)
	if err != nil {
		return refusalResult(err)
	}
	switch known.name {
	case "chat.send", "chat.reply", "chat.respond":
		// A delivery to an idle native session is rung on its socket (section 8.3).
		startPusher(ctx, server.chat.store)
	case "chat.inbox":
		page := result.(chatstore.InboxResult)
		return framedResult(page.Messages, page.Notices, map[string]any{"notices_more": page.NoticesMore,
			"more": page.More, "next_cursor": page.NextCursor})
	case "chat.thread":
		page := result.(chatstore.ThreadResult)
		return framedResult(page.Messages, nil, map[string]any{"next_cursor": page.NextCursor})
	}
	return toolResult(result)
}

// framedResult is a result that carries other sessions' text (section 5.3), as one text block:
// each message in its frame, once, then the messages' other fields (without their body or summary,
// which the frame holds), the notices and the paging. It has no second, structured copy.
func framedResult(messages []chatstore.MessageView, notices []string, paging map[string]any) map[string]any {
	var text strings.Builder
	fields := make([]chatstore.MessageView, 0, len(messages))
	for _, view := range messages {
		frame, _ := chatstore.Frame(view, view.ID, view.Message, 0)
		text.WriteString(frame + "\n")
		view.Message, view.Summary, view.Attachments, view.Response = "", "", nil, nil
		fields = append(fields, view)
	}
	if len(messages) == 0 {
		text.WriteString("No messages.\n")
	}
	rest := map[string]any{"messages": fields, "notices": notices}
	for name, value := range paging {
		rest[name] = value
	}
	encoded, err := json.Marshal(rest)
	if err != nil {
		return toolFailure("the result could not be encoded")
	}
	text.WriteString("fields: " + string(encoded) + "\n")
	if text.Len() > maxResponseBytes/2 {
		return toolFailure("the result exceeds its size limit; ask for fewer")
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text.String()}}}
}

func (server *Server) run(ctx context.Context, name string, session chatstore.Session, input arguments) (any, error) {
	store := server.chat.store
	key := session.Key
	switch name {
	case "chat.whoami", "chat.self":
		if input.Name != "" {
			renamed, err := store.Rename(ctx, key, input.Name)
			if err != nil {
				return nil, err
			}
			session = renamed
		}
		return whoami(session, server.chat), nil
	case "chat.sessions":
		return sessionsOf(ctx, store, session, input)
	case "chat.channels":
		return channelsOf(ctx, store, session)
	case "chat.subscribe":
		if input.Subscribed == nil {
			return nil, &chatstore.Refusal{Reason: chatstore.ReasonInvalid, Detail: "subscribed is true or false"}
		}
		updated, err := store.Subscribe(ctx, key, input.Channel, *input.Subscribed)
		if err != nil {
			return nil, err
		}
		return map[string]any{"subscriptions": updated.Subscriptions}, nil
	case "chat.send":
		return store.Send(ctx, key, chatstore.Scope{}, sendInput(input))
	case "chat.reply":
		return server.reply(ctx, session, input)
	case "chat.inbox":
		limit := input.Limit
		if limit == 0 {
			limit = 20
		}
		if here, err := os.Getwd(); err == nil {
			pullOwedReviews(ctx, store, key, here)
		}
		result, err := store.Inbox(ctx, key, chatstore.InboxInput{IDs: input.IDs, UnreadOnly: defaultTrue(input.UnreadOnly),
			Limit: limit, Cursor: input.Cursor, MarkRead: defaultTrue(input.MarkRead)})
		if err != nil {
			return nil, err
		}
		if refs := result.NoticeRefs; len(refs) > 0 {
			// Marked once the response is written (section 8.5): a stop before that shows them again.
			server.afterWrite = func() {
				marking, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = store.MarkShown(marking, key, refs)
			}
		}
		return result, nil
	case "chat.ack":
		state := input.State
		if state == "" {
			state = "acted"
		}
		return store.Ack(ctx, key, input.MessageID, state, input.Note)
	case "chat.respond":
		return store.Respond(ctx, key, chatstore.RespondInput{RequestID: input.RequestID, Decision: input.Decision,
			Choice: input.Choice, Reason: input.Reason, Message: input.Message, Attachments: input.Attachments,
			Meta: input.Meta, IdempotencyKey: input.IdempotencyKey})
	case "chat.outbox":
		return store.Outbox(ctx, key, chatstore.OutboxInput{MessageID: input.MessageID, States: input.States,
			RequestStates: input.RequestStates, ThreadID: input.ThreadID, Limit: input.Limit, Cursor: input.Cursor})
	case "chat.thread":
		return store.Thread(ctx, key, chatstore.ThreadInput{ThreadID: input.ThreadID, Channel: input.Channel,
			Limit: input.Limit, Cursor: input.Cursor})
	case "review.submit":
		return server.submitReview(ctx, key, input)
	case "review.status":
		return server.reviewStatus(input)
	}
	return nil, errors.New("unknown tool")
}

func defaultTrue(value *bool) bool { return value == nil || *value }

// sendInput is chat.send's input, with the 0.5.x names of section 5.5: body for message, and kind
// request for a request expecting an answer.
func sendInput(input arguments) chatstore.SendInput {
	message := input.Message
	if message == "" {
		message = input.Body
	}
	send := chatstore.SendInput{To: input.To, Message: message, Summary: input.Summary, NotifyWhenIdle: input.NotifyWhenIdle,
		ThreadID: input.ThreadID, ReplyTo: input.ReplyTo, Priority: input.Priority, ExpiresInSeconds: input.ExpiresInSeconds,
		Attachments: input.Attachments, Meta: input.Meta, IdempotencyKey: input.IdempotencyKey}
	switch {
	case input.Request != nil:
		send.Request = &chatstore.RequestInput{Expects: input.Request.Expects, Options: input.Request.Options}
	case input.Kind == "request":
		send.Request = &chatstore.RequestInput{Expects: "answer"}
	}
	return send
}

// reply is chat.reply (section 5.5): chat.send to the sender of message_id, as a reply to it.
func (server *Server) reply(ctx context.Context, session chatstore.Session, input arguments) (any, error) {
	found, err := server.chat.store.Inbox(ctx, session.Key, chatstore.InboxInput{IDs: []string{input.MessageID}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(found.Messages) == 0 {
		return nil, &chatstore.Refusal{Reason: chatstore.ReasonUnknownMessage, Detail: "no message " + input.MessageID + " addressed to this session"}
	}
	return server.chat.store.Send(ctx, session.Key, chatstore.Scope{}, chatstore.SendInput{To: found.Messages[0].From.Address,
		Message: input.Body, ReplyTo: input.MessageID, IdempotencyKey: input.IdempotencyKey})
}

// whoami is chat.whoami's answer (section 5.2).
func whoami(session chatstore.Session, chat local) map[string]any {
	answer := map[string]any{
		"address": session.Name + " [" + session.Ref + "]", "name": session.Name, "ref": session.Ref,
		"engine": session.Engine, "key_source": session.KeySource, "repository": chat.channel.Name,
		"channel": "#" + chat.channel.Name, "subscriptions": session.Subscriptions, "delivery": session.Delivery,
		"state": session.State, "notices": session.Notices,
	}
	if session.Parent != "" {
		answer["parent"] = session.Parent
	}
	return answer
}

// sessionsOf is chat.sessions: the sessions of one channel (this session's by default).
func sessionsOf(ctx context.Context, store *chatstore.Store, self chatstore.Session, input arguments) (any, error) {
	sessions, err := store.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	channelID := self.ChannelID
	if input.Channel != "" {
		channels, err := store.Channels(ctx)
		if err != nil {
			return nil, err
		}
		channel, err := chatstore.ResolveChannel(input.Channel, channels)
		if err != nil {
			return nil, err
		}
		channelID = channel.ID
	}
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}
	var rows []map[string]any
	for _, session := range sessions {
		if session.ChannelID != channelID {
			continue
		}
		if len(rows) == limit {
			break
		}
		rows = append(rows, map[string]any{"address": session.Name + " [" + session.Ref + "]", "name": session.Name,
			"ref": session.Ref, "engine": session.Engine, "state": session.State, "since": session.Since,
			"last_seen": session.LastSeen, "delivery": session.Delivery,
			"ephemeral": session.KeySource == chatstore.SourceEphemeral, "reachable": true})
	}
	return map[string]any{"sessions": rows}, nil
}

// channelsOf is chat.channels.
func channelsOf(ctx context.Context, store *chatstore.Store, self chatstore.Session) (any, error) {
	channels, err := store.Channels(ctx)
	if err != nil {
		return nil, err
	}
	sessions, err := store.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	for _, channel := range channels {
		subscribers := 0
		for _, session := range sessions {
			for _, subscription := range session.Subscriptions {
				if subscription == channel.ID {
					subscribers++
				}
			}
		}
		subscribed := false
		for _, subscription := range self.Subscriptions {
			subscribed = subscribed || subscription == channel.ID
		}
		rows = append(rows, map[string]any{"channel": chatstore.ChannelAddress(channel, channels), "subscribers": subscribers,
			"subscribed": subscribed, "writable": true})
	}
	return map[string]any{"channels": rows}, nil
}

// submitReview is review.submit: a background review whose result comes to this session.
func (server *Server) submitReview(ctx context.Context, key string, input arguments) (any, error) {
	here, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.Base) == "" || strings.TrimSpace(input.Head) == "" ||
		strings.TrimSpace(input.Task) == "" || len(input.Reviewers) == 0 || len(input.Reviewers) > 2 {
		return nil, errors.New("review.submit needs base, head, task and one or two reviewers")
	}
	background, err := reviewBackground(here)
	if err != nil {
		return nil, err
	}
	if err := server.sameRepository(ctx, background.RepositoryRoot); err != nil {
		return nil, err
	}
	submitted, err := background.Submit(ctx, review.Submission{Revision: input.Base + ".." + input.Head,
		Task: input.Task, Reviewers: input.Reviewers, Subscriber: key})
	if err != nil {
		return nil, err
	}
	return map[string]any{"request": publicRequest(submitted.Request), "joined": submitted.Joined}, nil
}

func (server *Server) reviewStatus(input arguments) (any, error) {
	here, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.Task) == "" {
		return nil, errors.New("review.status needs a task")
	}
	ctx, cancel := context.WithTimeout(context.Background(), server.toolDeadline())
	defer cancel()
	background, err := reviewBackground(here)
	if err != nil {
		return nil, err
	}
	if err := server.sameRepository(ctx, background.RepositoryRoot); err != nil {
		return nil, err
	}
	status, err := background.StatusOf(input.Task)
	if err != nil {
		return nil, err
	}
	return publicStatus(status), nil
}

// sameRepository refuses a review whose project root is in another repository than the chat's
// session: a nested repository without a jackstay.json of its own would review its parent.
func (server *Server) sameRepository(ctx context.Context, root string) error {
	common, err := (gitcli.Git{Dir: root}).CommonDir(ctx)
	if err != nil {
		return err
	}
	channel, err := chatstore.RepositoryChannel(common, "")
	if err != nil {
		return err
	}
	if channel.ID != server.chat.channel.ID {
		return errors.New("the review's project is in another repository than this chat session; open the chat in that repository")
	}
	return nil
}

// publicStatus is a task's status without the sessions subscribed to its requests.
func publicStatus(status review.Status) map[string]any {
	answer := map[string]any{"report": status.Report}
	if status.Running != nil {
		answer["running"] = publicRequest(*status.Running)
	}
	if last := status.Last; last != nil {
		answer["last"] = map[string]any{"request": publicRequest(last.Request), "lost": last.Lost, "status": last.Status,
			"outcome": last.Outcome, "next": last.Next, "round": last.Round, "folder": last.Folder, "ended": last.Ended}
	}
	return answer
}

// publicRequest leaves out the other sessions subscribed to a request.
func publicRequest(request review.Request) map[string]any {
	return map[string]any{"id": request.ID, "task": request.Task, "base": request.Base, "head": request.Head,
		"reviewers": request.Reviewers, "running": request.Running, "created": request.Created, "log": request.Log}
}

// reviewBackground submits reviews with this binary; tests replace it.
var reviewBackground = func(folder string) (review.Background, error) {
	executable, err := os.Executable()
	if err != nil {
		return review.Background{}, err
	}
	return review.BackgroundAt(folder, executable)
}
