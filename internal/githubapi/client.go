// Package githubapi is jackstay's one GitHub client (docs/design/implementation.md, section 8.1; W-050):
// go-github with the project's own account's token, reads retried and writes never, GraphQL for
// checks and search. `jst release`, `jst lint pr`, `jst gh` and `jst tidy` ask GitHub through it; it
// does not read git remotes (the caller passes owner/name). githubfake is its fake for tests.
package githubapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
)

// Client is GitHub for one repository.
type Client struct {
	api      *github.Client
	owner    string
	name     string
	retry    Retry
	download *http.Client
}

type options struct {
	baseURL    string
	httpClient *http.Client
	retry      Retry
}

// Option changes how New builds a client.
type Option func(*options)

// WithBaseURL points the client at another API root (a test server); it ends in "/".
func WithBaseURL(url string) Option { return func(opts *options) { opts.baseURL = url } }

// WithHTTPClient is the HTTP client for the API and for log downloads.
func WithHTTPClient(client *http.Client) Option {
	return func(opts *options) { opts.httpClient = client }
}

// WithRetry replaces DefaultRetry.
func WithRetry(policy Retry) Option { return func(opts *options) { opts.retry = policy } }

// New is the client for repository (owner/name) acting with token.
func New(token, repository string, opts ...Option) (*Client, error) {
	owner, name, found := strings.Cut(repository, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("the repository %q is not owner/name", repository)
	}
	settings := options{retry: DefaultRetry(), httpClient: &http.Client{Timeout: 5 * time.Minute}}
	for _, opt := range opts {
		opt(&settings)
	}
	// go-github wraps its client's transport with the token; downloads go out on a copy without it.
	download, apiClient := *settings.httpClient, *settings.httpClient
	clientOptions := []github.ClientOptionsFunc{github.WithAuthToken(token), github.WithUserAgent("jackstay"),
		github.WithHTTPClient(&apiClient),
		// jackstay waits out limits itself (Read), on its own clock.
		github.WithDisableRateLimitCheck()}
	if settings.baseURL != "" {
		clientOptions = append(clientOptions, github.WithURLs(&settings.baseURL, &settings.baseURL))
	}
	api, err := github.NewClient(clientOptions...)
	if err != nil {
		return nil, err
	}
	return &Client{api: api, owner: owner, name: name, retry: settings.retry, download: &download}, nil
}

// API is go-github's client, for calls this package does not wrap (`jst release`'s releases).
func (client *Client) API() *github.Client { return client.api }

// Owner is the repository's owner.
func (client *Client) Owner() string { return client.owner }

// Name is the repository's name.
func (client *Client) Name() string { return client.name }

// Retry is the client's retry policy, for reads made through API.
func (client *Client) Retry() Retry { return client.retry }

// StatusOf is the HTTP status of a GitHub error answer, or 0.
func StatusOf(err error) int {
	var responseErr *github.ErrorResponse
	if errors.As(err, &responseErr) && responseErr.Response != nil {
		return responseErr.Response.StatusCode
	}
	return 0
}

// PullRequest is what jackstay reads of a pull request.
type PullRequest struct {
	Number         int
	NodeID         string
	State          string // "open" or "closed"
	Merged         bool   // closed with merged_at
	MergedAt       time.Time
	Draft          bool
	Title          string
	Body           string
	URL            string
	Author         string
	Head           string // the head branch
	HeadSHA        string
	HeadRepository string // owner/name; differs from BaseRepository for a fork
	Base           string
	BaseSHA        string
	BaseRepository string
	// Mergeable is "MERGEABLE", "CONFLICTING", or "UNKNOWN" while GitHub computes it (REST's null).
	Mergeable string
}

// PullRequestOf is jackstay's view of go-github's pull request.
func PullRequestOf(pull *github.PullRequest) PullRequest {
	mergeable := "UNKNOWN"
	if pull.Mergeable != nil {
		mergeable = map[bool]string{true: "MERGEABLE", false: "CONFLICTING"}[*pull.Mergeable]
	}
	return PullRequest{
		Number: pull.GetNumber(), NodeID: pull.GetNodeID(), State: pull.GetState(),
		Merged: pull.GetMerged() || pull.MergedAt != nil, MergedAt: pull.GetMergedAt().Time,
		Draft: pull.GetDraft(), Title: pull.GetTitle(), Body: pull.GetBody(), URL: pull.GetHTMLURL(),
		Author: pull.GetUser().GetLogin(),
		Head:   pull.GetHead().GetRef(), HeadSHA: pull.GetHead().GetSHA(), HeadRepository: pull.GetHead().GetRepo().GetFullName(),
		Base: pull.GetBase().GetRef(), BaseSHA: pull.GetBase().GetSHA(), BaseRepository: pull.GetBase().GetRepo().GetFullName(),
		Mergeable: mergeable,
	}
}

