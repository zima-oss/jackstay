// Package gh is `jst gh`: waiting on GitHub and landing a pull request, correctly, once
// (docs/design/implementation.md sections 8.1 and 8.3; the Card line of docs/design/implementation.md section 4.1). GitHub is asked through internal/githubapi as the
// project's own account; git pushes, fetches and deletes the remote branch, and nothing here
// changes the checkout it runs in (W-019).
//
// Exit codes are a contract, because these run inside loops and gates: 0 the thing asked about is
// fine; 1 it is not, the reason printed; 2 the request was wrong; 3 still pending at the deadline;
// 4 the pull request's head moved while waiting; 5 an external format is not what jackstay assumes.
package gh

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/telemetry"
)

func init() {
	cli.Register(cli.Command{Name: "gh", Summary: "wait on GitHub's checks and runs, find and land pull requests", Usage: help, Run: Run})
}

// The exit codes of the contract.
const (
	exitFine    = 0
	exitNotFine = 1
	exitRequest = 2
	exitPending = 3
	exitMoved   = 4
	exitFormat  = 5
)

const usage = `usage:
  jst gh ci-wait [PR|URL] [--timeout S] [--interval S] [--lines N] [--any-head]
  jst gh ci-status [PR|URL]
  jst gh run-wait WORKFLOW [--ref BRANCH|SHA] [--repo URL] [--timeout S] [--interval S] [--appear S]
  jst gh pr-find [--head BRANCH] [--search TEXT] [--repo URL] [--wait S] [--state open|closed|merged|all]
  jst gh pr-status [--pr PR|URL]
  jst gh pr-land [--pr PR|URL] [--title T] [--body-file F] [--card ID]... [--reason TEXT] [--no-push] [--no-create]
                [--no-wait] [--no-merge] [--dry-run] [--timeout S] [--interval S] [--lines N]
`

// GitHub is what jst gh asks of GitHub: internal/githubapi's client (through client, below) or a
// fake in tests.
type GitHub interface {
	PullRequestsByHead(ctx context.Context, head, state string) ([]githubapi.PullRequest, error)
	PullRequest(ctx context.Context, number int) (githubapi.PullRequest, error)
	SearchPullRequestsBySHA(ctx context.Context, text string) ([]githubapi.PullRequest, error)
	Checks(ctx context.Context, sha string) ([]githubapi.Check, error)
	WorkflowRuns(ctx context.Context, filter githubapi.RunFilter) ([]githubapi.WorkflowRun, error)
	Jobs(ctx context.Context, runID int64) ([]githubapi.Job, error)
	JobLog(ctx context.Context, jobID int64) (string, error)
	DefaultBranch(ctx context.Context) (string, error)
	CreatePullRequest(ctx context.Context, request githubapi.NewPullRequest) (githubapi.PullRequest, error)
	Merge(ctx context.Context, number int, headSHA, title, body string) (githubapi.MergeResult, error)
	// WorkflowRun is one run by its id (one pushed out of the newest listed).
	WorkflowRun(ctx context.Context, runID int64) (githubapi.WorkflowRun, error)
	// MergeableState is REST's mergeable_state of a pull request: "behind", "clean", "blocked", …
	MergeableState(ctx context.Context, number int) (string, error)
	// UpdateBranch merges the base into the pull request's branch on GitHub, while its head is
	// headSHA; a write, never retried.
	UpdateBranch(ctx context.Context, number int, headSHA string) error
	// RequiredChecks are the checks a branch's rules require before a merge: its rulesets' and its
	// classic protection's; none is an empty list.
	RequiredChecks(ctx context.Context, branch string) ([]RequiredCheck, error)
}

// Git asks git a question in the checkout (internal/gitcli.Git is one).
type Git interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// Clock is the time waits are measured on; tests pass their own (W-013).
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, wait time.Duration) error
}

// Deps are what a call of jst gh works with.
type Deps struct {
	Stdout, Stderr io.Writer
	// Dir is the working folder: its jackstay.json and its checkout.
	Dir string
	// Git is git in a folder.
	Git func(dir string) Git
	// Connect is GitHub for repository (owner/name) as account (github.account; "" when unset).
	Connect func(ctx context.Context, account, repository string) (GitHub, githubapi.Token, error)
	Clock   Clock
}

// Run is `jst gh` with the words after it.
func Run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "jst gh: %v\n", err)
		return exitRequest
	}
	return Main(args, Deps{Stdout: stdout, Stderr: stderr, Dir: workDir,
		Git:     func(dir string) Git { return gitcli.Git{Dir: dir} },
		Connect: connectReal, Clock: realClock{}})
}

