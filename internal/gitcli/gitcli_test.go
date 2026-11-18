package gitcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestAnswers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(dir, "elsewhere")) // taken out: the question is of Dir
	g := Git{Dir: dir}
	ctx := context.Background()
	if _, err := g.Run(ctx, "init", "-q", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	branch, err := g.Line(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil || branch != "main" {
		t.Fatalf("got %q, %v", branch, err)
	}
	_, err = g.Run(ctx, "rev-parse", "--verify", "--quiet", "no-such-ref")
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 1 {
		t.Fatalf("a missing ref: %v", err)
	}
}

func TestNotRepository(t *testing.T) {
	dir := t.TempDir()
	g := Git{Dir: dir, Env: append(WithoutRepository(os.Environ()), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))}
	if _, err := g.Run(context.Background(), "status"); !errors.Is(err, ErrNotRepository) {
		t.Fatalf("got %v", err)
	}
}

func TestTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	g := Git{Dir: t.TempDir(), Git: "sleep"} // a git that never answers in time
	if _, err := g.Run(ctx, "5"); !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v", err)
	}
}

// outerTimeout only ends a broken test; no outcome is decided by it (W-013).
const outerTimeout = 90 * time.Second

// waitFor looks until condition holds; the outer timeout only ends a broken test.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(outerTimeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("never happened: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// readPIDs waits for the ready file and reads the process ids written in it.
func readPIDs(t *testing.T, path string) []int {
	t.Helper()
	var pids []int
	waitFor(t, "the ready file "+path, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pids = pids[:0]
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err != nil {
				return false
			}
			pids = append(pids, pid)
		}
		return len(pids) > 0
	})
	for _, pid := range pids {
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	}
	return pids
}

// runUntilCancelled runs git in the background, cancels its context once ready names its
// processes, and returns those processes and the error.
func runUntilCancelled(t *testing.T, git Git, ready string, args ...string) ([]int, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := git.Run(ctx, args...)
		result <- err
	}()
	pids := readPIDs(t, ready)
	cancel()
	select {
	case err := <-result:
		return pids, err
	case <-time.After(outerTimeout):
		t.Fatal("git outlived its context")
	}
	return nil, nil
}

// alive says whether pid still names a process (a zombie does until it is reaped).
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestAStalledHookIsStoppedWithItsGroupAtTheDeadline(t *testing.T) {
	dir := t.TempDir()
	hooks := filepath.Join(t.TempDir(), "hooks")
	ready := filepath.Join(t.TempDir(), "ready")
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	// The hook ignores SIGTERM, as does the sleep it waits on: only the kill ends them.
	hook := "#!/bin/sh\ntrap '' TERM\nsleep 600 &\nchild=$!\necho \"$$ $child\" > \"$JACKSTAY_TEST_READY.tmp\"\n" +
		"mv \"$JACKSTAY_TEST_READY.tmp\" \"$JACKSTAY_TEST_READY\"\nwait\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(hook), 0o700); err != nil { // #nosec G306 -- a hook runs
		t.Fatal(err)
	}
	git := Git{Dir: dir, Env: append(WithoutRepository(os.Environ()), "JACKSTAY_TEST_READY="+ready)}
	if _, err := git.Run(context.Background(), "init", "-q", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	pids, err := runUntilCancelled(t, git, ready, "-c", "core.hooksPath="+hooks, "-c", "user.name=jackstay",
		"-c", "user.email=jackstay@example.invalid", "commit", "-q", "--allow-empty", "-m", "stalled")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v", err)
	}
	for _, pid := range pids {
		waitFor(t, "the hook's process "+strconv.Itoa(pid)+" is gone", func() bool { return !alive(pid) })
	}
}

func TestAGitThatIgnoresTermIsKilledAfterTheGrace(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	git := Git{Dir: t.TempDir(), Git: "sh"}
	pids, err := runUntilCancelled(t, git, ready, "-c", `trap '' TERM; echo $$ > "$0.tmp"; mv "$0.tmp" "$0"; sleep 600`, ready)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v", err)
	}
	waitFor(t, "the leader is gone", func() bool { return !alive(pids[0]) })
}

