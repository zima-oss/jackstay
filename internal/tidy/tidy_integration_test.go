//go:build integration

package tidy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/githubapi/githubfake"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

// fixture is a repository: a bare origin, a clone on main with a.txt and a
// .gitignore of .build/, .env and .omc/, origin/HEAD set.
type fixture struct {
	t      *testing.T
	repo   *testrepo.Repo
	base   string
	root   string
	origin string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	repo := testrepo.New(t, map[string]string{"a.txt": "one\n", ".gitignore": ".build/\n.env\n.omc/\n"})
	base := filepath.Dir(repo.Root)
	origin := filepath.Join(base, "origin.git")
	repo.Git("init", "-q", "--bare", "-b", "main", origin)
	repo.Git("remote", "add", "origin", origin)
	repo.Git("push", "-q", "-u", "origin", "main")
	repo.Git("remote", "set-head", "origin", "main")
	return &fixture{t: t, repo: repo, base: base, root: repo.Root, origin: origin}
}

func write(t *testing.T, place, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(place), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(place, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// branch makes a branch from start with one commit per file, and returns its tip.
func (fix *fixture) branch(name string, files map[string]string, start string) string {
	fix.t.Helper()
	fix.repo.Git("branch", name, start)
	place := filepath.Join(fix.base, "tmp-"+strings.ReplaceAll(name, "/", "-"))
	fix.repo.Git("worktree", "add", "-q", place, name)
	names := make([]string, 0, len(files))
	for file := range files {
		names = append(names, file)
	}
	slices.Sort(names)
	for _, file := range names {
		write(fix.t, filepath.Join(place, file), files[file])
		fix.repo.GitIn(place, "add", file)
		fix.repo.GitIn(place, "commit", "-qm", name+": "+file)
	}
	fix.repo.Git("worktree", "remove", place)
	return fix.repo.Git("rev-parse", name)
}

// commitOn adds a commit to a branch through a worktree that is removed afterwards.
func (fix *fixture) commitOn(name, folder string, files map[string]string) string {
	fix.t.Helper()
	place := filepath.Join(fix.base, folder)
	fix.repo.Git("worktree", "add", "-q", place, name)
	for file, text := range files {
		write(fix.t, filepath.Join(place, file), text)
	}
	fix.repo.GitIn(place, "add", "-A")
	fix.repo.GitIn(place, "commit", "-qm", "more on "+name)
	fix.repo.Git("worktree", "remove", place)
	return fix.repo.Git("rev-parse", name)
}

func (fix *fixture) squashOntoMain(files map[string]string) {
	fix.t.Helper()
	fix.repo.Commit("squash", files)
	fix.repo.Git("push", "-q", "origin", "main")
}

// add is a finished branch (or one with files) checked out in a worktree of its own.
func (fix *fixture) add(name string, files map[string]string) string {
	fix.t.Helper()
	fix.branch(name, files, "main")
	place := filepath.Join(fix.base, "wt-"+name)
	fix.repo.Git("worktree", "add", "-q", place, name)
	return place
}

func nobody(context.Context, string) (seen, error) { return seen{complete: true}, nil }

func (fix *fixture) tidier(github *hub, view processView) *tidier {
	workDir, err := os.Getwd()
	if err != nil {
		fix.t.Fatal(err)
	}
	return &tidier{
		connect: func(context.Context, string, config.Config) (*hub, string, error) {
			if github == nil {
				return nil, "", errors.New("no GitHub in this test")
			}
			return github, "", nil
		},
		processes: view, workDir: realPath(workDir), pid: os.Getpid(), noGitHub: github == nil,
	}
}

func (fix *fixture) survey(tidy *tidier, wanted scope) *report {
	fix.t.Helper()
	if wanted.start == "" {
		wanted.start = fix.root
	}
	found, err := tidy.survey(context.Background(), wanted)
	if err != nil {
		fix.t.Fatal(err)
	}
	return found
}

// plain is the plainest survey: fetched, no GitHub, nobody working.
func (fix *fixture) plain(wanted scope) *report {
	fix.t.Helper()
	wanted.fetch = true
	return fix.survey(fix.tidier(nil, nobody), wanted)
}

func (fix *fixture) fix(found *report) (int, []action) {
	var done []action
	failures := fix.tidier(nil, nobody).fix(context.Background(), found, func(one action) { done = append(done, one) })
	return failures, done
}

func merged(number int, head, name string) githubapi.PullRequest {
	return githubapi.PullRequest{Number: number, State: "closed", Merged: true, HeadSHA: head, Head: name}
}

func opened(number int) githubapi.PullRequest {
	return githubapi.PullRequest{Number: number, State: "open", HeadSHA: "x"}
}

// hub answers as a fake GitHub: each local branch's pull requests from byHead,
// each searched tip from byTip (searchErr when set), none for the rest.
func (fix *fixture) hub(byHead, byTip map[string][]githubapi.PullRequest, searchErr error) *hub {
	fix.t.Helper()
	fake := githubfake.New()
	tips := map[string]bool{}
	for _, line := range strings.Split(fix.repo.Git("for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads"), "\n") {
		name, tip, _ := strings.Cut(line, " ")
		fake.OnPullRequestsByHead("o:"+name, "all", githubfake.Ok(byHead[name]))
		tips[tip] = true
	}
	for tip := range byTip {
		tips[tip] = true
	}
	for tip := range tips {
		if searchErr != nil {
			fake.OnSearchPullRequestsBySHA(tip, githubfake.Fail[[]githubapi.PullRequest](searchErr))
		} else {
			fake.OnSearchPullRequestsBySHA(tip, githubfake.Ok(byTip[tip]))
		}
	}
	return &hub{client: fake, owner: "o"}
}

func branchVerdicts(found *report) map[string]branchItem {
	out := map[string]branchItem{}
	for _, item := range found.Branches {
		out[item.Name] = item
	}
	return out
}

func treeVerdicts(found *report) map[string]treeItem {
	out := map[string]treeItem{}
	for _, item := range found.Worktrees {
		out[filepath.Base(item.Path)] = item
	}
	return out
}

func keepIDs(reasons []reason) string {
	var ids []string
	for _, one := range reasons {
		ids = append(ids, one.ID+": "+one.Detail)
	}
	return strings.Join(ids, " | ")
}

func heads(fix *fixture) []string {
	return strings.Fields(fix.repo.Git("for-each-ref", "--format=%(refname:short)", "refs/heads"))
}

func TestMergedAndSquashMergedAreFinishedOwnWorkIsKept(t *testing.T) {
	fix := newFixture(t)
	fix.branch("merged", nil, "main")
	fix.branch("own", map[string]string{"b.txt": "mine\n"}, "main")
	fix.branch("squashed", map[string]string{"c.txt": "c\n", "a.txt": "two\n"}, "main")
	fix.squashOntoMain(map[string]string{"c.txt": "c\n", "a.txt": "two\n"})
	got := branchVerdicts(fix.plain(scope{}))
	if !got["merged"].Remove || got["merged"].Why.ID != whyMerged {
		t.Errorf("merged: %+v", got["merged"])
	}
	if !got["squashed"].Remove || got["squashed"].Why.ID != whyAddsNothing {
		t.Errorf("squashed: %+v", got["squashed"])
	}
	if got["own"].Remove || got["own"].Why.ID != whyOwnWork {
		t.Errorf("own: %+v", got["own"])
	}
	if _, listed := got["main"]; listed {
		t.Error("the default branch is listed")
	}
}

func TestMergedIntoTheLocalMainBeforeThePushIsFinished(t *testing.T) {
	fix := newFixture(t)
	fix.branch("landed", map[string]string{"l.txt": "l\n"}, "main")
	fix.branch("squashed-here", map[string]string{"m.txt": "m\n"}, "main")
	fix.branch("pending", map[string]string{"n.txt": "n\n"}, "main")
	fix.repo.Git("merge", "-q", "--ff-only", "landed")
	fix.repo.Commit("squash here, not pushed", map[string]string{"m.txt": "m\n"})
	got := branchVerdicts(fix.survey(fix.tidier(nil, nobody), scope{}))
	for _, name := range []string{"landed", "squashed-here"} {
		if !got[name].Remove || !strings.Contains(got[name].Why.Detail, "refs/heads/main") {
			t.Errorf("%s: %+v", name, got[name])
		}
	}
	if got["pending"].Remove {
		t.Errorf("pending: %+v", got["pending"])
	}
}

func TestWorkTakenBackIsNotFinished(t *testing.T) {
	fix := newFixture(t)
	fix.branch("taken-back", map[string]string{"a.txt": "two\n"}, "main")
	fix.commitOn("taken-back", "tb", map[string]string{"a.txt": "one\n"})
	fix.squashOntoMain(map[string]string{"d.txt": "d\n"})
	if got := branchVerdicts(fix.plain(scope{}))["taken-back"]; got.Remove {
		t.Errorf("work done and taken back is finished: %+v", got)
	}
}

func TestGitHubMergedPullRequestWithThisTip(t *testing.T) {
	fix := newFixture(t)
	tip := fix.branch("feature", map[string]string{"e.txt": "e\n"}, "main")
	fix.squashOntoMain(map[string]string{"e.txt": "e\n"})
	fix.squashOntoMain(map[string]string{"e.txt": "rewritten later on main\n"})
	github := fix.hub(map[string][]githubapi.PullRequest{"feature": {merged(7, tip, "")}}, nil, nil)
	got := branchVerdicts(fix.survey(fix.tidier(github, nobody), scope{}))["feature"]
	if !got.Remove || got.Why.ID != whyPullMerged || !strings.Contains(got.Why.Detail, "#7") {
		t.Errorf("feature: %+v", got)
	}
}

func TestWhatPrLandLeavesIsRemovedByFix(t *testing.T) {
	// W-019: pr-land merges on GitHub, deletes only the remote branch and leaves the local one.
	fix := newFixture(t)
	tip := fix.branch("landed", map[string]string{"e.txt": "e\n"}, "main")
	fix.repo.Git("push", "-q", "-u", "origin", "landed")
	fix.squashOntoMain(map[string]string{"e.txt": "e\n"})
	fix.squashOntoMain(map[string]string{"e.txt": "changed on main afterwards\n"})
	fix.repo.Git("push", "-q", "origin", ":refs/heads/landed")
	tidy := fix.tidier(fix.hub(map[string][]githubapi.PullRequest{"landed": {merged(7, tip, "")}}, nil, nil), nobody)
	found := fix.survey(tidy, scope{})
	var done []action
	if failures := tidy.fix(context.Background(), found, func(one action) { done = append(done, one) }); failures != 0 {
		t.Fatalf("failures %d: %+v", failures, done)
	}
	if slices.Contains(heads(fix), "landed") {
		t.Errorf("landed is still there: %+v", done)
	}
}

func TestGitHubCommitsAfterTheMergeOrAnOpenPullRequestKeepIt(t *testing.T) {
	fix := newFixture(t)
	old := fix.branch("later", map[string]string{"g.txt": "g\n"}, "main")
	fix.commitOn("later", "lt", map[string]string{"h.txt": "after the merge\n"})
	fix.branch("open", map[string]string{"i.txt": "i\n"}, "main")
	github := fix.hub(map[string][]githubapi.PullRequest{"later": {merged(8, old, "")}, "open": {opened(9)}}, nil, nil)
	got := branchVerdicts(fix.survey(fix.tidier(github, nobody), scope{}))
	if got["later"].Remove || got["later"].Why.ID != whyAfterMerged || !strings.Contains(got["later"].Why.Detail, "commits that are not in it") {
		t.Errorf("later: %+v", got["later"])
	}
	if got["open"].Remove || got["open"].Why.ID != whyPullOpen || !strings.Contains(got["open"].Why.Detail, "#9 is open") {
		t.Errorf("open: %+v", got["open"])
	}
}

func TestABranchOfAnotherNameHoldingAMergedTipIsFinished(t *testing.T) {
	fix := newFixture(t)
	tip := fix.branch("feature", map[string]string{"e.txt": "e\n"}, "main")
	fix.repo.Git("branch", "pr-7", tip)
	fix.squashOntoMain(map[string]string{"e.txt": "e\n"})
	fix.squashOntoMain(map[string]string{"e.txt": "rewritten later on main\n"})
	pulls := []githubapi.PullRequest{merged(7, tip, "feature")}
	github := fix.hub(map[string][]githubapi.PullRequest{"feature": pulls}, map[string][]githubapi.PullRequest{tip: pulls}, nil)
	got := branchVerdicts(fix.survey(fix.tidier(github, nobody), scope{}))["pr-7"]
	if !got.Remove || !strings.Contains(got.Why.Detail, "#7 (from feature) was merged with this very tip") {
		t.Errorf("pr-7: %+v", got)
	}
}

func TestATipHeldByAnOpenPullRequestKeepsIt(t *testing.T) {
	fix := newFixture(t)
	tip := fix.branch("copy", map[string]string{"o.txt": "o\n"}, "main")
	held := []githubapi.PullRequest{merged(7, tip, ""), {Number: 8, State: "open", HeadSHA: "x"}}
	github := fix.hub(nil, map[string][]githubapi.PullRequest{tip: held}, nil)
	got := branchVerdicts(fix.survey(fix.tidier(github, nobody), scope{}))["copy"]
	if got.Remove || got.Why.ID != whySearchOpen || !strings.Contains(got.Why.Detail, "#8 holds this tip and is open") {
		t.Errorf("copy: %+v", got)
	}
}

func TestATipMergedUnderAnotherHeadWithMoreHereIsKept(t *testing.T) {
	fix := newFixture(t)
	old := fix.branch("grown", map[string]string{"p.txt": "p\n"}, "main")
	fix.commitOn("grown", "gr", map[string]string{"q.txt": "after\n"})
	github := fix.hub(nil, map[string][]githubapi.PullRequest{old: {merged(7, old, "")}}, nil)
	if got := branchVerdicts(fix.survey(fix.tidier(github, nobody), scope{}))["grown"]; got.Remove {
		t.Errorf("grown: %+v", got)
	}
}

func TestAFailedSearchByTipKeepsItAndIsSaid(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		note string
	}{
		{"failed", errors.New("GitHub answered 502"), noteSearchFailed},
		{"rate limited", &githubapi.RateLimitedError{Until: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}, noteSearchRefused},
	} {
		t.Run(test.name, func(t *testing.T) {
			fix := newFixture(t)
			fix.branch("lonely", map[string]string{"r.txt": "r\n"}, "main")
			found := fix.survey(fix.tidier(fix.hub(nil, nil, test.err), nobody), scope{})
			if got := branchVerdicts(found)["lonely"]; got.Remove || got.Why.ID != whySearchUnknown {
				t.Errorf("lonely: %+v", got)
			}
			if !slices.ContainsFunc(found.Notes, func(said note) bool {
				return said.ID == test.note && strings.Contains(said.Detail, "lonely")
			}) {
				t.Errorf("notes: %+v", found.Notes)
			}
		})
	}
}

func TestGitHubFailingIsNotFinishedAndIsSaid(t *testing.T) {
	// Also "rows of another shape": githubapi decodes them, and an answer it
	// cannot decode is an error like any other.
	fix := newFixture(t)
	fix.branch("merged", nil, "main")
	fix.branch("feature", map[string]string{"e.txt": "e\n"}, "main")
	fake := githubfake.New()
	fake.OnPullRequestsByHead("o:merged", "all", githubfake.Fail[[]githubapi.PullRequest](&githubapi.GraphQLError{Messages: []string{"odd"}}))
	fake.OnPullRequestsByHead("o:feature", "all", githubfake.Fail[[]githubapi.PullRequest](errors.New("502")))
	found := fix.survey(fix.tidier(&hub{client: fake, owner: "o"}, nobody), scope{})
	got := branchVerdicts(found)
	for _, name := range []string{"merged", "feature"} {
		if got[name].Remove || got[name].Why.ID != whyGitHubUnknown {
			t.Errorf("%s: %+v", name, got[name])
		}
		if !slices.ContainsFunc(found.Notes, func(said note) bool { return said.ID == noteGitHubFailed && strings.Contains(said.Detail, name) }) {
			t.Errorf("no note for %s: %+v", name, found.Notes)
		}
	}
}

func TestAnOpenPullRequestKeepsEvenAMergedBranch(t *testing.T) {
	fix := newFixture(t)
	fix.branch("merged-but-open", nil, "main")
	github := fix.hub(map[string][]githubapi.PullRequest{"merged-but-open": {{Number: 3, State: "open", HeadSHA: "x"}}}, nil, nil)
	if got := branchVerdicts(fix.survey(fix.tidier(github, nobody), scope{}))["merged-but-open"]; got.Remove || got.Why.ID != whyPullOpen {
		t.Errorf("merged-but-open: %+v", got)
	}
}

func TestGitHubUnavailableIsSaidAndKeepsWhatOnlyItCouldShow(t *testing.T) {
	fix := newFixture(t)
	tip := fix.branch("feature", map[string]string{"e.txt": "e\n"}, "main")
	fix.squashOntoMain(map[string]string{"e.txt": "e\n"})
	fix.squashOntoMain(map[string]string{"e.txt": "rewritten later on main\n"})
	tidy := fix.tidier(nil, nobody)
	tidy.noGitHub = false
	tidy.connect = func(context.Context, string, config.Config) (*hub, string, error) {
		return nil, "", &githubapi.AccountMissingError{}
	}
	found := fix.survey(tidy, scope{})
	if got := branchVerdicts(found)["feature"]; got.Remove {
		t.Errorf("feature (%s) finished without GitHub: %+v", tip, got)
	}
	if !slices.ContainsFunc(found.Notes, func(said note) bool {
		return said.ID == noteGitHubMissing && strings.Contains(said.Detail, "github.account")
	}) {
		t.Errorf("notes: %+v", found.Notes)
	}
}

func TestADefaultBranchWithASlashIsTheDefaultBranch(t *testing.T) {
	fix := newFixture(t)
	fix.repo.Git("checkout", "-q", "-b", "release/stable")
	fix.repo.Git("push", "-q", "-u", "origin", "release/stable")
	fix.repo.Git("remote", "set-head", "origin", "release/stable")
	fix.repo.Git("checkout", "-q", "main")
	found := fix.survey(fix.tidier(nil, nobody), scope{})
	if found.Default != "refs/remotes/origin/release/stable" {
		t.Errorf("default %q", found.Default)
	}
	if _, listed := branchVerdicts(found)["release/stable"]; listed {
		t.Error("the default branch is listed")
	}
}

func TestABranchThatMovedAfterTheSurveyIsKept(t *testing.T) {
	// The re-judgement before removal: a commit made between the report and --fix calls it off.
	fix := newFixture(t)
	fix.branch("merged", nil, "main")
	found := fix.plain(scope{})
	if !branchVerdicts(found)["merged"].Remove {
		t.Fatalf("merged: %+v", branchVerdicts(found)["merged"])
	}
	fix.commitOn("merged", "late", map[string]string{"late.txt": "committed after the survey\n"})
	failures, done := fix.fix(found)
	if failures != 1 || exitFor(failures) != 1 {
		t.Errorf("failures %d: %+v", failures, done)
	}
	if !slices.Contains(heads(fix), "merged") {
		t.Error("merged was deleted")
	}
	if !slices.ContainsFunc(done, func(one action) bool { return one.ID == didKeepBranch && one.Target == "merged" }) {
		t.Errorf("done: %+v", done)
	}
}

func TestABranchCheckedOutInANewWorktreeAfterTheSurveyIsKept(t *testing.T) {
	fix := newFixture(t)
	fix.branch("merged", nil, "main")
	found := fix.plain(scope{})
	place := filepath.Join(fix.base, "new-home")
	fix.repo.Git("worktree", "add", "-q", place, "merged")
	failures, done := fix.fix(found)
	if failures != 1 {
		t.Errorf("failures %d: %+v", failures, done)
	}
	if !slices.Contains(heads(fix), "merged") {
		t.Error("merged was deleted")
	}
	if _, err := os.Stat(place); err != nil {
		t.Error(err)
	}
	if !slices.ContainsFunc(done, func(one action) bool { return strings.Contains(one.Detail, "checked out in") }) {
		t.Errorf("done: %+v", done)
	}
}

func TestFixDeletesOnlyWhatIsFinished(t *testing.T) {
	fix := newFixture(t)
	fix.branch("merged", nil, "main")
	fix.branch("own", map[string]string{"b.txt": "mine\n"}, "main")
	failures, done := fix.fix(fix.plain(scope{}))
	if failures != 0 {
		t.Errorf("failures %d: %+v", failures, done)
	}
	if got := heads(fix); !slices.Equal(got, []string{"main", "own"}) {
		t.Errorf("heads %v", got)
	}
	if !slices.ContainsFunc(done, func(one action) bool { return one.ID == didDeleteBranch && one.Target == "merged" }) {
		t.Errorf("done: %+v", done)
	}
}

func TestAFailedFetchIsSaidAndJudgedOnTheRefsAsTheyWere(t *testing.T) {
	fix := newFixture(t)
	fix.branch("merged", nil, "main")
	fix.repo.Git("remote", "set-url", "origin", filepath.Join(fix.base, "gone.git"))
	found := fix.plain(scope{})
	if !slices.ContainsFunc(found.Notes, func(said note) bool { return said.ID == noteFetchFailed }) {
		t.Errorf("notes: %+v", found.Notes)
	}
	if !branchVerdicts(found)["merged"].Remove {
		t.Errorf("merged: %+v", branchVerdicts(found)["merged"])
	}
}

func TestAFinishedCleanWorktreeGoesWithItsBranch(t *testing.T) {
	fix := newFixture(t)
	place := fix.add("done", nil)
	for _, folder := range []string{".build/x", ".omc/state"} {
		if err := os.MkdirAll(filepath.Join(place, folder), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	found := fix.plain(scope{})
	if got := treeVerdicts(found)["wt-done"]; !got.Remove {
		t.Fatalf("wt-done: %s", keepIDs(got.Keep))
	}
	if failures, done := fix.fix(found); failures != 0 {
		t.Fatalf("failures %d: %+v", failures, done)
	}
	if _, err := os.Stat(place); !os.IsNotExist(err) {
		t.Errorf("the worktree is still there: %v", err)
	}
	if slices.Contains(heads(fix), "done") {
		t.Error("the branch is still there")
	}
}

func TestWhatWouldBeLostKeepsTheWorktreeAndItsBranch(t *testing.T) {
	fix := newFixture(t)
	dirty := fix.add("dirty", nil)
	write(t, filepath.Join(dirty, "new.txt"), "untracked\n")
	secret := fix.add("secret", nil)
	write(t, filepath.Join(secret, ".env"), "TOKEN=local\n")
	changed := fix.add("changed", nil)
	write(t, filepath.Join(changed, "a.txt"), "edited\n")
	locked := fix.add("locked", nil)
	fix.repo.Git("worktree", "lock", locked)
	own := fix.add("own", map[string]string{"b.txt": "b\n"})
	found := fix.plain(scope{})
	got := treeVerdicts(found)
	for name, want := range map[string]string{"wt-dirty": whyUncommitted, "wt-secret": whyIgnoredKept, "wt-changed": whyUncommitted,
		"wt-locked": whyLocked, "wt-own": whyOwnWork} {
		if !slices.ContainsFunc(got[name].Keep, func(one reason) bool { return one.ID == want }) || got[name].Remove {
			t.Errorf("%s: want %s, got %s", name, want, keepIDs(got[name].Keep))
		}
	}
	if !strings.Contains(keepIDs(got["wt-secret"].Keep), ".env") || !strings.Contains(keepIDs(got["wt-dirty"].Keep), "untracked") {
		t.Errorf("details: %s / %s", keepIDs(got["wt-secret"].Keep), keepIDs(got["wt-dirty"].Keep))
	}
	for _, item := range found.Branches {
		if item.Remove {
			t.Errorf("branch %s marked for removal", item.Name)
		}
	}
	if failures, done := fix.fix(found); failures != 0 {
		t.Errorf("failures %d: %+v", failures, done)
	}
	for _, place := range []string{dirty, secret, changed, locked, own} {
		if _, err := os.Stat(place); err != nil {
			t.Error(err)
		}
	}
}

func TestAnIgnoredFileWrittenAfterTheSurveyKeepsTheWorktree(t *testing.T) {
	fix := newFixture(t)
	place := fix.add("done", nil)
	found := fix.plain(scope{})
	if !treeVerdicts(found)["wt-done"].Remove {
		t.Fatalf("wt-done: %s", keepIDs(treeVerdicts(found)["wt-done"].Keep))
	}
	write(t, filepath.Join(place, ".env"), "TOKEN=written later\n")
	failures, done := fix.fix(found)
	if failures != 1 || !slices.ContainsFunc(done, func(one action) bool { return one.ID == didKeepTree }) {
		t.Errorf("failures %d: %+v", failures, done)
	}
	if _, err := os.Stat(place); err != nil {
		t.Error(err)
	}
	if !slices.Contains(heads(fix), "done") {
		t.Error("the branch was deleted")
	}
}

func TestEditsGitWasToldNotToSeeKeepTheWorktree(t *testing.T) {
	for _, flag := range []string{"--skip-worktree", "--assume-unchanged"} {
		t.Run(flag, func(t *testing.T) {
			fix := newFixture(t)
			place := fix.add("hidden", nil)
			write(t, filepath.Join(place, "a.txt"), "a hidden local edit\n")
			fix.repo.GitIn(place, "update-index", flag, "a.txt")
			if status := fix.repo.GitIn(place, "status", "--porcelain"); status != "" {
				t.Fatalf("status shows %q", status)
			}
			got := treeVerdicts(fix.plain(scope{}))["wt-hidden"]
			if got.Remove || !strings.Contains(keepIDs(got.Keep), whyHiddenEdits) {
				t.Errorf("wt-hidden: %s", keepIDs(got.Keep))
			}
		})
	}
}

func TestAPatternForEveryNameDoesNotMakeAnEnvFileRegenerable(t *testing.T) {
	fix := newFixture(t)
	write(t, filepath.Join(fix.root, "jackstay.json"), `{"jackstay_schema": 1, "guard": {"regenerable": ["?*", ".e*", "dir/x", "cache"]}}`)
	place := fix.add("secret", nil)
	write(t, filepath.Join(place, ".env"), "TOKEN=local\n")
	got := treeVerdicts(fix.plain(scope{}))["wt-secret"]
	if got.Remove || !strings.Contains(keepIDs(got.Keep), ".env") {
		t.Errorf("wt-secret: %s", keepIDs(got.Keep))
	}
	patterns := regenerablePatterns(fix.root)
	if !slices.Contains(patterns, "cache") {
		t.Errorf("cache not applied: %v", patterns)
	}
	for _, refused := range []string{"?*", ".e*", "dir/x"} {
		if slices.Contains(patterns, refused) {
			t.Errorf("%s applied: %v", refused, patterns)
		}
	}
}

func TestTheWorktreesOwnJackstayJSONNamesRegenerableFolders(t *testing.T) {
	// The rule reached the remote's main through the branch, but this checkout's main has not been
	// brought up to date: the worktree's own jackstay.json says `.build-*` may go.
	fix := newFixture(t)
	place := fix.add("cfg", map[string]string{"jackstay.json": `{"jackstay_schema": 1, "guard": {"regenerable": [".build-*"]}}`,
		".gitignore": ".build-*/\n"})
	fix.repo.Git("push", "-q", "origin", "cfg:main")
	write(t, filepath.Join(place, "Packages", "Core", ".build-graph-check", "x"), "x\n")
	if _, err := os.Stat(filepath.Join(fix.root, "jackstay.json")); !os.IsNotExist(err) {
		t.Fatal("the main checkout has a jackstay.json")
	}
	if got := treeVerdicts(fix.plain(scope{}))["wt-cfg"]; !got.Remove {
		t.Errorf("wt-cfg: %s", keepIDs(got.Keep))
	}
}

func TestNamingAWorktreeTakesItsBranchToo(t *testing.T) {
	fix := newFixture(t)
	place := fix.add("one", nil)
	fix.add("two", nil)
	found := fix.plain(scope{worktrees: []string{place}})
	if len(found.Branches) != 1 || found.Branches[0].Name != "one" {
		t.Fatalf("branches: %+v", found.Branches)
	}
	if failures, done := fix.fix(found); failures != 0 {
		t.Fatalf("failures %d: %+v", failures, done)
	}
	if got := heads(fix); slices.Contains(got, "one") || !slices.Contains(got, "two") {
		t.Errorf("heads %v", got)
	}
}

func TestAWorktreeInUseOrAnUnreadableTableOrAPartialViewIsKept(t *testing.T) {
	fix := newFixture(t)
	place := fix.add("busy", nil)
	views := map[string]processView{
		whyInUse: func(context.Context, string) (seen, error) {
			return seen{holders: []holder{{PID: 123, Name: "claude"}}, complete: true}, nil
		},
		whyProcessUnusable: func(context.Context, string) (seen, error) { return seen{}, errors.New("sysctl refused") },
		whyProcessPartial:  func(context.Context, string) (seen, error) { return seen{unseen: []holder{{PID: 77}}}, nil },
	}
	for want, view := range views {
		found := fix.survey(fix.tidier(nil, view), scope{fetch: true})
		got := treeVerdicts(found)["wt-busy"]
		if got.Remove || !strings.Contains(keepIDs(got.Keep), want) {
			t.Errorf("%s: %s", want, keepIDs(got.Keep))
		}
		if want == whyInUse && !strings.Contains(keepIDs(got.Keep), "claude (123)") {
			t.Errorf("in use: %s", keepIDs(got.Keep))
		}
		if branch := branchVerdicts(found)["busy"]; branch.Remove || branch.Why.ID != whyCheckedOut {
			t.Errorf("%s: branch %+v", want, branch)
		}
		tidy := fix.tidier(nil, view)
		if failures := tidy.fix(context.Background(), found, func(action) {}); failures != 0 {
			t.Errorf("%s: failures %d", want, failures)
		}
	}
	if _, err := os.Stat(place); err != nil {
		t.Error(err)
	}
}

func TestAProcessWorkingInAWorktreeKeepsIt(t *testing.T) {
	fix := newFixture(t)
	place := fix.add("worked-in", nil)
	ready := filepath.Join(fix.base, "ready")
	child := exec.Command("/bin/sh", "-c", `echo ready > "$1"; exec /bin/sleep 600`, "sh", ready)
	child.Dir = place
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	waitForFile(t, ready)
	got := treeVerdicts(fix.survey(fix.tidier(nil, realProcesses), scope{}))["wt-worked-in"]
	if got.Remove || !slices.ContainsFunc(got.Holders, func(one holder) bool { return one.PID == child.Process.Pid }) ||
		!strings.Contains(keepIDs(got.Keep), whyInUse) {
		t.Errorf("wt-worked-in (child %d): %s, holders %+v", child.Process.Pid, keepIDs(got.Keep), got.Holders)
	}
}

// waitForFile waits for a child's acknowledgement; the bound only ends a broken test.
func waitForFile(t *testing.T, place string) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(place); err == nil {
			return
		}
	}
	t.Fatalf("no %s", place)
}

func TestTidyRunFromInsideAWorktreeKeepsThatOne(t *testing.T) {
	fix := newFixture(t)
	place := fix.add("mine", nil)
	t.Chdir(place)
	got := treeVerdicts(fix.survey(fix.tidier(nil, realProcesses), scope{start: place}))["wt-mine"]
	if got.Remove || !strings.Contains(keepIDs(got.Keep), whyInUse) {
		t.Errorf("wt-mine: %s", keepIDs(got.Keep))
	}
}

func TestReviewA9AgentsPlansAndHandoffsKeepTheWorktree(t *testing.T) {
	fix := newFixture(t)
	place := fix.add("planned", nil)
	if err := os.MkdirAll(filepath.Join(place, ".omc", "state"), 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(place, ".omc", "plans", "next.md"), "the plan\n")
	got := treeVerdicts(fix.plain(scope{}))["wt-planned"]
	if got.Remove || !strings.Contains(keepIDs(got.Keep), ".omc") {
		t.Errorf("wt-planned: %s", keepIDs(got.Keep))
	}
}

func TestReviewA9OnlyTheRecordsAskedAboutArePruned(t *testing.T) {
	fix := newFixture(t)
	asked, other := fix.add("asked", nil), fix.add("other", nil)
	for _, place := range []string{asked, other} {
		if err := os.RemoveAll(place); err != nil {
			t.Fatal(err)
		}
	}
	found := fix.plain(scope{worktrees: []string{asked}})
	if !slices.Equal(found.Prunable, []string{asked}) {
		t.Fatalf("prunable %v", found.Prunable)
	}
	fix.fix(found)
	listed := fix.repo.Git("worktree", "list")
	if strings.Contains(listed, asked) || !strings.Contains(listed, other) {
		t.Errorf("worktree list:\n%s", listed)
	}
}

func TestARecordWhoseFolderIsGoneIsPrunedOnlyWhenNamed(t *testing.T) {
	fix := newFixture(t)
	place := fix.add("gone", nil)
	if err := os.RemoveAll(place); err != nil {
		t.Fatal(err)
	}
	found := fix.plain(scope{})
	if !slices.Equal(found.Prunable, []string{place}) {
		t.Fatalf("prunable %v", found.Prunable)
	}
	_, done := fix.fix(found)
	if !strings.Contains(fix.repo.Git("worktree", "list"), place) {
		t.Error("a sweep pruned: the worktree may have been moved")
	}
	if !slices.ContainsFunc(done, func(one action) bool { return strings.Contains(one.Detail, "git worktree repair") }) {
		t.Errorf("done: %+v", done)
	}
	fix.fix(fix.plain(scope{worktrees: []string{place}}))
	if strings.Contains(fix.repo.Git("worktree", "list"), place) {
		t.Error("the named record was not pruned")
	}
}

func TestARemovalGitRefusesIsSaidAndTheRestGoesOn(t *testing.T) {
	fix := newFixture(t)
	place := fix.add("stuck", nil)
	fix.branch("merged", nil, "main")
	stuck := filepath.Join(place, ".build", "sealed")
	write(t, filepath.Join(stuck, "x"), "x\n")
	if err := os.Chmod(stuck, 0o500); err != nil { // #nosec G302 -- a folder git cannot empty
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o750) })
	found := fix.plain(scope{})
	if !treeVerdicts(found)["wt-stuck"].Remove {
		t.Fatalf("wt-stuck: %s", keepIDs(treeVerdicts(found)["wt-stuck"].Keep))
	}
	failures, done := fix.fix(found)
	if failures == 0 || !slices.ContainsFunc(done, func(one action) bool { return one.ID == didFailTree }) {
		t.Errorf("failures %d: %+v", failures, done)
	}
	if !slices.ContainsFunc(done, func(one action) bool { return one.ID == didDeleteBranch && one.Target == "merged" }) {
		t.Errorf("the rest did not go on: %+v", done)
	}
	if !slices.Contains(heads(fix), "stuck") {
		t.Error("the branch of a worktree not removed was deleted")
	}
}

