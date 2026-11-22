// Events: the recent delivery stages of a repository (pull requests, tests, reviews, background
// jobs) projected from the local journal without private fields (formerly `jst timeline`).

package status

import (
	"cmp"
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/genai"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/telemetry/query"
)

const (
	// maximumAncestry bounds the spans kept to attribute events to worktrees.
	maximumAncestry = 8192
	// maximumLine bounds one journal line.
	maximumLine = 16 * journal.LineLimit
)

// window is the time the events and sessions are read for, and how many events are kept.
type window struct {
	from, until time.Time
	limit       int
}

// Event is one delivery stage, attributed to a worktree; it carries no argument, prompt or path.
type Event struct {
	Stage        string    `json:"stage"`
	Status       string    `json:"status"`
	At           time.Time `json:"at"`
	DurationMS   int64     `json:"duration_ms"`
	WorktreeID   string    `json:"worktree_id"`
	TraceID      string    `json:"trace_id"`
	SpanID       string    `json:"span_id"`
	ParentSpanID string    `json:"parent_span_id,omitempty"`
	Head         string    `json:"head,omitempty"`
	Pull         string    `json:"pull_request_id,omitempty"`
	TestRunID    string    `json:"test_run_id,omitempty"`
	BackgroundID string    `json:"background_id,omitempty"`
	Tokens       *Tokens   `json:"tokens,omitempty"`
}

// Tokens are an agent run's measured token counts.
type Tokens struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CachedInput int64 `json:"cached_input"`
}

type spanKey struct{ trace, span string }

// observed keeps only projected fields. Raw journal objects may contain arguments and prompts.
type observed struct {
	key, parent spanKey
	explicit    bool
	location    string
	worktree    string
	ended       time.Time
	rows        []Event
}

// observations holds the newest ancestry candidates by end time. The root of the heap is the
// oldest retained span. An ancestry gap after truncation never grants a worktree identity.
type observations []observed

func (queue observations) Len() int { return len(queue) }
func (queue observations) Less(left, right int) bool {
	return compareObserved(queue[left], queue[right]) < 0
}
func (queue observations) Swap(left, right int) {
	queue[left], queue[right] = queue[right], queue[left]
}
func (queue *observations) Push(value any) { *queue = append(*queue, value.(observed)) }
func (queue *observations) Pop() any {
	last := len(*queue) - 1
	item := (*queue)[last]
	(*queue)[last] = observed{}
	*queue = (*queue)[:last]
	return item
}

func compareObserved(left, right observed) int {
	return cmp.Or(left.ended.Compare(right.ended), cmp.Compare(left.key.trace, right.key.trace),
		cmp.Compare(left.key.span, right.key.span))
}

// eventCollector projects spans into events while the journal is read.
type eventCollector struct {
	asked    window
	retained observations
	partial  bool
}

func (collect *eventCollector) add(span journal.Span) {
	key := spanKey{trace: hexID(span.TraceID, 32), span: hexID(span.SpanID, 16)}
	if key.trace == "" || key.span == "" {
		return
	}
	projected := observed{key: key, ended: span.End}
	if collect.retained.Len() == maximumAncestry {
		collect.partial = true
		if compareObserved(projected, collect.retained[0]) <= 0 {
			return
		}
	}
	location := span.Text(telemetry.AttrWorktree)
	if location == "" {
		location = span.Text(telemetry.AttrCwd)
	}
	projected.explicit = location != ""
	if len(location) <= 4096 {
		projected.location = location
	}
	projected.parent = spanKey{trace: key.trace, span: hexID(span.ParentID, 16)}
	projected.rows = projectRows(span, projected, collect.asked)
	if collect.retained.Len() == maximumAncestry {
		collect.retained[0] = projected
		heap.Fix(&collect.retained, 0)
	} else {
		heap.Push(&collect.retained, projected)
	}
}

// events are the newest events of the repository's worktrees, oldest first, and how many older
// ones the limit left out.
func (collect *eventCollector) events(found checkout) (kept []Event, omitted int) {
	bySpan := make(map[spanKey]observed, collect.retained.Len())
	byPath := make(map[string]string)
	for _, item := range collect.retained {
		if item.location != "" {
			owner, cached := byPath[item.location]
			if !cached {
				owner = found.worktreeOwning(item.location)
				byPath[item.location] = owner
			}
			item.worktree = owner
		}
		bySpan[item.key] = item
	}
	kept = []Event{}
	for _, item := range bySpan {
		owner := worktreeOf(item, bySpan)
		if owner == "" {
			continue
		}
		parent, knownParent := bySpan[item.parent]
		for _, point := range item.rows {
			if knownParent && worktreeOf(parent, bySpan) == "" {
				point.ParentSpanID = ""
			}
			point.WorktreeID = owner
			kept = append(kept, point)
		}
	}
	slices.SortFunc(kept, func(left, right Event) int {
		return cmp.Or(left.At.Compare(right.At), cmp.Compare(left.TraceID, right.TraceID),
			cmp.Compare(left.SpanID, right.SpanID), cmp.Compare(left.Stage, right.Stage))
	})
	if len(kept) > collect.asked.limit {
		omitted = len(kept) - collect.asked.limit
		kept = kept[omitted:]
	}
	return kept, omitted
}

func worktreeOf(item observed, bySpan map[spanKey]observed) string {
	for range 64 {
		if item.explicit {
			return item.worktree
		}
		parent, ok := bySpan[item.parent]
		if !ok {
			return ""
		}
		item = parent
	}
	return ""
}