// When git's end cannot be learnt without reaping it (the watch fails), every stop is disarmed
// before the reap and nothing is signalled after it: the context ending at the last moment before
// the disarm (the fake arm fires the stop then) signals git's group while git is still unreaped.
func TestAFailedWatchSignalsNothingAfterTheReap(t *testing.T) {
	type sent struct {
		signal syscall.Signal
		reaped bool
	}
	var (
		mutex   sync.Mutex
		signals []sent
	)
	defer func(await func(int) error, arm func(context.Context, func()) func() bool,
		signal func(int, syscall.Signal) error) {
		awaitExit, armStop, signalGroup = await, arm, signal
	}(awaitExit, armStop, signalGroup)
	awaitExit = func(int) error { return errors.New("the kernel refused the watch") }
	armStop = func(_ context.Context, stop func()) func() bool {
		return func() bool {
			stop() // the context ended just before the disarm
			return false
		}
	}
	signalGroup = func(group int, signal syscall.Signal) error {
		// The leader is unreaped (running or a zombie) while signal 0 reaches it.
		reaped := errors.Is(syscall.Kill(-group, 0), syscall.ESRCH)
		mutex.Lock()
		signals = append(signals, sent{signal: signal, reaped: reaped})
		mutex.Unlock()
		if reaped {
			return syscall.ESRCH // never signal an id that may be another's
		}
		return syscall.Kill(group, signal)
	}
	g := Git{Dir: t.TempDir(), Git: "/bin/sh"}
	_, _ = g.Run(context.Background(), "-c", "exit 0")
	mutex.Lock()
	defer mutex.Unlock()
	if len(signals) == 0 {
		t.Fatal("the stop was never fired: the case was not exercised")
	}
	for _, each := range signals {
		if each.reaped {
			t.Fatalf("%v sent after git was reaped (signals %v)", each.signal, signals)
		}
	}
}

// Only the variables that point at another repository (or change how a pathspec is read or a diff
// printed) are taken out; configuration and transport reach git: the agent sandbox's proxy
// credentials come in GIT_CONFIG_PARAMETERS (measured in Claude Code's sandbox).
func TestWithoutRepositoryKeepsConfigurationAndTransport(t *testing.T) {
	kept := WithoutRepository([]string{"GIT_DIR=/x", "GIT_WORK_TREE=/y", "GIT_INDEX_FILE=/z", "GIT_LITERAL_PATHSPECS=1", "GIT_CONFIG=/c", "GIT_CEILING_DIRECTORIES=/d",
		"GIT_CONFIG_PARAMETERS='http.proxyAuthMethod=basic'", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory",
		"GIT_CONFIG_VALUE_0=/w", "GIT_SSH_COMMAND=ssh -o X", "GIT_ASKPASS=/a", "HTTPS_PROXY=http://p", "HOME=/h"})
	want := []string{"GIT_CONFIG_PARAMETERS='http.proxyAuthMethod=basic'", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory",
		"GIT_CONFIG_VALUE_0=/w", "GIT_SSH_COMMAND=ssh -o X", "GIT_ASKPASS=/a", "HTTPS_PROXY=http://p", "HOME=/h"}
	if strings.Join(kept, "\n") != strings.Join(want, "\n") {
		t.Fatalf("kept %q", kept)
	}
}

// Through gitcli, configuration given in the environment reaches git; GIT_DIR and GIT_CONFIG (a
// file of another repository's configuration) still do not.
func TestConfigurationInTheEnvironmentReachesGit(t *testing.T) {
	dir := t.TempDir()
	g := Git{Dir: dir}
	ctx := context.Background()
	if _, err := g.Run(ctx, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere"))
	t.Setenv("GIT_CONFIG_PARAMETERS", "'jackstay.probe=from-the-environment'")
	foreign := filepath.Join(t.TempDir(), "foreign.config")
	if err := os.WriteFile(foreign, []byte("[jackstay]\n\tforeign = yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG", foreign)
	got, err := g.Line(ctx, "config", "--get", "jackstay.probe")
	if err != nil || got != "from-the-environment" {
		t.Fatalf("got %q, %v", got, err)
	}
	if foreignValue, err := g.Line(ctx, "config", "--get", "jackstay.foreign"); err == nil {
		t.Fatalf("git read the file GIT_CONFIG names: %q", foreignValue)
	}
}
