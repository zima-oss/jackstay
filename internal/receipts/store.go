package receipts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/version"
)

// ReadCurrent checks a stored pass against the current computation of its receipt key.
// Report paths are per-attempt transport. The saved material and counted verdict suffice.
func (store Store) ReadCurrent(key Key) (*Receipt, string) {
	receipt, why := store.Read(key.Value)
	if receipt == nil {
		return nil, why
	}
	if !receipt.matchesMaterial() || receipt.EvidenceCount <= 0 || receipt.Project != key.Material.Project {
		return nil, "the receipt has inconsistent key material or no counted pass"
	}
	return receipt, ""
}

func (receipt Receipt) matchesMaterial() bool {
	data, err := json.Marshal(receipt.Material)
	return err == nil && sha256Text(data) == receipt.Key && receipt.Material.Project == receipt.Project &&
		(receipt.Material.Head == "" || receipt.Material.Head == receipt.Head)
}

// Receipt is a stored pass of a key (schema 3, Go's own).
type Receipt struct {
	Schema        int      `json:"schema"`
	Key           string   `json:"key"`
	Outcome       string   `json:"outcome"`
	Project       string   `json:"project"`
	Command       []string `json:"command"`
	Seconds       float64  `json:"seconds"`
	EvidenceCount int      `json:"evidence_count"`
	Started       string   `json:"started"`
	StartedEpoch  float64  `json:"started_epoch"`
	Ended         string   `json:"ended"`
	EndedEpoch    float64  `json:"ended_epoch"`
	Material      Material `json:"material"`
	Jackstay      string   `json:"jackstay"`
	RunID         string   `json:"run_id"`
	Worktree      string   `json:"worktree"`
	Head          string   `json:"head"`
	Branch        string   `json:"branch"`
	// Path is where it was read from; not stored.
	Path string `json:"-"`
}

// Store is one project's receipts and failure notes.
type Store struct {
	Folder string
	// MaxAge is receipts.max_age_hours; a receipt older is never used, and pruning keeps at least a day.
	MaxAge time.Duration
	Clock  Clock
	PID    int
}

// OpenStore is the store of a project.
func OpenStore(project string, section map[string]any, clock Clock) (Store, error) {
	folder, err := StateFolder(project, "receipts")
	if err != nil {
		return Store{}, err
	}
	return Store{Folder: folder, MaxAge: MaxAge(section), Clock: clock, PID: os.Getpid()}, nil
}

// MaxAge is receipts.max_age_hours (72 when absent or not a positive number).
func MaxAge(section map[string]any) time.Duration {
	if hours, ok := numberOf(section["max_age_hours"]); ok && hours > 0 && !math.IsInf(hours, 0) {
		return time.Duration(hours * float64(time.Hour))
	}
	return DefaultMaxAgeHours * time.Hour
}

// numberOf is a number of jackstay.json or of a log line.
func numberOf(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	}
	return 0, false
}

// RemoveFile removes a receipt or a gate mark a run is about to replace; tests make it fail, as a
// protected file would.
var RemoveFile = os.Remove

// truncateFile empties a receipt that cannot be removed; tests make it fail with RemoveFile.
var truncateFile = os.Truncate

func (store Store) receiptPath(key string) string { return filepath.Join(store.Folder, key+".json") }

// failurePrefix is the name every failure note of the key begins with.
func failurePrefix(key string) string { return key + ".failed" }

// Read is the stored receipt for the key when it can be used, else why not.
func (store Store) Read(key string) (*Receipt, string) {
	place := store.receiptPath(key)
	data, err := os.ReadFile(place)
	if err != nil {
		return nil, "no receipt"
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, "the receipt file is not JSON"
	}
	if receipt.Schema != Schema || receipt.Key != key || receipt.Outcome != OutcomePassed {
		return nil, "the receipt file is not a receipt for this key"
	}
	if receipt.Jackstay != version.Version {
		return nil, fmt.Sprintf("the receipt was written by jackstay %s, not %s", receipt.Jackstay, version.Version)
	}
	if receipt.EndedEpoch <= 0 {
		return nil, "the receipt's time cannot be read"
	}
	age := store.Clock.Now().Sub(fromEpoch(receipt.EndedEpoch))
	if age < -clockSkew || age > store.MaxAge {
		return nil, fmt.Sprintf("the receipt is %d h old (limit %g h)", int(math.Floor(age.Hours())), store.MaxAge.Hours())
	}
	if failed, found := store.FailureTime(key); found && !failed.Before(fromEpoch(receipt.StartedEpoch)) {
		return nil, "a run of this same tree failed after the receipt's run had started"
	}
	receipt.Path = place
	return &receipt, ""
}

