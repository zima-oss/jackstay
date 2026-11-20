package gh

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
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

// ParsePullURL is the repository (owner/name) and number a GitHub pull request URL names
// (github.com/OWNER/REPO/pull/N); anything else is an error.
func ParsePullURL(text string) (repository string, number int, err error) {
	parts := pullURL.FindStringSubmatch(strings.TrimSpace(text))
	if parts == nil {
		return "", 0, fmt.Errorf("%q is not a GitHub pull request URL (github.com/<owner>/<repo>/pull/<number>)", text)
	}
	number, err = strconv.Atoi(parts[2])
	if err != nil || number <= 0 {
		return "", 0, fmt.Errorf("%q names no pull request number", text)
	}
	return parts[1], number, nil
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
	pull, found, err := work.openPullOf(ref, "")
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, failf(exitNotFine, "no open pull request from %s in %s", ref, work.repository)
	}
	return pull.Number, nil
}

func openPulls(ctx context.Context, client GitHub, repository, branch string) ([]githubapi.PullRequest, error) {
	owner, _, _ := strings.Cut(repository, "/")
	pulls, err := client.PullRequestsByHead(ctx, owner+":"+branch, "open")
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(slices.Clone(pulls), func(pull githubapi.PullRequest) bool {
		return pull.Head != branch || (pull.HeadRepository != "" && !strings.EqualFold(pull.HeadRepository, repository))
	}), nil
}

// openPullOf resolves one open pull request in this repository. A base is supplied only when
// recovering a lost reply to creating a pull request for that base.
func (work *session) openPullOf(branch, base string) (githubapi.PullRequest, bool, error) {
	return openPullOf(work.ctx, work.github, work.repository, branch, base)
}

func openPullOf(ctx context.Context, client GitHub, repository, branch, base string) (githubapi.PullRequest, bool, error) {
	matching, err := openPulls(ctx, client, repository, branch)
	if err != nil {
		return githubapi.PullRequest{}, false, err
	}
	if base != "" {
		matching = slices.DeleteFunc(matching, func(pull githubapi.PullRequest) bool { return pull.Base != base })
	}
	switch len(matching) {
	case 0:
		return githubapi.PullRequest{}, false, nil
	case 1:
		return matching[0], true, nil
	default:
		slices.SortFunc(matching, func(first, second githubapi.PullRequest) int { return first.Number - second.Number })
		choices := make([]string, 0, len(matching))
		for _, pull := range matching {
			choices = append(choices, fmt.Sprintf("#%d into %s", pull.Number, pull.Base))
		}
		return githubapi.PullRequest{}, false, failf(exitRequest,
			"multiple open pull requests from %s: %s; select a pull request by number or URL (use --change with jst land or jst land status)",
			branch, strings.Join(choices, ", "))
	}
}

// pullForBranch handles the optional explicit selection in commands that operate on this checkout.
func (work *session) pullForBranch(branch, selected string) (githubapi.PullRequest, bool, error) {
	if selected == "" {
		return work.openPullOf(branch, "")
	}
	target, _, err := parsePullTarget(selected)
	if err != nil {
		return githubapi.PullRequest{}, false, err
	}
	number := target.number
	if number == 0 && digits.MatchString(target.ref) {
		number, err = strconv.Atoi(target.ref)
		if err != nil {
			number = 0
		}
	}
	if number <= 0 {
		return githubapi.PullRequest{}, false, failf(exitRequest, "--change takes a positive pull request number or its GitHub URL")
	}
	if target.repository != "" && !strings.EqualFold(target.repository, work.repository) {
		return githubapi.PullRequest{}, false, failf(exitRequest, "--change names %s, but this checkout is in %s", target.repository, work.repository)
	}
	pull, err := work.github.PullRequest(work.ctx, number)
	if err != nil {
		return githubapi.PullRequest{}, false, fmt.Errorf("pull request #%d: %w", number, err)
	}
	if pull.Number != number || pull.State != "open" || pull.Merged || pull.Base == "" || pull.Head != branch ||
		!strings.EqualFold(pull.HeadRepository, work.repository) || !strings.EqualFold(pull.BaseRepository, work.repository) {
		return githubapi.PullRequest{}, false, failf(exitRequest,
			"#%d is not an open pull request from %s in %s", number, branch, work.repository)
	}
	return pull, true, nil
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

// commandFlags is a flag set named name that writes to standard error and returns its errors.
func commandFlags(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
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
