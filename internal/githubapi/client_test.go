package githubapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var epoch = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

// clock is a test's time: Sleep moves it, nothing waits (W-013).
type clock struct {
	mutex  sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func (c *clock) policy() Retry {
	return Retry{Attempts: 4, Base: time.Second, Jitter: func(int64) int64 { return 0 },
		Now: func() time.Time { c.mutex.Lock(); defer c.mutex.Unlock(); return c.now },
		Sleep: func(_ context.Context, wait time.Duration) error {
			c.mutex.Lock()
			defer c.mutex.Unlock()
			c.sleeps = append(c.sleeps, wait)
			c.now = c.now.Add(wait)
			return nil
		}}
}

// recorded is a test server answering each request in turn from a list; it fails the test on a
// request past the list's end.
type recorded struct {
	t        *testing.T
	mutex    sync.Mutex
	answers  []func(w http.ResponseWriter, r *http.Request)
	requests []*http.Request
	bodies   []string
}

func serve(t *testing.T, answers ...func(w http.ResponseWriter, r *http.Request)) (*recorded, *httptest.Server) {
	t.Helper()
	rec := &recorded{t: t, answers: answers}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mutex.Lock()
		index := len(rec.requests)
		rec.requests = append(rec.requests, r)
		rec.bodies = append(rec.bodies, string(body))
		rec.mutex.Unlock()
		if index >= len(rec.answers) {
			t.Errorf("unexpected request %d: %s %s", index+1, r.Method, r.URL)
			w.WriteHeader(http.StatusTeapot)
			return
		}
		rec.answers[index](w, r)
	}))
	t.Cleanup(server.Close)
	return rec, server
}

func jsonAnswer(status int, body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func newClient(t *testing.T, server *httptest.Server, c *clock) *Client {
	t.Helper()
	client, err := New("token", "o/p", WithBaseURL(server.URL+"/"), WithHTTPClient(server.Client()), WithRetry(c.policy()))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

const pullJSON = `{"number": 7, "node_id": "N", "state": "open", "draft": true, "mergeable": null,
	"head": {"ref": "jackstay/release", "sha": "h1", "repo": {"full_name": "o/p"}},
	"base": {"ref": "main", "sha": "b1", "repo": {"full_name": "o/p"}}, "user": {"login": "bot[bot]"}}`

func TestNewRefusesARepositoryThatIsNotOwnerName(t *testing.T) {
	for _, repository := range []string{"", "o", "o/", "/p", "o/p/x"} {
		if _, err := New("token", repository); err == nil {
			t.Errorf("%q was taken", repository)
		}
	}
}

func TestReadsAreRetriedAfterAServerError(t *testing.T) {
	rec, server := serve(t, jsonAnswer(http.StatusBadGateway, `{}`), jsonAnswer(http.StatusServiceUnavailable, `{}`), jsonAnswer(http.StatusOK, pullJSON))
	c := &clock{now: epoch}
	pull, err := newClient(t, server, c).PullRequest(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.requests) != 3 || rec.requests[2].URL.Path != "/repos/o/p/pulls/7" || rec.requests[2].Header.Get("Authorization") != "Bearer token" {
		t.Fatalf("requests %d %v", len(rec.requests), rec.requests[2].URL)
	}
	if fmt.Sprint(c.sleeps) != "[500ms 1s]" {
		t.Fatalf("waits %v: base·2ⁿ with the jitter's low end", c.sleeps)
	}
	if pull.Head != "jackstay/release" || pull.HeadSHA != "h1" || !pull.Draft || pull.Author != "bot[bot]" || pull.Mergeable != "UNKNOWN" || pull.Merged {
		t.Fatalf("pull %+v", pull)
	}
}

func TestReadsGiveUpAfterTheAttempts(t *testing.T) {
	bad := jsonAnswer(http.StatusInternalServerError, `{}`)
	rec, server := serve(t, bad, bad, bad, bad)
	c := &clock{now: epoch}
	if _, err := newClient(t, server, c).PullRequest(context.Background(), 7); StatusOf(err) != http.StatusInternalServerError || len(rec.requests) != 4 {
		t.Fatalf("err %v after %d requests", err, len(rec.requests))
	}
}

func TestANotFoundIsNotRetried(t *testing.T) {
	rec, server := serve(t, jsonAnswer(http.StatusNotFound, `{"message": "Not Found"}`))
	if _, err := newClient(t, server, &clock{now: epoch}).PullRequest(context.Background(), 7); StatusOf(err) != http.StatusNotFound || len(rec.requests) != 1 {
		t.Fatalf("err %v requests %d", err, len(rec.requests))
	}
}

func TestBackoffStopsAtTheDeadline(t *testing.T) {
	rec, server := serve(t, jsonAnswer(http.StatusBadGateway, `{}`))
	c := &clock{now: epoch}
	policy := c.policy()
	policy.Now = func() time.Time { return time.Now().Add(time.Hour) } // the deadline below is already past
	client, _ := New("token", "o/p", WithBaseURL(server.URL+"/"), WithRetry(policy))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := client.PullRequest(ctx, 7); StatusOf(err) != http.StatusBadGateway || len(rec.requests) != 1 || len(c.sleeps) != 0 {
		t.Fatalf("err %v requests %d sleeps %v", err, len(rec.requests), c.sleeps)
	}
}

func TestWritesAreNeverRetried(t *testing.T) {
	for name, write := range map[string]func(*Client) error{
		"merge": func(client *Client) error { _, err := client.Merge(context.Background(), 7, "h", "t", "b"); return err },
		"create": func(client *Client) error {
			_, err := client.CreatePullRequest(context.Background(), NewPullRequest{Head: "h"})
			return err
		},
		"edit":   func(client *Client) error { return client.EditPullRequestBody(context.Background(), 7, "b") },
		"mutate": func(client *Client) error { return client.Mutate(context.Background(), "mutation { x }", nil, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			rec, server := serve(t, jsonAnswer(http.StatusBadGateway, `{}`))
			if err := write(newClient(t, server, &clock{now: epoch})); err == nil || len(rec.requests) != 1 {
				t.Fatalf("err %v requests %d", err, len(rec.requests))
			}
		})
	}
}

func primaryLimit(reset time.Time) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset.Unix()))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message": "API rate limit exceeded"}`)
	}
}

