package gh

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/githubapi"
)

// appearStep bounds a look for a run that has not appeared yet.
const appearStep = 10 * time.Second

// repositoryOf is --repo's repository, else the checkout's; then GitHub for it.
func (work *session) repositoryOf(repositoryFlag string) error {
	repository := ""
	if repositoryFlag != "" {
		found, err := repositoryFromURL(repositoryFlag)
		if err != nil {
			return err
		}
		repository = found
	} else {
		found, err := work.checkoutRepository()
		if err != nil {
			return err
		}
		repository = found
	}
	return work.connect(repository)
}

func runRunWait(work *session, args []string) (int, error) {
	timeoutDefault, intervalDefault, _ := work.waitDefaults()
	flags := commandFlags("run-wait", work.deps.Stderr)
	ref := flags.String("ref", "", "branch name or SHA prefix (at least 7 characters); default: the newest run")
	repositoryFlag := flags.String("repo", "", "https://github.com/<owner>/<repo>; default: this checkout")
	timeout := flags.Float64("timeout", timeoutDefault, "seconds to wait for the run to complete")
	interval := flags.Float64("interval", intervalDefault, "seconds between looks")
	appear := flags.Float64("appear", 120, "seconds to allow the run to be scheduled")
	positional, err := parseFlags(flags, args, 1)
	if err != nil {
		return 0, err
	}
	if len(positional) != 1 {
		return 0, failf(exitRequest, "run-wait needs WORKFLOW (a workflow's file name, e.g. release.yml)")
	}
	timeoutSpan, err := duration("timeout", *timeout, false)
	if err != nil {
		return 0, err
	}
	intervalSpan, err := duration("interval", *interval, true)
	if err != nil {
		return 0, err
	}
	appearSpan, err := duration("appear", *appear, false)
	if err != nil {
		return 0, err
	}
	if err := work.repositoryOf(*repositoryFlag); err != nil {
		return 0, err
	}
	return work.runWait(positional[0], *ref, timeoutSpan, intervalSpan, appearSpan)
}

// matchingRuns are the runs matching ref (a branch, or a SHA prefix of at least seven characters:
// a shorter one is not an identity), newest first; every run when ref is "".
func matchingRuns(runs []githubapi.WorkflowRun, ref string) []githubapi.WorkflowRun {
	if ref == "" {
		return runs
	}
	var out []githubapi.WorkflowRun
	for _, run := range runs {
		if run.HeadBranch == ref || (len(ref) >= 7 && strings.HasPrefix(run.HeadSHA, ref)) {
			out = append(out, run)
		}
	}
	return out
}

// runVerdict is a run's exit code and verdict: 0 good, 1 bad, 3 not completed. A completed run
// with no conclusion is not a success.
func runVerdict(run githubapi.WorkflowRun) (int, string) {
	if run.Status != "completed" {
		status := run.Status
		if status == "" {
			status = "unknown"
		}
		return exitPending, status
	}
	switch run.Conclusion {
	case "success", "skipped", "neutral":
		return exitFine, "completed: " + run.Conclusion
	case "":
		return exitNotFine, "completed: no conclusion"
	}
	return exitNotFine, "completed: " + run.Conclusion
}

// runWait blocks until the newest run of workflow for ref completes. The run may not exist yet (a
// merge just happened): it gets appear (never past the deadline) to show up, and its absence
// after that is exit 3 with what was seen.
func (work *session) runWait(workflow, ref string, timeout, interval, appear time.Duration) (int, error) {
	appear = min(appear, timeout)
	started := work.deps.Clock.Now()
	deadline, appearBy := started.Add(timeout), started.Add(appear)
	var chosen *githubapi.WorkflowRun
	for {
		runs, err := work.github.WorkflowRuns(work.ctx, githubapi.RunFilter{Workflow: workflow})
		if err != nil {
			return 0, fmt.Errorf("the runs of %s: %w", workflow, err)
		}
		if chosen == nil {
			matches := matchingRuns(runs, ref)
			if len(matches) == 0 {
				if !work.deps.Clock.Now().Before(appearBy) {
					var seen []string
					for _, run := range runs[:min(5, len(runs))] {
						seen = append(seen, run.HeadBranch+"@"+short(run.HeadSHA, 7))
					}
					if len(seen) == 0 {
						seen = []string{"nothing"}
					}
					asked := ref
					if asked == "" {
						asked = "any ref"
					}
					return 0, failf(exitPending, "no run of %s for %s in %s within %s (seen: %s)", workflow, asked, work.repository,
						humanSeconds(appear), strings.Join(seen, ", "))
				}
				if err := work.deps.Clock.Sleep(work.ctx, min(interval, appearStep, appearBy.Sub(work.deps.Clock.Now()))); err != nil {
					return 0, err
				}
				continue
			}
			first := matches[0]
			chosen = &first
			if len(matches) > 1 {
				var others []string
				for _, run := range matches[1:min(4, len(matches))] {
					others = append(others, fmt.Sprintf("%d %s/%s", run.ID, run.Status, run.Conclusion))
				}
				work.say("%d runs of %s match %s; watching the newest, the others: %s", len(matches), workflow, ref, strings.Join(others, ", "))
			}
			work.say("%s run %d on %s@%s in %s", workflow, chosen.ID, chosen.HeadBranch, short(chosen.HeadSHA, 7), work.repository)
		}
		current, found := githubapi.WorkflowRun{}, false
		for _, run := range runs {
			if run.ID == chosen.ID {
				current, found = run, true
			}
		}
		if !found {
			// Pushed out of the newest listed by later runs: asked for by its id, never the stale row.
			current, err = work.github.WorkflowRun(work.ctx, chosen.ID)
			if err != nil {
				return 0, fmt.Errorf("run %d: %w", chosen.ID, err)
			}
			if current.ID != chosen.ID {
				return 0, failf(exitFormat, "asked for run %d, GitHub answered about run %d", chosen.ID, current.ID)
			}
		}
		code, verdict := runVerdict(current)
		if code != exitPending {
			work.say("%s %s", workflow, verdict)
			return code, nil
		}
		now := work.deps.Clock.Now()
		if !now.Before(deadline) {
			work.say("%s still %s after %s", workflow, verdict, humanSeconds(timeout))
			return exitPending, nil
		}
		if err := work.deps.Clock.Sleep(work.ctx, min(interval, deadline.Sub(now))); err != nil {
			return 0, err
		}
	}
}

