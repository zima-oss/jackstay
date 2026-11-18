package adopt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheGuardCommandIsRelativeToHomeWhenItCanBe(t *testing.T) {
	cases := []struct{ launcher, home, want string }{
		{"/home/ann/.local/bin/jst-guard", "/home/ann", `"$HOME/.local/bin/jst-guard"`},
		{"/home/ann/tools/jackstay/bin/jst-guard", "/home/ann", `"$HOME/tools/jackstay/bin/jst-guard"`},
		{"/opt/jackstay/bin/jst-guard", "/home/ann", `"/opt/jackstay/bin/jst-guard"`},
		{"/home/annex/bin/jst-guard", "/home/ann", `"/home/annex/bin/jst-guard"`},
		{"/home/ann/my $tools/bin/jst-guard", "/home/ann", `"$HOME/my \$tools/bin/jst-guard"`},
		{"/opt/jackstay/bin/jst-guard", "", `"/opt/jackstay/bin/jst-guard"`},
	}
	for _, each := range cases {
		if got := guardCommandFor(each.launcher, each.home); got != each.want {
			t.Errorf("%s under %q: got %s, want %s", each.launcher, each.home, got, each.want)
		}
		if back, err := guardLauncherOf(guardCommandFor(each.launcher, each.home), each.home); err != nil || back != each.launcher {
			t.Errorf("%s read back as %s (%v)", each.launcher, back, err)
		}
	}
}

func TestTheGuardLauncherIsReadAsTheShellReadsIt(t *testing.T) {
	home := "/home/ann"
	for command, want := range map[string]string{
		`"$HOME/.local/bin/jst-guard"`:        "/home/ann/.local/bin/jst-guard",
		`"${HOME}/.local/bin/jst-guard"`:      "/home/ann/.local/bin/jst-guard",
		`~/.local/bin/jst-guard`:              "/home/ann/.local/bin/jst-guard",
		`/opt/x/bin/jst-guard --verbose`:      "/opt/x/bin/jst-guard",
		`'/opt/my tools/bin/jst-guard'`:       "/opt/my tools/bin/jst-guard",
		`"/opt/x/bin/jst-guard" --verbose`:    "/opt/x/bin/jst-guard",
		`"/opt/Jackstay Tools/bin/jst-guard"`: "/opt/Jackstay Tools/bin/jst-guard",
		`"~/.local/bin/jst-guard"`:            "~/.local/bin/jst-guard", // quoted: the shell keeps the tilde
		`'$HOME/.local/bin/jst-guard'`:        "$HOME/.local/bin/jst-guard",
		`"$HOME/my \$tools/bin/jst-guard"`:    "/home/ann/my $tools/bin/jst-guard",
	} {
		if got, err := guardLauncherOf(command, home); err != nil || got != want {
			t.Errorf("%s: got %q (%v), want %q", command, got, err, want)
		}
	}
	for _, command := range []string{`"$(which jst-guard)"`, `$OTHER/jst-guard`, `a; b`, `if true; then x; fi`, `"unclosed`,
		`<(echo /bin)/jst-guard`, "`which jst-guard`", `${HOME:-/x}/jst-guard`, `${#HOME}/jst-guard`, `$((1))/jst-guard`,
		`/opt/*/bin/jst-guard`, `${HOME/a/b}/jst-guard`} {
		if got, err := guardLauncherOf(command, home); err == nil {
			t.Errorf("%s: read as %q, want an error", command, got)
		}
	}
}

func TestAHookUnderASpacedPrefixIsFoundAgain(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "demo"}` + "\n"})
	options := optionsFor(root)
	options.GuardCommand = guardCommandFor("/opt/Jackstay Tools/bin/jst-guard", "/home/ann")
	first := plan(t, options)
	if err := first.Apply(); err != nil {
		t.Fatal(err)
	}
	if again := plan(t, options); len(again.Changes) != 0 {
		t.Fatalf("a second plan changes %v", again.Changes)
	}
}

func TestAGuardHookThatRunsNothingIsAProblem(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	write := func(command string) {
		t.Helper()
		settings := `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": ` +
			strings.ReplaceAll(`"`+strings.ReplaceAll(command, `"`, `\"`)+`"`, "\n", "") + `}]}]}}`
		if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if problems := HookProblems(root, home); len(problems) != 0 {
		t.Fatalf("no settings file, problems %v", problems)
	}
	write(`"$HOME/.local/bin/jst-guard"`)
	if problems := HookProblems(root, home); len(problems) != 1 || !strings.Contains(problems[0], "unguarded") {
		t.Fatalf("a hook that runs nothing: %v", problems)
	}
	launcher := filepath.Join(home, ".local", "bin", "jst-guard")
	if err := os.MkdirAll(filepath.Dir(launcher), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if problems := HookProblems(root, home); len(problems) != 1 {
		t.Fatalf("a launcher that is not executable: %v", problems)
	}
	if err := os.Chmod(launcher, 0o700); err != nil {
		t.Fatal(err)
	}
	if problems := HookProblems(root, home); len(problems) != 0 {
		t.Fatalf("a hook that runs its launcher: %v", problems)
	}
	write(`"~/.local/bin/jst-guard"`) // the shell keeps a quoted tilde: no such file
	if problems := HookProblems(root, home); len(problems) != 1 {
		t.Fatalf("a quoted tilde: %v", problems)
	}
}

func TestANewGuardHookTakesTheGivenCommand(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "demo"}` + "\n"})
	options := optionsFor(root)
	options.GuardCommand = `"/opt/jackstay/bin/jst-guard"`
	made := plan(t, options)
	for _, change := range made.writes {
		if change.path == settingsFile {
			if !strings.Contains(string(change.data), `/opt/jackstay/bin/jst-guard`) {
				t.Fatalf("settings written: %s", change.data)
			}
			return
		}
	}
	t.Fatal("no guard hook planned")
}

func TestAnInstallWithoutItsGuardLauncherIsAConflict(t *testing.T) {
	prefix := t.TempDir()
	previous := runningInstall
	runningInstall = func() (string, bool) { return prefix, true }
	t.Cleanup(func() { runningInstall = previous })
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "demo"}` + "\n"})
	made := plan(t, optionsFor(root))
	found := false
	for _, conflict := range made.Conflicts {
		found = found || conflict.ID == "hook:no-guard-launcher"
	}
	if !found {
		t.Fatalf("no conflict for an install without jst-guard: %+v", made.Conflicts)
	}
	launcher := filepath.Join(prefix, "bin", "jst-guard")
	if err := os.MkdirAll(filepath.Dir(launcher), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if again := plan(t, optionsFor(root)); len(again.Conflicts) != 0 {
		t.Fatalf("conflicts once jst-guard is there: %+v", again.Conflicts)
	}
}
