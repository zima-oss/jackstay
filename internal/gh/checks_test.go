package gh

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/githubapi/githubfake"
)

// answer is githubfake's Ok, shortened.
func answer[Value any](value Value) githubfake.Result[Value] { return githubfake.Ok(value) }

func onPull(hub *fakeGitHub, pulls ...githubapi.PullRequest) {
	var answers []githubfake.Result[githubapi.PullRequest]
	for _, each := range pulls {
		answers = append(answers, answer(each))
	}
	hub.OnPullRequest(7, answers...)
}

func onChecks(hub *fakeGitHub, sha string, rows ...[]githubapi.Check) {
	var answers []githubfake.Result[[]githubapi.Check]
	for _, each := range rows {
		answers = append(answers, answer(each))
	}
	hub.OnChecks(sha, answers...)
}

type checkDeadlineHub struct {
	*fakeGitHub
	deadlines []time.Time
}

func (hub *checkDeadlineHub) noteDeadline(ctx context.Context) {
	deadline, _ := ctx.Deadline()
	hub.deadlines = append(hub.deadlines, deadline)
}

func (hub *checkDeadlineHub) PullRequest(ctx context.Context, number int) (githubapi.PullRequest, error) {
	hub.noteDeadline(ctx)
	return hub.fakeGitHub.PullRequest(ctx, number)
}

func (hub *checkDeadlineHub) Checks(ctx context.Context, head string) ([]githubapi.Check, error) {
	hub.noteDeadline(ctx)
	return hub.fakeGitHub.Checks(ctx, head)
}

func (hub *checkDeadlineHub) Jobs(ctx context.Context, number int64) ([]githubapi.Job, error) {
	hub.noteDeadline(ctx)
	return hub.fakeGitHub.Jobs(ctx, number)
}

func TestCIWaitSharesOneDeadlineForLookupChecksAndFailureDetail(t *testing.T) {
	hub := &checkDeadlineHub{fakeGitHub: newGitHub()}
	onPull(hub.fakeGitHub, pull(7, "feat", shaA))
	onChecks(hub.fakeGitHub, shaA, []githubapi.Check{failed("lint", 1)})
	var output strings.Builder
	parent := context.Background()
	work := session{ctx: parent, deps: Deps{Stdout: &output, Clock: newClock()}, github: hub}
	code, err := work.ciWait(7, waitOptions{timeout: time.Minute, lines: 5})
	if code != exitNotFine || err != nil || work.ctx != parent {
		t.Fatalf("exit %d: %v; original context restored %v", code, err, work.ctx == parent)
	}
	if len(hub.deadlines) != 4 || hub.deadlines[0].IsZero() {
		t.Fatalf("request deadlines %v", hub.deadlines)
	}
	for _, deadline := range hub.deadlines[1:] {
		if !deadline.Equal(hub.deadlines[0]) {
			t.Fatalf("requests use different deadlines: %v", hub.deadlines)
		}
	}
}

func TestCIWaitReportsDeadlineExpiryAsPending(t *testing.T) {
	parent, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	work := session{ctx: parent, deps: Deps{Clock: newClock()}}
	_, err := work.withCIWaitBudget(time.Second, func(_, _ time.Time) (int, error) {
		return 0, fmt.Errorf("lookup: %w", work.ctx.Err())
	})
	if codeOf(err) != exitPending {
		t.Fatalf("deadline gave %v", err)
	}
}

func TestNoChecksRegisteredIsWaitingNotAPass(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, nil)
	stdout, stderr := bench.expect(exitPending, "wait", "7", "--timeout", "1", "--interval", "0.1")
	contains(t, stdout, "this is not a pass")
	contains(t, stderr, "no check ever registered for aaaaaaaa within 1s")
	if asked := bench.hub.count("Checks"); asked < 5 {
		t.Fatalf("asked %d times while waiting", asked)
	}
	if slept := bench.clock.slept(); slept != time.Second {
		t.Fatalf("slept %v, want the timeout exactly", slept)
	}
}

func TestTheHeadMovingWhileWaitingIsExit4(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA), pull(7, "feat", shaA), pull(7, "feat", shaB))
	onChecks(bench.hub, shaA, []githubapi.Check{pending("test")})
	_, stderr := bench.expect(exitMoved, "wait", "7", "--timeout", "30", "--interval", "1")
	contains(t, stderr, "moved from aaaaaaaa to bbbbbbbb")
}

