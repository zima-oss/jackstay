// Package timeline projects local journal spans into delivery stages.
package timeline

import (
	"cmp"
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/inventory"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/genai"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/telemetry/query"
)

const usage = `usage: jst timeline [--since DURATION] [--until RFC3339] [--limit N]

Print local delivery stages for this Git repository as versioned JSON.
  --since DURATION  window before --until (default 24h, maximum 720h from now)
  --until RFC3339   exclusive end of the window (default now)
  --limit N         keep at most N of the newest rows (default 100, maximum 1000)

exit: 0 report written; 1 incomplete journal or ancestry scan; 2 bad request or repository unavailable.

example: jst timeline --since 4h --limit 50`

const (
	defaultLimit    = 100
	maximumLimit    = 1000
	maximumWindow   = 30 * 24 * time.Hour
	maximumAncestry = 8192
	maximumLine     = 16 * journal.LineLimit
)

func init() {
	cli.Register(cli.Command{Name: "timeline", Summary: "local PR, test, review and background stages as JSON", Usage: usage, Run: run})
}

type window struct {
	from, until time.Time
	limit       int
}

type report struct {
	Schema       int       `json:"schema"`
	RepositoryID string    `json:"repository_id"`
	Since        time.Time `json:"since"`
	Until        time.Time `json:"until"`
	Rows         []row     `json:"rows"`
	OmittedRows  int       `json:"omitted_rows"`
	ScanPartial  bool      `json:"scan_partial"`
	Unreadable   int       `json:"unreadable_lines"`
	ReadProblems int       `json:"read_problems"`
}

type row struct {
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
	Tokens       *tokens   `json:"tokens,omitempty"`
}

type tokens struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CachedInput int64 `json:"cached_input"`
}

func run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	now := time.Now()
	asked, err := readWindow(args, now, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	current, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "jst timeline:", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	scope, err := inventory.OpenJournalScope(ctx, current)
	if err != nil {
		fmt.Fprintln(stderr, "jst timeline:", err)
		return 2
	}
	found := buildReport(scope, asked, now)
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(found); err != nil {
		fmt.Fprintln(stderr, "jst timeline:", err)
		return 2
	}
	if found.ScanPartial || found.Unreadable > 0 || found.ReadProblems > 0 {
		fmt.Fprintln(stderr, "jst timeline: incomplete journal or ancestry scan; see the report counters")
		return 1
	}
	return 0
}

func readWindow(args []string, now time.Time, stderr io.Writer) (window, error) {
	flags := flag.NewFlagSet("timeline", flag.ContinueOnError)
	flags.SetOutput(stderr)
	since := flags.Duration("since", 24*time.Hour, "duration")
	end := flags.String("until", "", "RFC3339 time")
	limit := flags.Int("limit", defaultLimit, "maximum rows")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *since <= 0 || *since > maximumWindow ||
		*limit < 1 || *limit > maximumLimit {
		return window{}, errors.New(usage)
	}
	until := now
	if *end != "" {
		moment, err := time.Parse(time.RFC3339, *end)
		if err != nil || moment.After(now) {
			return window{}, errors.New("jst timeline: --until must be an RFC3339 time at or before now")
		}
		until = moment
	}
	from := until.Add(-*since)
	if now.Sub(from) > maximumWindow {
		return window{}, errors.New("jst timeline: the window must begin no more than 720h ago")
	}
	return window{from: from.UTC(), until: until.UTC(), limit: *limit}, nil
}

type spanKey struct{ trace, span string }

// observed keeps only projected fields. Raw journal objects may contain arguments and prompts.
type observed struct {
	key, parent spanKey
	explicit    bool
	location    string
	worktree    string
	ended       time.Time
	rows        []row
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

func buildReport(scope inventory.JournalScope, asked window, now time.Time) report {
	result := report{Schema: 1, RepositoryID: scope.RepositoryID, Since: asked.from, Until: asked.until, Rows: []row{}}
	retained := observations{}
	read := journal.ReadBounded(scope.Folders, asked.from, asked.until, now, maximumLine, func(span journal.Span) {
		key := spanKey{trace: hexID(span.TraceID, 32), span: hexID(span.SpanID, 16)}
		if key.trace == "" || key.span == "" {
			return
		}
		projected := observed{key: key, ended: span.End}
		if retained.Len() == maximumAncestry {
			result.ScanPartial = true
			if compareObserved(projected, retained[0]) <= 0 {
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
		projected.rows = projectRows(span, projected, asked)
		if retained.Len() == maximumAncestry {
			retained[0] = projected
			heap.Fix(&retained, 0)
		} else {
			heap.Push(&retained, projected)
		}
	})
	result.Unreadable, result.ReadProblems = read.Unreadable, len(read.Problems)
	bySpan := make(map[spanKey]observed, retained.Len())
	byPath := make(map[string]string)
	for _, item := range retained {
		if item.location != "" {
			owner, cached := byPath[item.location]
			if !cached {
				owner = scope.WorktreeID(item.location)
				byPath[item.location] = owner
			}
			item.worktree = owner
		}
		bySpan[item.key] = item
	}
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
			result.Rows = append(result.Rows, point)
		}
	}
	slices.SortFunc(result.Rows, func(left, right row) int {
		return cmp.Or(left.At.Compare(right.At), cmp.Compare(left.TraceID, right.TraceID),
			cmp.Compare(left.SpanID, right.SpanID), cmp.Compare(left.Stage, right.Stage))
	})
	if len(result.Rows) > asked.limit {
		result.OmittedRows = len(result.Rows) - asked.limit
		result.Rows = result.Rows[result.OmittedRows:]
	}
	return result
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

func projectRows(span journal.Span, item observed, asked window) []row {
	base := row{TraceID: item.key.trace, SpanID: item.key.span, ParentSpanID: item.parent.span,
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
	rows := []row{}
	if stage := stageOf(span); stage != "" && inWindow(span.End, asked) {
		entry := base
		entry.Stage, entry.Status, entry.At = stage, statusOf(span), span.End.UTC()
		entry.DurationMS = max(0, span.Duration().Milliseconds())
		if usage, ok := genai.AgentRunOf(span); ok && len(usage.Turns) == 1 {
			total := usage.Turns[0]
			if total.Input > 0 || total.Output > 0 || total.CachedInput > 0 {
				entry.Tokens = &tokens{Input: max(0, total.Input), Output: max(0, total.Output), CachedInput: max(0, total.CachedInput)}
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
		switch operation := span.Text(telemetry.AttrGitHubOperation); operation {
		case "ci-wait", "ci-status", "run-wait", "pr-find", "pr-status", "pr-land":
			return "github." + operation
		default:
			return "github.command"
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
	case "ok", "passed", "failed", "pending", "reused", "resumed", "would-hit", "exited-0", "nothing-run", "passed-on-retry",
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

func backgroundEvents(span journal.Span, base row, asked window) []row {
	// A long command may report the same lifecycle stage repeatedly. Retain the
	// latest point for each kind on a span, so even those projections stay bounded.
	latest := map[string]row{}
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
	rows := make([]row, 0, len(latest))
	for _, item := range latest {
		rows = append(rows, item)
	}
	return rows
}