func fromEpoch(seconds float64) time.Time {
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fraction*1e9))
}

// Write stores a receipt atomically (a temporary in the same folder, synced, renamed) and prunes.
func (store Store) Write(receipt Receipt) (string, error) {
	if err := os.MkdirAll(store.Folder, 0o700); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", err
	}
	place := store.receiptPath(receipt.Key)
	if err := durable.WriteAtomic(place, append(data, '\n'), 0o600, 0); err != nil {
		return "", err
	}
	store.Prune()
	return place, nil
}

// Remove takes the key's receipt away: the key is being run again, or a run of it did not pass.
// True when one was taken away; a file that cannot be removed is emptied (an empty receipt is
// never used), and one that cannot be emptied either is an error naming it.
func (store Store) Remove(key string) (bool, error) {
	if key == "" {
		return false, nil
	}
	place := store.receiptPath(key)
	err := RemoveFile(place)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if truncateErr := truncateFile(place, 0); truncateErr == nil {
		return true, nil
	}
	return false, fmt.Errorf("the receipt %s can be neither removed nor emptied; remove it by hand", place)
}

// failureNote is a failure note's content.
type failureNote struct {
	Key     string  `json:"key"`
	Outcome string  `json:"outcome"`
	At      string  `json:"at"`
	AtEpoch float64 `json:"at_epoch"`
}

// NoteFailure leaves a note that a run of the key did not pass, now: one file per failure, made
// and never replaced, so a writer that was held up cannot put an older time over a newer one.
// False when it could not be written.
func (store Store) NoteFailure(key, outcome string) bool {
	if key == "" {
		return false
	}
	if err := os.MkdirAll(store.Folder, 0o700); err != nil {
		return false
	}
	now := store.Clock.Now()
	place := filepath.Join(store.Folder, fmt.Sprintf("%s.%020d-%d", failurePrefix(key), now.UnixNano(), store.PID))
	data, err := json.Marshal(failureNote{Key: key, Outcome: outcome, At: timeText(now), AtEpoch: epochOf(now)})
	if err != nil {
		return false
	}
	file, err := os.OpenFile(place, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return false
	}
	store.Prune()
	return true
}

// FailureTime is when a run of the key last failed, the newest of its notes. A note that cannot be
// read counts at its file's time (or now), and so does a store that cannot be listed.
func (store Store) FailureTime(key string) (time.Time, bool) {
	entries, err := os.ReadDir(store.Folder)
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, false
	}
	if err != nil {
		return store.Clock.Now(), true
	}
	prefix := failurePrefix(key)
	var newest time.Time
	found := false
	for _, entry := range entries {
		name := entry.Name()
		if name != prefix && (!strings.HasPrefix(name, prefix+".") || strings.HasSuffix(name, ".partial")) {
			continue
		}
		when, present := store.noteTime(filepath.Join(store.Folder, name))
		if !present {
			continue
		}
		if !found || when.After(newest) {
			newest, found = when, true
		}
	}
	return newest, found
}

func (store Store) noteTime(place string) (time.Time, bool) {
	data, err := os.ReadFile(place)
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, false
	}
	var note failureNote
	if err == nil && json.Unmarshal(data, &note) == nil && note.AtEpoch > 0 {
		return fromEpoch(note.AtEpoch), true
	}
	status, statErr := os.Lstat(place)
	if errors.Is(statErr, fs.ErrNotExist) {
		return time.Time{}, false
	}
	if statErr != nil {
		return store.Clock.Now(), true
	}
	return status.ModTime(), true
}

// Prune removes the store's receipts, failure notes and half-written files older than the age
// limit and than a day: a receipt that old is never used, a note that old speaks only of runs
// older than itself. Called by whoever writes one; the count removed.
func (store Store) Prune() int {
	entries, err := os.ReadDir(store.Folder)
	if err != nil {
		return 0
	}
	before := store.Clock.Now().Add(-max(store.MaxAge, markerFloor))
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".partial") && !strings.HasSuffix(name, ".failed") &&
			!strings.Contains(name, ".failed.") {
			continue
		}
		place := filepath.Join(store.Folder, name)
		status, err := os.Lstat(place)
		if err == nil && status.ModTime().Before(before) && os.Remove(place) == nil {
			removed++
		}
	}
	return removed
}
