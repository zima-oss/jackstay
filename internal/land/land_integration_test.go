//go:build integration

package land

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/hosting"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

const (
	greenSettings = `{"jackstay_schema": 3, "github": {"account": "someone", "repository": "owner/checkout"},
 "land": {"autonomy": "merge-when-green"}}`
	prepareSettings = `{"jackstay_schema": 3, "github": {"account": "someone", "repository": "owner/checkout"},
 "land": {"autonomy": "prepare"}}`
	noLandSettings = `{"jackstay_schema": 3, "github": {"account": "someone", "repository": "owner/checkout"}}`
	deploySettings = `{"jackstay_schema": 3, "github": {"account": "someone", "repository": "owner/checkout"},
 "land": {"autonomy": "merge-when-green", "human_paths": ["deploy/**"]}}`
	listedSettings = `{"jackstay_schema": 3, "github": {"account": "someone", "repository": "owner/checkout"},
 "land": {"autonomy": "merge-when-green", "checks": ["test"]}}`
)

// requiresTest are rules that require the check "test" and allow every merge method.
var requiresTest = hosting.Rules{ChecksRead: true, Required: required("test"), UpToDateRead: true, QueueRead: true,
	MethodsRead: true, Methods: []hosting.Method{hosting.Squash, hosting.Merge, hosting.Rebase}}

// landRepo is a checkout on feat, one commit ahead of main, with a bare origin holding main and a
// local SSH transport that answers for git@github.com:owner/checkout.git without the network.
type landRepo struct {
	*testrepo.Repo
	origin string
	host   *fakeHost
	clock  *fakeClock
	// terminal is what Deps.Terminal answers; stdin is what the person types.
	terminal bool
	stdin    string
}

// newLandRepo makes main from settings and baseFiles, pushes it, and commits featFiles on feat
// (b.txt when none are given). The host's change #7 is feat into main at those commits.
func newLandRepo(t *testing.T, settings string, baseFiles, featFiles map[string]string) *landRepo {
	t.Helper()
	all := map[string]string{"jackstay.json": settings, "a.txt": "one\n"}
	for name, text := range baseFiles {
		all[name] = text
	}
	repo := testrepo.New(t, all)
	origin := filepath.Join(filepath.Dir(repo.Root), "origin.git")
	repo.Git("init", "-q", "--bare", "-b", "main", origin)
	transport := filepath.Join(filepath.Dir(repo.Root), "local-ssh")
	const script = `#!/bin/sh
while [ "$#" -gt 2 ]; do shift; done
[ "$1" = git@github.com ] || exit 2
case "$2" in
	"git-upload-pack 'owner/checkout.git'") exec git upload-pack "$JST_TEST_BARE_REPO" ;;
	"git-receive-pack 'owner/checkout.git'") exec git receive-pack "$JST_TEST_BARE_REPO" ;;
	*) exit 2 ;;
esac
`
	if err := os.WriteFile(transport, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", transport)
	t.Setenv("GIT_SSH_VARIANT", "ssh")
	t.Setenv("JST_TEST_BARE_REPO", origin)
	repo.Git("remote", "add", "origin", "git@github.com:owner/checkout.git")
	repo.Git("push", "-q", "origin", "main")
	repo.Git("checkout", "-q", "-b", "feat")
	if featFiles == nil {
		featFiles = map[string]string{"b.txt": "two\n"}
	}
	repo.Commit("feature", featFiles)
	land := &landRepo{Repo: repo, origin: origin, clock: newClock()}
	land.host = newFakeHost(hosting.Change{Number: 7, ID: "PR_node7", URL: "https://github.com/owner/checkout/pull/7", State: "open",
		Head: "feat", HeadSHA: land.Git("rev-parse", "feat"), HeadRepository: "owner/checkout",
		Base: "main", BaseSHA: land.remoteBranch("main"), BaseRepository: "owner/checkout", Mergeable: "yes"}, requiresTest)
	land.host.checks = passing(check("test", hosting.Pass))
	// The host's branch tip is origin's, as the bare repository has it now.
	land.host.tip = land.remoteBranch
	return land
}

// run is `jst land ARGS` against the fake host: its exit code, standard output and standard error.
func (land *landRepo) run(args ...string) (int, string, string) {
	land.T.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(args, Deps{Stdin: strings.NewReader(land.stdin), Stdout: &stdout, Stderr: &stderr, Dir: land.Root,
		Git: func(dir string) Git { return gitcli.Git{Dir: dir} },
		Connect: func(_ context.Context, settings config.GitHub, repository string) (hosting.Host, string, error) {
			if settings.Account != "someone" || repository != "owner/checkout" {
				return nil, "", errors.New("connected to " + settings.Account + " " + repository)
			}
			return land.host, "", nil
		},
		Clock:    land.clock,
		Terminal: func() bool { return land.terminal }})
	return code, stdout.String(), stderr.String()
}

// expect runs jst land and asserts its exit code and its last line, `jst land: STATE`.
func (land *landRepo) expect(code int, state string, args ...string) string {
	land.T.Helper()
	got, stdout, stderr := land.run(args...)
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	// At a terminal the person's Enter ends the prompt's line; here nothing echoes it.
	last := strings.TrimPrefix(lines[len(lines)-1], "type the change's number to merge it (#7): ")
	if got != code || last != "jst land: "+state {
		land.T.Fatalf("jst land %v: exit %d, want %d and `jst land: %s`\nstdout:\n%s\nstderr:\n%s\nhost calls: %v",
			args, got, code, state, stdout, stderr, land.host.calls)
	}
	return stdout + stderr
}

func (land *landRepo) remoteBranch(name string) string {
	land.T.Helper()
	return land.GitIn(land.origin, "for-each-ref", "--format=%(objectname)", "refs/heads/"+name)
}

// checkoutState is what jst land must not change here (W-019): HEAD, every local branch, and
// every file of the working tree.
func (land *landRepo) checkoutState() string {
	land.T.Helper()
	var out strings.Builder
	out.WriteString(land.Git("symbolic-ref", "HEAD") + "\n" + land.Git("for-each-ref", "refs/heads") + "\n")
	err := filepath.WalkDir(land.Root, func(place string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(place)
			if err != nil {
				return err
			}
			out.WriteString(place + "=" + string(data) + "\n")
		}
		return nil
	})
	if err != nil {
		land.T.Fatal(err)
	}
	return out.String()
}

