package gh

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/githubapi/githubfake"
)

const deleteFeat = "push --quiet --force-with-lease=refs/heads/feat:" + shaA + " -- origin :refs/heads/feat"

// landing is a bench for pr-land: a checkout on feat at A, pushed; #7 open from it, green and
// mergeable; GitHub's default branch main; the merge lands.
func landing(t *testing.T, setups ...landSetup) (*harness, *scriptedGit) {
	t.Helper()
	setup := landSetup{pulls: []githubapi.PullRequest{pull(7, "feat", shaA)}, checks: []githubapi.Check{passed("test")}}
	for _, given := range setups {
		if given.pulls != nil {
			setup.pulls = given.pulls
		}
		if given.checks != nil {
			setup.checks = given.checks
		}
		setup.mergeErr = given.mergeErr
	}
	git := checkout(shaA)
	bench := newHarness(t, git)
	bench.hub.OnDefaultBranch(answer("main"))
	bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{pull(7, "feat", shaA)}))
	onPull(bench.hub, setup.pulls...)
	onChecks(bench.hub, shaA, setup.checks)
	if setup.mergeErr != nil {
		bench.hub.OnMerge(7, githubfake.Fail[githubapi.MergeResult](setup.mergeErr))
	} else {
		bench.hub.OnMerge(7, answer(githubapi.MergeResult{Merged: true, SHA: shaC}))
		bench.hub.afterMerge = merged(7, "feat", shaA)
	}
	return bench, git
}

// landSetup changes landing's GitHub: the pull request's answers in turn, the head's checks, a
// merge GitHub refuses.
type landSetup struct {
	pulls    []githubapi.PullRequest
	checks   []githubapi.Check
	mergeErr error
}

// neverChangesTheCheckout fails when git was asked anything that changes this checkout (W-019).
func neverChangesTheCheckout(t *testing.T, git *scriptedGit) {
	t.Helper()
	for _, call := range git.calls {
		word, _, _ := strings.Cut(call, " ")
		if word == "checkout" || word == "switch" || word == "pull" || word == "reset" || word == "merge" || word == "rebase" ||
			word == "stash" || strings.HasPrefix(call, "branch -d") || strings.HasPrefix(call, "branch -D") {
			t.Fatalf("pr-land changed the checkout: git %s", call)
		}
	}
}

func TestImplicitPullRequestSelectionRefusesMultipleBases(t *testing.T) {
	for _, operation := range []string{"ci-wait", "ci-status", "pr-land", "pr-status"} {
		t.Run(operation, func(t *testing.T) {
			git := checkout(shaA)
			bench := newHarness(t, git)
			bench.hub.OnDefaultBranch(answer("main"))
			main := pull(7, "feat", shaA)
			alternate := pull(8, "feat", shaA)
			alternate.Base = "release"
			bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{alternate, main}))
			_, stderr := bench.expect(exitRequest, operation)
			contains(t, stderr, "#7 into main, #8 into release")
			if len(git.callsStarting("push")) != 0 || len(bench.hub.Merges()) != 0 {
				t.Fatal("an ambiguous selection made a write")
			}
		})
	}
}

func TestExplicitPullRequestSelectionUsesItsActualBase(t *testing.T) {
	bench, git := landing(t)
	selected := pull(8, "feat", shaA)
	selected.Base = "release"
	bench.hub.OnPullRequest(8, answer(selected))
	bench.hub.OnMerge(8, answer(githubapi.MergeResult{Merged: true, SHA: shaC}))
	landed := merged(8, "feat", shaA)
	landed.Base = "release"
	bench.hub.afterMerge = landed

	stdout, _ := bench.expect(exitFine, "pr-status", "--pr", "https://github.com/owner/checkout/pull/8")
	contains(t, stdout, "branch  feat  (base release)")
	stdout, _ = bench.expect(exitFine, "pr-land", "--pr", "8", "--no-wait")
	contains(t, stdout, "merged #8 into release")
	contains(t, stdout, "The merge is on the remote's release.")
	if merges := bench.hub.Merges(); len(merges) != 1 || merges[0].Number != 8 {
		t.Fatalf("merges %+v", merges)
	}
	contains(t, stdout, "pull request #7 into main is still open")
	if bench.hub.count("PullRequestsByHead") != 1 || len(git.callsStarting("push")) != 0 {
		t.Fatal("landing did not check for open sibling pull requests before deleting the branch")
	}
}

func TestExplicitLandingRefusesUnrelatedPullRequest(t *testing.T) {
	for _, test := range []struct {
		selection string
		update    func(*githubapi.PullRequest)
	}{
		{"https://github.com/other/checkout/pull/8", nil},
		{"999999999999999999999999999999", nil},
		{"8", func(pull *githubapi.PullRequest) { pull.Head = "different" }},
		{"8", func(pull *githubapi.PullRequest) { pull.HeadRepository = "other/checkout" }},
		{"8", func(pull *githubapi.PullRequest) { pull.State = "closed" }},
	} {
		bench, git := landing(t)
		selected := pull(8, "feat", shaA)
		if test.update != nil {
			test.update(&selected)
		}
		bench.hub.OnPullRequest(8, answer(selected))
		bench.expect(exitRequest, "pr-land", "--pr", test.selection)
		if len(git.callsStarting("push")) != 0 || len(bench.hub.Merges()) != 0 {
			t.Fatal("an unrelated selection made a write")
		}
	}
}

