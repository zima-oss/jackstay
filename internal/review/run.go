package review

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/review/codex"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

// Exit statuses of `jst review` (section 7).
const (
	ExitClear       = 0
	ExitBlocks      = 1
	ExitUsage       = 2
	ExitRedesign    = 3
	ExitOwner       = 4
	ExitReviewer    = 5
	ExitEnvironment = 6
)

// MaxSlots is how many reviewer runs the machine's slots allow at most (section 6).
const MaxSlots = 16

// Options are one call's words (section 7).
type Options struct {
	Revision  string // REV or BASE..REV; "" is HEAD
	Task      string
	Reviewers []string
	BriefFile string
	Focus     string
	Fresh     bool
	PastLimit bool
	Model     string
	Effort    string
	Timeout   int // minutes; 0 keeps the configuration
}

// Machine is what a round needs of the machine; Real gives the real one, tests their own.
type Machine struct {
	RunReviewer func(ctx context.Context, request codex.Request) codex.Result
	Probe       func(ctx context.Context, command string) (string, error)
	// CheckEnvironment refuses a run that must not start: a reviewer's program is not installed,
	// or Codex's login cannot be used here. "" when the run may start.
	CheckEnvironment func(commands []string) string
	RecordLogin      func(reason string)
	SlotsDir         string
	StartLockPath    string
	Now              func() time.Time
	TreeAlive        func(reason string) bool
	SlotPoll         time.Duration
}

// RealMachine is the machine jackstay runs on.
func RealMachine(shared func(parts ...string) (string, error)) (Machine, error) {
	slots, err := shared("reviews", "slots")
	if err != nil {
		return Machine{}, err
	}
	startLock, err := shared("reviews", "start.lock")
	if err != nil {
		return Machine{}, err
	}
	marker, err := codex.LoginMarker()
	if err != nil {
		return Machine{}, err
	}
	return Machine{
		RunReviewer: codex.Run,
		Probe:       codex.Probe,
		CheckEnvironment: func(commands []string) string {
			// Installed first: on a machine without Codex, "run `codex login`" would send a
			// stranger to a program that is not there.
			for _, command := range commands {
				if _, err := exec.LookPath(command); err != nil {
					return "Codex is not installed (`" + command + "` is not on PATH): install it, or leave `jst review` out; nothing else in jackstay needs it"
				}
			}
			return codex.CheckEnvironment(codex.Home(os.Environ()), marker)
		},
		RecordLogin:   func(reason string) { codex.RecordLoginFailure(marker, reason) },
		SlotsDir:      slots,
		StartLockPath: startLock,
		Now:           time.Now,
		TreeAlive:     reasonAlive,
		SlotPoll:      time.Second,
	}, nil
}

// Round is one call of `jst review`: what it was asked, and where.
type Round struct {
	Settings Settings
	Repo     Repository
	Store    Store
	Machine  Machine
	Out      io.Writer // the three lines, and the budget first
	Errors   io.Writer // warnings and waiting
}

// Result of a call: the exit status, the folder written, and the record when it became a round.
type Result struct {
	Status int
	Folder string
	Record *RoundRecord
	Next   string
}

// usage is a refusal before anything was started.
func usage(format string, values ...any) Result {
	return Result{Status: ExitUsage, Next: fmt.Sprintf(format, values...)}
}

