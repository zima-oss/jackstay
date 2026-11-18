package lint

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

func noWait() githubapi.Retry {
	policy := githubapi.DefaultRetry()
	policy.Sleep = func(context.Context, time.Duration) error { return nil }
	return policy
}

// `jst lint pr --pr N` asks GitHub with the token section 8.1 of docs/design/implementation.md chooses: in
// GitHub Actions GH_TOKEN, elsewhere the project's account through gh, checked by GET /user.
func TestFromGitHubChoosesTheToken(t *testing.T) {
	for _, row := range []struct {
		name      string
		github    string // the jackstay.json section; "" for none
		remote    string // the URL of the folder's one remote; "" for no repository
		vars      map[string]string
		wantToken string // the Authorization the server must see; "" when GitHub must not be asked
		wantGh    bool
		problem   string
		saysNote  bool
	}{
		{name: "Actions: GH_TOKEN", vars: map[string]string{"GITHUB_ACTIONS": "true", "GH_TOKEN": "app", "GITHUB_REPOSITORY": "o/p"},
			github: `{"account": "octocat"}`, wantToken: "app"},
		{name: "Actions without GH_TOKEN", vars: map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "o/p"}, problem: "GH_TOKEN is not set"},
		{name: "locally without an account", vars: map[string]string{"GH_TOKEN": "x", "GITHUB_REPOSITORY": "o/p"}, problem: "set github.account"},
		{name: "locally as the account", github: `{"account": "octocat", "repository": "o/p"}`, vars: map[string]string{"GH_TOKEN": "stray"},
			wantToken: "account-token", wantGh: true, saysNote: true},
		{name: "locally as JACKSTAY_GITHUB_ACCOUNT, the remote's repository", remote: "https://github.com/o/p.git",
			vars: map[string]string{"JACKSTAY_GITHUB_ACCOUNT": "octocat", "GITHUB_REPOSITORY": "elsewhere/q"}, wantToken: "account-token", wantGh: true},
		// A GITHUB_REPOSITORY left in a local shell does not
		// override the project's repository (the server answers only /repos/o/p).
		{name: "locally GITHUB_REPOSITORY is not read", github: `{"account": "octocat", "repository": "o/p"}`,
			vars: map[string]string{"GITHUB_REPOSITORY": "elsewhere/q"}, wantToken: "account-token", wantGh: true},
		{name: "a broken section", github: `{"account": "octocat", "token": "x"}`, vars: map[string]string{}, problem: "unknown key `github.token`"},
	} {
		t.Run(row.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if row.wantToken == "" || r.Header.Get("Authorization") != "Bearer "+row.wantToken {
					t.Errorf("asked %s with %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				switch r.URL.Path {
				case "/user":
					if !row.wantGh {
						t.Errorf("GET /user inside Actions")
					}
					_, _ = w.Write([]byte(`{"login": "octocat"}`))
				case "/repos/o/p/pulls/7":
					_, _ = w.Write([]byte(`{"number": 7, "body": "Card: none (x)", "head": {"ref": "feature", "sha": "h1", "repo": {"full_name": "fork/p"}},
						"base": {"ref": "main", "sha": "b1", "repo": {"full_name": "o/p"}}}`))
				default:
					t.Errorf("asked %s", r.URL.Path)
				}
			}))
			defer server.Close()
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
			var ghAsked []string
			var stderr strings.Builder
			space := workspace{root: root, git: gitcli.Git{Dir: root}, stderr: &stderr,
				gitHubEnv: githubapi.Environment{
					Getenv:  func(name string) string { return row.vars[name] },
					Environ: func() []string { return nil },
					Run: func(_ context.Context, _ []string, name string, args ...string) ([]byte, []byte, int, error) {
						ghAsked = append(ghAsked, name+" "+strings.Join(args, " "))
						return []byte("account-token\n"), nil, 0, nil
					},
				},
				gitHubOptions: []githubapi.Option{githubapi.WithBaseURL(server.URL + "/"), githubapi.WithRetry(noWait())}}
			facts, err := space.fromGitHub(7)
			if row.problem != "" {
				var failure *problem
				if !errors.As(err, &failure) || failure.code != 2 || !strings.Contains(err.Error(), row.problem) {
					t.Fatalf("err %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if facts.headBranch != "feature" || facts.head != "h1" || facts.base != "b1" || !facts.fork || !facts.mergeOf {
				t.Fatalf("facts %+v", facts)
			}
			if row.wantGh != (len(ghAsked) == 1) {
				t.Fatalf("gh asked %v", ghAsked)
			}
			if strings.Contains(stderr.String(), "GH_TOKEN is ignored") != row.saysNote {
				t.Fatalf("stderr %q", stderr.String())
			}
		})
	}
}

// A broken github section stops only `--pr` and the queue-copy lookup: the rest of lint loads.
func TestABrokenGitHubSectionStopsOnlyWhatReadsIt(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "jackstay.json"), []byte(`{"jackstay_schema": 2, "github": {"account": "-x"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	space, err := loadWorkspace()
	if err != nil {
		t.Fatalf("lint stopped by a section it does not read: %v", err)
	}
	if space.config.Section("github") != nil {
		t.Fatal("an unusable section is not left out")
	}
}