func TestAnExemptBranchCanOpenWithoutACardLine(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	settings := `{"jackstay_schema": 2, "cards": {"dir": "docs/cards", "id": "APP-[0-9]+", "unit": "pr", "since": "2026-01-01", "exempt_branches": ["feat"]}}`
	if err := os.WriteFile(filepath.Join(bench.dir, "jackstay.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	bench.hub.OnDefaultBranch(answer("main"))
	bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{}))
	bench.hub.OnCreatePullRequest("feat", answer(pull(7, "feat", shaA)))
	bench.expect(exitFine, "pr-land", "--title", "exempt", "--no-push", "--no-wait", "--no-merge")
	if created := bench.hub.Created(); len(created) != 1 {
		t.Fatalf("created %+v", created)
	}
}

func TestTheWholeFlowMergesTheGreenHeadAndDeletesOnlyTheRemoteBranch(t *testing.T) {
	bench, git := landing(t)
	stdout, _ := bench.expect(exitFine, "pr-land", "--timeout", "30")
	contains(t, stdout, "pull request #7 already open")
	contains(t, stdout, "GREEN: 1 checks")
	contains(t, stdout, "merged #7")
	contains(t, stdout, "deleted origin/feat")
	contains(t, stdout, "still on feat")
	merges := bench.hub.Merges()
	if len(merges) != 1 || merges[0].Number != 7 || merges[0].HeadSHA != shaA || merges[0].Message != "What it does." {
		t.Fatalf("merges %+v", merges)
	}
	if !git.called(deleteFeat) {
		t.Fatalf("the remote branch was not deleted with a lease: %v", git.calls)
	}
	if len(git.callsStarting("push")) != 1 {
		t.Fatalf("pushes %v", git.callsStarting("push"))
	}
	neverChangesTheCheckout(t, git)
}

func TestLandingRefusesToPushOutsideThePRRepository(t *testing.T) {
	for _, test := range []struct {
		name, remote, pushURLs, diagnostic string
	}{
		{"another upstream", "fork", "git@github.com:other/checkout.git\n", "other/checkout"},
		{"an overridden push URL", "origin", "https://github.com/owner/elsewhere.git\n", "owner/elsewhere"},
		{"another destination after the right one", "origin", "git@github.com:Owner/Checkout.git\ngit@github.com:owner/elsewhere.git\n", "owner/elsewhere"},
		{"a URL whose repository cannot be verified", "origin", "https://someone:fake-value@example.invalid/owner/checkout.git\n", "cannot verify"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bench, git := landing(t)
			git.on("remote", ok("origin\nfork\n"))
			git.on("config --get branch.feat.remote", ok(test.remote+"\n"))
			git.on("rev-list --count "+test.remote+"/feat..feat", ok("1\n"))
			git.on("remote get-url --push --all -- "+test.remote, ok(test.pushURLs))
			_, stderr := bench.expect(exitRequest, "pr-land", "--no-wait")
			contains(t, stderr, test.diagnostic)
			lacks(t, stderr, "fake-value")
			if len(git.callsStarting("push")) != 0 || len(bench.hub.Merges()) != 0 {
				t.Fatal("push or merge ran despite the unverified destination")
			}
		})
	}
}

func TestLandingChecksThePushDestinationAgainBeforeDeleting(t *testing.T) {
	bench, git := landing(t)
	git.on("remote get-url --push --all -- origin", ok("git@github.com:Owner/Checkout.git\n"))
	bench.hub.onMerge = func() {
		git.on("remote get-url --push --all -- origin", ok("git@github.com:owner/elsewhere.git\n"))
	}
	stdout, _ := bench.expect(exitFine, "pr-land", "--no-wait")
	contains(t, stdout, "merged #7")
	contains(t, stdout, "is not deleted")
	contains(t, stdout, "owner/elsewhere")
	if len(git.callsStarting("push")) != 0 {
		t.Fatal("deleted on the remote's changed destination")
	}
}

func TestLandingKeepsABranchWhenTheMergedHeadRepositoryDoesNotMatch(t *testing.T) {
	bench, git := landing(t)
	bench.hub.afterMerge.HeadRepository = "owner/elsewhere"
	stdout, _ := bench.expect(exitFine, "pr-land", "--no-wait", "--no-push")
	contains(t, stdout, "merged #7")
	contains(t, stdout, "is not deleted")
	if len(git.callsStarting("push")) != 0 {
		t.Fatal("deleted without a matching merged head repository")
	}
}

func TestAFailedDeleteDoesNotLookForTheBranchInADifferentRepository(t *testing.T) {
	bench, git := landing(t)
	git.on(deleteFeat, exit(128))
	git.on("remote get-url -- origin", ok("https://github.com/owner/elsewhere.git\n"))
	git.on("ls-remote --heads -- origin refs/heads/feat", ok(""))
	stdout, _ := bench.expect(exitFine, "pr-land", "--no-wait", "--no-push")
	contains(t, stdout, "origin/feat is not deleted")
	if len(git.callsStarting("ls-remote")) != 0 {
		t.Fatal("queried the wrong repository for a deletion result")
	}
}

