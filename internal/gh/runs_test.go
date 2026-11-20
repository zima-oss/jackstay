package gh

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/githubapi/githubfake"
)

// --- the pull request asked about ---------------------------------------------------------------

func TestAURLTargetsTheRepositoryItNames(t *testing.T) {
	for _, url := range []string{
		"https://github.com/octo-org/web-app/pull/10",
		"https://github.com/octo-org/web-app/pull/10/checks",
		"https://www.github.com/octo-org/web-app/pull/10",
		"https://GitHub.com/octo-org/web-app/pull/10",
		"github.com/octo-org/web-app/pull/10",
		"http://github.com/octo-org/web-app/pull/10/files?x=1#y",
	} {
		target, isURL, err := parsePullTarget(url)
		if err != nil || !isURL || target.repository != "octo-org/web-app" || target.number != 10 {
			t.Fatalf("%s: %+v %v %v", url, target, isURL, err)
		}
	}
	git := newGit() // no git question may be asked: the URL wins over the checkout
	bench := newHarness(t, git)
	bench.hub.OnPullRequest(10, answer(pull(10, "x", shaA)))
	onChecks(bench.hub, shaA, []githubapi.Check{passed("test")})
	bench.expect(exitFine, "status", "https://github.com/octo-org/web-app/pull/10")
	if strings.Join(bench.connected, ",") != "octo-org/web-app" {
		t.Fatalf("connected for %v", bench.connected)
	}
	if len(git.calls) != 0 {
		t.Fatalf("the checkout was asked: %v", git.calls)
	}
}

func TestAURLItCannotReadIsRefusedNotGuessed(t *testing.T) {
	for _, url := range []string{
		"https://github.com/octo-org/web-app/pull/8abc",
		"https://github.com/octo-org/web-app/pulls/8",
		"https://ghe.example.com/octo-org/web-app/pull/8",
		"www.github.com/octo-org/web-app/issues/8",
	} {
		bench := newHarness(t, checkout(shaA)) // the fallback to the checkout would succeed here
		_, stderr := bench.expect(exitRequest, "status", url)
		contains(t, stderr, "Refusing rather than guessing")
		if len(bench.connected) != 0 || len(bench.hub.Calls()) != 0 {
			t.Fatalf("%s: GitHub was asked", url)
		}
	}
}

func TestABranchOrNumberFallsBackToTheCheckout(t *testing.T) {
	for _, ref := range []string{"feat/pull/x", "fix/pulls/y"} {
		bench := newHarness(t, checkout(shaA))
		bench.hub.OnPullRequestsByHead("owner:"+ref, "open", answer([]githubapi.PullRequest{pull(7, ref, shaA)}))
		onPull(bench.hub, pull(7, ref, shaA))
		onChecks(bench.hub, shaA, []githubapi.Check{passed("test")})
		bench.expect(exitFine, "status", ref)
		if bench.connected[0] != "owner/checkout" {
			t.Fatalf("connected for %v", bench.connected)
		}
	}
	bench := newHarness(t, checkout(shaA))
	bench.hub.OnPullRequest(42, answer(pull(42, "x", shaA)))
	onChecks(bench.hub, shaA, []githubapi.Check{passed("test")})
	bench.expect(exitFine, "status", "42")

	bench = newHarness(t, checkout(shaA)) // default: this branch
	bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{pull(7, "feat", shaA)}))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{passed("test")})
	bench.expect(exitFine, "wait")
}

func TestAForksPullRequestOfTheSameBranchIsNotThisOne(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	fork := pull(9, "feat", shaA)
	fork.HeadRepository = "someone/checkout"
	bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{fork}))
	_, stderr := bench.expect(exitNotFine, "status")
	contains(t, stderr, "no open pull request from feat in owner/checkout")
}

func TestACheckoutWithoutARemoteIsRefused(t *testing.T) {
	git := checkout(shaA)
	git.on("remote", ok(""))
	bench := newHarness(t, git)
	_, stderr := bench.expect(exitRequest, "status", "7")
	contains(t, stderr, "no git remote")
	if len(bench.connected) != 0 {
		t.Fatal("GitHub was asked")
	}
}

