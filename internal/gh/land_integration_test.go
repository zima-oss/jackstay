//go:build integration

package gh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

const plainSettings = `{"jackstay_schema": 2, "github": {"account": "someone", "repository": "owner/checkout"}}`

const cardSettings = `{"jackstay_schema": 2, "github": {"account": "someone", "repository": "owner/checkout"},
 "cards": {"dir": "cards", "id": "APP-[0-9]+", "unit": "pr", "branch": "main", "since": "2026-01-01"}}`

// landRepo is a checkout on feat, one commit ahead of main, with a bare origin holding main; and a
// bench whose git is the real one in it.
type landRepo struct {
	*testrepo.Repo
	origin string
	bench  *harness
}

func newLandRepo(t *testing.T, settings string, files map[string]string) *landRepo {
	t.Helper()
	all := map[string]string{"jackstay.json": settings, "a.txt": "one\n"}
	for name, text := range files {
		all[name] = text
	}
	repo := testrepo.New(t, all)
	origin := filepath.Join(filepath.Dir(repo.Root), "origin.git")
	repo.Git("init", "-q", "--bare", origin)
	repo.Git("remote", "add", "origin", origin)
	repo.Git("push", "-q", "-u", "origin", "main")
	repo.Git("checkout", "-q", "-b", "feat")
	repo.Commit("b", map[string]string{"b.txt": "two\n"})
	bench := newHarness(t, gitcli.Git{Dir: repo.Root})
	bench.dir = repo.Root
	bench.hub.OnDefaultBranch(answer("main"))
	return &landRepo{Repo: repo, origin: origin, bench: bench}
}

// lands programs GitHub for #7 open from feat at tip, green, mergeable, merging.
func (land *landRepo) lands(tip, body string) {
	open := pull(7, "feat", tip)
	open.Body = body
	land.bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{open}))
	land.bench.hub.OnPullRequest(7, answer(open))
	onChecks(land.bench.hub, tip, []githubapi.Check{passed("test")})
	land.bench.hub.OnMerge(7, answer(githubapi.MergeResult{Merged: true}))
	land.bench.hub.afterMerge = merged(7, "feat", tip)
}

// state is what pr-land must not change here: HEAD, every local branch, every file of the
// working tree (ignored ones too).
func (land *landRepo) state() string {
	land.T.Helper()
	var out strings.Builder
	out.WriteString(land.Git("symbolic-ref", "HEAD") + "\n" + land.Git("for-each-ref", "refs/heads") + "\n")
	err := filepath.WalkDir(land.Root, func(place string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(place)
			if err != nil {
				return err
			}
			out.WriteString(place + "=" + string(data) + "\n")
		}
		return nil
	})
	if err != nil {
		land.T.Fatal(err)
	}
	return out.String()
}

func (land *landRepo) remoteBranch(name string) string {
	land.T.Helper()
	return land.GitIn(land.origin, "for-each-ref", "--format=%(objectname)", "refs/heads/"+name)
}

func (land *landRepo) clone(name string) string {
	land.T.Helper()
	place := filepath.Join(filepath.Dir(land.Root), name)
	land.Git("clone", "-q", "-b", "feat", land.origin, place)
	return place
}

func TestLandPushesOpensMergesDeletesTheRemoteBranchAndLeavesTheCheckout(t *testing.T) {
	land := newLandRepo(t, plainSettings, nil)
	tip := land.Git("rev-parse", "feat")
	hub := land.bench.hub
	hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{}))
	hub.OnCreatePullRequest("feat", answer(pull(7, "feat", tip)))
	hub.OnPullRequest(7, answer(pull(7, "feat", tip)))
	onChecks(hub, tip, []githubapi.Check{passed("test")})
	hub.OnMerge(7, answer(githubapi.MergeResult{Merged: true}))
	pushedTip := ""
	hub.onMerge = func() { pushedTip = land.remoteBranch("feat") }
	hub.afterMerge = merged(7, "feat", tip)
	before := land.state()
	stdout, _ := land.bench.expect(exitFine, "pr-land", "--title", "x", "--timeout", "30")
	contains(t, stdout, "1. push feat (new upstream)")
	contains(t, stdout, "pushed feat")
	contains(t, stdout, "deleted origin/feat")
	if pushedTip != tip {
		t.Fatalf("the remote had %q at the merge, want %s", pushedTip, tip)
	}
	if merges := hub.Merges(); len(merges) != 1 || merges[0].HeadSHA != tip {
		t.Fatalf("merges %+v", merges)
	}
	if created := hub.Created(); len(created) != 1 || created[0].Body != "" { // one commit, with no body
		t.Fatalf("created %+v", created)
	}
	if land.remoteBranch("feat") != "" {
		t.Fatal("the remote branch is still there")
	}
	if land.Git("config", "--get", "branch.feat.remote") != "origin" {
		t.Fatal("the new upstream was not set")
	}
	if after := land.state(); after != before {
		t.Fatalf("the checkout changed (W-019):\nbefore %s\nafter %s", before, after)
	}
}

