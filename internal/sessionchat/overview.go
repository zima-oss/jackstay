package sessionchat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/zima-oss/jackstay/internal/durable"
)

// Overview is a content-free count of retained repository chat activity.
type Overview struct {
	Sessions int `json:"sessions"`
	Pending  int `json:"pending"`
}

// ReadOverview inspects chat state without creating a directory, lock, or state file.
// Retention is applied in memory, so the counts agree with the next normal store read.
func (store Store) ReadOverview() (Overview, error) {
	data, err := durable.ReadLimited(store.path, stateLimit)
	if errors.Is(err, os.ErrNotExist) {
		return Overview{}, nil
	}
	if errors.Is(err, durable.ErrTooLarge) {
		return Overview{}, errors.New("chat state exceeds limit")
	}
	if err != nil {
		return Overview{}, err
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return Overview{}, errors.New("chat state: expected an object")
	}
	var state snapshot
	if err := json.Unmarshal(data, &state); err != nil {
		return Overview{}, fmt.Errorf("chat state: %w", err)
	}
	cutoff := store.now().Add(-time.Duration(store.policy.RetentionHours) * time.Hour)
	var overview Overview
	pendingRecipients := map[string]bool{}
	for _, message := range state.Messages {
		created, err := time.Parse(time.RFC3339Nano, message.Created)
		if err == nil && retainedMessage(message, created, cutoff) && !message.Acknowledged {
			overview.Pending++
			pendingRecipients[message.To] = true
		}
	}
	for _, session := range state.Sessions {
		seen, err := time.Parse(time.RFC3339Nano, session.LastSeen)
		if err == nil && (seen.After(cutoff) || pendingRecipients[session.ID]) {
			overview.Sessions++
		}
	}
	return overview, nil
}
