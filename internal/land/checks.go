package land

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/hosting"
)

// requiredChecks are the checks a merge needs (docs/design/land.md section 4): the base's rules'
// and those `land.checks` lists in the base's file, never fewer. gate is "" when they make CI a
// gate; else why they do not (the rules require none, or cannot be read and `checks` lists none).
func requiredChecks(rules hosting.Rules, basePolicy config.Land) ([]hosting.RequiredCheck, string) {
	var required []hosting.RequiredCheck
	if rules.ChecksRead {
		required = append(required, rules.Required...)
	}
	for _, name := range basePolicy.Checks {
		listed := hosting.RequiredCheck{Name: name}
		if !slices.ContainsFunc(required, func(check hosting.RequiredCheck) bool { return check.Name == name }) {
			required = append(required, listed)
		}
	}
	switch {
	case len(required) > 0:
		return required, ""
	case !rules.ChecksRead:
		unread := rules.Unread
		if unread == "" {
			unread = "the host did not say"
		}
		return nil, "the base's rules cannot be read (" + unread + ") and `land.checks` lists none"
	default:
		return nil, "the base's rules require no check and `land.checks` lists none"
	}
}

// verdict is a change's checks judged.
type verdict struct {
	pending  []string        // required, not settled or not registered yet
	failed   []hosting.Check // required, failed or cancelled
	passed   int             // required, passed or skipped
	reported []hosting.Check // not required, and failed, cancelled or pending: said, never waited for
	onMerge  bool            // the required checks were judged on the test merge
}

// judge judges checks against the required ones. Each required check must pass or be skipped on
// the commit the host judges: the test merge once it has results, else the head. With none
// required (no gate), every check is waited for and reported, and none blocks.
func judge(checks []hosting.Check, required []hosting.RequiredCheck) verdict {
	var judged verdict
	judged.onMerge = slices.ContainsFunc(checks, func(check hosting.Check) bool { return check.OnTestMerge })
	if len(required) == 0 {
		for _, check := range checks {
			switch {
			case !check.State.Settled():
				judged.pending = append(judged.pending, check.Name)
			case check.State == hosting.Fail || check.State == hosting.Cancelled:
				judged.reported = append(judged.reported, check)
			default:
				judged.passed++
			}
		}
		return judged
	}
	present := make([]bool, len(required))
	for _, check := range checks {
		index := slices.IndexFunc(required, func(wanted hosting.RequiredCheck) bool { return wanted.SatisfiedBy(check) })
		if index < 0 {
			if check.State != hosting.Pass && check.State != hosting.Skipped {
				judged.reported = append(judged.reported, check)
			}
			continue
		}
		if check.OnTestMerge != judged.onMerge {
			continue // the host judges the other commit
		}
		present[index] = true
		switch check.State {
		case hosting.Pass, hosting.Skipped:
			judged.passed++
		case hosting.Fail, hosting.Cancelled:
			judged.failed = append(judged.failed, check)
		default:
			judged.pending = append(judged.pending, check.Name)
		}
	}
	for index, check := range required {
		if !present[index] {
			judged.pending = append(judged.pending, check.String()+" (not registered yet)")
		}
	}
	return judged
}

// identity is what binds checks to a change: its repository, head, base and test merge.
func sameIdentity(before, now hosting.Change) bool {
	return before.Number == now.Number && before.Head == now.Head && before.HeadSHA == now.HeadSHA &&
		before.Base == now.Base && before.BaseSHA == now.BaseSHA && before.TestMerge == now.TestMerge &&
		strings.EqualFold(before.HeadRepository, now.HeadRepository) && strings.EqualFold(before.BaseRepository, now.BaseRepository)
}

// movedError says what moved between two reads of a change.
func movedError(before, now hosting.Change) error {
	switch {
	case before.HeadSHA != now.HeadSHA:
		return failf(exitMoved, "the head of #%d moved from %s to %s; run it again", before.Number, short(before.HeadSHA, 8), short(now.HeadSHA, 8))
	case before.Base != now.Base:
		return failf(exitMoved, "the base of #%d changed from %s to %s; run it again", before.Number, before.Base, now.Base)
	case before.BaseSHA != now.BaseSHA:
		return failf(exitMoved, "%s moved (from %s to %s) after the checks of #%d were read; run it again",
			before.Base, short(before.BaseSHA, 8), short(now.BaseSHA, 8), before.Number)
	case before.TestMerge != now.TestMerge:
		return failf(exitMoved, "the test merge of #%d changed after its checks were read; run it again", before.Number)
	}
	return failf(exitMoved, "the identity of #%d changed after its checks were read; run it again", before.Number)
}

