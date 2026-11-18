package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"
)

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func graphQLRequests(t *testing.T, rec *recorded) []graphQLRequest {
	t.Helper()
	var out []graphQLRequest
	for index, body := range rec.bodies {
		if rec.requests[index].URL.Path != "/graphql" || rec.requests[index].Method != http.MethodPost {
			t.Fatalf("request %d: %s %s", index, rec.requests[index].Method, rec.requests[index].URL.Path)
		}
		var request graphQLRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		out = append(out, request)
	}
	return out
}

func TestBucketOf(t *testing.T) {
	for _, row := range []struct {
		state string
		want  Bucket
	}{
		{"SUCCESS", Pass},
		{"SKIPPED", Skipping},
		{"NEUTRAL", Skipping},
		{"FAILURE", Fail},
		{"ERROR", Fail},
		{"TIMED_OUT", Fail},
		{"ACTION_REQUIRED", Fail},
		{"STARTUP_FAILURE", Fail},
		{"CANCELLED", Cancel},
		{"STALE", Pending},
		{"PENDING", Pending},
		{"EXPECTED", Pending},
		{"", Pending},
		{"SOMETHING_NEW", Pending},
	} {
		t.Run(row.state, func(t *testing.T) {
			if got := BucketOf(row.state); got != row.want {
				t.Fatalf("got %s, want %s", got, row.want)
			}
		})
	}
}

func TestCheckBucket(t *testing.T) {
	for _, row := range []struct {
		name  string
		check Check
		want  Bucket
	}{
		{"a completed run by its conclusion", Check{Kind: CheckRun, Status: "COMPLETED", Conclusion: "SUCCESS"}, Pass},
		{"a completed failed run", Check{Kind: CheckRun, Status: "COMPLETED", Conclusion: "FAILURE"}, Fail},
		{"a completed stale run", Check{Kind: CheckRun, Status: "COMPLETED", Conclusion: "STALE"}, Pending},
		{"a run in progress", Check{Kind: CheckRun, Status: "IN_PROGRESS"}, Pending},
		{"a queued run", Check{Kind: CheckRun, Status: "QUEUED"}, Pending},
		{"a status that succeeded", Check{Kind: StatusContext, State: "SUCCESS"}, Pass},
		{"a status in error", Check{Kind: StatusContext, State: "ERROR"}, Fail},
		{"a status that failed", Check{Kind: StatusContext, State: "FAILURE"}, Fail},
		{"a pending status", Check{Kind: StatusContext, State: "PENDING"}, Pending},
		{"an expected status", Check{Kind: StatusContext, State: "EXPECTED"}, Pending},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := row.check.Bucket(); got != row.want {
				t.Fatalf("got %s, want %s", got, row.want)
			}
		})
	}
}

func TestChecksArePagedToTheEnd(t *testing.T) {
	rec, server := serve(t,
		jsonAnswer(http.StatusOK, `{"data": {"repository": {"object": {"statusCheckRollup": {"contexts": {
			"pageInfo": {"hasNextPage": true, "endCursor": "C1"},
			"nodes": [{"__typename": "CheckRun", "name": "test", "status": "COMPLETED", "conclusion": "SUCCESS", "detailsUrl": "d1",
				"startedAt": "2026-11-16T10:00:00Z", "completedAt": "2026-11-16T10:01:00Z",
				"checkSuite": {"app": {"databaseId": 15368}, "workflowRun": {"databaseId": 9, "workflow": {"name": "ci"}}}}]}}}}}}`),
		jsonAnswer(http.StatusBadGateway, `{}`), // a read: retried
		jsonAnswer(http.StatusOK, `{"data": {"repository": {"object": {"statusCheckRollup": {"contexts": {
			"pageInfo": {"hasNextPage": false, "endCursor": "C2"},
			"nodes": [{"__typename": "StatusContext", "context": "deploy/preview", "state": "PENDING", "targetUrl": "t1", "description": "building"},
				{"__typename": "CheckRun", "name": "lint", "status": "IN_PROGRESS", "conclusion": null, "checkSuite": {"workflowRun": null}}]}}}}}}`))
	c := &clock{now: epoch}
	checks, err := newClient(t, server, c).Checks(context.Background(), "h1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Check{
		{Name: "test", Kind: CheckRun, Status: "COMPLETED", Conclusion: "SUCCESS", Link: "d1", Workflow: "ci", RunID: 9, AppID: 15368,
			StartedAt: checks[0].StartedAt, CompletedAt: checks[0].CompletedAt},
		{Name: "deploy/preview", Kind: StatusContext, State: "PENDING", Link: "t1", Description: "building"},
		{Name: "lint", Kind: CheckRun, Status: "IN_PROGRESS"},
	}
	if len(checks) != len(want) || checks[0].StartedAt.IsZero() {
		t.Fatalf("checks %+v", checks)
	}
	for index := range want {
		if !reflect.DeepEqual(checks[index], want[index]) {
			t.Errorf("check %d: %+v, want %+v", index, checks[index], want[index])
		}
	}
	buckets := []Bucket{checks[0].Bucket(), checks[1].Bucket(), checks[2].Bucket()}
	if buckets[0] != Pass || buckets[1] != Pending || buckets[2] != Pending {
		t.Fatalf("buckets %v", buckets)
	}
	requests := graphQLRequests(t, rec)
	if requests[0].Variables["oid"] != "h1" || requests[0].Variables["owner"] != "o" || requests[0].Variables["name"] != "p" ||
		requests[0].Variables["after"] != nil || requests[2].Variables["after"] != "C1" {
		t.Fatalf("variables %v / %v", requests[0].Variables, requests[2].Variables)
	}
}