func TestARemoteOnAnotherHostIsRefused(t *testing.T) {
	git := checkout(shaA)
	git.on("config --get remote.origin.url", ok("https://gitlab.com/owner/checkout.git\n"))
	_, stderr := newHarness(t, git).expect(exitRequest, "status", "7")
	contains(t, stderr, "github.com only")
}

func TestWithoutAnAccountGitHubIsNotAskedAndTheReasonIsSaid(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("JACKSTAY_GITHUB_ACCOUNT", "")
	bench := newHarness(t, checkout(shaA))
	var stdout, stderr strings.Builder
	code := Main([]string{"status", "7"}, Deps{Stdout: &stdout, Stderr: &stderr, Dir: bench.dir,
		Git: func(string) Git { return bench.git }, Connect: connectReal, Clock: bench.clock})
	if code != exitRequest {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	contains(t, stderr.String(), "set github.account in jackstay.json")
}

func TestTheTokensNoteIsSaidOnStandardError(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	bench.note = "GH_TOKEN is ignored outside GitHub Actions"
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{passed("test")})
	stdout, stderr := bench.expect(exitFine, "status", "7")
	contains(t, stderr, "GH_TOKEN is ignored outside GitHub Actions")
	lacks(t, stdout, "GH_TOKEN")
}

// --- run-wait -------------------------------------------------------------------------------

func workflowRun(id int64, branch, sha, status, conclusion string) githubapi.WorkflowRun {
	return githubapi.WorkflowRun{ID: id, HeadBranch: branch, HeadSHA: sha, Status: status, Conclusion: conclusion}
}

func onRuns(hub *fakeGitHub, workflow string, lists ...[]githubapi.WorkflowRun) {
	onFilteredRuns(hub, githubapi.RunFilter{Workflow: workflow}, lists...)
}

func onFilteredRuns(hub *fakeGitHub, filter githubapi.RunFilter, lists ...[]githubapi.WorkflowRun) {
	var answers []githubfake.Result[[]githubapi.WorkflowRun]
	for _, each := range lists {
		answers = append(answers, answer(each))
	}
	hub.OnWorkflowRuns(filter, answers...)
}

func TestRunWaitUsesGitHubToFindAnOlderBranchOrExactCommit(t *testing.T) {
	for _, test := range []struct {
		ref    string
		filter githubapi.RunFilter
	}{
		{"releases/v1", githubapi.RunFilter{Workflow: "ci.yml", Branch: "releases/v1"}},
		{strings.ToUpper(shaA), githubapi.RunFilter{Workflow: "ci.yml", HeadSHA: shaA}},
	} {
		t.Run(test.ref, func(t *testing.T) {
			bench := newHarness(t, checkout(shaA))
			onRuns(bench.hub, "ci.yml", []githubapi.WorkflowRun{workflowRun(110, "main", shaB, "completed", "success")})
			onFilteredRuns(bench.hub, test.filter, []githubapi.WorkflowRun{workflowRun(5, "releases/v1", shaA, "completed", "failure")})
			stdout, _ := bench.expect(exitNotFine, "run-wait", "ci.yml", "--ref", test.ref, "--appear", "0")
			contains(t, stdout, "ci.yml run 5 on releases/v1@aaaaaaa")
			contains(t, stdout, "completed: failure")
		})
	}
}

type workflowDeadlineHub struct {
	*fakeGitHub
	listDeadlines []time.Time
	runDeadlines  []time.Time
}

func (hub *workflowDeadlineHub) WorkflowRuns(ctx context.Context, filter githubapi.RunFilter) ([]githubapi.WorkflowRun, error) {
	deadline, _ := ctx.Deadline()
	hub.listDeadlines = append(hub.listDeadlines, deadline)
	return hub.fakeGitHub.WorkflowRuns(ctx, filter)
}

