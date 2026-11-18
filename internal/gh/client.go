package gh

import (
	"context"
	"errors"

	"github.com/google/go-github/v93/github"

	"github.com/zima-oss/jackstay/internal/githubapi"
)

// client is internal/githubapi's client with the three requests jst gh needs that it does not
// wrap, made through its go-github client with its retry rule (reads only).
type client struct {
	*githubapi.Client
}

// connectReal is GitHub as docs/design/implementation.md section 8.1 says: the process's environment, the account's token.
func connectReal(ctx context.Context, account, repository string) (GitHub, githubapi.Token, error) {
	connected, token, err := githubapi.Connect(ctx, githubapi.OSEnvironment(), account, repository)
	if err != nil {
		return nil, githubapi.Token{}, err
	}
	return client{Client: connected}, token, nil
}

// WorkflowRun is one run by its id.
func (api client) WorkflowRun(ctx context.Context, runID int64) (githubapi.WorkflowRun, error) {
	run, err := githubapi.Read(ctx, api.Retry(), func() (*github.WorkflowRun, error) {
		run, _, err := api.API().Actions.GetWorkflowRunByID(ctx, api.Owner(), api.Name(), runID)
		return run, err
	})
	if err != nil {
		return githubapi.WorkflowRun{}, err
	}
	return githubapi.WorkflowRun{ID: run.GetID(), Name: run.GetName(), WorkflowID: run.GetWorkflowID(),
		Status: run.GetStatus(), Conclusion: run.GetConclusion(), HeadSHA: run.GetHeadSHA(), HeadBranch: run.GetHeadBranch(),
		Event: run.GetEvent(), URL: run.GetHTMLURL(), Attempt: run.GetRunAttempt(), CreatedAt: run.GetCreatedAt().Time}, nil
}

// MergeableState is REST's mergeable_state of a pull request.
func (api client) MergeableState(ctx context.Context, number int) (string, error) {
	pull, err := githubapi.Read(ctx, api.Retry(), func() (*github.PullRequest, error) {
		pull, _, err := api.API().PullRequests.Get(ctx, api.Owner(), api.Name(), number)
		return pull, err
	})
	if err != nil {
		return "", err
	}
	return pull.GetMergeableState(), nil
}

// UpdateBranch asks GitHub to merge the base into the branch; GitHub answers 202 and does it in
// the background, which is success here.
func (api client) UpdateBranch(ctx context.Context, number int, headSHA string) error {
	_, _, err := api.API().PullRequests.UpdateBranch(ctx, api.Owner(), api.Name(), number,
		github.UpdatePullRequestBranchRequest{ExpectedHeadSHA: &headSHA})
	var accepted *github.AcceptedError
	if errors.As(err, &accepted) {
		return nil
	}
	return err
}
