// Package gitcli is the one place jackstay runs git. Each consumer declares the small interface it needs
// (the guard's questions, the release's reader and writer) and this adapter satisfies it: git runs
// in a given folder under a deadline from the context, with an environment the caller states
// (never the caller's GIT_* by accident), no prompts and no optional locks, and its failures come
// back typed so a caller can tell "not a repository" and "too slow" from git's own refusal.
//
// git runs in a process group of its own. At the context's end the group is asked to stop
// (SIGTERM) and killed (SIGKILL) after stopGrace; once git has ended, what a stop left of its group
// (a hook, a filter such as LFS) is killed before git is reaped, and reading its output is bounded
// by stopGrace: a stalled hook or filter cannot hold a jackstay command past its deadline. The group is
// signalled only while git is unreaped (its end is learnt without reaping it,
// supervise.AwaitExit), so its id cannot have been given to another process.
package gitcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/mask"
)

// stopGrace is how long git's group has between SIGTERM at the deadline and SIGKILL, and how long
// its output may still be read after git's end.
const stopGrace = 2 * time.Second

// ErrNotRepository is git saying the folder is not in a repository.
var ErrNotRepository = errors.New("not a git repository")

// ErrTimeout is git not answering before the context's deadline.
var ErrTimeout = errors.New("git did not answer in time")

// ExitError is git ending with a status other than 0.
type ExitError struct {
	Args   []string
	Code   int
	Stderr string
}

func (exitErr *ExitError) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s", mask.Text(strings.Join(exitErr.Args, " ")), exitErr.Code, exitErr.Diagnostic())
}

// Diagnostic is stderr fit to show to a user. Stderr remains available for classifying Git's reply.
func (exitErr *ExitError) Diagnostic() string {
	return mask.Text(strings.TrimSpace(exitErr.Stderr))
}

// Git runs git in Dir. Env is the environment git gets, as KEY=VALUE; nil means this process's
// environment without the variables that point at another repository (WithoutRepository), so a
// question is asked of Dir's repository. Git is the program, "git" when empty.
type Git struct {
	Dir string
	Env []string
	Git string
}

// Run runs `git ARGS…` and returns its standard output.
func (git Git) Run(ctx context.Context, args ...string) ([]byte, error) {
	return git.RunInput(ctx, nil, args...)
}

// RunInput runs `git ARGS…` with input on its standard input (nil: none) and returns its standard
// output.
func (git Git) RunInput(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	program := git.Git
	if program == "" {
		program = "git"
	}
	began := time.Now()
	defer func() { telemetry.NoteGit(time.Since(began)) }() // counted, not a span (observability.md, section 2)
	command := exec.Command(program, args...)               // #nosec G204 -- the arguments are jackstay's own git questions
	command.Dir = git.Dir
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = stopGrace
	env := git.Env
	if env == nil {
		env = WithoutRepository(os.Environ())
	}
	command.Env = append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	err := runInGroup(ctx, command)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("git %s: %w", mask.Text(strings.Join(args, " ")), ErrTimeout)
	}
	if err == nil || errors.Is(err, exec.ErrWaitDelay) {
		// ErrWaitDelay: git ended and something it left held its output open; what git wrote is read.
		return stdout.Bytes(), nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return nil, fmt.Errorf("git %s: %w", mask.Text(strings.Join(args, " ")), err)
	}
	if strings.Contains(stderr.String(), "not a git repository") {
		return nil, fmt.Errorf("git %s: %w", mask.Text(strings.Join(args, " ")), ErrNotRepository)
	}
	return nil, &ExitError{Args: args, Code: exitErr.ExitCode(), Stderr: stderr.String()}
}

// These are seams for tests: how git's end is learnt without reaping it, how the stop is armed at
// the context's end, and how a group is signalled.
var (
	awaitExit   = supervise.AwaitExit
	armStop     = context.AfterFunc
	signalGroup = syscall.Kill
)

// runInGroup starts command (its own group, WaitDelay set) and waits for it, stopping its group at
// the context's end. The stop is armed with context.AfterFunc: no goroutine runs unless the context
// ends.
func runInGroup(ctx context.Context, command *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	group := &groupStop{leader: command.Process.Pid}
	disarm := armStop(ctx, group.term)
	// Once git has ended (and is unreaped), or once its end cannot be learnt without reaping (the
	// kernel refused the watch), no stop starts, what a stop left is killed, and only then is git
	// reaped: nothing is signalled after the reap, when the group's id may be another's. Without
	// the watch, git that has not ended is waited for without its deadline.
	_ = awaitExit(group.leader)
	disarm()
	group.end()
	return command.Wait()
}

// groupStop stops git's process group: SIGTERM, then SIGKILL after stopGrace. Nothing is signalled
// once end has been called.
type groupStop struct {
	mutex  sync.Mutex
	leader int
	asked  bool
	over   bool
	kill   *time.Timer
}