func TestTheHeadIsComparedAfterTheRowsAreRead(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	// Green rows for A, but the head is B by the time they are read: they say nothing about B.
	onPull(bench.hub, pull(7, "feat", shaA), pull(7, "feat", shaB))
	onChecks(bench.hub, shaA, []githubapi.Check{passed("test")})
	bench.expect(exitMoved, "wait", "7")
	calls := bench.hub.Calls()
	if calls[1].Op != "Checks" || calls[2].Op != "PullRequest" {
		t.Fatalf("the rows must be read before the head is compared: %v", calls)
	}
}

func TestAnyHeadFollowsAMovedHead(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA), pull(7, "feat", shaB))
	onChecks(bench.hub, shaA, []githubapi.Check{pending("test")})
	onChecks(bench.hub, shaB, []githubapi.Check{passed("test")})
	stdout, _ := bench.expect(exitFine, "wait", "7", "--any-head", "--timeout", "30", "--interval", "1")
	contains(t, stdout, "following it")
	contains(t, stdout, "GREEN: 1 checks")
}

func stampedLog(start time.Time, lines ...string) string {
	var out strings.Builder
	for index, line := range lines {
		fmt.Fprintf(&out, "%s %s\n", start.Add(time.Duration(index)*300*time.Millisecond).Format("2006-01-02T15:04:05.0000000Z"), line)
	}
	return out.String()
}

func TestCIWaitReportsARedCheckWhileAnotherIsPending(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{failed("lint", 0), pending("test")})
	stdout, _ := bench.expect(exitNotFine, "wait", "7", "--timeout", "1", "--interval", "0.1")
	contains(t, stdout, "RED:")
	if slept := bench.clock.slept(); slept != 0 {
		t.Fatalf("waited %v after observing a failed check", slept)
	}
}

func TestRedIsExit1WithTheFailedStepsLog(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{passed("lint"), failed("test", 123)})
	base := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	setup := stampedLog(base, "setting up", "checking out") // 09:00:00.0 .. 00.3
	var testLines []string
	for index := 1; index <= 40; index++ {
		testLines = append(testLines, fmt.Sprintf("step line %d", index))
	}
	run := stampedLog(base.Add(2*time.Second), testLines...)                 // 09:00:02 .. 09:00:13.7
	post := stampedLog(base.Add(20*time.Second), "post job cleanup", "done") // 09:00:20
	bench.hub.OnJobs(123, answer([]githubapi.Job{
		{ID: 1, Name: "lint", Conclusion: "success"},
		{ID: 9, Name: "test", Conclusion: "failure", Steps: []githubapi.Step{
			{Name: "Set up job", Conclusion: "success", StartedAt: base, CompletedAt: base.Add(time.Second)},
			{Name: "Run tests", Conclusion: "failure", StartedAt: base.Add(2 * time.Second), CompletedAt: base.Add(13 * time.Second)},
			{Name: "Post", Conclusion: "success", StartedAt: base.Add(20 * time.Second), CompletedAt: base.Add(21 * time.Second)},
		}},
	}))
	bench.hub.OnJobLog(9, answer(setup+run+post))
	stdout, _ := bench.expect(exitNotFine, "wait", "7", "--lines", "5", "--timeout", "30", "--interval", "1")
	contains(t, stdout, "RED: 1 of 2 checks did not pass.")
	contains(t, stdout, "[test / Run tests]")
	contains(t, stdout, "    step line 40")
	contains(t, stdout, "    step line 36")
	lacks(t, stdout, "step line 35")
	lacks(t, stdout, "post job cleanup")
	lacks(t, stdout, "2026-11-17T09")
	contains(t, stdout, "(2 check runs attached to aaaaaaaa)")
}

func TestALogThatCannotBeCutShowsItsLastLinesAndSaysSo(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{failed("test", 5)})
	bench.hub.OnJobs(5, answer([]githubapi.Job{{ID: 9, Name: "test", Conclusion: "failure",
		Steps: []githubapi.Step{{Name: "Run tests", Conclusion: "failure"}}}}))
	bench.hub.OnJobLog(9, answer("one\ntwo\nthree\nfour\n"))
	stdout, _ := bench.expect(exitNotFine, "wait", "7", "--lines", "2")
	contains(t, stdout, `the step "Run tests" could not be cut from it: the job log's last 2 lines`)
	contains(t, stdout, "    three\n    four")
	lacks(t, stdout, "    two")
}

