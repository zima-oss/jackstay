package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// registerForTest adds commands whose Run fails the test, and removes them when it ends.
func registerForTest(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		Register(Command{Name: name, Summary: "the summary of " + name, Usage: "usage: jst " + name + "\n\nexample: jst " + name + "\n",
			Run: func([]string, io.Reader, io.Writer, io.Writer) int {
				t.Errorf("%s ran", name)
				return 0
			}})
	}
	t.Cleanup(func() {
		for _, name := range names {
			delete(commands, name)
		}
	})
}

// call runs Dispatch with buffers and returns the exit status, stdout and stderr.
func call(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Dispatch(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestHas(t *testing.T) {
	registerForTest(t, "fake sub")
	if !Has("fake sub") || !Has("version") {
		t.Fatal("a registered command is not found")
	}
	if Has("fake") || Has("no-such-command") {
		t.Fatal("Has found a command that is not registered")
	}
}

func TestHelpRunsNothing(t *testing.T) {
	registerForTest(t, "fake", "fake sub")
	for _, args := range [][]string{{"fake", "--help"}, {"fake", "-h"}, {"help", "fake"}} {
		code, stdout, stderr := call(args...)
		if code != 0 || stderr != "" {
			t.Fatalf("%v: %d %q", args, code, stderr)
		}
		if !strings.HasPrefix(stdout, "jst fake: the summary of fake\n\nusage: jst fake\n") ||
			!strings.Contains(stdout, "jst fake sub  the summary of fake sub") {
			t.Fatalf("%v: %q", args, stdout)
		}
	}
	code, flagged, _ := call("fake", "sub", "--help")
	_, asked, _ := call("help", "fake", "sub")
	if code != 0 || flagged != asked || !strings.HasPrefix(flagged, "jst fake sub: the summary of fake sub\n") {
		t.Fatalf("jst fake sub --help %d %q, jst help fake sub %q", code, flagged, asked)
	}
}

func TestHelpIsOnlyTheOnlyWord(t *testing.T) {
	ran := []string{}
	Register(Command{Name: "fake", Usage: "usage: jst fake\n", Run: func(args []string, _ io.Reader, _, _ io.Writer) int {
		ran = append(ran, strings.Join(args, " "))
		return 0
	}})
	defer delete(commands, "fake")
	call("fake", "--help", "x")
	call("fake", "--", "--help")
	if strings.Join(ran, "|") != "--help x|-- --help" {
		t.Fatalf("ran with %q", ran)
	}
}

func TestGroupHelp(t *testing.T) {
	registerForTest(t, "group first", "group second")
	for _, args := range [][]string{{"group", "--help"}, {"group", "-h"}, {"help", "group"}} {
		code, stdout, stderr := call(args...)
		want := "jst group: these commands (each answers --help)\n\n" +
			"  jst group first   the summary of group first\n" +
			"  jst group second  the summary of group second\n\n" +
			"exit: 0 this list; 2 `jst group` alone, or with a word that is no command under it (nothing\n" +
			"is run); each command's --help names its own.\n"
		if code != 0 || stdout != want || stderr != "" {
			t.Fatalf("%v: %d %q %q", args, code, stdout, stderr)
		}
	}
	if code, _, stderr := call("group"); code != 2 || !strings.Contains(stderr, `no command "group"`) {
		t.Fatalf("the first word alone: %d %q", code, stderr)
	}
}

func TestHelpOfNothing(t *testing.T) {
	for _, args := range [][]string{{"help", "no-such-command"}, {"help", "version", "extra"}, {"no-such-command", "--help"}} {
		if code, stdout, stderr := call(args...); code != 2 || stdout != "" || !strings.Contains(stderr, "no command") {
			t.Fatalf("%v: %d %q %q", args, code, stdout, stderr)
		}
	}
	if code, stdout, _ := call("--help"); code != 0 || !strings.HasPrefix(stdout, "usage: jst COMMAND") {
		t.Fatalf("jst --help: %d %q", code, stdout)
	}
}
