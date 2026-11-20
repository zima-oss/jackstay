package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/cli"
)

var update = flag.Bool("update", false, "write internal/cli/testdata/help.txt anew")

// runJst runs jackstay with the table cmd/jst builds and returns its exit, stdout and stderr.
func runJst(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := cli.Dispatch(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// helpNames are every command and every first word of two-word commands that is not a command.
func helpNames() []string {
	var names []string
	seen := map[string]bool{}
	for _, name := range cli.Names() {
		if first, _, two := strings.Cut(name, " "); two && !cli.Has(first) && !seen[first] {
			seen[first] = true
			names = append(names, first)
		}
		names = append(names, name)
	}
	return names
}

// TestEveryCommandHelp walks the table: each command answers --help with a usage line, and the
// whole output is the golden file.
func TestEveryCommandHelp(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "internal", "cli", "testdata", "help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	var golden strings.Builder
	for _, name := range helpNames() {
		code, stdout, stderr := runJst(append(strings.Fields(name), "--help")...)
		if code != 0 || stderr != "" {
			t.Errorf("jst %s --help: exit %d, stderr %q", name, code, stderr)
		}
		if cli.Has(name) && !hasUsageLine(stdout) {
			t.Errorf("jst %s --help has no line starting with usage:\n%s", name, stdout)
		}
		if cli.Has(name) && (linesStarting(stdout, "exit:") != 1 || linesStarting(stdout, "example:") != 1) {
			t.Errorf("jst %s --help needs one exit: line and one example: line (docs/STYLE.md rule 7):\n%s", name, stdout)
		}
		golden.WriteString("==> jst " + name + " --help\n" + stdout + "\n")
	}
	if *update {
		if err := os.WriteFile(path, []byte(golden.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) // #nosec G304 -- the golden file of this repository
	if err != nil {
		t.Fatalf("%v (go test ./cmd/jst -run TestEveryCommandHelp -update writes it)", err)
	}
	if string(want) != golden.String() {
		t.Errorf("the help differs from %s; run go test ./cmd/jst -run TestEveryCommandHelp -update and read the diff", path)
	}
}

// linesStarting counts the lines of text that start with prefix.
func linesStarting(text, prefix string) int {
	count := 0
	for line := range strings.Lines(text) {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

func hasUsageLine(text string) bool {
	for line := range strings.Lines(text) {
		if strings.HasPrefix(line, "usage:") {
			return true
		}
	}
	return false
}

// TestTrashHelpMovesNothing: `jst trash --help` once tried to trash a file named --help.
func TestTrashHelpMovesNothing(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	t.Chdir(dir)
	named := filepath.Join(dir, "--help")
	if err := os.WriteFile(named, []byte("kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runJst("trash", "--help")
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "jst trash: ") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(named); err != nil {
		t.Fatalf("the file named --help is gone: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(home, ".trash")); len(entries) > 0 {
		t.Fatalf("the trash holds %d entries", len(entries))
	}
}

// TestGroupHelpListsItsCommands: `jst changes --help` lists the changes commands; `jst changes`
// alone is still no command.
func TestGroupHelpListsItsCommands(t *testing.T) {
	code, stdout, _ := runJst("changes", "--help")
	if code != 0 || !strings.Contains(stdout, "jst changes new") || !strings.Contains(stdout, "jst changes check") {
		t.Fatalf("exit %d, stdout %q", code, stdout)
	}
	if code, _, stderr := runJst("changes"); code != 2 || !strings.Contains(stderr, "no command") {
		t.Fatalf("jst changes: exit %d, stderr %q", code, stderr)
	}
}

// TestEveryCommandHasAHeading: `jst help` lists every command under one of its headings, none under
// "Other".
func TestEveryCommandHasAHeading(t *testing.T) {
	for _, heading := range cli.Grouped() {
		if heading.Title == "Other" {
			t.Errorf("no heading of jst help names these commands' first word (internal/cli groups): %v", heading.Names)
		}
	}
}