func TestThePlanIsPrintedFirstAndDryRunChangesNothing(t *testing.T) {
	git := checkout(shaA)
	git.on("rev-list --count origin/feat..feat", exit(128)) // no remote copy yet
	bench := newHarness(t, git)
	bench.hub.OnDefaultBranch(answer("main"))
	bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{}))
	stdout, _ := bench.expect(exitFine, "pr-land", "--title", "x", "--dry-run")
	for _, line := range []string{"1. push feat (new upstream)", "2. open a pull request", "3. wait for every check on the resulting head",
		"4. squash-merge at the head that was checked, then delete the remote branch", "--dry-run: stopping here"} {
		contains(t, stdout, line)
	}
	lacks(t, stdout, "5.")
	if len(git.callsStarting("push")) != 0 || len(bench.hub.Created()) != 0 || len(bench.hub.Merges()) != 0 {
		t.Fatal("a dry run only reads")
	}
}

func TestOnTheBaseBranchItRefuses(t *testing.T) {
	for _, branch := range []string{"main", "master"} {
		git := checkout(shaA)
		git.on("rev-parse --abbrev-ref HEAD", ok(branch+"\n"))
		bench := newHarness(t, git)
		_, stderr := bench.expect(exitRequest, "pr-land", "--title", "x")
		contains(t, stderr, "needs a feature branch")
		if len(bench.connected) != 0 {
			t.Fatal("GitHub was asked")
		}
	}
	// The default branch is GitHub's own, whatever its name.
	git := checkout(shaA)
	git.on("rev-parse --abbrev-ref HEAD", ok("develop\n"))
	bench := newHarness(t, git)
	bench.hub.OnDefaultBranch(answer("develop"))
	_, stderr := bench.expect(exitRequest, "pr-land", "--title", "x")
	contains(t, stderr, "on develop: this flow needs a feature branch")
}

func TestADirtyTreeIsRefusedBeforeGitHubIsAsked(t *testing.T) {
	git := checkout(shaA)
	git.on("status --porcelain", ok("?? c.txt\n"))
	bench := newHarness(t, git)
	_, stderr := bench.expect(exitRequest, "pr-land", "--title", "x")
	contains(t, stderr, "work tree is dirty")
	if len(bench.connected) != 0 {
		t.Fatal("GitHub was asked")
	}
}

func TestANewPullRequestWithoutATitleIsRefusedBeforeThePush(t *testing.T) {
	git := checkout(shaA)
	git.on("rev-list --count origin/feat..feat", exit(128))
	bench := newHarness(t, git)
	bench.hub.OnDefaultBranch(answer("main"))
	bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{}))
	_, stderr := bench.expect(exitRequest, "pr-land")
	contains(t, stderr, "needs --title")
	if len(git.callsStarting("push")) != 0 {
		t.Fatal("pushed")
	}
}

func TestNoMergeOpensThePullRequestAndLeavesTheBranch(t *testing.T) {
	git := checkout(shaA)
	git.on("rev-list --count origin/feat..feat", exit(128))
	git.on("push -u -- origin refs/heads/feat:refs/heads/feat", ok(""))
	git.on("log --reverse --format=%s%x1f%b%x1e refs/remotes/origin/main..refs/heads/feat", ok("one\x1fits body\n\x1e\n"))
	bench := newHarness(t, git)
	bench.hub.OnDefaultBranch(answer("main"))
	bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{}))
	bench.hub.OnCreatePullRequest("feat", answer(pull(7, "feat", shaA)))
	stdout, _ := bench.expect(exitFine, "pr-land", "--title", "x", "--no-wait", "--no-merge")
	contains(t, stdout, "pushed feat")
	contains(t, stdout, "opened #7")
	created := bench.hub.Created()
	if len(created) != 1 || created[0] != (githubapi.NewPullRequest{Head: "feat", Base: "main", Title: "x", Body: "its body"}) {
		t.Fatalf("created %+v", created)
	}
	if len(bench.hub.Merges()) != 0 || git.called(deleteFeat) {
		t.Fatal("merged with --no-merge")
	}
}

func TestALostAnswerToTheCreateIsSettledByLookingAgain(t *testing.T) {
	git := checkout(shaA)
	bench := newHarness(t, git)
	bench.hub.OnDefaultBranch(answer("main"))
	alternate := pull(8, "feat", shaA)
	alternate.Base = "release"
	bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{}),
		answer([]githubapi.PullRequest{alternate, pull(7, "feat", shaA)}))
	bench.hub.OnCreatePullRequest("feat", githubfake.Fail[githubapi.PullRequest](errors.New("connection reset")))
	git.on("log --reverse --format=%s%x1f%b%x1e refs/remotes/origin/main..refs/heads/feat", ok(""))
	stdout, _ := bench.expect(exitFine, "pr-land", "--title", "x", "--no-wait", "--no-merge")
	contains(t, stdout, "opened #7")
	if len(bench.hub.Created()) != 1 {
		t.Fatal("a write is never made twice")
	}
}

