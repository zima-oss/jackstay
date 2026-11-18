//go:build integration

package review

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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
	parent, _ := git.FirstParent(ctx, root)
	if parent != emptyTree {
		t.Fatalf("a root commit's base is the empty tree, not %s", parent)
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
