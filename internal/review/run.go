package review

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/review/access"
	"github.com/zima-oss/jackstay/internal/review/claude"
	"github.com/zima-oss/jackstay/internal/review/codex"
	"github.com/zima-oss/jackstay/internal/review/runner"
	"github.com/zima-oss/jackstay/internal/review/sourceview"
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
	RunReviewer func(ctx context.Context, request runner.Request) runner.Result
	Probe       func(ctx context.Context, reviewer, command string) (string, error)
	// CheckEnvironment refuses a run that must not start: a reviewer's program is not installed,
	// or Codex's login cannot be used here. "" when the run may start.
	CheckEnvironment func(reviewers []ReviewerSettings) string
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
		RunReviewer: func(ctx context.Context, request runner.Request) runner.Result {
			if request.Reviewer == claude.Name {
				return claude.Run(ctx, request)
			}
			return codex.Run(ctx, request)
		},
		Probe: func(ctx context.Context, reviewer, command string) (string, error) {
			if reviewer == claude.Name {
				return claude.Probe(ctx, command)
			}
			return codex.Probe(ctx, command)
		},
		CheckEnvironment: func(reviewers []ReviewerSettings) string {
			usesCodex := false
			for _, reviewer := range reviewers {
				usesCodex = usesCodex || reviewer.Name == "codex"
				if _, err := exec.LookPath(reviewer.Command); err != nil {
					program := "Codex"
					if reviewer.Name == claude.Name {
						program = "Claude Code"
					}
					return program + " is not installed (`" + reviewer.Command + "` is not on PATH)"
				}
			}
			if usesCodex {
				if reason := codex.CheckEnvironment(codex.Home(os.Environ()), marker); reason != "" {
					return reason
				}
			}
			if slices.ContainsFunc(reviewers, func(reviewer ReviewerSettings) bool { return reviewer.Name == claude.Name }) {
				return claude.CheckEnvironment(claude.ConfigFile(os.Environ(), homedir.AccountHome()))
			}
			return ""
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
	RepositoryRoot string
	Settings       Settings
	Repo           Repository
	Store          Store
	Machine        Machine
	Out            io.Writer // the three lines, and the budget first
	Errors         io.Writer // warnings and waiting
	Request        string    // the background request this run serves (background.go), "" in the foreground
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
	names := settings.Use
	if len(options.Reviewers) > 0 {
		names = options.Reviewers
		settings.Use = names
	}
	if len(names) == 0 {
		return usage("`review.use` is empty: choose a reviewer with --reviewer %s", strings.Join(KnownReviewers, " or "))
	}
	if options.Model != "" || options.Effort != "" || options.Timeout != 0 {
		settings.Reviewers = overridden(settings.Reviewers, names, options)
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
	reviewers := make([]ReviewerSettings, 0, len(names))
	for _, name := range names {
		reviewers = append(reviewers, settings.Reviewers[name])
	}
	if reason := round.Machine.CheckEnvironment(reviewers); reason != "" {
		return Result{Status: ExitEnvironment, Next: reason}
	}
	if dirty, err := round.Repo.Dirty(ctx); err == nil && dirty {
		fmt.Fprintln(round.Errors, "jst review: the checkout has uncommitted changes; only commit "+short(commit)+" is reviewed")
	}

	releaseTask, err := round.Store.tryTaskLock(task)
	if errors.Is(err, flock.ErrBusy) {
		return usage("task %q is busy: another `jst review` of it is running", task)
	}
	if err != nil {
		return usage("the task's lock: %v", err)
	}
	defer releaseTask()
	if round.Request != "" {
		if err := round.Store.markRunning(ctx, task, round.Request); err != nil {
			return usage("the background request: %v", err)
		}
	}
	if err := round.Store.removeAbandonedSources(task); err != nil {
		return Result{Status: ExitReviewer, Next: fmt.Sprintf("removing an abandoned source view: %v", err)}
	}
	if removed, err := round.Store.Prune(settings.KeepDays, round.Machine.Now()); err == nil && len(removed) > 0 {
		fmt.Fprintf(round.Errors, "jst review: removed %d task(s) older than %d days\n", len(removed), settings.KeepDays)
	}
	_ = os.MkdirAll(round.Store.TreesPath(), 0o700)
	RemoveStaleTrees(ctx, round.Repo, round.Store, round.Machine.TreeAlive)

	plan, refusal := round.plan(ctx, task, commit, givenBase, settings, options)
	if refusal != nil {
		return *refusal
	}
	return round.attempt(ctx, settings, plan, names, options)
}

// plan is where a round stands in its task and chain.
type plan struct {
	task, commit, base, previous string
	contentPolicy, templateText  string
	number, attempt, chain       int
	follows                      int
	ledger                       []LedgerFinding
	streaks                      map[string]int
	// limitCleared: the newest round reached the limit and nothing of it blocks by today's rule,
	// so the rounds after it need no owner's word.
	limitCleared bool
}

func (round Round) plan(ctx context.Context, task, commit, givenBase string, settings Settings, options Options) (plan, *Result) {
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
	// Whether the newest round still blocks is read from its ledger by today's predicate, not from
	// the flag it saved: a rule that changed since (W-094) applies to it as `jst review check` does.
	if newest != nil && newest.Round >= round.Settings.RoundLimit && !ledgerBlocks(round.Settings, newest.Outcome.Ledger) {
		planned.limitCleared = true
	}
	if newest != nil && newest.Round >= round.Settings.RoundLimit && ledgerBlocks(round.Settings, newest.Outcome.Ledger) && !options.PastLimit {
		result := Result{Status: ExitOwner, Next: fmt.Sprintf(
			"round %d reached the limit of %d and still blocks: the owner decides; a further round needs --past-limit after the owner's word",
			newest.Round, round.Settings.RoundLimit)}
		return plan{}, &result
	}
	var briefText, templateText []byte
	for _, file := range []struct {
		name, path string
		content    *[]byte
	}{
		{"brief", settings.Brief, &briefText},
		{"template", settings.Template, &templateText},
	} {
		if file.path == "" {
			continue
		}
		content, err := round.Repo.Show(ctx, commit, file.path)
		if err != nil {
			result := usage("`review.%s` %s is not in commit %s", file.name, file.path, short(commit))
			return plan{}, &result
		}
		*file.content = content
	}
	if settings.Brief != "" || settings.Template != "" {
		data, _ := json.Marshal(struct{ Brief, Template string }{string(briefText), string(templateText)})
		planned.contentPolicy = fmt.Sprintf("v1:%x", sha256.Sum256(data))
	}
	planned.templateText = string(templateText)
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
	if newest.Policy != settings.policyID() || newest.ContentPolicy != planned.contentPolicy || !sameReviewers(newest.Runs, settings.Use) {
		result := usage("the review policy or reviewers changed since round %d; name BASE..REV and use --fresh for a full review", newest.Round)
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
	if err := round.Store.prepareTask(planned.task); err != nil {
		return usage("the task's folder: %v", err)
	}
	folder := filepath.Join(round.Store.TaskPath(planned.task), fmt.Sprintf("attempt-%d", planned.attempt))
	ctx, span := startAttempt(ctx, planned, folder)
	defer func() { endAttempt(span, result.Record, result.Next, result.Status, result.Folder) }()
	if err := writeJSON(filepath.Join(folder, AttemptName), AttemptRecord{Task: planned.task, Attempt: planned.attempt,
		Commit: planned.commit, Base: planned.base, Request: round.Request}); err != nil {
		return usage("the attempt's folder: %v", err)
	}
	data, brief, refusal := round.promptData(ctx, settings, planned, options, folder)
	if refusal != nil {
		return *refusal
	}
	prompt, err := BuildPrompt(planned.templateText, data)
	if err != nil {
		return usage("%v", err)
	}
	if err := writeFileWhole(filepath.Join(folder, "prompt.md"), prompt); err != nil {
		return usage("the attempt's folder: %v", err)
	}
	binding := bindingOf(planned, settings, names, prompt, Schema(settings.Severities), brief)
	if err := writeJSON(filepath.Join(folder, AttemptName), AttemptRecord{Task: planned.task, Attempt: planned.attempt,
		Commit: planned.commit, Base: planned.base, Request: round.Request, Binding: binding}); err != nil {
		return usage("the attempt's folder: %v", err)
	}
	reusable := round.reusableAnswers(planned, settings, binding, names)
	for _, name := range names {
		if saved, ok := reusable[name]; ok {
			fmt.Fprintf(round.Errors, "jst review: %s answered this question in %s; its answer is reused, not paid for again\n", name, saved.from)
		}
	}
	if len(reusable) == len(names) {
		return round.finish(settings, planned, names, options, folder, reusable, make([]ReviewerRun, len(names)), make([]Answer, len(names)))
	}

	queueContext, cancelQueue := context.WithTimeout(ctx, time.Duration(settings.QueueMinutes)*time.Minute)
	defer cancelQueue()
	preparing := round.Machine.Now()
	sourceSlot, err := round.takeSourceSlot(queueContext, settings.MaxConcurrent)
	if err != nil {
		return round.sourceFailure(planned, folder, fmt.Sprintf("waiting for a source view slot: %v", err))
	}
	tree := filepath.Join(folder, "source")
	defer func() {
		if tree != "" {
			if err := os.RemoveAll(tree); err != nil {
				fmt.Fprintf(round.Errors, "jst review: could not remove the temporary source view: %v\n", err)
			}
		}
		sourceSlot.Release()
	}()
	if err := round.Repo.BuildSource(queueContext, tree, sourceview.Revisions{Base: planned.base, Head: planned.commit, Previous: planned.previous}); err != nil {
		return round.sourceFailure(planned, folder, fmt.Sprintf("preparing the pinned source view: %v", err))
	}
	if options.BriefFile != "" {
		if err := writeFileWhole(filepath.Join(tree, "review-brief.md"), brief); err != nil {
			return round.sourceFailure(planned, folder, fmt.Sprintf("writing the source view brief: %v", err))
		}
	}

	if len(data.OpenFindings) != 0 {
		if err := writeJSON(filepath.Join(tree, "open-findings.json"), data.OpenFindings); err != nil {
			return round.sourceFailure(planned, folder, fmt.Sprintf("writing the open findings to the source view: %v", err))
		}
	}
	prepareSeconds := round.Machine.Now().Sub(preparing).Seconds()

	runs := make([]ReviewerRun, len(names))
	answers := make([]Answer, len(names))
	var reviewersDone sync.WaitGroup
	for position, name := range names {
		if _, ok := reusable[name]; ok {
			continue
		}
		reviewersDone.Add(1)
		go func() {
			defer reviewersDone.Done()
			runs[position], answers[position] = round.review(ctx, queueContext, settings, planned, name, prompt, folder, tree)
		}()
	}
	reviewersDone.Wait() // jackstay:not-a-process
	if err := os.RemoveAll(tree); err != nil {
		return round.sourceFailure(planned, folder, fmt.Sprintf("removing the temporary source view: %v", err))
	}
	tree = ""
	finished := round.finish(settings, planned, names, options, folder, reusable, runs, answers)
	if finished.Record != nil {
		finished.Record.PrepareSeconds = prepareSeconds
		_ = writeJSON(filepath.Join(finished.Folder, RecordName), *finished.Record)
	}
	return finished
}

// finish turns an attempt's runs, those made now and those reused, into its outcome: a round when
// every reviewer answered.
func (round Round) finish(settings Settings, planned plan, names []string, options Options, folder string,
	reusable map[string]reused, runs []ReviewerRun, answers []Answer) Result {
	for position, name := range names {
		saved, ok := reusable[name]
		if !ok {
			continue
		}
		run, err := saved.adopt(filepath.Join(folder, name))
		if err != nil {
			run.Outcome, run.Reason = runner.OutcomeFailed, "the reused answer could not be copied: "+err.Error()
		}
		runs[position], answers[position] = run, saved.answer
	}
	record := RoundRecord{Task: planned.task, Request: round.Request, Round: planned.number, Attempt: planned.attempt, Chain: planned.chain,
		Base: planned.base, Commit: planned.commit, Follows: planned.follows, Focus: options.Focus, Policy: settings.policyID(), ContentPolicy: planned.contentPolicy,
		PastLimit: options.PastLimit, Made: round.Machine.Now().Format(time.RFC3339), Runs: runs,
		Summaries: map[string]string{}}
	allDone := true
	for _, run := range runs {
		if run.Outcome != runner.OutcomeDone {
			allDone = false
		}
		if run.Reviewer == "codex" && run.Outcome == runner.OutcomeLogin {
			round.Machine.RecordLogin(run.Reason)
		}
	}
	if !allDone {
		lines := failedLines(planned, runs, folder)
		_ = writeFileWhole(filepath.Join(folder, "report.md"), []byte(FailureReport(planned.task, planned.attempt, runs)))
		_ = round.Store.AppendIndex(lines)
		status := ExitReviewer
		for _, run := range runs {
			if run.Outcome == runner.OutcomeLogin || run.Outcome == runner.OutcomeRefused {
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
	record.OverLimit = planned.number > settings.RoundLimit && !options.PastLimit && !planned.limitCleared
	status, next := Disposition(settings, record)
	if err := writeJSON(filepath.Join(folder, RecordName), record); err != nil {
		return Result{Status: ExitReviewer, Folder: folder, Next: "the round's record could not be written: " + err.Error()}
	}
	_ = writeFileWhole(filepath.Join(folder, "report.md"), []byte(Report(settings, record)))
	roundFolder := filepath.Join(round.Store.TaskPath(planned.task), fmt.Sprintf("round-%d", planned.number))
	if err := os.Rename(folder, roundFolder); err != nil {
		return Result{Status: ExitReviewer, Folder: folder, Next: "the attempt could not become a round: " + err.Error()}
	}
	_ = round.Store.AppendIndex(recordLines(record, roundFolder))
	return Result{Status: status, Folder: roundFolder, Record: &record, Next: next}
}

func (round Round) sourceFailure(planned plan, folder, reason string) Result {
	ended := round.Machine.Now().Format(time.RFC3339)
	run := ReviewerRun{Reviewer: "source-view", Exit: -1, Started: ended, Ended: ended,
		Outcome: runner.OutcomeFailed, Reason: reason}
	_ = writeRun(filepath.Join(folder, "source-view", "run.json"), run)
	_ = writeFileWhole(filepath.Join(folder, "report.md"), []byte(FailureReport(planned.task, planned.attempt, []ReviewerRun{run})))
	_ = round.Store.AppendIndex(failedLines(planned, []ReviewerRun{run}, folder))
	return Result{Status: ExitReviewer, Folder: folder, Next: reason + "; read the report, then rerun"}
}

// Disposition is a round's exit status and next step (section 7: redesign before the owner).
func Disposition(settings Settings, record RoundRecord) (int, string) {
	recheck := ""
	if settings.Triage {
		recheck = "re-check each blocking rating (an unusual and severe finding blocks only with an ordinary-work path, W-094), "
	}
	switch {
	case len(record.Outcome.RedesignDue) > 0:
		return ExitRedesign, "redesign, do not patch (W-019): " + strings.Join(record.Outcome.RedesignDue, "; ")
	case record.Round >= settings.RoundLimit && record.Outcome.Blocks:
		return ExitOwner, fmt.Sprintf("round %d of a limit of %d still blocks: %sthen ask the owner", record.Round, settings.RoundLimit, recheck)
	case record.Outcome.Blocks:
		return ExitBlocks, recheck + "fix the blocking findings that stand, then run the next round"
	}
	return ExitClear, "nothing blocks"
}

func (round Round) promptData(ctx context.Context, settings Settings, planned plan, options Options, folder string) (PromptData, []byte, *Result) {
	fail := func(format string, values ...any) (PromptData, []byte, *Result) {
		result := usage(format, values...)
		return PromptData{}, nil, &result
	}
	var references []string
	if settings.Brief != "" {
		if !round.Repo.Exists(ctx, planned.commit, settings.Brief) {
			return fail("`review.brief` %s is not in commit %s", settings.Brief, short(planned.commit))
		}
		references = append(references, "`"+path.Join("head", settings.Brief)+"`")
	}
	var brief []byte
	if options.BriefFile != "" {
		text, err := os.ReadFile(options.BriefFile)
		if err != nil {
			return fail("--brief: %v", err)
		}
		if err := writeFileWhole(filepath.Join(folder, "brief.md"), text); err != nil {
			return fail("the attempt's folder: %v", err)
		}
		brief = text
		references = append(references, "`review-brief.md`")
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
	diffPaths := "changes/base.paths"
	if planned.previous != "" {
		diffPaths = "changes/previous.paths"
	}
	subject, _ := round.Repo.Subject(ctx, planned.commit)
	return PromptData{
		Brief: strings.Join(references, ", "), Commit: planned.commit, Subject: subject, Base: planned.base,
		DiffStat: "Changed paths: " + diffPaths, Tree: "head", Cite: cite, Round: planned.number, Previous: planned.previous,
		OpenFindings: Open(planned.ledger), Triage: settings.Triage, Severities: settings.Severities,
		Blocking: settings.Blocking, Focus: options.Focus,
	}, brief, nil
}

// review runs a provider under the same pinned source and verdict contract.
func (round Round) review(ctx, queueContext context.Context, settings Settings, planned plan, name string, prompt []byte,
	folder, tree string) (ReviewerRun, Answer) {
	reviewer := settings.Reviewers[name]
	reviewerFolder := filepath.Join(folder, name)
	run := ReviewerRun{Reviewer: name, Model: reviewer.Model, Effort: reviewer.Effort, Exit: -1,
		Started: round.Machine.Now().Format(time.RFC3339)}
	finish := func(outcome, reason string) (ReviewerRun, Answer) {
		run.Outcome, run.Reason = outcome, reason
		if run.Ended == "" {
			run.Ended = round.Machine.Now().Format(time.RFC3339)
		}
		_ = writeRun(filepath.Join(reviewerFolder, "run.json"), run)
		return run, Answer{}
	}
	if err := os.MkdirAll(reviewerFolder, 0o700); err != nil {
		return finish(runner.OutcomeFailed, err.Error())
	}
	version, err := round.Machine.Probe(ctx, name, reviewer.Command)
	if err != nil {
		return finish(runner.OutcomeRefused, err.Error())
	}
	run.Version = version
	schemaPath := filepath.Join(reviewerFolder, "schema.json")
	if err := writeFileWhole(schemaPath, Schema(settings.Severities)); err != nil {
		return finish(runner.OutcomeFailed, err.Error())
	}

	waiting := round.Machine.Now()
	slot, err := round.takeSlot(queueContext, settings.MaxConcurrent)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return finish(OutcomeNoSlot, "no slot within "+fmt.Sprint(settings.QueueMinutes)+" minutes")
		}
		return finish(runner.OutcomeFailed, "cannot acquire a review slot: "+err.Error())
	}
	defer slot.Release()

	startLock, err := flock.WaitFile(queueContext, round.Machine.StartLockPath)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return finish(OutcomeNoSlot, "the start lock was not free within the queue's time")
		}
		return finish(runner.OutcomeFailed, "cannot acquire the start lock: "+err.Error())
	}
	if queueContext.Err() != nil {
		startLock.Release()
		return finish(OutcomeNoSlot, "the queue time elapsed before the reviewer could start")
	}
	run.WaitSeconds = round.Machine.Now().Sub(waiting).Seconds()
	agentCtx, agent := startAgent(ctx, name)
	defer func() { endAgent(agent, run, reviewerFolder) }()
	result := round.Machine.RunReviewer(agentCtx, runner.Request{
		Reviewer: name, Command: reviewer.Command, Model: reviewer.Model, Effort: reviewer.Effort,
		Timeout: time.Duration(reviewer.TimeoutMinutes) * time.Minute, Isolation: reviewer.Isolation, MCPServers: reviewer.MCPServers,
		Tree: tree, PinnedSource: true, RepositoryRoot: round.RepositoryRoot, Folder: reviewerFolder, Prompt: prompt, SchemaPath: schemaPath, Answered: startLock.Release,
	})
	startLock.Release()
	run.Argv, run.Exit = redactedArgv(result.Argv), result.Exit // round.json keeps the runs too
	run.Started, run.Ended = result.Started.Format(time.RFC3339), result.Ended.Format(time.RFC3339)
	run.Seconds = result.Ended.Sub(result.Started).Seconds()
	if result.Outcome != runner.OutcomeDone {
		return finish(result.Outcome, result.Reason)
	}
	answer, outcome, reason := ReadAnswer(result.AnswerPath)
	if outcome != runner.OutcomeDone {
		return finish(outcome, reason)
	}
	if problem := CheckAnswer(answer, settings, planned.ledger); problem != "" {
		return finish(OutcomeInconsistent, problem)
	}
	if answer.Verdict == "incomplete" {
		return finish(OutcomeIncomplete, strings.TrimSpace(answer.Summary))
	}
	run.Outcome = runner.OutcomeDone
	_ = writeRun(filepath.Join(reviewerFolder, "run.json"), run)
	return run, answer
}

// takeSlot takes a free slot k < most, waiting until the context ends (section 6).
func (round Round) takeSlot(ctx context.Context, most int) (*flock.File, error) {
	return round.takeNamedSlot(ctx, most, round.Machine.SlotsDir, "review")
}

func (round Round) takeSourceSlot(ctx context.Context, most int) (*flock.File, error) {
	return round.takeNamedSlot(ctx, most, round.Machine.SlotsDir+"-source", "source view")
}

func (round Round) takeNamedSlot(ctx context.Context, most int, directory, name string) (*flock.File, error) {
	said := time.Time{}
	for {
		for slot := range most {
			held, err := flock.TryFile(filepath.Join(directory, fmt.Sprintf("%d.lock", slot)))
			if err == nil {
				if err := ctx.Err(); err != nil {
					held.Release()
					return nil, err
				}
				return held, nil
			}
			if !errors.Is(err, flock.ErrBusy) {
				return nil, err
			}
		}
		if now := round.Machine.Now(); said.IsZero() || now.Sub(said) >= time.Minute {
			fmt.Fprintf(round.Errors, "jst review: all %d %s slots are taken; waiting\n", most, name)
			said = now
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(round.Machine.SlotPoll):
		}
	}
}

func overridden(reviewers map[string]ReviewerSettings, names []string, options Options) map[string]ReviewerSettings {
	changed := maps.Clone(reviewers)
	for _, name := range names {
		reviewer, available := reviewers[name]
		if !available {
			continue
		}
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
			Commit: planned.commit, Base: planned.base, Outcome: run.Outcome, Seconds: run.Seconds, Path: folder, Reused: run.Reused})
	}
	return lines
}

func failureWords(runs []ReviewerRun) string {
	var words []string
	for _, run := range runs {
		if run.Outcome != runner.OutcomeDone {
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
	OutcomeIncomplete   = "incomplete"
	OutcomeNoSlot       = "no-slot"
)

// redactedArgv is argv as kept in run.json and round.json (access.RedactedArgv).
func redactedArgv(argv []string) []string { return access.RedactedArgv(argv) }

// writeRun writes a run's record, its argv redacted (redactedArgv): the one way run.json is written.
func writeRun(path string, run ReviewerRun) error {
	run.Argv = redactedArgv(run.Argv)
	return writeJSON(path, run)
}