func TestNoCreateWithoutAPullRequestStops(t *testing.T) {
	git := checkout(shaA)
	bench := newHarness(t, git)
	bench.hub.OnDefaultBranch(answer("main"))
	fork := pull(9, "feat", shaA)
	fork.HeadRepository = "someone/checkout" // a fork's pull request of the same branch is not this one
	bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{fork}))
	stdout, _ := bench.expect(exitFine, "pr-land", "--no-create", "--no-wait")
	contains(t, stdout, "no pull request and --no-create: stopping")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged a fork's pull request")
	}
}

func TestRedChecksStopTheMerge(t *testing.T) {
	bench, git := landing(t, landSetup{checks: []githubapi.Check{failed("test", 0)}})
	stdout, _ := bench.expect(exitNotFine, "pr-land", "--timeout", "30")
	contains(t, stdout, "not merging #7: checks did not pass")
	if len(bench.hub.Merges()) != 0 || git.called(deleteFeat) {
		t.Fatal("merged on red")
	}
}

func TestPendingAtTheDeadlineStopsTheMergeWithExit3(t *testing.T) {
	bench, _ := landing(t, landSetup{checks: []githubapi.Check{pending("test")}})
	bench.expect(exitPending, "pr-land", "--timeout", "10", "--interval", "5")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged while pending")
	}
}

func TestUnpushedLocalCommitsStopTheMerge(t *testing.T) {
	bench, git := landing(t)
	git.on("rev-parse --verify --quiet refs/heads/feat", ok(shaC+"\n"))
	git.on("cat-file -e "+shaA+"^{commit}", ok(""))
	git.on("merge-base --is-ancestor "+shaC+" "+shaA, exit(1))
	_, stderr := bench.expect(exitNotFine, "pr-land", "--no-wait", "--no-push")
	contains(t, stderr, "would not land")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged")
	}
}

func TestNoWaitStillMergesOnlyTheHeadSeen(t *testing.T) {
	bench, git := landing(t)
	bench.expect(exitFine, "pr-land", "--no-wait")
	if merges := bench.hub.Merges(); len(merges) != 1 || merges[0].HeadSHA != shaA {
		t.Fatalf("merges %+v", merges)
	}
	if bench.hub.count("Checks") != 0 {
		t.Fatal("--no-wait waited")
	}
	neverChangesTheCheckout(t, git)
}

func TestAPullRequestOnlyQueuedIsNotCalledMergedAndKeepsItsBranch(t *testing.T) {
	bench, git := landing(t)
	bench.hub.afterMerge = nil // a merge queue: GitHub answers, and the pull request is still open
	stdout, _ := bench.expect(exitPending, "pr-land", "--no-wait")
	lacks(t, stdout, "merged #7")
	contains(t, stdout, "not merged yet (GitHub says open")
	if len(git.callsStarting("push")) != 0 {
		t.Fatal("the remote branch was touched")
	}
}

func TestAMergeGitHubRefusesIsExit1(t *testing.T) {
	bench, git := landing(t, landSetup{mergeErr: errors.New("409 Head branch was modified")})
	_, stderr := bench.expect(exitNotFine, "pr-land", "--no-wait")
	contains(t, stderr, "Head branch was modified")
	if len(git.callsStarting("push")) != 0 {
		t.Fatal("the remote branch was touched")
	}
}

func TestAmbiguousMergeKeepsRemoteBranchForReconciliation(t *testing.T) {
	bench, git := landing(t, landSetup{mergeErr: errors.New("merge response lost")})
	bench.hub.afterMerge = merged(7, "feat", shaA)
	stdout, stderr := bench.expect(exitNotFine, "pr-land", "--no-wait")
	lacks(t, stdout, "merged #7")
	contains(t, stderr, "did not confirm this merge request")
	if len(git.callsStarting("push")) != 0 {
		t.Fatal("an unconfirmed merge deleted the remote branch")
	}
}

func TestDifferentMergeCommitKeepsRemoteBranch(t *testing.T) {
	bench, git := landing(t)
	bench.hub.afterMerge.MergeCommitSHA = shaB
	_, stderr := bench.expect(exitNotFine, "pr-land", "--no-wait")
	contains(t, stderr, "does not match the confirmed merge result")
	if len(git.callsStarting("push")) != 0 {
		t.Fatal("a different merge deleted the remote branch")
	}
}

func TestABranchGitHubDeletedAlreadyIsSaidSo(t *testing.T) {
	bench, git := landing(t)
	git.on(deleteFeat, gitAnswer{code: 1, out: "error: unable to delete 'feat': remote ref does not exist"})
	git.on("ls-remote --heads -- origin refs/heads/feat", ok(""))
	stdout, _ := bench.expect(exitFine, "pr-land", "--no-wait")
	contains(t, stdout, "origin/feat is already deleted")
}

func TestARemoteThatCannotBeReachedAfterTheMergeIsSaid(t *testing.T) {
	bench, git := landing(t)
	git.on(deleteFeat, gitAnswer{code: 128, out: "fatal: could not read from remote repository"})
	git.on("ls-remote --heads -- origin refs/heads/feat", exit(128))
	stdout, _ := bench.expect(exitFine, "pr-land", "--no-wait", "--no-push")
	contains(t, stdout, "merged #7")
	contains(t, stdout, "the remote branch origin/feat is not deleted")
}

