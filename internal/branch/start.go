package branch

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const startUsage = "usage: jst start NAME [--from REF] [--path DIR] [--no-fetch]"

// notInSlug is what a branch name loses in its worktree folder's name.
var notInSlug = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// startRequest is what `jst start` was asked.
type startRequest struct {
	name, from, path string
	noFetch          bool
}

func runStart(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	var request startRequest
	positional, err := parseArgs(args, map[string]*bool{"--no-fetch": &request.noFetch},
		map[string]*string{"--from": &request.from, "--path": &request.path})
	switch {
	case errors.Is(err, errHelp):
		fmt.Fprintln(stdout, startUsage)
		return 0
	case err == nil && len(positional) != 1:
		err = fmt.Errorf("one branch name, not %d", len(positional))
	}
	if err != nil {
		fmt.Fprintf(stderr, "jst start: %v\n%s\n", err, startUsage)
		return 2
	}
	request.name = positional[0]
	workDir, err := os.Getwd()
	if err != nil {
		return finish("start", refused("%v", err), stderr)
	}
	return finish("start", start(request, workDir, stdout), stderr)
}

// slug is a branch name as a folder's name (`feat/new-thing` is `feat-new-thing`).
func slug(name string) string {
	if made := strings.Trim(notInSlug.ReplaceAllString(name, "-"), "-."); made != "" {
		return made
	}
	return "work"
}

func start(request startRequest, workDir string, stdout io.Writer) error {
	where, err := locate(workDir)
	if err != nil {
		return err
	}
	main := at(where.main)
	if strings.HasPrefix(request.name, "-") || !main.succeeds("check-ref-format", "--branch", request.name) {
		return refused("%q is not a branch name git accepts", request.name)
	}
	if main.succeeds("rev-parse", "--verify", "--quiet", "refs/heads/"+request.name) {
		return refused("the branch %s is already there (`jst tidy --branch %s` says whether it is finished)", request.name, request.name)
	}
	if !request.noFetch {
		if err := where.fetch(stdout); err != nil {
			return err
		}
	}
	base := request.from
	if base == "" {
		// The default branch as fetched, not a local `main` that may be days old; with no remote at
		// all, the local default branch is the only one there is.
		found, err := where.defaultBranch(where.remote != "")
		if err != nil {
			return refused("%v", err)
		}
		base = found.Ref
	} else if strings.HasPrefix(base, "-") || !main.succeeds("rev-parse", "--verify", "--quiet", base+"^{commit}") {
		return refused("%s is not a commit here", base)
	}
	path := request.path
	if path == "" {
		inside := filepath.Join(".claude", "worktrees", slug(request.name))
		if main.succeeds("check-ignore", "-q", inside) {
			path = filepath.Join(where.main, inside)
		} else {
			path = filepath.Join(where.main+"-worktrees", slug(request.name))
		}
	}
	if path, err = absolute(path); err != nil {
		return refused("%v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		return refused("%s is already there", path)
	}
	if _, err := main.run("worktree", "add", "--quiet", "--no-track", "-b", request.name, path, base); err != nil {
		return failed("git worktree add failed: %s", gitSaid(err, 300))
	}
	short, _ := main.line("rev-parse", "--short", base)
	fmt.Fprintf(stdout, "branch %s on %s (%s), worktree %s\n", request.name, base, short, path)
	fmt.Fprintf(stdout, "cd %s\n", path)
	return nil
}
