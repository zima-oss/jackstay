package tidy

import (
	"context"
	"errors"
	"fmt"

	"github.com/zima-oss/jackstay/internal/githubapi"
)

// The identifiers of the verdicts, the reasons an item is kept and the notes (the --json facts).
const (
	whyMerged          = "merged"
	whyAddsNothing     = "adds-nothing"
	whyPullMerged      = "pr-merged"
	whyDetachedOnMain  = "detached-on-default"
	whyDetachedHeld    = "detached-held"
	whyDefault         = "default-branch"
	whyNoBranch        = "no-such-branch"
	whyPullOpen        = "pr-open"
	whySearchOpen      = "pr-search-open"
	whyGitHubUnknown   = "github-unknown"
	whySearchUnknown   = "github-search-unknown"
	whyAfterMerged     = "commits-after-merged-pr"
	whyOwnWork         = "own-work"
	whyCheckedOut      = "checked-out"
	whyDetachedUnheld  = "detached-unheld"
	whyNoCommit        = "no-commit"
	whyLocked          = "locked"
	whyHiddenEdits     = "hidden-edits"
	whyUncommitted     = "uncommitted"
	whyIgnoredKept     = "ignored-not-regenerable"
	whyStatusFailed    = "git-status-failed"
	whyInUse           = "in-use"
	whyProcessPartial  = "process-view-partial"
	whyProcessUnusable = "process-table-unreadable"

	noteFetchFailed     = "fetch-failed"
	noteNoRemote        = "no-remote"
	noteRemoteUnclear   = "remote-unclear"
	noteNoDefault       = "no-default-branch"
	noteConfig          = "config-unusable"
	noteGitHubOff       = "github-not-asked"
	noteGitHubMissing   = "github-unavailable"
	noteGitHubToken     = "github-token"
	noteGitHubFailed    = "github-failed"
	noteSearchFailed    = "github-search-failed"
	noteSearchRefused   = "github-search-refused"
	noteProcessUnusable = "process-table-unreadable"
)

// A reason is a verdict or a reason to keep an item: an identifier and its words.
type reason struct {
	ID     string `json:"id"`
	Detail string `json:"detail"`
}

// A note is something the report says about the whole run.
type note = reason

// pullRequests is what tidy asks GitHub (internal/githubapi.Client, or githubfake in tests).
type pullRequests interface {
	PullRequestsByHead(ctx context.Context, head, state string) ([]githubapi.PullRequest, error)
	SearchPullRequestsBySHA(ctx context.Context, sha string) ([]githubapi.PullRequest, error)
}

// hub is GitHub for the project's repository.
type hub struct {
	client pullRequests
	// owner is the repository's owner, for REST's `owner:branch` head filter.
	owner string
}

// ancestry is what the verdict asks git about commits.
type ancestry interface {
	// ahead is how many commits of tip are not on ref (false: git cannot say).
	ahead(ctx context.Context, ref, tip string) (int, bool)
	// addsNothing is the guard's test of tip against the default ref base.
	addsNothing(ctx context.Context, base, tip string) bool
	// holds says whether commit head is here and holds tip.
	holds(ctx context.Context, head, tip string) bool
}

// judge decides whether a branch is finished (docs/design/implementation.md section 8.4, the first table).
type judge struct {
	git ancestry
	// hub is nil when GitHub is not asked; the report says why.
	hub          *hub
	defaultRef   string
	defaultShort string
	// defaults are the refs a branch may be finished against: the default ref and the local branch
	// of its name (merged into a local main that one session pushes later).
	defaults []string
	notes    []note
}

type verdict struct {
	finished bool
	why      reason
}

func finished(id, format string, values ...any) verdict {
	return verdict{finished: true, why: reason{ID: id, Detail: fmt.Sprintf(format, values...)}}
}

func kept(id, format string, values ...any) verdict {
	return verdict{why: reason{ID: id, Detail: fmt.Sprintf(format, values...)}}
}

func (judge *judge) say(id, format string, values ...any) {
	judge.notes = append(judge.notes, note{ID: id, Detail: fmt.Sprintf(format, values...)})
}

