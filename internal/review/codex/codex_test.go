package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/procs"
)

// fakeCodex is a shell script standing in for Codex: it records its argv, environment and
// standard input (read to its end), then does what MODE in its folder says.
const fakeCodex = `#!/bin/sh
dir=$(dirname "$0")
printf '%s\n' "$@" > "$dir/argv"
env > "$dir/env"
cat > "$dir/stdin"
answer=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then answer=$2; fi
  shift
done
mode=$(cat "$dir/mode")
case "$mode" in
  done)
    echo '{"type":"thread.started"}'
    echo '{"type":"item.completed","item":{"type":"agent_message","text":"401 refresh token in the reviewed code"}}'
    echo '{"verdict":"approve","summary":"","findings":[],"prior":[]}' > "$answer"
    echo '{"type":"turn.completed"}'
    ;;
  silent) echo '{"type":"turn.completed"}' ;;
  login)
    echo '{"type":"error","message":"unexpected status 401 Unauthorized: refresh token was already used"}'
    exit 1 ;;
  itemlogin)
    echo '{"type":"item.completed","item":{"type":"agent_message","text":"refresh token 401"}}'
    exit 1 ;;
  slow)
    sleep 3000 &
    echo "$$ $!" > "$dir/pids.tmp"; mv "$dir/pids.tmp" "$dir/pids"
    wait ;;
  leftover)
    sleep 3000 &
    echo "$!" > "$dir/pids.tmp"; mv "$dir/pids.tmp" "$dir/pids"
    echo '{"verdict":"approve","summary":"","findings":[],"prior":[]}' > "$answer"
    echo '{"type":"turn.completed"}'
    ;;
  version) echo "codex-cli 9.9.9" ;;
  versionhang) echo "codex-cli 9.9.9"; sleep 30 & ;;
esac
`

func setup(t *testing.T, mode string) Request {
	t.Helper()
	dir := t.TempDir()
	program := filepath.Join(dir, "codex")
	if err := os.WriteFile(program, []byte(fakeCodex), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	return Request{Command: program, Effort: "medium", Timeout: 10 * time.Second, Isolation: []string{"--ephemeral"},
		Tree: tree, Folder: filepath.Join(dir, "run"), Prompt: []byte("the prompt\n"), SchemaPath: filepath.Join(dir, "schema.json"),
		Environ: []string{"PATH=/usr/bin:/bin", "HOME=/home/x", "CLAUDE_SESSION=1", "JACKSTAY_HOME=/w", "OPENAI_API_KEY=k", "GIT_DIR=/g"}}
}

func read(t *testing.T, request Request, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(request.Command), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRunDone(t *testing.T) {
	request := setup(t, "done")
	answered := 0
	request.Answered = func() { answered++ }
	result := Run(context.Background(), request)
	if result.Outcome != OutcomeDone || result.Exit != 0 {
		t.Fatalf("%+v", result)
	}
	if answered != 1 {
		t.Fatalf("Answered called %d times", answered)
	}
	if read(t, request, "stdin") != "the prompt\n" {
		t.Fatal("the prompt did not arrive on stdin")
	}
	argv := strings.Split(strings.TrimSpace(read(t, request, "argv")), "\n")
	for _, want := range [][]string{{"-a", "never"}, {"--sandbox", "read-only"}, {"-C", request.Tree}, {"--output-schema", request.SchemaPath}} {
		position := slices.Index(argv, want[0])
		if position < 0 || argv[position+1] != want[1] {
			t.Errorf("argv lacks %v: %v", want, argv)
		}
	}
	if argv[len(argv)-1] != "-" || !slices.Contains(argv, "--json") || !slices.Contains(argv, `model_reasoning_effort="medium"`) {
		t.Fatalf("argv: %v", argv)
	}
	env := read(t, request, "env")
	for _, gone := range []string{"CLAUDE_SESSION", "JACKSTAY_HOME", "OPENAI_API_KEY", "GIT_DIR"} {
		if strings.Contains(env, gone+"=") {
			t.Errorf("%s reached Codex", gone)
		}
	}
	if !strings.Contains(env, "HOME=/home/x") {
		t.Error("HOME did not reach Codex")
	}
	events, _ := os.ReadFile(filepath.Join(request.Folder, "events.jsonl"))
	if !strings.Contains(string(events), "turn.completed") {
		t.Fatal("events not kept")
	}
}

func TestRunOutcomes(t *testing.T) {
	cases := map[string]string{"silent": OutcomeNoAnswer, "login": OutcomeLogin, "itemlogin": OutcomeFailed}
	for mode, want := range cases {
		result := Run(context.Background(), setup(t, mode))
		if result.Outcome != want {
			t.Errorf("%s: %s (%s)", mode, result.Outcome, result.Reason)
		}
	}
}

// outerGuard only ends a broken test; no outcome is decided by it (W-013).
const outerGuard = 60 * time.Second

// pidsOf waits for the fake Codex's acknowledgement: the pids it wrote (itself and what it started).
func pidsOf(t *testing.T, request Request) []int {
	t.Helper()
	place := filepath.Join(filepath.Dir(request.Command), "pids")
	for deadline := time.Now().Add(outerGuard); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if data, err := os.ReadFile(place); err == nil {
			var pids []int
			for _, field := range strings.Fields(string(data)) {
				pid, err := strconv.Atoi(field)
				if err != nil {
					t.Fatalf("pids %q", data)
				}
				pids = append(pids, pid)
			}
			return pids
		}
	}
	t.Fatalf("the fake Codex never acknowledged within %s", outerGuard)
	return nil
}

// expectGone waits for each process to be gone (a zombie no one reaped yet counts); the guard
// only ends a broken test.
func expectGone(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		deadline := time.Now().Add(outerGuard)
		for !gone(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d is still running", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func gone(pid int) bool {
	row, found, err := procs.Row(pid)
	if err != nil {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	}
	return !found || row.Zombie
}

// The deadline stops Codex's whole group: Codex never ends by itself, so the outcome is the
// timeout whatever the machine's speed, and what it started is gone afterwards.
func TestRunDeadlineStopsTheGroup(t *testing.T) {
	request := setup(t, "slow")
	request.Timeout = 300 * time.Millisecond
	result := Run(context.Background(), request)
	if result.Outcome != OutcomeTimeout {
		t.Fatalf("%+v", result)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(request.Command), "pids")); err == nil {
		expectGone(t, pidsOf(t, request)) // the deadline may come before Codex wrote them
	}
}

// What Codex left in its group holding its output is ended: the run is done and the leftover gone.
func TestLeftoverInTheGroupIsEnded(t *testing.T) {
	request := setup(t, "leftover")
	result := Run(context.Background(), request)
	if result.Outcome != OutcomeDone {
		t.Fatalf("%+v", result)
	}
	expectGone(t, pidsOf(t, request))
}

// A stop asked once Codex is ready (its acknowledgement) ends the run as failed, its group gone.
func TestRunStopsWhenAsked(t *testing.T) {
	request := setup(t, "slow")
	request.Timeout = time.Hour // never the trigger: the test's own stop is
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan Result, 1)
	go func() { results <- Run(ctx, request) }()
	pids := pidsOf(t, request)
	cancel()
	select {
	case result := <-results:
		if result.Outcome != OutcomeFailed || !strings.Contains(result.Reason, "asked to stop") {
			t.Fatalf("%+v", result)
		}
	case <-time.After(outerGuard):
		t.Fatalf("the run did not end within %s of the stop", outerGuard)
	}
	expectGone(t, pids)
}

