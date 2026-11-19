package gh

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/githubapi"
)

// settled are the buckets of a finished check; pending is not one of them.
var settled = map[githubapi.Bucket]bool{githubapi.Pass: true, githubapi.Fail: true, githubapi.Skipping: true, githubapi.Cancel: true}

// failing are the settled buckets that are not a pass: a cancelled check is red too.
func failing(bucket githubapi.Bucket) bool {
	return bucket == githubapi.Fail || bucket == githubapi.Cancel
}

// waitOptions are ci-wait's flags.
type waitOptions struct {
	timeout  time.Duration
	interval time.Duration
	lines    int
	anyHead  bool
	// required are the checks the base branch's rules require (pr-land reads them). Empty, every
	// check is waited for and judged; else only these are waited for, and of the others only a
	// failure stops (one pending, never started or cancelled is reported, not waited for).
	required []RequiredCheck
}

// RequiredCheck is a check a branch's rules require: its name, and the app that must report it
// (0: any).
type RequiredCheck struct {
	Name  string
	AppID int64
}

// satisfiedBy says whether a row is this required check: its name, and its app when one is named
// (a commit status names no app, so it is not one that requires an app).
func (check RequiredCheck) satisfiedBy(row githubapi.Check) bool {
	return row.Name == check.Name && (check.AppID == 0 || (row.Kind == githubapi.CheckRun && row.AppID == check.AppID))
}

func (check RequiredCheck) String() string {
	if check.AppID == 0 {
		return check.Name
	}
	return fmt.Sprintf("%s (app %d)", check.Name, check.AppID)
}

func (work *session) waitFlags(name string, args []string, most int) ([]string, waitOptions, error) {
	timeoutDefault, intervalDefault, linesDefault := work.waitDefaults()
	flags := commandFlags(name, work.deps.Stderr)
	timeout := flags.Float64("timeout", timeoutDefault, "seconds to wait for every check to settle")
	interval := flags.Float64("interval", intervalDefault, "seconds between looks")
	lines := flags.Int("lines", linesDefault, "log lines to show per failed check")
	anyHead := flags.Bool("any-head", false, "follow the pull request's head when it moves (do not stop with exit 4)")
	positional, err := parseFlags(flags, args, most)
	if err != nil {
		return nil, waitOptions{}, err
	}
	options := waitOptions{lines: *lines, anyHead: *anyHead}
	if options.timeout, err = duration("timeout", *timeout, false); err != nil {
		return nil, waitOptions{}, err
	}
	if options.interval, err = duration("interval", *interval, true); err != nil {
		return nil, waitOptions{}, err
	}
	if options.lines < 1 {
		return nil, waitOptions{}, failf(exitRequest, "--lines must be above 0")
	}
	return positional, options, nil
}

func runCIWait(work *session, args []string) (int, error) {
	positional, options, err := work.waitFlags("ci-wait", args, 1)
	if err != nil {
		return 0, err
	}
	target, _, err := parsePullTarget(firstOf(positional))
	if err != nil {
		return 0, err
	}
	return work.withCIWaitBudget(options.timeout, func(started, deadline time.Time) (int, error) {
		number, err := work.resolvePull(target)
		if err != nil {
			return 0, err
		}
		return work.ciWaitUntil(number, options, started, deadline)
	})
}

func runCIStatus(work *session, args []string) (int, error) {
	positional, err := parseFlags(commandFlags("ci-status", work.deps.Stderr), args, 1)
	if err != nil {
		return 0, err
	}
	target, _, err := parsePullTarget(firstOf(positional))
	if err != nil {
		return 0, err
	}
	number, err := work.resolvePull(target)
	if err != nil {
		return 0, err
	}
	return work.ciStatus(number)
}