// Run makes one attempt and, when every reviewer answers, one round.
func (round Round) Run(ctx context.Context, options Options) Result {
	settings := round.Settings
	if options.Model != "" || options.Effort != "" || options.Timeout != 0 {
		settings.Reviewers = overridden(settings.Reviewers, options)
	}
	names := settings.Use
	if len(options.Reviewers) > 0 {
		names = options.Reviewers
		settings.Use = names
	}
	if problems := settings.problems(); len(problems) > 0 {
		return usage("%s", strings.Join(problems, "; "))
	}
	// One budget for the whole call (section 6): every git call and every wait ends with it.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(settings.Budget(names))*time.Minute)
	defer cancel()
	fmt.Fprintf(round.Out, "jst review: budget %d minutes (queue %d, run up to %d, 2 more); run it under `jst bg --for %d`.\n",
		settings.Budget(names), settings.QueueMinutes, settings.LongestTimeout(names), settings.Budget(names))

	baseName, revisionName, ranged := strings.Cut(options.Revision, "..")
	if !ranged {
		revisionName, baseName = options.Revision, ""
	}
	if revisionName == "" {
		revisionName = "HEAD"
	}
	commit, err := round.Repo.Resolve(ctx, revisionName)
	if err != nil {
		return usage("%v", err)
	}
	givenBase := ""
	if ranged {
		if givenBase, err = round.Repo.Resolve(ctx, baseName); err != nil {
			return usage("%v", err)
		}
	}
	task := options.Task
	if task == "" {
		if task, err = round.Repo.Branch(ctx); err != nil || task == "" {
			return usage("no branch is checked out: name the task with --task")
		}
	}
	if len(task) > 200 || !round.Repo.ValidBranchName(ctx, task) {
		return usage("task %q is not a name git takes for a branch (at most 200 bytes)", task)
	}
	commands := make([]string, 0, len(names))
	for _, name := range names {
		commands = append(commands, settings.Reviewers[name].Command)
	}
	if reason := round.Machine.CheckEnvironment(commands); reason != "" {
		return Result{Status: ExitEnvironment, Next: reason}
	}
	if dirty, err := round.Repo.Dirty(ctx); err == nil && dirty {
		fmt.Fprintln(round.Errors, "jst review: the checkout has uncommitted changes; only commit "+short(commit)+" is reviewed")
	}

	taskLock, err := flock.TryFile(round.Store.TaskLockPath(task))
	if errors.Is(err, flock.ErrBusy) {
		return usage("task %q is busy: another `jst review` of it is running", task)
	}
	if err != nil {
		return usage("the task's lock: %v", err)
	}
	defer taskLock.Release()
	if removed, err := round.Store.Prune(settings.KeepDays, round.Machine.Now()); err == nil && len(removed) > 0 {
		fmt.Fprintf(round.Errors, "jst review: removed %d task(s) older than %d days\n", len(removed), settings.KeepDays)
	}
	_ = os.MkdirAll(round.Store.TreesPath(), 0o700)
	RemoveStaleTrees(ctx, round.Repo, round.Store, round.Machine.TreeAlive)

	plan, refusal := round.plan(ctx, task, commit, givenBase, options)
	if refusal != nil {
		return *refusal
	}
	return round.attempt(ctx, settings, plan, names, options)
}

// plan is where a round stands in its task and chain.
type plan struct {
	task, commit, base, previous string
	number, attempt, chain       int
	follows                      int
	ledger                       []LedgerFinding
	streaks                      map[string]int
}

func (round Round) plan(ctx context.Context, task, commit, givenBase string, options Options) (plan, *Result) {
	entries, err := round.Store.Entries(task)
	if err != nil {
		result := usage("the task's folder: %v", err)
		return plan{}, &result
	}
	rounds := Rounds(entries)
	planned := plan{task: task, commit: commit, number: len(rounds) + 1, attempt: NextAttempt(entries), chain: 1,
		streaks: map[string]int{}}
	var newest *RoundRecord
	if len(rounds) > 0 {
		record, err := readRecord(rounds[len(rounds)-1].Path)
		if err != nil {
			result := usage("the newest round: %v", err)
			return plan{}, &result
		}
		newest = &record
		planned.number = record.Round + 1
		planned.chain = record.Chain
	}
	if newest != nil && newest.Round >= round.Settings.RoundLimit && newest.Outcome.Blocks && !options.PastLimit {
		result := Result{Status: ExitOwner, Next: fmt.Sprintf(
			"round %d reached the limit of %d and still blocks: the owner decides; a further round needs --past-limit after the owner's word",
			newest.Round, round.Settings.RoundLimit)}
		return plan{}, &result
	}
	if newest == nil || options.Fresh {
		if newest != nil {
			planned.chain = newest.Chain + 1
		}
		planned.base = givenBase
		if planned.base == "" {
			if planned.base, err = round.Repo.FirstParent(ctx, commit); err != nil {
				result := usage("%v", err)
				return plan{}, &result
			}
		}
		return planned, nil
	}
	if givenBase != "" && givenBase != newest.Base {
		result := usage("this chain reviews from %s; --fresh starts a full review from a new base", short(newest.Base))
		return plan{}, &result
	}
	planned.base, planned.previous, planned.follows = newest.Base, newest.Commit, newest.Round
	planned.ledger = newest.Outcome.Ledger
	for place, count := range newest.Outcome.Streaks {
		planned.streaks[place] = count
	}
	return planned, nil
}

