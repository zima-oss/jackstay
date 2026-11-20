package land

import (
	"fmt"
	"slices"
	"time"

	"github.com/zima-oss/jackstay/internal/hosting"
)

// merge is steps 6 and 7: the host asked to merge at the head step 5 rechecked (or to queue it
// when the base requires a queue), a lost answer settled by reading the change, never by a
// second request; then the remote branch deleted under its lease.
func (work *session) merge(landing ready, deadline time.Time) (int, error) {
	change := landing.change
	headSHA := change.HeadSHA
	if err := work.nothingLeftBehind(change.Number, headSHA); err != nil {
		return 0, err
	}
	body := change.Body
	if work.cardSection() != nil {
		var err error
		if body, err = work.judgeMerge(change); err != nil {
			return 0, err
		}
	}
	if err := work.recheck(landing); err != nil {
		return 0, err
	}
	method := methodOf(landing.base)
	if landing.rules.MethodsRead && !slices.Contains(landing.rules.Methods, method) {
		return 0, failf(exitRefused, "the repository does not allow a %s merge (`land.method`); it allows: %v", method, landing.rules.Methods)
	}
	queued := landing.rules.QueueRead && landing.rules.Queue
	var answer hosting.Merged
	var asked error
	if queued {
		asked = work.host.Enqueue(work.ctx, change, headSHA)
		if asked == nil {
			work.say("#%d is in the host's merge queue at %s", change.Number, short(headSHA, 10))
		}
	} else {
		answer, asked = work.host.Merge(work.ctx, change, hosting.MergeRequest{Method: method, ExpectedHead: headSHA, Title: "", Body: body})
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
	if asked != nil {
		work.say("#%d: the answer to the merge was lost (%v); the host says it merged at %s", change.Number, asked, short(headSHA, 10))
	} else if answer.SHA != "" {
		work.say("merged #%d into %s (%s)", change.Number, landed.Base, short(answer.SHA, 10))
	} else {
		work.say("merged #%d into %s", change.Number, landed.Base)
	}
	work.deleteRemoteBranch(landed, headSHA)
	work.say("this checkout is left as it is: still on %s. `jst tidy --fix --branch %s` removes the branch once nothing would "+
		"be lost.", work.branch, work.branch)
	return exitLanded, nil
}
