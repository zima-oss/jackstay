package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestFind(t *testing.T) {
	Register(Command{Name: "x y", Run: func([]string, io.Reader, io.Writer, io.Writer) int { return 0 }})
	defer delete(commands, "x y")
	if c, n, ok := Find([]string{"x", "y", "z"}); !ok || c.Name != "x y" || n != 2 {
		t.Fatalf("got %q %d %v", c.Name, n, ok)
	}
	if c, n, ok := Find([]string{"version"}); !ok || c.Name != "version" || n != 1 {
		t.Fatalf("got %q %d %v", c.Name, n, ok)
	}
	if _, _, ok := Find([]string{"x"}); ok {
		t.Fatal("a first word alone found a two-word command")
	}
}

// child runs Main in a fresh process of this test binary and returns its exit, stdout and stderr.
func child(t *testing.T, env []string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestChild$") // #nosec G204 -- this test's own binary
	cmd.Env = append(append(os.Environ(), "JACKSTAY_CLI_CHILD="+strings.Join(args, "\x1f")), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

func TestChild(t *testing.T) {
	words, ok := os.LookupEnv("JACKSTAY_CLI_CHILD")
	if !ok {
		t.Skip("run by the tests below")
	}
	var args []string
	if words != "" {
		args = strings.Split(words, "\x1f")
	}
	os.Exit(Main("jst", args))
}

func TestMain_(t *testing.T) {
	if code, _, stderr := child(t, nil, "no-such-command"); code != 2 || !strings.Contains(stderr, "no command") {
		t.Fatalf("an unknown command: %d %q", code, stderr)
	}
	if code, _, stderr := child(t, nil); code != 2 || !strings.Contains(stderr, "usage: jst") {
		t.Fatalf("no command: %d %q", code, stderr)
	}
	if code, stdout, _ := child(t, nil, "help"); code != 0 || !strings.Contains(stdout, "jst version") {
		t.Fatalf("help: %d %q", code, stdout)
	}
	for _, word := range []string{"version", "--version", "-V"} {
		if code, stdout, _ := child(t, nil, word); code != 0 || !strings.HasPrefix(stdout, "jackstay ") {
			t.Fatalf("%s: %d %q", word, code, stdout)
		}
	}
	if code, _, _ := child(t, nil, "--version", "extra"); code != 2 {
		t.Fatalf("--version with a word after it: %d", code)
	}
}

func TestHelpFlags(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		if entry, _, found := Find([]string{"help"}); !found || entry.Name != "help" {
			t.Fatal("help is in the table")
		}
		if code := Main("jst", []string{flag}); code != 0 {
			t.Errorf("%s: %d", flag, code)
		}
	}
}
