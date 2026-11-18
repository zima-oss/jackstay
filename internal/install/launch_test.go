package install

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The install and doctor packages start, wait for and signal processes only through internal/supervise
// (supervise.Capture) or gitcli (docs/design/implementation.md section 1), as internal/review does.
var part5Packages = []string{"../install", "../layout", "../prove", "../expected", "../doctor"}

var forbiddenLaunch = regexp.MustCompile(`exec\.CommandContext|exec\.Command\(|\.Output\(\)|\.CombinedOutput\(\)|\.Start\(\)|` +
	`Process\.(Wait|Kill|Signal|Release)|os\.StartProcess|os\.FindProcess|syscall\.(ForkExec|Kill|Wait4)|unix\.(Kill|Wait4)`)

func TestEveryChildProcessGoesThroughSupervise(t *testing.T) {
	for _, bad := range []string{`exec.Command("git")`, "cmd.Output()", "syscall.Kill(-1, 9)", "command.Start()"} {
		if !forbiddenLaunch.MatchString(bad) {
			t.Fatalf("not seen: %s", bad)
		}
	}
	var found []string
	for _, folder := range part5Packages {
		entries, err := os.ReadDir(folder)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(folder, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(string(data), "\n") {
				if forbiddenLaunch.MatchString(line) {
					found = append(found, filepath.Join(folder, entry.Name())+": "+strings.TrimSpace(line))
				}
			}
		}
	}
	if len(found) > 0 {
		t.Fatalf("processes started outside supervise or gitcli:\n%s", strings.Join(found, "\n"))
	}
}