// term asks the group to stop and arms its kill.
func (stop *groupStop) term() {
	stop.mutex.Lock()
	defer stop.mutex.Unlock()
	if stop.over {
		return
	}
	stop.asked = true
	_ = signalGroup(-stop.leader, syscall.SIGTERM)
	stop.kill = time.AfterFunc(stopGrace, stop.killNow)
}

// killNow kills the group when the grace is over.
func (stop *groupStop) killNow() {
	stop.mutex.Lock()
	defer stop.mutex.Unlock()
	if !stop.over {
		_ = signalGroup(-stop.leader, syscall.SIGKILL)
	}
}

// end is called once git has ended, before its reap: what a stop left of the group is killed, and
// nothing is signalled afterwards.
func (stop *groupStop) end() {
	stop.mutex.Lock()
	defer stop.mutex.Unlock()
	if stop.asked && !stop.over {
		_ = signalGroup(-stop.leader, syscall.SIGKILL)
	}
	stop.over = true
	if stop.kill != nil {
		stop.kill.Stop()
	}
}

// Line runs git and returns its output's first line, without the line end.
func (git Git) Line(ctx context.Context, args ...string) (string, error) {
	out, err := git.Run(ctx, args...)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return line, nil
}

// CommonDir is the repository's common folder, absolute: the main checkout's .git, also when Dir
// is a linked worktree.
func (git Git) CommonDir(ctx context.Context) (string, error) {
	line, err := git.Line(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	return strings.TrimSpace(line), err
}

// MainCheckout is the main checkout's top folder (the one that holds the common .git), whichever
// worktree Dir is in; an error when the repository has no main checkout (a bare one).
func (git Git) MainCheckout(ctx context.Context) (string, error) {
	common, err := git.CommonDir(ctx)
	if err != nil {
		return "", err
	}
	if filepath.Base(common) != ".git" {
		return "", fmt.Errorf("%s is not a checkout's .git folder (a bare repository has no main checkout)", common)
	}
	return filepath.Dir(common), nil
}

// SameFolderInMain is folder's place in the main checkout: the same path below the top, so a
// worktree's project folder and the main checkout's are one place. Outside a repository, or in
// one with no main checkout, it is folder itself.
func (git Git) SameFolderInMain(ctx context.Context, folder string) string {
	top, err := git.Line(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return folder
	}
	main, err := git.MainCheckout(ctx)
	if err != nil {
		return folder
	}
	below, err := filepath.Rel(resolved(top), resolved(folder))
	if err != nil || below == ".." || strings.HasPrefix(below, ".."+string(filepath.Separator)) {
		return folder
	}
	return filepath.Join(main, below)
}

// resolved is a path with its links resolved (itself when that fails): git prints real paths.
func resolved(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

// repositoryVariables point git at a repository other than the folder it runs in (git's own
// `rev-parse --local-env-vars`, less the configuration ones): a hook or an outer git sets them, and
// a question asked of a folder must be asked of that folder's repository. With them go the ones
// that change how git reads a pathspec or prints a diff, which jackstay states and parses itself.
var repositoryVariables = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_IMPLICIT_WORK_TREE": true, "GIT_COMMON_DIR": true,
	"GIT_INDEX_FILE": true, "GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_GRAFT_FILE": true, "GIT_SHALLOW_FILE": true, "GIT_PREFIX": true, "GIT_NO_REPLACE_OBJECTS": true,
	"GIT_REPLACE_REF_BASE": true, "GIT_NAMESPACE": true, "GIT_QUARANTINE_PATH": true, "GIT_CEILING_DIRECTORIES": true,
	// GIT_CONFIG names the file `git config` reads and writes: kept, it would point a question about
	// the repository's configuration (its remote, a clean copy's settings) at another file.
	"GIT_CONFIG":            true,
	"GIT_LITERAL_PATHSPECS": true, "GIT_GLOB_PATHSPECS": true, "GIT_NOGLOB_PATHSPECS": true, "GIT_ICASE_PATHSPECS": true,
	"GIT_EXTERNAL_DIFF": true, "GIT_DIFF_OPTS": true,
}

// WithoutRepository is env without the variables that point git at another repository. The rest
// stays: configuration (GIT_CONFIG_PARAMETERS, GIT_CONFIG_COUNT/KEY/VALUE, which carry a sandbox's
// proxy credentials and safe.directory) and transport (GIT_SSH_COMMAND, GIT_ASKPASS, GIT_SSL_*).
// Dropping every GIT_* made a fetch through the agent sandbox's proxy fail with "Proxy CONNECT
// aborted" (measured in Claude Code's sandbox, 2026-11-17).
func WithoutRepository(env []string) []string {
	out := make([]string, 0, len(env))
	for _, pair := range env {
		name, _, _ := strings.Cut(pair, "=")
		if !repositoryVariables[name] {
			out = append(out, pair)
		}
	}
	return out
}