func (round Round) attempt(ctx context.Context, settings Settings, planned plan, names []string, options Options) (result Result) {
	folder := filepath.Join(round.Store.TaskPath(planned.task), fmt.Sprintf("attempt-%d", planned.attempt))
	ctx, span := startAttempt(ctx, planned, folder)
	defer func() { endAttempt(span, result.Record, result.Next, result.Status, result.Folder) }()
	if err := writeJSON(filepath.Join(folder, AttemptName), AttemptRecord{Task: planned.task, Attempt: planned.attempt,
		Commit: planned.commit, Base: planned.base}); err != nil {
		return usage("the attempt's folder: %v", err)
	}
	data, refusal := round.promptData(ctx, settings, planned, options, folder)
	if refusal != nil {
		return *refusal
	}
	templateText := ""
	if settings.Template != "" {
		text, err := round.Repo.Show(ctx, planned.commit, settings.Template)
		if err != nil {
			return usage("`review.template` %s is not in commit %s", settings.Template, short(planned.commit))
		}
		templateText = string(text)
	}
	prompt, err := BuildPrompt(templateText, data)
	if err != nil {
		return usage("%v", err)
	}
	if err := writeFileWhole(filepath.Join(folder, "prompt.md"), prompt); err != nil {
		return usage("the attempt's folder: %v", err)
	}

	runs := make([]ReviewerRun, len(names))
	answers := make([]Answer, len(names))
	var reviewersDone sync.WaitGroup
	for position, name := range names {
		reviewersDone.Add(1)
		go func() {
			defer reviewersDone.Done()
			runs[position], answers[position] = round.review(ctx, settings, planned, name, prompt, folder)
		}()
	}
	reviewersDone.Wait() // jackstay:not-a-process

	record := RoundRecord{Task: planned.task, Round: planned.number, Attempt: planned.attempt, Chain: planned.chain,
		Base: planned.base, Commit: planned.commit, Follows: planned.follows, Focus: options.Focus,
		PastLimit: options.PastLimit, Made: round.Machine.Now().Format(time.RFC3339), Runs: runs,
		Summaries: map[string]string{}}
	allDone := true
	for _, run := range runs {
		if run.Outcome != codex.OutcomeDone {
			allDone = false
		}
		if run.Outcome == codex.OutcomeLogin {
			round.Machine.RecordLogin(run.Reason)
		}
	}
	if !allDone {
		lines := failedLines(planned, runs, folder)
		_ = writeFileWhole(filepath.Join(folder, "report.md"), []byte(FailureReport(planned.task, planned.attempt, runs)))
		_ = round.Store.AppendIndex(lines)
		status := ExitReviewer
		for _, run := range runs {
			if run.Outcome == codex.OutcomeLogin || run.Outcome == codex.OutcomeRefused {
				status = ExitEnvironment
			}
		}
		return Result{Status: status, Folder: folder, Next: "no round taken: " + failureWords(runs) + "; read the report, then rerun"}
	}
	answered := map[string]Answer{}
	for position, name := range names {
		answered[name] = answers[position]
		record.Summaries[name] = answers[position].Summary
	}
	record.Outcome = Advance(settings, planned.ledger, planned.streaks, answered, planned.number, planned.commit)
	record.OverLimit = planned.number > settings.RoundLimit && !options.PastLimit
	if err := writeJSON(filepath.Join(folder, RecordName), record); err != nil {
		return Result{Status: ExitReviewer, Folder: folder, Next: "the round's record could not be written: " + err.Error()}
	}
	_ = writeFileWhole(filepath.Join(folder, "report.md"), []byte(Report(settings, record)))
	roundFolder := filepath.Join(round.Store.TaskPath(planned.task), fmt.Sprintf("round-%d", planned.number))
	if err := os.Rename(folder, roundFolder); err != nil {
		return Result{Status: ExitReviewer, Folder: folder, Next: "the attempt could not become a round: " + err.Error()}
	}
	_ = round.Store.AppendIndex(recordLines(record, roundFolder))
	status, next := Disposition(settings, record)
	return Result{Status: status, Folder: roundFolder, Record: &record, Next: next}
}

// Disposition is a round's exit status and next step (section 7: redesign before the owner).
func Disposition(settings Settings, record RoundRecord) (int, string) {
	switch {
	case len(record.Outcome.RedesignDue) > 0:
		return ExitRedesign, "redesign, do not patch (W-019): " + strings.Join(record.Outcome.RedesignDue, "; ")
	case record.Round >= settings.RoundLimit && record.Outcome.Blocks:
		return ExitOwner, fmt.Sprintf("round %d of a limit of %d still blocks: ask the owner", record.Round, settings.RoundLimit)
	case record.Outcome.Blocks:
		return ExitBlocks, "fix the open blocking findings, then run the next round"
	}
	return ExitClear, "nothing blocks"
}

