package land

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/hosting"
	"github.com/zima-oss/jackstay/internal/remote"
)

func (work *session) currentBranch() (string, error) {
	branch, err := work.gitLine("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", failf(exitInvalid, "this is not a checkout with a branch (%v)", err)
	}
	if branch == "HEAD" {
		return "", failf(exitInvalid, "this checkout is on no branch (a detached HEAD)")
	}
	return branch, nil
}

func (work *session) clean() (bool, error) {
	status, err := work.gitLine("status", "--porcelain")
	if err != nil {
		return false, failf(exitInvalid, "the work tree's state cannot be read: %v", err)
	}
	return status == "", nil
}

// pushRemote is the remote the branch's work goes to: its own upstream remote, else the rule
// sync, start and tidy share (github.remote, origin, the remote main tracks, the only one).
func (work *session) pushRemote() (string, error) {
	listed, err := work.git.Run(work.ctx, "remote")
	if err != nil {
		return "", failf(exitInvalid, "the remotes cannot be listed: %v", err)
	}
	tracked, err := work.gitLine("config", "--get", "branch."+work.branch+".remote")
	if err == nil && slices.Contains(strings.Fields(string(listed)), tracked) {
		return tracked, nil
	}
	name, err := remote.Name(work.ctx, work.git, work.settings.Remote)
	if err != nil {
		return "", failf(exitInvalid, "%v", err)
	}
	return name, nil
}

// verifyPushRemote checks every destination git will push to, including pushurl and URL
// rewrites. The URL itself stays out of the error, since it can hold a credential.
func (work *session) verifyPushRemote(remoteName string) error {
	addresses, err := work.git.Run(work.ctx, "remote", "get-url", "--push", "--all", "--", remoteName)
	if err != nil {
		return failf(exitInvalid, "cannot verify the push destinations of remote %q for %s", remoteName, work.repository)
	}
	for index, address := range strings.Split(strings.TrimSuffix(string(addresses), "\n"), "\n") {
		repository, err := remote.ParseURL(address)
		if err != nil {
			return failf(exitInvalid, "cannot verify push destination %d of remote %q as %s", index+1, remoteName, work.repository)
		}
		if !strings.EqualFold(repository.String(), work.repository) {
			return failf(exitInvalid, "push destination %d of remote %q names %s, but this change is in %s",
				index+1, remoteName, repository, work.repository)
		}
	}
	return nil
}

// ahead is how many commits of the branch its remote copy lacks; -1 when there is no remote copy.
func (work *session) ahead() int {
	count, err := work.gitLine("rev-list", "--count", "refs/remotes/"+work.remoteName+"/"+work.branch+"..refs/heads/"+work.branch)
	if err != nil {
		return -1
	}
	number, err := strconv.Atoi(count)
	if err != nil {
		return -1
	}
	return number
}

func (work *session) push(newUpstream bool) error {
	if err := work.verifyPushRemote(work.remoteName); err != nil {
		return err
	}
	args := []string{"push"}
	if newUpstream {
		args = append(args, "-u")
	}
	args = append(args, "--", work.remoteName, "refs/heads/"+work.branch+":refs/heads/"+work.branch)
	if _, err := work.git.Run(work.ctx, args...); err != nil {
		return failf(exitRefused, "the push of %s to %s: %v", work.branch, work.remoteName, err)
	}
	work.say("pushed %s", work.branch)
	return nil
}

// open opens the change request. A write is not retried: an answer lost after it is settled by
// looking for the change again.
func (work *session) open(body string) (hosting.Change, error) {
	created, err := work.host.Open(work.ctx, hosting.NewChange{Head: work.branch, Base: work.base, Title: work.options.title, Body: body})
	if err != nil {
		found, open, lookErr := work.host.Change(work.ctx, hosting.ChangeRef{Head: work.branch})
		if lookErr != nil || !open || found.Base != work.base {
			return hosting.Change{}, fmt.Errorf("the change request could not be opened: %w", err)
		}
		created = found
	}
	work.say("opened #%d %s", created.Number, created.URL)
	return created, nil
}

// haveCommit makes sure the checkout has the commit (the head may have been made elsewhere: an
// update of the branch on the host, another checkout): fetched by the change's ref, then the
// branch.
func (work *session) haveCommit(number int, commit string) error {
	present := func() bool {
		_, err := work.git.Run(work.ctx, "cat-file", "-e", commit+"^{commit}")
		return err == nil
	}
	if present() {
		return nil
	}
	_, _ = work.git.Run(work.ctx, "fetch", "--quiet", "--", work.remoteName, fmt.Sprintf("refs/pull/%d/head", number))
	if present() {
		return nil
	}
	_, _ = work.git.Run(work.ctx, "fetch", "--quiet", "--", work.remoteName, "refs/heads/"+work.branch)
	if present() {
		return nil
	}
	return failf(exitRefused, "#%d: the commit %s cannot be fetched from %s", number, short(commit, 10), work.remoteName)
}

// haveBase fetches the change's base into its remote-tracking ref and makes sure the base commit
// the host names is here.
func (work *session) haveBase(change hosting.Change) error {
	if _, err := work.git.Run(work.ctx, "fetch", "--quiet", "--", work.remoteName,
		"refs/heads/"+change.Base+":refs/remotes/"+work.remoteName+"/"+change.Base); err != nil {
		return failf(exitRefused, "#%d: %s cannot be fetched from %s: %v", change.Number, change.Base, work.remoteName, err)
	}
	if _, err := work.git.Run(work.ctx, "cat-file", "-e", change.BaseSHA+"^{commit}"); err != nil {
		return failf(exitMoved, "#%d: the host's base commit %s is not on %s/%s as just fetched; run it again",
			change.Number, short(change.BaseSHA, 10), work.remoteName, change.Base)
	}
	return nil
}