func TestBehindThenUpdateBranchThenMergeTheNewHead(t *testing.T) {
	bench, git := landing(t)
	bench.hub.mergeStates = []string{"behind"}
	updated := pull(7, "feat", shaB)
	bench.hub.afterUpdate = &updated
	bench.hub.afterMerge = merged(7, "feat", shaB)
	git.on("cat-file -e "+shaB+"^{commit}", exit(1), ok(""))
	git.on("fetch --quiet origin refs/pull/7/head", ok(""))
	git.on("merge-base --is-ancestor "+shaA+" "+shaB, ok(""))
	git.on("push --quiet --force-with-lease=refs/heads/feat:"+shaB+" -- origin :refs/heads/feat", ok(""))
	stdout, _ := bench.expect(exitFine, "pr-land", "--no-wait", "--no-push")
	contains(t, stdout, "BEHIND the base; updating the branch on GitHub")
	if len(bench.hub.updates) != 1 || bench.hub.updates[0] != shaA {
		t.Fatalf("updates %v", bench.hub.updates)
	}
	if merges := bench.hub.Merges(); len(merges) != 1 || merges[0].HeadSHA != shaB {
		t.Fatalf("merges %+v", merges)
	}
}

func TestBehindWithWaitWaitsForTheNewHeadsChecks(t *testing.T) {
	bench, git := landing(t)
	bench.hub.mergeStates = []string{"behind"}
	updated := pull(7, "feat", shaB)
	bench.hub.afterUpdate = &updated
	bench.hub.afterMerge = merged(7, "feat", shaB)
	onChecks(bench.hub, shaB, []githubapi.Check{passed("test")})
	git.on("cat-file -e "+shaB+"^{commit}", ok(""))
	git.on("merge-base --is-ancestor "+shaA+" "+shaB, ok(""))
	git.on("push --quiet --force-with-lease=refs/heads/feat:"+shaB+" -- origin :refs/heads/feat", ok(""))
	bench.expect(exitFine, "pr-land", "--no-push")
	if merges := bench.hub.Merges(); len(merges) != 1 || merges[0].HeadSHA != shaB {
		t.Fatalf("merges %+v: the green head after the update is the one merged", merges)
	}
}

func TestAnUpdateThatNeverShowsIsExit3(t *testing.T) {
	bench, _ := landing(t)
	bench.hub.mergeStates = []string{"behind"}
	stdout, _ := bench.expect(exitPending, "pr-land", "--no-wait", "--no-push")
	contains(t, stdout, "has not shown on GitHub")
	if len(bench.hub.Merges()) != 0 || bench.clock.slept() != mergeableWait {
		t.Fatalf("merged, or waited %v", bench.clock.slept())
	}
}

func TestItWaitsWhileGitHubComputesMergeability(t *testing.T) {
	unknown := pull(7, "feat", shaA)
	unknown.Mergeable = "UNKNOWN"
	bench, _ := landing(t, landSetup{pulls: []githubapi.PullRequest{unknown, unknown, pull(7, "feat", shaA)}})
	bench.expect(exitFine, "pr-land", "--no-wait")
	if bench.clock.slept() != 2*mergeableStep {
		t.Fatalf("waited %v", bench.clock.slept())
	}

	bench, _ = landing(t, landSetup{pulls: []githubapi.PullRequest{unknown}})
	stdout, _ := bench.expect(exitPending, "pr-land", "--no-wait")
	contains(t, stdout, "has not said within 1m00s whether it merges")

	conflicting := pull(7, "feat", shaA)
	conflicting.Mergeable = "CONFLICTING"
	bench, _ = landing(t, landSetup{pulls: []githubapi.PullRequest{conflicting}})
	_, stderr := bench.expect(exitNotFine, "pr-land", "--no-wait")
	contains(t, stderr, "#7 is CONFLICTING")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged")
	}
}

func TestGitHubRejectsAHeadThatMovesAfterTheLastRead(t *testing.T) {
	// GitHub still holds the checked head SHA if it moves after jackstay's last look.
	open := pull(7, "feat", shaA)
	bench, _ := landing(t, landSetup{
		pulls:    []githubapi.PullRequest{open, open, open, open, open, pull(7, "feat", shaB)},
		mergeErr: errors.New("409 Head branch was modified"),
	})
	bench.expect(exitNotFine, "pr-land")
	if merges := bench.hub.Merges(); len(merges) != 1 || merges[0].HeadSHA != shaA {
		t.Fatalf("merges %+v", merges)
	}
}

func TestCardFlagsAreCheckedAsARequest(t *testing.T) {
	for _, args := range [][]string{
		{"--reason", "x"},
		{"--card", "none"},
		{"--card", "none", "--card", "APP-1", "--reason", "x"},
		{"--card", "APP-1", "--reason", "x"},
		{"--no-sync"},
		{"extra"},
	} {
		bench, _ := landing(t)
		bench.expect(exitRequest, append([]string{"pr-land"}, args...)...)
		if len(bench.connected) != 0 {
			t.Fatalf("%v: GitHub was asked", args)
		}
	}
	bench, _ := landing(t) // --card for a pull request that is open already
	_, stderr := bench.expect(exitRequest, "pr-land", "--card", "APP-1")
	contains(t, stderr, "open already")
}

