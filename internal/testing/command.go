package scopes

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/judge"
	"github.com/zima-oss/jackstay/internal/receipts"
	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/testreport"
	"github.com/zima-oss/jackstay/internal/version"
)

// readSlack is how far after the command's end a leftover may be born and still be its.
const readSlack = 500 * time.Millisecond

// ran is one command's run: its record's facts and what it left to say.
type ran struct {
	*leafRun
	runID    string
	runTmp   string
	started  time.Time
	ended    time.Time
	kept     *receipts.Kept
	facts    map[string]any
	setAside bool
	veto     *regexp.Regexp
}

// command runs the leaf's words in a session of their own, judges and records the run, and
// stores a receipt for a counted pass of a keyed tree.
func (run *leafRun) command(asked time.Time, held *flock.Held) result {
	if problem := run.pinnedProblem(); problem != "" {
		return run.notStarted(problem)
	}
	did := &ran{leafRun: run}
	if run.keyErr == nil && run.storeOK {
		// The command is about to run for this key: its receipt stops speaking for the tree now.
		removed, err := run.store.Remove(run.key.Value)
		if err != nil {
			run.say("%v", err)
		}
		did.setAside = removed
	}
	did.runID = receipts.RunID(run.clock.Now(), run.pid)
	runTmp, err := os.MkdirTemp(run.tmpdir, "jackstay-run-"+did.runID+".")
	if err != nil {
		return run.notStarted(fmt.Sprintf("no run folder could be made in %s (%v)", run.tmpdir, err))
	}
	did.runTmp = runTmp
	options := run.judgeOptions()
	reader, err := judge.New(options)
	if err != nil {
		_ = os.RemoveAll(runTmp)
		return run.notStarted("jackstay.json cannot be used: " + err.Error())
	}
	if len(options.Vetoes) > 0 {
		did.veto, _ = regexp.Compile(options.Vetoes[0])
	}
	env := setEnv(setEnv(run.env, supervise.RunIDVariable, did.runID), supervise.RunProjectVariable, run.name)
	env = setEnv(setEnv(env, RunIDVariable, did.runID), RunTmpVariable, runTmp)
	argv := run.words.argv
	var report *testreport.Report
	if testreport.FileJudge(string(options.Kind)) {
		names := run.leaf.reports
		if run.leaf.report != "" {
			names = []string{run.leaf.report}
		}
		report, err = testreport.OpenReports(runTmp, names)
		if err != nil {
			_ = os.RemoveAll(runTmp)
			return run.notStarted("the private test report could not be prepared: " + err.Error())
		}
		defer func() { _ = report.Close() }()
		values := map[string]string{}
		if run.leaf.report != "" {
			env = setEnv(env, testreport.PathVariable, report.Path())
			values["report"] = report.Path()
		} else {
			env = setEnv(env, testreport.DirectoryVariable, report.Directory())
			values["report_dir"] = report.Directory()
		}
		argv = slices.Clone(argv)
		for index := 1; index < len(argv); index++ {
			argv[index] = replacePlaceholders(argv[index], values)
		}
	}
	scopeFacts := map[string]any{"judge": run.leaf.judge, "timeout": nil}
	if version := judge.AdapterVersion(options.Kind); version != 0 {
		scopeFacts["adapter_version"] = version
	}
	for name, value := range run.facts {
		scopeFacts[name] = value
	}
	if run.leaf.timeout > 0 {
		scopeFacts["timeout"] = run.leaf.timeout
	}
	if len(run.leaf.env) > 0 {
		scopeFacts["env"] = run.leaf.env
	}
	if run.words.quick != "" {
		scopeFacts["quick"] = run.words.quick
		run.say("quick=%s", run.words.quick)
	}
	did.kept, err = receipts.KeepOutput(run.name, did.runID, run.logPaths(), run.clock.Now())
	if err != nil {
		run.say("the output of this run is not kept (%v); `jst test last` will not have it.", err)
	}
	locked := held != nil || run.getenv(flock.HeldVariable) != ""
	var evidence, output any
	if run.leaf.evidence != "" {
		evidence = run.leaf.evidence
	}
	if did.kept != nil {
		output = did.kept.Path
	}
	did.facts = map[string]any{"run_id": did.runID, "pid": run.pid, "locked": locked, "evidence": evidence,
		"receipt_removed": did.setAside, "output": output, "run_tmp": runTmp}
	seen := newLeftovers(leftoverRules(run.section), did.runID, runTmp, run.tmpdir, func(started, ended time.Time) []string {
		return overlappingRuns(receipts.ReadRecords(run.logPaths()), did.runID, run.runLog.Inside, started, ended, run.clock.Now(), run.alive)
	})
	did.started = run.clock.Now()
	scopeFacts["lock_waited_seconds"] = secondsBetween(asked, did.started)
	for name, value := range scopeFacts {
		did.facts[name] = value
	}
	startFields := map[string]any{"run_id": did.runID, "pid": run.pid, "locked": locked, "started_epoch": epochText(did.started)}
	for name, value := range scopeFacts {
		startFields[name] = value
	}
	if !run.log(receipts.EventStart, run.words.argv, run.keyValue(), startFields) {
		run.notLogged("the start of this run")
	}
	spec := supervise.Spec{Argv: argv, Env: env, Dir: run.runCwd, Mode: supervise.Session,
		Output: &sink{stdout: run.stdout, stderr: run.stderr, kept: did.kept, reader: reader}, Grace: supervise.SessionGrace,
		Settle: settle, Say: run.stderr, Who: "jst test", Clock: run.clock, System: run.system,
		RecordSessionIn: run.getenv(supervise.SessionsFileVariable),
		Started: func(pid int) {
			if held != nil {
				held.WriteNote(flock.NoteWords(fmt.Sprintf("project %s: %s in %s", run.name, joinSpace(run.words.argv), run.root),
					fmt.Sprintf("the command's session %d", pid), run.pid, did.started))
			}
		}}
	timeout := time.Duration(run.leaf.timeout * float64(time.Second))
	done := make(chan struct{})
	watching := watchStops(run.signals, reader, done)
	launchedRun, runErr := run.launch(run.signals, launch{spec: spec, timeout: timeout, stopping: func() {
		reader.Stopping()
		run.say("the run reached its timeout of %s s; stopping the whole run (SIGTERM, then SIGKILL after %s).",
			formatSeconds(run.leaf.timeout), supervise.Seconds(supervise.SessionGrace))
	}})
	close(done)
	_ = watching.Wait()
	did.ended = run.clock.Now()
	if !launchedRun.outcome.Ended.IsZero() {
		did.ended = launchedRun.outcome.Ended
	}
	var startErr *supervise.StartError
	if errors.As(runErr, &startErr) || runErr != nil {
		run.say("%v", runErr)
		_ = os.RemoveAll(runTmp)
		return did.finish(receipts.OutcomeFailed, receipts.StatusNoStart, map[string]any{"status": receipts.StatusNoStart},
			fmt.Sprintf("outcome=failed (exit %d: the command could not be started, %s s)", receipts.StatusNoStart,
				formatSeconds(secondsBetween(did.started, did.ended))))
	}
	if report != nil && !launchedRun.timedOut && launchedRun.outcome.StoppedBy == 0 && run.signals.Context().Err() == nil {
		reader.ReadReport(report)
	}
	return did.judged(launchedRun, reader, seen)
}