func TestAPrimaryRateLimitIsWaitedOutInsideTheDeadline(t *testing.T) {
	c := &clock{now: time.Now()}
	reset := c.now.Add(10 * time.Minute).Truncate(time.Second)
	rec, server := serve(t, primaryLimit(reset), jsonAnswer(http.StatusOK, pullJSON))
	ctx, cancel := context.WithDeadline(context.Background(), c.now.Add(time.Hour))
	defer cancel()
	if _, err := newClient(t, server, c).PullRequest(ctx, 7); err != nil || len(rec.requests) != 2 {
		t.Fatalf("err %v requests %d", err, len(rec.requests))
	}
	if len(c.sleeps) != 1 || !c.now.Equal(reset) {
		t.Fatalf("slept %v, now %v, reset %v", c.sleeps, c.now, reset)
	}
}

func TestAPrimaryRateLimitPastTheDeadlineNamesTheTime(t *testing.T) {
	c := &clock{now: time.Now()}
	reset := c.now.Add(2 * time.Hour).Truncate(time.Second)
	rec, server := serve(t, primaryLimit(reset))
	ctx, cancel := context.WithDeadline(context.Background(), c.now.Add(time.Hour))
	defer cancel()
	_, err := newClient(t, server, c).PullRequest(ctx, 7)
	var limited *RateLimitedError
	if !errors.As(err, &limited) || !limited.Until.Equal(reset) || limited.Secondary || limited.Code() != 1 || len(rec.requests) != 1 || len(c.sleeps) != 0 {
		t.Fatalf("err %v requests %d sleeps %v", err, len(rec.requests), c.sleeps)
	}
	if !strings.Contains(err.Error(), reset.Local().Format("15:04:05")) {
		t.Fatalf("the time is not named: %v", err)
	}
}

