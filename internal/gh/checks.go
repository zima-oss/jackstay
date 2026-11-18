package gh

import (
	"fmt"
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
	// rulesOf is the base whose rules were read: a pull request retargeted while waiting stops.
	rulesOf string
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
	number, err := work.resolvePull(target)
	if err != nil {
		return 0, err
	}
	return work.ciWait(number, options)
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

// ciWait blocks until every check on the pull request's head has settled: 0 all passed or
// skipped, 1 one failed or was cancelled (and why is printed), 3 the deadline passed with checks
// pending or none registered, 4 the head moved.
func (work *session) ciWait(number int, options waitOptions) (int, error) {
	pull, err := work.github.PullRequest(work.ctx, number)
	if err != nil {
		return 0, fmt.Errorf("pull request #%d: %w", number, err)
	}
	head := pull.HeadSHA
	work.say("PR #%d (%s) head %s in %s", number, pull.Head, short(head, 8), work.repository)
	started := work.deps.Clock.Now()
	deadline := started.Add(options.timeout)
	announced := false
	for {
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
		if options.rulesOf != "" && now.Base != options.rulesOf {
			return 0, failf(exitMoved, "the base of #%d changed from %s to %s while waiting: the checks waited for are the ones "+
				"%s requires. Run it again.", number, options.rulesOf, now.Base, options.rulesOf)
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
		if len(rows) == 0 {
			// Queued but nothing registered: the state four hand-rolled loops took for "finished".
			if !announced {
				work.say("no checks registered for this commit yet — waiting for the run to appear (this is not a pass)")
				announced = true
			}
			more, err := work.sleepUntil(deadline, options.interval)
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
		weighed := weigh(rows, options.required)
		pending := weighed.pending
		if len(pending) > 0 {
			names := strings.Join(pending[:min(4, len(pending))], ", ")
			if len(pending) > 4 {
				names += fmt.Sprintf(" (+%d)", len(pending)-4)
			}
			work.say("  %s: %d pending — %s", humanSeconds(work.deps.Clock.Now().Sub(started)), len(pending), names)
			more, err := work.sleepUntil(deadline, options.interval)
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
		work.say("\n(%d check runs attached to %s)", checkRuns(rows), short(head, 8))
		for _, line := range weighed.notes() {
			work.say("%s", line)
		}
		if len(weighed.failed) > 0 {
			work.say("%s", work.failureDetail(weighed.failed, options.lines))
			work.say("\nRED: %d of %d checks did not pass.", len(weighed.failed), weighed.judged)
			return exitNotFine, nil
		}
		work.say("\nGREEN: %d checks, %s waiting.", weighed.judged, humanSeconds(work.deps.Clock.Now().Sub(started)))
		work.judged[number] = head
		return exitFine, nil
	}
}

// ciStatus is one look, no waiting: 0 green, 1 a check failed, 3 pending or nothing registered,
// 4 the head moved while the checks were read.
func (work *session) ciStatus(number int) (int, error) {
	pull, err := work.github.PullRequest(work.ctx, number)
	if err != nil {
		return 0, fmt.Errorf("pull request #%d: %w", number, err)
	}
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
	if len(rows) == 0 {
		work.say("no checks registered for this ref yet")
		return exitPending, nil
	}
	work.say("%s", summarise(rows))
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

// weighed is a head's checks judged: the ones still to wait for, the ones that stop a merge, and
// what is only reported.
type weighed struct {
	pending    []string          // names waited for: not settled, or required and not registered
	failed     []githubapi.Check // failed or cancelled checks that count
	judged     int               // how many checks count
	superseded []githubapi.Check // cancelled runs a newer run of the same workflow replaced
	ignored    []githubapi.Check // not required, and pending or cancelled: reported, not waited for
}

// weigh judges rows against the required checks (none: every check counts). A cancelled check run
// is superseded when a newer run of its workflow for the same event (a higher run id) has a check
// of the same name: a concurrency group cancelled it for that run, which is judged instead.
func weigh(rows []githubapi.Check, required []RequiredCheck) weighed {
	var out weighed
	var live []githubapi.Check
	for _, row := range rows {
		if replacedBy(row, rows) {
			out.superseded = append(out.superseded, row)
			continue
		}
		live = append(live, row)
	}
	present := make([]bool, len(required))
	for _, row := range live {
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

// replacedBy says whether row is a cancelled check run that a newer run of its workflow, started
// by the same event, replaced. A run of another event (a manual dispatch beside a push) is an
// independent run: it does not hide the cancellation.
func replacedBy(row githubapi.Check, rows []githubapi.Check) bool {
	if row.Kind != githubapi.CheckRun || row.Bucket() != githubapi.Cancel || row.WorkflowID == 0 || row.RunID == 0 || row.Event == "" {
		return false
	}
	for _, other := range rows {
		if other.Kind == githubapi.CheckRun && other.WorkflowID == row.WorkflowID && other.Event == row.Event &&
			other.Name == row.Name && other.RunID > row.RunID {
			return true
		}
	}
	return false
}

// notes are the lines that say what was set aside: superseded runs, checks not required, and
// cancelled checks that count (a cancellation, not a failure).
func (judged weighed) notes() []string {
	var lines []string
	for _, row := range judged.superseded {
		lines = append(lines, fmt.Sprintf("superseded: %s (run %d of %s, cancelled; a newer run replaced it)", row.Name, row.RunID, row.Workflow))
	}
	for _, row := range judged.ignored {
		lines = append(lines, fmt.Sprintf("not required: %s %s (not waited for)", row.Name, row.Bucket()))
	}
	for _, row := range judged.failed {
		if row.Bucket() == githubapi.Cancel {
			lines = append(lines, fmt.Sprintf("cancelled: %s (run %d of %s), and no newer run replaced it", row.Name, row.RunID, row.Workflow))
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
