//go:build integration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pathWithout is PATH without the folders that hold any of the named programs: what a machine
// without them sees.
func pathWithout(programs ...string) string {
	var kept []string
	for _, folder := range filepath.SplitList(os.Getenv("PATH")) {
		holds := false
		for _, program := range programs {
			if info, err := os.Stat(filepath.Join(folder, program)); err == nil && !info.IsDir() {
				holds = true
			}
		}
		if !holds {
			kept = append(kept, folder)
		}
	}
	return strings.Join(kept, string(os.PathListSeparator))
}

// TestTheCoreNeedsNeitherClaudeCodeNorCodex runs the built jst in a fresh repository with no
// .claude folder and no claude or codex on PATH: running a recorded test, adopting the project and
// checking it all work. A review needs an explicit selection or project policy and only checks
// for the selected provider (W-059: both are optional integrations).
func TestTheCoreNeedsNeitherClaudeCodeNorCodex(t *testing.T) {
	base := t.TempDir()
	binary := filepath.Join(base, "jst")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	path := pathWithout("claude", "codex")
	if _, err := lookIn(path, "git"); err != nil {
		t.Skip("no git left on PATH once the folders holding claude or codex are left out")
	}
	for _, program := range []string{"claude", "codex"} {
		if found, err := lookIn(path, program); err == nil {
			t.Fatalf("%s is still on the trimmed PATH: %s", program, found)
		}
	}
	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "project")
	for _, folder := range []string{home, repo} {
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{
		"PATH=" + path, "HOME=" + home, "JACKSTAY_HOME=" + filepath.Join(home, ".jackstay"),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "TMPDIR=" + os.TempDir(),
		"CODEX_HOME=" + filepath.Join(home, ".codex"),
	}
	run := func(want int, args ...string) string {
		t.Helper()
		var command *exec.Cmd
		if args[0] == "git" {
			command = exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args[1:]...)...)
		} else {
			command = exec.Command(binary, args...)
		}
		command.Dir, command.Env = repo, environment
		out, err := command.CombinedOutput()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if code != want {
			t.Fatalf("%v: exit %d, want %d\n%s", args, code, want, out)
		}
		return string(out)
	}
	run(0, "git", "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("a project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(0, "git", "add", "README.md")
	run(0, "git", "commit", "-q", "-m", "start")

	// Before any adoption: a recorded run of any command.
	if out := run(0, "test", "--project", "standalone", "--", "sh", "-c", "echo hello"); !strings.Contains(out, "hello") {
		t.Fatalf("the command's output is not shown:\n%s", out)
	}
	run(3, "adopt", "--plan")
	// A new project at 0.6.0 or later gets a `land` section; who merges is a person's choice, given
	// here as there is no terminal to ask at.
	run(0, "adopt", "--apply", "--autonomy", "prepare")
	run(0, "adopt", "--plan")
	run(0, "check")
	// Without the Claude Code hook the project still works: the hook is an integration.
	if err := os.RemoveAll(filepath.Join(repo, ".claude")); err != nil {
		t.Fatal(err)
	}
	run(0, "check")
	run(0, "test", "--", "sh", "-c", "echo again")
	if out := run(6, "review", "HEAD"); !strings.Contains(out, "Codex is not installed") {
		t.Fatalf("the default reviewer is not Codex:\n%s", out)
	}
	if out := run(2, "review", "HEAD", "--reviewer", ""); !strings.Contains(out, "--reviewer names no reviewer") {
		t.Fatalf("an empty --reviewer was not refused:\n%s", out)
	}
	for _, each := range []struct{ name, program string }{{"codex", "Codex"}, {"claude-code", "Claude Code"}} {
		out := run(6, "review", "HEAD", "--reviewer", each.name)
		if !strings.Contains(out, each.program+" is not installed") {
			t.Fatalf("the refusal does not identify the selected reviewer %s:\n%s", each.name, out)
		}
	}
}

// lookIn finds a program on the given PATH.
func lookIn(path, program string) (string, error) {
	for _, folder := range filepath.SplitList(path) {
		candidate := filepath.Join(folder, program)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}