// waitChecks is step 3: wait until the required checks pass (exit 0, with the change the checks
// were read for), one fails (refused), the deadline passes (pending), or the head moves (moved).
// With no gate it waits for every check to settle and reports them.
func (work *session) waitChecks(number int, headSHA string, required []hosting.RequiredCheck, deadline time.Time) (hosting.Change, int, error) {
	started := work.deps.Clock.Now()
	announced := false
	for {
		before, err := work.readChange(number)
		if err != nil {
			return hosting.Change{}, 0, err
		}
		if before.HeadSHA != headSHA {
			return hosting.Change{}, 0, movedError(hosting.Change{Number: number, HeadSHA: headSHA}, before)
		}
		checks, err := work.host.Checks(work.ctx, before)
		if err != nil {
			return hosting.Change{}, 0, fmt.Errorf("the checks of #%d: %w", number, err)
		}
		// Read after the checks: checks read, then a change still the same, are that change's.
		after, err := work.readChange(number)
		if err != nil {
			return hosting.Change{}, 0, err
		}
		if after.HeadSHA != headSHA {
			return hosting.Change{}, 0, movedError(before, after)
		}
		settledRead := sameIdentity(before, after)
		judged := judge(checks, required)
		if len(judged.failed) > 0 && settledRead {
			work.report(checks, judged)
			if explainer, ok := work.host.(hosting.Explainer); ok {
				if detail := explainer.Explain(work.ctx, judged.failed, work.options.lines); detail != "" {
					work.say("%s", detail)
				}
			}
			return hosting.Change{}, 0, failf(exitRefused, "%d required check(s) of #%d did not pass: %s", len(judged.failed),
				number, strings.Join(names(judged.failed), ", "))
		}
		if len(checks) == 0 && len(required) == 0 && !announced {
			work.say("no checks registered for %s yet: waiting for them to appear (this is not a pass)", short(headSHA, 8))
			announced = true
		}
		// With no gate, a repository with no CI never registers a check: once the checks had time
		// to appear, nothing more is waited for (the person is told so).
		noneAppeared := len(checks) == 0 && len(required) == 0 && !work.deps.Clock.Now().Before(started.Add(checksAppearWait))
		if noneAppeared && settledRead {
			work.say("no check registered for %s within %s: nothing to wait for", short(headSHA, 8), humanSeconds(checksAppearWait))
			return after, exitLanded, nil
		}
		if settledRead && len(judged.pending) == 0 && (len(checks) > 0 || len(required) > 0) {
			work.report(checks, judged)
			work.say("GREEN: %d required check(s) passed on %s, %s waiting.", judged.passed,
				map[bool]string{true: "the test merge " + short(after.TestMerge, 8), false: "the head " + short(headSHA, 8)}[judged.onMerge],
				humanSeconds(work.deps.Clock.Now().Sub(started)))
			return after, exitLanded, nil
		}
		if len(judged.pending) > 0 {
			shown := judged.pending[:min(4, len(judged.pending))]
			more := ""
			if len(judged.pending) > len(shown) {
				more = fmt.Sprintf(" (+%d)", len(judged.pending)-len(shown))
			}
			work.say("  %s: %d pending — %s%s", humanSeconds(work.deps.Clock.Now().Sub(started)), len(judged.pending), strings.Join(shown, ", "), more)
		}
		more, err := work.sleepUntil(deadline, work.options.interval)
		if err != nil {
			return hosting.Change{}, 0, err
		}
		if !more {
			work.report(checks, judged)
			if len(judged.pending) == 0 {
				return hosting.Change{}, 0, failf(exitPending, "no check registered for #%d within %s", number, humanSeconds(work.options.timeout))
			}
			return hosting.Change{}, 0, failf(exitPending, "still pending at the deadline: %s", strings.Join(judged.pending, ", "))
		}
	}
}

