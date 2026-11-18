package scopes

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/receipts"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// tailLines is how many of a failed check's last lines are shown.
const tailLines = 5

// tail keeps the last lines of a check's output, both streams together.
type tail struct {
	mutex sync.Mutex
	lines []string
}

func (kept *tail) Line(_ supervise.Stream, text string) error {
	kept.mutex.Lock()
	defer kept.mutex.Unlock()
	kept.lines = append(kept.lines, text)
	if len(kept.lines) > tailLines {
		kept.lines = kept.lines[len(kept.lines)-tailLines:]
	}
	return nil
}

func (kept *tail) text() []string {
	kept.mutex.Lock()
	defer kept.mutex.Unlock()
	var found []string
	for _, line := range kept.lines {
		if strings.TrimSpace(line) != "" {
			found = append(found, line)
		}
	}
	return found
}

// checkResult is how a `requires` check ended.
type checkResult struct {
	status    int
	timedOut  bool
	tail      []string
	session   int
	killed    string
	survivors string
	blind     bool
	// asked is a request to stop jackstay that came while the check ran (0: none).
	asked syscall.Signal
}

// passed says the check answered yes: it ended 0 by itself, within its timeout, and nothing of it
// is left that could not be stopped (nor is that unknown).
func (result checkResult) passed() bool {
	return !result.timedOut && result.status == 0 && !result.blind && result.survivors == "" && result.asked == 0
}

// runCheck runs a `requires` check in a session of its own, stopped whole at its timeout like a
// run's command (jst test's own requests to stop reach it too).
func (proj *project) runCheck(each check, cwd string, env []string) checkResult {
	kept := &tail{}
	spec := supervise.Spec{Argv: each.run, Env: env, Dir: cwd, Mode: supervise.Session, Output: kept,
		Grace: supervise.SessionGrace, Settle: settle, Say: proj.stderr, Who: "jst test", Clock: proj.clock, System: proj.system,
		RecordSessionIn: proj.getenv(supervise.SessionsFileVariable)}
	timeout := time.Duration(each.timeout * float64(time.Second))
	ran, err := proj.launch(proj.signals, launch{spec: spec, timeout: timeout})
	if err != nil {
		return checkResult{status: receipts.StatusNoStart, tail: []string{err.Error()}}
	}
	result := checkResult{status: ran.outcome.Status(), timedOut: ran.timedOut, tail: kept.text(), session: ran.outcome.PID,
		survivors: survivorNames(ran.outcome.Survivors), blind: blindOf(proj.system, ran.outcome)}
	result.killed = ran.killed
	if len(ran.outcome.Left) > 0 {
		result.killed = joinNames(result.killed, supervise.Names(ran.outcome.Left))
	}
	if ran.outcome.StoppedBy != 0 {
		result.asked = ran.outcome.StoppedBy
	}
	return result
}

func joinNames(first, second string) string {
	switch {
	case first == "":
		return second
	case second == "":
		return first
	}
	return first + "," + second
}

// checkWhy is why a check that did not pass stops the run, after the check's own `why`.
func checkWhy(each check, result checkResult) string {
	switch {
	case result.asked != 0:
		return each.why + " (jst test was asked to stop (" + supervise.SignalName(result.asked) + ") while the check ran)"
	case result.timedOut && result.survivors != "":
		return fmt.Sprintf("%s (the check timed out after %s s; its stop is not confirmed, still running: %s)", each.why,
			formatSeconds(each.timeout), result.survivors)
	case result.timedOut:
		return fmt.Sprintf("%s (the check timed out after %s s)", each.why, formatSeconds(each.timeout))
	case result.survivors != "":
		return fmt.Sprintf("%s (the check left processes that could not be stopped: %s)", each.why, result.survivors)
	case result.blind && result.status == 0:
		return each.why + " (the process table cannot be read: that the check left nothing running cannot be confirmed)"
	}
	return fmt.Sprintf("%s (the check ended %d)", each.why, result.status)
}

// formatSeconds is a number of seconds as people read it (600, 0.5).
func formatSeconds(seconds float64) string { return strconv.FormatFloat(seconds, 'g', -1, 64) }
