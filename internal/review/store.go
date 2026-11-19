package review

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

// Store is where a project's reviews are kept (section 5).
type Store struct{ Root string }

// StoreFor is the store of a project's settings: `review.store`, else ~/.jackstay/<project>/reviews.
func StoreFor(settings Settings, project string) (Store, error) {
	if settings.Store != "" {
		return Store{Root: homedir.Expand(settings.Store)}, nil
	}
	root, err := homedir.ProjectPath(project, "reviews")
	return Store{Root: root}, err
}

// legacyTaskFolder encodes a task as releases before the hashed layout did.
func legacyTaskFolder(task string) string {
	var builder strings.Builder
	for _, octet := range []byte(task) {
		switch {
		case octet >= 'a' && octet <= 'z', octet >= 'A' && octet <= 'Z', octet >= '0' && octet <= '9',
			octet == '.' && builder.Len() > 0, octet == '_', octet == '-':
			builder.WriteByte(octet)
		default:
			fmt.Fprintf(&builder, "%%%02X", octet)
		}
	}
	return builder.String()
}

// legacyTaskName reverses the old encoding ("" and false when the folder is not one).
func legacyTaskName(folder string) (string, bool) {
	var out []byte
	for index := 0; index < len(folder); index++ {
		if folder[index] != '%' {
			out = append(out, folder[index])
			continue
		}
		if index+2 >= len(folder) {
			return "", false
		}
		value, err := strconv.ParseUint(folder[index+1:index+3], 16, 8)
		if err != nil {
			return "", false
		}
		out = append(out, byte(value))
		index += 2
	}
	return string(out), len(out) > 0
}

// taskKey is fixed-length and lower-case, so case folding and filesystem name limits
// cannot confuse or exclude task names.
func taskKey(task string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(task)))
}

func (store Store) tasksPath() string { return filepath.Join(store.Root, ".tasks") }

const taskNameFile = "task.json"

// TaskPath finds the task's current folder or an exactly named legacy folder. Work
// already written in the legacy layout stays readable and can still be pruned.
func (store Store) TaskPath(task string) string {
	current := filepath.Join(store.tasksPath(), taskKey(task))
	if _, err := os.Stat(current); !errors.Is(err, fs.ErrNotExist) {
		return current
	}
	folder := legacyTaskFolder(task)
	if folder == "" || len(folder) > 255 {
		return current
	}
	legacy := filepath.Join(store.Root, folder)
	if info, err := os.Lstat(legacy); err != nil || !info.IsDir() {
		return current
	}
	// Stat alone can also find a differently cased name on Darwin.
	listed, _ := os.ReadDir(store.Root)
	for _, item := range listed {
		if item.Name() == folder && item.IsDir() {
			return legacy
		}
	}
	return current
}

// prepareTask records the original spelling for enumeration. The caller holds the
// task lock and keeps writing to an existing legacy folder.
func (store Store) prepareTask(task string) error {
	folder := store.TaskPath(task)
	if filepath.Dir(folder) != store.tasksPath() {
		return nil
	}
	return writeJSON(filepath.Join(folder, taskNameFile), task)
}

// TaskLockPath is the same lock in both layouts, even if pruning removes the old folder.
func (store Store) TaskLockPath(task string) string {
	return filepath.Join(store.Root, ".locks", "v2", taskKey(task)+".lock")
}

// tryTaskLock also takes the old lock while writing an existing legacy task, so
// an earlier binary already using that folder cannot write it at the same time.
func (store Store) tryTaskLock(task string) (func(), error) {
	held, err := flock.TryFile(store.TaskLockPath(task))
	if err != nil {
		return nil, err
	}
	folder := store.TaskPath(task)
	legacyName := filepath.Base(folder) + ".lock"
	if filepath.Dir(folder) == store.tasksPath() || len(legacyName) > 255 {
		return held.Release, nil
	}
	legacy, err := flock.TryFile(filepath.Join(store.Root, ".locks", legacyName))
	if err != nil {
		held.Release()
		return nil, err
	}
	return func() {
		legacy.Release()
		held.Release()
	}, nil
}

// TreesPath holds the pinned worktrees while they run.
func (store Store) TreesPath() string { return filepath.Join(store.Root, ".trees") }

func (store Store) indexPath() string     { return filepath.Join(store.Root, "index.jsonl") }
func (store Store) indexLockPath() string { return filepath.Join(store.Root, "index.lock") }