// --- pr-find -----------------------------------------------------------------------------------

var pullStates = map[string]bool{"open": true, "closed": true, "merged": true, "all": true}

func runPRFind(work *session, args []string) (int, error) {
	_, intervalDefault, _ := work.waitDefaults()
	flags := commandFlags("pr-find", work.deps.Stderr)
	head := flags.String("head", "", "the pull request's head branch")
	search := flags.String("search", "", "GitHub search text")
	repositoryFlag := flags.String("repo", "", "https://github.com/<owner>/<repo>; default: this checkout")
	wait := flags.Float64("wait", 0, "seconds to wait for it to appear")
	state := flags.String("state", "open", "open, closed, merged or all")
	if _, err := parseFlags(flags, args, 0); err != nil {
		return 0, err
	}
	if *head == "" && *search == "" {
		return 0, failf(exitRequest, "pr-find needs --head or --search")
	}
	if !pullStates[*state] {
		return 0, failf(exitRequest, "--state is open, closed, merged or all, not %q", *state)
	}
	waitSpan, err := duration("wait", *wait, false)
	if err != nil {
		return 0, err
	}
	if err := work.repositoryOf(*repositoryFlag); err != nil {
		return 0, err
	}
	interval, _ := duration("interval", intervalDefault, true)
	return work.prFind(*head, *search, *state, waitSpan, interval)
}

// inState says whether a pull request is in a pr-find state ("closed" includes merged, as gh's).
func inState(pull githubapi.PullRequest, state string) bool {
	switch state {
	case "open":
		return pull.State == "open"
	case "closed":
		return pull.State == "closed"
	case "merged":
		return pull.Merged
	}
	return true
}

// findPull is the newest pull request by head and search in the state, or none.
func (work *session) findPull(head, search, state string) (githubapi.PullRequest, bool, error) {
	var pulls []githubapi.PullRequest
	var err error
	if search != "" {
		// GitHub's search, as `gh pr list --search` runs it: `is:pr TEXT repo:owner/name`.
		pulls, err = work.github.SearchPullRequestsBySHA(work.ctx, search)
	} else {
		owner, _, _ := strings.Cut(work.repository, "/")
		listState := state
		if state == "merged" {
			listState = "closed" // REST lists no merged state: merged is closed with merged_at
		}
		pulls, err = work.github.PullRequestsByHead(work.ctx, owner+":"+head, listState)
	}
	if err != nil {
		return githubapi.PullRequest{}, false, err
	}
	sort.SliceStable(pulls, func(left, right int) bool { return pulls[left].Number > pulls[right].Number })
	for _, pull := range pulls {
		if (head == "" || pull.Head == head) && inState(pull, state) {
			return pull, true, nil
		}
	}
	return githubapi.PullRequest{}, false, nil
}

// prFind prints one pull request as `number head url`, waiting up to wait for it to appear (a
// bot's pull request after a publish). Nothing found is exit 1, never "null".
func (work *session) prFind(head, search, state string, wait, interval time.Duration) (int, error) {
	deadline := work.deps.Clock.Now().Add(wait)
	for {
		pull, found, err := work.findPull(head, search, state)
		if err != nil {
			return 0, err
		}
		if found {
			work.say("%d %s %s", pull.Number, pull.Head, pull.URL)
			return exitFine, nil
		}
		now := work.deps.Clock.Now()
		if !now.Before(deadline) {
			var asked []string
			if head != "" {
				asked = append(asked, "head "+head)
			}
			if search != "" {
				asked = append(asked, fmt.Sprintf("search %q", search))
			}
			after := ""
			if wait > 0 {
				after = " after " + humanSeconds(wait)
			}
			work.say("no %s pull request in %s for %s%s", state, work.repository, strings.Join(asked, " and "), after)
			return exitNotFine, nil
		}
		if err := work.deps.Clock.Sleep(work.ctx, min(interval, deadline.Sub(now))); err != nil {
			return 0, err
		}
	}
}
