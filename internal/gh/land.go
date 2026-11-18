package gh

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/lint"
	"github.com/zima-oss/jackstay/internal/remote"
)

// How long pr-land lets GitHub compute mergeability, or show a branch update, after a push.
const (
	mergeableWait = 60 * time.Second
	mergeableStep = 5 * time.Second
)

// pushRemote is the remote the branch's work goes to: its own upstream remote, else the rule
// sync, start and tidy share (github.remote, origin, the remote main tracks, the only one).
func (work *session) pushRemote(branch string) (string, error) {
	listed, err := work.git.Run(work.ctx, "remote")
	if err != nil {
		return "", failf(exitRequest, "the remotes cannot be listed: %v", err)
	}
	if branch != "" {
		tracked, err := work.gitLine("config", "--get", "branch."+branch+".remote")
		if err == nil && slices.Contains(strings.Fields(string(listed)), tracked) {
			return tracked, nil
		}
	}
	name, err := remote.Name(work.ctx, work.git, work.settings.Remote)
	if err != nil {
		return "", failf(exitRequest, "%v", err)
	}
	return name, nil
}

// ahead is how many commits of branch its remote copy lacks; -1 when there is no remote copy.
func (work *session) ahead(branch string) int {
	remoteName, err := work.pushRemote(branch)
	if err != nil {
		return -1
	}
	count, err := work.gitLine("rev-list", "--count", remoteName+"/"+branch+".."+branch)
	if err != nil {
		return -1
	}
	number, err := strconv.Atoi(count)
	if err != nil {
		return -1
	}
	return number
}

func (work *session) clean() (bool, error) {
	status, err := work.gitLine("status", "--porcelain")
	if err != nil {
		return false, failf(exitRequest, "the work tree's state cannot be read: %v", err)
	}
	return status == "", nil
}

// --- pr-status ---------------------------------------------------------------------------------

func runPRStatus(work *session, args []string) (int, error) {
	if _, err := parseFlags(commandFlags("pr-status", work.deps.Stderr), args, 0); err != nil {
		return 0, err
	}
	branch, err := work.currentBranch()
	if err != nil {
		return 0, err
	}
	repository, err := work.checkoutRepository()
	if err != nil {
		return 0, err
	}
	if err := work.connect(repository); err != nil {
		return 0, err
	}
	base, err := work.github.DefaultBranch(work.ctx)
	if err != nil {
		base = "unknown: " + err.Error() // read-only: said, not a stop
	}
	isClean, err := work.clean()
	if err != nil {
		return 0, err
	}
	work.say("repo    %s", work.repository)
	work.say("branch  %s  (base %s)", branch, base)
	work.say("tree    %s", map[bool]string{true: "clean", false: "DIRTY"}[isClean])
	if count := work.ahead(branch); count < 0 {
		work.say("ahead   no upstream yet")
	} else {
		work.say("ahead   %d commit(s) unpushed", count)
	}
	pull, found, err := work.openPullOf(branch)
	if err != nil {
		return 0, err
	}
	if !found {
		work.say("pr      none open for this branch")
		return exitFine, nil
	}
	state, err := work.github.MergeableState(work.ctx, pull.Number)
	if err != nil {
		return 0, err
	}
	draft := ""
	if pull.Draft {
		draft = " DRAFT"
	}
	work.say("pr      #%d %s%s — %s", pull.Number, strings.ToUpper(state), draft, pull.Title)
	return work.ciStatus(pull.Number)
}

// --- pr-land -----------------------------------------------------------------------------------

type landOptions struct {
	title, bodyFile, reason           string
	cards                             []string
	push, create, wait, merge, dryRun bool
	waiting                           waitOptions
}