// side is a clone of origin by path, for commits made elsewhere (on the host).
func (land *landRepo) side() string {
	land.T.Helper()
	place := filepath.Join(filepath.Dir(land.Root), "side")
	if _, err := os.Stat(place); err != nil {
		land.Git("clone", "-q", land.origin, place)
	}
	return place
}

// advanceMain pushes a commit to origin's main from elsewhere and returns it.
func (land *landRepo) advanceMain(file string) string {
	land.T.Helper()
	place := land.side()
	land.GitIn(place, "checkout", "-q", "main")
	land.GitIn(place, "pull", "-q", "origin", "main")
	if err := os.WriteFile(filepath.Join(place, file), []byte("base\n"), 0o600); err != nil {
		land.T.Fatal(err)
	}
	land.GitIn(place, "add", "-A")
	land.GitIn(place, "commit", "-q", "-m", "base moves")
	land.GitIn(place, "push", "-q", "origin", "main")
	return land.remoteBranch("main")
}

func (land *landRepo) noMerge() {
	land.T.Helper()
	if len(land.host.merges) != 0 || len(land.host.enqueued) != 0 {
		land.T.Fatalf("merged though it must not: merges %+v, enqueued %v", land.host.merges, land.host.enqueued)
	}
}

func (land *landRepo) mergedOnceAt(head string) {
	land.T.Helper()
	if merges := land.host.merges; len(merges) != 1 || merges[0].ExpectedHead != head || merges[0].Method != hosting.Squash {
		land.T.Fatalf("merges %+v, want one squash at %s", merges, head)
	}
}

func contains(t *testing.T, text, part string) {
	t.Helper()
	if !strings.Contains(text, part) {
		t.Fatalf("%q is not in:\n%s", part, text)
	}
}

// --- landing -----------------------------------------------------------------------------------

func TestLandMergesAGreenChangeDeletesTheRemoteBranchAndLeavesTheCheckout(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	head := land.Git("rev-parse", "feat")
	remoteAtMerge := ""
	land.host.mergeAnswer = func(host *fakeHost, request hosting.MergeRequest) (hosting.Merged, error) {
		remoteAtMerge = land.remoteBranch("feat")
		host.change.State = "merged"
		return hosting.Merged{Merged: true, SHA: "5ca1ab1e00000000000000000000000000000000"}, nil
	}
	before := land.checkoutState()
	out := land.expect(0, "landed", "--title", "a feature")
	contains(t, out, "pushed feat")
	contains(t, out, "opened #7")
	contains(t, out, "deleted origin/feat")
	land.mergedOnceAt(head)
	if remoteAtMerge != head {
		t.Fatalf("origin had %q at the merge, want %s", remoteAtMerge, head)
	}
	if len(land.host.opens) != 1 || land.host.opens[0].Title != "a feature" || land.host.opens[0].Base != "main" {
		t.Fatalf("opens %+v", land.host.opens)
	}
	if land.remoteBranch("feat") != "" {
		t.Fatal("the remote branch is still there")
	}
	if after := land.checkoutState(); after != before {
		t.Fatalf("the checkout changed (W-019):\nbefore %s\nafter %s", before, after)
	}
}

