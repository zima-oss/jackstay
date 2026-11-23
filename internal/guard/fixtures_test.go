package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// The cases that need a real repository, a real folder or real links: git
// answers itself here, as it does in the hook.

func gitIn(t *testing.T, folder string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", folder, "-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"-c", "commit.gpgsign=false"}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	_ = command.Run()
}

// objectIDs is every object of the repository, loose or packed, one id a line in git's order: the
// set, not the folder's entries, so packing or a new fan-out folder does not count as a write.
func objectIDs(t *testing.T, folder string) string {
	t.Helper()
	command := exec.Command("git", "-C", folder, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func write(t *testing.T, place, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(place), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(place, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func realFolder(t *testing.T) string {
	t.Helper()
	folder, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return folder
}

func liveRules(command, folder string, scratch ...string) []string {
	return Decide(command, Settings{Folder: folder, Environ: map[string]string{"TMPDIR": "/var/folders/ab/cdef/T/"},
		Scratch: scratch, Home: "/Users/someone", Git: testGit(), Deadline: testBudget}).Rules()
}

func expectRules(t *testing.T, command, folder string, want []string, scratch ...string) {
	t.Helper()
	got := liveRules(command, folder, scratch...)
	if !slices.Equal(got, want) && (len(got) != 0 || len(want) != 0) {
		t.Errorf("%q in %s: got %v, want %v", command, folder, got, want)
	}
}

func TestLiveConflictQuestion(t *testing.T) {
	folder := realFolder(t)
	gitIn(t, folder, "init", "-q", "-b", "main")
	write(t, filepath.Join(folder, "f"), "base\n")
	gitIn(t, folder, "add", "f")
	gitIn(t, folder, "commit", "-q", "-m", "base")
	gitIn(t, folder, "checkout", "-q", "-b", "other")
	write(t, filepath.Join(folder, "f"), "theirs\n")
	gitIn(t, folder, "commit", "-q", "-am", "theirs")
	gitIn(t, folder, "checkout", "-q", "main")
	write(t, filepath.Join(folder, "f"), "ours\n")
	gitIn(t, folder, "commit", "-q", "-am", "ours")
	expectRules(t, "git checkout --theirs -- f", folder, []string{"git-checkout-path"})
	gitIn(t, folder, "merge", "other")
	expectRules(t, "git checkout --theirs -- f", folder, nil)
	write(t, filepath.Join(folder, "unrelated"), "x\n")
	gitIn(t, folder, "add", "unrelated")
	write(t, filepath.Join(folder, "unrelated"), "an edit that is not part of the merge\n")
	expectRules(t, "git checkout --theirs .", folder, []string{"git-checkout-path"})
	expectRules(t, "git restore --theirs .", folder, []string{"git-restore-worktree"})
	expectRules(t, "git checkout --theirs -- f", folder, nil)
}

func TestLiveBranchQuestions(t *testing.T) {
	folder := realFolder(t)
	gitIn(t, folder, "init", "-q", "-b", "main")
	gitIn(t, folder, "commit", "-q", "--allow-empty", "-m", "one")
	gitIn(t, folder, "branch", "merged")
	gitIn(t, folder, "checkout", "-q", "-b", "alone")
	write(t, filepath.Join(folder, "work.txt"), "")
	gitIn(t, folder, "add", "work.txt")
	gitIn(t, folder, "commit", "-q", "-m", "two")
	gitIn(t, folder, "checkout", "-q", "main")
	git := testGit()
	if git.Held(folder, "merged") != answerYes || git.Held(folder, "alone") != answerNo || git.Held(folder, "nope") != answerUnsure {
		t.Fatal("held")
	}
	if git.BranchExists(folder, "main") != answerYes || git.BranchExists(folder, "nope") != answerNo ||
		git.BranchExists(t.TempDir(), "main") != answerUnsure {
		t.Fatal("branch exists")
	}
	expectRules(t, "git checkout -B alone main", folder, []string{"git-branch-reset"})
	expectRules(t, "git checkout -B merged main", folder, nil)
	expectRules(t, "git checkout -B brand-new main", folder, nil)
	expectRules(t, "git branch -D merged", folder, nil)
	expectRules(t, "git branch -D alone", folder, []string{"git-branch-force-delete"})
}

func TestLiveAddsNothing(t *testing.T) {
	folder := realFolder(t)
	commit := func(name, text, message string) {
		write(t, filepath.Join(folder, name), text)
		gitIn(t, folder, "add", name)
		gitIn(t, folder, "commit", "-q", "-m", message)
	}
	gitIn(t, folder, "init", "-q", "-b", "main")
	// No packing behind the test's back: the objects are compared before and after.
	gitIn(t, folder, "config", "gc.auto", "0")
	gitIn(t, folder, "config", "maintenance.auto", "false")
	commit("f", "base\n", "base")
	gitIn(t, folder, "checkout", "-q", "-b", "squashed")
	commit("g", "one\n", "one")
	commit("g", "two\n", "two")
	gitIn(t, folder, "checkout", "-q", "-b", "half", "main")
	commit("h", "kept\n", "kept")
	commit("i", "not merged\n", "more")
	gitIn(t, folder, "checkout", "-q", "-b", "clash", "main")
	commit("f", "theirs\n", "theirs")
	gitIn(t, folder, "checkout", "-q", "main")
	gitIn(t, folder, "merge", "-q", "--squash", "squashed")
	gitIn(t, folder, "commit", "-q", "-m", "squash")
	gitIn(t, folder, "checkout", "-q", "half~1", "--", "h")
	gitIn(t, folder, "commit", "-q", "-m", "a part of half")
	commit("f", "ours\n", "ours")
	objects := objectIDs(t, folder)
	git := testGit()
	if !git.AddsNothing(folder, "squashed") || git.AddsNothing(folder, "half") || git.AddsNothing(folder, "clash") ||
		git.AddsNothing(folder, "no-such-branch") || git.AddsNothing(folder, "main") || git.AddsNothing(folder, "--help") {
		t.Fatal("adds nothing")
	}
	if after := objectIDs(t, folder); after != objects {
		t.Fatal("the repository was written to")
	}
	expectRules(t, "git branch -D squashed", folder, nil)
	expectRules(t, "git branch -D squashed half", folder, []string{"git-branch-force-delete"})
	gitIn(t, folder, "update-ref", "refs/remotes/origin/main", "main~3")
	if git.AddsNothing(folder, "squashed") {
		t.Fatal("a local main ahead of origin/main proves nothing")
	}
}

func TestLiveWhatWouldBeLost(t *testing.T) {
	folder := realFolder(t)
	gitIn(t, folder, "init", "-q", "-b", "main")
	for name, text := range map[string]string{"a": "a\n", "b": "b\n", "gone": "x\n", ".gitignore": "*.log\n"} {
		write(t, filepath.Join(folder, name), text)
	}
	gitIn(t, folder, "add", ".")
	gitIn(t, folder, "commit", "-q", "-m", "base")
	gitIn(t, folder, "checkout", "-q", "-b", "other")
	write(t, filepath.Join(folder, "new"), "theirs\n")
	write(t, filepath.Join(folder, "x.log"), "theirs\n")
	gitIn(t, folder, "add", "-f", "new", "x.log")
	gitIn(t, folder, "commit", "-q", "-m", "other")
	gitIn(t, folder, "checkout", "-q", "main")
	expectRules(t, "git checkout -- a", folder, nil)
	expectRules(t, "git checkout .", folder, nil)
	expectRules(t, "git checkout other -- new", folder, nil)
	write(t, filepath.Join(folder, "a"), "edited\n")
	_ = os.Remove(filepath.Join(folder, "gone"))
	expectRules(t, "git checkout -- a", folder, []string{"git-checkout-path"})
	expectRules(t, "git checkout -- b", folder, nil)
	expectRules(t, "git checkout -- gone", folder, nil)
	expectRules(t, "git restore .", folder, []string{"git-restore-worktree"})
	gitIn(t, folder, "add", "a")
	expectRules(t, "git checkout -- a", folder, nil)
	expectRules(t, "git checkout HEAD -- a", folder, []string{"git-checkout-path"})
	write(t, filepath.Join(folder, "new"), "mine, untracked\n")
	expectRules(t, "git checkout other -- new", folder, []string{"git-checkout-path"})
	expectRules(t, "git checkout other -- b", folder, nil)
	tree := filepath.Join(folder, "wt")
	gitIn(t, folder, "worktree", "add", "-q", tree, "other")
	expectRules(t, "git worktree remove --force wt", folder, nil)
	write(t, filepath.Join(tree, "untracked"), "work\n")
	expectRules(t, "git worktree remove --force wt", folder, []string{"git-worktree-force"})
	expectRules(t, "git worktree remove --force no-such-folder", folder, []string{"git-worktree-force"})
}

func TestLiveEditsGitIsToldNotToSee(t *testing.T) {
	folder := realFolder(t)
	gitIn(t, folder, "init", "-q", "-b", "main")
	for name, text := range map[string]string{"a": "a\n", "b": "b\n", "c": "c\n", "d/e": "e\n"} {
		write(t, filepath.Join(folder, name), text)
	}
	gitIn(t, folder, "add", ".")
	gitIn(t, folder, "commit", "-q", "-m", "base")
	gitIn(t, folder, "update-index", "--assume-unchanged", "a")
	write(t, filepath.Join(folder, "a"), "edited, and git does not say so\n")
	expectRules(t, "git checkout -- a", folder, []string{"git-checkout-path"})
	expectRules(t, "git restore a", folder, []string{"git-restore-worktree"})
	expectRules(t, "git checkout -- b d", folder, nil)
	gitIn(t, folder, "update-index", "--skip-worktree", "d/e")
	write(t, filepath.Join(folder, "d/e"), "edited too\n")
	expectRules(t, "git restore d", folder, []string{"git-restore-worktree"})
	expectRules(t, "git restore b c", folder, nil)
}

func TestLiveOnlyTheSessionsOwnScratchIsAThrowaway(t *testing.T) {
	other := realFolder(t)
	repo := filepath.Join(other, "repo")
	gitIn(t, other, "init", "-q", repo)
	for _, command := range []string{"git -C " + repo + " reset --hard", "git -C " + repo + " clean -fdx"} {
		if len(liveRules(command, "/Users/someone/project")) == 0 {
			t.Errorf("%q passed without a scratch folder", command)
		}
		expectRules(t, command, "/Users/someone/project", nil, other)
	}
	expectRules(t, "rm -rf "+repo+"/sub", "/Users/someone/project", nil, other)
	gitIn(t, repo, "config", "core.worktree", "/Users/someone/project")
	expectRules(t, "git -C "+repo+" reset --hard", "/Users/someone/project", []string{"git-reset-hard"}, other)
}

func TestLiveLinksOnDisk(t *testing.T) {
	folder := realFolder(t)
	home := realFolder(t)
	if err := os.Symlink(home, filepath.Join(folder, "l")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(folder, ".build")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(folder, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"rm -rf " + folder + "/l/", "rm -rf " + folder + "/l/Documents", "rm -rf .build/", "rm -rf .build/x"} {
		if len(liveRules(command, folder)) == 0 {
			t.Errorf("%q passed", command)
		}
	}
	scratch := realPath(os.TempDir())
	expectRules(t, "rm -rf .build", folder, nil, scratch)
	expectRules(t, "rm -rf \"$PWD/.build\"", folder, nil, scratch)
}

func TestLiveZshPatterns(t *testing.T) {
	folder := realFolder(t)
	write(t, filepath.Join(folder, "a.swift"), "")
	write(t, filepath.Join(folder, "lib", "a.py"), "")
	zsh := func(command string) []string {
		// The judge itself, the row set to refuse; its default level (warn) is tested in levels_test.go.
		return Decide(command, Settings{Folder: folder, Environ: map[string]string{"SHELL": "/bin/zsh"}, Home: "/Users/someone",
			Git: testGit(), Guard: map[string]any{"rules": map[string]any{"zsh-glob": map[string]any{"level": "refuse"}}}, Deadline: testBudget}).Rules()
	}
	for _, command := range []string{"grep -rn foo --include=*.swift .", "rg foo -g *.swift", "find . -name *.swift",
		"ls *.nothing", "cat notes/*.md", "echo done?", "ls {lib,tests}/*.py", "zsh -c 'grep -r x --include=*.swift .'",
		"set -e; grep -r x --include=*.swift ."} {
		if got := zsh(command); !slices.Equal(got, []string{"zsh-glob"}) {
			t.Errorf("%q: %v", command, got)
		}
	}
	for _, command := range []string{"grep -rn foo --include='*.swift' .", "ls *.swift", "noglob grep -r x --include=*.md .",
		"[[ $x == *.swift ]] && echo y", "bash -c 'ls *.nothing'", "ls **/*.nothing", "printf '%s\\n' nothing_here*(N)",
		"setopt nonomatch; ls jackstay-none-*.xxx", "ls {lib,lib}/*.py", "set -f; grep -r x --include=*.swift ."} {
		if got := zsh(command); len(got) > 0 {
			t.Errorf("%q: %v", command, got)
		}
	}
	if got := liveRules("grep -rn foo --include=*.swift .", folder); len(got) > 0 {
		t.Errorf("bash: %v", got)
	}
}