func (run *leafRun) keyValue() string {
	if run.keyErr != nil {
		return ""
	}
	return run.key.Value
}

// judged turns how the command ended, what it printed and what it left into the outcome.
func (did *ran) judged(launchedRun launched, reader *judge.Reader, seen *leftovers) result {
	outcome := launchedRun.outcome
	status := outcome.Status()
	first, firstAt, asked := did.firstStop(did.signals)
	if outcome.StoppedBy != 0 {
		first, asked = outcome.StoppedBy, true
	}
	// A request to stop that came after the command ended by itself keeps that end.
	endedByItself := asked && !outcome.Ended.IsZero() && firstAt.After(outcome.Ended)
	blind := blindOf(did.system, outcome)
	readProblem := outcome.Unread
	switch {
	case blind:
		why := "the process table could not be read, so that the run's process tree ended cannot be confirmed"
		if readProblem != "" {
			why += "; " + readProblem
		}
		readProblem = why
	case readProblem == "" && len(outcome.Problems) > 0:
		readProblem = "the end of the run's process tree cannot be confirmed: " + outcome.Problems[0].Error()
	}
	found := seen.judge(did.started, did.ended.Add(readSlack))
	end := judge.End{Status: status, TimedOut: launchedRun.timedOut, ReadProblem: readProblem,
		LeftRunning: supervise.Names(outcome.Left), Leftovers: judge.Leftovers{Failed: found.failed,
			Unattributed: found.unattributed, Overlap: found.overlap, Unreadable: found.unreadable}}
	if asked && !launchedRun.timedOut && (!endedByItself || status == 0) {
		end.Signal = supervise.SignalName(first)
	}
	verdict := reader.Verdict(end)
	if problem := did.pinnedProblem(); problem != "" && !asked {
		verdict = judge.Verdict{Outcome: judge.Unjudged, Why: problem}
	}
	more := map[string]any{"status": status, "counted": nil}
	if verdict.Counting {
		// evidence_count too: the run log's readers (stats, would-hit) count passes by it.
		more["counted"], more["evidence_count"] = verdict.Counted, verdict.Counted
	}
	if did.leaf.judge == "swift-test" {
		more["bundles"] = fmt.Sprintf("%d/%d", verdict.BundlesEnded, verdict.BundlesStarted)
		more["xctest"], more["swift_testing"], more["skipped"] = verdict.XCTest, verdict.SwiftTesting, verdict.Skipped
	}
	if judge.AdapterVersion(judge.Kind(did.leaf.judge)) != 0 && verdict.Counting {
		more["skipped"] = verdict.Skipped
	}
	if launchedRun.timedOut {
		more["timed_out"] = true
		more["killed"] = launchedRun.killed
	}
	if len(outcome.Survivors) > 0 {
		more["survivors"] = survivorNames(outcome.Survivors)
	}
	if len(outcome.Left) > 0 {
		more["left_running"] = supervise.Names(outcome.Left)
	}
	if blind {
		more["tree_unconfirmed"] = true
	}
	if recorded := found.record(); recorded != nil {
		more["leftovers"] = recorded
	}
	if verdict.Why != "" {
		more["why"] = verdict.Why
	}
	if seen.holdsRunTmp(found) {
		more["run_tmp_kept"] = did.runTmp
	} else {
		_ = os.RemoveAll(did.runTmp)
	}
	if len(found.listed) > 0 {
		did.say("left behind (listed, not judged): %s", strings.Join(found.listed[:min(10, len(found.listed))], ", "))
	}
	if did.leaf.durations && verdict.Outcome == judge.Passed {
		switch judge.Kind(did.leaf.judge) {
		case judge.GoJSON:
			samples, truncated := reader.GoDurations()
			recordCaseDurations(more, samples, truncated)
		case judge.JUnit:
			samples, truncated := reader.JUnitDurations()
			recordCaseDurations(more, samples, truncated)
		case judge.XCResultJSON:
			samples, truncated := reader.XCResultDurations()
			recordCaseDurations(more, samples, truncated)
		}
	}
	if did.leaf.durations && did.leaf.judge == string(judge.SwiftTest) && (verdict.Outcome == judge.Passed || verdict.Outcome == judge.Failed) {
		scope := did.leaf.scope
		if scope == "" {
			scope = did.leaf.name
		}
		if err := recordDurations(did.name, did.runID, scope, did.runLog.Where.Head, reader.Durations(), did.clock.Now()); err != nil {
			did.say("the durations of this run could not be recorded (%v)", err)
		}
	}
	detail := verdict.Detail()
	if did.words.quick != "" {
		detail += " quick=" + did.words.quick
	}
	took := formatSeconds(secondsBetween(did.started, did.ended))
	if asked {
		return did.stopped(verdict, first, endedByItself && status != 0, status, more, took)
	}
	switch verdict.Outcome {
	case judge.Passed:
		stored, whyNot, kind := did.storeReceipt(verdict.Counted)
		more["stored"], more["not_stored"] = stored != "", nullableText(whyNot)
		if kind != "" {
			more["not_stored_kind"] = kind
		}
		if stored != "" {
			did.say("passed; receipt stored: %s", stored)
		}
		return did.finish(receipts.OutcomePassed, 0, more, fmt.Sprintf("outcome=passed (%s s, %s)", took, detail))
	case judge.ExitedZero:
		more["stored"], more["not_stored"] = false, "judge `exit` counts no tests"
		more["not_stored_kind"] = receipts.NotStoredNoEvidence
		did.say("the command ended 0; its judge is `exit`, which counts no tests: this is not a pass.")
		return did.finish(receipts.OutcomeExited0, 0, more, fmt.Sprintf("outcome=exited-0 (no test count: not proof that tests ran), %s s", took))
	case judge.Failed:
		exitStatus := 1
		if status != 0 && !launchedRun.timedOut {
			exitStatus = status
		}
		return did.finish(receipts.OutcomeFailed, exitStatus, more,
			fmt.Sprintf("outcome=failed (exit %d, %s s; %s; %s)", exitStatus, took, verdict.Why, detail))
	case judge.TimedOut:
		words := fmt.Sprintf("outcome=timed-out (exit %d: the run passed its timeout of %s s and was stopped; killed=%s",
			StatusTimedOut, formatSeconds(did.leaf.timeout), orNothing(launchedRun.killed))
		if survivors, present := more["survivors"]; present {
			words += fmt.Sprintf("; survivors=%v", survivors)
		}
		return did.finish(outcomeTimedOut, StatusTimedOut, more, words+"; "+detail+"; no receipt)")
	case judge.Unjudged:
		return did.finish(receipts.OutcomeUnjudged, receipts.StatusUnjudged, more,
			fmt.Sprintf("outcome=unjudged (exit %d: %s; %s s; %s)", receipts.StatusUnjudged, verdict.Why, took, detail))
	case judge.PassedOnRetry:
		more["why"] = verdict.Why
		return did.finish(receipts.OutcomePassedOnRetry, receipts.StatusPassedOnRetry, more,
			fmt.Sprintf("outcome=passed-on-retry (exit %d: %s, not a clean pass; %s s; %s; no receipt)",
				receipts.StatusPassedOnRetry, verdict.Why, took, detail))
	}
	return did.finish(receipts.OutcomeNothingRun, receipts.StatusNothingRun, more,
		fmt.Sprintf("outcome=nothing-run (exit %d: no test was counted; %s s; %s)", receipts.StatusNothingRun, took, detail))
}