// Main is `jst gh` with its dependencies given.
func Main(args []string, deps Deps) int {
	if len(args) == 0 {
		fmt.Fprint(deps.Stderr, usage)
		return exitRequest
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(deps.Stdout, usage)
		return exitFine
	}
	commands := map[string]func(*session, []string) (int, error){
		"ci-wait": runCIWait, "ci-status": runCIStatus, "run-wait": runRunWait,
		"pr-find": runPRFind, "pr-status": runPRStatus, "pr-land": runPRLand,
	}
	command, found := commands[args[0]]
	if !found {
		fmt.Fprintf(deps.Stderr, "jst gh: no command %q\n%s", args[0], usage)
		return exitRequest
	}
	telemetry.Annotate(attribute.String(telemetry.AttrGitHubOperation, args[0]))
	// Only pr-land reads the cards section: a broken one stops no other command.
	need := []string{"github"}
	if args[0] == "pr-land" {
		need = append(need, "cards")
	}
	work, err := newSession(deps, need)
	if err != nil {
		return report(deps.Stderr, err)
	}
	defer work.cancel()
	code, err := command(work, args[1:])
	if err != nil {
		return report(deps.Stderr, err)
	}
	return code
}

// exitError is a refusal with the exit code it means.
type exitError struct {
	code    int
	message string
}

func (failure *exitError) Error() string { return failure.message }

func failf(code int, format string, values ...any) error {
	return &exitError{code: code, message: fmt.Sprintf(format, values...)}
}

// codeOf is the exit code an error means: its own, a typed error's Code(), 5 for JSON jackstay could not
// read, else 1.
func codeOf(err error) int {
	var failure *exitError
	if errors.As(err, &failure) {
		return failure.code
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code()
	}
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &typeErr) {
		return exitFormat
	}
	return exitNotFine
}

func report(stderr io.Writer, err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitFine
	}
	fmt.Fprintf(stderr, "jst gh: %v\n", err)
	return codeOf(err)
}

// session is one call: the project, the checkout, GitHub once connected, and the heads seen green.
type session struct {
	deps     Deps
	ctx      context.Context
	cancel   context.CancelFunc
	git      Git
	config   config.Config
	settings config.GitHub
	github   GitHub
	// repository is the owner/name GitHub was connected for.
	repository string
	// judged binds each green verdict to the pull request and commits pr-land may merge.
	judged map[int]checkEvidence
	// rulesOf is the base whose rules pr-land read for each pull request: one retargeted since stops.
	rulesOf map[int]string
}

// commandBound bounds a whole call, for the client's rate-limit waits; a wait's own deadline is
// the flag's, on the session's clock.
const commandBound = 6 * time.Hour

func newSession(deps Deps, need []string) (*session, error) {
	_, loaded, err := config.Load(deps.Dir, need)
	if err != nil {
		return nil, failf(exitRequest, "%v", err)
	}
	settings, _ := loaded.GitHub()
	ctx, cancel := context.WithTimeout(context.Background(), commandBound)
	return &session{deps: deps, ctx: ctx, cancel: cancel, git: deps.Git(deps.Dir), config: loaded,
		settings: settings, judged: map[int]checkEvidence{}, rulesOf: map[int]string{}}, nil
}

func (work *session) say(format string, values ...any) {
	fmt.Fprintf(work.deps.Stdout, format+"\n", values...)
}

// connect asks for GitHub for repository, once; the token's note is said on standard error.
func (work *session) connect(repository string) error {
	if work.github != nil && work.repository == repository {
		return nil
	}
	client, token, err := work.deps.Connect(work.ctx, work.settings.Account, repository)
	if err != nil {
		return failf(exitRequest, "GitHub cannot be asked: %v", err)
	}
	if token.Note != "" {
		fmt.Fprintf(work.deps.Stderr, "jst gh: %s\n", token.Note)
	}
	work.github, work.repository = client, repository
	return nil
}

// notePull records the remote scope of a known pull request for local correlation.
func (work *session) notePull(number int, head string) {
	if number > 0 && work.repository != "" {
		telemetry.Annotate(attribute.String(telemetry.AttrGitHubRepository, work.repository),
			attribute.Int(telemetry.AttrGitHubPull, number))
	}
	if head != "" {
		telemetry.Annotate(attribute.String(telemetry.AttrHead, head))
	}
}

// gitLine is git's answer, trimmed.
func (work *session) gitLine(args ...string) (string, error) {
	out, err := work.git.Run(work.ctx, args...)
	return strings.TrimSpace(string(out)), err
}

// sleepUntil sleeps step, or less up to the deadline; false when the deadline has passed.
func (work *session) sleepUntil(deadline time.Time, step time.Duration) (bool, error) {
	now := work.deps.Clock.Now()
	if !now.Before(deadline) {
		return false, nil
	}
	return true, work.deps.Clock.Sleep(work.ctx, min(step, deadline.Sub(now)))
}

// humanSeconds is a duration as 45s or 3m07s.
func humanSeconds(span time.Duration) string {
	seconds := int(span / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

func short(sha string, length int) string {
	if len(sha) > length {
		return sha[:length]
	}
	return sha
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
