package receipts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/testrepo"
	"github.com/zima-oss/jackstay/internal/version"
)

const keyJSON = `{"jackstay_schema": 2, "project": "demo", "receipts": {"inputs": ["src"], "env": ["STOREFRONT_*", "PLAIN"]}}`

type keyFixture struct {
	t       *testing.T
	repo    *testrepo.Repo
	config  string
	command []string
	dir     string
	environ []string
	scope   any
}

func newKeyFixture(t *testing.T, configText string, files map[string]string) *keyFixture {
	t.Helper()
	all := map[string]string{"jackstay.json": configText, "src/a.swift": "let a = 1\n", ".gitignore": "build/\n"}
	for name, content := range files {
		all[name] = content
	}
	repo := testrepo.New(t, all)
	return &keyFixture{t: t, repo: repo, config: configText, command: []string{"make", "test"}, dir: repo.Root,
		environ: []string{"PATH=" + os.Getenv("PATH")}}
}

func (f *keyFixture) key() (Key, error) {
	f.t.Helper()
	loaded, err := config.Parse([]byte(f.config))
	if err != nil {
		f.t.Fatal(err)
	}
	return KeyOf(context.Background(), Keying{Root: f.repo.Root, Config: loaded, Project: "demo", Command: f.command, Dir: f.dir,
		Environ: f.environ, Scope: f.scope})
}

func (f *keyFixture) must() Key {
	f.t.Helper()
	key, err := f.key()
	if err != nil {
		f.t.Fatalf("no key: %v", err)
	}
	return key
}

func (f *keyFixture) noKey(contains string) {
	f.t.Helper()
	_, err := f.key()
	var unkeyable *Unkeyable
	if !errors.As(err, &unkeyable) || !strings.Contains(err.Error(), contains) {
		f.t.Fatalf("want no key (%q), got %v", contains, err)
	}
}

func TestWhatChangesTheKeyAndWhatDoesNot(t *testing.T) {
	f := newKeyFixture(t, keyJSON, nil)
	first := f.must()
	if first.Material.Schema != 3 || first.Material.Jackstay != version.Version || first.Material.Files != 1 {
		t.Fatalf("material %+v", first.Material)
	}
	if f.must().Value != first.Value {
		t.Fatal("the same tree gave two keys")
	}
	changes := map[string]func(){
		"an edit": func() { f.repo.Write(map[string]string{"src/a.swift": "let a = 2\n"}) },
		"a mode":  func() { _ = os.Chmod(filepath.Join(f.repo.Root, "src", "a.swift"), 0o755) },
		"a new untracked input": func() {
			f.repo.Write(map[string]string{"src/b.swift": "x"})
		},
		"the words":                  func() { f.command = []string{"make", "test", "X=1"} },
		"the folder":                 func() { f.dir = filepath.Join(f.repo.Root, "src") },
		"a named variable":           func() { f.environ = append(f.environ, "PLAIN=1") },
		"a variable by pattern":      func() { f.environ = append(f.environ, "STOREFRONT_SEED=4") },
		"MAKEFLAGS":                  func() { f.environ = append(f.environ, "MAKEFLAGS=STRICT=1") },
		"MAKEOVERRIDES":              func() { f.environ = append(f.environ, "MAKEOVERRIDES=X=1") },
		"a scope's definition":       func() { f.scope = map[string]any{"judge": "swift-test"} },
		"a rule of the project file": func() { f.config = strings.Replace(keyJSON, `"env"`, `"exclude": ["src/b.*"], "env"`, 1) },
		"a toolchain's words": func() {
			f.config = strings.Replace(keyJSON, `"env"`, `"toolchain": [["echo", "swift 6.4"]], "env"`, 1)
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			saved := *f
			f.repo.Git("checkout", "-q", "--", ".")
			f.repo.Git("clean", "-qfd")
			_ = os.Chmod(filepath.Join(f.repo.Root, "src", "a.swift"), 0o600)
			before := f.must()
			change()
			if after := f.must(); after.Value == before.Value {
				t.Fatalf("%s did not change the key", name)
			}
			*f = saved
		})
	}
	f.repo.Git("checkout", "-q", "--", ".")
	_ = os.Chmod(filepath.Join(f.repo.Root, "src", "a.swift"), 0o600)
	base := f.must()
	// Not in the key: an unlisted variable, the job server's address, the lock's holder, the run id.
	f.environ = append(f.environ, "OTHER=1", "STOREFRONT_LOCK_HELD=/x:1", "JACKSTAY_TEST_RUN_ID=1-2", "JACKSTAY_LOCK_HELD=/y:3")
	if f.must().Value != base.Value {
		t.Fatal("an unlisted or per-run variable changed the key")
	}
	f.environ = append(f.environ, "MAKEFLAGS=-j4 --jobserver-auth=fifo:/tmp/GMfifo1")
	withJobs := f.must().Value
	f.environ[len(f.environ)-1] = "MAKEFLAGS=-j4 --jobserver-auth=fifo:/tmp/GMfifo2"
	if f.must().Value != withJobs {
		t.Fatal("the job server's address changed the key")
	}
}