func TestPlanStopsBeforePushingOrOpening(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	out := land.expect(0, "planned", "--plan", "--title", "a feature")
	contains(t, out, "push feat (new upstream)")
	contains(t, out, "wait for the required checks (test)")
	if land.remoteBranch("feat") != "" {
		t.Fatal("--plan pushed")
	}
	if len(land.host.opens) != 0 {
		t.Fatalf("--plan opened %+v", land.host.opens)
	}
	land.noMerge()
}

func TestNoLandSectionInTheCheckoutIsInvalidAndNamesAdopt(t *testing.T) {
	land := newLandRepo(t, noLandSettings, nil, nil)
	out := land.expect(2, "invalid", "--title", "a feature")
	contains(t, out, "`jst adopt`")
	if len(land.host.calls) != 0 {
		t.Fatalf("the host was asked: %v", land.host.calls)
	}
}

func TestADraftIsRefused(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.host.template.Draft = true
	out := land.expect(1, "refused", "--title", "a feature")
	contains(t, out, "a draft is not merged")
	land.noMerge()
}

// --- policy ------------------------------------------------------------------------------------

func TestAChangeTouchingAHumanPathIsReadyForAPerson(t *testing.T) {
	cases := []struct {
		name, settings string
		files          map[string]string
	}{
		{"jackstay.json", greenSettings, map[string]string{"jackstay.json": greenSettings + "\n"}},
		{"a workflow", greenSettings, map[string]string{".github/workflows/ci.yml": "on: push\n"}},
		{"the agent settings", greenSettings, map[string]string{".claude/settings.json": "{}\n"}},
		{"the base's human_paths", deploySettings, map[string]string{"deploy/x": "x\n"}},
	}
	for _, each := range cases {
		t.Run(each.name, func(t *testing.T) {
			land := newLandRepo(t, each.settings, nil, each.files)
			out := land.expect(6, "ready for a person", "--title", "a feature")
			contains(t, out, "it touches paths only a person lands")
			contains(t, out, "GREEN") // handed over with its checks passed
			land.noMerge()
		})
	}
}

func TestAPrepareBaseIsReadyForAPerson(t *testing.T) {
	land := newLandRepo(t, prepareSettings, nil, nil)
	out := land.expect(6, "ready for a person", "--title", "a feature")
	contains(t, out, "`land.autonomy` is prepare on main")
	land.noMerge()
}

// Autonomy is read from the base only: a branch whose file says merge-when-green does not lift a
// prepare base.
func TestABranchCannotGrantItselfMergeWhenGreenOnAPrepareBase(t *testing.T) {
	land := newLandRepo(t, prepareSettings, nil, map[string]string{"jackstay.json": greenSettings})
	out := land.expect(6, "ready for a person", "--title", "a feature")
	contains(t, out, "`land.autonomy` is prepare on main")
	land.noMerge()
}

func TestAChangeAddingTheFirstLandSectionIsReadyForAPerson(t *testing.T) {
	land := newLandRepo(t, noLandSettings, nil, map[string]string{"jackstay.json": greenSettings})
	out := land.expect(6, "ready for a person", "--title", "a feature")
	contains(t, out, "main has no landing policy yet")
	land.noMerge()
}

func TestPrepareFlagIsReadyForAPerson(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	out := land.expect(6, "ready for a person", "--prepare", "--title", "a feature")
	contains(t, out, "for a person: --prepare")
	land.noMerge()
}

func TestConfirmWithoutATerminalIsInvalid(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.stdin = "7\n"
	land.expect(2, "invalid", "--confirm", "--title", "a feature")
	if len(land.host.calls) != 0 {
		t.Fatalf("the host was asked: %v", land.host.calls)
	}
}

func TestConfirmWithPrepareIsInvalid(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.terminal, land.stdin = true, "7\n"
	land.expect(2, "invalid", "--confirm", "--prepare", "--title", "a feature")
	land.noMerge()
}

