package githubapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// ghRun is a fake `gh`: what it was asked and with which environment, and its answer.
type ghRun struct {
	stdout, stderr string
	exitCode       int
	err            error
	asked          [][]string
	env            []string
}

func (run *ghRun) runner() Runner {
	return func(_ context.Context, env []string, name string, args ...string) ([]byte, []byte, int, error) {
		run.asked = append(run.asked, append([]string{name}, args...))
		run.env = env
		return []byte(run.stdout), []byte(run.stderr), run.exitCode, run.err
	}
}

func environment(vars map[string]string, run *ghRun) Environment {
	return Environment{
		Getenv: func(name string) string { return vars[name] },
		Environ: func() []string {
			var out []string
			for name, value := range vars {
				out = append(out, name+"="+value)
			}
			return out
		},
		Run: run.runner(),
	}
}

func TestResolveToken(t *testing.T) {
	for _, row := range []struct {
		name       string
		vars       map[string]string
		configured string
		gh         ghRun
		wantToken  string
		wantUser   string // the account asked of gh; "" when gh must not run
		wantErr    any
		wantNote   bool
	}{
		{name: "Actions: GH_TOKEN only", vars: map[string]string{"GITHUB_ACTIONS": "true", "GH_TOKEN": "app", "JACKSTAY_GITHUB_ACCOUNT": "x"},
			configured: "octocat", wantToken: "app"},
		{name: "Actions without GH_TOKEN", vars: map[string]string{"GITHUB_ACTIONS": "true"}, configured: "octocat",
			wantErr: new(*ActionsTokenMissingError)},
		{name: "no account", vars: map[string]string{}, wantErr: new(*AccountMissingError)},
		{name: "the section's account", vars: map[string]string{}, configured: "octocat", gh: ghRun{stdout: "s3cr3t\n"},
			wantToken: "s3cr3t", wantUser: "octocat"},
		{name: "JACKSTAY_GITHUB_ACCOUNT wins", vars: map[string]string{"JACKSTAY_GITHUB_ACCOUNT": "other"}, configured: "octocat",
			gh: ghRun{stdout: "s3cr3t"}, wantToken: "s3cr3t", wantUser: "other"},
		{name: "JACKSTAY_GITHUB_ACCOUNT without a section", vars: map[string]string{"JACKSTAY_GITHUB_ACCOUNT": "other"},
			gh: ghRun{stdout: "s3cr3t"}, wantToken: "s3cr3t", wantUser: "other"},
		{name: "GH_TOKEN outside Actions is ignored and said", vars: map[string]string{"GH_TOKEN": "stray", "GITHUB_TOKEN": "stray2"},
			configured: "octocat", gh: ghRun{stdout: "s3cr3t"}, wantToken: "s3cr3t", wantUser: "octocat", wantNote: true},
		{name: "not signed in", vars: map[string]string{}, configured: "octocat",
			gh: ghRun{exitCode: 1, stderr: "no oauth token found for github.com account octocat"}, wantUser: "octocat",
			wantErr: new(*NotSignedInError)},
		{name: "a keychain failure", vars: map[string]string{}, configured: "octocat",
			gh: ghRun{exitCode: 4, stderr: "keychain locked"}, wantUser: "octocat", wantErr: new(*TokenCommandError)},
		{name: "gh missing", vars: map[string]string{}, configured: "octocat",
			gh: ghRun{err: errors.New("executable file not found")}, wantUser: "octocat", wantErr: new(*TokenCommandError)},
		{name: "an empty answer", vars: map[string]string{}, configured: "octocat", gh: ghRun{stdout: "\n"},
			wantUser: "octocat", wantErr: new(*TokenCommandError)},
	} {
		t.Run(row.name, func(t *testing.T) {
			run := row.gh
			token, err := ResolveToken(context.Background(), environment(row.vars, &run), row.configured)
			if row.wantErr != nil {
				if !errors.As(err, row.wantErr) {
					t.Fatalf("err %v (%T)", err, err)
				}
				var coded interface{ Code() int }
				if !errors.As(err, &coded) || coded.Code() != 2 {
					t.Fatalf("exit status of %v", err)
				}
			} else if err != nil || token.Value != row.wantToken {
				t.Fatalf("token %q err %v", token.Value, err)
			}
			if row.wantUser == "" {
				if len(run.asked) != 0 {
					t.Fatalf("gh was run: %v", run.asked)
				}
				return
			}
			want := "gh auth token --hostname github.com --user " + row.wantUser
			if len(run.asked) != 1 || strings.Join(run.asked[0], " ") != want {
				t.Fatalf("asked %v, want %s", run.asked, want)
			}
			for _, entry := range run.env {
				if strings.HasPrefix(entry, "GH_TOKEN=") || strings.HasPrefix(entry, "GITHUB_TOKEN=") {
					t.Fatalf("gh was given %s: it would answer with that token", entry)
				}
			}
			if (token.Note != "") != row.wantNote {
				t.Fatalf("note %q", token.Note)
			}
			if err == nil && (token.Account != row.wantUser || strings.Contains(token.String(), token.Value)) {
				t.Fatalf("token %+v / %s", token, token)
			}
		})
	}
}

func TestConnectChecksTheLogin(t *testing.T) {
	user := func(login string) func(w http.ResponseWriter, r *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/user" {
				t.Errorf("asked %s", r.URL.Path)
			}
			jsonAnswer(http.StatusOK, `{"login": "`+login+`"}`)(w, r)
		}
	}
	for _, row := range []struct {
		name  string
		login string
		ok    bool
	}{
		{"the same login", "octocat", true},
		{"the same login in other case", "OctoCat", true},
		{"another login", "someone", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			rec, server := serve(t, user(row.login))
			run := ghRun{stdout: "tok"}
			client, token, err := Connect(context.Background(), environment(map[string]string{}, &run), "octocat", "o/p",
				WithBaseURL(server.URL+"/"), WithRetry((&clock{now: epoch}).policy()))
			if len(rec.requests) != 1 || rec.requests[0].Header.Get("Authorization") != "Bearer tok" {
				t.Fatalf("requests %d", len(rec.requests))
			}
			if row.ok {
				if err != nil || client == nil || token.Account != "octocat" {
					t.Fatalf("err %v", err)
				}
				return
			}
			var wrong *WrongLoginError
			if !errors.As(err, &wrong) || wrong.Login != "someone" || client != nil {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestConnectInActionsAsksNoUser(t *testing.T) {
	rec, server := serve(t)
	run := ghRun{}
	client, token, err := Connect(context.Background(), environment(map[string]string{"GITHUB_ACTIONS": "true", "GH_TOKEN": "app"}, &run),
		"", "o/p", WithBaseURL(server.URL+"/"))
	if err != nil || client == nil || token.Account != "" || len(rec.requests) != 0 || len(run.asked) != 0 {
		t.Fatalf("err %v requests %d gh %v", err, len(rec.requests), run.asked)
	}
}

func TestTestServerOnlyOnLoopback(t *testing.T) {
	for address, want := range map[string]bool{
		"http://127.0.0.1:4000/": true, "http://localhost:1/": true, "http://[::1]:9/": true,
		"https://127.0.0.1:4000/": false, "http://example.com/": false, "http://127.0.0.1:4000": false, "": false,
	} {
		if got := isLoopback(address); got != want {
			t.Errorf("%q: %v, want %v", address, got, want)
		}
	}
}