func TestAStatusWithoutARunSaysToOpenItsLink(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{{Name: "ci/external", Kind: githubapi.StatusContext, State: "ERROR", Link: "https://ci.example/1"}})
	stdout, _ := bench.expect(exitNotFine, "wait", "7")
	contains(t, stdout, "--- ci/external — https://ci.example/1")
	contains(t, stdout, "no workflow run behind this check")
	if bench.hub.count("Jobs") != 0 {
		t.Fatal("no run, no jobs to ask for")
	}
}

func TestCancelIsSettledAndFailing(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{passed("lint"), checkRun("test", "COMPLETED", "CANCELLED", 4)})
	bench.hub.OnWorkflowRuns(githubapi.RunFilter{HeadSHA: shaA}, answer([]githubapi.WorkflowRun{}))
	stdout, _ := bench.expect(exitNotFine, "wait", "7", "--timeout", "30")
	contains(t, stdout, "RED: 1 of 2")
	contains(t, stdout, "cancelled: test (run 4 of ci): a cancellation is not a pass")
	lacks(t, stdout, "pending")
	if bench.hub.count("Jobs") != 0 {
		t.Fatal("a cancelled check has no failed step to show")
	}
	bench.expect(exitNotFine, "status", "7")
}

func TestPendingThenGreenIsExit0(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{pending("test"), passed("lint")},
		[]githubapi.Check{passed("test"), passed("lint"), checkRun("docs", "COMPLETED", "SKIPPED", 0)})
	stdout, _ := bench.expect(exitFine, "wait", "7", "--timeout", "30", "--interval", "20")
	contains(t, stdout, "PR #7 (feat) head aaaaaaaa in owner/checkout")
	contains(t, stdout, "0s: 1 pending — test")
	contains(t, stdout, "GREEN: 3 checks, 20s waiting.")
	contains(t, stdout, "(3 check runs attached to aaaaaaaa)")
}

func TestAGitHubFailureIsNotReadAsNoChecksYet(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	bench.hub.OnChecks(shaA, githubfake.Fail[[]githubapi.Check](errBadCredentials))
	_, stderr := bench.expect(exitNotFine, "wait", "7", "--timeout", "30")
	contains(t, stderr, "Bad credentials")
	if bench.hub.count("Checks") != 1 || bench.clock.slept() != 0 {
		t.Fatal("a failure is said at once, not waited out")
	}
}

func TestAFormatJackstayCannotReadIsExit5(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	bench.hub.OnChecks(shaA, githubfake.Fail[[]githubapi.Check](&json.SyntaxError{}))
	bench.expect(exitFormat, "status", "7")
}

func TestCIStatusReportsOnce(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{pending("test")}, []githubapi.Check{failed("test", 0)},
		[]githubapi.Check{passed("test")}, nil)
	bench.expect(exitPending, "status", "7")
	bench.expect(exitNotFine, "status", "7")
	bench.expect(exitFine, "status", "7")
	stdout, _ := bench.expect(exitPending, "status", "7")
	contains(t, stdout, "no checks registered for this ref yet")
	if bench.clock.slept() != 0 {
		t.Fatal("status never waits")
	}
}

func TestCIStatusHeadMovingWhileTheChecksAreReadIsExit4(t *testing.T) {
	// Green rows for A, and the head is B by the time they are
	// read. They say nothing about B.
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA), pull(7, "feat", shaB))
	onChecks(bench.hub, shaA, []githubapi.Check{passed("test")})
	_, stderr := bench.expect(exitMoved, "status", "7")
	contains(t, stderr, "moved from aaaaaaaa to bbbbbbbb")
	calls := bench.hub.Calls()
	if len(calls) != 3 || calls[1].Op != "Checks" || calls[2].Op != "PullRequest" {
		t.Fatalf("the rows must be read before the head is compared: %v", calls)
	}
}