func orNothing(text string) string {
	if text == "" {
		return "nothing"
	}
	return text
}

// stopped ends a run jackstay was asked to stop: `failed` when a failure was seen before the stop (or
// the command had ended by itself with a status that is not 0), else `stopped`; jackstay then ends by
// the signal.
func (did *ran) stopped(verdict judge.Verdict, sig syscall.Signal, failedAlone bool, status int, more map[string]any, took string) result {
	name := supervise.SignalName(sig)
	more["signal"], more["status"] = name, 128+int(sig)
	var ended result
	if verdict.Outcome == judge.Failed {
		failure := verdict.Why
		if failedAlone {
			exit := fmt.Sprintf("exit %d", status)
			failure = strings.Replace(failure, exit, exit+" (the command had ended by itself)", 1)
		}
		more["why"] = failure
		ended = did.finish(receipts.OutcomeFailed, 128+int(sig), more,
			fmt.Sprintf("outcome=failed (a failure was seen before the stop: %s; %s after %s s)", failure, name, took))
	} else {
		ended = did.finish(receipts.OutcomeStopped, 128+int(sig), more,
			fmt.Sprintf("outcome=stopped (%s after %s s; the whole run was stopped; no receipt)", name, took))
	}
	ended.signalled = true
	return ended
}

// finish records the run and says its last lines: the failure note and the receipt taken away for
// a run that did not pass, the run's line, what went wrong, where the output is, the outcome.
func (did *ran) finish(outcome string, status int, more map[string]any, words string) result {
	notAPass := outcome != receipts.OutcomePassed && outcome != receipts.OutcomeExited0
	if notAPass && did.keyErr == nil && did.storeOK {
		did.store.NoteFailure(did.key.Value, outcome)
		if removed, err := did.store.Remove(did.key.Value); err == nil && removed {
			did.setAside = true
		}
	}
	seconds := secondsBetween(did.started, did.ended)
	fields := map[string]any{"outcome": outcome, "started_epoch": epochText(did.started), "ended_epoch": epochText(did.ended),
		"seconds": seconds}
	for name, value := range did.facts {
		fields[name] = value
	}
	fields["receipt_removed"] = did.setAside
	for name, value := range more {
		fields[name] = value
	}
	written := did.log(receipts.EventRun, did.words.argv, did.keyValue(), fields)
	whole := did.kept.Close()
	if did.kept != nil {
		if notAPass {
			lines, extra := receipts.NotableLines(did.kept.Path, did.veto)
			receipts.WriteNotable(did.stderr, "jst test: ", lines, extra)
		}
		if whole {
			did.say("the whole output: %s  (`jst test last` shows this again)", did.kept.Path)
		} else {
			did.say("the kept output is not whole (writing it failed): %s", did.kept.Path)
		}
	}
	if !written {
		did.notLogged("this run")
	}
	did.say("%s", words)
	counted, _ := more["counted"].(int)
	return result{outcome: outcome, status: status, seconds: seconds, runID: did.runID, counted: counted}
}