func TestARemoteNotCalledOriginIsPushedTo(t *testing.T) {
	land := newLandRepo(t, plainSettings, nil)
	land.Git("remote", "rename", "origin", "upstream-x")
	tip := land.Git("rev-parse", "feat")
	land.bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{}))
	land.bench.hub.OnCreatePullRequest("feat", answer(pull(7, "feat", tip)))
	land.bench.expect(exitFine, "pr-land", "--title", "x", "--no-wait", "--no-merge")
	if land.remoteBranch("feat") != tip {
		t.Fatal("not pushed to the only remote")
	}
	if land.Git("config", "--get", "branch.feat.remote") != "upstream-x" {
		t.Fatal("upstream not set")
	}
}

func TestARemoteBranchPushedAfterTheMergeIsNotDeleted(t *testing.T) {
	land := newLandRepo(t, plainSettings, nil)
	land.Git("push", "-q", "-u", "origin", "feat")
	tip := land.Git("rev-parse", "feat")
	other := land.clone("later")
	land.GitIn(other, "commit", "-q", "--allow-empty", "-m", "pushed after the merge")
	later := land.GitIn(other, "rev-parse", "HEAD")
	land.lands(tip, "What it does.")
	land.bench.hub.onMerge = func() { land.GitIn(other, "push", "-q", "origin", "feat") }
	stdout, _ := land.bench.expect(exitFine, "pr-land", "--no-wait")
	contains(t, stdout, "merged #7")
	contains(t, stdout, "is not deleted")
	if land.remoteBranch("feat") != later {
		t.Fatal("a push made after the merge was deleted")
	}
}

func TestABranchGitHubDeletedAlreadyIsSaidSoForReal(t *testing.T) {
	land := newLandRepo(t, plainSettings, nil)
	land.Git("push", "-q", "-u", "origin", "feat")
	land.lands(land.Git("rev-parse", "feat"), "")
	land.bench.hub.onMerge = func() { land.GitIn(land.origin, "update-ref", "-d", "refs/heads/feat") }
	stdout, _ := land.bench.expect(exitFine, "pr-land", "--no-wait")
	contains(t, stdout, "origin/feat is already deleted")
}

func TestAFileTheBaseTracksAndThePullRequestRemovesIsNotTouched(t *testing.T) {
	// main tracks .env; the pull request stops tracking and ignores it; this checkout has its own.
	// A switch to main (what `gh pr merge --delete-branch` does before it pulls) would write over it.
	land := newLandRepo(t, plainSettings, nil)
	land.Git("checkout", "-q", "main")
	land.Write(map[string]string{".env": "THEIRS\n"})
	land.Git("add", "-f", ".env")
	land.Git("commit", "-q", "-m", "track .env")
	land.Git("push", "-q", "origin", "main")
	land.Git("checkout", "-q", "feat")
	land.Git("merge", "-q", "main", "-m", "main into feat")
	land.Git("rm", "-q", "--cached", ".env")
	land.Commit("stop tracking .env", map[string]string{".gitignore": ".env\n"})
	land.Write(map[string]string{".env": "MINE\n"})
	land.Git("push", "-q", "-u", "origin", "feat")
	land.lands(land.Git("rev-parse", "feat"), "")
	before := land.state()
	land.bench.expect(exitFine, "pr-land", "--no-wait")
	if land.state() != before {
		t.Fatal("the checkout changed")
	}
}

