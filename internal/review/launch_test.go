package review

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every child process review starts, waits for or signals goes through internal/supervise
// (codex.GroupSpec): git's and Codex's launchers held a call past its deadline whenever one path
// bypassed it. No file of review starts, waits for or
// signals a process itself. A line that waits on something that is not a process (a
// sync.WaitGroup) says so with `jackstay:not-a-process`.
var forbiddenLaunch = regexp.MustCompile(`exec\.CommandContext|\.Output\(\)|\.CombinedOutput\(\)|\.Run\(\)|\.Start\(\)|\.Wait\(\)|` +
	`exec\.Command\(|Process\.(Wait|Kill|Signal|Release)|os\.StartProcess|os\.FindProcess|syscall\.(ForkExec|Kill|Wait4)|unix\.(Kill|Wait4)|gitcli\.Git\{`)

func launchViolations(source string) []string {
	var found []string
	for _, line := range strings.Split(source, "\n") {
		if forbiddenLaunch.MatchString(line) && !strings.Contains(line, "jackstay:not-a-process") {
			found = append(found, strings.TrimSpace(line))
		}
	}
	return found
}

func TestLaunchViolationsAreSeen(t *testing.T) {
	for _, bad := range []string{"command.Start()", "err := command.Wait()", "command.Process.Wait()", "command.Process.Kill()",
		"exec.CommandContext(ctx, \"git\")", "command := exec.Command(\"git\", args...)", "out, err := command.Output()", "syscall.Kill(-pid, 9)", "gitcli.Git{Dir: root}.Run(ctx)"} {
		if len(launchViolations(bad)) != 1 {
			t.Errorf("not seen: %s", bad)
		}
	}
	for _, good := range []string{"outcome, err := supervise.Run(nil, spec)", "exec.LookPath(request.Command)",
		"waiting.Wait() // jackstay:not-a-process", "codex.GroupSpec(argv)"} {
		if len(launchViolations(good)) != 0 {
			t.Errorf("taken for a launch: %s", good)
		}
	}
}

func TestEveryChildProcessGoesThroughSupervise(t *testing.T) {
	var found []string
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range launchViolations(string(data)) {
			found = append(found, path+": "+line)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("a process started, waited for or signalled outside internal/supervise:\n%s", strings.Join(found, "\n"))
	}
}