func TestACommitOfTheSameContentIsTheSameTree(t *testing.T) {
	f := newKeyFixture(t, keyJSON, nil)
	clean := f.must().Value
	f.repo.Write(map[string]string{"src/a.swift": "let a = 2\n"})
	edited := f.must().Value
	f.repo.Git("commit", "-qam", "edit")
	if f.must().Value != edited {
		t.Fatal("a commit of the same content changed the key")
	}
	f.repo.Write(map[string]string{"src/a.swift": "let a = 1\n"})
	if f.must().Value != clean {
		t.Fatal("a reverted edit is not the first tree")
	}
	f.config = strings.Replace(keyJSON, `"env"`, `"include_head": true, "env"`, 1)
	if f.must().Value == clean || f.must().Material.Head == "" {
		t.Fatal("include_head did not put HEAD in")
	}
}

func TestInputsTrackedMissingIgnoredAndLinked(t *testing.T) {
	f := newKeyFixture(t, keyJSON, map[string]string{"src/b.swift": "b", "src/linked.txt": "target"})
	present := f.must()
	if err := os.Remove(filepath.Join(f.repo.Root, "src", "b.swift")); err != nil {
		t.Fatal(err)
	}
	missing := f.must()
	if missing.Value == present.Value || missing.Stamps["src/b.swift"] != "-" {
		t.Fatalf("a tracked file that is missing is not in the key: %v", missing.Stamps)
	}
	f.repo.Git("checkout", "-q", "--", "src/b.swift")
	// A file only a committed .gitignore hides is no input; one only .git/info/exclude hides is.
	f.repo.Write(map[string]string{"src/build/x.o": "1"})
	if _, in := f.must().Stamps["src/build/x.o"]; in {
		t.Fatal("a file a committed rule ignores is an input")
	}
	f.repo.Write(map[string]string{".git/info/exclude": "private.swift\n", "src/private.swift": "p"})
	if _, in := f.must().Stamps["src/private.swift"]; !in {
		t.Fatal("a private ignore rule hid a file from the key")
	}
	// A link is its target's name and, when that is a file, its content.
	if err := os.Symlink("linked.txt", filepath.Join(f.repo.Root, "src", "link")); err != nil {
		t.Fatal(err)
	}
	before := f.must()
	f.repo.Write(map[string]string{"src/linked.txt": "changed"})
	after := f.must()
	if before.Value == after.Value || !strings.Contains(after.Stamps["src/link"], ">") {
		t.Fatalf("a link's target content is not in the key: %v", after.Stamps["src/link"])
	}
	// The stamp moves when a file is rewritten, also to the same content.
	place := filepath.Join(f.repo.Root, "src", "a.swift")
	_ = os.Remove(place)
	f.repo.Write(map[string]string{"src/a.swift": "let a = 1\n"})
	if again := f.must(); again.Value != after.Value || again.Stamps["src/a.swift"] == after.Stamps["src/a.swift"] {
		t.Fatal("a rewrite to the same content is not seen in the stamps (or changed the key)")
	}
}

