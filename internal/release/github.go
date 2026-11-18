package release

import (
	"context"
	"fmt"

	"github.com/google/go-github/v93/github"

	"github.com/zima-oss/jackstay/internal/githubapi"
)

// PullRequest is what the workflow reads of a pull request.
type PullRequest struct {
	Number  int
	NodeID  string
	Head    string // the head branch
	HeadSHA string
	Base    string
	BaseSHA string
	// HeadRepository and BaseRepository are owner/name; they differ for a fork.
	HeadRepository string
	BaseRepository string
	Draft          bool
	State          string // "open" or "closed"
	Merged         bool
	Author         string
	Title          string
	Body           string
}

// Release is what the workflow reads of a GitHub release.
type Release struct {
	ID         int64
	Tag        string
	Name       string
	Body       string
	Draft      bool
	Prerelease bool
	Target     string
}

// Edit is an edit of a release; nil fields are left as they are.
type Edit struct {
	Name       *string
	Body       *string
	Draft      *bool
	Prerelease *bool
	MakeLatest *string // "true" or "false"
}

// GitHub is everything the workflow's two commands ask of GitHub (an interface here, where it is
// used; goGitHub is the real one and the tests use a fake).
type GitHub interface {
	OpenPullRequests(ctx context.Context) ([]PullRequest, error)
	PullRequest(ctx context.Context, number int) (PullRequest, error)
	CreatePullRequest(ctx context.Context, head, base, title, body string, draft bool) (PullRequest, error)
	UpdatePullRequest(ctx context.Context, number int, title, body string) error
	ClosePullRequest(ctx context.Context, number int, comment string) error
	ConvertToDraft(ctx context.Context, nodeID string) error
	Releases(ctx context.Context) ([]Release, error)
	CreateRelease(ctx context.Context, tag, target, name, body string, draft bool) (Release, error)
	EditRelease(ctx context.Context, id int64, change Edit) (Release, error)
}

// goGitHub is GitHub's REST API through jackstay's one client (internal/githubapi: its token, its
// retries of reads), with GraphQL for turning a pull request into a draft (REST cannot).
type goGitHub struct {
	jackstayClient *githubapi.Client
	client         *github.Client
	owner          string
	repo           string
}

// NewGitHub is the client for owner/repo with a token.
func NewGitHub(token, ownerAndRepo string, options ...githubapi.Option) (GitHub, error) {
	client, err := githubapi.New(token, ownerAndRepo, options...)
	if err != nil {
		return nil, err
	}
	return gitHubOf(client), nil
}

// gitHubOf is the workflow's GitHub on jackstay's client.
func gitHubOf(client *githubapi.Client) GitHub {
	return goGitHub{jackstayClient: client, client: client.API(), owner: client.Owner(), repo: client.Name()}
}

func pullRequestOf(pull *github.PullRequest) PullRequest {
	return PullRequest{
		Number: pull.GetNumber(), NodeID: pull.GetNodeID(), Head: pull.GetHead().GetRef(), HeadSHA: pull.GetHead().GetSHA(),
		Base: pull.GetBase().GetRef(), BaseSHA: pull.GetBase().GetSHA(),
		HeadRepository: pull.GetHead().GetRepo().GetFullName(), BaseRepository: pull.GetBase().GetRepo().GetFullName(), Draft: pull.GetDraft(), State: pull.GetState(), Merged: pull.GetMerged(),
		Author: pull.GetUser().GetLogin(), Title: pull.GetTitle(), Body: pull.GetBody(),
	}
}

func releaseOf(release *github.RepositoryRelease) Release {
	return Release{ID: release.GetID(), Tag: release.GetTagName(), Name: release.GetName(), Body: release.GetBody(),
		Draft: release.GetDraft(), Prerelease: release.GetPrerelease(), Target: release.GetTargetCommitish()}
}

func (client goGitHub) OpenPullRequests(ctx context.Context) ([]PullRequest, error) {
	options := &github.PullRequestListOptions{State: "open", ListOptions: github.ListOptions{PerPage: 100}}
	var out []PullRequest
	for {
		page, response, err := githubapi.ReadPage(ctx, client.jackstayClient.Retry(), func() ([]*github.PullRequest, *github.Response, error) {
			return client.client.PullRequests.List(ctx, client.owner, client.repo, options)
		})
		if err != nil {
			return nil, err
		}
		for _, pull := range page {
			out = append(out, pullRequestOf(pull))
		}
		if response.NextPage == 0 {
			return out, nil
		}
		options.Page = response.NextPage
	}
}

