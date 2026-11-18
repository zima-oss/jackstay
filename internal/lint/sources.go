package lint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// gitRunner is what lint asks git through (internal/gitcli satisfies it).
type gitRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

func run(git gitRunner, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	return git.Run(ctx, args...)
}

// runWithin asks git within ctx, or, when ctx is nil (lint's own sources), with run's timeout.
func runWithin(ctx context.Context, git gitRunner, args ...string) ([]byte, error) {
	if ctx == nil {
		return run(git, args...)
	}
	return git.Run(ctx, args...)
}

// WorktreeFiles is the checkout at root as the rules read it, git asked within ctx.
func WorktreeFiles(ctx context.Context, root string, git gitRunner) Files {
	return worktreeFiles{root: root, git: git, ctx: ctx}
}

// RevisionFiles is a commit's or a tree object's files as the rules read them, git asked within ctx.
func RevisionFiles(ctx context.Context, revision string, git gitRunner) Files {
	return revisionFiles{revision: revision, git: git, ctx: ctx}
}

// GitHistory is the history behind the revisions (a merge's two sides, or one commit) as the
// tree's rules read it, git asked within ctx.
func GitHistory(ctx context.Context, git gitRunner, revisions ...string) History {
	return gitHistory{revisions: revisions, git: git, ctx: ctx}
}

// worktreeFiles is the checkout on disk: the files git tracks and the untracked ones it does not
// ignore (a change not committed yet is judged too).
type worktreeFiles struct {
	root string
	git  gitRunner
	ctx  context.Context // nil: run's timeout
}

func (files worktreeFiles) Paths() ([]string, error) {
	out, err := runWithin(files.ctx, files.git, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var paths []string
	seen := map[string]bool{}
	for _, filePath := range strings.Split(string(out), "\x00") {
		if filePath == "" || seen[filePath] {
			continue
		}
		seen[filePath] = true
		if _, err := os.Lstat(filepath.Join(files.root, filepath.FromSlash(filePath))); err == nil {
			paths = append(paths, filePath)
		}
	}
	return paths, nil
}

// Regular says whether a path is a regular file on disk (false when absent or anything else).
func (files worktreeFiles) Regular(filePath string) (bool, error) {
	info, err := os.Lstat(filepath.Join(files.root, filepath.FromSlash(filePath)))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

func (files worktreeFiles) Read(filePath string) ([]byte, bool, error) {
	data, err := os.ReadFile(filepath.Join(files.root, filepath.FromSlash(filePath)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return data, err == nil, err
}

// revisionFiles is a commit's (or a tree object's) files.
type revisionFiles struct {
	revision string
	git      gitRunner
	ctx      context.Context // nil: run's timeout
}

func (files revisionFiles) Paths() ([]string, error) {
	out, err := runWithin(files.ctx, files.git, "ls-tree", "-r", "-z", "--name-only", files.revision)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, filePath := range strings.Split(string(out), "\x00") {
		if filePath != "" {
			paths = append(paths, filePath)
		}
	}
	return paths, nil
}

// Regular says whether a path is a regular file (100644 or 100755) in the tree.
func (files revisionFiles) Regular(filePath string) (bool, error) {
	out, err := runWithin(files.ctx, files.git, "ls-tree", files.revision, "--", filePath)
	if err != nil {
		return false, err
	}
	fields := strings.Fields(string(out))
	return len(fields) >= 1 && (fields[0] == "100644" || fields[0] == "100755"), nil
}

func (files revisionFiles) Read(filePath string) ([]byte, bool, error) {
	out, err := runWithin(files.ctx, files.git, "ls-tree", files.revision, "--", filePath)
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil, false, nil
	}
	data, err := runWithin(files.ctx, files.git, "cat-file", "blob", files.revision+":"+filePath)
	return data, err == nil, err
}

// gitHistory is the history of a revision as the tree's rules read it.
type gitHistory struct {
	revisions []string // commits whose history is read (a merge's two sides, or one commit)
	git       gitRunner
	ctx       context.Context // nil: run's timeout
}

func (history gitHistory) Added(dir string) ([]string, error) {
	// NUL-separated names (no quoting of non-ASCII paths); -m so a file a merge resolution added
	// is found too.
	out, err := runWithin(history.ctx, history.git, append(append([]string{"log", "-m", "-z", "--diff-filter=A", "--name-only", "--format="}, history.revisions...), "--", dir)...)
	if err != nil {
		return nil, err
	}
	var paths []string
	seen := map[string]bool{}
	for _, name := range strings.Split(string(out), "\x00") {
		name = strings.Trim(name, "\n")
		if name != "" && !seen[name] {
			seen[name] = true
			paths = append(paths, name)
		}
	}
	return paths, nil
}

func (history gitHistory) FirstAdded(filePath string) (string, []byte, error) {
	out, err := runWithin(history.ctx, history.git, append(append([]string{"log", "-m", "--diff-filter=A", "--format=%H"}, history.revisions...), "--", filePath)...)
	if err != nil {
		return "", nil, err
	}
	commits := strings.Fields(string(out))
	if len(commits) == 0 {
		return "", nil, nil
	}
	first := commits[len(commits)-1]
	data, _, err := revisionFiles{revision: first, git: history.git, ctx: history.ctx}.Read(filePath)
	return first, data, err
}