func firstOf(words []string) string {
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

// checkEvidence binds the result to the pull request and the commits whose checks were read.
// With required checks, the test merge SHA also records whether GitHub had made a candidate.
type checkEvidence struct {
	repository string
	pull       githubapi.PullRequest
	watchMerge bool
}

func (seen checkEvidence) confirm(repository string, now githubapi.PullRequest) error {
	before := seen.pull
	switch {
	case !strings.EqualFold(seen.repository, repository), before.Number != now.Number, before.Head != now.Head,
		!strings.EqualFold(before.HeadRepository, now.HeadRepository), !strings.EqualFold(before.BaseRepository, now.BaseRepository):
		return failf(exitMoved, "the identity of #%d changed after its checks were read; run it again", before.Number)
	case before.HeadSHA != now.HeadSHA:
		return failf(exitMoved, "the head of #%d changed from %s to %s after its checks were read; run it again",
			before.Number, short(before.HeadSHA, 8), short(now.HeadSHA, 8))
	case before.Base != now.Base:
		return failf(exitMoved, "the base of #%d changed from %s to %s after its checks were read; run it again",
			before.Number, before.Base, now.Base)
	case before.BaseSHA != "" && before.BaseSHA != now.BaseSHA:
		return failf(exitMoved, "the base commit of #%d changed after its checks were read (checked %s); run it again",
			before.Number, short(before.BaseSHA, 8))
	case seen.watchMerge && before.MergeCommitSHA != now.MergeCommitSHA:
		return failf(exitMoved, "the test merge commit of #%d changed after its checks were read; run it again", before.Number)
	}
	return nil
}

// ciWait blocks until every check on the pull request's head has settled: 0 all passed or
// skipped, 1 one failed or was cancelled (and why is printed), 3 the deadline passed with checks
// pending or none registered, 4 the head moved.
func (work *session) ciWait(number int, options waitOptions) (int, error) {
	return work.withCIWaitBudget(options.timeout, func(started, deadline time.Time) (int, error) {
		return work.ciWaitUntil(number, options, started, deadline)
	})
}

// withCIWaitBudget bounds the GitHub requests, including authentication and branch lookup when
// called by ci-wait. pr-land gets its original context back for work outside the check wait.
func (work *session) withCIWaitBudget(timeout time.Duration, wait func(started, deadline time.Time) (int, error)) (int, error) {
	started := work.deps.Clock.Now()
	budget := timeout
	if budget == 0 {
		budget = 10 * time.Second // one immediate look may still need several requests
	}
	original := work.ctx
	ctx, cancel := context.WithTimeout(original, budget)
	work.ctx = ctx
	defer func() {
		work.ctx = original
		cancel()
	}()
	code, err := wait(started, started.Add(timeout))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && (code != exitNotFine || err != nil) {
		return 0, failf(exitPending, "the check wait reached its deadline after %s while asking GitHub", humanSeconds(budget))
	}
	return code, err
}

func (work *session) ciWaitUntil(number int, options waitOptions, started, deadline time.Time) (int, error) {
	work.notePull(number, "")
	pull, err := work.github.PullRequest(work.ctx, number)
	if err != nil {
		return 0, fmt.Errorf("pull request #%d: %w", number, err)
	}
	head := pull.HeadSHA
	work.say("PR #%d (%s) head %s in %s", number, pull.Head, short(head, 8), work.repository)
	nextLook := func() (bool, error) {
		more, err := work.sleepUntil(deadline, options.interval)
		return more && work.deps.Clock.Now().Before(deadline), err
	}
	announced, mergeSaid := false, false
	for {
		work.notePull(number, head)
		rows, err := work.github.Checks(work.ctx, head)
		if err != nil {
			return 0, fmt.Errorf("the checks of %s: %w", short(head, 8), err)
		}
		// The head is compared AFTER the rows are read: rows read and then a head still the one
		// announced means the rows are that head's.
		now, err := work.github.PullRequest(work.ctx, number)
		if err != nil {
			return 0, fmt.Errorf("pull request #%d: %w", number, err)
		}
		if err := work.sameBase(now); err != nil {
			return 0, err
		}
		if now.HeadSHA != head {
			if !options.anyHead {
				return 0, failf(exitMoved, "the head of #%d moved from %s to %s while waiting. Checks for the old commit say "+
					"nothing about the new one; re-run against the new head.", number, short(head, 8), short(now.HeadSHA, 8))
			}
			work.say("the head of #%d moved from %s to %s; following it (--any-head)", number, short(head, 8), short(now.HeadSHA, 8))
			head = now.HeadSHA
			if !work.deps.Clock.Now().Before(deadline) {
				return 0, failf(exitPending, "the head of #%d was still moving after %s", number, humanSeconds(options.timeout))
			}
			continue
		}
		evidence := checkEvidence{repository: work.repository, pull: now, watchMerge: len(options.required) > 0}
		onMerge := false
		if evidence.watchMerge && now.MergeCommitSHA != "" {
			if rows, onMerge, err = work.withMergeCommitRows(rows, options.required, now.MergeCommitSHA); err != nil {
				return 0, err
			}
			confirmed, err := work.github.PullRequest(work.ctx, number)
			if err != nil {
				return 0, fmt.Errorf("pull request #%d: %w", number, err)
			}
			if err := evidence.confirm(work.repository, confirmed); err != nil {
				return 0, err
			}
			evidence.pull = confirmed
			if onMerge && !mergeSaid {
				work.say("the test merge commit %s has results: the required checks are judged on it, as GitHub judges them",
					short(now.MergeCommitSHA, 8))
				mergeSaid = true
			}
		}
		if len(rows) == 0 && len(options.required) == 0 {
			// Queued but nothing registered (with required checks, weigh names the ones missing): the state four hand-rolled loops took for "finished".
			if !announced {
				work.say("no checks registered for this commit yet — waiting for the run to appear (this is not a pass)")
				announced = true
			}
			more, err := nextLook()
			if err != nil {
				return 0, err
			}
			if !more {
				return 0, failf(exitPending, "no check ever registered for %s within %s. Either no workflow matches this branch, "+
					"or a `runs-on` label matches no runner — a mistyped label is a job that queues for ever.",
					short(head, 8), humanSeconds(options.timeout))
			}
			continue
		}
		candidateSHAs := []string{head}
		if onMerge {
			candidateSHAs = append(candidateSHAs, now.MergeCommitSHA)
		}
		rows, replacements, err := work.reconcileCancelled(rows, candidateSHAs)
		if err != nil {
			return 0, err
		}
		if len(replacements) > 0 {
			confirmed, err := work.github.PullRequest(work.ctx, number)
			if err != nil {
				return 0, fmt.Errorf("pull request #%d: %w", number, err)
			}
			if err := evidence.confirm(work.repository, confirmed); err != nil {
				return 0, err
			}
			evidence.pull = confirmed
		}
		for _, replacement := range replacements {
			work.say("%s", replacement)
		}
		weighed := weigh(rows, options.required)
		pending := weighed.pending
		if len(pending) > 0 && len(weighed.failed) == 0 {
			names := strings.Join(pending[:min(4, len(pending))], ", ")
			if len(pending) > 4 {
				names += fmt.Sprintf(" (+%d)", len(pending)-4)
			}
			work.say("  %s: %d pending — %s", humanSeconds(work.deps.Clock.Now().Sub(started)), len(pending), names)
			more, err := nextLook()
			if err != nil {
				return 0, err
			}
			if !more {
				work.say("\n%s", summarise(rows))
				return 0, failf(exitPending, "still pending after %s: %s", humanSeconds(options.timeout), strings.Join(pending, ", "))
			}
			continue
		}
		work.say("\n%s", summarise(rows))
		if onMerge {
			work.say("\n(%d check runs judged for head %s and test merge commit %s)",
				checkRuns(rows), short(head, 8), short(now.MergeCommitSHA, 8))
		} else {
			work.say("\n(%d check runs attached to %s)", checkRuns(rows), short(head, 8))
		}
		for _, line := range weighed.notes() {
			work.say("%s", line)
		}
		if len(weighed.failed) > 0 {
			work.say("%s", work.failureDetail(weighed.failed, options.lines))
			work.say("\nRED: %d of %d checks did not pass.", len(weighed.failed), weighed.judged)
			return exitNotFine, nil
		}
		work.say("\nGREEN: %d checks, %s waiting.", weighed.judged, humanSeconds(work.deps.Clock.Now().Sub(started)))
		work.judged[number] = evidence
		return exitFine, nil
	}
}

// ciStatus is one look, no waiting: 0 green, 1 a check failed, 3 pending or nothing registered,
// 4 the head moved while the checks were read.
func (work *session) ciStatus(number int) (int, error) {
	work.notePull(number, "")
	pull, err := work.github.PullRequest(work.ctx, number)
	if err != nil {
		return 0, fmt.Errorf("pull request #%d: %w", number, err)
	}
	work.notePull(number, pull.HeadSHA)
	rows, err := work.github.Checks(work.ctx, pull.HeadSHA)
	if err != nil {
		return 0, fmt.Errorf("the checks of %s: %w", short(pull.HeadSHA, 8), err)
	}
	// As in ciWait: the head is compared after the rows are read, so the rows are that head's.
	now, err := work.github.PullRequest(work.ctx, number)
	if err != nil {
		return 0, fmt.Errorf("pull request #%d: %w", number, err)
	}
	if now.HeadSHA != pull.HeadSHA {
		return 0, failf(exitMoved, "the head of #%d moved from %s to %s while its checks were read. Checks for the old "+
			"commit say nothing about the new one; run it again.", number, short(pull.HeadSHA, 8), short(now.HeadSHA, 8))
	}
	rows, replacements, err := work.reconcileCancelled(rows, []string{pull.HeadSHA})
	if err != nil {
		return 0, err
	}
	if len(replacements) > 0 {
		confirmed, err := work.github.PullRequest(work.ctx, number)
		if err != nil {
			return 0, fmt.Errorf("pull request #%d: %w", number, err)
		}
		if err := (checkEvidence{repository: work.repository, pull: now}).confirm(work.repository, confirmed); err != nil {
			return 0, err
		}
	}
	if len(rows) == 0 {
		work.say("no checks registered for this ref yet")
		return exitPending, nil
	}
	work.say("%s", summarise(rows))
	for _, replacement := range replacements {
		work.say("%s", replacement)
	}
	weighed := weigh(rows, nil)
	for _, line := range weighed.notes() {
		work.say("%s", line)
	}
	if len(weighed.failed) > 0 {
		return exitNotFine, nil
	}
	if len(weighed.pending) > 0 {
		return exitPending, nil
	}
	return exitFine, nil
}

// withMergeCommitRows applies GitHub's rule for the required checks, which is by commit: when the
// pull request's test merge commit has any result, the required checks are judged on it; else on
// the head. On the merge commit, the head's rows of required checks are dropped (one the merge
// commit lacks is then waited for), and the merge commit's rows of required checks are judged in
// their place. A merge commit GitHub has not made yet, or that has no result, changes nothing.
func (work *session) withMergeCommitRows(rows []githubapi.Check, required []RequiredCheck, mergeSHA string) ([]githubapi.Check, bool, error) {
	merged, err := work.github.Checks(work.ctx, mergeSHA)
	var notFound *githubapi.CommitNotFoundError
	if errors.As(err, &notFound) {
		return rows, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("the checks of the test merge commit %s: %w", short(mergeSHA, 8), err)
	}
	if len(merged) == 0 {
		return rows, false, nil
	}
	isRequired := func(row githubapi.Check) bool {
		return slices.ContainsFunc(required, func(check RequiredCheck) bool { return check.satisfiedBy(row) })
	}
	var out []githubapi.Check
	for _, row := range rows {
		if !isRequired(row) {
			out = append(out, row)
		}
	}
	for _, row := range merged {
		if isRequired(row) {
			out = append(out, row)
		}
	}
	return out, true, nil
}

// sameBase refuses a pull request whose base is no longer the one whose rules pr-land read: the
// checks waited for are the old base's. Asked before every write and in every look.
func (work *session) sameBase(pull githubapi.PullRequest) error {
	rulesOf := work.rulesOf[pull.Number]
	if rulesOf == "" || pull.Base == rulesOf {
		return nil
	}
	return failf(exitMoved, "the base of #%d changed from %s to %s: the checks waited for are the ones %s requires. "+
		"Run it again.", pull.Number, rulesOf, pull.Base, rulesOf)
}

// reconcileCancelled replaces a cancelled job only when the repository's run listing proves a
// newer run of the same workflow and commit. A replacement without this job's result stays
// pending; the workflow's success alone cannot satisfy a required check.
func (work *session) reconcileCancelled(rows []githubapi.Check, commits []string) ([]githubapi.Check, []string, error) {
	needsLookup := false
	for _, row := range rows {
		needsLookup = needsLookup || (row.Bucket() == githubapi.Cancel && row.RunID > 0)
	}
	if !needsLookup {
		return rows, nil, nil
	}
	runs := make(map[string][]githubapi.WorkflowRun, len(commits))
	for _, sha := range commits {
		if sha == "" {
			continue
		}
		listed, err := work.github.WorkflowRuns(work.ctx, githubapi.RunFilter{HeadSHA: sha})
		if err != nil {
			return nil, nil, fmt.Errorf("workflow runs of %s: %w", short(sha, 8), err)
		}
		runs[sha] = listed
	}
	updated := make([]githubapi.Check, 0, len(rows))
	var notes []string
	for _, row := range rows {
		if row.Bucket() != githubapi.Cancel || row.RunID == 0 {
			updated = append(updated, row)
			continue
		}
		var replacement githubapi.WorkflowRun
		for sha, listed := range runs {
			var original githubapi.WorkflowRun
			for _, run := range listed {
				if run.ID == row.RunID && run.HeadSHA == sha && run.WorkflowID > 0 {
					original = run
					break
				}
			}
			if original.ID == 0 {
				continue
			}
			for _, run := range listed {
				if run.WorkflowID != original.WorkflowID || run.HeadSHA != sha ||
					(original.Event != "" && run.Event != original.Event) ||
					(original.HeadBranch != "" && run.HeadBranch != original.HeadBranch) ||
					!newerWorkflowRun(run, original) {
					continue
				}
				if replacement.ID == 0 || newerWorkflowRun(run, replacement) {
					replacement = run
				}
			}
		}
		if replacement.ID == 0 {
			updated = append(updated, row)
			continue
		}
		notes = append(notes, fmt.Sprintf("run %d supersedes cancelled run %d of %s for %s", replacement.ID, row.RunID, row.Workflow, row.Name))
		found := false
		for _, current := range rows {
			if current.RunID == replacement.ID && current.Kind == githubapi.CheckRun && current.Name == row.Name && current.AppID == row.AppID {
				found = true
				break
			}
		}
		if found {
			continue
		}
		row.RunID = replacement.ID
		row.Status, row.Conclusion = "IN_PROGRESS", ""
		if replacement.Status == "completed" && replacement.Conclusion != "success" && replacement.Conclusion != "skipped" && replacement.Conclusion != "neutral" {
			row.Status, row.Conclusion = "COMPLETED", "FAILURE"
		}
		updated = append(updated, row)
	}
	return updated, notes, nil
}

func newerWorkflowRun(candidate, previous githubapi.WorkflowRun) bool {
	if candidate.ID <= previous.ID {
		return false
	}
	if !candidate.CreatedAt.IsZero() && !previous.CreatedAt.IsZero() && candidate.CreatedAt.Before(previous.CreatedAt) {
		return false
	}
	return true
}

// weighed is a head's checks judged: the ones still to wait for, the ones that stop a merge, and
// what is only reported.
type weighed struct {
	pending []string          // names waited for: not settled, or required and not registered
	failed  []githubapi.Check // failed or cancelled checks that count
	judged  int               // how many checks count
	ignored []githubapi.Check // not required, and pending or cancelled: reported, not waited for
}

// weigh judges rows against the required checks (none: every check counts).
func weigh(rows []githubapi.Check, required []RequiredCheck) weighed {
	var out weighed
	present := make([]bool, len(required))
	for _, row := range rows {
		counts := len(required) == 0
		for index, check := range required {
			if check.satisfiedBy(row) {
				present[index], counts = true, true
			}
		}
		bucket := row.Bucket()
		switch {
		case counts && !settled[bucket]:
			out.pending = append(out.pending, row.Name)
			out.judged++
		case counts && failing(bucket), !counts && bucket == githubapi.Fail:
			out.failed = append(out.failed, row)
			out.judged++
		case counts:
			out.judged++
		case bucket == githubapi.Pending || bucket == githubapi.Cancel:
			out.ignored = append(out.ignored, row)
		}
	}
	for index, check := range required {
		if !present[index] {
			out.pending = append(out.pending, check.String()+" (not registered yet)")
		}
	}
	return out
}

// notes are the lines that say what was set aside: checks not required, and
// cancelled checks that count (a cancellation, not a failure).
func (judged weighed) notes() []string {
	var lines []string
	for _, row := range judged.ignored {
		lines = append(lines, fmt.Sprintf("not required: %s %s (not waited for)", row.Name, row.Bucket()))
	}
	for _, row := range judged.failed {
		if row.Bucket() == githubapi.Cancel {
			lines = append(lines, fmt.Sprintf("cancelled: %s (run %d of %s): a cancellation is not a pass", row.Name, row.RunID, row.Workflow))
		}
	}
	return lines
}

// checkRuns is how many of the rows are check runs (the rest are commit statuses), counted over
// every page of the rollup.
func checkRuns(rows []githubapi.Check) int {
	count := 0
	for _, row := range rows {
		if row.Kind == githubapi.CheckRun {
			count++
		}
	}
	return count
}

var bucketOrder = map[githubapi.Bucket]int{githubapi.Fail: 0, githubapi.Cancel: 1, githubapi.Pending: 2, githubapi.Pass: 3, githubapi.Skipping: 4}

// summarise is one line per check: red first, then pending, then green.
func summarise(rows []githubapi.Check) string {
	sorted := append([]githubapi.Check(nil), rows...)
	sort.SliceStable(sorted, func(left, right int) bool {
		leftOrder, rightOrder := bucketOrder[sorted[left].Bucket()], bucketOrder[sorted[right].Bucket()]
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return sorted[left].Name < sorted[right].Name
	})
	width := 0
	for _, row := range sorted {
		width = max(width, len(row.Name))
	}
	lines := make([]string, 0, len(sorted))
	for _, row := range sorted {
		lines = append(lines, strings.TrimRight(fmt.Sprintf("  %-*s  %-8s %s", width, row.Name, row.Bucket(), row.Workflow), " "))
	}
	return strings.Join(lines, "\n")
}

// failureDetail is, for each failed check, its failed step's log: a red result that does not say
// why is a red result someone has to go and ask about.
func (work *session) failureDetail(rows []githubapi.Check, lines int) string {
	var out []string
	for _, row := range rows {
		if row.Bucket() != githubapi.Fail {
			continue
		}
		out = append(out, fmt.Sprintf("\n--- %s — %s", row.Name, row.Link))
		if row.RunID == 0 {
			out = append(out, "    (no workflow run behind this check; open its link by hand)")
			continue
		}
		jobs, err := work.github.Jobs(work.ctx, row.RunID)
		if err != nil {
			out = append(out, fmt.Sprintf("    (the jobs of run %d cannot be read: %v)", row.RunID, err))
			continue
		}
		failedJobs := failedJobsOf(jobs, row.Name)
		if len(failedJobs) == 0 {
			out = append(out, "    (no failed job in its run; the job may have been cancelled during setup, which reads as a failure here)")
			continue
		}
		for _, job := range failedJobs {
			out = append(out, work.jobDetail(job, lines)...)
		}
	}
	return strings.Join(out, "\n")
}

// failedJobsOf are the run's failed jobs named as the check (an Actions check run is its job),
// else every failed job of the run.
func failedJobsOf(jobs []githubapi.Job, name string) []githubapi.Job {
	var named, all []githubapi.Job
	for _, job := range jobs {
		if job.Conclusion != "failure" && job.Conclusion != "timed_out" {
			continue
		}
		all = append(all, job)
		if job.Name == name {
			named = append(named, job)
		}
	}
	if len(named) > 0 {
		return named
	}
	return all
}

// jobDetail is a failed job's failed step, cut from the job's log by the step's start and end; when
// it cannot be cut, the log's last lines, saying so.
func (work *session) jobDetail(job githubapi.Job, lines int) []string {
	log, err := work.github.JobLog(work.ctx, job.ID)
	if err != nil {
		return []string{fmt.Sprintf("    (the log of job %q cannot be read: %v)", job.Name, err)}
	}
	var step *githubapi.Step
	for index := range job.Steps {
		if job.Steps[index].Conclusion == "failure" || job.Steps[index].Conclusion == "timed_out" {
			step = &job.Steps[index]
			break
		}
	}
	if step != nil {
		if cut := cutLog(log, step.StartedAt, step.CompletedAt); len(cut) > 0 {
			out := []string{fmt.Sprintf("    [%s / %s]", job.Name, step.Name)}
			return append(out, indent(tail(cut, lines))...)
		}
	}
	whole := tail(stripStamps(log), lines)
	if len(whole) == 0 {
		return []string{fmt.Sprintf("    (job %q has an empty log; it may have been cancelled during setup)", job.Name)}
	}
	reason := "no step is marked failed"
	if step != nil {
		reason = fmt.Sprintf("the step %q could not be cut from it", step.Name)
	}
	out := []string{fmt.Sprintf("    [%s] (%s: the job log's last %d lines)", job.Name, reason, len(whole))}
	return append(out, indent(whole)...)
}

// stamped splits a job log's line into its timestamp and its text.
func stamped(line string) (time.Time, string, bool) {
	stamp, text, found := strings.Cut(line, " ")
	if !found {
		stamp, text = line, ""
	}
	stampedAt, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(stamp, "\ufeff"))
	if err != nil {
		return time.Time{}, line, false
	}
	return stampedAt, text, true
}

// cutLog is the log's lines inside a step's span. The step's times are whole seconds and its
// lines are not, so the span is widened to the seconds it touches; a line without a stamp goes
// with the line before it.
func cutLog(log string, started, completed time.Time) []string {
	if started.IsZero() || completed.IsZero() {
		return nil
	}
	from, until := started.Truncate(time.Second), completed.Truncate(time.Second).Add(time.Second)
	var out []string
	inside := false
	for _, line := range strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n") {
		lineAt, text, ok := stamped(line)
		if ok {
			inside = !lineAt.Before(from) && lineAt.Before(until)
		}
		if inside {
			out = append(out, text)
		}
	}
	return out
}

func stripStamps(log string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n") {
		_, text, _ := stamped(line)
		out = append(out, text)
	}
	return out
}

// tail is the last count lines that are not blank.
func tail(lines []string, count int) []string {
	var kept []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	return kept[max(0, len(kept)-count):]
}

func indent(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, "    "+line)
	}
	return out
}