func TestBuckets(t *testing.T) {
	cases := []struct {
		name  string
		check githubapi.Check
		want  int
	}{
		{"success", checkRun("c", "COMPLETED", "SUCCESS", 0), exitFine},
		{"skipped", checkRun("c", "COMPLETED", "SKIPPED", 0), exitFine},
		{"neutral", checkRun("c", "COMPLETED", "NEUTRAL", 0), exitFine},
		{"failure", checkRun("c", "COMPLETED", "FAILURE", 0), exitNotFine},
		{"timed out", checkRun("c", "COMPLETED", "TIMED_OUT", 0), exitNotFine},
		{"action required", checkRun("c", "COMPLETED", "ACTION_REQUIRED", 0), exitNotFine},
		{"startup failure", checkRun("c", "COMPLETED", "STARTUP_FAILURE", 0), exitNotFine},
		{"cancelled", checkRun("c", "COMPLETED", "CANCELLED", 0), exitNotFine},
		{"stale", checkRun("c", "COMPLETED", "STALE", 0), exitPending},
		{"queued", checkRun("c", "QUEUED", "", 0), exitPending},
		{"status pending", githubapi.Check{Name: "s", Kind: githubapi.StatusContext, State: "PENDING"}, exitPending},
		{"status expected", githubapi.Check{Name: "s", Kind: githubapi.StatusContext, State: "EXPECTED"}, exitPending},
		{"status error", githubapi.Check{Name: "s", Kind: githubapi.StatusContext, State: "ERROR"}, exitNotFine},
		{"status success", githubapi.Check{Name: "s", Kind: githubapi.StatusContext, State: "SUCCESS"}, exitFine},
	}
	for _, each := range cases {
		t.Run(each.name, func(t *testing.T) {
			bench := newHarness(t, checkout(shaA))
			onPull(bench.hub, pull(7, "feat", shaA))
			onChecks(bench.hub, shaA, []githubapi.Check{each.check})
			bench.expect(each.want, "status", "7")
		})
	}
	if settled[githubapi.Pending] {
		t.Fatal("pending is not settled")
	}
}

func TestARerunsNewestResultWins(t *testing.T) {
	// The rollup holds the newest run of each check name: a re-run that passed after a cancelled
	// or failed one is one green row, not a red one left behind.
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{passed("test")})
	bench.expect(exitFine, "wait", "7")
}

func TestSummaryOrderRedFirst(t *testing.T) {
	text := summarise([]githubapi.Check{passed("b"), pending("c"), failed("z", 0), checkRun("a", "COMPLETED", "CANCELLED", 0)})
	lines := strings.Split(text, "\n")
	order := []string{"z", "a", "c", "b"}
	for index, name := range order {
		if !strings.HasPrefix(strings.TrimSpace(lines[index]), name+" ") {
			t.Fatalf("line %d is %q, want %s first:\n%s", index, lines[index], name, text)
		}
	}
}

func TestCutLog(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	log := "\ufeff" + stampedLog(base, "before") + stampedLog(base.Add(5*time.Second), "in one", "in two") +
		"continued without a stamp\n" + stampedLog(base.Add(9*time.Second), "after")
	got := cutLog(log, base.Add(5*time.Second), base.Add(6*time.Second))
	want := []string{"in one", "in two", "continued without a stamp"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("cut %q, want %q", got, want)
	}
	if cutLog(log, time.Time{}, base) != nil {
		t.Fatal("a step without times cannot be cut")
	}
	if got := tail([]string{"a", "", "b", " ", "c"}, 2); strings.Join(got, "") != "bc" {
		t.Fatalf("tail %q", got)
	}
}

func TestHumanSeconds(t *testing.T) {
	for span, want := range map[time.Duration]string{0: "0s", 59 * time.Second: "59s", 187 * time.Second: "3m07s"} {
		if got := humanSeconds(span); got != want {
			t.Fatalf("%v: %s, want %s", span, got, want)
		}
	}
}

