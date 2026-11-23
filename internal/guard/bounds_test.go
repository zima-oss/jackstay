package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Each bound of guard.md section 3 with a test that reaches it.

// testBudget is every wall-clock budget the tests give real work: far above any machine's time, so
// only a budget's own test below reaches it.
const testBudget = time.Hour

// testGit is the real git with testBudget for each question and for a call.
func testGit() *Git {
	return NewGitWithBudget(nil, testBudget, testBudget)
}

func plainRules(command string) []string {
	return Decide(command, Settings{Folder: "/Users/someone/project", Environ: map[string]string{"TMPDIR": "/var/folders/ab/cdef/T/"},
		Scratch: []string{"/tmp"}, Home: "/Users/someone", Git: corpusGit{Git: testGit()}, Deadline: testBudget}).Rules()
}

func TestTheCommandTextBound(t *testing.T) {
	if got := plainRules(strings.Repeat("x", maxCommandBytes+1)); !slices.Equal(got, []string{ruleTooLong}) {
		t.Fatalf("over 1 MiB: %v", got)
	}
}

func TestTheStepsBound(t *testing.T) {
	line := strings.Repeat("true\n", maxSteps+1)
	if got := plainRules(line); !slices.Equal(got, []string{ruleTooLong}) {
		t.Fatalf("over %d commands: %v", maxSteps, got)
	}
	if got := plainRules(strings.Repeat("true\n", maxSteps/2)); len(got) > 0 {
		t.Fatalf("under the bound: %v", got)
	}
}

func TestTheNestingBound(t *testing.T) {
	line := "git status"
	for range maxDepth + 1 {
		line = "bash -c " + "'" + strings.ReplaceAll(line, "'", `'\''`) + "'"
	}
	if got := plainRules(line); !slices.Equal(got, []string{ruleTooDeep}) {
		t.Fatalf("nested %d: %v", maxDepth+1, got)
	}
	line = "git status"
	for range maxDepth - 1 {
		line = "bash -c " + "'" + strings.ReplaceAll(line, "'", `'\''`) + "'"
	}
	if got := plainRules(line); len(got) > 0 {
		t.Fatalf("nested %d: %v", maxDepth-1, got)
	}
}

func TestTheReadingsBound(t *testing.T) {
	// Each D holds one of two texts: 2^6 = 64 readings are followed, 2^7 are not.
	build := func(count int) string {
		var parts []string
		for index := range count {
			parts = append(parts, fmt.Sprintf("if x%d; then D%d=/tmp/a; else D%d=/tmp/b; fi", index, index, index))
		}
		var names strings.Builder
		for index := range count {
			fmt.Fprintf(&names, "$D%d", index)
		}
		return strings.Join(parts, "; ") + "; rm -rf " + names.String()
	}
	if got := plainRules(build(6)); len(got) > 0 {
		t.Fatalf("64 readings: %v", got)
	}
	if got := plainRules(build(7)); !slices.Equal(got, []string{"rm-recursive"}) {
		t.Fatalf("128 readings: %v", got)
	}
}

func TestTheValueBound(t *testing.T) {
	long := strings.Repeat("a", maxValueBytes+1)
	if got := plainRules("D=/tmp/" + long + "; rm -rf \"$D\""); !slices.Equal(got, []string{"rm-recursive"}) {
		t.Fatalf("a value over the bound: %v", got)
	}
	if got := plainRules("D=/tmp/aaa; rm -rf \"$D\""); len(got) > 0 {
		t.Fatalf("a short value: %v", got)
	}
}

func TestThePlacesBound(t *testing.T) {
	var parts []string
	for index := range maxPlaces + 1 {
		parts = append(parts, fmt.Sprintf("cd /tmp/p%d", index))
	}
	line := strings.Join(parts, "; ") + "; rm -rf .build/x"
	if got := plainRules(line); !slices.Equal(got, []string{"rm-recursive"}) {
		t.Fatalf("more places than followed: %v", got)
	}
	if got := plainRules("cd /tmp/a; cd /tmp/b; rm -rf .build/x"); len(got) > 0 {
		t.Fatalf("a few places: %v", got)
	}
}

func TestTheLoopPassesBound(t *testing.T) {
	// A variable that grows each round never settles: after the passes it is unknown.
	if got := plainRules("D=/tmp/x; while true; do D=$D/a; done; rm -rf \"$D\""); !slices.Equal(got, []string{"rm-recursive"}) {
		t.Fatalf("a loop that never settles: %v", got)
	}
}

func TestTheReadingDeadline(t *testing.T) {
	// The rule is the check: a 1 ns budget is spent before the reading ends, however fast the machine.
	settings := Settings{Folder: "/", Home: "/", Git: corpusGit{Git: testGit()}, Deadline: time.Nanosecond}
	if got := Decide(strings.Repeat("true\n", 2000), settings).Rules(); !slices.Equal(got, []string{ruleTooLong}) {
		t.Fatalf("past the deadline: %v", got)
	}
}

func TestTheGitQuestionBudget(t *testing.T) {
	// A git that never answers: the question's budget, not git, ends it, and the answer is unsure.
	fake := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 600\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	git := NewGitWithBudget(nil, time.Nanosecond, testBudget)
	git.program = fake
	if got := git.Held(realFolder(t), "main"); got != answerUnsure {
		t.Fatalf("a stalled git answered %v", got)
	}
}

func TestTheGitCallBudget(t *testing.T) {
	// A call's budget spent, no question is asked of git at all.
	git := NewGitWithBudget(nil, testBudget, time.Nanosecond)
	git.spent = time.Nanosecond
	if got := git.BranchExists(realFolder(t), "main"); got != answerUnsure {
		t.Fatalf("past the call's budget git answered %v", got)
	}
}

func TestTheHookAnswersWhenJackstayJSONStalls(t *testing.T) {
	folder := realFolder(t)
	if err := syscall.Mkfifo(filepath.Join(folder, "jackstay.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A short answer budget: the stalled read never ends, so the budget is what answers.
	within := generousBudgets
	within.answer = 50 * time.Millisecond
	out, _ := runHookWithin(t, within, t.TempDir(), bashCall("git status", folder), "")
	if message, _ := out["systemMessage"].(string); !strings.Contains(message, "did NOT check") {
		t.Fatalf("answer %v", out)
	}
	_ = os.Remove(filepath.Join(folder, "jackstay.json"))
}
