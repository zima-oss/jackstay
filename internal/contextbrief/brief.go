// Package contextbrief keeps an explicitly written task summary for one worktree.
package contextbrief

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/homedir"
)

const maxBriefBytes = 4096
const defaultMaxAge = 7 * 24 * time.Hour
const maximumMaxAge = 30 * 24 * time.Hour

// Brief contains only short, explicitly supplied task facts. It never reads a transcript.
type Brief struct {
	TaskID    string   `json:"task_id"`
	Task      string   `json:"task"`
	Next      []string `json:"next"`
	Decisions []string `json:"decisions,omitempty"`
}

// Identity records the Git branch and HEAD at the time of saving.
type Identity struct {
	Branch       string `json:"branch"`
	Head         string `json:"head"`
	RepositoryID string `json:"repository_id"`
}

// Record retains a revision even after clearing a brief, preventing ABA writes.
type Record struct {
	Schema        int       `json:"schema"`
	Revision      uint64    `json:"revision"`
	Brief         *Brief    `json:"brief"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
	MaxAgeSeconds int64     `json:"max_age_seconds,omitempty"`
	Identity      Identity  `json:"identity,omitempty"`
}

// Store contains a separate note for each resolved worktree root.
type Store struct {
	path string
	now  func() time.Time
}

// StoreFor selects the state file without writing into the repository.
func StoreFor(root string) (Store, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Store{}, err
	}
	digest := sha256.Sum256([]byte(resolved))
	path, err := homedir.SharedPath("context-brief", hex.EncodeToString(digest[:]), "brief.json")
	return Store{path: path, now: time.Now}, err
}

func validateBrief(brief Brief) error {
	if strings.TrimSpace(brief.TaskID) == "" || len(brief.TaskID) > 80 || strings.ContainsAny(brief.TaskID, "\r\n") {
		return errors.New("task_id must be 1–80 characters on one line")
	}
	if strings.TrimSpace(brief.Task) == "" || len(brief.Next) == 0 || len(brief.Next) > 12 || len(brief.Decisions) > 12 {
		return errors.New("task must be nonempty, next have 1–12 items, and decisions at most 12")
	}
	for _, item := range append(append([]string{brief.TaskID, brief.Task}, brief.Next...), brief.Decisions...) {
		if strings.TrimSpace(item) == "" || strings.ContainsAny(item, "\r\n") {
			return errors.New("each brief item must be nonempty and on one line")
		}
	}
	encoded, err := json.Marshal(brief)
	if err != nil || len(encoded) > maxBriefBytes {
		return fmt.Errorf("brief exceeds %d bytes", maxBriefBytes)
	}
	return nil
}

func (store Store) read() (Record, error) {
	encoded, err := durable.ReadLimited(store.path, maxBriefBytes+1024)
	if errors.Is(err, os.ErrNotExist) {
		return Record{Schema: 1}, nil
	}
	if errors.Is(err, durable.ErrTooLarge) {
		return Record{}, errors.New("brief state exceeds limit")
	}
	if err != nil {
		return Record{}, err
	}
	var record Record
	if json.Unmarshal(encoded, &record) != nil {
		return Record{}, errors.New("brief state is corrupt")
	}
	if record.Schema != 1 || record.Revision == 0 || (record.Brief != nil && validateBrief(*record.Brief) != nil) {
		return Record{}, errors.New("brief state has invalid fields")
	}
	return record, nil
}

// Load returns revision zero when no state has ever been written.
func (store Store) Load() (Record, error) { return store.read() }

// Save replaces the brief only at the expected revision, preserving its task identity.
func (store Store) Save(brief Brief, expected uint64, maxAge time.Duration, identity Identity) (Record, error) {
	if err := validateBrief(brief); err != nil {
		return Record{}, err
	}
	if maxAge < time.Hour || maxAge > maximumMaxAge || maxAge%time.Second != 0 {
		return Record{}, errors.New("max age must be 1 hour to 30 days in whole seconds")
	}
	if identity.Branch == "" || identity.Head == "" || identity.RepositoryID == "" {
		return Record{}, errors.New("a Git branch, HEAD and repository identity are required")
	}
	return store.update(expected, func(record *Record) error {
		if record.Brief != nil && record.Identity.RepositoryID != identity.RepositoryID {
			return errors.New("repository changed; clear the previous brief first")
		}
		if record.Brief != nil && record.Brief.TaskID != brief.TaskID {
			return errors.New("task_id changed; clear the previous task first")
		}
		record.Brief = &brief
		record.UpdatedAt = store.now().UTC()
		record.MaxAgeSeconds = int64(maxAge / time.Second)
		record.Identity = identity
		return nil
	})
}

// Clear leaves a revision tombstone. Another task starts from that revision.
func (store Store) Clear(expected uint64) (Record, error) {
	return store.update(expected, func(record *Record) error {
		record.Brief = nil
		record.UpdatedAt = time.Time{}
		record.MaxAgeSeconds = 0
		record.Identity = Identity{}
		return nil
	})
}

func (store Store) update(expected uint64, change func(*Record) error) (Record, error) {
	var record Record
	err := durable.Update(context.Background(), store.path, 0o600, maxBriefBytes+1024, func([]byte) ([]byte, error) {
		var err error
		record, err = store.read()
		if err != nil {
			return nil, err
		}
		if record.Revision != expected {
			return nil, fmt.Errorf("brief revision conflict: current revision %d", record.Revision)
		}
		if record.Revision == math.MaxUint64 {
			return nil, errors.New("brief revision exhausted")
		}
		if err := change(&record); err != nil {
			return nil, err
		}
		record.Revision++
		return json.Marshal(record)
	})
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

// Freshness reports whether this note may be injected, and why it needs verification.
func (store Store) Freshness(record Record, current Identity) string {
	if record.Brief == nil {
		return "absent"
	}
	if current.Branch == "" || current.Head == "" || current.RepositoryID == "" {
		return "git-unavailable"
	}
	if current.RepositoryID != record.Identity.RepositoryID {
		return "repository-changed"
	}
	if current.Branch != record.Identity.Branch {
		return "branch-changed"
	}
	if record.UpdatedAt.IsZero() || record.MaxAgeSeconds <= 0 || store.now().After(record.UpdatedAt.Add(time.Duration(record.MaxAgeSeconds)*time.Second)) {
		return "expired"
	}
	if current.Head != record.Identity.Head {
		return "head-changed"
	}
	return "current"
}
