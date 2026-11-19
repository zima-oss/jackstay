// Package tidy is `jst tidy`: a session removes the branches and worktrees whose work is done
// (docs/design/implementation.md section 8.4, W-006). The guard refuses the raw forms
// (`git branch -D`, `git worktree remove --force`, `rm -r` of a worktree) because from a command's
// text it cannot tell finished work from unfinished; tidy asks git, GitHub and the process table
// first, and removes only what it has shown loses nothing.
//
//	jst tidy [--fix] [--branch NAME]... [--worktree PATH]... [--no-fetch] [--no-github] [--json] [DIR]
//
// Reporting is the default; --fix removes. Exit 0: nothing finished is left; 1: something
// finished is listed and not removed, or a removal failed or was called off; 2: the request was
// wrong (not a repository, a branch or worktree that does not exist, an unknown flag).
package tidy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/project"
)

func init() {
	cli.Register(cli.Command{Name: "tidy", Summary: "report the branches and worktrees whose work is done (--fix: remove them)", Usage: help, Run: run})
}

const usage = `usage: jst tidy [--fix] [--branch NAME]... [--worktree PATH]... [--no-fetch] [--no-github] [--json] [DIR]

  --fix          remove what is finished (default: only report)
  --branch NAME  only this branch (and its worktree); may repeat
  --worktree P   only this worktree (and its branch); may repeat
  --no-fetch     do not fetch the remote first
  --no-github    do not ask GitHub about pull requests
  --json         the report as JSON
`

// request is the command line read.
type request struct {
	fix, noFetch, noGitHub, json bool
	branches, worktrees          []string
	dir                          string
}

// errHelp is -h or --help.
var errHelp = errors.New("help")

func parse(args []string) (request, error) {
	wanted := request{dir: "."}
	var positional []string
	switches := map[string]*bool{"--fix": &wanted.fix, "--no-fetch": &wanted.noFetch, "--no-github": &wanted.noGitHub, "--json": &wanted.json}
	lists := map[string]*[]string{"--branch": &wanted.branches, "--worktree": &wanted.worktrees}
	for index := 0; index < len(args); index++ {
		word := args[index]
		switch {
		case word == "--":
			positional = append(positional, args[index+1:]...)
			index = len(args)
			continue
		case word == "-h" || word == "--help":
			return request{}, errHelp
		case !strings.HasPrefix(word, "-") || word == "-":
			positional = append(positional, word)
			continue
		}
		name, given, hasValue := strings.Cut(word, "=")
		if target, ok := switches[name]; ok && !hasValue {
			*target = true
			continue
		}
		target, ok := lists[name]
		switch {
		case !ok:
			return request{}, fmt.Errorf("unknown option %s", word)
		case hasValue && given != "":
			*target = append(*target, given)
		case !hasValue && index+1 < len(args) && args[index+1] != "":
			index++
			*target = append(*target, args[index])
		default:
			return request{}, fmt.Errorf("%s needs a value", name)
		}
	}
	switch len(positional) {
	case 0:
	case 1:
		wanted.dir = positional[0]
	default:
		return request{}, fmt.Errorf("one folder at most, not %s", strings.Join(positional, " "))
	}
	return wanted, nil
}

func run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	wanted, err := parse(args)
	if errors.Is(err, errHelp) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "jst tidy: %v\n%s", err, usage)
		return 2
	}
	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "jst tidy: %v\n", err)
		return 2
	}
	tidy := &tidier{connect: connectGitHub, processes: realProcesses, workDir: realPath(workDir), pid: os.Getpid(),
		noGitHub: wanted.noGitHub}
	return tidy.main(context.Background(), wanted, stdout, stderr)
}

// main surveys, shows, and with --fix removes; its result is the exit status.
func (tidy *tidier) main(ctx context.Context, wanted request, stdout, stderr io.Writer) int {
	found, err := tidy.survey(ctx, scope{start: realPath(wanted.dir), branches: wanted.branches,
		worktrees: wanted.worktrees, fetch: !wanted.noFetch})
	if err != nil {
		fmt.Fprintf(stderr, "jst tidy: %v\n", err)
		var problem *project.Problem
		if errors.As(err, &problem) {
			return problem.Code
		}
		return 2
	}
	if !wanted.json {
		show(found, stdout)
	}
	waiting := found.waiting()
	if !wanted.fix {
		switch {
		case wanted.json:
			return writeJSON(found, waiting, stdout, stderr)
		case waiting > 0:
			fmt.Fprintf(stdout, "\n%d finished; `jst tidy --fix` removes them (the same filters).\n", waiting)
		default:
			fmt.Fprintln(stdout, "\nnothing finished to remove")
		}
		return exitFor(waiting)
	}
	if !wanted.json {
		fmt.Fprintln(stdout)
	}
	failures := tidy.fix(ctx, found, func(done action) {
		found.Done = append(found.Done, done)
		if !wanted.json {
			fmt.Fprintln(stdout, actionLine(done))
		}
	})
	if wanted.json {
		found.Failures = &failures
		return writeJSON(found, failures, stdout, stderr)
	}
	return exitFor(failures)
}

func exitFor(count int) int {
	if count > 0 {
		return 1
	}
	return 0
}

func writeJSON(found *report, count int, stdout, stderr io.Writer) int {
	text, err := json.MarshalIndent(found, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "jst tidy: %v\n", err)
		return 2
	}
	fmt.Fprintln(stdout, string(text))
	return exitFor(count)
}

func show(found *report, out io.Writer) {
	known := found.Default
	if known == "" {
		known = "unknown"
	}
	fmt.Fprintf(out, "repository %s  (default branch: %s)\n", found.Repository, known)
	for _, said := range found.Notes {
		fmt.Fprintf(out, "note: %s\n", said.Detail)
	}
	for _, item := range found.Worktrees {
		label, branch, text := "keep  ", item.Branch, details(item.Keep)
		if item.Remove {
			label, text = "REMOVE", item.Why.Detail
		}
		if branch == "" {
			branch = "detached"
		}
		fmt.Fprintf(out, "%s worktree %s [%s]: %s\n", label, item.Path, branch, text)
	}
	for _, item := range found.Branches {
		label, text := "keep  ", details(item.Keep)
		if item.Remove {
			label, text = "REMOVE", item.Why.Detail
		}
		fmt.Fprintf(out, "%s branch %s: %s\n", label, item.Name, text)
	}
	for _, place := range found.Prunable {
		fmt.Fprintf(out, "PRUNE  worktree record %s: its folder is gone\n", place)
	}
}

func actionLine(done action) string {
	switch done.ID {
	case didRemoveTree:
		return "removed worktree " + done.Target
	case didDeleteBranch:
		return "deleted branch " + done.Target + " (" + done.Detail + ")"
	case didKeepTree:
		return "kept worktree " + done.Target + ": " + done.Detail
	case didKeepBranch:
		return "kept branch " + done.Target + ": " + done.Detail
	case didFailTree:
		return "could not remove worktree " + done.Target + ": " + done.Detail
	case didFailBranch:
		return "could not delete branch " + done.Target + ": " + done.Detail
	case didPrune:
		return "pruned the worktree record for " + done.Target + ": " + done.Detail
	case didFailPrune:
		return "could not prune the worktree record for " + done.Target + ": " + done.Detail
	default:
		return done.Detail + ": " + done.Target
	}
}