func TestCardLine(t *testing.T) {
	for _, each := range []struct {
		ids    []string
		reason string
		want   string
	}{
		{nil, "", ""},
		{[]string{"APP-1", "APP-2", "APP-1"}, "", "Card: APP-1, APP-2"},
		{[]string{"none"}, " docs only ", "Card: none (docs only)"},
	} {
		if got, err := cardLine(each.ids, each.reason); err != nil || got != each.want {
			t.Fatalf("%v %q: %q %v", each.ids, each.reason, got, err)
		}
	}
	if withLine("Body.\n\n", "Card: A") != "Body.\n\nCard: A\n" || withLine("", "Card: A") != "Card: A\n" || withLine("b", "") != "b" {
		t.Fatal("withLine")
	}
}

func TestPRStatus(t *testing.T) {
	git := checkout(shaA)
	git.on("rev-list --count origin/feat..feat", exit(128))
	bench := newHarness(t, git)
	bench.hub.OnDefaultBranch(answer("main"))
	bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{}))
	stdout, _ := bench.expect(exitFine, "pr-status")
	for _, line := range []string{"repo    owner/checkout", "branch  feat  (base main)", "tree    clean", "ahead   no upstream yet",
		"pr      none open for this branch"} {
		contains(t, stdout, line)
	}

	bench, _ = landing(t, landSetup{checks: []githubapi.Check{pending("test")}})
	bench.hub.mergeStates = []string{"blocked"}
	stdout, _ = bench.expect(exitPending, "pr-status")
	contains(t, stdout, "ahead   0 commit(s) unpushed")
	contains(t, stdout, "pr      #7 BLOCKED — t")
	if bench.clock.slept() != time.Duration(0) || len(bench.hub.Merges()) != 0 {
		t.Fatal("pr-status only reads, once")
	}
}

// Issue #14: pr-land waits for the checks the base's rules require; another check still pending
// (a job no runner picked up) is reported and does not hold the merge.
func TestPRLandWaitsOnlyForTheRequiredChecks(t *testing.T) {
	bench, _ := landing(t, landSetup{checks: []githubapi.Check{passed("gate"), pending("gate-macos"),
		checkRun("extra", "COMPLETED", "CANCELLED", 3)}})
	bench.hub.required = []RequiredCheck{{Name: "gate"}}
	bench.hub.OnWorkflowRuns(githubapi.RunFilter{HeadSHA: shaA}, answer([]githubapi.WorkflowRun{}))
	stdout, _ := bench.expect(exitFine, "pr-land", "--timeout", "30")
	contains(t, stdout, "wait for the checks the rules of main require on the resulting head (gate)")
	contains(t, stdout, "not required: gate-macos pending (not waited for)")
	contains(t, stdout, "not required: extra cancel (not waited for)")
	contains(t, stdout, "GREEN: 1 checks")
	if len(bench.hub.Merges()) != 1 {
		t.Fatal("not merged")
	}
}

func TestPRLandMergesOnlyWhenCancelledRequiredCheckHasAProvenReplacement(t *testing.T) {
	for _, matching := range []bool{false, true} {
		rows := []githubapi.Check{checkRun("gate", "COMPLETED", "CANCELLED", 4), checkRun("gate", "COMPLETED", "SUCCESS", 5)}
		bench, _ := landing(t, landSetup{checks: rows})
		bench.hub.required = []RequiredCheck{{Name: "gate", AppID: 15368}}
		workflowID := int64(2)
		if matching {
			workflowID = 1
		}
		bench.hub.OnWorkflowRuns(githubapi.RunFilter{HeadSHA: shaA}, answer([]githubapi.WorkflowRun{
			{ID: 5, WorkflowID: workflowID, HeadSHA: shaA, Status: "completed", Conclusion: "success"},
			{ID: 4, WorkflowID: 1, HeadSHA: shaA, Status: "completed", Conclusion: "cancelled"},
		}))
		want := exitNotFine
		if matching {
			want = exitFine
		}
		bench.expect(want, "pr-land", "--timeout", "30")
		if got := len(bench.hub.Merges()); got != map[bool]int{false: 0, true: 1}[matching] {
			t.Fatalf("matching %v: %d merges", matching, got)
		}
	}
}

func TestAFailedCheckNotRequiredStillStopsTheMerge(t *testing.T) {
	bench, _ := landing(t, landSetup{checks: []githubapi.Check{passed("gate"), failed("lint", 0)}})
	bench.hub.required = []RequiredCheck{{Name: "gate"}}
	stdout, _ := bench.expect(exitNotFine, "pr-land", "--timeout", "30")
	contains(t, stdout, "not merging #7")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged on red")
	}
}

