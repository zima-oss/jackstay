package gh

import (
	"slices"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/remote"
)

// runLandStatus is `jst land status`: where this branch and its pull request stand, then the pull
// request's checks once (its exit code is theirs).
func runLandStatus(work *session, args []string) (int, error) {
	flags := commandFlags(work.name, work.deps.Stderr)
	selected := flags.String("change", "", "an open pull request from this branch, by number or URL")
	if _, err := parseFlags(flags, args, 0); err != nil {
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
	pull, found, err := work.pullForBranch(branch, *selected)
	if err != nil {
		return 0, err
	}
	base := pull.Base
	if !found || base == "" {
		base, err = work.github.DefaultBranch(work.ctx)
		if err != nil {
			base = "unknown: " + err.Error() // read-only: said, not a stop
		}
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