func (hub *workflowDeadlineHub) WorkflowRun(ctx context.Context, runID int64) (githubapi.WorkflowRun, error) {
	deadline, _ := ctx.Deadline()
	hub.runDeadlines = append(hub.runDeadlines, deadline)
	return hub.fakeGitHub.WorkflowRun(ctx, runID)
}

func TestRunWaitBoundsAppearanceAndRunReadsSeparately(t *testing.T) {
	var stdout strings.Builder
	hub := &workflowDeadlineHub{fakeGitHub: newGitHub()}
	onRuns(hub.fakeGitHub, "ci.yml", []githubapi.WorkflowRun{workflowRun(5, "main", shaA, "queued", "")}, nil)
	hub.runsByID[5] = workflowRun(5, "main", shaA, "completed", "success")
	work := session{ctx: context.Background(), deps: Deps{Stdout: &stdout, Clock: newClock()}, github: hub}
	const appearance, total = time.Hour, 2 * time.Hour
	code, err := work.runWait("ci.yml", "", total, time.Minute, appearance)
	if code != exitFine || err != nil {
		t.Fatalf("exit %d: %v\n%s", code, err, &stdout)
	}
	if len(hub.listDeadlines) != 2 || len(hub.runDeadlines) != 1 {
		t.Fatalf("list deadlines %v, ID deadlines %v", hub.listDeadlines, hub.runDeadlines)
	}
	appearanceDeadline, waitDeadline := hub.listDeadlines[0], hub.listDeadlines[1]
	if appearanceDeadline.IsZero() || waitDeadline.IsZero() ||
		!appearanceDeadline.Before(waitDeadline) || !hub.runDeadlines[0].Equal(waitDeadline) {
		t.Fatalf("appearance %v, wait %v, ID read %v", appearanceDeadline, waitDeadline, hub.runDeadlines[0])
	}
}

func TestARunThatNeverAppearsIsExit3WithWhatWasSeen(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onFilteredRuns(bench.hub, githubapi.RunFilter{Workflow: "ci.yml", Branch: "main"}, nil)
	_, stderr := bench.expect(exitPending, "run-wait", "ci.yml", "--ref", "main", "--appear", "0", "--timeout", "5")
	contains(t, stderr, "no run of ci.yml for main in owner/checkout within 0s (seen: nothing)")
}

func TestTheAppearPhaseNeverOutlivesTheDeadline(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onRuns(bench.hub, "ci.yml", nil)
	_, stderr := bench.expect(exitPending, "run-wait", "ci.yml", "--timeout", "5", "--appear", "60")
	contains(t, stderr, "within 5s (seen: nothing)")
	if slept := bench.clock.slept(); slept.Seconds() != 5 {
		t.Fatalf("slept %v", slept)
	}
}

func TestRunVerdicts(t *testing.T) {
	for conclusion, want := range map[string]int{"success": 0, "skipped": 0, "neutral": 0, "failure": 1, "cancelled": 1, "": 1} {
		bench := newHarness(t, checkout(shaA))
		onFilteredRuns(bench.hub, githubapi.RunFilter{Workflow: "ci.yml", Branch: "main"},
			[]githubapi.WorkflowRun{workflowRun(5, "main", shaA, "completed", conclusion)})
		bench.expect(want, "run-wait", "ci.yml", "--ref", "main", "--timeout", "5")
	}
}

func TestItWaitsForARunToComplete(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onRuns(bench.hub, "ci.yml", nil, []githubapi.WorkflowRun{workflowRun(5, "main", shaA, "queued", "")},
		[]githubapi.WorkflowRun{workflowRun(5, "main", shaA, "in_progress", "")},
		[]githubapi.WorkflowRun{workflowRun(5, "main", shaA, "completed", "success")})
	stdout, _ := bench.expect(exitFine, "run-wait", "ci.yml", "--ref", "aaaaaaa", "--interval", "1", "--appear", "5")
	contains(t, stdout, "ci.yml run 5 on main@aaaaaaa in owner/checkout")
	contains(t, stdout, "ci.yml completed: success")
}

