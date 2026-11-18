package gh

import (
	"errors"
	"flag"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/remote"
)

var (
	// pullURL is a pull request's URL: it names its repository, and that repository wins over the
	// checkout's.
	pullURL = regexp.MustCompile(`(?i)^(?:https?://)?(?:www\.)?github\.com/([^/\s]+/[^/\s]+)/pull/(\d+)(?:[/?#].*)?$`)
	// urlLike is anything that looks like a URL: it must parse or be refused, since falling back to
	// the checkout is the wrong-repository report this exists to stop. Not `/pull/` alone:
	// `feat/pull/x` is a valid branch name.
	urlLike = regexp.MustCompile(`(?i)://|^(?:www\.)?github\.com/`)
	// repositoryURL is the plain repository URL --repo takes.
	repositoryURL = regexp.MustCompile(`^https://github\.com/([\w.-]+/[\w.-]+?)(?:\.git)?/?$`)
	digits        = regexp.MustCompile(`^[0-9]+$`)
)

// pullTarget is the pull request asked about: a URL's repository and number, or a number or a
// branch of the checkout's repository ("" ref: this branch).
type pullTarget struct {
	repository string
	number     int
	ref        string
}

func parsePullTarget(text string) (pullTarget, bool, error) {
	text = strings.TrimSpace(text)
	if parts := pullURL.FindStringSubmatch(text); parts != nil {
		number, err := strconv.Atoi(parts[2])
		if err != nil || number <= 0 {
			return pullTarget{}, false, failf(exitRequest, "%q names no pull request number", text)
		}
		return pullTarget{repository: parts[1], number: number}, true, nil
	}
	if urlLike.MatchString(text) {
		return pullTarget{}, false, failf(exitRequest, "'%s' looks like a pull request URL but is not one this tool can read "+
			"(expected github.com/<owner>/<repo>/pull/<number>). Refusing rather than guessing a repository.", text)
	}
	return pullTarget{ref: text}, false, nil
}

// repositoryFromURL is owner/name of a plain https://github.com/<owner>/<repo> URL; another URL
// is refused, not guessed.
func repositoryFromURL(text string) (string, error) {
	parts := repositoryURL.FindStringSubmatch(strings.TrimSpace(text))
	if parts == nil {
		return "", failf(exitRequest, "'%s' is not a https://github.com/<owner>/<repo> URL; refusing to guess", text)
	}
	return parts[1], nil
}

// checkoutRepository is the repository the checkout's remote names (or github.repository).
func (work *session) checkoutRepository() (string, error) {
	repository, err := remote.RepositoryOf(work.ctx, work.git, work.config.Section("github"))
	switch {
	case errors.Is(err, remote.ErrNoRemote):
		return "", failf(exitRequest, "%s has no git remote, so there is nothing to ask GitHub about. "+
			"This command needs a GitHub repository (or a URL that names one).", work.deps.Dir)
	case err != nil:
		return "", failf(exitRequest, "which GitHub repository this is cannot be told: %v", err)
	}
	return repository.String(), nil
}

// resolvePull connects to the target's repository and names the pull request by its number: a
// branch is its open pull request in this repository (one from a fork is not it).
func (work *session) resolvePull(target pullTarget) (int, error) {
	repository := target.repository
	if repository == "" {
		found, err := work.checkoutRepository()
		if err != nil {
			return 0, err
		}
		repository = found
	}
	if err := work.connect(repository); err != nil {
		return 0, err
	}
	if target.number > 0 {
		return target.number, nil
	}
	ref := target.ref
	if ref == "" {
		branch, err := work.currentBranch()
		if err != nil {
			return 0, err
		}
		ref = branch
	}
	if digits.MatchString(ref) {
		number, err := strconv.Atoi(ref)
		if err == nil && number > 0 {
			return number, nil
		}
	}
	pull, found, err := work.openPullOf(ref)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, failf(exitNotFine, "no open pull request from %s in %s", ref, work.repository)
	}
	return pull.Number, nil
}

// openPullOf is this repository's open pull request from branch.
func (work *session) openPullOf(branch string) (githubapi.PullRequest, bool, error) {
	owner, _, _ := strings.Cut(work.repository, "/")
	pulls, err := work.github.PullRequestsByHead(work.ctx, owner+":"+branch, "open")
	if err != nil {
		return githubapi.PullRequest{}, false, err
	}
	for _, pull := range pulls {
		if pull.Head == branch && (pull.HeadRepository == "" || strings.EqualFold(pull.HeadRepository, work.repository)) {
			return pull, true, nil
		}
	}
	return githubapi.PullRequest{}, false, nil
}

func (work *session) currentBranch() (string, error) {
	branch, err := work.gitLine("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", failf(exitRequest, "this is not a checkout with a branch (%v); name the pull request", err)
	}
	if branch == "HEAD" {
		return "", failf(exitRequest, "this checkout is on no branch (a detached HEAD); name the pull request")
	}
	return branch, nil
}

// --- flags -------------------------------------------------------------------------------------

// commandFlags is a flag set that writes to standard error and returns its errors.
func commandFlags(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet("jst gh "+name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

// parseFlags parses flags written before and after the positional words (as argparse does); a
// bad flag is exit 2, --help exit 0.
func parseFlags(flags *flag.FlagSet, args []string, most int) ([]string, error) {
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, failf(exitRequest, "%v", err)
		}
		rest := flags.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	if len(positional) > most {
		return nil, failf(exitRequest, "%s takes at most %d word(s) besides its flags: %s", flags.Name(), most, strings.Join(positional, " "))
	}
	return positional, nil
}

// duration is seconds as a duration; a negative, infinite or NaN value is a bad request.
func duration(name string, value float64, positive bool) (time.Duration, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || (positive && value == 0) {
		return 0, failf(exitRequest, "--%s must be a number of seconds%s", name, map[bool]string{true: " above 0", false: ""}[positive])
	}
	return time.Duration(value * float64(time.Second)), nil
}

// waitDefaults are github.wait's numbers, else the defaults.
func (work *session) waitDefaults() (timeout, interval float64, lines int) {
	timeout, interval, lines = 1800, 20, 25
	if work.settings.Wait.TimeoutSeconds > 0 {
		timeout = work.settings.Wait.TimeoutSeconds
	}
	if work.settings.Wait.IntervalSeconds > 0 {
		interval = work.settings.Wait.IntervalSeconds
	}
	if work.settings.Wait.LogLines > 0 {
		lines = work.settings.Wait.LogLines
	}
	return timeout, interval, lines
}

// listFlag is a repeatable flag.
type listFlag []string

func (list *listFlag) String() string { return strings.Join(*list, ",") }

func (list *listFlag) Set(value string) error {
	*list = append(*list, value)
	return nil
}