// storeReceipt writes the receipt of a counted pass when every rule allows it: (path, why not,
// the kind of why not).
func (did *ran) storeReceipt(counted int) (stored, whyNot, kind string) {
	if did.keyErr != nil {
		return "", did.keyErr.Error(), receipts.NotStoredNoKey
	}
	if !did.storeOK {
		return "", "the receipt store cannot be found", receipts.NotStoredWriteFailed
	}
	after, err := receipts.KeyOf(did.ctx, did.keyed)
	if err != nil {
		return "", err.Error(), receipts.NotStoredNoKey
	}
	if after.Value != did.key.Value {
		return "", "the tree changed while the command ran", receipts.NotStoredTreeChanged
	}
	var rewritten []string
	for name, stamp := range after.Stamps {
		if did.key.Stamps[name] != stamp {
			rewritten = append(rewritten, name)
		}
	}
	if len(rewritten) > 0 {
		sort.Strings(rewritten)
		return "", "an input file was rewritten while the command ran (its content is the same now): " +
			strings.Join(rewritten[:min(3, len(rewritten))], ", "), receipts.NotStoredInputRewritten
	}
	if failed, found := did.store.FailureTime(did.key.Value); found && !failed.Before(did.started) {
		return "", "another run of this same tree failed after this one had started", receipts.NotStoredFailedSince
	}
	where := did.runLog.Where
	receipt := receipts.Receipt{Schema: receipts.Schema, Key: did.key.Value, Outcome: receipts.OutcomePassed, Project: did.name,
		Command: slices.Clone(did.words.argv), Seconds: secondsBetween(did.started, did.ended), EvidenceCount: counted,
		Started: did.started.Format("2006-01-02T15:04:05-0700"), StartedEpoch: epochText(did.started),
		Ended: did.ended.Format("2006-01-02T15:04:05-0700"), EndedEpoch: epochText(did.ended), Material: did.key.Material,
		Jackstay: version.Version, RunID: did.runID, Worktree: where.Worktree, Head: where.Head, Branch: where.Branch}
	place, err := did.store.Write(receipt)
	if err != nil {
		return "", fmt.Sprintf("the receipt could not be written: %v", err), receipts.NotStoredWriteFailed
	}
	return place, "", ""
}

func recordCaseDurations[Sample any](record map[string]any, samples []Sample, truncated bool) {
	if len(samples) > 0 {
		record["case_durations"] = samples
	}
	if truncated {
		record["case_durations_truncated"] = true
	}
}
