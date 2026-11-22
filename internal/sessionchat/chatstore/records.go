package chatstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// The records of section 6, one JSON object per file (section 7.1), each read and written with the
// bound it has as encoded on disk (section 6.4).

// Bounds of the records as encoded.
const (
	envelopeBound = 48 << 10
	deliveryBound = 4 << 10
	commitBound   = 1 << 10
	sessionBound  = 64 << 10
	channelBound  = 16 << 10
	journalBound  = 8 << 10
	// MaxBodyBytes is the largest body, in bytes of UTF-8.
	MaxBodyBytes = 32 << 10
	// renderedBound is a message rendered without its body (section 5.2): at most 12 KiB.
	renderedBound = 12 << 10
)

// Engines.
const (
	EngineClaudeCode = "claude-code"
	EngineCodex      = "codex"
	EngineOther      = "other"
	// SystemKey is the sender of jackstay's own events and notices (section 3.1).
	SystemKey = "jackstay"
)

// Key sources.
const (
	SourceEngine    = "engine"
	SourceToken     = "token"
	SourceEphemeral = "ephemeral"
)

// Live states (section 3.3).
const (
	StateBusy    = "busy"
	StateIdle    = "idle"
	StateGone    = "gone"
	StateUnknown = "unknown"
)

// IdleWatcher is one session waiting for this session's next idle or end (section 4.9).
type IdleWatcher struct {
	Key             string `json:"key"`
	Until           string `json:"until"`
	MessageID       string `json:"message_id,omitempty"`
	Armed           bool   `json:"armed"`
	ArmedGeneration int    `json:"armed_generation,omitempty"`
}

// SessionNotice is an idle notice owed to, or shown to, this session (section 4.9).
type SessionNotice struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	About     string `json:"about"`
	MessageID string `json:"message_id,omitempty"`
	At        string `json:"at"`
	State     string `json:"state"`
}

// Session is a session record (section 6.1).
type Session struct {
	V              int           `json:"v"`
	Key            string        `json:"key"`
	Ref            string        `json:"ref"`
	Name           string        `json:"name"`
	Engine         string        `json:"engine"`
	KeySource      string        `json:"key_source"`
	ChannelID      string        `json:"channel_id"`
	Mailbox        bool          `json:"mailbox,omitempty"`
	Worktree       string        `json:"worktree,omitempty"`
	Parent         string        `json:"parent,omitempty"`
	State          string        `json:"state"`
	Since          string        `json:"since"`
	LastSeen       string        `json:"last_seen"`
	Registered     string        `json:"registered"`
	Delivery       string        `json:"delivery,omitempty"`
	Subscriptions  []string      `json:"subscriptions,omitempty"`
	IdleWatchers   []IdleWatcher `json:"idle_watchers,omitempty"`
	IdleGeneration int           `json:"idle_generation,omitempty"`
	// IdleAt is when the idle that advanced idle_generation was observed (section 4.9).
	IdleAt     string          `json:"idle_at,omitempty"`
	Notices    []SessionNotice `json:"notices,omitempty"`
	StopBlocks int             `json:"stop_blocks,omitempty"`
	Exchange   map[string]int  `json:"exchange,omitempty"`
}