func TestExcludeIsForTheWholePath(t *testing.T) {
	cases := []struct {
		path, pattern string
		want          bool
	}{
		{"README.md", "*.md", true}, {"docs/a.md", "*.md", false}, {"docs/a.md", "**/*.md", true}, {"a.md", "**/*.md", true},
		{"docs/x/y.txt", "docs/**", true}, {"docsx/y", "docs/**", false}, {"a.c", "?.c", true}, {"ab.c", "?.c", false},
		{"a+b.c", "a+b.c", true},
	}
	for _, c := range cases {
		if got := Excluded(c.path, []string{c.pattern}); got != c.want {
			t.Errorf("Excluded(%q, %q) = %v", c.path, c.pattern, got)
		}
	}
	f := newKeyFixture(t, strings.Replace(keyJSON, `"env"`, `"exclude": ["src/*.md"], "extra_files": ["notes.txt"], "env"`, 1),
		map[string]string{"src/a.md": "doc", "notes.txt": "n"})
	key := f.must()
	if _, in := key.Stamps["src/a.md"]; in {
		t.Fatal("excluded file in the key")
	}
	if _, in := key.Stamps["notes.txt"]; !in {
		t.Fatal("extra file not in the key")
	}
}

func TestEveryCaseWithoutAKey(t *testing.T) {
	t.Run("no jackstay.json", func(t *testing.T) {
		_, err := KeyOf(context.Background(), Keying{Command: []string{"x"}})
		if err == nil || !strings.Contains(err.Error(), "no jackstay.json") {
			t.Fatal(err)
		}
	})
	t.Run("no inputs", func(t *testing.T) {
		newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {}}`, nil).noKey("names no receipts.inputs")
	})
	t.Run("an input that names nothing", func(t *testing.T) {
		newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src", "nowhere"]}}`, nil).noKey(`the input "nowhere" names no file`)
	})
	t.Run("an input outside the checkout", func(t *testing.T) {
		newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["../x"]}}`, nil).noKey("a plain path inside the checkout")
	})
	t.Run("everything excluded", func(t *testing.T) {
		newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"], "exclude": ["**"]}}`, nil).noKey("no file at all")
	})
	t.Run("a folder among the inputs", func(t *testing.T) {
		f := newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"], "extra_files": ["vendor"]}}`, nil)
		if err := os.MkdirAll(filepath.Join(f.repo.Root, "vendor"), 0o750); err != nil {
			t.Fatal(err)
		}
		f.noKey("is a folder, not a file")
	})
	t.Run("a nested repository git lists as one entry", func(t *testing.T) {
		f := newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"]}}`, nil)
		inner := filepath.Join(f.repo.Root, "src", "inner")
		f.repo.Write(map[string]string{"src/inner/x.txt": "x"})
		f.repo.GitIn(inner, "init", "-q")
		f.noKey("is a folder, not a file")
	})
	t.Run("a link to a folder", func(t *testing.T) {
		f := newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"]}}`, map[string]string{"real/x.txt": "x"})
		if err := os.Symlink("../real", filepath.Join(f.repo.Root, "src", "dir")); err != nil {
			t.Fatal(err)
		}
		f.noKey("is a link to a folder")
	})
	t.Run("an untracked .gitignore", func(t *testing.T) {
		f := newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"]}}`, nil)
		f.repo.Write(map[string]string{"src/.gitignore": "*.swift\n"})
		f.noKey("is not committed and no committed rule ignores its folder")
	})
	t.Run("an untracked .gitignore inside an ignored folder is the exception", func(t *testing.T) {
		f := newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"]}}`, nil)
		f.repo.Write(map[string]string{"build/.gitignore": "*\n"})
		f.must()
	})
	t.Run("a .gitignore that differs from HEAD", func(t *testing.T) {
		f := newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"]}}`, nil)
		f.repo.Write(map[string]string{".gitignore": "build/\n*.swift\n"})
		f.noKey(".gitignore differs from the commit (HEAD)")
	})
	t.Run("a .gitignore hidden by assume-unchanged", func(t *testing.T) {
		f := newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"]}}`, nil)
		f.repo.Git("update-index", "--assume-unchanged", ".gitignore")
		f.noKey("git is told not to look at this ignore file")
	})
	t.Run("a .gitignore hidden by skip-worktree", func(t *testing.T) {
		f := newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"]}}`, nil)
		f.repo.Git("update-index", "--skip-worktree", ".gitignore")
		f.noKey("git is told not to look at this ignore file")
	})
	t.Run("a ref that cannot be resolved", func(t *testing.T) {
		newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"], "refs": ["origin/nowhere"]}}`, nil).noKey("git rev-parse --verify failed")
	})
	t.Run("a command's own merge base that cannot be resolved", func(t *testing.T) {
		newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"], "commands": [{"argv": ["make", "test"], "merge_bases": ["origin/nowhere"]}]}}`,
			nil).noKey("git merge-base HEAD failed")
	})
	t.Run("a toolchain command that fails", func(t *testing.T) {
		newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"], "toolchain": [["false"]]}}`, nil).noKey("the toolchain command false failed (1)")
	})
	t.Run("a toolchain command that cannot run", func(t *testing.T) {
		newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"], "toolchain": [["/nonexistent/tool"]]}}`, nil).noKey("could not be run")
	})
}

