package tidy

import (
	"context"
	"strings"
)

// The identifiers of what --fix did or called off.
const (
	didRemoveTree   = "removed-worktree"
	didDeleteBranch = "deleted-branch"
	didKeepTree     = "kept-worktree"
	didKeepBranch   = "kept-branch"
	didFailTree     = "remove-failed"
	didFailBranch   = "delete-failed"
	didPrune        = "pruned"
	didFailPrune    = "prune-failed"
	didNotSweep     = "not-pruned"
)

// fix removes what the report marks removable and returns the number of failures (an item called
// off counts: it was asked to go and is still there). Between the survey and this another session
// may commit on a branch, write an ignored file or start working in a worktree, so each item is
// judged again just before it is removed (without fetching), and a branch is deleted only at the
// tip that was judged (`git update-ref -d`, which git refuses when the branch has moved).
func (tidy *tidier) fix(ctx context.Context, found *report, say func(action)) int {
	main, failures := found.Repository, 0
	here := repo{dir: main}
	again := func(wanted scope) (*report, string) {
		wanted.start = main
		fresh, err := tidy.survey(ctx, wanted)
		if err != nil {
			return nil, err.Error()
		}
		return fresh, ""
	}
	switch {
	case len(found.Prunable) > 0 && !found.Scoped:
		// A record whose folder is gone may be a worktree that was moved, not removed: pruning it
		// would cut the moved folder off from the repository. Only a record named is pruned.
		failures += len(found.Prunable)
		say(action{ID: didNotSweep, Target: strings.Join(found.Prunable, ", "),
			Detail: "worktree records whose folder is gone are not pruned by a sweep (a moved worktree needs " +
				"`git worktree repair <new path>`; a removed one: `jst tidy --fix --worktree <path>`)"})
	case len(found.Prunable) > 0:
		for _, done := range pruneRecords(ctx, main, found.Prunable) {
			if done.ID == didFailPrune {
				failures++
			}
			say(done)
		}
	}
	gone := map[string]bool{}
	for _, item := range found.Worktrees {
		if !item.Remove {
			continue
		}
		fresh, whyNot := again(scope{worktrees: []string{item.Path}})
		var now *treeItem
		if fresh != nil {
			for index := range fresh.Worktrees {
				if fresh.Worktrees[index].Path == item.Path {
					now = &fresh.Worktrees[index]
				}
			}
		}
		switch {
		case whyNot != "":
		case now == nil:
			whyNot = "not found again"
		case now.Head != item.Head:
			whyNot = "its commit moved"
		case !now.Remove:
			whyNot = details(now.Keep)
		}
		if whyNot != "" {
			failures++
			say(action{ID: didKeepTree, Target: item.Path, Detail: "it changed since the survey (" + whyNot + ")"})
			continue
		}
		if _, err := here.run(ctx, "worktree", "remove", item.Path); err != nil {
			failures++
			say(action{ID: didFailTree, Target: item.Path, Detail: gitSaid(err, 300)})
			continue
		}
		gone[item.Path] = true
		say(action{ID: didRemoveTree, Target: item.Path, Detail: item.Why.Detail})
	}
	for _, item := range found.Branches {
		if !item.Remove || (item.WithWorktree != "" && !gone[item.WithWorktree]) {
			continue
		}
		fresh, whyNot := again(scope{branches: []string{item.Name}})
		var now *branchItem
		if fresh != nil {
			for index := range fresh.Branches {
				if fresh.Branches[index].Name == item.Name {
					now = &fresh.Branches[index]
				}
			}
		}
		switch {
		case whyNot != "":
		case now == nil:
			whyNot = "not found again"
		case now.Why.ID == whyCheckedOut:
			whyNot = "it is " + now.Why.Detail + " now"
		case now.WithWorktree != "" && !gone[now.WithWorktree]:
			whyNot = "it is checked out in " + now.WithWorktree + " now"
		case !now.Remove || item.Tip == "" || now.Tip != item.Tip:
			whyNot = "its tip moved or it is no longer finished"
		}
		if whyNot != "" {
			failures++
			say(action{ID: didKeepBranch, Target: item.Name, Detail: "it changed since the survey (" + whyNot + ")"})
			continue
		}
		if _, err := here.run(ctx, "update-ref", "-d", "refs/heads/"+item.Name, item.Tip); err != nil {
			failures++
			say(action{ID: didFailBranch, Target: item.Name, Detail: gitSaid(err, 300)})
			continue
		}
		say(action{ID: didDeleteBranch, Target: item.Name, Detail: item.Why.Detail})
	}
	return failures
}

func details(reasons []reason) string {
	said := make([]string, 0, len(reasons))
	for _, why := range reasons {
		said = append(said, why.Detail)
	}
	return strings.Join(said, "; ")
}