func TestAHeadThatMovedPastThisCheckoutIsMergedAndNothingHereChanges(t *testing.T) {
	land := newLandRepo(t, plainSettings, nil)
	land.Git("push", "-q", "-u", "origin", "feat")
	other := land.clone("pushed")
	if err := os.WriteFile(filepath.Join(other, ".env"), []byte("THEIRS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	land.GitIn(other, "add", "-f", ".env")
	land.GitIn(other, "commit", "-q", "-m", "add .env")
	land.GitIn(other, "push", "-q", "origin", "feat")
	moved := land.GitIn(other, "rev-parse", "HEAD")
	exclude := filepath.Join(land.Root, ".git", "info", "exclude")
	if err := os.WriteFile(exclude, []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	land.Write(map[string]string{".env": "MINE\n"})
	land.lands(moved, "")
	before := land.state()
	land.bench.expect(exitFine, "pr-land", "--no-wait", "--no-push")
	if merges := land.bench.hub.Merges(); len(merges) != 1 || merges[0].HeadSHA != moved {
		t.Fatalf("merges %+v", merges)
	}
	if land.state() != before {
		t.Fatal("the checkout changed")
	}
}

func TestUnpushedLocalCommitsStopTheMergeForReal(t *testing.T) {
	land := newLandRepo(t, plainSettings, nil)
	land.Git("push", "-q", "-u", "origin", "feat")
	tip := land.Git("rev-parse", "feat")
	land.Commit("later, not pushed", map[string]string{"c.txt": "later\n"})
	land.lands(tip, "")
	_, stderr := land.bench.expect(exitNotFine, "pr-land", "--no-wait", "--no-push")
	contains(t, stderr, "would not land")
	if len(land.bench.hub.Merges()) != 0 || land.remoteBranch("feat") != tip {
		t.Fatal("merged, or the remote branch was touched")
	}
}

func TestARemoteThatCannotBeReachedAfterTheMergeIsSaidForReal(t *testing.T) {
	land := newLandRepo(t, plainSettings, nil)
	land.Git("push", "-q", "-u", "origin", "feat")
	land.lands(land.Git("rev-parse", "feat"), "")
	land.Git("remote", "set-url", "origin", filepath.Join(filepath.Dir(land.Root), "no-such.git"))
	stdout, _ := land.bench.expect(exitFine, "pr-land", "--no-wait", "--no-push")
	contains(t, stdout, "merged #7")
	contains(t, stdout, "is not deleted")
}

// --- the Card line ------------------------------------------------------------------------------

var cardFiles = map[string]string{"cards/APP-1-x.md": "---\n{\"id\": \"APP-1\", \"state\": \"active\"}\n---\n# APP-1\n"}

func TestWithCardsAPullRequestIsNotOpenedWithoutADescriptionItCanJudge(t *testing.T) {
	land := newLandRepo(t, cardSettings, cardFiles)
	land.bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{}))
	_, stderr := land.bench.expect(exitNotFine, "pr-land", "--title", "x")
	contains(t, stderr, "asks for a `Card:` line")
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("What it does.\n\nCard: APP-9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr = land.bench.expect(exitNotFine, "pr-land", "--title", "x", "--body-file", bodyFile)
	contains(t, stderr, "APP-9 has no card file in cards")
	if land.remoteBranch("feat") != "" || len(land.bench.hub.Created()) != 0 {
		t.Fatal("pushed or opened before the description was judged")
	}
}

func TestCardAddsTheLineToTheNewDescription(t *testing.T) {
	land := newLandRepo(t, cardSettings, cardFiles)
	tip := land.Git("rev-parse", "feat")
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("What it does.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	land.bench.hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{}))
	land.bench.hub.OnCreatePullRequest("feat", answer(pull(7, "feat", tip)))
	land.bench.expect(exitFine, "pr-land", "--title", "x", "--body-file", bodyFile, "--card", "APP-1", "--no-wait", "--no-merge")
	if created := land.bench.hub.Created(); len(created) != 1 || created[0].Body != "What it does.\n\nCard: APP-1\n" {
		t.Fatalf("created %+v", created)
	}
}

func TestTheSquashBodyIsTheDescriptionThatWasJudged(t *testing.T) {
	land := newLandRepo(t, cardSettings, cardFiles)
	land.Git("push", "-q", "-u", "origin", "feat")
	body := "What it does.\n\nCard: APP-1\n"
	land.lands(land.Git("rev-parse", "feat"), body)
	stdout, _ := land.bench.expect(exitFine, "pr-land", "--no-wait")
	contains(t, stdout, "Card line and tree's rules fine on the merge with main as just fetched")
	if merges := land.bench.hub.Merges(); len(merges) != 1 || merges[0].Message != body {
		t.Fatalf("merges %+v", merges)
	}
}

func TestADescriptionEditedToBreakTheRulesStopsTheMerge(t *testing.T) {
	land := newLandRepo(t, cardSettings, cardFiles)
	land.Git("push", "-q", "-u", "origin", "feat")
	land.lands(land.Git("rev-parse", "feat"), "Card: APP-9\n")
	_, stderr := land.bench.expect(exitNotFine, "pr-land", "--no-wait")
	contains(t, stderr, "APP-9 has no card file in cards")
	if len(land.bench.hub.Merges()) != 0 {
		t.Fatal("merged")
	}
}

func TestTheCardIsJudgedOnTheMergeWithTheBaseAsJustFetched(t *testing.T) {
	// The card file reaches main after the branch was made: the checkout's tree lacks it, the
	// merge with main as just fetched has it.
	land := newLandRepo(t, cardSettings, cardFiles)
	land.Git("push", "-q", "-u", "origin", "feat")
	other := filepath.Join(filepath.Dir(land.Root), "mainline")
	land.Git("clone", "-q", "-b", "main", land.origin, other)
	if err := os.WriteFile(filepath.Join(other, "cards", "APP-2-y.md"), []byte("---\n{\"id\": \"APP-2\", \"state\": \"active\"}\n---\n# APP-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	land.GitIn(other, "add", "-A")
	land.GitIn(other, "commit", "-q", "-m", "card APP-2")
	land.GitIn(other, "push", "-q", "origin", "main")
	land.lands(land.Git("rev-parse", "feat"), "Card: APP-2\n")
	land.bench.expect(exitFine, "pr-land", "--no-wait")
	if len(land.bench.hub.Merges()) != 1 {
		t.Fatal("not merged")
	}
}