func TestASecondaryRateLimitHonoursRetryAfter(t *testing.T) {
	secondary := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message": "You have exceeded a secondary rate limit", "documentation_url": "https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`)
	}
	rec, server := serve(t, secondary, jsonAnswer(http.StatusOK, pullJSON))
	c := &clock{now: epoch}
	if _, err := newClient(t, server, c).PullRequest(context.Background(), 7); err != nil || len(rec.requests) != 2 {
		t.Fatalf("err %v requests %d", err, len(rec.requests))
	}
	if fmt.Sprint(c.sleeps) != "[42s]" {
		t.Fatalf("waits %v", c.sleeps)
	}
}

type netFailure struct{}

func (netFailure) Error() string   { return "connection reset" }
func (netFailure) Timeout() bool   { return false }
func (netFailure) Temporary() bool { return true }

var _ net.Error = netFailure{}

func TestReadRetriesANetworkFailureButNotAnotherError(t *testing.T) {
	c := &clock{now: epoch}
	calls := 0
	got, err := Read(context.Background(), c.policy(), func() (string, error) {
		calls++
		if calls == 1 {
			return "", netFailure{}
		}
		return "answer", nil
	})
	if err != nil || got != "answer" || calls != 2 {
		t.Fatalf("got %q err %v calls %d", got, err, calls)
	}
	calls = 0
	if _, err := Read(context.Background(), c.policy(), func() (string, error) { calls++; return "", errors.New("bad") }); err == nil || calls != 1 {
		t.Fatalf("err %v calls %d", err, calls)
	}
}

func TestPullRequestsByHead(t *testing.T) {
	var server *httptest.Server
	rec, server := serve(t,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/p/pulls?page=2>; rel="next"`, server.URL))
			jsonAnswer(http.StatusOK, `[{"number": 1, "state": "closed", "merged_at": "2026-11-15T10:00:00Z", "mergeable": true}]`)(w, nil)
		},
		jsonAnswer(http.StatusOK, `[{"number": 2, "state": "closed", "mergeable": false}]`))
	pulls, err := newClient(t, server, &clock{now: epoch}).PullRequestsByHead(context.Background(), "o:feature", "all")
	if err != nil {
		t.Fatal(err)
	}
	query := rec.requests[0].URL.Query()
	if query.Get("head") != "o:feature" || query.Get("state") != "all" || rec.requests[1].URL.Query().Get("page") != "2" {
		t.Fatalf("queries %v %v", query, rec.requests[1].URL)
	}
	if len(pulls) != 2 || !pulls[0].Merged || pulls[0].MergedAt.IsZero() || pulls[0].Mergeable != "MERGEABLE" ||
		pulls[1].Merged || pulls[1].State != "closed" || pulls[1].Mergeable != "CONFLICTING" {
		t.Fatalf("pulls %+v", pulls)
	}
	if _, err := newClient(t, server, &clock{now: epoch}).PullRequestsByHead(context.Background(), "feature", "open"); err == nil {
		t.Fatal("a head without its owner matches nothing on GitHub and must be refused")
	}
}

func TestCreateEditAndMergeSendWhatTheySay(t *testing.T) {
	rec, server := serve(t,
		jsonAnswer(http.StatusCreated, pullJSON),
		jsonAnswer(http.StatusOK, pullJSON),
		jsonAnswer(http.StatusOK, `{"sha": "m1", "merged": true, "message": "Pull Request successfully merged"}`))
	client := newClient(t, server, &clock{now: epoch})
	ctx := context.Background()
	if _, err := client.CreatePullRequest(ctx, NewPullRequest{Head: "feature", Base: "main", Title: "T", Body: "B", Draft: true}); err != nil {
		t.Fatal(err)
	}
	if err := client.EditPullRequestBody(ctx, 7, "new body"); err != nil {
		t.Fatal(err)
	}
	merged, err := client.Merge(ctx, 7, "h1", "Title (#7)", "Card: none (x)")
	if err != nil || !merged.Merged || merged.SHA != "m1" {
		t.Fatalf("merge %+v %v", merged, err)
	}
	for index, want := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPost, "/repos/o/p/pulls", map[string]any{"head": "feature", "base": "main", "title": "T", "body": "B", "draft": true}},
		{http.MethodPatch, "/repos/o/p/pulls/7", map[string]any{"body": "new body"}},
		{http.MethodPut, "/repos/o/p/pulls/7/merge", map[string]any{"sha": "h1", "merge_method": "squash", "commit_title": "Title (#7)", "commit_message": "Card: none (x)"}},
	} {
		request := rec.requests[index]
		var body map[string]any
		_ = json.Unmarshal([]byte(rec.bodies[index]), &body)
		if request.Method != want.method || request.URL.Path != want.path {
			t.Errorf("request %d: %s %s", index, request.Method, request.URL.Path)
		}
		for key, value := range want.body {
			if fmt.Sprint(body[key]) != fmt.Sprint(value) {
				t.Errorf("request %d: %s = %v, want %v (%s)", index, key, body[key], value, rec.bodies[index])
			}
		}
	}
}