func TestRefsAndMergeBasesAreInTheKey(t *testing.T) {
	f := newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"], "refs": ["main"], "commands": [{"argv": ["make", "test"], "merge_bases": ["main"]}]}}`, nil)
	f.repo.Git("checkout", "-q", "-b", "feature")
	before := f.must()
	if before.Material.Refs["main"] == "" || before.Material.MergeBases["main"] == "" {
		t.Fatalf("material %+v", before.Material)
	}
	f.repo.Git("checkout", "-q", "main")
	f.repo.Write(map[string]string{"other.txt": "o"})
	f.repo.Git("add", "other.txt")
	f.repo.Git("commit", "-qm", "moved")
	f.repo.Git("checkout", "-q", "feature")
	if f.must().Value == before.Value {
		t.Fatal("a moved ref did not change the key")
	}
}

func TestCommandEntry(t *testing.T) {
	loaded, err := config.Parse([]byte(`{"jackstay_schema": 2, "receipts": {"commands": [{"argv_prefix": ["make"]}, {"argv_prefix": ["make", "test"], "veto": "v"},
		{"argv": ["make", "test", "X"], "evidence": "E ([0-9]+)"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	section := Section(loaded)
	if CommandEntry(section, []string{"make", "test", "X"})["evidence"] == nil {
		t.Fatal("argv exactly")
	}
	if CommandEntry(section, []string{"make", "test", "Y"})["veto"] != "v" {
		t.Fatal("the longest prefix")
	}
	if CommandEntry(section, []string{"just"}) != nil {
		t.Fatal("not listed")
	}
}

func TestToolchainKeyUsesTheCommandsDirectoryAndEnvironment(t *testing.T) {
	f := newKeyFixture(t, `{"jackstay_schema": 2, "receipts": {"inputs": ["src"], "toolchain": [["audit-toolchain"]]}}`,
		map[string]string{"src/marker": "selected checkout"})
	f.dir = filepath.Join(f.repo.Root, "src")
	tools := t.TempDir()
	program := "#!/bin/sh\ncat marker\nprintf ':%s' \"$SDK_MODE\"\n"
	if err := os.WriteFile(filepath.Join(tools, "audit-toolchain"), []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	f.environ = []string{"PATH=" + tools + ":/bin", "SDK_MODE=pinned"}
	key := f.must()
	if len(key.Material.Facts.Toolchain) != 1 || key.Material.Facts.Toolchain[0].Said != "selected checkout:pinned" {
		t.Fatalf("probed the wrong execution context: %+v", key.Material.Facts.Toolchain)
	}
}
