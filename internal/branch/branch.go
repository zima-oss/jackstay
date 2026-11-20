// Package branch is `jst sync` (this branch brought up to date with the default branch, merge only)
// and `jst start` (a new branch and worktree on the default branch as fetched):
// docs/design/implementation.md section 8.5. Exit 0 done or nothing to do; 1 a
// conflict (the files named, the merge left to resolve or abort), branches moved apart, git
// refused; 2 the request (uncommitted work, a detached HEAD, a merge under way, a taken name).
package branch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/project"
	"github.com/zima-oss/jackstay/internal/remote"
)

// gitTimeout bounds each call to git (a fetch or a push over a slow network included).
const gitTimeout = 5 * time.Minute

func init() {
	cli.Register(
		cli.Command{Name: "sync", Summary: "bring this branch up to date with the default branch", Usage: syncHelp, Run: runSync},
		cli.Command{Name: "start", Summary: "a new branch and its worktree on the default branch as fetched", Usage: startHelp, Run: runStart},
	)
}

// errHelp is -h or --help.
var errHelp = errors.New("help")

// refused is the request refused (exit 2); failed is git refusing or a conflict (exit 1).
func refused(format string, values ...any) error { return project.Fail(format, values...) }

func failed(format string, values ...any) error {
	return &project.Problem{Code: 1, Message: fmt.Sprintf(format, values...)}
}

// finish prints a command's error and is its exit status.
func finish(command string, err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "jst %s: %v\n", command, err)
	var problem *project.Problem
	if errors.As(err, &problem) {
		return problem.Code
	}
	return 2
}

// parseArgs reads flags anywhere among the words (`--name value`, `--name=value`; `--` ends them):
// switches are the flags without a value, values those with one. The rest is returned in order.
func parseArgs(args []string, switches map[string]*bool, values map[string]*string) ([]string, error) {
	var positional []string
	for index := 0; index < len(args); index++ {
		word := args[index]
		switch {
		case word == "--":
			return append(positional, args[index+1:]...), nil
		case word == "-h" || word == "--help":
			return nil, errHelp
		case !strings.HasPrefix(word, "-") || word == "-":
			positional = append(positional, word)
			continue
		}
		name, given, hasValue := strings.Cut(word, "=")
		if target, ok := switches[name]; ok && !hasValue {
			*target = true
			continue
		}
		target, ok := values[name]
		switch {
		case !ok:
			return nil, fmt.Errorf("unknown option %s", word)
		case hasValue:
			*target = given
		case index+1 < len(args):
			index++
			*target = args[index]
		default:
			return nil, fmt.Errorf("%s needs a value", name)
		}
	}
	return positional, nil
}

// repo asks git about one folder.
type repo struct {
	git gitcli.Git
}

func at(dir string) repo { return repo{git: gitcli.Git{Dir: dir}} }

func (here repo) run(args ...string) ([]byte, error) {
	return here.runInput(nil, args...)
}

func (here repo) runInput(input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return here.git.RunInput(ctx, input, args...)
}

// line is git's answer without its surrounding space ("" with the error when git refused).
func (here repo) line(args ...string) (string, error) {
	out, err := here.run(args...)
	return strings.TrimSpace(string(out)), err
}

func (here repo) succeeds(args ...string) bool {
	_, err := here.run(args...)
	return err == nil
}

// gitSaid is what git said on its standard error when it refused, cut to limit characters.
func gitSaid(err error, limit int) string {
	var exitErr *gitcli.ExitError
	said := err.Error()
	if errors.As(err, &exitErr) {
		said = exitErr.Diagnostic()
	}
	if len(said) > limit {
		said = said[:limit] + " …"
	}
	return said
}

// place is where a command works: this checkout's root, the main checkout, and the remote ("" when
// the repository has none).
type place struct {
	root, main, remote string
}

// locate finds the checkout around dir, its main checkout and the remote (the shared rule,
// `github.remote` of jackstay.json first).
func locate(dir string) (place, error) {
	root, err := at(dir).line("rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		if errors.Is(err, gitcli.ErrNotRepository) {
			return place{}, refused("%s is not in a git repository", dir)
		}
		return place{}, refused("%s: git cannot read it: %s", dir, gitSaid(err, 300))
	}
	listed, err := at(root).run("worktree", "list", "--porcelain", "-z")
	if err != nil {
		return place{}, refused("git worktree list failed: %s", gitSaid(err, 300))
	}
	first, _, _ := bytes.Cut(listed, []byte{0})
	main, found := strings.CutPrefix(string(first), "worktree ")
	if !found || main == "" {
		return place{}, refused("git worktree list gave no main checkout")
	}
	_, loaded, err := config.Load(root, []string{"github"})
	if err != nil {
		return place{}, refused("%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	name, err := remote.Name(ctx, at(main).git, remote.ConfiguredRemote(loaded.Section("github")))
	switch {
	case errors.Is(err, remote.ErrNoRemote):
		name = ""
	case err != nil:
		return place{}, refused("%v", err)
	}
	return place{root: root, main: main, remote: name}, nil
}

// fetch fetches the remote (nothing when there is none, said).
func (where place) fetch(stdout io.Writer) error {
	if where.remote == "" {
		fmt.Fprintln(stdout, "no remote: nothing fetched")
		return nil
	}
	if _, err := at(where.main).run("fetch", "--prune", "--quiet", where.remote); err != nil {
		return failed("git fetch %s failed: %s", where.remote, gitSaid(err, 300))
	}
	return nil
}

// defaultBranch is the default branch of where's repository, fetched or local.
func (where place) defaultBranch(fetchedOnly bool) (remote.Default, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if fetchedOnly {
		return remote.FetchedDefault(ctx, at(where.main).git, where.remote)
	}
	return remote.DefaultBranch(ctx, at(where.main).git, where.remote)
}

// splitNames are the names of a NUL-separated list, as git wrote them (nothing trimmed: a name may
// begin with a space or a tab).
func splitNames(listed []byte) []string {
	var names []string
	for _, name := range bytes.Split(listed, []byte{0}) {
		if len(name) > 0 {
			names = append(names, string(name))
		}
	}
	return names
}

// listed is up to five names and a count of the rest, each quoted.
func listed(names []string) string {
	shown := names
	if len(shown) > 5 {
		shown = shown[:5]
	}
	quoted := make([]string, len(shown))
	for index, name := range shown {
		quoted[index] = fmt.Sprintf("%q", name)
	}
	text := strings.Join(quoted, ", ")
	if len(names) > len(shown) {
		text += fmt.Sprintf(" and %d more", len(names)-len(shown))
	}
	return text
}

// absolute is a path made absolute from the working folder.
func absolute(given string) (string, error) {
	if filepath.IsAbs(given) {
		return filepath.Clean(given), nil
	}
	workDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(workDir, given), nil
}