func TestARequiredCheckNotRegisteredIsWaitedFor(t *testing.T) {
	bench, _ := landing(t, landSetup{checks: []githubapi.Check{passed("lint")}})
	bench.hub.required = []RequiredCheck{{Name: "gate"}}
	_, stderr := bench.expect(exitPending, "pr-land", "--timeout", "10", "--interval", "5")
	contains(t, stderr, "gate (not registered yet)")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged without the required check")
	}
}

func TestRulesThatCannotBeReadMeanEveryCheckIsWaitedFor(t *testing.T) {
	bench, _ := landing(t, landSetup{checks: []githubapi.Check{passed("gate"), pending("gate-macos")}})
	bench.hub.requiredErr = errors.New("403 Resource not accessible")
	stdout, _ := bench.expect(exitPending, "pr-land", "--timeout", "10", "--interval", "5")
	contains(t, stdout, "wait for every check on the resulting head (the rules of main cannot be read: 403 Resource not accessible)")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged while a check was pending")
	}
}

// A check required from an app is not satisfied by a commit status of the same name.
func TestARequiredChecksAppIsMatched(t *testing.T) {
	status := githubapi.Check{Name: "gate", Kind: githubapi.StatusContext, State: "SUCCESS"}
	bench, _ := landing(t, landSetup{checks: []githubapi.Check{status}})
	bench.hub.required = []RequiredCheck{{Name: "gate", AppID: 15368}}
	_, stderr := bench.expect(exitPending, "pr-land", "--timeout", "10", "--interval", "5")
	contains(t, stderr, "gate (app 15368) (not registered yet)")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged on a status from another source")
	}
	if judged := weigh([]githubapi.Check{passed("gate")}, bench.hub.required); len(judged.pending) != 0 || judged.judged != 1 {
		t.Fatalf("the app's own check run does not satisfy it: %+v", judged)
	}
}

// The rules read are the base's: a pull request retargeted while pr-land waits stops it.
func TestARetargetedPullRequestStopsTheWait(t *testing.T) {
	moved := pull(7, "feat", shaA)
	moved.Base = "release"
	bench, _ := landing(t, landSetup{pulls: []githubapi.PullRequest{pull(7, "feat", shaA), pull(7, "feat", shaA), moved},
		checks: []githubapi.Check{pending("gate")}})
	bench.hub.required = []RequiredCheck{{Name: "gate"}}
	_, stderr := bench.expect(exitMoved, "pr-land", "--timeout", "30", "--interval", "1")
	contains(t, stderr, "the base of #7 changed from main to release")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged into another base")
	}
}

// A pull request retargeted after the wait stops before anything is written on GitHub: no branch
// update, no merge.
func TestARetargetSeenBeforeTheBranchUpdateWritesNothing(t *testing.T) {
	moved := pull(7, "feat", shaA)
	moved.Base = "release"
	bench, _ := landing(t, landSetup{pulls: []githubapi.PullRequest{pull(7, "feat", shaA), pull(7, "feat", shaA), moved}})
	bench.hub.required = []RequiredCheck{{Name: "test"}}
	bench.hub.mergeStates = []string{"behind"}
	_, stderr := bench.expect(exitMoved, "pr-land", "--timeout", "30")
	contains(t, stderr, "the base of #7 changed from main to release")
	if len(bench.hub.updates) != 0 || len(bench.hub.Merges()) != 0 {
		t.Fatalf("wrote after the retarget: updates %v merges %v", bench.hub.updates, bench.hub.Merges())
	}
}

// GitHub judges the required checks by commit: when the test merge commit has results, all of them
// are judged there, so one only on the head is still waited for.
func TestRequiredChecksAreNotSplitAcrossTheTwoCommits(t *testing.T) {
	open := pull(7, "feat", shaA)
	open.MergeCommitSHA = shaC
	bench, _ := landing(t, landSetup{pulls: []githubapi.PullRequest{open}, checks: []githubapi.Check{passed("lint")}})
	onChecks(bench.hub, shaC, []githubapi.Check{{Name: "build", Kind: githubapi.StatusContext, State: "SUCCESS"}})
	bench.hub.required = []RequiredCheck{{Name: "build"}, {Name: "lint"}}
	stdout, stderr := bench.expect(exitPending, "pr-land", "--timeout", "10", "--interval", "5")
	contains(t, stdout, "the required checks are judged on it")
	contains(t, stderr, "lint (not registered yet)")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged on checks taken from two commits")
	}
}

// With nothing on the head, a required check on the test merge commit is found.
func TestARequiredCheckOnlyOnTheTestMergeCommitIsFound(t *testing.T) {
	open := pull(7, "feat", shaA)
	open.BaseSHA = shaB
	open.MergeCommitSHA = shaC
	bench, _ := landing(t, landSetup{pulls: []githubapi.PullRequest{open}, checks: []githubapi.Check{}})
	onChecks(bench.hub, shaC, []githubapi.Check{{Name: "external", Kind: githubapi.StatusContext, State: "SUCCESS"}})
	bench.hub.required = []RequiredCheck{{Name: "external"}}
	stdout, _ := bench.expect(exitFine, "pr-land", "--timeout", "30")
	contains(t, stdout, "GREEN: 1 checks")
	if merges := bench.hub.Merges(); len(merges) != 1 || merges[0].HeadSHA != shaA {
		t.Fatalf("merges %+v", merges)
	}
}