func inWindow(at time.Time, asked window) bool {
	return !at.Before(asked.from) && at.Before(asked.until)
}

func projectRows(span journal.Span, item observed, asked window) []Event {
	base := Event{TraceID: item.key.trace, SpanID: item.key.span, ParentSpanID: item.parent.span,
		Head: hexID(span.Text(telemetry.AttrHead), 40, 64)}
	if head := hexID(span.Text(telemetry.AttrTestHead), 40, 64); head != "" {
		base.Head = head
	}
	if id := span.Text(telemetry.AttrTestRunID); runID.MatchString(id) {
		base.TestRunID = id
	}
	if id := span.Text(telemetry.AttrBgID); runID.MatchString(id) {
		base.BackgroundID = id
	}
	base.Pull = pullID(span.Text(telemetry.AttrGitHubRepository), span.Number(telemetry.AttrGitHubPull))
	rows := []Event{}
	if stage := stageOf(span); stage != "" && inWindow(span.End, asked) {
		entry := base
		entry.Stage, entry.Status, entry.At = stage, statusOf(span), span.End.UTC()
		entry.DurationMS = max(0, span.Duration().Milliseconds())
		if usage, ok := genai.AgentRunOf(span); ok && len(usage.Turns) == 1 {
			total := usage.Turns[0]
			if total.Input > 0 || total.Output > 0 || total.CachedInput > 0 {
				entry.Tokens = &Tokens{Input: max(0, total.Input), Output: max(0, total.Output), CachedInput: max(0, total.CachedInput)}
			}
		}
		rows = append(rows, entry)
	}
	// The procs commands can stop or sweep jobs from other repositories. Their
	// legacy events record the group ID but not its owner, so report only bg's own lifecycle.
	if span.Text(telemetry.AttrCommand) == "bg" {
		rows = append(rows, backgroundEvents(span, base, asked)...)
	}
	return rows
}

var runID = regexp.MustCompile(`^[0-9a-f]{1,16}-[0-9]{1,20}$`)

func hexID(value string, sizes ...int) string {
	if !slices.Contains(sizes, len(value)) {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return strings.ToLower(value)
}

func pullID(repository string, number int64) string {
	owner, name, found := strings.Cut(repository, "/")
	if number <= 0 || !found || owner == "" || name == "" || len(repository) > 256 {
		return ""
	}
	digest := sha256.Sum256([]byte(strings.ToLower(repository)))
	return fmt.Sprintf("github:%x#%d", digest, number)
}

func stageOf(span journal.Span) string {
	switch span.Name {
	case "test.run", "test.gate", "review.attempt":
		return span.Name
	}
	if genai.IsAgentRun(span) && span.Text(telemetry.AttrReviewReviewer) != "" {
		return "review.agent"
	}
	switch span.Text(telemetry.AttrCommand) {
	case "gh":
		// Spans recorded by jackstay before 0.6.0, whose `jst gh` is removed.
		switch operation := span.Text(telemetry.AttrGitHubOperation); operation {
		case "ci-wait", "ci-status", "run-wait", "pr-find", "pr-status", "pr-land":
			return "github." + operation
		default:
			return "github.command"
		}
	case "checks":
		switch operation := span.Text(telemetry.AttrGitHubOperation); operation {
		case "wait", "status", "run-wait":
			return "checks." + operation
		default:
			return "checks.command"
		}
	case "land":
		switch operation := span.Text(telemetry.AttrGitHubOperation); operation {
		case "status", "find":
			return "land." + operation
		default:
			return "land.command"
		}
	case "lint pr":
		return "github.lint-pr"
	case "test":
		return "test.command"
	case "review":
		return "review.command"
	case "bg":
		return "background.command"
	default:
		return ""
	}
}

func statusOf(span journal.Span) string {
	switch outcome := query.Outcome(span); outcome {
	case "ok", "passed", "failed", "pending", "reused", "would-hit", "exited-0", "nothing-run", "passed-on-retry",
		"timed-out", "unjudged", "not-started", "stopped", "crashed", "done", "clear", "blocks", "redesign-due",
		"no-round", "timeout", "unparsable":
		return outcome
	}
	switch span.StatusCode {
	case journal.StatusError:
		return "failed"
	case journal.StatusOK:
		return "ok"
	default:
		return "unknown"
	}
}

func backgroundEvents(span journal.Span, base Event, asked window) []Event {
	// A long command may report the same lifecycle stage repeatedly. Retain the
	// latest point for each kind on a span, so even those projections stay bounded.
	latest := map[string]Event{}
	for _, event := range span.Events {
		stage, status := "", ""
		switch event.Name {
		case telemetry.EventBgStarted:
			stage, status = "background.started", "started"
		case telemetry.EventBgEnded:
			stage, status = "background.ended", "ended"
		case telemetry.EventBgStopped:
			stage, status = "background.stopped", "stopped"
		case telemetry.EventBgHeld:
			stage, status = "background.held", "held"
		case telemetry.EventBgCleaned:
			stage, status = "background.cleaned", "cleaned"
		}
		if stage == "" || !inWindow(event.Time, asked) || latest[stage].At.After(event.Time) {
			continue
		}
		entry := base
		entry.Stage, entry.Status, entry.At = stage, status, event.Time.UTC()
		if id := event.Text(telemetry.AttrBgID); runID.MatchString(id) {
			entry.BackgroundID = id
		}
		latest[stage] = entry
	}
	rows := make([]Event, 0, len(latest))
	for _, item := range latest {
		rows = append(rows, item)
	}
	return rows
}
