package judge

import (
	"fmt"
	"strconv"
	"strings"
)

// End is how the command ended and what the run left, known after the output was read.
type End struct {
	// Status is the command's own status (128+N for a signal N).
	Status int
	// TimedOut and Signal say that jackstay stopped it: at its timeout, or for a signal sent to jackstay
	// (its name). Such a status is no failure.
	TimedOut bool
	Signal   string
	// ReadProblem says why the output was not read to its end; empty when it was.
	ReadProblem string
	// LeftRunning names the processes the command left after a normal end, which were stopped.
	LeftRunning string
	Leftovers   Leftovers
}

// Leftovers are what the run left behind (testing.md section 6), entries with `"fail": true`.
type Leftovers struct {
	// Failed are the run's own: they fail it.
	Failed []string
	// Unattributed another run may have made (Overlap names the runs): unjudged.
	Unattributed []string
	Overlap      []string
	// Unreadable could not be looked for: unjudged.
	Unreadable []string
}

// Counts are what the output counted.
type Counts struct {
	// Counted is the tests counted: `swift-test`'s count, else the evidence count.
	Counted int
	// Counting is false for a judge that counts nothing (`exit` with no evidence pattern).
	Counting       bool
	BundlesStarted int
	BundlesEnded   int
	XCTest         int
	SwiftTesting   int
	Skipped        int
	// EvidenceCount is the evidence pattern's sum, for any judge.
	EvidenceCount int
}

// A Verdict is a run's outcome and what it rests on.
type Verdict struct {
	Outcome Outcome
	// Why holds the failures of a failed run, or why an unjudged run cannot be judged.
	Why string
	// Failure is the output's first failing line (a case, a suite, a bundle, a Swift Testing test).
	Failure  string
	VetoLine string
	Counts
	judge Kind
}

// Verdict combines the reading with how the command ended, by the order of verdicts. Lines read
// after it do not change it.
func (reader *Reader) Verdict(end End) Verdict {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	verdict := Verdict{judge: reader.kind}
	var unjudged []string
	if reader.swift != nil {
		verdict.Counts, unjudged = reader.swift.counts()
		verdict.Counting = true
		verdict.Failure = reader.swift.failure
	} else {
		verdict.Counted, verdict.Counting = reader.evidenceCount, reader.evidence != nil
	}
	verdict.EvidenceCount = reader.evidenceCount
	vetoed, vetoLine := reader.vetoed, reader.vetoLine
	stopped := end.TimedOut || end.Signal != ""
	if stopped && reader.stopping {
		verdict.Failure, vetoed, vetoLine = reader.failureAtStop, reader.vetoedAtStop, reader.vetoAtStop
	}
	if vetoed {
		verdict.VetoLine = vetoLine
	}

	var failures []string
	if !stopped && end.Status != 0 {
		failures = append(failures, "exit "+strconv.Itoa(end.Status))
	}
	if verdict.Failure != "" {
		failures = append(failures, verdict.Failure)
	}
	if vetoed {
		failures = append(failures, "a veto line: "+vetoLine)
	}
	if len(end.Leftovers.Failed) > 0 {
		failures = append(failures, "left behind: "+strings.Join(firstThree(end.Leftovers.Failed), ", "))
	}
	switch {
	case len(failures) > 0:
		return verdict.with(Failed, failures)
	case end.TimedOut:
		return verdict.with(TimedOut, nil)
	case stopped:
		return verdict.with(Stopped, nil)
	}

	var why []string
	switch {
	case end.ReadProblem != "":
		why = append(why, end.ReadProblem)
	case reader.notNumber:
		why = append(why, fmt.Sprintf("the evidence pattern matched a line whose group is not a number: '%s'", reader.uncounted))
	}
	if len(unjudged) > 0 {
		why = append(why, strings.Join(unjudged, "; "))
	}
	if end.LeftRunning != "" {
		why = append(why, "the command ended and left processes running, which were stopped: "+end.LeftRunning)
	}
	if len(end.Leftovers.Unattributed) > 0 {
		why = append(why, fmt.Sprintf("leftovers that another run may have made (runs overlapped: %s): %s",
			strings.Join(firstThree(end.Leftovers.Overlap), ", "), strings.Join(firstThree(end.Leftovers.Unattributed), ", ")))
	}
	if len(end.Leftovers.Unreadable) > 0 {
		why = append(why, "leftovers cannot be looked for: "+strings.Join(firstThree(end.Leftovers.Unreadable), "; "))
	}
	switch {
	case len(why) > 0:
		return verdict.with(Unjudged, why)
	case reader.kind == Exit:
		return verdict.with(ExitedZero, nil)
	case verdict.Counted <= 0:
		return verdict.with(NothingRun, nil)
	}
	return verdict.with(Passed, nil)
}

func (verdict Verdict) with(outcome Outcome, why []string) Verdict {
	verdict.Outcome = outcome
	verdict.Why = strings.Join(why, "; ")
	return verdict
}

// Detail is the counts as the outcome line's detail:
// `counted=2290 bundles=9/9 xctest=2290 swift-testing=0 skipped=1` for `swift-test`,
// `counted=N` (or `counted=-` when nothing counts) for the others.
func (verdict Verdict) Detail() string {
	if verdict.judge != SwiftTest {
		if !verdict.Counting {
			return "counted=-"
		}
		return "counted=" + strconv.Itoa(verdict.Counted)
	}
	return fmt.Sprintf("counted=%d bundles=%d/%d xctest=%d swift-testing=%d skipped=%d",
		verdict.Counted, verdict.BundlesEnded, verdict.BundlesStarted, verdict.XCTest, verdict.SwiftTesting, verdict.Skipped)
}

func firstThree(items []string) []string {
	return items[:min(len(items), 3)]
}