func TestWaitDefaultsComeFromTheGitHubSectionAndFlagsWin(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	settings := `{"jackstay_schema": 2, "github": {"account": "someone", "wait": {"timeout_seconds": 3, "interval_seconds": 1, "log_lines": 2}}}`
	if err := os.WriteFile(filepath.Join(bench.dir, "jackstay.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, nil)
	_, stderr := bench.expect(exitPending, "wait", "7")
	contains(t, stderr, "within 3s")
	_, stderr = bench.expect(exitPending, "wait", "7", "--timeout", "1")
	contains(t, stderr, "within 1s")
}

func TestACancelledCheckUsesANewerRunOfTheSameWorkflowAndCommit(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{checkRun("lint", "COMPLETED", "CANCELLED", 4), checkRun("lint", "COMPLETED", "SUCCESS", 5)})
	bench.hub.OnWorkflowRuns(githubapi.RunFilter{HeadSHA: shaA}, answer([]githubapi.WorkflowRun{
		{ID: 5, WorkflowID: 1, HeadSHA: shaA, Status: "completed", Conclusion: "success"},
		{ID: 4, WorkflowID: 1, HeadSHA: shaA, Status: "completed", Conclusion: "cancelled"},
	}))
	stdout, _ := bench.expect(exitFine, "wait", "7", "--timeout", "30")
	contains(t, stdout, "run 5 supersedes cancelled run 4")
}

func TestACancelledCheckWaitsForTheReplacementJob(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA,
		[]githubapi.Check{checkRun("lint", "COMPLETED", "CANCELLED", 4)},
		[]githubapi.Check{checkRun("lint", "COMPLETED", "CANCELLED", 4), checkRun("lint", "COMPLETED", "SUCCESS", 5)})
	bench.hub.OnWorkflowRuns(githubapi.RunFilter{HeadSHA: shaA}, answer([]githubapi.WorkflowRun{
		{ID: 5, WorkflowID: 1, HeadSHA: shaA, Status: "in_progress"},
		{ID: 4, WorkflowID: 1, HeadSHA: shaA, Status: "completed", Conclusion: "cancelled"},
	}))
	stdout, _ := bench.expect(exitFine, "wait", "7", "--timeout", "20", "--interval", "5")
	contains(t, stdout, "0s: 1 pending — lint")
	contains(t, stdout, "run 5 supersedes cancelled run 4")
}

// docs/design/land.md section 4: a newer run that finished without the job leaves the check
// cancelled; only while it runs is the check pending.
func TestAFinishedReplacementWithoutTheJobLeavesTheCheckCancelled(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{checkRun("lint", "COMPLETED", "CANCELLED", 4)})
	bench.hub.OnWorkflowRuns(githubapi.RunFilter{HeadSHA: shaA}, answer([]githubapi.WorkflowRun{
		{ID: 5, WorkflowID: 1, HeadSHA: shaA, Status: "completed", Conclusion: "success"},
		{ID: 4, WorkflowID: 1, HeadSHA: shaA, Status: "completed", Conclusion: "cancelled"},
	}))
	stdout, _ := bench.expect(exitNotFine, "wait", "7", "--timeout", "5", "--interval", "5")
	contains(t, stdout, "finished without lint: the cancellation stands")
}

func TestARunningReplacementWithoutTheJobYetIsPending(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{checkRun("lint", "COMPLETED", "CANCELLED", 4)})
	bench.hub.OnWorkflowRuns(githubapi.RunFilter{HeadSHA: shaA}, answer([]githubapi.WorkflowRun{
		{ID: 5, WorkflowID: 1, HeadSHA: shaA, Status: "in_progress"},
		{ID: 4, WorkflowID: 1, HeadSHA: shaA, Status: "completed", Conclusion: "cancelled"},
	}))
	stdout, stderr := bench.expect(exitPending, "wait", "7", "--timeout", "5", "--interval", "5")
	contains(t, stdout, "run 5 supersedes cancelled run 4")
	contains(t, stderr, "still pending after 5s: lint")
}

func TestACancelledCheckDoesNotHideAFailedReplacement(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA))
	onChecks(bench.hub, shaA, []githubapi.Check{checkRun("lint", "COMPLETED", "CANCELLED", 4), checkRun("lint", "COMPLETED", "FAILURE", 5)})
	bench.hub.OnWorkflowRuns(githubapi.RunFilter{HeadSHA: shaA}, answer([]githubapi.WorkflowRun{
		{ID: 5, WorkflowID: 1, HeadSHA: shaA, Status: "completed", Conclusion: "failure"},
		{ID: 4, WorkflowID: 1, HeadSHA: shaA, Status: "completed", Conclusion: "cancelled"},
	}))
	stdout, _ := bench.expect(exitNotFine, "wait", "7", "--timeout", "30")
	contains(t, stdout, "run 5 supersedes cancelled run 4")
	contains(t, stdout, "RED: 1 of 1 checks")
}