func names(checks []hosting.Check) []string {
	out := make([]string, 0, len(checks))
	for _, check := range checks {
		out = append(out, check.Name)
	}
	return out
}

// report says each check, red first, and what is set aside.
func (work *session) report(checks []hosting.Check, judged verdict) {
	order := map[hosting.State]int{hosting.Fail: 0, hosting.Cancelled: 1, hosting.Pending: 2, hosting.Pass: 3, hosting.Skipped: 4}
	sorted := slices.Clone(checks)
	sort.SliceStable(sorted, func(left, right int) bool {
		if order[sorted[left].State] != order[sorted[right].State] {
			return order[sorted[left].State] < order[sorted[right].State]
		}
		return sorted[left].Name < sorted[right].Name
	})
	width := 0
	for _, check := range sorted {
		width = max(width, len(check.Name))
	}
	for _, check := range sorted {
		where := "head"
		if check.OnTestMerge {
			where = "test merge"
		}
		work.say("%s", strings.TrimRight(fmt.Sprintf("  %-*s  %-8s %-10s %s", width, check.Name, check.State, where, check.Workflow), " "))
		if check.Note != "" {
			work.say("    %s", check.Note)
		}
	}
	for _, check := range judged.reported {
		work.say("not required: %s %s (reported, not waited for, never blocking)", check.Name, check.State)
	}
}

func (work *session) readChange(number int) (hosting.Change, error) {
	change, found, err := work.host.Change(work.ctx, hosting.ChangeRef{Number: number})
	if err != nil {
		return hosting.Change{}, fmt.Errorf("change #%d: %w", number, err)
	}
	if !found {
		return hosting.Change{}, failf(exitUnexpected, "the host no longer finds change #%d", number)
	}
	return change, nil
}

// ready is a change through steps 2 to 5: the policy, and the change and rules its checks were
// judged for.
type ready struct {
	policy
	change   hosting.Change
	rules    hosting.Rules
	required []hosting.RequiredCheck
	gate     string
}

// prepareLanding is steps 2 to 5 of docs/design/land.md section 3.1: policy, checks, freshness
// (back to step 2 after an update), and the recheck just before a merge.
func (work *session) prepareLanding(number int, deadline time.Time) (ready, int, error) {
	for {
		if !work.deps.Clock.Now().Before(deadline) {
			return ready{}, 0, failf(exitPending, "#%d was not judged ready within %s", number, humanSeconds(work.options.timeout))
		}
		change, err := work.readChange(number)
		if err != nil {
			return ready{}, 0, err
		}
		if change.State != "open" {
			return ready{}, 0, failf(exitRefused, "#%d is %s", number, change.State)
		}
		if change.Draft {
			return ready{}, 0, failf(exitRefused, "#%d is a draft: a draft is not merged", number)
		}
		if change.Base != work.base {
			return ready{}, 0, failf(exitMoved, "the base of #%d changed from %s to %s; run it again", number, work.base, change.Base)
		}
		// Step 2.
		decision, err := work.judgePolicy(change)
		if err != nil {
			return ready{}, 0, err
		}
		rules, err := work.host.Rules(work.ctx, change.Base)
		if err != nil {
			return ready{}, 0, fmt.Errorf("the rules of %s: %w", change.Base, err)
		}
		required, gate := requiredChecks(rules, decision.base)
		if gate != "" {
			decision.agentMay = false
			decision.reasons = append(decision.reasons, gate)
		}
		// Step 3.
		checked, code, err := work.waitChecks(number, change.HeadSHA, required, deadline)
		if err != nil || code != exitLanded {
			return ready{}, code, err
		}
		if checked.BaseSHA != decision.baseSHA {
			continue // the base moved while waiting: judge the policy at the new base
		}
		// Step 4.
		mergeable, err := work.mergeable(number, deadline)
		if err != nil {
			return ready{}, 0, err
		}
		if mergeable.HeadSHA != checked.HeadSHA {
			return ready{}, 0, movedError(checked, mergeable)
		}
		switch mergeable.Mergeable {
		case "no":
			return ready{}, 0, failf(exitRefused, "#%d conflicts with %s: resolve that first", number, change.Base)
		case "unknown":
			return ready{}, 0, failf(exitPending, "the host has not said whether #%d merges", number)
		}
		if mergeable.Behind && rules.UpToDateRead && rules.UpToDate {
			if err := work.updateBranch(mergeable, deadline); err != nil {
				return ready{}, 0, err
			}
			continue
		}
		// Step 5.
		landing := ready{policy: decision, change: checked, rules: rules, required: required, gate: gate}
		if err := work.recheck(landing); err != nil {
			return ready{}, 0, err
		}
		return landing, exitLanded, nil
	}
}

