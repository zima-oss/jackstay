package gh

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/go-github/v93/github"

	"github.com/zima-oss/jackstay/internal/githubapi"
)

// client is internal/githubapi's client with the requests this package needs that it does not
// wrap, made through its go-github client with its retry rule.
type client struct {
	*githubapi.Client
}

// RequiredCheck is a check a branch's rules require: its name, and the app that must report it
// (0: any).
type RequiredCheck struct {
	Name  string
	AppID int64
}

// BranchRules are what a branch's rules ask of a merge.
type BranchRules struct {
	Required []RequiredCheck
	// UpToDate: the branch must contain the base's tip (a ruleset's strict policy, or classic
	// protection's strict).
	UpToDate bool
	// Queue: a ruleset requires the merge queue.
	Queue bool
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

// BranchRules are a branch's rules: the required_status_checks rules of the rulesets that apply
// to it (GitHub lists the active ones, paged) and its classic protection's required checks, each
// with the app that must report it when one is named; whether either requires the branch up to
// date; whether a ruleset requires the merge queue.
func (api client) BranchRules(ctx context.Context, branch string) (BranchRules, error) {
	var answer BranchRules
	listing := &github.ListOptions{PerPage: 100}
	for {
		var response *github.Response
		rules, err := githubapi.Read(ctx, api.Retry(), func() (*github.BranchRules, error) {
			rules, answer, err := api.API().Repositories.ListRulesForBranch(ctx, api.Owner(), api.Name(), branch, listing)
			response = answer
			return rules, err
		})
		if err != nil {
			return BranchRules{}, fmt.Errorf("the rules of %s: %w", branch, err)
		}
		if rules != nil {
			for _, rule := range rules.RequiredStatusChecks {
				answer.UpToDate = answer.UpToDate || rule.Parameters.StrictRequiredStatusChecksPolicy
				for _, check := range rule.Parameters.RequiredStatusChecks {
					answer.Required = append(answer.Required, RequiredCheck{Name: check.Context, AppID: check.GetIntegrationID()})
				}
			}
			answer.Queue = answer.Queue || len(rules.MergeQueue) > 0
		}
		if response == nil || response.NextPage == 0 {
			break
		}
		listing.Page = response.NextPage
	}
	found, err := githubapi.Read(ctx, api.Retry(), func() (*github.Branch, error) {
		found, _, err := api.API().Repositories.GetBranch(ctx, api.Owner(), api.Name(), branch, 1)
		return found, err
	})
	if err != nil {
		return BranchRules{}, fmt.Errorf("the protection of %s: %w", branch, err)
	}
	if protection := found.GetProtection(); found.GetProtected() && protection != nil && protection.RequiredStatusChecks != nil {
		answer.UpToDate = answer.UpToDate || protection.RequiredStatusChecks.Strict
		if checks := protection.RequiredStatusChecks.Checks; checks != nil && len(*checks) > 0 {
			for _, check := range *checks {
				answer.Required = append(answer.Required, RequiredCheck{Name: check.Context, AppID: max(check.GetAppID(), 0)}) // -1: any app
			}
		} else if contexts := protection.RequiredStatusChecks.Contexts; contexts != nil {
			for _, name := range *contexts {
				answer.Required = append(answer.Required, RequiredCheck{Name: name})
			}
		}
	}
	slices.SortFunc(answer.Required, func(left, right RequiredCheck) int {
		if left.Name != right.Name {
			return strings.Compare(left.Name, right.Name)
		}
		return cmp.Compare(left.AppID, right.AppID)
	})
	answer.Required = slices.Compact(answer.Required)
	return answer, nil
}

// MergeMethods are the merge methods the repository allows, by its allow_squash_merge,
// allow_merge_commit and allow_rebase_merge; GitHub leaves them out for an account that may not
// administer the repository, and then read is false.
func (api client) MergeMethods(ctx context.Context) ([]string, bool, error) {
	repository, err := githubapi.Read(ctx, api.Retry(), func() (*github.Repository, error) {
		repository, _, err := api.API().Repositories.Get(ctx, api.Owner(), api.Name())
		return repository, err
	})
	if err != nil {
		return nil, false, err
	}
	if repository.AllowSquashMerge == nil && repository.AllowMergeCommit == nil && repository.AllowRebaseMerge == nil {
		return nil, false, nil
	}
	var methods []string
	for _, each := range []struct {
		method  string
		allowed bool
	}{
		{"squash", repository.GetAllowSquashMerge()},
		{"merge", repository.GetAllowMergeCommit()},
		{"rebase", repository.GetAllowRebaseMerge()},
	} {
		if each.allowed {
			methods = append(methods, each.method)
		}
	}
	return methods, true, nil
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