func runPRLand(work *session, args []string) (int, error) {
	timeoutDefault, intervalDefault, linesDefault := work.waitDefaults()
	flags := commandFlags("pr-land", work.deps.Stderr)
	title := flags.String("title", "", "the new pull request's title")
	bodyFile := flags.String("body-file", "", "the new pull request's description, from a file")
	var cardIDs listFlag
	flags.Var(&cardIDs, "card", "the card this is work for (repeatable), or `none` with --reason")
	reason := flags.String("reason", "", "why a change is no card's work (with --card none)")
	noPush := flags.Bool("no-push", false, "do not push")
	noCreate := flags.Bool("no-create", false, "do not open a pull request")
	noWait := flags.Bool("no-wait", false, "do not wait for the checks")
	noMerge := flags.Bool("no-merge", false, "do not merge")
	dryRun := flags.Bool("dry-run", false, "print the plan and stop")
	timeout := flags.Float64("timeout", timeoutDefault, "seconds to wait for the checks")
	interval := flags.Float64("interval", intervalDefault, "seconds between looks")
	lines := flags.Int("lines", linesDefault, "log lines to show per failed check")
	if _, err := parseFlags(flags, args, 0); err != nil {
		return 0, err
	}
	options := landOptions{title: *title, bodyFile: *bodyFile, reason: *reason, cards: cardIDs,
		push: !*noPush, create: !*noCreate, wait: !*noWait, merge: !*noMerge, dryRun: *dryRun,
		waiting: waitOptions{lines: *lines}}
	var err error
	if options.waiting.timeout, err = duration("timeout", *timeout, false); err != nil {
		return 0, err
	}
	if options.waiting.interval, err = duration("interval", *interval, true); err != nil {
		return 0, err
	}
	if options.waiting.lines < 1 {
		return 0, failf(exitRequest, "--lines must be above 0")
	}
	return work.prLand(options)
}

// prLand is push → open → wait → squash-merge on GitHub → delete the remote branch. Every step can
// be skipped; the order cannot. The plan is printed first; --dry-run stops there. This checkout is
// never changed: no switch, no pull, no local branch deleted (W-019); `jst tidy` removes the
// branch once GitHub says its pull request merged at that tip.
func (work *session) prLand(options landOptions) (int, error) {
	line, err := cardLine(options.cards, options.reason)
	if err != nil {
		return 0, err
	}
	branch, err := work.currentBranch()
	if err != nil {
		return 0, err
	}
	if branch == "main" || branch == "master" {
		return 0, failf(exitRequest, "on %s: this flow needs a feature branch. `%s` takes pull requests only.", branch, branch)
	}
	isClean, err := work.clean()
	if err != nil {
		return 0, err
	}
	if !isClean {
		return 0, failf(exitRequest, "the work tree is dirty. Commit or stash first — landing a pull request while changes are "+
			"uncommitted is how a change gets left behind.")
	}
	root, err := work.gitLine("rev-parse", "--show-toplevel")
	if err != nil {
		return 0, failf(exitRequest, "not a checkout: %v", err)
	}
	repository, err := work.checkoutRepository()
	if err != nil {
		return 0, err
	}
	if err := work.connect(repository); err != nil {
		return 0, err
	}
	// The base is GitHub's own default branch: never a guess from what this checkout fetched.
	base, err := work.github.DefaultBranch(work.ctx)
	if err != nil {
		return 0, fmt.Errorf("the default branch of %s: %w", work.repository, err)
	}
	if branch == base {
		return 0, failf(exitRequest, "on %s: this flow needs a feature branch. `%s` takes pull requests only.", branch, branch)
	}
	pull, open, err := work.openPullOf(branch)
	if err != nil {
		return 0, err
	}
	// Asked before anything is pushed (asking after the push would leave the branch published).
	if options.create && !open && options.title == "" {
		return 0, failf(exitRequest, "a new pull request needs --title")
	}
	body, err := work.newDescription(options, open, line, branch, base, root)
	if err != nil {
		return 0, err
	}
	ahead := work.ahead(branch)
	var plan []string
	if options.push && ahead != 0 {
		if ahead > 0 {
			plan = append(plan, fmt.Sprintf("push %s (%d commits)", branch, ahead))
		} else {
			plan = append(plan, fmt.Sprintf("push %s (new upstream)", branch))
		}
	}
	if options.create && !open {
		plan = append(plan, "open a pull request")
	}
	if options.wait {
		plan = append(plan, work.requiredChecks(&options, open, pull, base))
	}
	if options.merge {
		plan = append(plan, "squash-merge at the head that was checked, then delete the remote branch "+
			"(this checkout, its HEAD and its branches are left as they are)")
	}
	if len(plan) == 0 {
		work.say("nothing to do with the flags given")
		return exitFine, nil
	}
	work.say("plan:")
	for index, step := range plan {
		work.say("  %d. %s", index+1, step)
	}
	if options.dryRun {
		work.say("\n--dry-run: stopping here")
		return exitFine, nil
	}
	work.say("")

	if options.push && ahead != 0 {
		if err := work.pushBranch(branch, ahead < 0); err != nil {
			return 0, err
		}
	}
	switch {
	case !open && options.create:
		if pull, err = work.openPull(options.title, body, base, branch); err != nil {
			return 0, err
		}
	case open:
		work.say("pull request #%d already open", pull.Number)
	default:
		work.say("no pull request and --no-create: stopping")
		return exitFine, nil
	}
	number := pull.Number

	if options.wait {
		code, err := work.ciWait(number, options.waiting)
		if err != nil {
			return 0, err
		}
		if code != exitFine {
			work.say("\nnot merging #%d: checks did not pass", number)
			return code, nil
		}
	}
	if !options.merge {
		return exitFine, nil
	}
	return work.mergeAndDelete(number, branch, base, options)
}

