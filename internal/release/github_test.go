package release

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/githubapi"
)

// noWait is a retry policy that does not wait (W-013).
func noWait() githubapi.Retry {
	policy := githubapi.DefaultRetry()
	policy.Sleep = func(context.Context, time.Duration) error { return nil }
	return policy
}

func TestGitHubAdapterRetriesAServerError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/repos/o/p/pulls/7" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number": 7, "node_id": "N", "state": "open", "draft": true,
			"head": {"ref": "jackstay/release", "sha": "h1", "repo": {"full_name": "o/p"}},
			"base": {"ref": "main", "sha": "b1", "repo": {"full_name": "o/p"}}, "user": {"login": "bot[bot]"}}`))
	}))
	defer server.Close()
	client, err := NewGitHub("token", "o/p", githubapi.WithBaseURL(server.URL+"/"), githubapi.WithRetry(noWait()))
	if err != nil {
		t.Fatal(err)
	}
	pull, err := client.PullRequest(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || pull.Head != "jackstay/release" || pull.HeadSHA != "h1" || !pull.Draft || pull.Author != "bot[bot]" || pull.BaseSHA != "b1" {
		t.Fatalf("calls %d pull %+v", calls, pull)
	}
}

func TestGitHubAdapterDoesNotRetryANotFound(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	client, _ := NewGitHub("token", "o/p", githubapi.WithBaseURL(server.URL+"/"), githubapi.WithRetry(noWait()))
	if _, err := client.PullRequest(context.Background(), 7); err == nil || calls != 1 {
		t.Fatalf("err %v calls %d", err, calls)
	}
}

// localEnvironment is an environment outside GitHub Actions with a fake `gh`.
func localEnvironment(vars map[string]string, ghExit int, asked *[]string) githubapi.Environment {
	return githubapi.Environment{
		Getenv:  func(name string) string { return vars[name] },
		Environ: func() []string { return nil },
		Run: func(_ context.Context, _ []string, name string, args ...string) ([]byte, []byte, int, error) {
			*asked = append(*asked, name+" "+strings.Join(args, " "))
			return []byte("account-token"), nil, ghExit, nil
		},
	}
}

// Outside GitHub Actions a release command is a dry run: GH_TOKEN is ignored, GitHub is read as
// the project's account when one is named (docs/design/implementation.md, section 8.1), else offline.
func TestLocalFlowFollowsTheAccount(t *testing.T) {
	user := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer account-token" {
			t.Errorf("request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"login": "octocat"}`))
	}))
	defer user.Close()
	for _, row := range []struct {
		name     string
		github   string // the jackstay.json section; "" for none
		remote   string // the URL of the folder's one remote; "" for no repository
		vars     map[string]string
		ghExit   int
		online   bool
		problem  string
		saysNote bool
		// repository is the one the flow reads ("" when not checked)
		repository string
	}{
		{name: "no account: offline, GH_TOKEN ignored", vars: map[string]string{"GH_TOKEN": "x", "GITHUB_REPOSITORY": "o/p"}, saysNote: true},
		{name: "no account and no GH_TOKEN: offline", vars: map[string]string{"GITHUB_REPOSITORY": "o/p"}},
		{name: "an account and no repository: offline", github: `{"account": "octocat"}`, vars: map[string]string{}},
		{name: "the account, the section's repository", github: `{"account": "octocat", "repository": "o/p"}`, vars: map[string]string{}, online: true},
		{name: "JACKSTAY_GITHUB_ACCOUNT without a section, the remote's repository", remote: "https://github.com/o/p.git",
			vars:   map[string]string{"JACKSTAY_GITHUB_ACCOUNT": "octocat", "GITHUB_REPOSITORY": "elsewhere/q", "GH_TOKEN": "x"},
			online: true, saysNote: true, repository: "o/p"},
		// A GITHUB_REPOSITORY left in a local shell is not read.
		{name: "GITHUB_REPOSITORY is not read locally", github: `{"account": "octocat", "repository": "o/p"}`,
			remote: "https://github.com/other/r.git", vars: map[string]string{"GITHUB_REPOSITORY": "elsewhere/q"}, online: true,
			repository: "o/p"},
		{name: "GITHUB_REPOSITORY alone names no repository locally", github: `{"account": "octocat"}`,
			vars: map[string]string{"GITHUB_REPOSITORY": "elsewhere/q"}},
		{name: "not signed in", github: `{"account": "octocat", "repository": "o/p"}`, vars: map[string]string{}, ghExit: 1, problem: "gh auth login"},
		{name: "a broken section", github: `{"account": "octo--cat"}`, vars: map[string]string{}, problem: "not a GitHub login"},
	} {
		t.Run(row.name, func(t *testing.T) {
			root := t.TempDir()
			doc := `{"jackstay_schema": 2}`
			if row.github != "" {
				doc = `{"jackstay_schema": 2, "github": ` + row.github + `}`
			}
			if err := os.WriteFile(filepath.Join(root, "jackstay.json"), []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
			if row.remote != "" {
				for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", row.remote}} {
					if _, err := (gitcli.Git{Dir: root}).Run(context.Background(), args...); err != nil {
						t.Fatal(err)
					}
				}
			}
			var asked []string
			var stderr strings.Builder
			flow, _, err := localFlow(context.Background(), Flow{Repository: row.vars["GITHUB_REPOSITORY"], DryRun: true}, Settings{}, root,
				localEnvironment(row.vars, row.ghExit, &asked), &stderr, githubapi.WithBaseURL(user.URL+"/"), githubapi.WithRetry(noWait()))
			if row.problem != "" {
				var failure *Problem
				if !errors.As(err, &failure) || failure.Code != 2 || !strings.Contains(err.Error(), row.problem) {
					t.Fatalf("err %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, offline := flow.GitHub.(offlineGitHub)
			if offline == row.online {
				t.Fatalf("online %v, want %v", !offline, row.online)
			}
			if row.online != (len(asked) == 1) || (row.online && asked[0] != "gh auth token --hostname github.com --user octocat") {
				t.Fatalf("gh asked %v", asked)
			}
			if strings.Contains(stderr.String(), "GH_TOKEN is ignored") != row.saysNote {
				t.Fatalf("stderr %q", stderr.String())
			}
			if row.repository != "" && flow.Repository != row.repository {
				t.Fatalf("repository %q, want %q", flow.Repository, row.repository)
			}
		})
	}
}
