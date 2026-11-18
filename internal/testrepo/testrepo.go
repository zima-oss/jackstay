// Package testrepo makes git repositories for tests: a fresh folder, fixed dates and author, jackstay's
// own variables cleared so the developer's JACKSTAY_HOME never reaches a test.
package testrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a repository made for one test.
type Repo struct {
	T    *testing.T
	Root string
	Home string
}

// New makes a repository with files committed on main, sets JACKSTAY_HOME to a fresh folder and works in
// the repository's root.
func New(t *testing.T, files map[string]string) *Repo {
	t.Helper()
	for _, pair := range os.Environ() {
		if name, _, _ := strings.Cut(pair, "="); strings.HasPrefix(name, "JACKSTAY_") {
			t.Setenv(name, "")
			_ = os.Unsetenv(name)
		}
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := &Repo{T: t, Root: filepath.Join(base, "repo"), Home: filepath.Join(base, "home")}
	t.Setenv("JACKSTAY_HOME", repo.Home)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.invalid")
	t.Setenv("GIT_AUTHOR_DATE", "2026-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:00Z")
	if err := os.MkdirAll(repo.Root, 0o750); err != nil {
		t.Fatal(err)
	}
	repo.Git("init", "-q", "-b", "main")
	repo.Write(files)
	repo.Git("add", "-A")
	repo.Git("commit", "-q", "--allow-empty", "-m", "fixture")
	t.Chdir(repo.Root)
	return repo
}

// Write writes files (paths from the root), making their folders.
func (repo *Repo) Write(files map[string]string) {
	repo.T.Helper()
	for name, content := range files {
		place := filepath.Join(repo.Root, name)
		if err := os.MkdirAll(filepath.Dir(place), 0o750); err != nil {
			repo.T.Fatal(err)
		}
		if err := os.WriteFile(place, []byte(content), 0o600); err != nil {
			repo.T.Fatal(err)
		}
	}
}

// Git runs git in the root and returns its output; a failure fails the test.
func (repo *Repo) Git(args ...string) string {
	repo.T.Helper()
	return repo.GitIn(repo.Root, args...)
}

// GitIn runs git in a folder.
func (repo *Repo) GitIn(folder string, args ...string) string {
	repo.T.Helper()
	command := exec.Command("git", append([]string{"-C", folder}, args...)...)
	out, err := command.CombinedOutput()
	if err != nil {
		repo.T.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Commit commits everything with a message (and an author date when given).
func (repo *Repo) Commit(message string, files map[string]string) string {
	repo.T.Helper()
	repo.Write(files)
	repo.Git("add", "-A")
	repo.Git("commit", "-q", "--allow-empty", "-m", message)
	return repo.Git("rev-parse", "HEAD")
}
