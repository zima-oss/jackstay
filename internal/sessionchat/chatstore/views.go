package chatstore

import (
	"encoding/json"
	"strings"
)

// The shapes the chat tools return (section 5.2). A send is checked against the rendering of its
// message without the body (section 6.4), so every result page can hold one message and advance.

// SessionRef is how a result names a session.
type SessionRef struct {
	Address    string `json:"address"`
	Name       string `json:"name"`
	Ref        string `json:"ref"`
	Engine     string `json:"engine"`
	Repository string `json:"repository,omitempty"`
}

// RequestView is a request as a reader sees it.
type RequestView struct {
	RequestID string   `json:"request_id"`
	Expects   string   `json:"expects"`
	Options   []string `json:"options,omitempty"`
	State     string   `json:"state"`
}

// MessageView is one message in a result.
type MessageView struct {
	ID          string            `json:"id"`
	ThreadID    string            `json:"thread_id"`
	ReplyTo     string            `json:"reply_to,omitempty"`
	Kind        string            `json:"kind"`
	Event       string            `json:"event,omitempty"`
	From        SessionRef        `json:"from"`
	To          string            `json:"to"`
	Summary     string            `json:"summary"`
	Message     string            `json:"message"`
	Offset      int               `json:"offset"`
	Complete    bool              `json:"complete"`
	Priority    string            `json:"priority"`
	Created     string            `json:"created"`
	ExpiresAt   string            `json:"expires_at"`
	Request     *RequestView      `json:"request,omitempty"`
	Response    *Response         `json:"response,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
	State       string            `json:"state,omitempty"`
}

// renderedSize is the bytes a message takes in a result without its body, its sender's address and
// repository at their bounds (a name of 64 four-byte characters), so a later rename cannot push it
// past the bound.
func renderedSize(envelope Envelope) int {
	name := strings.Repeat("\U0001D465", 64)
	longest := SessionRef{
		Address: name + " [0123456789]", Name: name,
		Ref: "0123456789", Engine: EngineClaudeCode, Repository: "#" + strings.Repeat("x", 48) + " [0123456789]",
	}
	view := MessageView{
		ID: envelope.ID, ThreadID: envelope.ThreadID, ReplyTo: envelope.ReplyTo, Kind: envelope.Kind,
		Event: envelope.Event, From: longest, To: envelope.To, Summary: envelope.Summary, Complete: true,
		Priority: envelope.Priority, Created: envelope.Created, ExpiresAt: envelope.ExpiresAt,
		Response: envelope.Response, Attachments: envelope.Attachments, Meta: envelope.Meta, State: "delivered",
	}
	if envelope.Request != nil {
		view.Request = &RequestView{RequestID: envelope.Request.RequestID, Expects: envelope.Request.Expects,
			Options: envelope.Request.Options, State: "answered"}
	}
	data, _ := json.Marshal(view)
	return len(data)
}