func TestStillPendingAtTheDeadlineIsExit3(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onRuns(bench.hub, "ci.yml", []githubapi.WorkflowRun{workflowRun(5, "main", shaA, "in_progress", "")})
	stdout, _ := bench.expect(exitPending, "run-wait", "ci.yml", "--timeout", "0")
	contains(t, stdout, "ci.yml still in_progress after 0s")
}

func TestSeveralRunsForOneRefAreAllReportedAndTheNewestIsWatched(t *testing.T) {
	rows := []githubapi.WorkflowRun{workflowRun(3, "main", "82635eeaaaaaa", "completed", "failure"),
		workflowRun(2, "main", "82635eeaaaaaa", "completed", "success"),
		workflowRun(1, "other", "1111111aaaaaa", "completed", "success")}
	git := newGit()
	bench := newHarness(t, git)
	onRuns(bench.hub, "release.yml", rows)
	stdout, _ := bench.expect(exitNotFine, "run-wait", "release.yml", "--ref", "82635ee", "--repo", "https://github.com/octo-org/api-service")
	contains(t, stdout, "2 runs of release.yml match 82635ee; watching the newest, the others: 2 completed/success")
	if bench.connected[0] != "octo-org/api-service" || len(git.calls) != 0 {
		t.Fatalf("connected for %v; git asked %v", bench.connected, git.calls)
	}
}

func TestMatchingRunsByBranchOrALongEnoughSHAPrefix(t *testing.T) {
	rows := []githubapi.WorkflowRun{workflowRun(2, "main", "bb22476abcdef", "completed", "success"),
		workflowRun(1, "feat/x", "0123456789ab", "in_progress", "")}
	for ref, want := range map[string]string{"": "2,1", "feat/x": "1", "bb22476": "2", "bb224": "", "nothing": ""} {
		var got []string
		for _, run := range matchingRuns(rows, ref) {
			got = append(got, string(rune('0'+run.ID)))
		}
		if strings.Join(got, ",") != want {
			t.Fatalf("%q: %v, want %s", ref, got, want)
		}
	}
}

func TestARepositoryURLIsParsedOrRefused(t *testing.T) {
	for url, want := range map[string]string{"https://github.com/octo-org/api-service": "octo-org/api-service",
		"https://github.com/octo-org/api-service.git/": "octo-org/api-service"} {
		if got, err := repositoryFromURL(url); err != nil || got != want {
			t.Fatalf("%s: %s %v", url, got, err)
		}
	}
	for _, bad := range []string{"github.com/octo-org/api-service", "https://www.github.com/a/b", "https://github.com/a/b/pull/3"} {
		bench := newHarness(t, newGit())
		bench.expect(exitRequest, "run-wait", "ci.yml", "--repo", bad)
		if len(bench.connected) != 0 {
			t.Fatalf("%s: GitHub was asked", bad)
		}
	}
}

func TestARunPushedOutOfTheNewestIsAskedForByItsID(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	var others []githubapi.WorkflowRun
	for index := range 20 {
		others = append(others, workflowRun(int64(100+index), "main", shaC, "completed", "success"))
	}
	onFilteredRuns(bench.hub, githubapi.RunFilter{Workflow: "ci.yml", Branch: "feat"},
		[]githubapi.WorkflowRun{workflowRun(5, "feat", shaA, "queued", "")}, others)
	bench.hub.runsByID[5] = workflowRun(5, "feat", shaA, "completed", "failure")
	stdout, _ := bench.expect(exitNotFine, "run-wait", "ci.yml", "--ref", "feat", "--interval", "1", "--timeout", "5")
	contains(t, stdout, "completed: failure")
}

func TestAnAnswerAboutAnotherRunIsAChangedFormat(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onFilteredRuns(bench.hub, githubapi.RunFilter{Workflow: "ci.yml", Branch: "feat"},
		[]githubapi.WorkflowRun{workflowRun(5, "feat", shaA, "queued", "")}, nil)
	bench.hub.runsByID[5] = workflowRun(6, "feat", shaA, "completed", "success")
	bench.expect(exitFormat, "run-wait", "ci.yml", "--ref", "feat", "--interval", "1", "--timeout", "5")
}

