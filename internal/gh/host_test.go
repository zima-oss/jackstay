package gh

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"

	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/githubapi/githubfake"
	"github.com/zima-oss/jackstay/internal/hosting"
)

func hostOver(hub *fakeGitHub) *host { return newHost(hub, "owner/checkout") }

// answeredWith is a GitHub error answer with an HTTP status.
func answeredWith(status int) error {
	return &github.ErrorResponse{Response: &http.Response{StatusCode: status}, Message: http.StatusText(status)}
}

func TestAPullRequestIsAChange(t *testing.T) {
	open := pull(7, "feat", shaA)
	open.NodeID, open.BaseSHA, open.MergeCommitSHA, open.MergeableState, open.Draft = "PR_7", shaB, shaC, "BEHIND", true
	change := changeOf(open)
	want := hosting.Change{Number: 7, URL: open.URL, Title: "t", Body: "What it does.", State: "open", Draft: true,
		Head: "feat", HeadSHA: shaA, HeadRepository: "owner/checkout", Base: "main", BaseSHA: shaB, BaseRepository: "owner/checkout",
		TestMerge: shaC, Mergeable: "yes", Behind: true}
	if change != want {
		t.Fatalf("change %+v\nwant   %+v", change, want)
	}
	for mergeable, want := range map[string]string{"MERGEABLE": "yes", "CONFLICTING": "no", "UNKNOWN": "unknown", "": "unknown"} {
		open.Mergeable = mergeable
		if got := changeOf(open).Mergeable; got != want {
			t.Errorf("%q: %q, want %q", mergeable, got, want)
		}
	}
	landed := *merged(7, "feat", shaA)
	if change := changeOf(landed); change.State != "merged" || !change.Merged() || change.TestMerge != "" {
		t.Fatalf("a merged pull request: %+v", change)
	}
	closed := pull(7, "feat", shaA)
	closed.State, closed.MergeCommitSHA = "closed", shaC
	if change := changeOf(closed); change.State != "closed" || change.TestMerge != "" {
		t.Fatalf("a closed pull request has no test merge: %+v", change)
	}
}

func TestAChangeByNumberOrByItsHead(t *testing.T) {
	hub := newGitHub()
	adapter := hostOver(hub)
	ctx := context.Background()
	onPull(hub, pull(7, "feat", shaA))
	fork := pull(9, "feat", shaB)
	fork.HeadRepository = "someone/checkout"
	hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{fork, pull(7, "feat", shaA)}))
	hub.OnPullRequestsByHead("owner:none", "open", answer([]githubapi.PullRequest{}))
	other := pull(8, "two", shaB)
	other.Base = "release"
	hub.OnPullRequestsByHead("owner:two", "open", answer([]githubapi.PullRequest{pull(6, "two", shaA), other}))

	for _, ref := range []hosting.ChangeRef{{Number: 7}, {Head: "feat"}} {
		change, found, err := adapter.Change(ctx, ref)
		if err != nil || !found || change.Number != 7 {
			t.Fatalf("%+v: %+v %v %v", ref, change, found, err)
		}
	}
	if _, found, err := adapter.Change(ctx, hosting.ChangeRef{Head: "none"}); found || err != nil {
		t.Fatalf("no open change: found %v, %v", found, err)
	}
	_, _, err := adapter.Change(ctx, hosting.ChangeRef{Head: "two"})
	if err == nil || !strings.Contains(err.Error(), "#6 into main, #8 into release") {
		t.Fatalf("two open changes: %v", err)
	}
	if _, _, err := adapter.Change(ctx, hosting.ChangeRef{}); err == nil {
		t.Fatal("a change needs a number or a head")
	}
	changes, err := adapter.OpenChanges(ctx, "feat")
	if err != nil || len(changes) != 1 || changes[0].Number != 7 {
		t.Fatalf("open changes from feat, the fork's excluded: %+v %v", changes, err)
	}
}