func TestConfirmWithTheNumberTypedMergesAPersonsChange(t *testing.T) {
	land := newLandRepo(t, deploySettings, nil, map[string]string{"deploy/x": "x\n"})
	land.terminal, land.stdin = true, "7\n"
	head := land.Git("rev-parse", "feat")
	out := land.expect(0, "landed", "--confirm", "--title", "a feature")
	contains(t, out, "type the change's number")
	land.mergedOnceAt(head)
}

func TestConfirmWithTheWrongNumberTypedIsRefused(t *testing.T) {
	land := newLandRepo(t, deploySettings, nil, map[string]string{"deploy/x": "x\n"})
	land.terminal, land.stdin = true, "8\n"
	out := land.expect(1, "refused", "--confirm", "--title", "a feature")
	contains(t, out, "not merging")
	land.noMerge()
}

// --confirm always asks, even for a change the agent could merge on its own.
func TestConfirmAsksEvenForAChangeTheAgentMayMerge(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.terminal, land.stdin = true, ""
	out := land.expect(1, "refused", "--confirm", "--title", "a feature")
	contains(t, out, "no answer was typed")
	land.noMerge()
}

// The recheck reads the base branch's live tip, not only the base the host reports for the
// change: a base that moved after the policy and checks were judged stops the landing.
func TestABaseThatMovedAfterItWasJudgedIsMoved(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.host.onRules = func(call int) {
		if call == 3 { // the recheck's read: the base moved during the local checks
			land.advanceMain("c.txt")
		}
	}
	out := land.expect(4, "moved", "--title", "a feature")
	contains(t, out, "not at")
	land.noMerge()
}

// A change for a person is rechecked before it is handed over: a required check that turned red
// after the wait stops it with moved, not ready for a person.
func TestAHandOverIsRecheckedFirst(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.host.checks = func(call int, change hosting.Change) []hosting.Check {
		if call == 1 {
			return onHead(change, []hosting.Check{check("test", hosting.Pass)})
		}
		return onHead(change, []hosting.Check{check("test", hosting.Fail)})
	}
	out := land.expect(4, "moved", "--prepare", "--title", "a feature")
	contains(t, out, "no longer passes (test)")
	land.noMerge()
}

const cardSettings = `{"jackstay_schema": 3, "github": {"account": "someone", "repository": "owner/checkout"},
 "land": {"autonomy": "merge-when-green"},
 "cards": {"dir": "cards", "id": "APP-[0-9]+", "unit": "pr", "branch": "main", "since": "2026-01-01"}}`

var firstCard = map[string]string{"cards/APP-1-x.md": "---\n{\"id\": \"APP-1\", \"state\": \"active\"}\n---\n# APP-1\n"}

