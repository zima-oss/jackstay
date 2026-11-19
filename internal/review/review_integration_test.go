//go:build integration

package review

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

// processGone says a process no longer runs (a zombie no one reaped yet counts as gone).
func processGone(pid int) bool {
	row, found, err := procs.Row(pid)
	if err != nil {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	}
	return !found || row.Zombie
}

func TestPinnedTreeWithRealGit(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"AGENTS.md": "rules\n", "a.go": "one\n"})
	first := repo.Commit("first", nil)
	git := RepositoryAt(repo.Root)
	ctx := context.Background()
	store := Store{Root: filepath.Join(t.TempDir(), "store")}
	if err := os.MkdirAll(store.TreesPath(), 0o700); err != nil {
		t.Fatal(err)
	}

	resolved, err := git.Resolve(ctx, "HEAD")
	if err != nil || resolved != first {
		t.Fatal(resolved, err)
	}
	if _, err := git.Resolve(ctx, "--output=/tmp/x"); err == nil {
		t.Fatal("an option taken for a revision")
	}
	root := strings.TrimSpace(repo.Git("rev-list", "--max-parents=0", "HEAD"))
	parent, err := git.FirstParent(ctx, root)
	if want := repo.Git("hash-object", "-t", "tree", os.DevNull); err != nil || parent != want {
		t.Fatalf("a root commit's base is %q, %v; want %q", parent, err, want)
	}
	if !git.Exists(ctx, first, "AGENTS.md") || git.Exists(ctx, first, "missing.md") {
		t.Fatal("exists")
	}
	if !git.ValidBranchName(ctx, "feature/review") || git.ValidBranchName(ctx, "bad..name") {
		t.Fatal("branch names")
	}

	// A hook that would hold the call past any deadline never runs (W-019's redesign).
	hookRan := filepath.Join(t.TempDir(), "hook-ran")
	hooks := strings.TrimSpace(repo.Git("rev-parse", "--git-path", "hooks"))
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(repo.Root, hooks)
	}
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\ntouch '"+hookRan+"'\nsleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tree := treePath(store, "feature/x", 1, "codex")
	if err := git.AddTree(ctx, tree, first, ownReason()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hookRan); err == nil {
		t.Fatal("review's git ran a hook")
	}
	repo.Write(map[string]string{"a.go": "changed in the checkout\n"})
	pinned, _ := os.ReadFile(filepath.Join(tree, "a.go"))
	if string(pinned) != "one\n" {
		t.Fatalf("the pinned tree moved: %q", pinned)
	}
	repo.Git("worktree", "prune")
	locked, err := git.LockedTrees(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for path, reason := range locked {
		if strings.HasSuffix(path, filepath.Base(tree)) && strings.HasPrefix(reason, reasonPrefix) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the tree is not locked by jst review: %v", locked)
	}
	// Our own process is alive: not stale.
	if removed := RemoveStaleTrees(ctx, git, store, reasonAlive); len(removed) != 0 {
		t.Fatalf("removed a live tree: %v", removed)
	}
	// A dead owner: stale, removed.
	if removed := RemoveStaleTrees(ctx, git, store, func(string) bool { return false }); len(removed) != 1 {
		t.Fatalf("stale tree not removed: %v", removed)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatal("the stale tree is still there")
	}

	// A checkout filter that stalls (as a broken LFS would): a stop at the deadline, made once the
	// filter has acknowledged it runs, ends git and the filter. The guard only ends a broken test.
	const guard = 60 * time.Second
	ready := filepath.Join(t.TempDir(), "filter-pid")
	repo.Git("config", "filter.stall.smudge", "echo $$ > '"+ready+".tmp'; mv '"+ready+".tmp' '"+ready+"'; exec sleep 3000")
	repo.Git("config", "filter.stall.required", "true")
	repo.Git("config", "filter.stall.clean", "cat")
	stalled := repo.Commit("stalled", map[string]string{".gitattributes": "*.bin filter=stall\n", "x.bin": "data\n"})
	deadline, cancel := context.WithCancel(ctx)
	defer cancel()
	added := make(chan error, 1)
	go func() { added <- git.AddTree(deadline, treePath(store, "feature/x", 9, "codex"), stalled, ownReason()) }()
	filter := 0
	for limit := time.Now().Add(guard); filter == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(limit) {
			t.Fatal("the checkout filter never started")
		}
		if data, err := os.ReadFile(ready); err == nil {
			filter, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
	}
	cancel() // the deadline
	select {
	case err := <-added:
		if err == nil {
			t.Fatal("a stalled checkout did not fail at the deadline")
		}
	case <-time.After(guard):
		t.Fatal("a stalled checkout filter held the call past the deadline")
	}
	for limit := time.Now().Add(guard); !processGone(filter); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(limit) {
			t.Fatalf("the checkout filter %d still runs after the deadline", filter)
		}
	}
	repo.Git("config", "--unset", "filter.stall.smudge")
	repo.Git("config", "--unset", "filter.stall.required")
	repo.Git("config", "--unset", "filter.stall.clean")

	second := repo.Commit("second", map[string]string{"a.go": "two\n"})
	stat, err := git.DiffStat(ctx, first, second)
	if err != nil || !strings.Contains(stat, "a.go") {
		t.Fatal(stat, err)
	}
	other := treePath(store, "feature/x", 2, "codex")
	if err := git.AddTree(ctx, other, second, ownReason()); err != nil {
		t.Fatal(err)
	}
	if err := git.RemoveTree(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("a locked tree was not removed")
	}
	if !reasonAlive(ownReason()) {
		t.Fatal("our own process is not alive by its reason")
	}
}

