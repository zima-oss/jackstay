package sessionchat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// Overview is a content-free count of retained repository chat activity.
type Overview struct {
	Sessions int `json:"sessions"`
	Pending  int `json:"pending"`
}

// ReadOverview inspects chat state without creating a directory, lock, or state file.
// Retention is applied in memory, so the counts agree with the next normal store read.
func (store Store) ReadOverview() (Overview, error) {
	file, err := os.OpenFile(store.path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return Overview{}, nil
	}
	if err != nil {
		return Overview{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Overview{}, errors.New("chat state is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 12<<20+1))
	if err != nil {
		return Overview{}, err
	}
	if len(data) > 12<<20 {
		return Overview{}, errors.New("chat state exceeds limit")
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
		if err == nil && created.After(cutoff) && !message.Acknowledged {
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
