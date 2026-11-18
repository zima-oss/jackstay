package receipts

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/version"
)

// Events of the run log.
const (
	EventStart    = "start"
	EventRun      = "run"
	EventReused   = "reused"
	EventWouldHit = "would-hit"
	EventGate     = "gate"
)

// Kinds of not_stored (the run line's not_stored_kind; not_stored says the same in words).
const (
	NotStoredNoKey          = "no-key"
	NotStoredTreeChanged    = "tree-changed"
	NotStoredInputRewritten = "input-rewritten"
	NotStoredFailedSince    = "failed-since"
	NotStoredWriteFailed    = "write-failed"
	NotStoredNoEvidence     = "no-evidence"
)

// RunLogPath is `<jackstay home>/.all/runs.jsonl`.
func RunLogPath() (string, error) { return homedir.SharedPath("runs.jsonl") }

// RunLogPaths are every file holding the run record: the run log.
func RunLogPaths() []string {
	var paths []string
	if current, err := RunLogPath(); err == nil {
		paths = append(paths, current)
	}
	return paths
}

// Record is one line of the run log.
type Record map[string]any

// Text is a field as text ("" when absent or not text).
func (record Record) Text(name string) string {
	text, _ := record[name].(string)
	return text
}

// Number is a field as a number.
func (record Record) Number(name string) (float64, bool) {
	return numberOf(record[name])
}

// Inside says the record is part of an outer run of the same project (counted with that run).
func (record Record) Inside() bool {
	value, present := record["inside"]
	return present && value != nil && value != "" && value != false
}

// ReadRecords are the records of the logs in order (a missing file has none; a line that is not a
// JSON object is skipped).
func ReadRecords(paths []string) []Record {
	var found []Record
	for _, place := range paths {
		data, err := os.ReadFile(place)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
		for scanner.Scan() {
			var record Record
			if json.Unmarshal(scanner.Bytes(), &record) == nil && record != nil {
				found = append(found, record)
			}
		}
	}
	return found
}

// Forced says a would-hit was a run a person forced (--no-reuse, JACKSTAY_NO_REUSE).
func Forced(record Record) bool {
	forced, _ := record["forced"].(bool)
	return forced
}

// NotStoredKind is why a pass stored no receipt, as a kind ("" when it says none).
func NotStoredKind(record Record) string { return record.Text("not_stored_kind") }

// Where is the checkout a run ran in.
type Where struct {
	Worktree string
	Head     string
	Branch   string
}

// WhereOf is the checkout at root (an empty Where without one).
func WhereOf(ctx context.Context, root string) Where {
	if root == "" {
		return Where{}
	}
	git := gitcli.Git{Dir: root}
	bounded, cancel := context.WithTimeout(ctx, gitBudget)
	defer cancel()
	head, _ := git.Line(bounded, "rev-parse", "HEAD")
	branch, _ := git.Line(bounded, "symbolic-ref", "--short", "-q", "HEAD")
	return Where{Worktree: realPath(root), Head: strings.TrimSpace(head), Branch: strings.TrimSpace(branch)}
}

// RunLog writes one run's lines: every line has at, epoch, event, project, command, key, version,
// worktree, head, branch, and `inside` for a run nested in another of the same project.
type RunLog struct {
	Path    string
	Project string
	Command []string
	Where   Where
	// Inside is the outer run's id ("" when this run is not nested in a run of its project).
	Inside string
	Clock  Clock
}

// ParentRun is the outer run's id when jst test runs inside a `jst test` of the same project.
func ParentRun(getenv func(string) string, project string) string {
	outer := getenv(supervise.RunIDVariable)
	if outer != "" && getenv(supervise.RunProjectVariable) == project {
		return outer
	}
	return ""
}

// Write appends one line; false when it could not be written (the caller says so before the
// outcome line). key "" is written null.
func (log RunLog) Write(event, key string, fields map[string]any) bool {
	if log.Path == "" {
		return false
	}
	now := log.Clock.Now()
	record := map[string]any{"at": timeText(now), "epoch": epochOf(now), "event": event, "project": log.Project,
		"command": slices.Clone(log.Command), "key": nullable(key), "version": version.Version,
		"worktree": nullable(log.Where.Worktree), "head": log.Where.Head, "branch": log.Where.Branch}
	if record["command"] == nil {
		record["command"] = []string{}
	}
	for name, value := range fields {
		record[name] = value
	}
	if log.Inside != "" {
		record["inside"] = log.Inside
	}
	written := homedir.AppendLine(log.Path, record)
	observe(log, event, key, fields)
	return written
}

func nullable(text string) any {
	if text == "" {
		return nil
	}
	return text
}

// EarlierPass is the log's last counted `passed` run of the key, when it is within the age limit
// and no run of the key failed, ran nothing or was unjudged after it (lines `inside` another run
// skipped): what a would-hit is counted from, not the store.
func EarlierPass(records []Record, project, key string, maxAge time.Duration, now time.Time) (Record, bool) {
	var found Record
	for _, record := range records {
		if record.Text("event") != EventRun || record.Text("key") != key || record.Text("project") != project || record.Inside() {
			continue
		}
		switch record.Text("outcome") {
		case OutcomePassed:
			if count, ok := record.Number("evidence_count"); ok && count != 0 {
				found = record
			}
		case OutcomeFailed, OutcomeNothingRun, OutcomeUnjudged:
			found = nil
		}
	}
	if found == nil {
		return nil, false
	}
	epoch, ok := found.Number("epoch")
	if !ok {
		return nil, false
	}
	age := now.Sub(fromEpoch(epoch))
	if age < -clockSkew || age > maxAge {
		return nil, false
	}
	return found, true
}
