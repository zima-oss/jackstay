package layout

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var base = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func write(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

// folder makes a version folder as an install does, made at the given time.
func folder(t *testing.T, prefix, version string, made time.Time) string {
	t.Helper()
	path, err := MakeFolder(prefix, version, "src", made, func(binary string) error {
		return os.WriteFile(binary, []byte("#!/bin/sh\necho \"jackstay "+version+"\"\n"), 0o755)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, made, made); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustRead(t *testing.T, prefix string) State {
	t.Helper()
	state, err := Read(prefix)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func kinds(state State) string {
	var words []string
	for _, launcher := range state.Launchers {
		words = append(words, launcher.Kind.String())
	}
	return strings.Join(words, ",")
}

func names(folders []Folder) (out []string) {
	for _, folder := range folders {
		out = append(out, folder.Name)
	}
	return out
}

func evaluated(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// A whole folder is read; one missing a launcher link or its VERSION is not a version folder.
func TestReadFolders(t *testing.T) {
	prefix := t.TempDir()
	whole := folder(t, prefix, "0.5.0 (a)", base)
	broken := folder(t, prefix, "0.5.1 (b)", base)
	if err := os.Remove(filepath.Join(broken, Guard)); err != nil {
		t.Fatal(err)
	}
	unversioned := folder(t, prefix, "0.5.2 (c)", base)
	if err := os.Remove(filepath.Join(unversioned, VersionFile)); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(Lib(prefix), "someone-else", "x"), "", 0o644)
	state := mustRead(t, prefix)
	if got := names(state.Folders); len(got) != 1 || got[0] != filepath.Base(whole) {
		t.Fatalf("folders %v", got)
	}
	if state.Link != LinkAbsent || state.Live != nil || kinds(state) != "absent,absent,absent" {
		t.Fatalf("an empty prefix read as %+v", state)
	}
}

// A fresh prefix: lib/jackstay first, then each launcher; an update is the one rename.
func TestActivateFreshThenUpdate(t *testing.T) {
	prefix := t.TempDir()
	first := folder(t, prefix, "0.5.0 (a)", base)
	var steps []string
	activation := Activation{Prefix: prefix, AfterStep: func(step string) error { steps = append(steps, step); return nil }}
	if err := activation.Activate(first); err != nil {
		t.Fatal(err)
	}
	if strings.Join(steps, ",") != "activate,launcher jst,launcher jackstay,launcher jst-guard" {
		t.Fatalf("steps %v", steps)
	}
	state := mustRead(t, prefix)
	if !state.AllFixed() || state.Live == nil || state.Live.Path != first {
		t.Fatalf("not live: %s %+v", kinds(state), state.Live)
	}
	for _, name := range Launchers {
		if real := evaluated(t, filepath.Join(Bin(prefix), name)); real != filepath.Join(evaluated(t, first), Program) {
			t.Fatalf("%s leads to %s", name, real)
		}
	}
	second := folder(t, prefix, "0.5.1 (b)", base.Add(time.Hour))
	steps = nil
	if err := activation.Activate(second); err != nil {
		t.Fatal(err)
	}
	if strings.Join(steps, ",") != "activate" {
		t.Fatalf("an update took %v", steps)
	}
	state = mustRead(t, prefix)
	if state.Live.Path != second || state.Before() == nil || state.Before().Path != first {
		t.Fatalf("live %+v, before %+v", state.Live, state.Before())
	}
}

// A stop after any step leaves a prefix the next activation finishes.
func TestActivateStoppedAfterEachStep(t *testing.T) {
	want := []string{"activate", "launcher jst", "launcher jackstay", "launcher jst-guard"}
	for stopAt := 1; stopAt <= len(want); stopAt++ {
		prefix := t.TempDir()
		path := folder(t, prefix, "0.5.0 (a)", base)
		seen := 0
		stopped := Activation{Prefix: prefix, AfterStep: func(string) error {
			if seen++; seen == stopAt {
				return ErrStopped
			}
			return nil
		}}
		if err := stopped.Activate(path); !errors.Is(err, ErrStopped) {
			t.Fatalf("stop at %d: %v", stopAt, err)
		}
		if err := (Activation{Prefix: prefix}).Activate(path); err != nil {
			t.Fatal(err)
		}
		if state := mustRead(t, prefix); !state.AllFixed() || state.Live == nil {
			t.Fatalf("stop at %d not finished: %s", stopAt, kinds(state))
		}
	}
}

func TestRefusal(t *testing.T) {
	prefix := t.TempDir()
	write(t, filepath.Join(Bin(prefix), Program), "#!/bin/sh\n", 0o755)
	if err := os.MkdirAll(Lib(prefix), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere/x", Link(prefix)); err != nil {
		t.Fatal(err)
	}
	refusal := mustRead(t, prefix).Refusal()
	if refusal == nil || !strings.Contains(refusal.Error(), "not a launcher jackstay wrote") || !strings.Contains(refusal.Error(), "not a version folder") {
		t.Fatalf("refusal %v", refusal)
	}
	if err := (Activation{Prefix: prefix}).Activate(folder(t, prefix, "0.5.0 (a)", base)); err == nil {
		t.Fatal("a foreign prefix was changed")
	}
}

// Prune keeps the live folder and the one named, and removes every other folder and partial.
func TestBeforeAndPrune(t *testing.T) {
	prefix := t.TempDir()
	oldest := folder(t, prefix, "0.4.9 (x)", base.Add(-48*time.Hour))
	before := folder(t, prefix, "0.5.0 (y)", base.Add(-time.Hour))
	live := folder(t, prefix, "0.5.1 (z)", base)
	if err := os.Symlink(filepath.Base(live), Link(prefix)); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(Lib(prefix), "jackstay-0.5.2-q-1-1"+PartialSuffix)
	write(t, filepath.Join(partial, "x"), "", 0o644)
	state := mustRead(t, prefix)
	if got := state.Before(); got == nil || got.Path != before {
		t.Fatalf("before = %+v", got)
	}
	removed, problems := Prune(state, state.Before().Name)
	if len(problems) > 0 || strings.Join(removed, ",") != oldest+","+partial {
		t.Fatalf("removed %v, problems %v", removed, problems)
	}
	for _, kept := range []string{before, live} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("%s was removed", kept)
		}
	}
}

func TestReplaceLinkLeavesNoPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jackstay")
	write(t, path+PartialSuffix, "left by a stop", 0o644)
	if err := ReplaceLink(path, "target"); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(path); target != "target" {
		t.Fatalf("link says %q", target)
	}
	if _, err := os.Lstat(path + PartialSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial left")
	}
}

// Two folders made in one second by one process get two names.
func TestFolderNamesDoNotCollide(t *testing.T) {
	prefix := t.TempDir()
	put := func(path string) error { return os.WriteFile(path, []byte("x"), 0o755) }
	first, err := MakeFolder(prefix, "0.5.0 (a)", "src", base, put)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MakeFolder(prefix, "0.5.0 (a)", "src", base, put)
	if err != nil || second == first {
		t.Fatalf("%s and %s: %v", first, second, err)
	}
}

func TestThePrefixOfAnInstalledProgram(t *testing.T) {
	for program, want := range map[string]string{
		"/home/ann/.local/lib/jackstay-0.5.8-1a2b3c4-20261118/jst": "/home/ann/.local",
		"/opt/jackstay/lib/jackstay-0.5.8-1a2b3c4-20261118/jst":    "/opt/jackstay",
	} {
		if got, ok := prefixOfProgram(program); !ok || got != want {
			t.Errorf("%s: %s %v, want %s", program, got, ok, want)
		}
	}
	for _, program := range []string{"/work/jackstay/.build/jst", "/home/ann/.local/bin/jst", "/opt/jackstay-x/jst"} {
		if got, ok := prefixOfProgram(program); ok {
			t.Errorf("%s: %s, want none", program, got)
		}
	}
}

func TestThePrefixIsTheOneWhoseGuardLeadsToTheProgram(t *testing.T) {
	base := t.TempDir()
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	mkdir := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, at string) {
		t.Helper()
		if err := os.Symlink(target, at); err != nil {
			t.Fatal(err)
		}
	}
	// An install whose lib is a link to a folder elsewhere, and an alias of its jst elsewhere.
	elsewhere := filepath.Join(base, "volume", "jackstay-lib")
	version := "jackstay-0.5.8-1a2b3c4-20261118"
	mkdir(filepath.Join(elsewhere, version))
	program := filepath.Join(elsewhere, version, "jst")
	if err := os.WriteFile(program, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(base, "opt")
	mkdir(filepath.Join(prefix, "bin"))
	link(elsewhere, filepath.Join(prefix, "lib"))
	link(version, filepath.Join(elsewhere, LinkName)) // the activation point
	link(FixedTarget("jst"), filepath.Join(prefix, "bin", "jst"))
	link(FixedTarget("jst-guard"), filepath.Join(prefix, "bin", "jst-guard"))
	alias := filepath.Join(base, "usr", "bin", "jst")
	mkdir(filepath.Dir(alias))
	link(filepath.Join(prefix, "bin", "jst"), alias)

	if got, ok := installPrefix(launcherChain(alias), program, program); !ok || got != prefix {
		t.Fatalf("through the alias: %s %v, want %s", got, ok, prefix)
	}
	if got, ok := installPrefix(launcherChain(filepath.Join(prefix, "bin", "jst")), program, program); !ok || got != prefix {
		t.Fatalf("through the launcher: %s %v, want %s", got, ok, prefix)
	}
	if _, ok := installPrefix([]string{alias}, program, program); ok {
		t.Fatal("an alias (no install's launcher) was taken for an install")
	}
	checkout := filepath.Join(base, "checkout", ".build", "jst")
	if _, ok := installPrefix([]string{checkout}, checkout, checkout); ok {
		t.Fatal("a checkout's build was taken for an install")
	}
	// An install that stopped before writing jst-guard is still found by its jst.
	if err := os.Remove(filepath.Join(prefix, "bin", "jst-guard")); err != nil {
		t.Fatal(err)
	}
	if got, ok := installPrefix(launcherChain(alias), program, program); !ok || got != prefix {
		t.Fatalf("without jst-guard: %s %v, want %s", got, ok, prefix)
	}
}
