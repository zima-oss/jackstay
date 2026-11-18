//go:build integration

package branch

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a working clone (remote named remoteName) of a bare repository, and another clone
// (remote `origin`) that moves the remote's main.
type fixture struct {
	t                       *testing.T
	base, bare, root, other string
	remoteName              string
}

func clearEnv(t *testing.T, home string) {
	t.Helper()
	for _, pair := range os.Environ() {
		if name, _, _ := strings.Cut(pair, "="); strings.HasPrefix(name, "JACKSTAY_") || strings.HasPrefix(name, "GIT_") {
			t.Setenv(name, "")
			_ = os.Unsetenv(name)
		}
	}
	// git as jackstay runs it gets no GIT_* variable: the identity and settings come from a HOME of the
	// test's own, never the developer's.
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	config := "[user]\n\tname = t\n\temail = t@example.invalid\n[init]\n\tdefaultBranch = main\n" +
		"[commit]\n\tgpgsign = false\n[advice]\n\tdetachedHead = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T, remoteName string) *fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o750); err != nil {
		t.Fatal(err)
	}
	clearEnv(t, home)
	fix := &fixture{t: t, base: base, bare: filepath.Join(base, "remote.git"), root: filepath.Join(base, "work"),
		other: filepath.Join(base, "other"), remoteName: remoteName}
	fix.git(base, "init", "-q", "--bare", "-b", "main", fix.bare)
	fix.git(base, "init", "-q", "-b", "main", fix.root)
	fix.git(fix.root, "remote", "add", remoteName, fix.bare)
	fix.write(fix.root, "a.txt", "one\n")
	fix.git(fix.root, "add", ".")
	fix.git(fix.root, "commit", "-qm", "first")
	fix.git(fix.root, "push", "-q", "-u", remoteName, "main")
	fix.git(base, "clone", "-q", fix.bare, fix.other)
	t.Chdir(fix.root)
	return fix
}