func TestOpeningSettlesALostAnswerByLookingAgain(t *testing.T) {
	hub := newGitHub()
	hub.OnCreatePullRequest("feat", githubfake.Fail[githubapi.PullRequest](errors.New("connection reset")))
	hub.OnPullRequestsByHead("owner:feat", "open", answer([]githubapi.PullRequest{}), answer([]githubapi.PullRequest{pull(7, "feat", shaA)}))
	adapter := hostOver(hub)
	request := hosting.NewChange{Head: "feat", Base: "main", Title: "T", Body: "B"}
	if _, err := adapter.Open(context.Background(), request); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("lost, and not there: %v", err)
	}
	change, err := adapter.Open(context.Background(), request)
	if err != nil || change.Number != 7 {
		t.Fatalf("lost, and there: %+v %v", change, err)
	}
	if created := hub.Created(); len(created) != 2 || created[0] != (githubapi.NewPullRequest{Head: "feat", Base: "main", Title: "T", Body: "B"}) {
		t.Fatalf("created %+v", created)
	}
}

func TestChecksAreTheHeadsAndTheTestMerges(t *testing.T) {
	status := githubapi.Check{Name: "ci/external", Kind: githubapi.StatusContext, State: "SUCCESS", Link: "https://ci.example/1"}
	change := changeOf(func() githubapi.PullRequest { open := pull(7, "feat", shaA); open.MergeCommitSHA = shaC; return open }())
	for _, test := range []struct {
		name      string
		mergeRows githubfake.Result[[]githubapi.Check]
		onMerge   int
	}{
		{name: "the test merge has results", mergeRows: answer([]githubapi.Check{failed("gate", 0)}), onMerge: 1},
		{name: "the test merge has none", mergeRows: answer([]githubapi.Check{})},
		{name: "the test merge is not made", mergeRows: githubfake.Fail[[]githubapi.Check](&githubapi.CommitNotFoundError{SHA: shaC})},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := newGitHub()
			onChecks(hub, shaA, []githubapi.Check{passed("gate"), pending("lint"), status})
			hub.OnChecks(shaC, test.mergeRows)
			checks, err := hostOver(hub).Checks(context.Background(), change)
			if err != nil || len(checks) != 3+test.onMerge {
				t.Fatalf("%+v %v", checks, err)
			}
			want := []hosting.Check{
				{Name: "gate", App: 15368, State: hosting.Pass, Commit: shaA, Link: passed("gate").Link, Workflow: "ci"},
				{Name: "lint", App: 15368, State: hosting.Pending, Commit: shaA, Link: passed("gate").Link, Workflow: "ci"},
				{Name: "ci/external", State: hosting.Pass, Commit: shaA, Link: "https://ci.example/1"},
				{Name: "gate", App: 15368, State: hosting.Fail, Commit: shaC, OnTestMerge: true, Link: passed("gate").Link, Workflow: "ci"},
			}[:3+test.onMerge]
			if !slices.Equal(checks, want) {
				t.Fatalf("checks %+v\nwant   %+v", checks, want)
			}
		})
	}
	hub := newGitHub()
	onChecks(hub, shaA, []githubapi.Check{passed("gate")})
	hub.OnChecks(shaC, githubfake.Fail[[]githubapi.Check](errBadCredentials))
	if _, err := hostOver(hub).Checks(context.Background(), change); err == nil {
		t.Fatal("a failure to read the test merge's checks is not none")
	}
}

func TestEveryBucketIsAState(t *testing.T) {
	for _, bucket := range []githubapi.Bucket{githubapi.Pass, githubapi.Fail, githubapi.Pending, githubapi.Skipping, githubapi.Cancel} {
		if stateOf[bucket] == "" || string(stateOf[bucket]) != string(bucket) {
			t.Errorf("%s: %q", bucket, stateOf[bucket])
		}
	}
}

