// Package chatevent publishes jackstay's own events into the agent chat (docs/design/chat.md
// section 10). A producer such as `jst review` builds an Event and gives it to the Publisher; the
// chat registers the Publisher that stores and delivers it. Without a registered chat, Publish
// answers ErrUnavailable and the producer keeps what it owes.
package chatevent

import (
	"context"
	"errors"
	"sync"
)

// The kinds of event in 0.6.0 (docs/design/chat.md section 10).
const (
	KindReviewFinished = "review.finished"
	KindGatePassed     = "gate.passed"
	KindGateFailed     = "gate.failed"
	KindLandMerged     = "land.merged"
	KindBriefStale     = "brief.stale"
)

// Bounds of an event as a message.
const (
	MaxSummaryRunes = 200
	MaxBodyBytes    = 4 << 10
)

// Attachment points at something jackstay keeps, such as jst:review/<task>/<round>.
type Attachment struct {
	URI   string `json:"uri"`
	Title string `json:"title,omitempty"`
}

// Event is one thing that happened, worth telling a session about.
type Event struct {
	Kind        string            `json:"kind"`
	Worktree    string            `json:"worktree"`
	Summary     string            `json:"summary"`
	Body        string            `json:"body,omitempty"`
	Origin      int               `json:"origin,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
	Recipients  []string          `json:"recipients,omitempty"`
	Key         string            `json:"key"`
}

// Publisher stores events. Publish returns an error when the event was not stored; the producer
// decides whether to keep it owed, and never fails its own command because of it.
type Publisher interface {
	Publish(ctx context.Context, event Event) error
}

// ErrUnavailable is the answer when no chat is registered in this binary.
var ErrUnavailable = errors.New("no chat is available to publish to")

var (
	guard      sync.Mutex
	registered Publisher
)

// Register makes publisher the one Default returns. The chat calls it from its package's init.
func Register(publisher Publisher) {
	guard.Lock()
	defer guard.Unlock()
	registered = publisher
}

// Default is the registered Publisher, or one that answers ErrUnavailable.
func Default() Publisher {
	guard.Lock()
	defer guard.Unlock()
	if registered == nil {
		return unavailable{}
	}
	return registered
}

type unavailable struct{}

func (unavailable) Publish(context.Context, Event) error { return ErrUnavailable }

// Summary cuts text to one line of at most MaxSummaryRunes code points.
func Summary(text string) string {
	runes := []rune(text)
	for index, value := range runes {
		if value == '\n' || value == '\r' {
			runes = runes[:index]
			break
		}
	}
	if len(runes) > MaxSummaryRunes {
		runes = runes[:MaxSummaryRunes]
	}
	return string(runes)
}

// Identifier finds the chat session of the calling process in worktree: "" when it has none.
type Identifier func(ctx context.Context, worktree string) (string, error)

var identifier Identifier

// RegisterIdentifier makes find the one Identify uses. The chat calls it from its package's init.
func RegisterIdentifier(find Identifier) {
	guard.Lock()
	defer guard.Unlock()
	identifier = find
}

// Identify is the chat session of the calling process in worktree, "" without a chat or a session.
func Identify(ctx context.Context, worktree string) (string, error) {
	guard.Lock()
	find := identifier
	guard.Unlock()
	if find == nil {
		return "", nil
	}
	return find(ctx, worktree)
}
