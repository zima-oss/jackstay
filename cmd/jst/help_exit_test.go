package main

import (
	"strings"
	"testing"
)

// TestEveryCommandSaysItsExits: every command's --help names its exit statuses on a line that
// starts with `exit:`, so an agent can tell a refusal from a failure without reading the code.
func TestEveryCommandSaysItsExits(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	for _, name := range helpNames() {
		if helpWithoutExitYet[name] {
			continue
		}
		_, stdout, _ := runJst(append(strings.Fields(name), "--help")...)
		if !hasExitLine(stdout) {
			t.Errorf("jst %s --help has no line starting with exit:", name)
		}
	}
}

// helpWithoutExitYet are the commands whose help is changed by another branch in flight: their
// exit line lands there. The test fails once such a help has its line, so an entry cannot outlive
// it. None is in flight now.
var helpWithoutExitYet = map[string]bool{}

// TestTheExceptionsAreStillNeeded keeps helpWithoutExitYet honest.
func TestTheExceptionsAreStillNeeded(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	for name := range helpWithoutExitYet {
		_, stdout, _ := runJst(append(strings.Fields(name), "--help")...)
		if hasExitLine(stdout) {
			t.Errorf("jst %s --help has its exit line now: remove it from helpWithoutExitYet", name)
		}
	}
}

func hasExitLine(text string) bool {
	for line := range strings.Lines(text) {
		if strings.HasPrefix(line, "exit:") {
			return true
		}
	}
	return false
}