func TestACancelledCheckRejectsAnUnrelatedReplacement(t *testing.T) {
	for _, newer := range []githubapi.WorkflowRun{
		{ID: 5, WorkflowID: 2, HeadSHA: shaA, HeadBranch: "feat", Event: "pull_request", Status: "completed", Conclusion: "success"},
		{ID: 5, WorkflowID: 1, HeadSHA: shaB, HeadBranch: "feat", Event: "pull_request", Status: "completed", Conclusion: "success"},
		{ID: 5, WorkflowID: 1, HeadSHA: shaA, HeadBranch: "other", Event: "pull_request", Status: "completed", Conclusion: "success"},
		{ID: 5, WorkflowID: 1, HeadSHA: shaA, HeadBranch: "feat", Event: "push", Status: "completed", Conclusion: "success"},
	} {
		bench := newHarness(t, checkout(shaA))
		onPull(bench.hub, pull(7, "feat", shaA))
		onChecks(bench.hub, shaA, []githubapi.Check{checkRun("lint", "COMPLETED", "CANCELLED", 4), checkRun("lint", "COMPLETED", "SUCCESS", 5)})
		bench.hub.OnWorkflowRuns(githubapi.RunFilter{HeadSHA: shaA}, answer([]githubapi.WorkflowRun{
			newer, {ID: 4, WorkflowID: 1, HeadSHA: shaA, HeadBranch: "feat", Event: "pull_request", Status: "completed", Conclusion: "cancelled"},
		}))
		stdout, _ := bench.expect(exitNotFine, "wait", "7", "--timeout", "30")
		contains(t, stdout, "cancelled: lint (run 4 of ci)")
	}
}

func TestACancelledCheckReplacementRequiresTheSamePullRequest(t *testing.T) {
	for _, replacement := range []struct {
		name  string
		pulls []int
		want  int
	}{
		{name: "same PR", pulls: []int{7}, want: exitFine},
		{name: "different PR", pulls: []int{8}, want: exitNotFine},
		{name: "unknown PR", want: exitNotFine},
	} {
		t.Run(replacement.name, func(t *testing.T) {
			bench := newHarness(t, checkout(shaA))
			onPull(bench.hub, pull(7, "feat", shaA))
			onChecks(bench.hub, shaA, []githubapi.Check{checkRun("lint", "COMPLETED", "CANCELLED", 4), checkRun("lint", "COMPLETED", "SUCCESS", 5)})
			bench.hub.OnWorkflowRuns(githubapi.RunFilter{HeadSHA: shaA}, answer([]githubapi.WorkflowRun{
				{ID: 5, WorkflowID: 1, HeadSHA: shaA, HeadBranch: "feat", Event: "pull_request", PullRequests: replacement.pulls},
				{ID: 4, WorkflowID: 1, HeadSHA: shaA, HeadBranch: "feat", Event: "pull_request", PullRequests: []int{7, 8}},
			}))
			bench.expect(replacement.want, "wait", "7", "--timeout", "30")
		})
	}
}

func TestACancelledCheckRejectsAHeadThatMovesDuringRunLookup(t *testing.T) {
	bench := newHarness(t, checkout(shaA))
	onPull(bench.hub, pull(7, "feat", shaA), pull(7, "feat", shaA), pull(7, "feat", shaB))
	onChecks(bench.hub, shaA, []githubapi.Check{checkRun("lint", "COMPLETED", "CANCELLED", 4), checkRun("lint", "COMPLETED", "SUCCESS", 5)})
	bench.hub.OnWorkflowRuns(githubapi.RunFilter{HeadSHA: shaA}, answer([]githubapi.WorkflowRun{
		{ID: 5, WorkflowID: 1, HeadSHA: shaA}, {ID: 4, WorkflowID: 1, HeadSHA: shaA},
	}))
	_, stderr := bench.expect(exitMoved, "wait", "7", "--timeout", "30")
	contains(t, stderr, "head of #7 changed")
}