// branch is the verdict on a local branch at tip ("" when there is no such branch). Every question
// is asked of that one commit. An open pull request keeps it whatever git says, so GitHub is asked
// first: for pull requests from a branch of its name and, when there is none, for those that hold
// its tip.
func (judge *judge) branch(ctx context.Context, name, tip string) verdict {
	if name == judge.defaultShort || "refs/heads/"+name == judge.defaultRef {
		return kept(whyDefault, "the default branch")
	}
	if tip == "" {
		return kept(whyNoBranch, "no such branch")
	}
	var pulls, held []githubapi.PullRequest
	if judge.hub != nil {
		found, err := judge.hub.client.PullRequestsByHead(ctx, judge.hub.owner+":"+name, "all")
		if err != nil {
			judge.say(noteGitHubFailed, "GitHub could not list the pull requests of %s (%v): it is kept", name, err)
			return kept(whyGitHubUnknown, "GitHub could not say whether a pull request from it is open")
		}
		pulls = found
		if open, ok := firstOpen(pulls); ok {
			return kept(whyPullOpen, "pull request #%d is open", open.Number)
		}
		if len(pulls) == 0 {
			// No pull request from a branch of this name: the same commits may have gone up under
			// another name (`pr-235` fetched from #235, a branch renamed before the push), and an
			// open one keeps the branch even when git calls it merged (docs/design/implementation.md section 8.4).
			if held, err = judge.hub.client.SearchPullRequestsBySHA(ctx, tip); err != nil {
				var limited *githubapi.RateLimitedError
				if errors.As(err, &limited) {
					judge.say(noteSearchRefused, "GitHub refused the search for %s's tip (%v): it is kept", name, err)
				} else {
					judge.say(noteSearchFailed, "GitHub could not search the pull requests that hold %s's tip (%v): it is kept", name, err)
				}
				return kept(whySearchUnknown, "no pull request from it, and GitHub could not say which ones hold its tip")
			}
			if open, ok := firstOpen(held); ok {
				return kept(whySearchOpen, "pull request #%d holds this tip and is open", open.Number)
			}
		}
	}
	for _, ref := range judge.defaults {
		if count, known := judge.git.ahead(ctx, ref, tip); known && count == 0 {
			return finished(whyMerged, "merged: every commit is on %s", ref)
		}
	}
	for _, ref := range judge.defaults {
		if judge.git.addsNothing(ctx, ref, tip) {
			return finished(whyAddsNothing, "adds nothing: merging it into %s would change no file", ref)
		}
	}
	if found, ok := judge.mergedHolding(ctx, tip, pulls); ok {
		return found
	}
	for _, pull := range pulls {
		if pull.Merged {
			return kept(whyAfterMerged, "#%d was merged at %s, and this branch has commits that are not in it",
				pull.Number, short(pull.HeadSHA))
		}
	}
	if found, ok := judge.mergedHolding(ctx, tip, held); ok {
		return found
	}
	return kept(whyOwnWork, "work of its own that is not on the default branch and not in a merged pull request")
}

func firstOpen(pulls []githubapi.PullRequest) (githubapi.PullRequest, bool) {
	for _, pull := range pulls {
		if pull.State == "open" {
			return pull, true
		}
	}
	return githubapi.PullRequest{}, false
}

// mergedHolding is the pr-merged verdict when one of pulls was merged with tip as its head or with
// a head that holds tip.
func (judge *judge) mergedHolding(ctx context.Context, tip string, pulls []githubapi.PullRequest) (verdict, bool) {
	for _, pull := range pulls {
		if !pull.Merged || pull.HeadSHA == "" {
			continue
		}
		source := ""
		if pull.Head != "" {
			source = " (from " + pull.Head + ")"
		}
		if pull.HeadSHA == tip {
			return finished(whyPullMerged, "pr-merged: #%d%s was merged with this very tip", pull.Number, source), true
		}
		if judge.git.holds(ctx, pull.HeadSHA, tip) {
			return finished(whyPullMerged, "pr-merged: #%d%s was merged at %s, which holds this tip",
				pull.Number, source, short(pull.HeadSHA)), true
		}
	}
	return verdict{}, false
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