// PullRequestsByHead lists the pull requests whose head is head ("owner:branch", REST's filter;
// a bare branch matches nothing), in state "open", "closed" or "all". "Merged" is closed with
// merged_at: REST's list has no merged state.
func (client *Client) PullRequestsByHead(ctx context.Context, head, state string) ([]PullRequest, error) {
	if !strings.Contains(head, ":") {
		return nil, fmt.Errorf("the head %q is not owner:branch", head)
	}
	listing := &github.PullRequestListOptions{Head: head, State: state, ListOptions: github.ListOptions{PerPage: 100}}
	var out []PullRequest
	for {
		page, response, err := ReadPage(ctx, client.retry, func() ([]*github.PullRequest, *github.Response, error) {
			return client.api.PullRequests.List(ctx, client.owner, client.name, listing)
		})
		if err != nil {
			return nil, err
		}
		for _, pull := range page {
			out = append(out, PullRequestOf(pull))
		}
		if response == nil || response.NextPage == 0 {
			return out, nil
		}
		listing.Page = response.NextPage
	}
}

// PullRequest is one pull request by its number.
func (client *Client) PullRequest(ctx context.Context, number int) (PullRequest, error) {
	pull, err := Read(ctx, client.retry, func() (*github.PullRequest, error) {
		pull, _, err := client.api.PullRequests.Get(ctx, client.owner, client.name, number)
		return pull, err
	})
	if err != nil {
		return PullRequest{}, err
	}
	return PullRequestOf(pull), nil
}

// NewPullRequest is a pull request to open.
type NewPullRequest struct {
	Head, Base, Title, Body string
	Draft                   bool
}

// CreatePullRequest opens a pull request; a write, never retried.
func (client *Client) CreatePullRequest(ctx context.Context, request NewPullRequest) (PullRequest, error) {
	pull, _, err := client.api.PullRequests.Create(ctx, client.owner, client.name, github.CreatePullRequest{
		Title: &request.Title, Head: request.Head, Base: request.Base, Body: &request.Body, Draft: &request.Draft,
	})
	if err != nil {
		return PullRequest{}, err
	}
	return PullRequestOf(pull), nil
}

// EditPullRequestBody replaces a pull request's description; a write, never retried.
func (client *Client) EditPullRequestBody(ctx context.Context, number int, body string) error {
	_, _, err := client.api.PullRequests.Edit(ctx, client.owner, client.name, number, &github.PullRequest{Body: &body})
	return err
}

// MergeResult is GitHub's answer to a merge.
type MergeResult struct {
	Merged  bool
	SHA     string
	Message string
}

// Merge is a squash merge of the pull request whose head must still be headSHA, with the commit's
// title and body as given (an empty body stays empty); a write, never retried: an answer lost
// after it is settled by reading the pull request again.
func (client *Client) Merge(ctx context.Context, number int, headSHA, title, body string) (MergeResult, error) {
	result, _, err := client.api.PullRequests.Merge(ctx, client.owner, client.name, number, body, &github.PullRequestOptions{
		CommitTitle: title, SHA: headSHA, MergeMethod: "squash", DontDefaultIfBlank: true,
	})
	if err != nil {
		return MergeResult{}, err
	}
	return MergeResult{Merged: result.GetMerged(), SHA: result.GetSHA(), Message: result.GetMessage()}, nil
}

// DefaultBranch is the repository's default branch.
func (client *Client) DefaultBranch(ctx context.Context) (string, error) {
	repository, err := Read(ctx, client.retry, func() (*github.Repository, error) {
		repository, _, err := client.api.Repositories.Get(ctx, client.owner, client.name)
		return repository, err
	})
	if err != nil {
		return "", err
	}
	return repository.GetDefaultBranch(), nil
}

// Login is the login the token belongs to (GET /user).
func (client *Client) Login(ctx context.Context) (string, error) {
	user, err := Read(ctx, client.retry, func() (*github.User, error) {
		user, _, err := client.api.Users.Get(ctx, "")
		return user, err
	})
	if err != nil {
		return "", err
	}
	return user.GetLogin(), nil
}

// maxLog bounds a job log read into memory.
const maxLog = 64 << 20

// JobLog is a job's log: GitHub answers with a redirect to blob storage, read without the token.
func (client *Client) JobLog(ctx context.Context, jobID int64) (string, error) {
	location, err := Read(ctx, client.retry, func() (string, error) {
		where, _, err := client.api.Actions.GetWorkflowJobLogs(ctx, client.owner, client.name, jobID, 1)
		if err != nil {
			return "", err
		}
		return where.String(), nil
	})
	if err != nil {
		return "", err
	}
	return Read(ctx, client.retry, func() (string, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
		if err != nil {
			return "", err
		}
		response, err := client.download.Do(request)
		if err != nil {
			return "", err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			return "", &downloadError{status: response.StatusCode}
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, maxLog))
		return string(data), err
	})
}