// channelRecord is a channel's identity and name (section 7.1). Its subscribers are the sessions
// whose records list it: the session record is the one truth of a subscription.
type channelRecord struct {
	V    int    `json:"v"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Request is a request's shape in its envelope (section 6.3).
type Request struct {
	RequestID string   `json:"request_id"`
	Expects   string   `json:"expects"`
	Options   []string `json:"options,omitempty"`
}

// Response is a response's shape in its envelope.
type Response struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
	Choice    string `json:"choice,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Attachment is a reference a message carries (section 4.7).
type Attachment struct {
	Kind       string `json:"kind"`
	Repository string `json:"repository,omitempty"`
	Path       string `json:"path,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Lines      string `json:"lines,omitempty"`
	URI        string `json:"uri,omitempty"`
	Title      string `json:"title,omitempty"`
}

// Envelope is a message's immutable record (section 6.3).
type Envelope struct {
	V              int               `json:"v"`
	ID             string            `json:"id"`
	ThreadID       string            `json:"thread_id"`
	ReplyTo        string            `json:"reply_to,omitempty"`
	Kind           string            `json:"kind"`
	Event          string            `json:"event,omitempty"`
	From           string            `json:"from"`
	FromName       string            `json:"from_name,omitempty"`
	FromChannelID  string            `json:"from_channel_id,omitempty"`
	To             string            `json:"to"`
	ToChannelID    string            `json:"to_channel_id,omitempty"`
	Summary        string            `json:"summary"`
	Priority       string            `json:"priority"`
	Created        string            `json:"created"`
	ExpiresAt      string            `json:"expires_at"`
	BodyBytes      int               `json:"body_bytes"`
	BodySHA256     string            `json:"body_sha256"`
	Recipients     []string          `json:"recipients"`
	Request        *Request          `json:"request,omitempty"`
	Response       *Response         `json:"response,omitempty"`
	Attachments    []Attachment      `json:"attachments,omitempty"`
	Meta           map[string]string `json:"meta,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

// Delivery is one recipient's delivery record (section 6.3).
type Delivery struct {
	V           int    `json:"v"`
	ID          string `json:"id"`
	Key         string `json:"key"`
	Route       string `json:"route"`
	State       string `json:"state"`
	At          string `json:"at"`
	Reason      string `json:"reason,omitempty"`
	Pusher      string `json:"pusher,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	Whole       bool   `json:"whole,omitempty"`
	NextAttempt string `json:"next_attempt,omitempty"`
	Notice      string `json:"notice,omitempty"`
	Note        string `json:"note,omitempty"`
}

// Commit is a message's commit record (section 6.3).
type Commit struct {
	V            int    `json:"v"`
	ID           string `json:"id"`
	Committed    string `json:"committed"`
	RequestState string `json:"request_state,omitempty"`
	ResponseID   string `json:"response_id,omitempty"`
	Closed       string `json:"closed,omitempty"`
	Notice       string `json:"notice,omitempty"`
}

// table is the part of a delivery record the transition table reads and writes.
func (delivery Delivery) table(request bool) deliverystate.Delivery {
	return deliverystate.Delivery{
		Route: deliverystate.Route(delivery.Route), State: deliverystate.State(delivery.State),
		Reason: deliverystate.Reason(delivery.Reason), Pusher: delivery.Pusher, Attempts: delivery.Attempts,
		Notice: deliverystate.Notice(delivery.Notice), Request: request,
	}
}

// withTable is the record after the table moved it; At changes only with the state.
func (delivery Delivery) withTable(next deliverystate.Delivery, moment string) Delivery {
	if string(next.State) != delivery.State {
		delivery.At = moment
	}
	delivery.Route, delivery.State, delivery.Reason = string(next.Route), string(next.State), string(next.Reason)
	delivery.Pusher, delivery.Attempts, delivery.Notice = next.Pusher, next.Attempts, string(next.Notice)
	return delivery
}

// request is the part of a commit record the transition table reads and writes.
func (commit Commit) request() deliverystate.Request {
	return deliverystate.Request{State: deliverystate.RequestState(commit.RequestState), Notice: deliverystate.Notice(commit.Notice)}
}

// layout is the store's folders and files (section 7.1).
type layout struct{ root string }

func (place layout) lock() string     { return filepath.Join(place.root, "lock") }
func (place layout) journal() string  { return filepath.Join(place.root, "journal") }
func (place layout) broken() string   { return filepath.Join(place.root, "journal", "broken") }
func (place layout) sessions() string { return filepath.Join(place.root, "sessions") }
func (place layout) session(hash string) string {
	return filepath.Join(place.root, "sessions", hash+".json")
}
func (place layout) channels() string { return filepath.Join(place.root, "channels") }
func (place layout) channel(id string) string {
	return filepath.Join(place.root, "channels", id+".json")
}
func (place layout) messages() string { return filepath.Join(place.root, "messages") }
func (place layout) envelope(id string) string {
	return filepath.Join(place.root, "messages", id+".json")
}
func (place layout) bodies() string                { return filepath.Join(place.root, "bodies") }
func (place layout) body(id string) string         { return filepath.Join(place.root, "bodies", id+".txt") }
func (place layout) deliveries(id string) string   { return filepath.Join(place.root, "deliveries", id) }
func (place layout) commits() string               { return filepath.Join(place.root, "commits") }
func (place layout) commit(id string) string       { return filepath.Join(place.root, "commits", id+".json") }
func (place layout) inbox(hash string) string      { return filepath.Join(place.root, "inbox", hash) }
func (place layout) outbox(hash string) string     { return filepath.Join(place.root, "outbox", hash) }
func (place layout) idempotency() string           { return filepath.Join(place.root, "idempotency") }
func (place layout) requests() string              { return filepath.Join(place.root, "requests") }
func (place layout) requestIndex(id string) string { return filepath.Join(place.root, "requests", id) }
func (place layout) delivery(id, hash string) string {
	return filepath.Join(place.root, "deliveries", id, hash+".json")
}
func (place layout) idempotencyEntry(sender, key string) string {
	return filepath.Join(place.root, "idempotency", hashOf(sender, key))
}

// readRecord reads a record within its bound; a missing file is os.ErrNotExist.
func readRecord(path string, bound int, record any) error {
	data, err := durable.ReadLimited(path, bound)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, record); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// encodeRecord encodes a record, refusing one past its bound (errTooLarge).
func encodeRecord(record any, bound int) ([]byte, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(data) > bound {
		return nil, fmt.Errorf("%w: %d bytes past %d", errTooLarge, len(data), bound)
	}
	return data, nil
}

// writeRecord replaces a record whole, within its bound, making its folder when missing.
func writeRecord(path string, record any, bound int) error {
	data, err := encodeRecord(record, bound)
	if err != nil {
		return err
	}
	if err := makeFolder(filepath.Dir(path)); err != nil {
		return err
	}
	return durable.WriteAtomic(path, data, 0o600, bound)
}

var errTooLarge = errors.New("too large")

// madeFolders are the folders this process made, or found, and fsynced up to the root: making one
// costs a fsync of every folder above it, which each write would otherwise repeat.
var madeFolders sync.Map

// makeFolder is durable.MakeFolder once per folder and process.
func makeFolder(folder string) error {
	if _, done := madeFolders.Load(folder); done {
		if info, err := os.Stat(folder); err == nil && info.IsDir() {
			return nil
		}
	}
	if err := durable.MakeFolder(folder, 0o700); err != nil {
		return err
	}
	madeFolders.Store(folder, true)
	return nil
}

// createExclusive makes an empty marker, or an entry holding data, under a name that must not
// exist yet, and fsyncs its folder (section 7.2). An existing name is os.ErrExist.
func createExclusive(path string, data []byte) error {
	if err := makeFolder(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- the store's own file
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(path)
		return err
	}
	return durable.SyncFolder(filepath.Dir(path))
}

// stamp is the store's time format: RFC 3339 in UTC with milliseconds (section 6).
func stamp(moment time.Time) string { return moment.UTC().Format("2006-01-02T15:04:05.000Z") }

// parseStamp reads a time written by stamp.
func parseStamp(text string) (time.Time, bool) {
	moment, err := time.Parse(time.RFC3339Nano, text)
	return moment, err == nil
}
