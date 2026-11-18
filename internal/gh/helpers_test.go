package gh

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/githubapi/githubfake"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaC = "cccccccccccccccccccccccccccccccccccccccc"
)

// fakeClock moves only when slept on (W-013).
type fakeClock struct {
	mutex  sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)} }

func (clock *fakeClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

func (clock *fakeClock) Sleep(_ context.Context, wait time.Duration) error {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.sleeps = append(clock.sleeps, wait)
	clock.now = clock.now.Add(max(wait, time.Millisecond))
	return nil
}

func (clock *fakeClock) slept() time.Duration {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	var total time.Duration
	for _, wait := range clock.sleeps {
		total += wait
	}
	return total
}

// fakeGitHub is githubfake's fake with the three requests jst gh adds, and the state GitHub has
// after a merge or a branch update.
type fakeGitHub struct {
	*githubfake.Fake
	mutex       sync.Mutex
	runsByID    map[int64]githubapi.WorkflowRun
	mergeStates []string
	updates     []string
	// afterUpdate and afterMerge are the pull request once UpdateBranch or Merge was called.
	afterUpdate *githubapi.PullRequest
	afterMerge  *githubapi.PullRequest
	// onMerge runs inside Merge (what another session or GitHub does at that moment).
	onMerge func()
	// required and requiredErr are RequiredChecks' answer (default: no rule requires a check).
	required    []RequiredCheck
	requiredErr error
}

func newGitHub() *fakeGitHub {
	return &fakeGitHub{Fake: githubfake.New(), runsByID: map[int64]githubapi.WorkflowRun{}}
}

func (hub *fakeGitHub) WorkflowRun(_ context.Context, runID int64) (githubapi.WorkflowRun, error) {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	run, found := hub.runsByID[runID]
	if !found {
		return githubapi.WorkflowRun{}, &githubfake.NoAnswerError{Op: "WorkflowRun"}
	}
	return run, nil
}

func (hub *fakeGitHub) MergeableState(context.Context, int) (string, error) {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	if len(hub.mergeStates) == 0 {
		return "clean", nil
	}
	state := hub.mergeStates[0]
	if len(hub.mergeStates) > 1 {
		hub.mergeStates = hub.mergeStates[1:]
	}
	return state, nil
}

func (hub *fakeGitHub) RequiredChecks(context.Context, string) ([]RequiredCheck, error) {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	return hub.required, hub.requiredErr
}

func (hub *fakeGitHub) UpdateBranch(_ context.Context, _ int, headSHA string) error {
	hub.mutex.Lock()
	defer hub.mutex.Unlock()
	hub.updates = append(hub.updates, headSHA)
	return nil
}

func (hub *fakeGitHub) PullRequest(ctx context.Context, number int) (githubapi.PullRequest, error) {
	hub.mutex.Lock()
	afterMerge, afterUpdate, updated := hub.afterMerge, hub.afterUpdate, len(hub.updates) > 0
	hub.mutex.Unlock()
	if afterMerge != nil && len(hub.Merges()) > 0 {
		return *afterMerge, nil
	}
	if afterUpdate != nil && updated {
		return *afterUpdate, nil
	}
	return hub.Fake.PullRequest(ctx, number)
}

func (hub *fakeGitHub) Merge(ctx context.Context, number int, headSHA, title, body string) (githubapi.MergeResult, error) {
	result, err := hub.Fake.Merge(ctx, number, headSHA, title, body)
	if hub.onMerge != nil {
		hub.onMerge()
	}
	return result, err
}

func (hub *fakeGitHub) count(operation string) int {
	count := 0
	for _, call := range hub.Calls() {
		if call.Op == operation {
			count++
		}
	}
	return count
}

// scriptedGit answers git's questions from a script: each command (its words joined by spaces)
// answers in turn, the last repeating; an unscripted command fails as git would and is recorded.
type scriptedGit struct {
	mutex      sync.Mutex
	answers    map[string][]gitAnswer
	served     map[string]int
	calls      []string
	unscripted []string
}

type gitAnswer struct {
	out  string
	code int
}

func newGit() *scriptedGit {
	return &scriptedGit{answers: map[string][]gitAnswer{}, served: map[string]int{}}
}

func (git *scriptedGit) on(command string, answers ...gitAnswer) {
	git.mutex.Lock()
	defer git.mutex.Unlock()
	git.answers[command] = answers
	git.served[command] = 0
}

func ok(out string) gitAnswer { return gitAnswer{out: out} }

func exit(code int) gitAnswer { return gitAnswer{code: code} }

func (git *scriptedGit) Run(_ context.Context, args ...string) ([]byte, error) {
	git.mutex.Lock()
	defer git.mutex.Unlock()
	command := strings.Join(args, " ")
	git.calls = append(git.calls, command)
	sequence := git.answers[command]
	if len(sequence) == 0 {
		git.unscripted = append(git.unscripted, command)
		return nil, &gitcli.ExitError{Args: args, Code: 128, Stderr: "unscripted"}
	}
	answer := sequence[min(git.served[command], len(sequence)-1)]
	git.served[command]++
	if answer.code != 0 {
		return nil, &gitcli.ExitError{Args: args, Code: answer.code, Stderr: answer.out}
	}
	return []byte(answer.out), nil
}

func (git *scriptedGit) called(command string) bool {
	git.mutex.Lock()
	defer git.mutex.Unlock()
	for _, call := range git.calls {
		if call == command {
			return true
		}
	}
	return false
}

func (git *scriptedGit) callsStarting(prefix string) []string {
	git.mutex.Lock()
	defer git.mutex.Unlock()
	var found []string
	for _, call := range git.calls {
		if strings.HasPrefix(call, prefix) {
			found = append(found, call)
		}
	}
	return found
}

// checkout scripts a clean checkout on feat at tip, pushed to origin (github.com/owner/checkout).
func checkout(tip string) *scriptedGit {
	git := newGit()
	git.on("rev-parse --abbrev-ref HEAD", ok("feat\n"))
	git.on("rev-parse --show-toplevel", ok("/repo\n"))
	git.on("status --porcelain", ok(""))
	git.on("remote", ok("origin\n"))
	git.on("config --get remote.origin.url", ok("https://github.com/owner/checkout.git\n"))
	git.on("config --get branch.feat.remote", ok("origin\n"))
	git.on("rev-list --count origin/feat..feat", ok("0\n"))
	git.on("rev-parse --verify --quiet refs/heads/feat", ok(tip+"\n"))
	git.on("push --quiet --force-with-lease=refs/heads/feat:"+tip+" origin :refs/heads/feat", ok(""))
	return git
}

// harness is one test's GitHub, git, clock and project folder.
type harness struct {
	t          *testing.T
	hub        *fakeGitHub
	git        Git
	clock      *fakeClock
	dir        string
	connected  []string
	note       string
	connectErr error
}

func newHarness(t *testing.T, git Git) *harness {
	t.Helper()
	return &harness{t: t, hub: newGitHub(), git: git, clock: newClock(), dir: t.TempDir()}
}

// run is `jst gh ARGS`: its exit code, standard output and standard error.
func (bench *harness) run(args ...string) (int, string, string) {
	bench.t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(args, Deps{Stdout: &stdout, Stderr: &stderr, Dir: bench.dir,
		Git: func(string) Git { return bench.git },
		Connect: func(_ context.Context, _, repository string) (GitHub, githubapi.Token, error) {
			bench.connected = append(bench.connected, repository)
			if bench.connectErr != nil {
				return nil, githubapi.Token{}, bench.connectErr
			}
			return bench.hub, githubapi.Token{Value: "t", Account: "someone", Note: bench.note}, nil
		},
		Clock: bench.clock})
	return code, stdout.String(), stderr.String()
}

func (bench *harness) expect(code int, args ...string) (string, string) {
	bench.t.Helper()
	got, stdout, stderr := bench.run(args...)
	if got != code {
		bench.t.Fatalf("jst gh %s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), got, code, stdout, stderr)
	}
	return stdout, stderr
}

func pull(number int, head, sha string) githubapi.PullRequest {
	return githubapi.PullRequest{Number: number, State: "open", Title: "t", Head: head, HeadSHA: sha,
		HeadRepository: "owner/checkout", Base: "main", BaseRepository: "owner/checkout", Mergeable: "MERGEABLE",
		URL: "https://github.com/owner/checkout/pull/7", Body: "What it does."}
}

func merged(number int, head, sha string) *githubapi.PullRequest {
	landed := pull(number, head, sha)
	landed.State, landed.Merged = "closed", true
	return &landed
}

func checkRun(name string, status, conclusion string, runID int64) githubapi.Check {
	return githubapi.Check{Name: name, Kind: githubapi.CheckRun, Status: status, Conclusion: conclusion, Workflow: "ci", WorkflowID: 11,
		RunID: runID, Event: "pull_request", Branch: "feat", AppID: 15368, Link: "https://github.com/owner/checkout/actions/runs/1/job/1"}
}

func passed(name string) githubapi.Check  { return checkRun(name, "COMPLETED", "SUCCESS", 0) }
func pending(name string) githubapi.Check { return checkRun(name, "IN_PROGRESS", "", 0) }
func failed(name string, runID int64) githubapi.Check {
	return checkRun(name, "COMPLETED", "FAILURE", runID)
}

func contains(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("missing %q in:\n%s", want, text)
	}
}

func lacks(t *testing.T, text, unwanted string) {
	t.Helper()
	if strings.Contains(text, unwanted) {
		t.Fatalf("unexpected %q in:\n%s", unwanted, text)
	}
}

var errBadCredentials = errors.New("GET https://api.github.com/graphql: 401 Bad credentials")