func TestRunWaitNeedsAWorkflow(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	bench.expect(exitRequest, "run-wait")
	bench.expect(exitRequest, "run-wait", "a.yml", "b.yml")
	bench.expect(exitRequest, "run-wait", "a.yml", "--interval", "0")
	bench.expect(exitRequest, "run-wait", "a.yml", "--timeout", "-1")
}

// --- jst land find --------------------------------------------------------------------------------

func TestAnEmptyListIsExit1AndNeverNull(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	bench.hub.OnPullRequestsByHead("owner:bot/update", "open", answer([]githubapi.PullRequest{}))
	stdout, stderr := bench.expect(exitNotFine, "land-find", "--head", "bot/update")
	lacks(t, stdout+stderr, "null")
	contains(t, stdout, "no open pull request in owner/checkout for head bot/update")
}

func TestFoundAfterWaitingPrintsNumberHeadURL(t *testing.T) {
	bench := newHarness(t, newGit())
	found := pull(9, "bot/update", shaA)
	found.URL = "https://github.com/o/r/pull/9"
	bench.hub.OnSearchPullRequestsBySHA("bump", answer([]githubapi.PullRequest{}), answer([]githubapi.PullRequest{found}))
	stdout, _ := bench.expect(exitFine, "land-find", "--search", "bump", "--repo", "https://github.com/o/r", "--wait", "60")
	if strings.TrimSpace(stdout) != "9 bot/update https://github.com/o/r/pull/9" {
		t.Fatalf("printed %q", stdout)
	}
	if bench.clock.slept() == 0 {
		t.Fatal("it waited for the pull request to appear")
	}
}

func TestLandFindStates(t *testing.T) {
	open, closed, landed := pull(5, "x", shaA), pull(6, "x", shaB), *merged(7, "x", shaC)
	closed.State = "closed"
	for state, want := range map[string]string{"open": "5", "closed": "7", "merged": "7", "all": "7"} {
		bench := newHarness(t, newGit())
		bench.hub.OnSearchPullRequestsBySHA("x", answer([]githubapi.PullRequest{open, closed, landed}))
		stdout, _ := bench.expect(exitFine, "land-find", "--search", "x", "--state", state, "--repo", "https://github.com/o/r")
		if !strings.HasPrefix(stdout, want+" ") {
			t.Fatalf("%s: %q, want #%s", state, stdout, want)
		}
	}
	// By head, merged is asked as closed (REST has no merged state) and filtered by merged_at.
	bench := newHarness(t, newGit())
	bench.hub.OnPullRequestsByHead("o:x", "closed", answer([]githubapi.PullRequest{closed}))
	bench.expect(exitNotFine, "land-find", "--head", "x", "--state", "merged", "--repo", "https://github.com/o/r")
}

func TestLandFindBadRequests(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	bench.expect(exitRequest, "land-find")
	bench.expect(exitRequest, "land-find", "--head", "x", "--state", "draft")
	bench.expect(exitRequest, "land-find", "--head", "x", "extra")
	if len(bench.connected) != 0 {
		t.Fatal("GitHub was asked")
	}
}

// --- the command line -------------------------------------------------------------------------

func TestTheCommandLine(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	bench.expect(exitRequest)
	bench.expect(exitRequest, "nope")
	bench.expect(exitRequest, "wait", "--nope")
	bench.expect(exitRequest, "wait", "1", "2")
	bench.expect(exitRequest, "wait", "7", "--lines", "0")
	bench.expect(exitFine, "--help")
	bench.expect(exitFine, "wait", "--help")
	bench.expect(exitFine, "run-wait", "-h")
	bench.expect(exitFine, "land-status", "--help")
	bench.expect(exitFine, "land-find", "-h")
	// The commands jst gh had are not jst checks commands.
	for _, removed := range []string{"ci-wait", "ci-status", "pr-land", "pr-status", "pr-find"} {
		_, stderr := bench.expect(exitRequest, removed)
		contains(t, stderr, "jst checks: no command")
	}
}
