package githubapi

import (
	"context"
	"time"

	"github.com/google/go-github/v93/github"
)

// RunFilter chooses workflow runs; an empty field does not filter.
type RunFilter struct {
	Workflow string // the workflow's file name (ci.yml) or its numeric id; "" for every workflow
	Branch   string
	HeadSHA  string
	Event    string
}

// WorkflowRun is what jackstay reads of a workflow run.
type WorkflowRun struct {
	ID           int64
	Name         string
	WorkflowID   int64
	Status       string // queued, in_progress, completed, …
	Conclusion   string // success, failure, cancelled, … once completed
	HeadSHA      string
	HeadBranch   string
	Event        string
	PullRequests []int
	URL          string
	Attempt      int
	CreatedAt    time.Time
}

// runsPerCall is how many runs WorkflowRuns reads: the newest, in one page.
const runsPerCall = 100

// WorkflowRuns is the newest runs (up to 100, newest first) that the filter chooses.
func (client *Client) WorkflowRuns(ctx context.Context, filter RunFilter) ([]WorkflowRun, error) {
	listing := &github.ListWorkflowRunsOptions{Branch: filter.Branch, HeadSHA: filter.HeadSHA, Event: filter.Event,
		ListOptions: github.ListOptions{PerPage: runsPerCall}}
	runs, err := Read(ctx, client.retry, func() (*github.WorkflowRuns, error) {
		var runs *github.WorkflowRuns
		var err error
		if filter.Workflow == "" {
			runs, _, err = client.api.Actions.ListRepositoryWorkflowRuns(ctx, client.owner, client.name, listing)
		} else {
			runs, _, err = client.api.Actions.ListWorkflowRunsByFileName(ctx, client.owner, client.name, filter.Workflow, listing)
		}
		return runs, err
	})
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowRun, 0, len(runs.WorkflowRuns))
	for _, run := range runs.WorkflowRuns {
		associated := make([]int, 0, len(run.PullRequests))
		for _, pull := range run.PullRequests {
			if pull.GetNumber() > 0 {
				associated = append(associated, pull.GetNumber())
			}
		}
		out = append(out, WorkflowRun{ID: run.GetID(), Name: run.GetName(), WorkflowID: run.GetWorkflowID(),
			Status: run.GetStatus(), Conclusion: run.GetConclusion(), HeadSHA: run.GetHeadSHA(), HeadBranch: run.GetHeadBranch(),
			Event: run.GetEvent(), PullRequests: associated, URL: run.GetHTMLURL(), Attempt: run.GetRunAttempt(), CreatedAt: run.GetCreatedAt().Time})
	}
	return out, nil
}

// Step is a step of a job.
type Step struct {
	Number      int64
	Name        string
	Status      string
	Conclusion  string
	StartedAt   time.Time
	CompletedAt time.Time
}

// Job is a job of a workflow run, with its steps.
type Job struct {
	ID          int64
	RunID       int64
	Name        string
	Status      string
	Conclusion  string
	URL         string
	StartedAt   time.Time
	CompletedAt time.Time
	Steps       []Step
}

// Jobs is a run's jobs of its latest attempt, with their steps.
func (client *Client) Jobs(ctx context.Context, runID int64) ([]Job, error) {
	listing := &github.ListWorkflowJobsOptions{Filter: "latest", ListOptions: github.ListOptions{PerPage: 100}}
	var out []Job
	for {
		page, response, err := ReadPage(ctx, client.retry, func() ([]*github.WorkflowJob, *github.Response, error) {
			jobs, response, err := client.api.Actions.ListWorkflowJobs(ctx, client.owner, client.name, runID, listing)
			if err != nil {
				return nil, response, err
			}
			return jobs.Jobs, response, nil
		})
		if err != nil {
			return nil, err
		}
		for _, job := range page {
			out = append(out, jobOf(job))
		}
		if response == nil || response.NextPage == 0 {
			return out, nil
		}
		listing.Page = response.NextPage
	}
}

func jobOf(job *github.WorkflowJob) Job {
	out := Job{ID: job.GetID(), RunID: job.GetRunID(), Name: job.GetName(), Status: job.GetStatus(),
		Conclusion: job.GetConclusion(), URL: job.GetHTMLURL(), StartedAt: job.GetStartedAt().Time,
		CompletedAt: job.GetCompletedAt().Time}
	for _, step := range job.Steps {
		out.Steps = append(out.Steps, Step{Number: step.GetNumber(), Name: step.GetName(), Status: step.GetStatus(),
			Conclusion: step.GetConclusion(), StartedAt: step.GetStartedAt().Time, CompletedAt: step.GetCompletedAt().Time})
	}
	return out
}