func (round Round) promptData(ctx context.Context, settings Settings, planned plan, options Options, folder string) (PromptData, *Result) {
	fail := func(format string, values ...any) (PromptData, *Result) {
		result := usage(format, values...)
		return PromptData{}, &result
	}
	var brief []string
	if settings.Brief != "" {
		text, err := round.Repo.Show(ctx, planned.commit, settings.Brief)
		if err != nil {
			return fail("`review.brief` %s is not in commit %s", settings.Brief, short(planned.commit))
		}
		brief = append(brief, strings.TrimSpace(string(text)))
	}
	if options.BriefFile != "" {
		text, err := os.ReadFile(options.BriefFile)
		if err != nil {
			return fail("--brief: %v", err)
		}
		if err := writeFileWhole(filepath.Join(folder, "brief.md"), text); err != nil {
			return fail("the attempt's folder: %v", err)
		}
		brief = append(brief, strings.TrimSpace(string(text)))
	}
	cite := settings.Cite
	if cite == nil {
		cite = []string{}
		if round.Repo.Exists(ctx, planned.commit, "AGENTS.md") {
			cite = []string{"AGENTS.md"}
		}
	}
	for _, path := range cite {
		if !round.Repo.Exists(ctx, planned.commit, path) {
			return fail("`review.cite` names %s, which is not in commit %s", path, short(planned.commit))
		}
	}
	from := planned.base
	if planned.previous != "" {
		from = planned.previous
	}
	stat, err := round.Repo.DiffStat(ctx, from, planned.commit)
	if err != nil {
		return fail("git diff --stat: %v", err)
	}
	subject, _ := round.Repo.Subject(ctx, planned.commit)
	return PromptData{
		Brief: strings.Join(brief, "\n\n"), Commit: planned.commit, Subject: subject, Base: planned.base,
		DiffStat: stat, Cite: cite, Round: planned.number, Previous: planned.previous,
		OpenFindings: Open(planned.ledger), Triage: settings.Triage, Severities: settings.Severities,
		Blocking: settings.Blocking, Focus: options.Focus,
	}, nil
}

// review is one reviewer's run: a slot, a pinned tree, the start lock, Codex, the answer.
func (round Round) review(ctx context.Context, settings Settings, planned plan, name string, prompt []byte,
	folder string) (ReviewerRun, Answer) {
	reviewer := settings.Reviewers[name]
	reviewerFolder := filepath.Join(folder, name)
	run := ReviewerRun{Reviewer: name, Model: reviewer.Model, Effort: reviewer.Effort, Exit: -1,
		Started: round.Machine.Now().Format(time.RFC3339)}
	finish := func(outcome, reason string) (ReviewerRun, Answer) {
		run.Outcome, run.Reason = outcome, reason
		if run.Ended == "" {
			run.Ended = round.Machine.Now().Format(time.RFC3339)
		}
		_ = writeJSON(filepath.Join(reviewerFolder, "run.json"), run)
		return run, Answer{}
	}
	if err := os.MkdirAll(reviewerFolder, 0o700); err != nil {
		return finish(codex.OutcomeFailed, err.Error())
	}
	version, err := round.Machine.Probe(ctx, reviewer.Command)
	if err != nil {
		return finish(codex.OutcomeRefused, err.Error())
	}
	run.Version = version
	schemaPath := filepath.Join(reviewerFolder, "schema.json")
	if err := writeFileWhole(schemaPath, Schema(settings.Severities)); err != nil {
		return finish(codex.OutcomeFailed, err.Error())
	}

	queueUntil := round.Machine.Now().Add(time.Duration(settings.QueueMinutes) * time.Minute)
	queueContext, cancelQueue := context.WithDeadline(ctx, queueUntil)
	defer cancelQueue()
	slot, err := round.takeSlot(queueContext, settings.MaxConcurrent)
	if err != nil {
		return finish(OutcomeNoSlot, "no slot within "+fmt.Sprint(settings.QueueMinutes)+" minutes")
	}
	defer slot.Release()

	tree := treePath(round.Store, planned.task, planned.number, name)
	// Removed on every way out, a half-made tree included (a checkout stopped at the deadline).
	defer func() {
		removeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_ = round.Repo.RemoveTree(removeContext, tree)
		_ = os.RemoveAll(tree)
	}()
	if err := round.Repo.AddTree(ctx, tree, planned.commit, ownReason()); err != nil {
		return finish(codex.OutcomeFailed, "the pinned tree could not be made: "+err.Error())
	}

	startLock, err := flock.WaitFile(queueContext, round.Machine.StartLockPath)
	if err != nil {
		return finish(OutcomeNoSlot, "the start lock was not free within the queue's time")
	}
	agentCtx, agent := startAgent(ctx, name)
	defer func() { endAgent(agent, run, reviewerFolder) }()
	result := round.Machine.RunReviewer(agentCtx, codex.Request{
		Command: reviewer.Command, Model: reviewer.Model, Effort: reviewer.Effort,
		Timeout: time.Duration(reviewer.TimeoutMinutes) * time.Minute, Isolation: reviewer.Isolation,
		Tree: tree, Folder: reviewerFolder, Prompt: prompt, SchemaPath: schemaPath, Answered: startLock.Release,
	})
	startLock.Release()
	run.Argv, run.Exit = result.Argv, result.Exit
	run.Started, run.Ended = result.Started.Format(time.RFC3339), result.Ended.Format(time.RFC3339)
	run.Seconds = result.Ended.Sub(result.Started).Seconds()
	if result.Outcome != codex.OutcomeDone {
		return finish(result.Outcome, result.Reason)
	}
	answer, outcome, reason := ReadAnswer(result.AnswerPath)
	if outcome != codex.OutcomeDone {
		return finish(outcome, reason)
	}
	if problem := CheckAnswer(answer, settings, planned.ledger); problem != "" {
		return finish(OutcomeInconsistent, problem)
	}
	run.Outcome = codex.OutcomeDone
	_ = writeJSON(filepath.Join(reviewerFolder, "run.json"), run)
	return run, answer
}