// The Card line is judged on the merge with the base it was judged at: a card added to the base
// after the branch split is there.
func TestACardAddedToTheBaseAfterTheSplitIsFound(t *testing.T) {
	land := newLandRepo(t, cardSettings, firstCard, nil)
	place := land.side()
	land.GitIn(place, "checkout", "-q", "main")
	land.GitIn(place, "pull", "-q", "origin", "main")
	if err := os.WriteFile(filepath.Join(place, "cards", "APP-2-y.md"), []byte("---\n{\"id\": \"APP-2\", \"state\": \"active\"}\n---\n# APP-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	land.GitIn(place, "add", "-A")
	land.GitIn(place, "commit", "-q", "-m", "card APP-2")
	land.GitIn(place, "push", "-q", "origin", "main")
	land.Git("push", "-q", "-u", "origin", "feat")
	land.host.change = land.host.template
	land.host.change.BaseSHA = land.remoteBranch("main")
	land.host.change.Body = "What it does.\n\nCard: APP-2\n"
	land.host.opened = true
	out := land.expect(0, "landed")
	contains(t, out, "its Card line is fine on the merge")
	if len(land.host.merges) != 1 || land.host.merges[0].Body != "What it does.\n\nCard: APP-2\n" {
		t.Fatalf("merges %+v", land.host.merges)
	}
}

// A rebase lands no description, so a project whose Card line is in the description refuses it.
func TestARebaseIsRefusedWhereTheCardLineIsInTheDescription(t *testing.T) {
	settings := strings.Replace(cardSettings, `"autonomy": "merge-when-green"`, `"autonomy": "merge-when-green", "method": "rebase"`, 1)
	land := newLandRepo(t, settings, firstCard, nil)
	out := land.expect(2, "invalid", "--title", "a feature", "--card", "APP-1")
	contains(t, out, "a rebase does not land")
	land.noMerge()
}

// --- checks ------------------------------------------------------------------------------------

func TestRulesUnreadWithListedChecksRequireOnlyThose(t *testing.T) {
	land := newLandRepo(t, listedSettings, nil, nil)
	land.host.rules = []hosting.Rules{{Unread: "the plan does not include rulesets"}}
	land.host.checks = passing(check("test", hosting.Pass), check("lint", hosting.Fail))
	head := land.Git("rev-parse", "feat")
	out := land.expect(0, "landed", "--title", "a feature")
	contains(t, out, "wait for the required checks (test)")
	contains(t, out, "not required: lint fail")
	land.mergedOnceAt(head)
}

func TestRulesUnreadWithoutListedChecksWaitReportAndStopForAPerson(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.host.rules = []hosting.Rules{{Unread: "the plan does not include rulesets"}}
	land.host.checks = func(call int, change hosting.Change) []hosting.Check {
		if call == 1 {
			return onHead(change, []hosting.Check{check("test", hosting.Pending)})
		}
		return onHead(change, []hosting.Check{check("test", hosting.Fail)})
	}
	out := land.expect(6, "ready for a person", "--title", "a feature")
	contains(t, out, "the base's rules cannot be read (the plan does not include rulesets)")
	contains(t, out, "not required: test fail") // reported, never a blocker
	if len(land.clock.sleeps) == 0 {
		t.Fatal("it did not wait for the checks to settle")
	}
	land.noMerge()
}

func TestRulesRequiringNoCheckStopForAPerson(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.host.rules = []hosting.Rules{{ChecksRead: true, MethodsRead: true, Methods: []hosting.Method{hosting.Squash}}}
	out := land.expect(6, "ready for a person", "--title", "a feature")
	contains(t, out, "the base's rules require no check and `land.checks` lists none")
	land.noMerge()
}

// A new repository with no CI: the rules require none and no check ever registers. It stops
// ready for a person once checks had time to appear, not pending at the deadline.
func TestABaseWithNoCheckAtAllStopsForAPerson(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.host.rules = []hosting.Rules{{ChecksRead: true, MethodsRead: true, Methods: []hosting.Method{hosting.Squash}}}
	land.host.checks = nil
	out := land.expect(6, "ready for a person", "--title", "a feature")
	contains(t, out, "nothing to wait for")
	land.noMerge()
}

func TestARequiredCheckFailingIsRefused(t *testing.T) {
	for _, state := range []hosting.State{hosting.Fail, hosting.Cancelled} {
		t.Run(string(state), func(t *testing.T) {
			land := newLandRepo(t, greenSettings, nil, nil)
			land.host.checks = passing(check("test", state))
			out := land.expect(1, "refused", "--title", "a feature")
			contains(t, out, "required check(s) of #7 did not pass: test")
			land.noMerge()
		})
	}
}

func TestAFailingCheckThatIsNotRequiredDoesNotBlockTheLanding(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.host.checks = passing(check("test", hosting.Pass), check("flaky", hosting.Fail))
	land.expect(0, "landed", "--title", "a feature")
	land.mergedOnceAt(land.Git("rev-parse", "feat"))
}

func TestChecksStillPendingAtTheDeadlineArePending(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.host.checks = passing(check("test", hosting.Pending))
	out := land.expect(3, "pending", "--title", "a feature", "--timeout", "60", "--interval", "20")
	contains(t, out, "still pending at the deadline: test")
	land.noMerge()
}

// --- races -------------------------------------------------------------------------------------

func TestTheHeadMovingDuringTheWaitIsMoved(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.host.checks = passing(check("test", hosting.Pending))
	land.host.beforeRead = func(host *fakeHost, read int) {
		if read == 4 { // the second look of the wait
			host.change.HeadSHA = strings.Repeat("e", 40)
		}
	}
	out := land.expect(4, "moved", "--title", "a feature")
	contains(t, out, "the head of #7 moved")
	land.noMerge()
}

func TestTheHeadMovingBetweenTheWaitAndTheRecheckIsMoved(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	// Reads: 1 the policy, 2 and 3 the wait (green), 4 mergeability, 5 the recheck.
	land.host.beforeRead = func(host *fakeHost, read int) {
		if read == 5 {
			host.change.HeadSHA = strings.Repeat("e", 40)
		}
	}
	out := land.expect(4, "moved", "--title", "a feature")
	contains(t, out, "GREEN")
	contains(t, out, "the head of #7 moved")
	land.noMerge()
}

func TestARequiredCheckRerunRedBeforeTheRecheckIsMoved(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.host.checks = func(call int, change hosting.Change) []hosting.Check {
		if call == 1 {
			return onHead(change, []hosting.Check{check("test", hosting.Pass)})
		}
		return onHead(change, []hosting.Check{check("test", hosting.Fail)})
	}
	out := land.expect(4, "moved", "--title", "a feature")
	contains(t, out, "a required check of #7 no longer passes (test)")
	land.noMerge()
}

// The base moving during the wait sends the change back to step 2: the policy is judged at the
// new base, and it lands at the new base.
func TestTheBaseMovingDuringTheWaitJudgesThePolicyAgain(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	moved := land.advanceMain("c.txt")
	land.host.beforeRead = func(host *fakeHost, read int) {
		if read == 3 { // after the checks of the first look were read
			host.change.BaseSHA = moved
		}
	}
	land.expect(0, "landed", "--title", "a feature")
	land.mergedOnceAt(land.Git("rev-parse", "feat"))
	if land.host.ruleCalls < 3 {
		t.Fatalf("the rules were read %d times: the policy was not judged again", land.host.ruleCalls)
	}
}

func TestALostMergeAnswerIsSettledByReadingTheChange(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	land.host.mergeAnswer = func(host *fakeHost, request hosting.MergeRequest) (hosting.Merged, error) {
		host.change.State = "merged"
		return hosting.Merged{}, errors.New("connection reset")
	}
	head := land.Git("rev-parse", "feat")
	out := land.expect(0, "landed", "--title", "a feature")
	contains(t, out, "the answer to the merge was lost (connection reset)")
	land.mergedOnceAt(head)
	if land.remoteBranch("feat") != "" {
		t.Fatal("the remote branch is still there")
	}
}

func TestTheHostsQueueMergesTheChange(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	queued := requiresTest
	queued.Queue = true
	land.host.rules = []hosting.Rules{queued}
	land.host.queueMergesAfter = 3
	head := land.Git("rev-parse", "feat")
	out := land.expect(0, "landed", "--title", "a feature")
	contains(t, out, "in the host's merge queue")
	if !slices.Equal(land.host.enqueued, []string{head}) || len(land.host.merges) != 0 {
		t.Fatalf("enqueued %v, merges %+v", land.host.enqueued, land.host.merges)
	}
}

func TestTheHostsQueueHoldingPastTheDeadlineIsPending(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	queued := requiresTest
	queued.Queue = true
	land.host.rules = []hosting.Rules{queued}
	out := land.expect(3, "pending", "--title", "a feature", "--timeout", "60", "--interval", "20")
	contains(t, out, "still in the merge queue at the deadline")
	if len(land.host.enqueued) != 1 || land.remoteBranch("feat") == "" {
		t.Fatalf("enqueued %v; the remote branch must be kept", land.host.enqueued)
	}
}

// --- freshness ---------------------------------------------------------------------------------

func TestABranchBehindAnUpToDateBaseIsUpdatedAndJudgedAgain(t *testing.T) {
	land := newLandRepo(t, greenSettings, nil, nil)
	fresh := requiresTest
	fresh.UpToDate = true
	land.host.rules = []hosting.Rules{fresh}
	oldHead := land.Git("rev-parse", "feat")
	land.host.template.BaseSHA = land.advanceMain("c.txt")
	land.host.template.Behind = true
	newHead := ""
	land.host.onUpdate = func(host *fakeHost) error {
		place := land.side()
		land.GitIn(place, "fetch", "-q", "origin")
		land.GitIn(place, "checkout", "-q", "-B", "feat", "origin/feat")
		land.GitIn(place, "merge", "-q", "--no-edit", "origin/main")
		land.GitIn(place, "push", "-q", "origin", "feat")
		newHead = land.remoteBranch("feat")
		host.change.HeadSHA, host.change.Behind = newHead, false
		return nil
	}
	out := land.expect(0, "landed", "--title", "a feature")
	contains(t, out, "judging it again")
	if !slices.Equal(land.host.updates, []string{oldHead}) {
		t.Fatalf("updates %v, want one at %s", land.host.updates, oldHead)
	}
	land.mergedOnceAt(newHead)
	if land.remoteBranch("feat") != "" {
		t.Fatal("the remote branch is still there")
	}
}
