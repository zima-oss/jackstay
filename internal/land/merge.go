package land

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/hosting"
)

// finish is steps 5 to 7 for a change through step 4, in one fixed order:
//
//  1. a change for a person (and no --confirm) is rechecked and handed over (exit 6);
//  2. otherwise the local checks run (nothing left behind, the Card line, the method);
//  3. the recheck (step 5) reads the change, the rules, the required checks and, last, the base
//     branch's tip on the host, and nothing local runs between it and the write;
//  4. the one write (step 6): a merge or an enqueue at the rechecked head, its answer settled by
//     reading the change, never by a second request;
//  5. the remote branch deleted under its lease (step 7).
func (work *session) finish(landing ready, deadline time.Time) (int, error) {
	if !landing.agentMay && !work.options.confirm {
		if _, err := work.recheck(landing); err != nil {
			return 0, err
		}
		work.say("\n#%d is ready for a person: merge it on the host's page, or run `jst land --confirm` at a terminal "+
			"and type its number", landing.change.Number)
		return exitForPerson, nil
	}
	body, err := work.localChecks(landing)
	if err != nil {
		return 0, err
	}
	change, err := work.recheck(landing)
	if err != nil {
		return 0, err
	}
	return work.write(landing, change, body, deadline)
}

// localChecks are what this checkout judges before the recheck: no commit here left behind, the
// Card line of the description, and a merge method the repository allows. It returns the
// description judged: the merge commit's message.
func (work *session) localChecks(landing ready) (string, error) {
	change := landing.change
	if err := work.nothingLeftBehind(change.Number, change.HeadSHA); err != nil {
		return "", err
	}
	method := methodOf(landing.base)
	if err := cardsNeedAMergeCommit(method, work.cardSection()); err != nil {
		return "", err
	}
	if landing.rules.MethodsRead && !slices.Contains(landing.rules.Methods, method) {
		return "", failf(exitRefused, "the repository does not allow a %s merge (`land.method`); it allows: %v", method, landing.rules.Methods)
	}
	if work.cardSection() == nil {
		return change.Body, nil
	}
	return work.judgeDescriptionOf(change)
}

// recheck is step 5: the change, the rules and the required checks read again, then the change
// once more, and last the base branch's tip as the host has it now. Anything moved stops with
// `moved`; the next run starts at step 2. It returns the change as last read: the write uses it.
func (work *session) recheck(landing ready) (hosting.Change, error) {
	now, err := work.readChange(landing.change.Number)
	if err != nil {
		return hosting.Change{}, err
	}
	if !sameIdentity(landing.change, now) {
		return hosting.Change{}, movedError(landing.change, now)
	}
	rules, err := work.host.Rules(work.ctx, now.Base)
	if err != nil {
		return hosting.Change{}, fmt.Errorf("the rules of %s: %w", now.Base, err)
	}
	if !sameRules(landing.rules, rules) {
		return hosting.Change{}, failf(exitMoved, "the rules of %s changed while #%d was judged; run it again", now.Base, now.Number)
	}
	if landing.gate == "" {
		if err := work.requiredStillPass(now, landing.required); err != nil {
			return hosting.Change{}, err
		}
	}
	last, err := work.readChange(now.Number)
	if err != nil {
		return hosting.Change{}, err
	}
	if !sameIdentity(now, last) {
		return hosting.Change{}, movedError(now, last)
	}
	tip, err := work.host.BranchTip(work.ctx, last.Base)
	if err != nil {
		return hosting.Change{}, fmt.Errorf("the tip of %s: %w", last.Base, err)
	}
	if tip != landing.change.BaseSHA {
		return hosting.Change{}, failf(exitMoved, "%s is at %s on the host, not at %s where #%d was judged; run it again",
			last.Base, short(tip, 8), short(landing.change.BaseSHA, 8), last.Number)
	}
	return last, nil
}

// write is step 6, then step 7: one merge or enqueue at the rechecked head.
func (work *session) write(landing ready, change hosting.Change, body string, deadline time.Time) (int, error) {
	headSHA := change.HeadSHA
	queued := landing.rules.QueueRead && landing.rules.Queue
	var answer hosting.Merged
	var asked error
	if queued {
		if asked = work.host.Enqueue(work.ctx, change, headSHA); asked == nil {
			work.say("#%d is in the host's merge queue at %s", change.Number, short(headSHA, 10))
		}
	} else {
		answer, asked = work.host.Merge(work.ctx, change, hosting.MergeRequest{Method: methodOf(landing.base), ExpectedHead: headSHA, Body: body})
	}
	landed, err := work.readChange(change.Number)
	if err != nil {
		return 0, fmt.Errorf("whether #%d merged: %w", change.Number, err)
	}
	for queued && asked == nil && !landed.Merged() && landed.State == "open" {
		more, err := work.sleepUntil(deadline, work.options.interval)
		if err != nil {
			return 0, err
		}
		if !more {
			return 0, failf(exitPending, "#%d is still in the merge queue at the deadline; the remote branch is kept", change.Number)
		}
		if landed, err = work.readChange(change.Number); err != nil {
			return 0, err
		}
	}
	if !landed.Merged() {
		if asked != nil {
			return 0, fmt.Errorf("the host did not merge #%d at %s: %w", change.Number, short(headSHA, 10), asked)
		}
		if landed.State != "open" {
			return 0, failf(exitRefused, "#%d is %s, not merged", change.Number, landed.State)
		}
		return 0, failf(exitPending, "#%d is not merged yet (the host holds it); the remote branch is kept", change.Number)
	}
	if landed.HeadSHA != headSHA {
		return 0, failf(exitUnexpected, "#%d merged at %s, not at %s, the head that was checked; inspect it before cleaning up",
			change.Number, short(landed.HeadSHA, 10), short(headSHA, 10))
	}
	switch {
	case asked != nil:
		work.say("#%d: the answer to the merge was lost (%v); the host says it merged at %s", change.Number, asked, short(headSHA, 10))
	case answer.SHA != "":
		work.say("merged #%d into %s (%s)", change.Number, landed.Base, short(answer.SHA, 10))
	default:
		work.say("merged #%d into %s", change.Number, landed.Base)
	}
	work.deleteRemoteBranch(landed, headSHA)
	work.say("this checkout is left as it is: still on %s. `jst tidy --fix --branch %s` removes the branch once nothing would "+
		"be lost.", work.branch, work.branch)
	return exitLanded, nil
}

// cardsNeedAMergeCommit refuses `land.method: rebase` in a project whose Card line is in the
// description: a rebase writes no merge commit, so the line would not land.
func cardsNeedAMergeCommit(method hosting.Method, cards map[string]any) error {
	if method == hosting.Rebase && cards != nil {
		return failf(exitInvalid, "`land.method` is rebase, and this project's Card line is in the change request's "+
			"description, which a rebase does not land: use squash or merge")
	}
	return nil
}

// requiredStillPass reads the checks afresh: a required check no longer passing means moved.
func (work *session) requiredStillPass(now hosting.Change, required []hosting.RequiredCheck) error {
	checks, err := work.host.Checks(work.ctx, now)
	if err != nil {
		return fmt.Errorf("the checks of #%d: %w", now.Number, err)
	}
	judged := judge(checks, required)
	if len(judged.failed) > 0 || len(judged.pending) > 0 {
		return failf(exitMoved, "a required check of #%d no longer passes (%s); run it again", now.Number,
			strings.Join(append(names(judged.failed), judged.pending...), ", "))
	}
	return nil
}