// takeSlot takes a free slot k < most, waiting until the context ends (section 6).
func (round Round) takeSlot(ctx context.Context, most int) (*flock.File, error) {
	said := time.Time{}
	for {
		for slot := range most {
			held, err := flock.TryFile(filepath.Join(round.Machine.SlotsDir, fmt.Sprintf("%d.lock", slot)))
			if err == nil {
				return held, nil
			}
		}
		if now := round.Machine.Now(); said.IsZero() || now.Sub(said) >= time.Minute {
			fmt.Fprintf(round.Errors, "jst review: all %d review slots are taken; waiting\n", most)
			said = now
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(round.Machine.SlotPoll):
		}
	}
}

func overridden(reviewers map[string]ReviewerSettings, options Options) map[string]ReviewerSettings {
	changed := map[string]ReviewerSettings{}
	for name, reviewer := range reviewers {
		if options.Model != "" {
			reviewer.Model = options.Model
		}
		if options.Effort != "" {
			reviewer.Effort = options.Effort
		}
		if options.Timeout != 0 {
			reviewer.TimeoutMinutes = options.Timeout
		}
		changed[name] = reviewer
	}
	return changed
}

func failedLines(planned plan, runs []ReviewerRun, folder string) []IndexLine {
	lines := make([]IndexLine, 0, len(runs))
	for _, run := range runs {
		lines = append(lines, IndexLine{At: run.Ended, Task: planned.task, Attempt: planned.attempt, Reviewer: run.Reviewer,
			Commit: planned.commit, Base: planned.base, Outcome: run.Outcome, Seconds: run.Seconds, Path: folder})
	}
	return lines
}

func failureWords(runs []ReviewerRun) string {
	var words []string
	for _, run := range runs {
		if run.Outcome != codex.OutcomeDone {
			words = append(words, fmt.Sprintf("%s %s (%s)", run.Reviewer, run.Outcome, run.Reason))
		}
	}
	return strings.Join(words, "; ")
}

// ReadRuns are an attempt's reviewer runs.
func ReadRuns(folder string) []ReviewerRun {
	listed, _ := os.ReadDir(folder)
	var runs []ReviewerRun
	for _, item := range listed {
		data, err := os.ReadFile(filepath.Join(folder, item.Name(), "run.json"))
		if err != nil {
			continue
		}
		var run ReviewerRun
		if strictjson.UnmarshalOpen(data, &run) == nil {
			runs = append(runs, run)
		}
	}
	slices.SortFunc(runs, func(first, second ReviewerRun) int { return strings.Compare(first.Reviewer, second.Reviewer) })
	return runs
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// Outcomes review adds to Codex's own.
const (
	OutcomeUnparsable   = "unparsable"
	OutcomeInconsistent = "inconsistent"
	OutcomeNoSlot       = "no-slot"
)