// lockIndex waits for the index's lock for at most a minute.
func (store Store) lockIndex() (*flock.File, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return flock.WaitFile(ctx, store.indexLockPath())
}

// ReviewerRun is one reviewer's run as `run.json` keeps it.
type ReviewerRun struct {
	Reviewer string   `json:"reviewer"`
	Argv     []string `json:"argv"`
	Version  string   `json:"version"`
	Model    string   `json:"model"`
	Effort   string   `json:"effort"`
	Started  string   `json:"started"`
	Ended    string   `json:"ended"`
	Seconds  float64  `json:"seconds"`
	Exit     int      `json:"exit"`
	Outcome  string   `json:"outcome"`
	Reason   string   `json:"reason"`
}

// RoundRecord is `round.json` (section 5).
type RoundRecord struct {
	Task      string            `json:"task"`
	Round     int               `json:"round"`
	Attempt   int               `json:"attempt"`
	Chain     int               `json:"chain"`
	Base      string            `json:"base"`
	Commit    string            `json:"commit"`
	Follows   int               `json:"follows"` // the round before it in the chain, 0 for a chain's first
	Focus     string            `json:"focus"`
	Policy    string            `json:"policy,omitempty"`
	PastLimit bool              `json:"past_limit"`
	OverLimit bool              `json:"over_limit"`
	Made      string            `json:"made"`
	Runs      []ReviewerRun     `json:"runs"`
	Outcome   Outcome           `json:"outcome"`
	Summaries map[string]string `json:"summaries"`
}

// AttemptName is an attempt's own record, written first, so a failed attempt keeps what it was for.
const AttemptName = "attempt.json"

// AttemptRecord is `attempt.json`.
type AttemptRecord struct {
	Task    string `json:"task"`
	Attempt int    `json:"attempt"`
	Commit  string `json:"commit"`
	Base    string `json:"base"`
}

// Entry is a round or an attempt in a task's folder.
type Entry struct {
	Name    string // round-N or attempt-K
	Path    string
	Round   int // 0 for an attempt that is not a round
	Attempt int
	Changed time.Time
}