func TestFirstParentUsesAFormatCorrectEmptyTreeAndRefusesAShallowBoundary(t *testing.T) {
	t.Setenv("GIT_DEFAULT_HASH", "sha256")
	repo := testrepo.New(t, map[string]string{"file": "contents\n"})
	root := repo.Git("rev-parse", "HEAD")
	child := repo.Commit("child", nil)
	repository := RepositoryAt(repo.Root)
	ctx := context.Background()
	base, err := repository.FirstParent(ctx, root)
	if want := repo.Git("hash-object", "-t", "tree", os.DevNull); err != nil || base != want {
		t.Fatalf("SHA-256 root base is %q, %v; want %q", base, err, want)
	}
	if stat, err := repository.DiffStat(ctx, base, root); err != nil || !strings.Contains(stat, "file") {
		t.Fatalf("root review diff = %q, %v", stat, err)
	}
	if got, err := repository.FirstParent(ctx, child); err != nil || got != root {
		t.Fatalf("first parent = %q, %v; want %q", got, err, root)
	}

	shallow := t.TempDir()
	repo.Git("clone", "--quiet", "--depth=1", "--no-local", repo.Root, shallow)
	if _, err := RepositoryAt(shallow).FirstParent(ctx, child); err == nil || !strings.Contains(err.Error(), "is unavailable") {
		t.Fatalf("shallow boundary gave %v; want an unavailable-parent error", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repository.FirstParent(canceled, child); !errors.Is(err, gitcli.ErrTimeout) {
		t.Fatalf("canceled command gave %v; want Git timeout", err)
	}
}

func TestClaudeCodeReviewThroughTheRealDispatcher(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"reviewed.txt": "before\n"})
	base := repo.Git("rev-parse", "HEAD")
	revision := repo.Commit("change", map[string]string{"reviewed.txt": "pinned-after\n"})
	repo.Write(map[string]string{"reviewed.txt": "uncommitted\n"})

	binary := filepath.Join(t.TempDir(), "claude-fixture")
	const script = `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  printf '%s\n' '2.1.248 (Claude Code)'
  exit 0
fi
arguments="$*"
case "$arguments" in
  *--restricted*--safe-mode*--no-session-persistence*--json-schema*--permission-mode*dontAsk*--tools*Read,Glob,Grep*--disallowedTools*mcp__*) ;;
  *) exit 21 ;;
esac
case "$arguments" in *dangerously*|*Bash*|*--add-dir*) exit 22 ;; esac
[ "$(cat reviewed.txt)" = pinned-after ]
[ -z "${CLAUDECODE:-}" ] && [ -z "${UNRELATED_FIXTURE_MARKER:-}" ]
prompt=$(cat)
case "$prompt" in
  *'diff --git a/reviewed.txt b/reviewed.txt'*'-before'*'+pinned-after'*) ;;
  *) exit 23 ;;
esac
case "$prompt" in *uncommitted*) exit 24 ;; esac
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"structured_output":{"verdict":"approve","summary":"pinned diff inspected","findings":[],"prior":[]},"usage":{"input_tokens":12,"output_tokens":4}}'
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("CLAUDECODE", "nested")
	t.Setenv("UNRELATED_FIXTURE_MARKER", "not-forwarded")

	settings := Defaults()
	settings.Use = []string{"claude-code"}
	configured := defaultReviewer("claude-code")
	configured.Command = binary
	settings.Reviewers["claude-code"] = configured
	machine, err := RealMachine(homedir.SharedPath)
	if err != nil {
		t.Fatal(err)
	}
	var out, complaints bytes.Buffer
	round := Round{Settings: settings, Repo: RepositoryAt(repo.Root),
		Store: Store{Root: filepath.Join(t.TempDir(), "review-store")}, Machine: machine,
		Out: &out, Errors: &complaints}
	result := round.Run(context.Background(), Options{Revision: base + ".." + revision, Task: "claude-test"})
	if result.Status != ExitClear || result.Record == nil || result.Record.Summaries["claude-code"] != "pinned diff inspected" {
		t.Fatalf("unexpected result %+v, stdout %q, stderr %q", result, &out, &complaints)
	}
	if outcomes, _ := CheckReasons(settings, round.Store, "claude-test", base, revision); len(outcomes) != 0 {
		t.Fatalf("recorded review cannot be checked: %v", outcomes)
	}
}
