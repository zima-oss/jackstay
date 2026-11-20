// Package gh is GitHub for jackstay: the adapter behind `jst land` (hosting.Host, docs/design/land.md
// section 2), the GitHub side of `jst land status` and `jst land find`, and `jst checks`, waiting on
// a pull request's checks and a workflow's runs (docs/design/implementation.md section 8.1).
// GitHub is asked through internal/githubapi as the project's own account; nothing here changes
// the checkout it runs in (W-019).
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
	cli.Register(cli.Command{Name: "checks", Summary: "wait on a pull request's checks and a workflow's runs on GitHub", Usage: help, Run: Run})
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
  jst checks wait [PR|URL] [--timeout S] [--interval S] [--lines N] [--any-head]
  jst checks status [PR|URL]
  jst checks run-wait WORKFLOW [--ref BRANCH|SHA] [--repo URL] [--timeout S] [--interval S] [--appear S]
`

// GitHub is what this package asks of GitHub: internal/githubapi's client (through client, below) or a
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
	// Merge merges by method ("squash", "merge" or "rebase") while the head is headSHA; a write,
	// never retried.
	Merge(ctx context.Context, number int, method, headSHA, title, body string) (githubapi.MergeResult, error)
	// Enqueue puts a pull request (by its node id) in the merge queue at expectedHead; a write.
	Enqueue(ctx context.Context, pullRequestID, expectedHead string) error
	// WorkflowRun is one run by its id (one pushed out of the newest listed).
	WorkflowRun(ctx context.Context, runID int64) (githubapi.WorkflowRun, error)
	// MergeableState is REST's mergeable_state of a pull request: "behind", "clean", "blocked", …
	MergeableState(ctx context.Context, number int) (string, error)
	// UpdateBranch merges the base into the pull request's branch on GitHub, while its head is
	// headSHA; a write, never retried.
	UpdateBranch(ctx context.Context, number int, headSHA string) error
	// BranchRules are a branch's rules as a merge meets them: its rulesets' and its classic
	// protection's required checks, whether it must be up to date, whether it has a merge queue.
	BranchRules(ctx context.Context, branch string) (BranchRules, error)
	// MergeMethods are the merge methods the repository allows (read false: GitHub did not say).
	MergeMethods(ctx context.Context) (methods []string, read bool, err error)
	// BranchTip is the commit a branch is at on GitHub now.
	BranchTip(ctx context.Context, branch string) (string, error)
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

// Deps are what a call of these commands works with.
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

// The commands, each with its name in messages and flag sets.
const (
	checksName = "jst checks"
	statusName = "jst land status"
	findName   = "jst land find"
)

// Run is `jst checks` with the words after it.
func Run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	return withRealDeps(checksName, args, stdout, stderr, Main)
}

// StatusMain is `jst land status` with the words after it: where this branch and its pull request
// stand, and its checks once.
func StatusMain(args []string, stdout, stderr io.Writer) int {
	return withRealDeps(statusName, args, stdout, stderr, statusMain)
}

// FindMain is `jst land find` with the words after it: one pull request by head or search.
func FindMain(args []string, stdout, stderr io.Writer) int {
	return withRealDeps(findName, args, stdout, stderr, findMain)
}

func withRealDeps(name string, args []string, stdout, stderr io.Writer, main func([]string, Deps) int) int {
	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return exitRequest
	}
	return main(args, Deps{Stdout: stdout, Stderr: stderr, Dir: workDir,
		Git:     func(dir string) Git { return gitcli.Git{Dir: dir} },
		Connect: connectReal, Clock: realClock{}})
}

// Main is `jst checks` with its dependencies given.
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
		"wait": runCIWait, "status": runCIStatus, "run-wait": runRunWait,
	}
	command, found := commands[args[0]]
	if !found {
		fmt.Fprintf(deps.Stderr, "%s: no command %q\n%s", checksName, args[0], usage)
		return exitRequest
	}
	return runOne(checksName, args[0], command, args[1:], deps)
}

func statusMain(args []string, deps Deps) int {
	return runOne(statusName, "status", runLandStatus, args, deps)
}

func findMain(args []string, deps Deps) int {
	return runOne(findName, "find", runLandFind, args, deps)
}

// runOne runs one command in a new session; operation names it in telemetry.
func runOne(name, operation string, command func(*session, []string) (int, error), args []string, deps Deps) int {
	telemetry.Annotate(attribute.String(telemetry.AttrGitHubOperation, operation))
	work, err := newSession(deps, name)
	if err != nil {
		return report(deps.Stderr, name, err)
	}
	defer work.cancel()
	code, err := command(work, args)
	if err != nil {
		return report(deps.Stderr, name, err)
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

func report(stderr io.Writer, name string, err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitFine
	}
	fmt.Fprintf(stderr, "%s: %v\n", name, err)
	return codeOf(err)
}

// session is one call: the project, the checkout, and GitHub once connected.
type session struct {
	// name is the command's, in messages and flag sets ("jst checks").
	name     string
	deps     Deps
	ctx      context.Context
	cancel   context.CancelFunc
	git      Git
	config   config.Config
	settings config.GitHub
	github   GitHub
	// repository is the owner/name GitHub was connected for.
	repository string
}

// commandBound bounds a whole call, for the client's rate-limit waits; a wait's own deadline is
// the flag's, on the session's clock.
const commandBound = 6 * time.Hour

func newSession(deps Deps, name string) (*session, error) {
	_, loaded, err := config.Load(deps.Dir, []string{"github"})
	if err != nil {
		return nil, failf(exitRequest, "%v", err)
	}
	settings, _ := loaded.GitHub()
	ctx, cancel := context.WithTimeout(context.Background(), commandBound)
	return &session{name: name, deps: deps, ctx: ctx, cancel: cancel, git: deps.Git(deps.Dir), config: loaded,
		settings: settings}, nil
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
		fmt.Fprintf(work.deps.Stderr, "%s: %s\n", work.name, token.Note)
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