// Entries are a task's rounds and attempts, oldest attempt first.
func (store Store) Entries(task string) ([]Entry, error) {
	folder := store.TaskPath(task)
	listed, err := os.ReadDir(folder)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, item := range listed {
		if !item.IsDir() {
			continue
		}
		entry := Entry{Name: item.Name(), Path: filepath.Join(folder, item.Name())}
		if info, err := item.Info(); err == nil {
			entry.Changed = info.ModTime()
		}
		switch {
		case strings.HasPrefix(item.Name(), "round-"):
			number, err := strconv.Atoi(strings.TrimPrefix(item.Name(), "round-"))
			if err != nil || number < 1 {
				continue
			}
			entry.Round = number
			if record, err := readRecord(entry.Path); err == nil {
				entry.Attempt = record.Attempt
			}
		case strings.HasPrefix(item.Name(), "attempt-"):
			number, err := strconv.Atoi(strings.TrimPrefix(item.Name(), "attempt-"))
			if err != nil || number < 1 {
				continue
			}
			entry.Attempt = number
		default:
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(first, second int) bool {
		if entries[first].Attempt != entries[second].Attempt {
			return entries[first].Attempt < entries[second].Attempt
		}
		return entries[first].Round < entries[second].Round
	})
	return entries, nil
}

// Rounds are a task's rounds, by number.
func Rounds(entries []Entry) []Entry {
	var rounds []Entry
	for _, entry := range entries {
		if entry.Round > 0 {
			rounds = append(rounds, entry)
		}
	}
	sort.Slice(rounds, func(first, second int) bool { return rounds[first].Round < rounds[second].Round })
	return rounds
}

// NextAttempt is the number for a new attempt: never one used before.
func NextAttempt(entries []Entry) int {
	next := len(entries) + 1
	for _, entry := range entries {
		next = max(next, entry.Attempt+1)
	}
	return next
}

// RecordName is a round's record file.
const RecordName = "round.json"

func readRecord(folder string) (RoundRecord, error) {
	data, err := os.ReadFile(filepath.Join(folder, RecordName))
	if err != nil {
		return RoundRecord{}, err
	}
	var record RoundRecord
	if err := strictjson.UnmarshalOpen(data, &record); err != nil {
		return RoundRecord{}, fmt.Errorf("%s: %w", filepath.Join(folder, RecordName), err)
	}
	return record, nil
}

// ReadRound is a task's round N's record.
func (store Store) ReadRound(task string, round int) (RoundRecord, error) {
	return readRecord(filepath.Join(store.TaskPath(task), fmt.Sprintf("round-%d", round)))
}

// Newest is the newest round's record of a task (false when it has none).
func (store Store) Newest(task string) (RoundRecord, bool, error) {
	entries, err := store.Entries(task)
	if err != nil {
		return RoundRecord{}, false, err
	}
	rounds := Rounds(entries)
	if len(rounds) == 0 {
		return RoundRecord{}, false, nil
	}
	record, err := readRecord(rounds[len(rounds)-1].Path)
	return record, err == nil, err
}

// writeFileWhole writes a file of jackstay's own (0600) through a temporary file in its folder.
func writeFileWhole(target string, data []byte) error {
	folder := filepath.Dir(target)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(folder, ".jackstay-review-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	_, writeErr := temporary.Write(data)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, target); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

func writeJSON(target string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFileWhole(target, append(data, '\n'))
}

// IndexLine is one line of `index.jsonl`: one reviewer run.
type IndexLine struct {
	At        string  `json:"at"`
	Task      string  `json:"task"`
	Round     *int    `json:"round"`
	Attempt   int     `json:"attempt"`
	Reviewer  string  `json:"reviewer"`
	Commit    string  `json:"commit"`
	Base      string  `json:"base"`
	Outcome   string  `json:"outcome"`
	Blocks    bool    `json:"blocks"`
	Realistic int     `json:"realistic"`
	Rare      int     `json:"rare"`
	Seconds   float64 `json:"seconds"`
	Path      string  `json:"path"`
}

// AppendIndex adds lines to the index under its lock.
func (store Store) AppendIndex(lines []IndexLine) error {
	held, err := store.lockIndex()
	if err != nil {
		return err
	}
	defer held.Release()
	var buffer bytes.Buffer
	for _, line := range lines {
		data, err := json.Marshal(line)
		if err != nil {
			return err
		}
		buffer.Write(append(data, '\n'))
	}
	file, err := os.OpenFile(store.indexPath(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(buffer.Bytes())
	return errors.Join(writeErr, file.Close())
}

// ReadIndex is the index's lines; a line that does not read is skipped and counted.
func (store Store) ReadIndex() ([]IndexLine, int, error) {
	file, err := os.Open(store.indexPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	var lines []IndexLine
	position := map[string]int{}
	skipped := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		var line IndexLine
		if err := strictjson.UnmarshalOpen(scanner.Bytes(), &line); err != nil {
			skipped++
			continue
		}
		// One line per run: a run appended after a rebuild already listed it is the same run.
		key := fmt.Sprintf("%s\x00%d\x00%s", line.Task, line.Attempt, line.Reviewer)
		if earlier, seen := position[key]; seen {
			lines[earlier] = line
			continue
		}
		position[key] = len(lines)
		lines = append(lines, line)
	}
	return lines, skipped, scanner.Err()
}

// rewriteIndex keeps the index's lines that keep says. The caller holds the lock.
func (store Store) rewriteIndex(keep func(IndexLine) bool) error {
	current, _, err := store.ReadIndex()
	if err != nil {
		return err
	}
	var lines []IndexLine
	for _, line := range current {
		if keep(line) {
			lines = append(lines, line)
		}
	}
	return store.writeIndex(lines)
}

// writeIndex replaces the index whole; the caller holds its lock.
func (store Store) writeIndex(lines []IndexLine) error {
	var buffer bytes.Buffer
	for _, line := range lines {
		data, err := json.Marshal(line)
		if err != nil {
			return err
		}
		buffer.Write(append(data, '\n'))
	}
	return writeFileWhole(store.indexPath(), buffer.Bytes())
}

// Tasks are the tasks in the store, by name.
func (store Store) Tasks() ([]string, error) {
	listed, err := os.ReadDir(store.Root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	for _, item := range listed {
		if !item.IsDir() || strings.HasPrefix(item.Name(), ".") {
			continue
		}
		if task, ok := legacyTaskName(item.Name()); ok && legacyTaskFolder(task) == item.Name() {
			found[task] = true
		}
	}
	current, err := os.ReadDir(store.tasksPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, item := range current {
		if !item.IsDir() {
			continue
		}
		var task string
		data, err := os.ReadFile(filepath.Join(store.tasksPath(), item.Name(), taskNameFile))
		if err == nil && json.Unmarshal(data, &task) == nil && task != "" && taskKey(task) == item.Name() {
			found[task] = true
		}
	}
	tasks := make([]string, 0, len(found))
	for task := range found {
		tasks = append(tasks, task)
	}
	sort.Strings(tasks)
	return tasks, nil
}

// Prune removes every task whose newest round or attempt is older than keepDays before now,
// skipping a task whose lock another run holds; it returns the tasks removed.
func (store Store) Prune(keepDays int, now time.Time) ([]string, error) {
	tasks, err := store.Tasks()
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	// Appends cannot commit to the index between removing a task and rewriting its
	// rows. Task locks here must stay nonblocking: a running review takes them first.
	indexLock, err := store.lockIndex()
	if err != nil {
		return nil, err
	}
	defer indexLock.Release()
	cutoff := now.Add(-time.Duration(keepDays) * 24 * time.Hour)
	var removed []string
	for _, task := range tasks {
		// The lock first, then the decision: a run that ends between a look and a removal
		// cannot lose its fresh round.
		release, err := store.tryTaskLock(task)
		if err != nil {
			continue
		}
		if store.expired(task, cutoff) && os.RemoveAll(store.TaskPath(task)) == nil {
			removed = append(removed, task)
		}
		release()
	}
	if len(removed) > 0 {
		gone := map[string]bool{}
		for _, task := range removed {
			gone[task] = true
		}
		if err := store.rewriteIndex(func(line IndexLine) bool { return !gone[line.Task] }); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// expired says whether a task's newest round or attempt is older than the cutoff (an empty task
// folder is). Read under the task's lock.
func (store Store) expired(task string, cutoff time.Time) bool {
	entries, err := store.Entries(task)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.Changed.Before(cutoff) {
			return false
		}
	}
	return true
}

// Rebuild writes the index anew from the folders.
func (store Store) Rebuild() error {
	held, err := store.lockIndex()
	if err != nil {
		return err
	}
	defer held.Release()
	tasks, err := store.Tasks()
	if err != nil {
		return err
	}
	var lines []IndexLine
	for _, task := range tasks {
		entries, err := store.Entries(task)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			lines = append(lines, linesOf(task, entry)...)
		}
	}
	return store.writeIndex(lines)
}

func linesOf(task string, entry Entry) []IndexLine {
	if entry.Round > 0 {
		record, err := readRecord(entry.Path)
		if err != nil {
			return nil
		}
		return recordLines(record, entry.Path)
	}
	var attempt AttemptRecord
	if data, err := os.ReadFile(filepath.Join(entry.Path, AttemptName)); err == nil {
		_ = strictjson.UnmarshalOpen(data, &attempt)
	}
	var lines []IndexLine
	reviewers, _ := os.ReadDir(entry.Path)
	for _, reviewer := range reviewers {
		if !reviewer.IsDir() {
			continue
		}
		var run ReviewerRun
		data, err := os.ReadFile(filepath.Join(entry.Path, reviewer.Name(), "run.json"))
		if err != nil || strictjson.UnmarshalOpen(data, &run) != nil {
			continue
		}
		lines = append(lines, IndexLine{At: run.Ended, Task: task, Attempt: entry.Attempt, Reviewer: run.Reviewer,
			Commit: attempt.Commit, Base: attempt.Base, Outcome: run.Outcome, Seconds: run.Seconds, Path: entry.Path})
	}
	return lines
}

func recordLines(record RoundRecord, path string) []IndexLine {
	realistic, rare := 0, 0
	for _, id := range record.Outcome.New {
		for _, finding := range record.Outcome.Ledger {
			if finding.ID != id {
				continue
			}
			if finding.Status == StatusDeferred {
				rare++
			} else {
				realistic++
			}
		}
	}
	round := record.Round
	var lines []IndexLine
	for _, run := range record.Runs {
		lines = append(lines, IndexLine{At: run.Ended, Task: record.Task, Round: &round, Attempt: record.Attempt,
			Reviewer: run.Reviewer, Commit: record.Commit, Base: record.Base, Outcome: run.Outcome,
			Blocks: record.Outcome.Blocks, Realistic: realistic, Rare: rare, Seconds: run.Seconds, Path: path})
	}
	return lines
}
