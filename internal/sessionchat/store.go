package sessionchat

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/homedir"
)

// Message is one durable direct communication between two registered sessions.
type Message struct {
	ID             string `json:"id"`
	From           string `json:"from"`
	To             string `json:"to"`
	Kind           string `json:"kind"`
	Body           string `json:"body"`
	ReplyTo        string `json:"reply_to,omitempty"`
	Created        string `json:"created"`
	Acknowledged   bool   `json:"acknowledged,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Session is a discoverable local agent connection, identified by an opaque ID.
type Session struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	LastSeen string `json:"last_seen"`
}
type snapshot struct {
	Sessions []Session `json:"sessions"`
	Messages []Message `json:"messages"`
}

// Store holds a repository's private chat state under jackstay home.
type Store struct {
	path      string
	namespace string
	policy    config.ChatPolicy
	now       func() time.Time
}

// StoreFor locates the shared state for the Git common directory.
func StoreFor(commonDirectory string, policy config.ChatPolicy) (Store, error) {
	resolved, err := filepath.EvalSymlinks(commonDirectory)
	if err != nil {
		return Store{}, err
	}
	digest := sha256.Sum256([]byte(resolved))
	path, err := homedir.SharedPath("session-chat", hex.EncodeToString(digest[:]), "state.json")
	return Store{path: path, namespace: hex.EncodeToString(digest[:]), policy: policy, now: time.Now}, err
}
func randomID() (string, error) {
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

// Register creates or resumes an identity; token remains outside the state file.
func (store Store) Register(name, token string) (Session, error) {
	if strings.TrimSpace(name) == "" || len(name) > 80 || strings.ContainsAny(name, "\r\n") {
		return Session{}, errors.New("session name must be 1 to 80 characters on one line")
	}
	var id string
	if token != "" {
		if len(token) < 32 || len(token) > 256 {
			return Session{}, errors.New("session token must be 32 to 256 characters")
		}
		digest := sha256.Sum256([]byte("jackstay-chat-v1\x00" + store.namespace + "\x00" + token))
		id = hex.EncodeToString(digest[:])
	} else {
		var err error
		id, err = randomID()
		if err != nil {
			return Session{}, err
		}
	}
	var result Session
	err := store.update(func(state *snapshot) (bool, error) {
		now := store.now().UTC().Format(time.RFC3339Nano)
		for index := range state.Sessions {
			if state.Sessions[index].ID == id {
				state.Sessions[index].Name = name
				state.Sessions[index].LastSeen = now
				result = state.Sessions[index]
				return true, nil
			}
		}
		if len(state.Sessions) >= store.policy.MaxSessions {
			return false, errors.New("session capacity reached")
		}
		result = Session{ID: id, Name: name, LastSeen: now}
		state.Sessions = append(state.Sessions, result)
		return true, nil
	})
	return result, err
}

// Sessions discovers identities retained in this repository namespace.
func (store Store) Sessions() ([]Session, error) {
	var result []Session
	err := store.update(func(state *snapshot) (bool, error) {
		result = append([]Session{}, state.Sessions...)
		sort.Slice(result, func(a, b int) bool { return result[a].ID < result[b].ID })
		return false, nil
	})
	return result, err
}

// Touch keeps a connected session discoverable while it uses the service.
func (store Store) Touch(id string) error {
	return store.update(func(state *snapshot) (bool, error) {
		for index := range state.Sessions {
			if state.Sessions[index].ID == id {
				lastSeen, err := time.Parse(time.RFC3339Nano, state.Sessions[index].LastSeen)
				refreshAfter := min(time.Hour, time.Duration(store.policy.RetentionHours)*time.Hour/2)
				if err == nil && store.now().Sub(lastSeen) < refreshAfter {
					return false, nil
				}
				state.Sessions[index].LastSeen = store.now().UTC().Format(time.RFC3339Nano)
				return true, nil
			}
		}
		return false, errors.New("session expired; reconnect to register a new identity")
	})
}

// Send durably accepts a direct message or returns the prior result for the same key.
func (store Store) Send(from, recipientID, kind, body, key, replyTo string) (Message, bool, error) {
	if recipientID == "" || recipientID == from {
		return Message{}, false, errors.New("recipient must be another session")
	}
	if kind != "message" && kind != "request" {
		return Message{}, false, errors.New("kind must be message or request")
	}
	if strings.TrimSpace(body) == "" || len(body) > store.policy.MaxMessageBytes {
		return Message{}, false, errors.New("message body is empty or exceeds configured bytes")
	}
	if key == "" || len(key) > 128 {
		return Message{}, false, errors.New("idempotency key must be 1 to 128 characters")
	}
	if strings.ContainsAny(key, "\r\n") {
		return Message{}, false, errors.New("invalid idempotency key")
	}
	var result Message
	duplicate := false
	err := store.update(func(state *snapshot) (bool, error) {
		for _, message := range state.Messages {
			if message.From == from && message.IdempotencyKey == key {
				if message.To != recipientID || message.Kind != kind || message.Body != body || message.ReplyTo != replyTo {
					return false, errors.New("idempotency key reused for a different message")
				}
				result = message
				duplicate = true
				return false, nil
			}
		}
		recipient := false
		sender := false
		for _, session := range state.Sessions {
			if session.ID == recipientID {
				recipient = true
			}
			if session.ID == from {
				sender = true
			}
		}
		if !sender || !recipient {
			return false, errors.New("unknown session")
		}
		pending := 0
		for _, message := range state.Messages {
			if !message.Acknowledged {
				pending++
			}
		}
		if pending >= store.policy.MaxPending {
			return false, errors.New("pending message capacity reached")
		}
		if len(state.Messages) >= 4*store.policy.MaxPending {
			return false, errors.New("retained message capacity reached")
		}
		if replyTo != "" {
			found := false
			for _, message := range state.Messages {
				if message.ID == replyTo && message.To == from && message.From == recipientID {
					found = true
					break
				}
			}
			if !found {
				return false, errors.New("reply target not addressed to this session")
			}
		}
		id, err := randomID()
		if err != nil {
			return false, err
		}
		result = Message{ID: id, From: from, To: recipientID, Kind: kind, Body: body, ReplyTo: replyTo, Created: store.now().UTC().Format(time.RFC3339Nano), IdempotencyKey: key}
		state.Messages = append(state.Messages, result)
		return true, nil
	})
	return result, duplicate, err
}

// Inbox returns unacknowledged messages addressed to id, oldest first.
func (store Store) Inbox(id string, limit int) ([]Message, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("limit must be 1 to 100")
	}
	result := []Message{}
	err := store.update(func(state *snapshot) (bool, error) {
		for _, message := range state.Messages {
			if message.To == id && !message.Acknowledged {
				result = append(result, message)
				if len(result) == limit {
					break
				}
			}
		}
		return false, nil
	})
	return result, err
}

// Acknowledge marks one recipient-owned message as processed.
func (store Store) Acknowledge(sessionID, messageID string) error {
	return store.update(func(state *snapshot) (bool, error) {
		for index := range state.Messages {
			if state.Messages[index].ID == messageID {
				if state.Messages[index].To != sessionID {
					return false, errors.New("message is not addressed to this session")
				}
				if state.Messages[index].Acknowledged {
					return false, nil
				}
				state.Messages[index].Acknowledged = true
				return true, nil
			}
		}
		return false, errors.New("unknown message")
	})
}

// Reply sends a correlated direct response to the original sender.
func (store Store) Reply(sessionID, messageID, body, key string) (Message, bool, error) {
	var original Message
	found := false
	err := store.update(func(state *snapshot) (bool, error) {
		for _, message := range state.Messages {
			if message.ID == messageID && message.To == sessionID {
				original = message
				found = true
				break
			}
		}
		return false, nil
	})
	if err != nil {
		return Message{}, false, err
	}
	if !found {
		return Message{}, false, errors.New("reply target not addressed to this session")
	}
	return store.Send(sessionID, original.From, "message", body, key, messageID)
}
func (store Store) update(change func(*snapshot) (bool, error)) error {
	folder := filepath.Dir(store.path)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(folder, "state.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	var state snapshot
	data, err := os.ReadFile(store.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(data) > 12<<20 {
		return errors.New("chat state exceeds limit")
	}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("chat state: %w", err)
		}
	}
	previousMessages, previousSessions := len(state.Messages), len(state.Sessions)
	cutoff := store.now().Add(-time.Duration(store.policy.RetentionHours) * time.Hour)
	kept := state.Messages[:0]
	for _, message := range state.Messages {
		when, err := time.Parse(time.RFC3339Nano, message.Created)
		if err == nil && when.After(cutoff) {
			kept = append(kept, message)
		}
	}
	state.Messages = kept
	sessions := state.Sessions[:0]
	for _, session := range state.Sessions {
		when, err := time.Parse(time.RFC3339Nano, session.LastSeen)
		pendingRecipient := false
		for _, message := range state.Messages {
			if !message.Acknowledged && message.To == session.ID {
				pendingRecipient = true
				break
			}
		}
		if err == nil && (when.After(cutoff) || pendingRecipient) {
			sessions = append(sessions, session)
		}
	}
	state.Sessions = sessions
	changed, err := change(&state)
	if err != nil {
		return err
	}
	if !changed && previousMessages == len(state.Messages) && previousSessions == len(state.Sessions) {
		return nil
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(encoded) > 12<<20 {
		return errors.New("chat state exceeds limit")
	}
	temporary, err := os.CreateTemp(folder, "state-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	defer func() { _ = temporary.Close() }()
	if err = temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err = temporary.Write(encoded); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporary.Name(), store.path); err != nil {
		return err
	}
	directory, err := os.Open(folder)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