func TestADetachedWorktreeIsJudgedByItsCommit(t *testing.T) {
	fix := newFixture(t)
	onMain := filepath.Join(fix.base, "wt-detached")
	fix.repo.Git("worktree", "add", "-q", "--detach", onMain, "main")
	loose := filepath.Join(fix.base, "wt-loose")
	fix.repo.Git("worktree", "add", "-q", "--detach", loose, "main")
	write(t, filepath.Join(loose, "z.txt"), "z\n")
	fix.repo.GitIn(loose, "add", "z.txt")
	fix.repo.GitIn(loose, "commit", "-qm", "loose")
	got := treeVerdicts(fix.plain(scope{}))
	if !got["wt-detached"].Remove || got["wt-detached"].Why.ID != whyDetachedOnMain {
		t.Errorf("wt-detached: %+v", got["wt-detached"])
	}
	if got["wt-loose"].Remove || got["wt-loose"].Why.ID != whyDetachedUnheld {
		t.Errorf("wt-loose: %+v", got["wt-loose"])
	}
}

func TestFiltersExitCodesAndJSON(t *testing.T) {
	fix := newFixture(t)
	fix.add("one", nil)
	fix.add("two", nil)
	found := fix.plain(scope{branches: []string{"one"}})
	if len(found.Worktrees) != 1 || filepath.Base(found.Worktrees[0].Path) != "wt-one" || len(found.Branches) != 1 || found.Branches[0].Name != "one" {
		t.Errorf("scoped: %+v / %+v", found.Worktrees, found.Branches)
	}
	tidy := fix.tidier(nil, nobody)
	for _, wanted := range []scope{{start: fix.root, branches: []string{"nope"}}, {start: fix.root, worktrees: []string{fix.base}}, {start: fix.base}} {
		if _, err := tidy.survey(context.Background(), wanted); err == nil {
			t.Errorf("%+v: no error", wanted)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := tidy.main(context.Background(), request{noFetch: true, noGitHub: true, branches: []string{"nope"}, dir: fix.root}, &stdout, &stderr); code != 2 {
		t.Errorf("an unknown branch: exit %d", code)
	}
	stdout.Reset()
	if code := tidy.main(context.Background(), request{noFetch: true, noGitHub: true, dir: fix.root}, &stdout, &stderr); code != 1 {
		t.Errorf("report with finished items: exit %d", code)
	}
	if !strings.Contains(stdout.String(), "wt-one") {
		t.Errorf("report:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(fix.base, "wt-one")); err != nil {
		t.Errorf("report mode removed something: %v", err)
	}
	stdout.Reset()
	if code := tidy.main(context.Background(), request{noFetch: true, json: true, dir: fix.root}, &stdout, &stderr); code != 1 {
		t.Errorf("--json report: exit %d", code)
	}
	var shape map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &shape); err != nil {
		t.Fatalf("%v:\n%s", err, stdout.String())
	}
	for _, key := range []string{"repository", "default", "scoped", "notes", "worktrees", "branches", "prunable"} {
		if _, ok := shape[key]; !ok {
			t.Errorf("--json has no %s", key)
		}
	}
	stdout.Reset()
	if code := tidy.main(context.Background(), request{fix: true, noFetch: true, json: true, dir: fix.root}, &stdout, &stderr); code != 0 {
		t.Errorf("--fix: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	var fixed report
	if err := json.Unmarshal(stdout.Bytes(), &fixed); err != nil || fixed.Failures == nil || *fixed.Failures != 0 || len(fixed.Done) != 4 {
		t.Errorf("--fix --json: %v\n%s", err, stdout.String())
	}
	stdout.Reset()
	if code := tidy.main(context.Background(), request{noFetch: true, dir: fix.root}, &stdout, &stderr); code != 0 {
		t.Errorf("after --fix: exit %d\n%s", code, stdout.String())
	}
}

// A review tree (docs/design/review.md section 3.1): a detached worktree at the reviewed commit, in
// the review store outside the checkout, locked by `jst review` with its reason. Its commit is on
// the default branch, so it would be finished; the lock keeps it, also with --fix.
func TestALockedReviewTreeOutsideTheCheckoutIsKept(t *testing.T) {
	fix := newFixture(t)
	store := filepath.Join(t.TempDir(), "home", ".jackstay", "demo", "reviews", ".trees")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(store, "R-1-r1-codex-abc123")
	fix.repo.Git("worktree", "add", "--detach", "--lock", "--reason", "jst review 4242 1763300000", tree, "main")
	found := fix.plain(scope{})
	verdicts := treeVerdicts(found)
	var mine *treeItem
	for name, verdict := range verdicts {
		if strings.Contains(name, "R-1-r1-codex") {
			mine = &verdict
		}
	}
	if mine == nil {
		t.Fatalf("the review tree is not in the report: %v", verdicts)
	}
	if mine.Remove || !slices.ContainsFunc(mine.Keep, func(one reason) bool { return one.ID == whyLocked }) {
		t.Fatalf("the review tree is not kept as locked: %s", keepIDs(mine.Keep))
	}
	if failures, done := fix.fix(found); failures != 0 {
		t.Errorf("failures %d: %+v", failures, done)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Errorf("the review tree was removed: %v", err)
	}
}