func TestChecksOfACommitWithNoneAndOfNoCommit(t *testing.T) {
	_, server := serve(t,
		jsonAnswer(http.StatusOK, `{"data": {"repository": {"object": {"statusCheckRollup": null}}}}`),
		jsonAnswer(http.StatusOK, `{"data": {"repository": {"object": null}}}`))
	client := newClient(t, server, &clock{now: epoch})
	if checks, err := client.Checks(context.Background(), "h1"); err != nil || len(checks) != 0 {
		t.Fatalf("no checks registered: %v %v", checks, err)
	}
	var missing *CommitNotFoundError
	if _, err := client.Checks(context.Background(), "h2"); !errors.As(err, &missing) || missing.SHA != "h2" {
		t.Fatalf("err %v", err)
	}
}

func TestSearchPullRequestsBySHA(t *testing.T) {
	rec, server := serve(t,
		jsonAnswer(http.StatusOK, `{"data": {"search": {"pageInfo": {"hasNextPage": true, "endCursor": "S1"},
			"nodes": [{"number": 4, "id": "P4", "state": "MERGED", "mergedAt": "2026-11-15T09:00:00Z", "headRefName": "feature", "headRefOid": "h1",
				"baseRefName": "main", "repository": {"nameWithOwner": "o/p"}, "headRepository": {"nameWithOwner": "o/p"}, "author": {"login": "me"}}]}}}`),
		jsonAnswer(http.StatusOK, `{"data": {"search": {"pageInfo": {"hasNextPage": false, "endCursor": null},
			"nodes": [{"number": 5, "state": "OPEN", "mergeable": "CONFLICTING"}, {}]}}}`))
	pulls, err := newClient(t, server, &clock{now: epoch}).SearchPullRequestsBySHA(context.Background(), "h1")
	if err != nil {
		t.Fatal(err)
	}
	if len(pulls) != 2 || !pulls[0].Merged || pulls[0].State != "closed" || pulls[0].Head != "feature" || pulls[0].Author != "me" ||
		pulls[0].Mergeable != "UNKNOWN" || pulls[1].State != "open" || pulls[1].Merged || pulls[1].Mergeable != "CONFLICTING" {
		t.Fatalf("pulls %+v", pulls)
	}
	requests := graphQLRequests(t, rec)
	if requests[0].Variables["q"] != "is:pr h1 repo:o/p" || requests[1].Variables["after"] != "S1" {
		t.Fatalf("variables %v %v", requests[0].Variables, requests[1].Variables)
	}
}

func TestGraphQLErrors(t *testing.T) {
	_, server := serve(t,
		jsonAnswer(http.StatusOK, `{"errors": [{"type": "NOT_FOUND", "message": "Could not resolve"}]}`),
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Reset", "1900000000")
			jsonAnswer(http.StatusOK, `{"errors": [{"type": "RATE_LIMITED", "message": "API rate limit exceeded"}]}`)(w, r)
		})
	c := &clock{now: time.Now()}
	client := newClient(t, server, c)
	var answered *GraphQLError
	if err := client.Query(context.Background(), "query { x }", nil, nil); !errors.As(err, &answered) || answered.Messages[0] != "Could not resolve" {
		t.Fatalf("err %v", err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), c.now.Add(time.Minute))
	defer cancel()
	var limited *RateLimitedError
	if err := client.Query(ctx, "query { x }", nil, nil); !errors.As(err, &limited) || limited.Until.Unix() != 1900000000 {
		t.Fatalf("err %v", err)
	}
}