// git runs git in dir with fixed dates; a failure fails the test.
func (fix *fixture) git(dir string, args ...string) string {
	fix.t.Helper()
	out, err := fix.gitMay(dir, args...)
	if err != nil {
		fix.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func (fix *fixture) gitMay(dir string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	out, err := command.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (fix *fixture) write(dir, name, text string) {
	fix.t.Helper()
	place := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(place), 0o750); err != nil {
		fix.t.Fatal(err)
	}
	if err := os.WriteFile(place, []byte(text), 0o600); err != nil {
		fix.t.Fatal(err)
	}
}

func (fix *fixture) read(name string) string {
	fix.t.Helper()
	data, err := os.ReadFile(filepath.Join(fix.root, name))
	if err != nil {
		fix.t.Fatal(err)
	}
	return string(data)
}

func (fix *fixture) exclude(pattern string) {
	fix.t.Helper()
	file, err := os.OpenFile(filepath.Join(fix.root, ".git", "info", "exclude"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		fix.t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(pattern + "\n"); err != nil {
		fix.t.Fatal(err)
	}
}

// advanceMain commits a file in the other clone and pushes main.
func (fix *fixture) advanceMain(name, text string) {
	fix.t.Helper()
	fix.write(fix.other, name, text)
	fix.git(fix.other, "add", ".")
	fix.git(fix.other, "commit", "-qm", "elsewhere "+name)
	fix.git(fix.other, "push", "-q", "origin", "main")
}

// commitHere commits files on the current branch of the working clone.
func (fix *fixture) commitHere(message string, files map[string]string) {
	fix.t.Helper()
	for name, text := range files {
		fix.write(fix.root, name, text)
	}
	fix.git(fix.root, "add", ".")
	fix.git(fix.root, "commit", "-qm", message)
}

// outcome is a command's exit status and output.
type outcome struct {
	code           int
	stdout, stderr string
}

func (fix *fixture) sync(args ...string) outcome {
	var stdout, stderr bytes.Buffer
	code := runSync(append(args, fix.root), nil, &stdout, &stderr)
	return outcome{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func (fix *fixture) start(args ...string) outcome {
	var stdout, stderr bytes.Buffer
	code := runStart(args, nil, &stdout, &stderr)
	return outcome{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func (got outcome) want(t *testing.T, code int, says ...string) {
	t.Helper()
	if got.code != code {
		t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", got.code, code, got.stdout, got.stderr)
	}
	for _, words := range says {
		if !strings.Contains(got.stdout+got.stderr, words) {
			t.Fatalf("%q not said\nstdout: %s\nstderr: %s", words, got.stdout, got.stderr)
		}
	}
}

func (fix *fixture) exists(name string) bool {
	_, err := os.Lstat(filepath.Join(fix.root, name))
	return err == nil
}

func TestSyncFastForwardsMain(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.advanceMain("b.txt", "from elsewhere\n")
	fix.sync().want(t, 0, "fast-forwarded")
	if !fix.exists("b.txt") {
		t.Fatal("b.txt not brought in")
	}
}

func TestSyncMergesTheDefaultBranchAndPushes(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.commitHere("mine", map[string]string{"c.txt": "mine\n"})
	fix.advanceMain("b.txt", "from elsewhere\n")
	fix.sync("--push").want(t, 0, "merged refs/remotes/origin/main into feature")
	if !fix.exists("b.txt") {
		t.Fatal("b.txt not merged")
	}
	if fix.git(fix.bare, "rev-parse", "feature") != fix.git(fix.root, "rev-parse", "HEAD") {
		t.Fatal("feature not pushed")
	}
	if got := fix.git(fix.root, "config", "--get", "branch.feature.merge"); got != "refs/heads/feature" {
		t.Fatalf("upstream %q, want refs/heads/feature", got)
	}
	fix.sync().want(t, 0, "already has every commit")
}

func TestSyncConflictStopsWithTheFilesNamedAndTheMergeLeft(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.commitHere("mine", map[string]string{"a.txt": "mine\n"})
	fix.advanceMain("a.txt", "theirs\n")
	fix.sync().want(t, 1, `"a.txt"`, "git merge --abort")
	if !fix.exists(".git/MERGE_HEAD") {
		t.Fatal("the merge was not left in place")
	}
	fix.sync("--no-fetch").want(t, 2, "already under way")
}

func TestSyncRefusesUncommittedWorkAndADetachedHead(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.write(fix.root, "loose.txt", "x\n")
	fix.sync().want(t, 2, "uncommitted or untracked", "loose.txt")
	if err := os.Remove(filepath.Join(fix.root, "loose.txt")); err != nil {
		t.Fatal(err)
	}
	fix.write(fix.root, "a.txt", "edited\n")
	fix.sync().want(t, 2, "a.txt")
	fix.git(fix.root, "checkout", "-q", "--", "a.txt")
	fix.git(fix.root, "checkout", "-q", "--detach")
	fix.sync().want(t, 2, "detached")
}

func TestSyncRefusesRebaseAndChangesNothing(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.commitHere("mine", map[string]string{"c.txt": "mine\n"})
	fix.advanceMain("b.txt", "x\n")
	before := fix.git(fix.root, "rev-parse", "HEAD")
	fix.sync("--rebase").want(t, 2, "merges only; rebase with git yourself")
	if fix.git(fix.root, "rev-parse", "HEAD") != before {
		t.Fatal("HEAD moved")
	}
	if _, err := fix.gitMay(fix.root, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/main~1"); err == nil {
		t.Fatal("--rebase fetched before refusing")
	}
}

func TestSyncAnIgnoredFileTheDefaultBranchNowTracks(t *testing.T) {
	for _, mode := range []string{"merge", "fast-forward"} {
		t.Run(mode, func(t *testing.T) {
			fix := newFixture(t, "origin")
			fix.commitHere("ignore .env", map[string]string{".gitignore": ".env\n"})
			fix.git(fix.root, "push", "-q", "origin", "main")
			fix.git(fix.other, "pull", "-q", "origin", "main")
			if mode == "merge" {
				fix.git(fix.root, "checkout", "-q", "-b", "feature")
				fix.commitHere("mine", map[string]string{"c.txt": "mine\n"})
			}
			fix.write(fix.root, ".env", "TOKEN=mine\n")
			fix.write(fix.other, ".env", "TOKEN=theirs\n")
			fix.git(fix.other, "add", "-f", ".env")
			fix.git(fix.other, "commit", "-qm", "track .env")
			fix.git(fix.other, "push", "-q", "origin", "main")
			fix.sync().want(t, 1, `".env"`, "written over")
			if got := fix.read(".env"); got != "TOKEN=mine\n" {
				t.Fatalf(".env is %q", got)
			}
		})
	}
}

func TestSyncATrackedFileRenamedOntoAnIgnoredOne(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.commitHere("two files", map[string]string{".gitignore": "local.cfg\n", "shared.cfg": "shared\n"})
	fix.git(fix.root, "push", "-q", "origin", "main")
	fix.git(fix.other, "pull", "-q", "origin", "main")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.write(fix.root, "local.cfg", "mine\n")
	fix.git(fix.other, "mv", "shared.cfg", "local.cfg")
	fix.git(fix.other, "add", "-f", "local.cfg")
	fix.git(fix.other, "commit", "-qm", "rename onto the ignored name")
	fix.git(fix.other, "push", "-q", "origin", "main")
	fix.sync().want(t, 1, "local.cfg")
	if got := fix.read("local.cfg"); got != "mine\n" {
		t.Fatalf("local.cfg is %q", got)
	}
}

func TestSyncPushSendsThisBranchOnly(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "config", "push.default", "matching")
	fix.git(fix.root, "branch", "other-local")
	fix.git(fix.root, "push", "-q", "origin", "other-local")
	fix.git(fix.root, "checkout", "-q", "other-local")
	fix.commitHere("local only", map[string]string{"x.txt": "not to be pushed\n"})
	fix.git(fix.root, "checkout", "-q", "-b", "feature", "main")
	fix.sync("--push").want(t, 0, "pushed feature")
	fix.sync("--push").want(t, 0, "pushed feature") // with an upstream now
	if fix.git(fix.bare, "rev-parse", "other-local") == fix.git(fix.root, "rev-parse", "other-local") {
		t.Fatal("other-local was pushed")
	}
}

func TestSyncAMainThatMovedApartIs1(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.commitHere("local", map[string]string{"local.txt": "on main here\n"})
	fix.advanceMain("b.txt", "x\n")
	fix.sync().want(t, 1, "moved apart")
}

func TestSyncPushNeverSendsABranchToAnotherName(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "checkout", "-q", "-b", "feature", "--track", "origin/main")
	fix.commitHere("mine", map[string]string{"c.txt": "mine\n"})
	fix.advanceMain("b.txt", "x\n")
	before := fix.git(fix.bare, "rev-parse", "main")
	head := fix.git(fix.root, "rev-parse", "HEAD")
	fix.sync("--push").want(t, 2, "own name")
	if fix.git(fix.bare, "rev-parse", "main") != before {
		t.Fatal("main on the remote moved")
	}
	if fix.git(fix.root, "rev-parse", "HEAD") != head {
		t.Fatal("the refused request still merged")
	}
}

func TestSyncPushWithNoRemoteIsRefused(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "remote", "remove", "origin")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.sync("--push").want(t, 2, "there is no remote")
}

func TestSyncAnIgnoredFileWhereTheDefaultBranchPutsAFolder(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.commitHere("ignore local", map[string]string{".gitignore": "local\n"})
	fix.write(fix.root, "local", "my notes\n")
	fix.advanceMain("local/x", "theirs\n")
	fix.sync().want(t, 1, "local/x")
	if got := fix.read("local"); got != "my notes\n" {
		t.Fatalf("local is %q", got)
	}
}

func TestSyncATrackedFileMainTurnsIntoAFolderIsMerged(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.advanceMain("local", "a file\n")
	fix.git(fix.root, "pull", "-q", "origin", "main")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.git(fix.other, "rm", "-q", "local")
	fix.advanceMain("local/x", "now a folder\n")
	fix.sync().want(t, 0)
	if got := fix.read("local/x"); got != "now a folder\n" {
		t.Fatalf("local/x is %q", got)
	}
}

func TestSyncATrackedFolderMainTurnsIntoAFile(t *testing.T) {
	for _, ignoredInside := range []bool{false, true} {
		t.Run(map[bool]string{false: "merged", true: "an ignored file inside stops it"}[ignoredInside], func(t *testing.T) {
			fix := newFixture(t, "origin")
			fix.advanceMain("local/x", "a folder\n")
			fix.git(fix.root, "pull", "-q", "origin", "main")
			fix.git(fix.root, "checkout", "-q", "-b", "feature")
			fix.git(fix.other, "rm", "-q", "-r", "local")
			fix.advanceMain("local", "now a file\n")
			if !ignoredInside {
				fix.sync().want(t, 0)
				if got := fix.read("local"); got != "now a file\n" {
					t.Fatalf("local is %q", got)
				}
				return
			}
			fix.exclude("local/.env")
			fix.write(fix.root, "local/.env", "PRIVATE\n")
			fix.sync().want(t, 1, "local/.env")
			if got := fix.read("local/.env"); got != "PRIVATE\n" {
				t.Fatalf("local/.env is %q", got)
			}
		})
	}
}

func TestSyncANameGitWouldQuoteIsFound(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	name := "local\tnotes"
	fix.exclude("local?notes")
	fix.write(fix.root, name, "mine\n")
	fix.advanceMain(name, "theirs\n")
	fix.sync().want(t, 1, `"local\tnotes"`)
	if got := fix.read(name); got != "mine\n" {
		t.Fatalf("%q is %q", name, got)
	}
}

func TestSyncANameBeginningWithASpaceOrATabIsKeptInEveryMode(t *testing.T) {
	for _, name := range []string{" .env", "\t.env"} {
		for _, mode := range []string{"merge", "fast-forward"} {
			t.Run(mode+" "+strings.ReplaceAll(name, "\t", "tab"), func(t *testing.T) {
				fix := newFixture(t, "origin")
				if mode == "merge" {
					fix.git(fix.root, "checkout", "-q", "-b", "feature")
					fix.commitHere("mine", map[string]string{"c.txt": "mine\n"})
				}
				fix.exclude("?.env")
				fix.write(fix.root, name, "MINE\n")
				fix.advanceMain(name, "THEIRS\n")
				fix.sync().want(t, 1, "written over")
				if got := fix.read(name); got != "MINE\n" {
					t.Fatalf("%q is %q", name, got)
				}
			})
		}
	}
}

func TestSyncAFolderRenamedOnMainTakesANewFileOntoAnIgnoredOne(t *testing.T) {
	// main renames `a` to `b`; this branch adds `a/new`; the merge puts it at `b/new`, a path
	// neither end has, where an ignored file of this checkout is (git 2.54 writes over it, even
	// with --no-overwrite-ignore).
	fix := newFixture(t, "origin")
	fix.advanceMain("a/one", "1\n")
	fix.git(fix.root, "pull", "-q", "origin", "main")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.commitHere("a/new", map[string]string{"a/new": "mine\n"})
	fix.git(fix.other, "mv", "a", "b")
	fix.git(fix.other, "commit", "-qm", "rename a to b")
	fix.git(fix.other, "push", "-q", "origin", "main")
	fix.exclude("b/new")
	fix.write(fix.root, "b/new", "PRIVATE\n")
	fix.sync().want(t, 1, "b/new")
	if got := fix.read("b/new"); got != "PRIVATE\n" {
		t.Fatalf("b/new is %q", got)
	}
}

// moveBetweenCheckAndMerge makes move run where another worktree's fetch could land: after the
// check of ignored files, before the merge.
func moveBetweenCheckAndMerge(t *testing.T, move func()) {
	t.Helper()
	saved := afterCheck
	afterCheck = move
	t.Cleanup(func() { afterCheck = saved })
}

func TestSyncMergesTheCommitItCheckedWhenAFetchMovesTheRefMeanwhile(t *testing.T) {
	// A fetch elsewhere moves origin/main after the check, to
	// a commit that tracks the ignored .env. The merge brings in the commit checked, not the new
	// one, so .env is not written over.
	fix := newFixture(t, "origin")
	fix.commitHere("ignore .env", map[string]string{".gitignore": ".env\n"})
	fix.git(fix.root, "push", "-q", "origin", "main")
	fix.git(fix.other, "pull", "-q", "origin", "main")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.commitHere("mine", map[string]string{"c.txt": "mine\n"})
	fix.write(fix.root, ".env", "TOKEN=mine\n")
	fix.advanceMain("b.txt", "checked\n")
	checked := fix.git(fix.bare, "rev-parse", "main")
	moveBetweenCheckAndMerge(t, func() {
		fix.write(fix.other, ".env", "TOKEN=theirs\n")
		fix.git(fix.other, "add", "-f", ".env")
		fix.git(fix.other, "commit", "-qm", "track .env")
		fix.git(fix.other, "push", "-q", "origin", "main")
		fix.git(fix.root, "fetch", "-q", "origin")
	})
	fix.sync().want(t, 0, "merged refs/remotes/origin/main into feature (1 commits)")
	if got := fix.read(".env"); got != "TOKEN=mine\n" {
		t.Fatalf(".env is %q", got)
	}
	if got := fix.git(fix.root, "rev-parse", "HEAD^2"); got != checked {
		t.Fatalf("merged %s, want the commit checked %s", got, checked)
	}
	if got := fix.git(fix.root, "log", "-1", "--format=%s"); got != "Merge remote-tracking branch 'refs/remotes/origin/main' into feature" {
		t.Fatalf("message %q", got)
	}
}

func TestSyncStopsWhenHEADMovesBetweenCheckAndMerge(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.advanceMain("b.txt", "x\n")
	moveBetweenCheckAndMerge(t, func() { fix.commitHere("meanwhile", map[string]string{"d.txt": "d\n"}) })
	fix.sync().want(t, 1, "HEAD moved")
	if fix.exists("b.txt") {
		t.Fatal("merged although HEAD moved")
	}
}

func TestSyncAConflictedMergeStillChecksIgnoredFiles(t *testing.T) {
	// The merge has a conflict in a.txt and would also write .env: the check reads the conflicted
	// merge's tree, so .env stops it before git writes anything.
	fix := newFixture(t, "origin")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.commitHere("mine", map[string]string{"a.txt": "mine\n"})
	fix.exclude(".env")
	fix.write(fix.root, ".env", "MINE\n")
	fix.write(fix.other, "a.txt", "theirs\n")
	fix.write(fix.other, ".env", "THEIRS\n")
	fix.git(fix.other, "add", "-f", ".")
	fix.git(fix.other, "commit", "-qm", "both")
	fix.git(fix.other, "push", "-q", "origin", "main")
	fix.sync().want(t, 1, `".env"`)
	if fix.exists(".git/MERGE_HEAD") {
		t.Fatal("the merge was started")
	}
	if got := fix.read(".env"); got != "MINE\n" {
		t.Fatalf(".env is %q", got)
	}
}

func TestSyncHiddenEditsStopIt(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.write(fix.root, "a.txt", "hidden edit\n")
	fix.git(fix.root, "update-index", "--assume-unchanged", "a.txt")
	fix.advanceMain("a.txt", "theirs\n")
	fix.sync().want(t, 2, "assume-unchanged")
	if got := fix.read("a.txt"); got != "hidden edit\n" {
		t.Fatalf("a.txt is %q", got)
	}
}

func TestSyncWithARemoteNotNamedOrigin(t *testing.T) {
	fix := newFixture(t, "octo-org.github.com")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.advanceMain("b.txt", "x\n")
	fix.sync().want(t, 0, "octo-org.github.com/main")
	if !fix.exists("b.txt") {
		t.Fatal("b.txt not merged")
	}
}

func TestSyncWithSeveralRemotesTheOneMainTracks(t *testing.T) {
	fix := newFixture(t, "upstream")
	fix.git(fix.root, "remote", "add", "mirror", fix.other)
	fix.advanceMain("b.txt", "x\n")
	fix.sync().want(t, 0, "upstream/main")
	fix.git(fix.root, "config", "--unset", "branch.main.remote")
	fix.sync().want(t, 2, "cannot tell which remote")
}

func TestSyncWithNoRemoteMergesTheLocalMain(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "remote", "remove", "origin")
	fix.git(fix.root, "checkout", "-q", "-b", "feature")
	fix.git(fix.root, "checkout", "-q", "main")
	fix.commitHere("on main", map[string]string{"m.txt": "m\n"})
	fix.git(fix.root, "checkout", "-q", "feature")
	fix.sync().want(t, 0, "no remote: nothing fetched", "merged refs/heads/main into feature")
}

func TestSyncInAWorktreeFetchesFromTheMainCheckout(t *testing.T) {
	fix := newFixture(t, "origin")
	tree := filepath.Join(fix.base, "tree")
	fix.git(fix.root, "worktree", "add", "-q", "-b", "feature", tree)
	fix.advanceMain("b.txt", "x\n")
	var stdout, stderr bytes.Buffer
	if code := runSync([]string{tree}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(tree, "b.txt")); err != nil {
		t.Fatal("b.txt not merged in the worktree")
	}
}

func TestSyncOutsideARepositoryAndBadArguments(t *testing.T) {
	fix := newFixture(t, "origin")
	var stdout, stderr bytes.Buffer
	if code := runSync([]string{fix.base}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("outside a repository: exit %d", code)
	}
	if code := runSync([]string{"a", "b"}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("two folders: exit %d", code)
	}
	if code := runSync([]string{"--frobnicate"}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("an unknown option: exit %d", code)
	}
	if code := runSync([]string{"--help"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("--help: exit %d", code)
	}
}

func TestStartAWorktreeOnTheFetchedDefaultBranch(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.advanceMain("b.txt", "from elsewhere\n")
	got := fix.start("feat/new-thing")
	path := filepath.Join(fix.root+"-worktrees", "feat-new-thing") // .claude/worktrees is not ignored here
	got.want(t, 0, "cd "+path)
	if _, err := os.Stat(filepath.Join(path, "b.txt")); err != nil {
		t.Fatal("not on the fetched main")
	}
	if branch := fix.git(path, "symbolic-ref", "--short", "HEAD"); branch != "feat/new-thing" {
		t.Fatalf("on %q", branch)
	}
	if upstream, err := fix.gitMay(path, "rev-parse", "--abbrev-ref", "@{u}"); err == nil {
		t.Fatalf("an upstream %q", upstream)
	}
}

func TestStartInsideDotClaudeWhenGitIgnoresIt(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.commitHere("ignore", map[string]string{".gitignore": ".claude/\n"})
	fix.git(fix.root, "push", "-q", "origin", "main")
	fix.start("x", "--no-fetch").want(t, 0)
	if info, err := os.Stat(filepath.Join(fix.root, ".claude", "worktrees", "x")); err != nil || !info.IsDir() {
		t.Fatal("no worktree under .claude/worktrees")
	}
}

func TestStartFromAWorktreeUsesTheMainCheckout(t *testing.T) {
	fix := newFixture(t, "origin")
	tree := filepath.Join(fix.base, "tree")
	fix.git(fix.root, "worktree", "add", "-q", "-b", "feature", tree)
	t.Chdir(tree)
	fix.start("y", "--no-fetch").want(t, 0, filepath.Join(fix.root+"-worktrees", "y"))
}

func TestStartRefusals(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "branch", "taken")
	cases := []struct {
		name string
		args []string
		says string
	}{
		{name: "a taken name", args: []string{"taken"}, says: "jst tidy --branch taken"},
		{name: "a bad name", args: []string{"bad..name"}, says: "not a branch name"},
		{name: "a name like an option", args: []string{"--", "-x"}, says: "not a branch name"},
		{name: "a taken folder", args: []string{"ok", "--path", fix.other}, says: "already there"},
		{name: "an unknown --from", args: []string{"ok2", "--from", "no-such-ref"}, says: "not a commit"},
		{name: "a --from like an option", args: []string{"ok3", "--from=--all"}, says: "not a commit"},
		{name: "no name", args: nil, says: "one branch name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fix.start(append([]string{"--no-fetch"}, c.args...)...).want(t, 2, c.says)
		})
	}
}

func TestStartNoDefaultBranchOnTheRemoteIsSaidNotGuessed(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.git(fix.root, "update-ref", "-d", "refs/remotes/origin/main")
	_, _ = fix.gitMay(fix.root, "remote", "set-head", "origin", "-d")
	fix.start("x", "--no-fetch").want(t, 2, "give --from")
	fix.start("x", "--no-fetch", "--from", "main").want(t, 0)
}

func TestStartWithARemoteNotNamedOrigin(t *testing.T) {
	fix := newFixture(t, "octo-org.github.com")
	fix.advanceMain("b.txt", "x\n")
	fix.start("z").want(t, 0, "refs/remotes/octo-org.github.com/main")
	if _, err := os.Stat(filepath.Join(fix.root+"-worktrees", "z", "b.txt")); err != nil {
		t.Fatal("not on the fetched main")
	}
}

func TestStartTwiceFromTheCommandLine(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.start("cli-made", "--no-fetch").want(t, 0)
	fix.start("cli-made", "--no-fetch").want(t, 2, "jst tidy --branch cli-made")
}

func TestStartARelativePath(t *testing.T) {
	fix := newFixture(t, "origin")
	fix.start("rel", "--no-fetch", "--path", "../made-here").want(t, 0, filepath.Join(fix.base, "made-here"))
}