// requiredChecks reads the checks the pull request's base requires into the wait's options, and
// is the plan's line for the wait. Rules that cannot be read, or that require none, leave every
// check waited for, as before.
func (work *session) requiredChecks(options *landOptions, open bool, pull githubapi.PullRequest, base string) string {
	target := base
	if open && pull.Base != "" {
		target = pull.Base
	}
	required, err := work.github.RequiredChecks(work.ctx, target)
	switch {
	case err != nil:
		return fmt.Sprintf("wait for every check on the resulting head (the rules of %s cannot be read: %v)", target, err)
	case len(required) == 0:
		return fmt.Sprintf("wait for every check on the resulting head (the rules of %s require none)", target)
	}
	options.waiting.required = required
	return fmt.Sprintf("wait for the checks the rules of %s require on the resulting head (%s); a failure of another check "+
		"also stops the merge, one still pending does not", target, strings.Join(required, ", "))
}

// newDescription is the new pull request's description (nil-like "" when there is none to give),
// judged before the push in a project with cards; --card is refused for an open pull request.
func (work *session) newDescription(options landOptions, open bool, line, branch, base, root string) (string, error) {
	section := work.cardSection()
	if !options.create || open {
		if line != "" {
			return "", failf(exitRequest, "--card is for the description of a new pull request; this one is open already: "+
				"edit its description on GitHub (it is judged again before the merge)")
		}
		return "", nil
	}
	body, given := "", false
	if options.bodyFile != "" {
		data, err := os.ReadFile(options.bodyFile)
		if err != nil {
			return "", failf(exitRequest, "cannot read %s: %v", options.bodyFile, err)
		}
		body, given = string(data), true
	}
	if line != "" {
		body, given = withLine(body, line), true
	}
	if section != nil {
		if !given {
			return "", failf(exitNotFine, "this project asks for a `Card:` line in the description: pass --card ID "+
				"(or --card none --reason TEXT), or a --body-file that has one")
		}
		if err := judgeDescription(section, lint.WorktreeFiles(work.ctx, root, work.git), body, branch); err != nil {
			return "", err
		}
	}
	if !given {
		return work.filled(branch, base), nil
	}
	return body, nil
}

// filled is a description from the branch's commits, as `gh pr create --fill` writes it: one
// commit's body, or a list of the commits' subjects, of the commits the base lacks.
func (work *session) filled(branch, base string) string {
	remoteName, err := work.pushRemote(branch)
	if err != nil {
		return ""
	}
	out, err := work.git.Run(work.ctx, "log", "--reverse", "--format=%s%x1f%b%x1e", "refs/remotes/"+remoteName+"/"+base+"..refs/heads/"+branch)
	if err != nil {
		return ""
	}
	var subjects, bodies []string
	for _, record := range strings.Split(string(out), "\x1e") {
		record = strings.TrimLeft(record, "\n")
		if record == "" {
			continue
		}
		subject, commitBody, _ := strings.Cut(record, "\x1f")
		subjects, bodies = append(subjects, subject), append(bodies, strings.TrimSpace(commitBody))
	}
	if len(subjects) == 1 {
		return bodies[0]
	}
	var list []string
	for _, subject := range subjects {
		list = append(list, "- "+subject)
	}
	return strings.Join(list, "\n")
}

