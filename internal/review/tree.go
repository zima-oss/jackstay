package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/pinned"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// emptyTree is git's tree with nothing in it: the base of a root commit.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// Repository is what a round asks of git (section 3.1); tests give a fake.
type Repository interface {
	Resolve(ctx context.Context, name string) (string, error)
	FirstParent(ctx context.Context, commit string) (string, error)
	Branch(ctx context.Context) (string, error)
	ValidBranchName(ctx context.Context, name string) bool
	Dirty(ctx context.Context) (bool, error)
	Subject(ctx context.Context, commit string) (string, error)
	DiffStat(ctx context.Context, from, to string) (string, error)
	Show(ctx context.Context, commit, path string) ([]byte, error)
	Exists(ctx context.Context, commit, path string) bool
	AddTree(ctx context.Context, path, commit, reason string) error
	RemoveTree(ctx context.Context, path string) error
	LockedTrees(ctx context.Context) (map[string]string, error)
}

// gitRepository is Repository through git's command line. Each call runs git in a process group of
// its own (supervise.Capture) under the call's context: at the deadline the whole group is
// ended — git and anything it started, a hook or a checkout filter — and reading its output is
// bounded, so no call outlives the budget (git's descendants would otherwise hold a call). Hooks and fsmonitor are also turned off: a review starts nothing
// in the repository but git itself.
type gitRepository struct{ root string }

// RepositoryAt is the repository of a checkout.
func RepositoryAt(root string) Repository { return gitRepository{root: root} }

// aloneArgs make git run no hook and no helper process.
var aloneArgs = []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false"}

func (repo gitRepository) run(ctx context.Context, args ...string) ([]byte, error) {
	env := append(gitcli.WithoutRepository(os.Environ()), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	captured, err := supervise.Capture(ctx, append(append([]string{"git"}, aloneArgs...), args...), repo.root, env, nil)
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), gitcli.ErrTimeout)
	case err != nil:
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	case captured.Status != 0:
		return nil, &gitcli.ExitError{Args: args, Code: captured.Status, Stderr: string(captured.Stderr)}
	}
	return captured.Stdout, nil
}

func (repo gitRepository) line(ctx context.Context, args ...string) (string, error) {
	out, err := repo.run(ctx, args...)
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(string(out), "\n")
	return first, nil
}

func (repo gitRepository) Resolve(ctx context.Context, name string) (string, error) {
	if strings.HasPrefix(name, "-") {
		return "", fmt.Errorf("%q is not a revision", name)
	}
	commit, err := repo.line(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", name+"^{commit}")
	if err != nil || commit == "" {
		return "", fmt.Errorf("%q is not a commit here", name)
	}
	return commit, nil
}

func (repo gitRepository) FirstParent(ctx context.Context, commit string) (string, error) {
	parent, err := repo.line(ctx, "rev-parse", "--verify", "--quiet", commit+"^1")
	if err != nil || parent == "" {
		var exitErr *gitcli.ExitError
		if errors.As(err, &exitErr) || parent == "" {
			return emptyTree, nil
		}
		return "", err
	}
	return parent, nil
}

func (repo gitRepository) Branch(ctx context.Context) (string, error) {
	name, err := repo.line(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	var exitErr *gitcli.ExitError
	if errors.As(err, &exitErr) {
		return "", nil
	}
	return name, err
}

func (repo gitRepository) ValidBranchName(ctx context.Context, name string) bool {
	_, err := repo.run(ctx, "check-ref-format", "--branch", name)
	return err == nil
}

func (repo gitRepository) Dirty(ctx context.Context) (bool, error) {
	out, err := repo.run(ctx, "status", "--porcelain")
	return len(strings.TrimSpace(string(out))) > 0, err
}

func (repo gitRepository) Subject(ctx context.Context, commit string) (string, error) {
	return repo.line(ctx, "log", "-1", "--format=%s", commit)
}

func (repo gitRepository) DiffStat(ctx context.Context, from, to string) (string, error) {
	out, err := repo.run(ctx, "diff", "--stat=120", from, to)
	return strings.TrimRight(string(out), "\n"), err
}

func (repo gitRepository) Show(ctx context.Context, commit, path string) ([]byte, error) {
	return repo.run(ctx, "show", commit+":"+path)
}

func (repo gitRepository) Exists(ctx context.Context, commit, path string) bool {
	path = strings.TrimSuffix(path, "/")
	kind, err := repo.line(ctx, "cat-file", "-t", commit+":"+path)
	return err == nil && (kind == "blob" || kind == "tree")
}

func (repo gitRepository) AddTree(ctx context.Context, path, commit, reason string) error {
	_, err := repo.run(ctx, "worktree", "add", "--detach", "--lock", "--reason", reason, path, commit)
	return err
}

func (repo gitRepository) RemoveTree(ctx context.Context, path string) error {
	_, unlockErr := repo.run(ctx, "worktree", "unlock", path)
	_, removeErr := repo.run(ctx, "worktree", "remove", "--force", path)
	if removeErr != nil {
		return errors.Join(unlockErr, removeErr)
	}
	return nil
}

// LockedTrees are the repository's locked worktrees: path to lock reason.
func (repo gitRepository) LockedTrees(ctx context.Context) (map[string]string, error) {
	out, err := repo.run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	locked := map[string]string{}
	current := ""
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimPrefix(line, "worktree ")
		case line == "locked":
			locked[current] = ""
		case strings.HasPrefix(line, "locked "):
			locked[current] = strings.TrimPrefix(line, "locked ")
		}
	}
	return locked, nil
}

// reasonPrefix begins the lock reason of every tree jst review makes.
const reasonPrefix = "jst review "

// ownReason is the lock reason naming this process: its id and its start.
func ownReason() string { return pinned.OwnWords(reasonPrefix) }

// reasonAlive says whether a lock reason of jst review names a process still running (the same
// id and start). A reason that cannot be read, or a process list that cannot be read, counts as
// alive: a tree is removed only when its owner is known to be gone.
func reasonAlive(reason string) bool { return pinned.Alive(reasonPrefix, reason) }

// RemoveStaleTrees removes trees under the store whose owner is gone (section 3.1).
func RemoveStaleTrees(ctx context.Context, repo Repository, store Store, alive func(string) bool) []string {
	locked, err := repo.LockedTrees(ctx)
	if err != nil {
		return nil
	}
	treesRoot, err := filepath.EvalSymlinks(store.TreesPath())
	if err != nil {
		return nil
	}
	var removed []string
	for path, reason := range locked {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			real = path
		}
		if filepath.Dir(real) != treesRoot || !strings.HasPrefix(reason, reasonPrefix) || alive(reason) {
			continue
		}
		if repo.RemoveTree(ctx, path) == nil {
			removed = append(removed, path)
		}
	}
	return removed
}

// treePath is a new pinned tree's folder.
func treePath(store Store, task string, round int, reviewer string) string {
	return filepath.Join(store.TreesPath(), fmt.Sprintf("%s-r%d-%s-%s", TaskFolder(task), round, reviewer, pinned.RandomName()))
}
