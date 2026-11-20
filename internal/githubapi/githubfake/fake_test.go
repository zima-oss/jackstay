package githubfake

import (
	"context"
	"errors"
	"testing"

	"github.com/zima-oss/jackstay/internal/githubapi"
)

// The calls `jst checks`, `jst land` and `jst tidy` make; the client and the fake both answer them.
type gitHub interface {
	PullRequestsByHead(ctx context.Context, head, state string) ([]githubapi.PullRequest, error)
	PullRequest(ctx context.Context, number int) (githubapi.PullRequest, error)
	SearchPullRequestsBySHA(ctx context.Context, sha string) ([]githubapi.PullRequest, error)
	Checks(ctx context.Context, sha string) ([]githubapi.Check, error)
	CreatePullRequest(ctx context.Context, request githubapi.NewPullRequest) (githubapi.PullRequest, error)
	EditPullRequestBody(ctx context.Context, number int, body string) error
	Merge(ctx context.Context, number int, method, headSHA, title, body string) (githubapi.MergeResult, error)
	Enqueue(ctx context.Context, pullRequestID, expectedHead string) error
	WorkflowRuns(ctx context.Context, filter githubapi.RunFilter) ([]githubapi.WorkflowRun, error)
	Jobs(ctx context.Context, runID int64) ([]githubapi.Job, error)
	JobLog(ctx context.Context, jobID int64) (string, error)
	DefaultBranch(ctx context.Context) (string, error)
}

var (
	_ gitHub = (*githubapi.Client)(nil)
	_ gitHub = (*Fake)(nil)
)

func TestASequenceThenItsLastAnswerRepeats(t *testing.T) {
	fake := New()
	pending := []githubapi.Check{{Name: "test", Kind: githubapi.CheckRun, Status: "IN_PROGRESS"}}
	passed := []githubapi.Check{{Name: "test", Kind: githubapi.CheckRun, Status: "COMPLETED", Conclusion: "SUCCESS"}}
	fake.OnChecks("h1", Ok(pending), Ok(passed))
	var got []githubapi.Bucket
	for range 3 {
		checks, err := fake.Checks(context.Background(), "h1")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, checks[0].Bucket())
	}
	if got[0] != githubapi.Pending || got[1] != githubapi.Pass || got[2] != githubapi.Pass {
		t.Fatalf("buckets %v", got)
	}
	var none *NoAnswerError
	if _, err := fake.Checks(context.Background(), "h2"); !errors.As(err, &none) {
		t.Fatalf("an unprogrammed request answered %v", err)
	}
	if calls := fake.Calls(); len(calls) != 4 || calls[3] != (Call{Op: "Checks", Key: "h2"}) {
		t.Fatalf("calls %v", calls)
	}
}

func TestWritesAreRecorded(t *testing.T) {
	fake := New()
	ctx := context.Background()
	fake.OnCreatePullRequest("feature", Ok(githubapi.PullRequest{Number: 7}))
	fake.OnEditPullRequestBody(7, nil)
	fake.OnMerge(7, Fail[githubapi.MergeResult](errors.New("405")), Ok(githubapi.MergeResult{Merged: true, SHA: "m1"}))
	if pull, err := fake.CreatePullRequest(ctx, githubapi.NewPullRequest{Head: "feature", Base: "main", Title: "T"}); err != nil || pull.Number != 7 {
		t.Fatalf("%+v %v", pull, err)
	}
	if err := fake.EditPullRequestBody(ctx, 7, "B"); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.Merge(ctx, 7, "squash", "h1", "T (#7)", "B"); err == nil {
		t.Fatal("the first merge answer is an error")
	}
	if merged, err := fake.Merge(ctx, 7, "rebase", "h2", "T (#7)", "B"); err != nil || !merged.Merged {
		t.Fatalf("%+v %v", merged, err)
	}
	if created := fake.Created(); len(created) != 1 || created[0].Title != "T" {
		t.Fatalf("created %v", created)
	}
	if edits := fake.BodyEdits(); len(edits) != 1 || edits[0] != (BodyEdit{Number: 7, Body: "B"}) {
		t.Fatalf("edits %v", edits)
	}
	if merges := fake.Merges(); len(merges) != 2 || merges[1] != (MergeCall{Number: 7, Method: "rebase", HeadSHA: "h2", Title: "T (#7)", Message: "B"}) {
		t.Fatalf("merges %v", merges)
	}
}

func TestEveryReadAnswersWhatWasProgrammed(t *testing.T) {
	fake := New()
	ctx := context.Background()
	filter := githubapi.RunFilter{Workflow: "ci.yml", HeadSHA: "h1"}
	fake.OnPullRequestsByHead("o:feature", "all", Ok([]githubapi.PullRequest{{Number: 1}}))
	fake.OnPullRequest(1, Ok(githubapi.PullRequest{Number: 1, Merged: true}))
	fake.OnSearchPullRequestsBySHA("h1", Ok([]githubapi.PullRequest{{Number: 2}}))
	fake.OnWorkflowRuns(filter, Ok([]githubapi.WorkflowRun{{ID: 9}}))
	fake.OnJobs(9, Ok([]githubapi.Job{{ID: 11}}))
	fake.OnJobLog(11, Ok("log"))
	fake.OnDefaultBranch(Ok("main"))
	byHead, _ := fake.PullRequestsByHead(ctx, "o:feature", "all")
	one, _ := fake.PullRequest(ctx, 1)
	found, _ := fake.SearchPullRequestsBySHA(ctx, "h1")
	runs, _ := fake.WorkflowRuns(ctx, filter)
	jobs, _ := fake.Jobs(ctx, 9)
	log, _ := fake.JobLog(ctx, 11)
	branch, _ := fake.DefaultBranch(ctx)
	if byHead[0].Number != 1 || !one.Merged || found[0].Number != 2 || runs[0].ID != 9 || jobs[0].ID != 11 || log != "log" || branch != "main" {
		t.Fatal("an answer is not what was programmed")
	}
	if _, err := fake.WorkflowRuns(ctx, githubapi.RunFilter{Workflow: "ci.yml"}); err == nil {
		t.Fatal("another filter is another request")
	}
}