func (work *session) pushBranch(branch string, newUpstream bool) error {
	remoteName, err := work.pushRemote(branch)
	if err != nil {
		return err
	}
	args := []string{"push"}
	if newUpstream {
		args = append(args, "-u")
	}
	args = append(args, remoteName, "refs/heads/"+branch+":refs/heads/"+branch)
	if _, err := work.git.Run(work.ctx, args...); err != nil {
		return fmt.Errorf("the push of %s to %s: %w", branch, remoteName, err)
	}
	work.say("pushed %s", branch)
	return nil
}

// openPull opens the pull request. A write is not retried: an answer lost after it is settled by
// looking for the pull request again.
func (work *session) openPull(title, body, base, branch string) (githubapi.PullRequest, error) {
	created, err := work.github.CreatePullRequest(work.ctx, githubapi.NewPullRequest{Head: branch, Base: base, Title: title, Body: body})
	if err != nil {
		found, open, lookErr := work.openPullOf(branch)
		if lookErr != nil || !open {
			return githubapi.PullRequest{}, fmt.Errorf("the pull request could not be opened: %w", err)
		}
		created = found
	}
	work.say("opened #%d %s", created.Number, created.URL)
	return created, nil
}

// mergeable is the pull request once GitHub has computed whether it merges (UNKNOWN right after a
// push), or as it stands after the wait.
func (work *session) mergeable(number int) (githubapi.PullRequest, error) {
	deadline := work.deps.Clock.Now().Add(mergeableWait)
	for {
		pull, err := work.github.PullRequest(work.ctx, number)
		if err != nil {
			return githubapi.PullRequest{}, fmt.Errorf("pull request #%d: %w", number, err)
		}
		if pull.Mergeable != "UNKNOWN" {
			return pull, nil
		}
		more, err := work.sleepUntil(deadline, mergeableStep)
		if err != nil || !more {
			return pull, err
		}
	}
}

// updateBehind merges the base into a branch that is behind it, on GitHub, and waits for the new
// head to show (GitHub does it in the background).
func (work *session) updateBehind(number int, head string, options landOptions) (int, error) {
	work.say("#%d is BEHIND the base; updating the branch on GitHub", number)
	if err := work.github.UpdateBranch(work.ctx, number, head); err != nil {
		return 0, fmt.Errorf("the update of #%d's branch: %w", number, err)
	}
	deadline := work.deps.Clock.Now().Add(mergeableWait)
	for {
		pull, err := work.github.PullRequest(work.ctx, number)
		if err != nil {
			return 0, fmt.Errorf("pull request #%d: %w", number, err)
		}
		if pull.HeadSHA != head {
			break
		}
		more, err := work.sleepUntil(deadline, mergeableStep)
		if err != nil {
			return 0, err
		}
		if !more {
			work.say("#%d: the update of its branch has not shown on GitHub within %s; not merging", number, humanSeconds(mergeableWait))
			return exitPending, nil
		}
	}
	if options.wait {
		return work.ciWait(number, options.waiting)
	}
	return exitFine, nil
}

