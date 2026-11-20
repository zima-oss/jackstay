package githubapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
)

// GraphQLError is a GraphQL answer that carries errors (an HTTP 200).
type GraphQLError struct{ Messages []string }

func (err *GraphQLError) Error() string { return "GitHub GraphQL: " + strings.Join(err.Messages, "; ") }

type graphQLAnswer struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"errors"`
}

// post sends one GraphQL request through go-github's client (its token, its base URL) and
// decodes data into out.
func (client *Client) post(ctx context.Context, query string, variables map[string]any, out any) error {
	request, err := client.api.NewRequest(ctx, http.MethodPost, "graphql", map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	var answer graphQLAnswer
	response, err := client.api.Do(request, &answer)
	if err != nil {
		return err
	}
	if len(answer.Errors) > 0 {
		messages := make([]string, 0, len(answer.Errors))
		for _, problem := range answer.Errors {
			if problem.Type == "RATE_LIMITED" && response != nil {
				return &github.RateLimitError{Rate: response.Rate, Response: response.Response, Message: problem.Message}
			}
			messages = append(messages, problem.Message)
		}
		return &GraphQLError{Messages: messages}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(answer.Data, out)
}

// Mutate is a GraphQL mutation; a write, never retried.
func (client *Client) Mutate(ctx context.Context, query string, variables map[string]any, out any) error {
	return client.post(ctx, query, variables, out)
}

const enqueueMutation = `mutation($pullRequestId: ID!, $expectedHeadOid: GitObjectID) {
  enqueuePullRequest(input: {pullRequestId: $pullRequestId, expectedHeadOid: $expectedHeadOid}) {
    mergeQueueEntry { id }
  }
}`

// Enqueue puts a pull request (by its node id) in its base's merge queue while its head is still
// expectedHead; a write, never retried.
func (client *Client) Enqueue(ctx context.Context, pullRequestID, expectedHead string) error {
	if pullRequestID == "" {
		return fmt.Errorf("the pull request's node id is unknown, so it cannot be put in the merge queue")
	}
	var data struct {
		EnqueuePullRequest *struct {
			MergeQueueEntry *struct {
				ID string `json:"id"`
			} `json:"mergeQueueEntry"`
		} `json:"enqueuePullRequest"`
	}
	variables := map[string]any{"pullRequestId": pullRequestID, "expectedHeadOid": expectedHead}
	if err := client.Mutate(ctx, enqueueMutation, variables, &data); err != nil {
		return err
	}
	if data.EnqueuePullRequest == nil || data.EnqueuePullRequest.MergeQueueEntry == nil {
		return fmt.Errorf("GitHub answered the merge queue request without a queue entry")
	}
	return nil
}

// Query is a read-only GraphQL query, retried as every read.
func (client *Client) Query(ctx context.Context, query string, variables map[string]any, out any) error {
	_, err := Read(ctx, client.retry, func() (struct{}, error) {
		return struct{}{}, client.post(ctx, query, variables, out)
	})
	return err
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

const searchQuery = `query($q: String!, $after: String) {
  search(query: $q, type: ISSUE, first: 100, after: $after) {
    pageInfo { hasNextPage endCursor }
    nodes { ... on PullRequest {
      number id state isDraft mergedAt title body url headRefName headRefOid baseRefName baseRefOid
      author { login } headRepository { nameWithOwner } repository { nameWithOwner } mergeable
    } }
  }
}`

type searchNode struct {
	Number         int        `json:"number"`
	ID             string     `json:"id"`
	State          string     `json:"state"`
	IsDraft        bool       `json:"isDraft"`
	MergedAt       *time.Time `json:"mergedAt"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	URL            string     `json:"url"`
	HeadRefName    string     `json:"headRefName"`
	HeadRefOid     string     `json:"headRefOid"`
	BaseRefName    string     `json:"baseRefName"`
	BaseRefOid     string     `json:"baseRefOid"`
	Mergeable      string     `json:"mergeable"`
	Author         struct{ Login string }
	HeadRepository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"headRepository"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

// SearchPullRequestsBySHA is the pull requests of this repository that hold a commit: GraphQL
// `search` (type ISSUE) of `is:pr <sha> repo:owner/name`, what `gh pr list --search` runs.
func (client *Client) SearchPullRequestsBySHA(ctx context.Context, sha string) ([]PullRequest, error) {
	variables := map[string]any{"q": fmt.Sprintf("is:pr %s repo:%s/%s", sha, client.owner, client.name), "after": nil}
	var out []PullRequest
	for {
		var data struct {
			Search struct {
				PageInfo pageInfo     `json:"pageInfo"`
				Nodes    []searchNode `json:"nodes"`
			} `json:"search"`
		}
		if err := client.Query(ctx, searchQuery, variables, &data); err != nil {
			return nil, err
		}
		for _, node := range data.Search.Nodes {
			if node.Number == 0 {
				continue // an issue: the fragment selects nothing of it
			}
			out = append(out, node.pullRequest())
		}
		if !data.Search.PageInfo.HasNextPage {
			return out, nil
		}
		variables["after"] = data.Search.PageInfo.EndCursor
	}
}

func (node searchNode) pullRequest() PullRequest {
	pull := PullRequest{Number: node.Number, NodeID: node.ID, State: "open", Draft: node.IsDraft, Title: node.Title,
		Body: node.Body, URL: node.URL, Author: node.Author.Login, Head: node.HeadRefName, HeadSHA: node.HeadRefOid,
		HeadRepository: node.HeadRepository.NameWithOwner, Base: node.BaseRefName, BaseSHA: node.BaseRefOid,
		BaseRepository: node.Repository.NameWithOwner, Mergeable: node.Mergeable}
	if node.State != "OPEN" {
		pull.State = "closed"
	}
	if node.MergedAt != nil {
		pull.Merged, pull.MergedAt = true, *node.MergedAt
	}
	if pull.Mergeable == "" {
		pull.Mergeable = "UNKNOWN"
	}
	return pull
}