func TestASupersededRunIsReplacedWithANote(t *testing.T) {
	hub := newGitHub()
	onChecks(hub, shaA, []githubapi.Check{checkRun("lint", "COMPLETED", "CANCELLED", 4), checkRun("test", "COMPLETED", "CANCELLED", 4),
		checkRun("lint", "COMPLETED", "SUCCESS", 5)})
	hub.OnWorkflowRuns(githubapi.RunFilter{HeadSHA: shaA}, answer([]githubapi.WorkflowRun{
		{ID: 5, WorkflowID: 1, HeadSHA: shaA, Status: "in_progress"},
		{ID: 4, WorkflowID: 1, HeadSHA: shaA, Status: "completed", Conclusion: "cancelled"},
	}))
	checks, err := hostOver(hub).Checks(context.Background(), changeOf(pull(7, "feat", shaA)))
	if err != nil || len(checks) != 2 {
		t.Fatalf("%+v %v", checks, err)
	}
	// test: the newer run's own job has no result yet, so it is pending; lint: the newer run's job passed.
	if checks[0].Name != "test" || checks[0].State != hosting.Pending || checks[0].Note != "run 5 supersedes cancelled run 4 of ci for test" {
		t.Fatalf("test %+v", checks[0])
	}
	if checks[1].Name != "lint" || checks[1].State != hosting.Pass || checks[1].Note != "run 5 supersedes cancelled run 4 of ci for lint" {
		t.Fatalf("lint %+v", checks[1])
	}
}

func TestExplainShowsEachFailedChecksLog(t *testing.T) {
	hub := newGitHub()
	failing := failed("test", 123)
	failing.Link = "https://github.com/owner/checkout/actions/runs/123/job/9"
	onChecks(hub, shaA, []githubapi.Check{passed("lint"), failing, {Name: "ci/external", Kind: githubapi.StatusContext, State: "ERROR", Link: "https://ci.example/1"}})
	hub.OnJobs(123, answer([]githubapi.Job{{ID: 9, Name: "test", Conclusion: "failure", Steps: []githubapi.Step{{Name: "Run tests", Conclusion: "failure"}}}}))
	hub.OnJobLog(9, answer("one\ntwo\nthree\n"))
	adapter := hostOver(hub)
	checks, err := adapter.Checks(context.Background(), changeOf(pull(7, "feat", shaA)))
	if err != nil {
		t.Fatal(err)
	}
	explained := adapter.Explain(context.Background(), checks, 2)
	contains(t, explained, "--- test — "+failing.Link)
	contains(t, explained, "    two\n    three")
	contains(t, explained, "--- ci/external — https://ci.example/1")
	contains(t, explained, "no workflow run behind this check")
	lacks(t, explained, "lint")
	if strings.HasPrefix(explained, "\n") {
		t.Fatalf("starts with a blank line: %q", explained)
	}
}

func TestRulesAreRead(t *testing.T) {
	hub := newGitHub()
	hub.rules = BranchRules{Required: []RequiredCheck{{Name: "gate", AppID: 15368}, {Name: "lint"}}, UpToDate: true, Queue: true}
	hub.methods, hub.methodsRead = []string{"squash", "rebase"}, true
	rules, err := hostOver(hub).Rules(context.Background(), "main")
	if err != nil {
		t.Fatal(err)
	}
	want := hosting.Rules{ChecksRead: true, Required: []hosting.RequiredCheck{{Name: "gate", App: 15368}, {Name: "lint"}},
		UpToDateRead: true, UpToDate: true, QueueRead: true, Queue: true, MethodsRead: true, Methods: []hosting.Method{hosting.Squash, hosting.Rebase}}
	if !rulesEqual(rules, want) {
		t.Fatalf("rules %+v\nwant  %+v", rules, want)
	}
}

func TestRulesGitHubRefusesAreUnreadAndSaySo(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		hub := newGitHub()
		hub.rulesErr = answeredWith(status)
		hub.methodsErr = answeredWith(status)
		rules, err := hostOver(hub).Rules(context.Background(), "main")
		if err != nil {
			t.Fatal(err)
		}
		if rules.ChecksRead || rules.UpToDateRead || rules.QueueRead || rules.MethodsRead {
			t.Fatalf("%d: %+v", status, rules)
		}
		contains(t, rules.Unread, "GitHub refused to show the rules of main")
		contains(t, rules.Unread, "plan or this account's permission")
		contains(t, rules.Unread, "merge methods owner/checkout allows")
	}
	hub := newGitHub()
	rules, err := hostOver(hub).Rules(context.Background(), "main")
	if err != nil || !rules.ChecksRead || rules.MethodsRead || !strings.Contains(rules.Unread, "does not show the merge methods") {
		t.Fatalf("methods not shown: %+v %v", rules, err)
	}
	if !rules.ChecksRead || len(rules.Required) != 0 {
		t.Fatalf("rules read and requiring none: %+v", rules)
	}
	for _, failure := range []func(*fakeGitHub){
		func(hub *fakeGitHub) { hub.rulesErr = answeredWith(http.StatusBadGateway) },
		func(hub *fakeGitHub) { hub.methodsErr = errBadCredentials },
	} {
		hub := newGitHub()
		failure(hub)
		if _, err := hostOver(hub).Rules(context.Background(), "main"); err == nil {
			t.Fatal("a failure that is not a refusal is an error")
		}
	}
}