func (work *session) mergeAndDelete(number int, branch, base string, options landOptions) (int, error) {
	state, err := work.mergeable(number)
	if err != nil {
		return 0, err
	}
	switch state.Mergeable {
	case "MERGEABLE":
	case "UNKNOWN":
		work.say("#%d: GitHub has not said within %s whether it merges; not merging", number, humanSeconds(mergeableWait))
		return exitPending, nil
	default:
		return 0, failf(exitNotFine, "#%d is %s: resolve that first", number, state.Mergeable)
	}
	mergeState, err := work.github.MergeableState(work.ctx, number)
	if err != nil {
		return 0, fmt.Errorf("pull request #%d: %w", number, err)
	}
	if strings.EqualFold(mergeState, "behind") {
		code, err := work.updateBehind(number, state.HeadSHA, options)
		if err != nil || code != exitFine {
			return code, err
		}
	}
	viewed, err := work.github.PullRequest(work.ctx, number)
	if err != nil {
		return 0, fmt.Errorf("pull request #%d: %w", number, err)
	}
	landing := work.judged[number]
	if landing == "" {
		if options.wait {
			return 0, failf(exitNotFine, "#%d: no head was seen green; not merging", number)
		}
		landing = viewed.HeadSHA // with --no-wait, the head seen here
	}
	if err := work.nothingLeftBehind(number, branch, landing); err != nil {
		return 0, err
	}
	body := viewed.Body
	if work.cardSection() != nil {
		// The squash commit's message is the description that was judged, not whatever the pull
		// request holds a moment later.
		if body, err = work.judgeMerge(number, landing, branch); err != nil {
			return 0, err
		}
	}
	// GitHub refuses the merge if the head is no longer the commit whose checks passed, or, with
	// --no-wait, the one seen here.
	_, mergeErr := work.github.Merge(work.ctx, number, landing, "", body)
	// A merge queue or auto-merge may hold it, and an answer may be lost: asked, not assumed.
	landed, err := work.github.PullRequest(work.ctx, number)
	if err != nil {
		return 0, fmt.Errorf("whether #%d merged: %w", number, err)
	}
	if !landed.Merged {
		if mergeErr != nil {
			return 0, fmt.Errorf("GitHub did not merge #%d at %s: %w", number, short(landing, 10), mergeErr)
		}
		work.say("#%d is not merged yet (GitHub says %s: a merge queue or auto-merge holds it); the remote branch is kept, "+
			"and nothing here changed", number, landed.State)
		return exitPending, nil
	}
	work.say("merged #%d", number)
	work.deleteRemoteBranch(branch, viewed.Head, landing)
	work.say("this checkout is left as it is: still on %s. `jst tidy --fix --branch %s` removes the branch once nothing would "+
		"be lost — from a worktree, with the worktree, when no process works in it; in the main checkout, after you switch "+
		"away from it. The merge is on the remote's %s.", branch, branch, base)
	return exitFine, nil
}

// nothingLeftBehind refuses a merge when this checkout's branch has commits the pull request's
// head lacks: they would not land. A head that moved past the checkout (a branch update on
// GitHub) is fine.
func (work *session) nothingLeftBehind(number int, branch, landing string) error {
	local, err := work.gitLine("rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil || local == "" || local == landing {
		return nil
	}
	remoteName, err := work.pushRemote(branch)
	if err != nil {
		return err
	}
	if err := work.haveCommit(number, landing, remoteName, branch); err != nil {
		return err
	}
	if _, err := work.git.Run(work.ctx, "merge-base", "--is-ancestor", local, landing); err != nil {
		return failf(exitNotFine, "#%d: this checkout's %s is at %s and the pull request at %s: commits here would not land. "+
			"Bring them together first (push, or pull what was pushed elsewhere)", number, branch, short(local, 10), short(landing, 10))
	}
	return nil
}

// deleteRemoteBranch deletes the pull request's branch on the remote only while it is still at
// the head that was merged (a lease): a push made after the merge is not deleted. Only the
// remote's ref, and git's remote-tracking ref of it, change; a failure is said, since the merge is
// what was asked for.
func (work *session) deleteRemoteBranch(branch, headName, landing string) {
	remoteName, err := work.pushRemote(branch)
	if err != nil {
		work.say("the remote branch is not deleted: %v", err)
		return
	}
	ref := "refs/heads/" + headName
	_, err = work.git.Run(work.ctx, "push", "--quiet", "--force-with-lease="+ref+":"+landing, remoteName, ":"+ref)
	if err == nil {
		work.say("deleted %s/%s", remoteName, headName)
		return
	}
	// GitHub may have deleted it already ("Automatically delete head branches"): asked, not guessed.
	there, lsErr := work.git.Run(work.ctx, "ls-remote", "--heads", remoteName, ref)
	if lsErr == nil && strings.TrimSpace(string(there)) == "" {
		work.say("%s/%s is already deleted", remoteName, headName)
		return
	}
	reason := err.Error()
	if len(reason) > 300 {
		reason = reason[:300]
	}
	work.say("the remote branch %s/%s is not deleted: %s", remoteName, headName, reason)
}