func TestMissingProgram(t *testing.T) {
	request := setup(t, "done")
	request.Command = filepath.Join(t.TempDir(), "nothing")
	if result := Run(context.Background(), request); result.Outcome != OutcomeRefused {
		t.Fatalf("%+v", result)
	}
	if _, err := Probe(context.Background(), request.Command); err == nil {
		t.Fatal("probe of a missing program")
	}
}

func TestProbe(t *testing.T) {
	request := setup(t, "version")
	t.Setenv("CLAUDE_SESSION_PROBE", "1")
	version, err := Probe(context.Background(), request.Command)
	if err != nil || version != "codex-cli 9.9.9" {
		t.Fatal(version, err)
	}
	if strings.Contains(read(t, request, "env"), "CLAUDE_SESSION_PROBE") {
		t.Fatal("the probe inherited the session's environment")
	}
}

func TestProbeNotHeldByADescendant(t *testing.T) {
	request := setup(t, "versionhang")
	started := time.Now()
	version, err := Probe(context.Background(), request.Command)
	if err != nil || version != "codex-cli 9.9.9" {
		t.Fatal(version, err)
	}
	if time.Since(started) > 8*time.Second {
		t.Fatalf("the probe was held %s", time.Since(started))
	}
}

func TestCheckEnvironment(t *testing.T) {
	home := t.TempDir()
	marker := filepath.Join(t.TempDir(), "login-failed")
	if reason := CheckEnvironment(home, marker); !strings.Contains(reason, "codex login") {
		t.Fatal(reason)
	}
	auth := filepath.Join(home, "auth.json")
	if err := os.WriteFile(auth, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason := CheckEnvironment(home, marker); reason != "" {
		t.Fatal(reason)
	}
	if err := os.Chmod(auth, 0o400); err != nil {
		t.Fatal(err)
	}
	if reason := CheckEnvironment(home, marker); !strings.Contains(reason, "outside the sandbox") {
		t.Fatal(reason)
	}
	if err := os.Chmod(auth, 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(auth, past, past); err != nil {
		t.Fatal(err)
	}
	RecordLoginFailure(marker, "401")
	if reason := CheckEnvironment(home, marker); !strings.Contains(reason, "not renewed") {
		t.Fatal(reason)
	}
	if err := os.Chtimes(auth, time.Now().Add(time.Minute), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if reason := CheckEnvironment(home, marker); reason != "" {
		t.Fatalf("a renewed login lifts the marker: %s", reason)
	}
}

func TestEnvironmentAndLoginWords(t *testing.T) {
	kept := Environment([]string{"PATH=/bin", "CODEX_HOME=/c", "TERM=x", "LC_ALL=C"})
	if strings.Join(kept, " ") != "PATH=/bin CODEX_HOME=/c LC_ALL=C" {
		t.Fatal(kept)
	}
	if !IsLoginFailure("Failed to refresh token: 401") || IsLoginFailure("stream disconnected; reconnecting") {
		t.Fatal("login words")
	}
	if codexLogLines("2026 ERROR codex_core: x\nplain line\n") != "2026 ERROR codex_core: x" {
		t.Fatal("log lines")
	}
}
