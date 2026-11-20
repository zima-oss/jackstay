// Package receipts is `jst test -- COMMAND`, `jst test last` and `jst receipts stats|key|list`
// (docs/design/implementation.md sections 5.4 and 9): a heavy
// test command run in a process group of its own given the terminal (under the machine-wide lock
// when the project or the call turns it on), judged from its output, recorded in the run log, its
// output kept, and a receipt stored for a counted pass keyed by the tree.
//
// What `jst test <scope>` (internal/testing) reuses is exported: the key (KeyOf), the store of
// receipts and failure notes (Store), the run log's writer and readers (RunLog, ReadRecords), the
// kept output (KeepOutput, NotableLines), the judge a run is fed through (Judge, Evidence) and the
// words of the outcome line (Say).
package receipts

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/zima-oss/jackstay/internal/homedir"
)

// Schema is the receipt's schema.
const Schema = 3

// Exit statuses of jst test's own.
const (
	StatusUnusable   = 2
	StatusVeto       = 1
	StatusGaveUp     = 75
	StatusNothingRun = 76
	StatusUnjudged   = 77
	// StatusPassedOnRetry is a run whose tests all passed, some only on a retry: not a clean pass.
	StatusPassedOnRetry = 78
	StatusNoStart       = 127
)

// Outcomes the last `jst test:` line names.
const (
	OutcomePassed         = "passed"
	OutcomeExited0        = "exited-0"
	OutcomeReused         = "reused"
	OutcomeFailed         = "failed"
	OutcomeNothingRun     = "nothing-run"
	OutcomeUnjudged       = "unjudged"
	OutcomePassedOnRetry  = "passed-on-retry"
	OutcomeStopped        = "stopped"
	OutcomeStoppedWaiting = "stopped-waiting"
	OutcomeGaveUp         = "gave-up"
	OutcomeNotStarted     = "not-started"
)

// Defaults of the receipts section.
const (
	DefaultMaxAgeHours = 72
	// markerFloor is the youngest a receipt or note can be pruned at, whatever max_age_hours says.
	markerFloor = 24 * time.Hour
	// clockSkew is how far in the future a receipt or a pass may seem and still count.
	clockSkew = 300 * time.Second
)

// DefaultNoReuseBranches are the branches where a command always runs.
var DefaultNoReuseBranches = []string{"main", "master"}

// Clock tells the time (W-013: tests give their own); supervise.Clock is one.
type Clock interface {
	Now() time.Time
}

// Say writes one of jst test's lines on its standard error: `jst test: WORDS`.
func Say(stderr io.Writer, format string, values ...any) {
	fmt.Fprintf(stderr, "jst test: "+format+"\n", values...)
}

// StateFolder is where a project's per-key state lives: `<jackstay home>/<project>/<parts…>`
// (receipts, failure notes, gate marks and bars).
func StateFolder(project string, parts ...string) (string, error) {
	return homedir.ProjectPath(project, parts...)
}

// epochOf is a time as the log writes it: seconds since 1970, to the millisecond.
func epochOf(moment time.Time) float64 {
	return roundTo(float64(moment.UnixNano())/1e9, 3)
}

// roundTo rounds to a number of decimals.
func roundTo(value float64, decimals int) float64 {
	scale := 1.0
	for range decimals {
		scale *= 10
	}
	if value < 0 {
		return -float64(int64(-value*scale+0.5)) / scale
	}
	return float64(int64(value*scale+0.5)) / scale
}

// secondsBetween is a span in seconds to the tenth, as the record holds it.
func secondsBetween(start, end time.Time) float64 {
	return roundTo(end.Sub(start).Seconds(), 1)
}

// timeText is a moment as the logs and receipts write it (2026-11-16T14:00:00+0200).
func timeText(moment time.Time) string { return moment.Format("2006-01-02T15:04:05-0700") }

// realPath is a path with its links resolved, or as given when that fails.
func realPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
