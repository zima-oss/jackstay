//go:build integration

package remote

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/gitcli"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestTheRuleOnARealRepository asks real git: the remote main tracks, its URL, and the default
// branch once fetched.
func TestTheRuleOnARealRepository(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", base)
	bare, work := filepath.Join(base, "remote.git"), filepath.Join(base, "work")
	gitIn(t, base, "init", "-q", "--bare", "-b", "main", bare)
	gitIn(t, base, "init", "-q", "-b", "main", work)
	gitIn(t, work, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "first")
	gitIn(t, work, "remote", "add", "octo-org.github.com", bare)
	gitIn(t, work, "remote", "add", "mirror", bare)
	git := gitcli.Git{Dir: work}
	ctx := context.Background()

	var unclear *UnclearError
	if _, err := Name(ctx, git, ""); !errors.As(err, &unclear) {
		t.Fatalf("two remotes, none chosen: %v", err)
	}
	gitIn(t, work, "push", "-q", "-u", "octo-org.github.com", "main")
	name, err := Name(ctx, git, "")
	if err != nil || name != "octo-org.github.com" {
		t.Fatalf("got %q, %v", name, err)
	}
	found, err := FetchedDefault(ctx, git, name)
	if err != nil || found.Ref != "refs/remotes/octo-org.github.com/main" || found.Short != "main" {
		t.Fatalf("got %+v, %v", found, err)
	}
	var other *OtherHostError
	if _, err := RepositoryOf(ctx, git, nil); !errors.As(err, &other) {
		t.Fatalf("a local path: %v", err)
	}
	gitIn(t, work, "remote", "set-url", "mirror", "https://me@github.com/zima-oss/jackstay.git")
	repository, err := RepositoryOf(ctx, git, map[string]any{"remote": "mirror"})
	if err != nil || repository.String() != "zima-oss/jackstay" || repository.User != "me" {
		t.Fatalf("got %+v, %v", repository, err)
	}
	if _, err := Name(ctx, git, "nope"); err == nil || !strings.Contains(err.Error(), "not a remote here") {
		t.Fatalf("an unknown github.remote: %v", err)
	}
}