func TestATestMergeCommitThatMovesWhileReadingItsChecksStopsLanding(t *testing.T) {
	open := pull(7, "feat", shaA)
	open.BaseSHA, open.MergeCommitSHA = shaB, shaC
	changed := open
	changed.MergeCommitSHA = shaB
	bench, _ := landing(t, landSetup{pulls: []githubapi.PullRequest{open, open, changed}, checks: []githubapi.Check{}})
	onChecks(bench.hub, shaC, []githubapi.Check{passed("gate")})
	bench.hub.required = []RequiredCheck{{Name: "gate"}}
	stdout, stderr := bench.expect(exitMoved, "pr-land", "--timeout", "30")
	contains(t, stderr, "test merge commit of #7 changed")
	lacks(t, stdout, "GREEN")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged on checks for the old test merge commit")
	}
}

func TestAChangeAfterTheGreenVerdictStopsTheMerge(t *testing.T) {
	for _, test := range []struct {
		name, diagnostic string
		change           func(*githubapi.PullRequest)
	}{
		{"head commit", "head of #7 changed", func(pull *githubapi.PullRequest) { pull.HeadSHA = shaB }},
		{"base commit", "base commit of #7 changed", func(pull *githubapi.PullRequest) { pull.BaseSHA = shaC }},
		{"test merge commit", "test merge commit of #7 changed", func(pull *githubapi.PullRequest) { pull.MergeCommitSHA = shaB }},
		{"head repository", "identity of #7 changed", func(pull *githubapi.PullRequest) { pull.HeadRepository = "other/checkout" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			open := pull(7, "feat", shaA)
			open.BaseSHA, open.MergeCommitSHA = shaB, shaC
			changed := open
			test.change(&changed)
			// Two reads surround head checks, a third follows merge checks. GitHub is then asked
			// about mergeability, the judged body and the state immediately before the merge.
			bench, git := landing(t, landSetup{
				pulls:  []githubapi.PullRequest{open, open, open, open, open, changed},
				checks: []githubapi.Check{},
			})
			onChecks(bench.hub, shaC, []githubapi.Check{passed("gate")})
			bench.hub.required = []RequiredCheck{{Name: "gate"}}
			stdout, stderr := bench.expect(exitMoved, "pr-land", "--timeout", "30")
			contains(t, stdout, "GREEN")
			contains(t, stderr, test.diagnostic)
			if len(bench.hub.Merges()) != 0 || len(git.callsStarting("push")) != 0 {
				t.Fatal("merged or deleted a branch on stale evidence")
			}
		})
	}
}

// When the test merge commit has a result for a required check, it is the one that must pass.
func TestTheTestMergeCommitsResultTakesPrecedence(t *testing.T) {
	open := pull(7, "feat", shaA)
	open.MergeCommitSHA = shaC
	bench, _ := landing(t, landSetup{pulls: []githubapi.PullRequest{open},
		checks: []githubapi.Check{{Name: "external", Kind: githubapi.StatusContext, State: "SUCCESS"}}})
	onChecks(bench.hub, shaC, []githubapi.Check{{Name: "external", Kind: githubapi.StatusContext, State: "FAILURE"}})
	bench.hub.required = []RequiredCheck{{Name: "external"}}
	stdout, _ := bench.expect(exitNotFine, "pr-land", "--timeout", "30")
	contains(t, stdout, "RED: 1 of 1")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged on the head's green over the merge commit's red")
	}
}

func TestOptionalFailureOnTestMergeCommitStopsLanding(t *testing.T) {
	open := pull(7, "feat", shaA)
	open.MergeCommitSHA = shaC
	bench, _ := landing(t, landSetup{pulls: []githubapi.PullRequest{open}, checks: []githubapi.Check{passed("gate")}})
	onChecks(bench.hub, shaC, []githubapi.Check{passed("gate"), failed("lint", 0)})
	bench.hub.required = []RequiredCheck{{Name: "gate"}}
	stdout, _ := bench.expect(exitNotFine, "pr-land", "--timeout", "30")
	contains(t, stdout, "RED: 1 of")
	if len(bench.hub.Merges()) != 0 {
		t.Fatal("merged despite an optional failed check on the test merge commit")
	}
}

// A merge commit with results, none of them required, and nothing on the head: the timeout names
// the required checks missing, not a workflow that never ran.
func TestAnEmptyJudgedCommitNamesTheMissingRequiredChecks(t *testing.T) {
	open := pull(7, "feat", shaA)
	open.MergeCommitSHA = shaC
	bench, _ := landing(t, landSetup{pulls: []githubapi.PullRequest{open}, checks: []githubapi.Check{}})
	onChecks(bench.hub, shaC, []githubapi.Check{{Name: "unrelated", Kind: githubapi.StatusContext, State: "SUCCESS"}})
	bench.hub.required = []RequiredCheck{{Name: "gate"}}
	_, stderr := bench.expect(exitPending, "pr-land", "--timeout", "10", "--interval", "5")
	contains(t, stderr, "gate (not registered yet)")
}