// recheck is step 5: the change, the base tip and the rules read again, and the required checks
// read afresh. Anything moved stops with `moved`; the next run starts at step 2.
func (work *session) recheck(landing ready) error {
	now, err := work.readChange(landing.change.Number)
	if err != nil {
		return err
	}
	if !sameIdentity(landing.change, now) {
		return movedError(landing.change, now)
	}
	rules, err := work.host.Rules(work.ctx, now.Base)
	if err != nil {
		return fmt.Errorf("the rules of %s: %w", now.Base, err)
	}
	if !sameRules(landing.rules, rules) {
		return failf(exitMoved, "the rules of %s changed while #%d was judged; run it again", now.Base, now.Number)
	}
	if landing.gate != "" {
		return nil
	}
	checks, err := work.host.Checks(work.ctx, now)
	if err != nil {
		return fmt.Errorf("the checks of #%d: %w", now.Number, err)
	}
	judged := judge(checks, landing.required)
	if len(judged.failed) > 0 || len(judged.pending) > 0 {
		return failf(exitMoved, "a required check of #%d no longer passes (%s); run it again", now.Number,
			strings.Join(append(names(judged.failed), judged.pending...), ", "))
	}
	last, err := work.readChange(now.Number)
	if err != nil {
		return err
	}
	if !sameIdentity(now, last) {
		return movedError(now, last)
	}
	return nil
}

func sameRules(before, now hosting.Rules) bool {
	return before.ChecksRead == now.ChecksRead && slices.Equal(before.Required, now.Required) &&
		before.UpToDateRead == now.UpToDateRead && before.UpToDate == now.UpToDate &&
		before.QueueRead == now.QueueRead && before.Queue == now.Queue &&
		before.MethodsRead == now.MethodsRead && slices.Equal(before.Methods, now.Methods)
}

// mergeable is the change once the host has computed whether it merges (unknown right after a
// push), or as it stands after the wait.
func (work *session) mergeable(number int, deadline time.Time) (hosting.Change, error) {
	limit := earlier(deadline, work.deps.Clock.Now().Add(mergeableWait))
	for {
		change, err := work.readChange(number)
		if err != nil || change.Mergeable != "unknown" {
			return change, err
		}
		more, err := work.sleepUntil(limit, mergeableStep)
		if err != nil || !more {
			return change, err
		}
	}
}

// updateBranch is step 4: the base merged into the change's branch on the host, at the head that
// was checked, and the new head waited for.
func (work *session) updateBranch(change hosting.Change, deadline time.Time) error {
	work.say("#%d is behind %s and its rules require it up to date: updating the branch on the host", change.Number, change.Base)
	if err := work.host.UpdateBranch(work.ctx, change, change.HeadSHA); err != nil {
		return fmt.Errorf("the update of #%d's branch: %w", change.Number, err)
	}
	limit := earlier(deadline, work.deps.Clock.Now().Add(mergeableWait))
	for {
		now, err := work.readChange(change.Number)
		if err != nil {
			return err
		}
		if now.HeadSHA != change.HeadSHA {
			work.say("#%d: the branch is at %s now; judging it again", change.Number, short(now.HeadSHA, 8))
			return nil
		}
		more, err := work.sleepUntil(limit, mergeableStep)
		if err != nil {
			return err
		}
		if !more {
			return failf(exitPending, "#%d: the update of its branch has not shown on the host within %s", change.Number, humanSeconds(mergeableWait))
		}
	}
}

func earlier(first, second time.Time) time.Time {
	if first.Before(second) {
		return first
	}
	return second
}