func TestDefaultBranchAndLogin(t *testing.T) {
	rec, server := serve(t, jsonAnswer(http.StatusOK, `{"default_branch": "trunk"}`), jsonAnswer(http.StatusOK, `{"login": "OctoCat"}`))
	client := newClient(t, server, &clock{now: epoch})
	branch, err := client.DefaultBranch(context.Background())
	if err != nil || branch != "trunk" {
		t.Fatalf("%q %v", branch, err)
	}
	login, err := client.Login(context.Background())
	if err != nil || login != "OctoCat" || rec.requests[1].URL.Path != "/user" {
		t.Fatalf("%q %v", login, err)
	}
}

func TestWorkflowRunsJobsAndTheJobLog(t *testing.T) {
	_, blob := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the token went to blob storage")
		}
		_, _ = io.WriteString(w, "2026-11-16T10:00:01.0000000Z line one\n")
	})
	rec, server := serve(t,
		jsonAnswer(http.StatusOK, `{"total_count": 1, "workflow_runs": [{"id": 9, "name": "ci", "workflow_id": 3, "status": "completed", "conclusion": "failure", "head_sha": "h1", "head_branch": "feature", "event": "pull_request", "html_url": "u", "run_attempt": 2}]}`),
		jsonAnswer(http.StatusOK, `{"total_count": 1, "jobs": [{"id": 11, "run_id": 9, "name": "test", "status": "completed", "conclusion": "failure",
			"started_at": "2026-11-16T10:00:00Z", "completed_at": "2026-11-16T10:05:00Z",
			"steps": [{"number": 1, "name": "Run tests", "status": "completed", "conclusion": "failure", "started_at": "2026-11-16T10:00:01Z", "completed_at": "2026-11-16T10:04:00Z"}]}]}`),
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, blob.URL+"/log.txt?sig=x", http.StatusFound)
		})
	client := newClient(t, server, &clock{now: epoch})
	ctx := context.Background()
	runs, err := client.WorkflowRuns(ctx, RunFilter{Workflow: "ci.yml", Branch: "feature", HeadSHA: "h1"})
	if err != nil || len(runs) != 1 || runs[0].ID != 9 || runs[0].Conclusion != "failure" || runs[0].Attempt != 2 {
		t.Fatalf("runs %+v %v", runs, err)
	}
	query := rec.requests[0].URL.Query()
	if rec.requests[0].URL.Path != "/repos/o/p/actions/workflows/ci.yml/runs" || query.Get("branch") != "feature" || query.Get("head_sha") != "h1" {
		t.Fatalf("runs request %v", rec.requests[0].URL)
	}
	jobs, err := client.Jobs(ctx, 9)
	if err != nil || len(jobs) != 1 || len(jobs[0].Steps) != 1 || jobs[0].Steps[0].Name != "Run tests" ||
		jobs[0].Steps[0].CompletedAt.Sub(jobs[0].Steps[0].StartedAt) != 239*time.Second || rec.requests[1].URL.Query().Get("filter") != "latest" {
		t.Fatalf("jobs %+v %v", jobs, err)
	}
	log, err := client.JobLog(ctx, 11)
	if err != nil || !strings.Contains(log, "line one") || rec.requests[2].URL.Path != "/repos/o/p/actions/jobs/11/logs" {
		t.Fatalf("log %q %v", log, err)
	}
}

func TestWorkflowRunsOfEveryWorkflow(t *testing.T) {
	rec, server := serve(t, jsonAnswer(http.StatusOK, `{"total_count": 0, "workflow_runs": []}`))
	if _, err := newClient(t, server, &clock{now: epoch}).WorkflowRuns(context.Background(), RunFilter{HeadSHA: "h1"}); err != nil ||
		rec.requests[0].URL.Path != "/repos/o/p/actions/runs" {
		t.Fatalf("%v %v", err, rec.requests[0].URL)
	}
}