func (client goGitHub) PullRequest(ctx context.Context, number int) (PullRequest, error) {
	pull, err := githubapi.Read(ctx, client.jackstayClient.Retry(), func() (*github.PullRequest, error) {
		pull, _, err := client.client.PullRequests.Get(ctx, client.owner, client.repo, number)
		return pull, err
	})
	if err != nil {
		return PullRequest{}, err
	}
	return pullRequestOf(pull), nil
}

func (client goGitHub) CreatePullRequest(ctx context.Context, head, base, title, body string, draft bool) (PullRequest, error) {
	pull, _, err := client.client.PullRequests.Create(ctx, client.owner, client.repo, github.CreatePullRequest{
		Title: &title, Head: head, Base: base, Body: &body, Draft: &draft,
	})
	if err != nil {
		return PullRequest{}, err
	}
	return pullRequestOf(pull), nil
}

func (client goGitHub) UpdatePullRequest(ctx context.Context, number int, title, body string) error {
	_, _, err := client.client.PullRequests.Edit(ctx, client.owner, client.repo, number, &github.PullRequest{Title: &title, Body: &body})
	return err
}

func (client goGitHub) ClosePullRequest(ctx context.Context, number int, comment string) error {
	if comment != "" {
		if _, _, err := client.client.Issues.CreateComment(ctx, client.owner, client.repo, number, github.IssueCommentRequest{Body: comment}); err != nil {
			return err
		}
	}
	closed := "closed"
	_, _, err := client.client.PullRequests.Edit(ctx, client.owner, client.repo, number, &github.PullRequest{State: &closed})
	return err
}

// ConvertToDraft is GraphQL's convertPullRequestToDraft; a write, never retried.
func (client goGitHub) ConvertToDraft(ctx context.Context, nodeID string) error {
	err := client.jackstayClient.Mutate(ctx, "mutation($id: ID!) { convertPullRequestToDraft(input: {pullRequestId: $id}) { pullRequest { isDraft } } }",
		map[string]any{"id": nodeID}, nil)
	if err != nil {
		return fmt.Errorf("convertPullRequestToDraft: %w", err)
	}
	return nil
}

func (client goGitHub) Releases(ctx context.Context) ([]Release, error) {
	options := &github.ListOptions{PerPage: 100}
	var out []Release
	for {
		page, response, err := githubapi.ReadPage(ctx, client.jackstayClient.Retry(), func() ([]*github.RepositoryRelease, *github.Response, error) {
			return client.client.Repositories.ListReleases(ctx, client.owner, client.repo, options)
		})
		if err != nil {
			return nil, err
		}
		for _, release := range page {
			out = append(out, releaseOf(release))
		}
		if response.NextPage == 0 {
			return out, nil
		}
		options.Page = response.NextPage
	}
}

func (client goGitHub) CreateRelease(ctx context.Context, tag, target, name, body string, draft bool) (Release, error) {
	// "legacy": GitHub picks "latest" by its own rule (date and version), W-047.
	prerelease, latest := false, "legacy"
	created, _, err := client.client.Repositories.CreateRelease(ctx, client.owner, client.repo, github.CreateReleaseRequest{
		TagName: tag, TargetCommitish: &target, Name: &name, Body: &body, Draft: &draft, Prerelease: &prerelease, MakeLatest: &latest,
	})
	if err != nil {
		return Release{}, err
	}
	return releaseOf(created), nil
}

func (client goGitHub) EditRelease(ctx context.Context, id int64, change Edit) (Release, error) {
	edited, _, err := client.client.Repositories.UpdateRelease(ctx, client.owner, client.repo, id, github.UpdateReleaseRequest{
		Name: change.Name, Body: change.Body, Draft: change.Draft, Prerelease: change.Prerelease, MakeLatest: change.MakeLatest,
	})
	if err != nil {
		return Release{}, err
	}
	return releaseOf(edited), nil
}
