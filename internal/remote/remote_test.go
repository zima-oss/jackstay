package remote

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeGit answers git questions from a table keyed by the joined arguments; a missing key is git's
// exit 1 (an unset config, a ref that is not there).
type fakeGit map[string]string

var errExit1 = errors.New("exit 1")

func (fake fakeGit) Run(_ context.Context, args ...string) ([]byte, error) {
	out, ok := fake[strings.Join(args, " ")]
	if !ok {
		return nil, errExit1
	}
	return []byte(out), nil
}

const commit = "^{commit}"

func TestName(t *testing.T) {
	cases := []struct {
		name, configured, want string
		git                    fakeGit
		wantErr                any
	}{
		{name: "origin first", git: fakeGit{"remote": "upstream\norigin\n"}, want: "origin"},
		{name: "configured wins over origin", configured: "upstream", git: fakeGit{"remote": "upstream\norigin\n"}, want: "upstream"},
		{name: "configured unknown", configured: "nope", git: fakeGit{"remote": "origin\n"}, wantErr: &UnknownError{}},
		{name: "main tracks", git: fakeGit{"remote": "a\nocto-org.github.com\n",
			"config --get branch.main.remote": "octo-org.github.com\n"}, want: "octo-org.github.com"},
		{name: "master tracks", git: fakeGit{"remote": "a\nb\n", "config --get branch.main.remote": ".\n",
			"config --get branch.master.remote": "b\n"}, want: "b"},
		{name: "the only remote", git: fakeGit{"remote": "solo\n"}, want: "solo"},
		{name: "several and none chosen", git: fakeGit{"remote": "a\nb\n"}, wantErr: &UnclearError{}},
		{name: "none", git: fakeGit{"remote": ""}, wantErr: ErrNoRemote},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Name(context.Background(), c.git, c.configured)
			switch want := c.wantErr.(type) {
			case nil:
				if err != nil || got != c.want {
					t.Fatalf("got %q, %v; want %q", got, err, c.want)
				}
			case *UnknownError:
				var typed *UnknownError
				if !errors.As(err, &typed) {
					t.Fatalf("got %q, %v; want UnknownError", got, err)
				}
			case *UnclearError:
				var typed *UnclearError
				if !errors.As(err, &typed) || len(typed.Remotes) != 2 {
					t.Fatalf("got %q, %v; want UnclearError", got, err)
				}
			case error:
				if !errors.Is(err, want) {
					t.Fatalf("got %q, %v; want %v", got, err, want)
				}
			}
		})
	}
}

func TestDefaultBranch(t *testing.T) {
	cases := []struct {
		name, remote string
		git          fakeGit
		want         Default
		fetchedErr   bool
		localErr     bool
	}{
		{name: "the remote's HEAD", remote: "origin", git: fakeGit{
			"symbolic-ref --quiet refs/remotes/origin/HEAD":                          "refs/remotes/origin/release/stable\n",
			"rev-parse --verify --quiet refs/remotes/origin/release/stable" + commit: "abc\n",
			"rev-parse --verify --quiet refs/remotes/origin/main" + commit:           "abc\n",
		}, want: Default{Ref: "refs/remotes/origin/release/stable", Short: "release/stable", Fetched: true}},
		{name: "a HEAD pointing elsewhere is not taken", remote: "up", git: fakeGit{
			"symbolic-ref --quiet refs/remotes/up/HEAD":                  "refs/heads/x\n",
			"rev-parse --verify --quiet refs/remotes/up/master" + commit: "abc\n",
		}, want: Default{Ref: "refs/remotes/up/master", Short: "master", Fetched: true}},
		{name: "a remote with a slash in its name", remote: "team/upstream", git: fakeGit{
			"rev-parse --verify --quiet refs/remotes/team/upstream/main" + commit: "abc\n",
		}, want: Default{Ref: "refs/remotes/team/upstream/main", Short: "main", Fetched: true}},
		{name: "only a local main", remote: "origin", git: fakeGit{
			"rev-parse --verify --quiet refs/heads/main" + commit: "abc\n",
		}, want: Default{Ref: "refs/heads/main", Short: "main"}, fetchedErr: true},
		{name: "no remote, local master", git: fakeGit{
			"rev-parse --verify --quiet refs/heads/master" + commit: "abc\n",
		}, want: Default{Ref: "refs/heads/master", Short: "master"}, fetchedErr: true},
		{name: "nothing", remote: "origin", git: fakeGit{}, fetchedErr: true, localErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fetched, err := FetchedDefault(context.Background(), c.git, c.remote)
			var missing *NoDefaultBranchError
			if c.fetchedErr {
				if !errors.As(err, &missing) || !missing.Fetched || !strings.Contains(err.Error(), "--from") {
					t.Fatalf("FetchedDefault: %v, %v; want a NoDefaultBranchError asking for --from", fetched, err)
				}
			} else if err != nil || fetched != c.want {
				t.Fatalf("FetchedDefault: %v, %v; want %v", fetched, err, c.want)
			}
			found, err := DefaultBranch(context.Background(), c.git, c.remote)
			if c.localErr {
				if !errors.As(err, &missing) || missing.Fetched {
					t.Fatalf("DefaultBranch: %v, %v; want a NoDefaultBranchError", found, err)
				}
				return
			}
			if err != nil || found != c.want {
				t.Fatalf("DefaultBranch: %v, %v; want %v", found, err, c.want)
			}
		})
	}
}