func rulesEqual(left, right hosting.Rules) bool {
	return left.ChecksRead == right.ChecksRead && slices.Equal(left.Required, right.Required) &&
		left.UpToDateRead == right.UpToDateRead && left.UpToDate == right.UpToDate &&
		left.QueueRead == right.QueueRead && left.Queue == right.Queue &&
		left.MethodsRead == right.MethodsRead && slices.Equal(left.Methods, right.Methods) && left.Unread == right.Unread
}

func TestMergeAsksTheMethodAtTheExpectedHeadOnce(t *testing.T) {
	hub := newGitHub()
	hub.OnMerge(7, answer(githubapi.MergeResult{Merged: true, SHA: shaC}), githubfake.Fail[githubapi.MergeResult](errors.New("lost")))
	adapter := hostOver(hub)
	change := changeOf(pull(7, "feat", shaA))
	result, err := adapter.Merge(context.Background(), change, hosting.MergeRequest{Method: hosting.Rebase, ExpectedHead: shaA, Title: "T (#7)", Body: "B"})
	if err != nil || result != (hosting.Merged{Merged: true, SHA: shaC}) {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := adapter.Merge(context.Background(), change, hosting.MergeRequest{Method: hosting.Squash, ExpectedHead: shaA}); err == nil {
		t.Fatal("a lost answer is the caller's to settle")
	}
	merges := hub.Merges()
	if len(merges) != 2 || merges[0] != (githubfake.MergeCall{Number: 7, Method: "rebase", HeadSHA: shaA, Title: "T (#7)", Message: "B"}) ||
		merges[1].Method != "squash" {
		t.Fatalf("merges %+v", merges)
	}
	for _, request := range []hosting.MergeRequest{{Method: "fast-forward", ExpectedHead: shaA}, {Method: hosting.Squash}} {
		if _, err := adapter.Merge(context.Background(), change, request); err == nil {
			t.Fatalf("%+v was sent", request)
		}
	}
	if len(hub.Merges()) != 2 {
		t.Fatal("a refused request reached GitHub")
	}
}

func TestEnqueueSendsThePullRequestsNodeAndTheExpectedHead(t *testing.T) {
	hub := newGitHub()
	open := pull(7, "feat", shaA)
	open.NodeID = "PR_7"
	onPull(hub, open)
	hub.OnEnqueue("PR_7", nil)
	if err := hostOver(hub).Enqueue(context.Background(), changeOf(open), shaA); err != nil {
		t.Fatal(err)
	}
	if queued := hub.Enqueues(); len(queued) != 1 || queued[0] != (githubfake.EnqueueCall{PullRequestID: "PR_7", ExpectedHead: shaA}) {
		t.Fatalf("queued %+v", queued)
	}
}

func TestUpdateBranchAndTheDefaultBranchAreGitHubs(t *testing.T) {
	hub := newGitHub()
	hub.OnDefaultBranch(answer("trunk"))
	adapter := hostOver(hub)
	if branch, err := adapter.DefaultBranch(context.Background()); err != nil || branch != "trunk" {
		t.Fatalf("%q %v", branch, err)
	}
	if err := adapter.UpdateBranch(context.Background(), changeOf(pull(7, "feat", shaB)), shaA); err != nil || !slices.Equal(hub.updates, []string{shaA}) {
		t.Fatalf("updates %v %v", hub.updates, err)
	}
	if adapter.Name() != "github" {
		t.Fatal(adapter.Name())
	}
}