// newDescription is the new change request's description: --body-file's, with the --card line
// added; without either, one from the branch's commits. Its Card line (where the base's file puts
// it in the description) and its changelog line (where the base has `changes`) are judged before
// anything is pushed, on the merge with the base's tip.
// --card is refused for an open change.
func (work *session) newDescription(open bool, line string, baseFile []byte, baseTip string) (string, error) {
	cards := cardsSectionOf(baseFile)
	if open {
		if line != "" {
			return "", failf(exitInvalid, "--card is for the description of a new change request; this one is open already: "+
				"edit its description on the host (it is judged again before the merge)")
		}
		return "", nil
	}
	body, given := "", false
	if work.options.bodyFile != "" {
		data, err := os.ReadFile(work.options.bodyFile)
		if err != nil {
			return "", failf(exitInvalid, "cannot read %s: %v", work.options.bodyFile, err)
		}
		body, given = string(data), true
	}
	if line != "" {
		body, given = withLine(body, line), true
	}
	if cards != nil && !cardExempt(cards, work.branch) {
		if !given {
			return "", failf(exitRefused, "the base asks for a `Card:` line in the description: pass --card ID "+
				"(or --card none --reason TEXT), or a --body-file that has one")
		}
		if err := work.judgeDescription(cards, baseTip, "refs/heads/"+work.branch, body); err != nil {
			return "", err
		}
	}
	if !given {
		body = work.filled()
	}
	if err := work.judgeChangesLine(baseFile, baseTip, "refs/heads/"+work.branch, body); err != nil {
		return "", err
	}
	return body, nil
}

// filled is a description from the branch's commits: one commit's body, or a list of the
// commits' subjects, of the commits the base lacks.
func (work *session) filled() string {
	remoteName, err := work.pushRemote()
	if err != nil {
		return ""
	}
	out, err := work.git.Run(work.ctx, "log", "--reverse", "--format=%s%x1f%b%x1e",
		"refs/remotes/"+remoteName+"/"+work.base+"..refs/heads/"+work.branch)
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

// nothingLeftBehind refuses a merge when this checkout's branch has commits the change's head
// lacks: they would not land. A head that moved past the checkout (an update on the host) is fine.
func (work *session) nothingLeftBehind(number int, landing string) error {
	local, err := work.gitLine("rev-parse", "--verify", "--quiet", "refs/heads/"+work.branch)
	if err != nil || local == "" || local == landing {
		return nil
	}
	if err := work.haveCommit(number, landing); err != nil {
		return err
	}
	if _, err := work.git.Run(work.ctx, "merge-base", "--is-ancestor", local, landing); err != nil {
		return failf(exitRefused, "#%d: this checkout's %s is at %s and the change at %s: commits here would not land. "+
			"Bring them together first (push, or merge what was pushed elsewhere)", number, work.branch, short(local, 10), short(landing, 10))
	}
	return nil
}

// deleteRemoteBranch is step 7: the change's branch deleted on the remote only while it is still
// at the head that was merged (a lease), no other open change uses it, and every push destination
// is this repository. A failure is said, since the merge is what was asked for.
func (work *session) deleteRemoteBranch(landed hosting.Change, landing string) {
	if landed.Head != work.branch || !strings.EqualFold(landed.HeadRepository, work.repository) || landed.HeadSHA != landing {
		work.say("the remote branch is not deleted: the host did not confirm that %s's %s merged at %s",
			work.repository, work.branch, short(landing, 10))
		return
	}
	open, err := work.host.OpenChanges(work.ctx, work.branch)
	if err != nil {
		work.say("the remote branch is not deleted: the open changes from %s cannot be confirmed", work.branch)
		return
	}
	for _, sibling := range open {
		if sibling.Number != landed.Number {
			work.say("the remote branch is not deleted: change #%d into %s is still open", sibling.Number, sibling.Base)
			return
		}
	}
	if err := work.verifyPushRemote(work.remoteName); err != nil {
		work.say("the remote branch is not deleted: %v", err)
		return
	}
	ref := "refs/heads/" + work.branch
	_, err = work.git.Run(work.ctx, "push", "--quiet", "--force-with-lease="+ref+":"+landing, "--", work.remoteName, ":"+ref)
	if err == nil {
		work.say("deleted %s/%s", work.remoteName, work.branch)
		return
	}
	// The host may have deleted it already: asked, not guessed. ls-remote reads from the fetch
	// URL, which can point to a different repository.
	fetchURL, fetchErr := work.gitLine("remote", "get-url", "--", work.remoteName)
	fetchRepository, parseErr := remote.ParseURL(fetchURL)
	if fetchErr == nil && parseErr == nil && strings.EqualFold(fetchRepository.String(), work.repository) {
		there, lsErr := work.git.Run(work.ctx, "ls-remote", "--heads", "--", work.remoteName, ref)
		if lsErr == nil && strings.TrimSpace(string(there)) == "" {
			work.say("%s/%s is already deleted", work.remoteName, work.branch)
			return
		}
	}
	reason := err.Error()
	if len(reason) > 300 {
		reason = reason[:300]
	}
	work.say("the remote branch %s/%s is not deleted: %s", work.remoteName, work.branch, reason)
}