func TestParseURL(t *testing.T) {
	cases := []struct {
		url, want, user string
		other, bad      bool
	}{
		{url: "https://github.com/zima-oss/jackstay.git", want: "zima-oss/jackstay"},
		{url: "https://github.com/zima-oss/jackstay", want: "zima-oss/jackstay"},
		{url: "https://github.com/zima-oss/jackstay/", want: "zima-oss/jackstay"},
		{url: "https://GitHub.com/zima-oss/jackstay.git", want: "zima-oss/jackstay"},
		{url: "https://octocat@github.com/zima-oss/jackstay.git", want: "zima-oss/jackstay", user: "octocat"},
		{url: "https://octocat:secret@github.com/zima-oss/jackstay.git", want: "zima-oss/jackstay", user: "octocat"},
		{url: "https://github.com:443/o/r", want: "o/r"},
		{url: "git@github.com:zima-oss/jackstay.git", want: "zima-oss/jackstay"},
		{url: "github.com:zima-oss/jackstay", want: "zima-oss/jackstay"},
		{url: "ssh://git@github.com/zima-oss/jackstay", want: "zima-oss/jackstay"},
		{url: "ssh://git@github.com:22/zima-oss/jackstay.git", want: "zima-oss/jackstay"},
		{url: "https://github.com/o/my.repo_name-1.git", want: "o/my.repo_name-1"},
		{url: "https://gitlab.com/o/r.git", other: true},
		{url: "https://github.com.evil.example/o/r.git", other: true},
		{url: "git@gitlab.com:o/r.git", other: true},
		{url: "/Users/me/remote.git", other: true},
		{url: "../remote.git", other: true},
		{url: "file:///Users/me/remote.git", other: true},
		{url: "https://github.com/onlyowner", bad: true},
		{url: "https://github.com/o/r/extra", bad: true},
		{url: "https://github.com/o/r.git?x=1", bad: true},
		{url: "https://github.com/o/r#frag", bad: true},
		{url: "http://github.com/o/r.git", bad: true},
		{url: "git://github.com/o/r.git", bad: true},
		{url: "https://github.com:8443/o/r", bad: true},
		{url: "git@github.com:o/r/x.git", bad: true},
		{url: "git@github.com:-o/r.git", bad: true},
		{url: "git@github.com:o/..", bad: true},
		{url: "git@github.com:o/", bad: true},
		{url: "https://github.com/o/r%20x", bad: true},
	}
	for _, c := range cases {
		t.Run(c.url, func(t *testing.T) {
			got, err := ParseURL(c.url)
			var other *OtherHostError
			var bad *BadURLError
			switch {
			case c.other:
				if !errors.As(err, &other) {
					t.Fatalf("got %v, %v; want OtherHostError", got, err)
				}
			case c.bad:
				if !errors.As(err, &bad) {
					t.Fatalf("got %v, %v; want BadURLError", got, err)
				}
			case err != nil || got.String() != c.want || got.User != c.user:
				t.Fatalf("got %v (user %q), %v; want %s (user %q)", got, got.User, err, c.want, c.user)
			}
		})
	}
}

func TestMalformedRemoteErrorsDoNotIncludeCredentials(t *testing.T) {
	const marker = "audit-fixture-value"
	for _, remote := range []string{
		"https://audit:" + marker + "@gitlab.example/owner/repo",
		"https://audit:" + marker + "@github.com/only-owner",
		"https://audit:" + marker + "%zz@github.com/owner/repo",
		"https://github.com/owner/repo?key=" + marker,
		"https://github.com/owner/repo#" + marker,
	} {
		_, err := ParseURL(remote)
		if err == nil || strings.Contains(err.Error(), marker) {
			t.Fatalf("wanted a sanitized parse error, got %v", err)
		}
	}
}

func TestRepositoryOf(t *testing.T) {
	git := fakeGit{"remote": "origin\nfork\n", "config --get remote.origin.url": "git@github.com:zima-oss/jackstay.git\n",
		"config --get remote.fork.url": "https://me@github.com/me/jackstay\n"}
	cases := []struct {
		name    string
		section map[string]any
		want    string
		user    string
		bad     bool
	}{
		{name: "no section", want: "zima-oss/jackstay"},
		{name: "the configured remote", section: map[string]any{"remote": "fork"}, want: "me/jackstay", user: "me"},
		{name: "repository overrides", section: map[string]any{"repository": "a/b", "remote": "fork"}, want: "a/b"},
		{name: "a repository that is not owner/name", section: map[string]any{"repository": "a/b/c"}, bad: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := RepositoryOf(context.Background(), git, c.section)
			var bad *BadURLError
			if c.bad {
				if !errors.As(err, &bad) {
					t.Fatalf("got %v, %v; want BadURLError", got, err)
				}
				return
			}
			if err != nil || got.String() != c.want || got.User != c.user {
				t.Fatalf("got %v (user %q), %v; want %s (user %q)", got, got.User, err, c.want, c.user)
			}
		})
	}
}
