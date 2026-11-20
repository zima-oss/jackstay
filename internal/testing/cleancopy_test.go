package scopes

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/proctree"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// cleanSection has steps that write where they ran and what they saw into $SIDE.
func cleanSection() map[string]any {
	section := gateSection()
	section = with(section, "scopes", "x-clean", map[string]any{"steps": argv("seen", "middle")})
	return with(section, "steps", "seen", map[string]any{"run": argv("sh", "-c",
		`pwd > "$SIDE/where"; cat Sources/a.swift > "$SIDE/content"; git rev-parse origin/main > "$SIDE/origin" 2>/dev/null; `+
			`git rev-parse HEAD > "$SIDE/head"; echo run >> "$SIDE/seen"`)})
}

func newCleanWorld(t *testing.T) *world {
	w := newWorld(t, cleanSection(), nil)
	w.commit()
	return w
}

func (w *world) copies() []string {
	w.t.Helper()
	folder, err := homedir.ProjectPath(w.projectName(), "clean-copies")
	if err != nil {
		w.t.Fatal(err)
	}
	entries, _ := os.ReadDir(folder)
	var found []string
	for _, entry := range entries {
		found = append(found, filepath.Join(folder, entry.Name()))
	}
	return found
}

func (w *world) projectName() string {
	root, loaded, err := config.Load(w.repo.Root, needs)
	if err != nil {
		w.t.Fatal(err)
	}
	return config.ProjectName(root, loaded, w.repo.Root)
}

func (w *world) seen(name string) string {
	data, _ := os.ReadFile(w.at(name))
	return strings.TrimSpace(string(data))
}

func TestTheScopeRunsOnACleanCopyOfTheCommitAndTheCopyGoes(t *testing.T) {
	w := newCleanWorld(t)
	committed := w.repo.Git("rev-parse", "HEAD")
	w.repo.Write(map[string]string{"Sources/a.swift": "let a = 2 // not committed\n"})
	w.expectStatus(w.jst("x-clean", "--commit", "HEAD"), 0)
	if w.seen("content") != "let a = 1" || w.seen("head") != committed {
		t.Fatalf("the copy saw %q at %q", w.seen("content"), w.seen("head"))
	}
	if !strings.Contains(w.seen("where"), "clean-copies") {
		t.Fatalf("ran in %q", w.seen("where"))
	}
	if copies := w.copies(); len(copies) != 0 {
		t.Fatalf("copies left: %v", copies)
	}
	if entries, _ := os.ReadDir(w.tmp); len(entries) != 0 {
		t.Fatalf("left in TMPDIR: %v", entries)
	}
}

func TestTheCopyGoesAfterAFailure(t *testing.T) {
	w := newCleanWorld(t)
	w.flag("middle-fails", true)
	w.expectStatus(w.jst("x-clean", "--commit", "HEAD"), 1)
	w.expectLast("failed at middle")
	if copies := w.copies(); len(copies) != 0 {
		t.Fatalf("copies left: %v", copies)
	}
}

func TestTheCopyGoesBeforeJstEndsByAStop(t *testing.T) {
	w := newCleanWorld(t)
	var copiesAtEnd []string
	w.overrides["middle"] = func(launch) *launched {
		return &launched{outcome: supervise.Outcome{PID: 80, Signal: syscall.SIGTERM, StoppedBy: syscall.SIGTERM}}
	}
	runner := w.tester()
	ending := runner.endBy
	runner.endBy = func(sig syscall.Signal) int {
		copiesAtEnd = w.copies()
		return ending(sig)
	}
	w.expectStatus(runner.scope([]string{"x-clean", "--commit", "HEAD"}), 143)
	if w.ended != syscall.SIGTERM || len(copiesAtEnd) != 0 {
		t.Fatalf("ended by %v with copies %v", w.ended, copiesAtEnd)
	}
}

func TestAStopWhileTheCopyIsMadeRemovesIt(t *testing.T) {
	w := newCleanWorld(t)
	w.firstStop = func(*supervise.Signals) (syscall.Signal, time.Time, bool) { return syscall.SIGINT, time.Now(), true }
	w.jst("x-clean", "--commit", "HEAD")
	if w.ended != syscall.SIGINT || len(w.copies()) != 0 || w.counter("seen") != 0 {
		t.Fatalf("ended %v copies %v seen %d", w.ended, w.copies(), w.counter("seen"))
	}
}

func TestTheCopyHasTheCheckoutsRefs(t *testing.T) {
	w := newCleanWorld(t)
	first := w.repo.Git("rev-list", "--max-parents=0", "HEAD")
	w.repo.Git("update-ref", "refs/remotes/origin/main", first) // origin/main, local main and HEAD all differ
	w.expectStatus(w.jst("x-clean", "--commit", "HEAD"), 0)
	if w.seen("origin") != first {
		t.Fatalf("origin/main in the copy is %q, not the checkout's %q", w.seen("origin"), first)
	}
}

func TestTheCopyKeepsTheDefaultRemoteRef(t *testing.T) {
	w := newCleanWorld(t)
	w.repo.Git("update-ref", "refs/remotes/origin/main", w.repo.Git("rev-parse", "main"))
	w.repo.Git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	runner := w.tester()
	made, inside, err := runner.makeCopy(options{commit: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	defer made.Remove()
	if got := w.repo.GitIn(inside, "symbolic-ref", "refs/remotes/origin/HEAD"); got != "refs/remotes/origin/main" {
		t.Fatalf("copy default remote ref = %q", got)
	}
}

func TestACommitOnNoRefChecksOutThroughTheAlternates(t *testing.T) {
	w := newCleanWorld(t)
	w.repo.Write(map[string]string{"Sources/a.swift": "let a = 3\n"})
	w.repo.Git("add", "-A")
	loose := w.repo.Git("commit-tree", w.repo.Git("write-tree"), "-p", "HEAD", "-m", "on no ref")
	w.expectStatus(w.jst("x-clean", "--commit", loose), 0)
	if w.seen("head") != loose || w.seen("content") != "let a = 3" {
		t.Fatalf("head %q content %q", w.seen("head"), w.seen("content"))
	}
}

func TestResumeAndANonCommitAreRefused(t *testing.T) {
	w := newCleanWorld(t)
	w.expectStatus(w.jst("x-clean", "--commit", "HEAD", "--resume"), 2)
	w.expectSaid("takes no --resume")
	w.expectStatus(w.jst("x-clean", "--commit", "no-such-thing"), 2)
	w.expectSaid("is not a commit here")
	w.expectStatus(w.jst("x-clean", "--commit", "--help"), 2)
	if len(w.copies()) != 0 {
		t.Fatalf("copies left: %v", w.copies())
	}
}

func TestTheCopyHasTheCheckoutsProjectName(t *testing.T) {
	w := newCleanWorld(t)
	// No `project` key, and a linked worktree of another folder name: the name comes from the
	// main checkout's folder, and the copy's own folder is named otherwise.
	worktree := filepath.Join(filepath.Dir(w.repo.Root), "other-name")
	w.repo.Git("worktree", "add", "-q", "--detach", worktree, "HEAD")
	runner := w.tester()
	runner.here = worktree
	for _, project := range []string{"", "chosen"} {
		made, inside, err := runner.makeCopy(options{commit: "HEAD", project: project})
		if err != nil {
			t.Fatal(err)
		}
		want := project
		if want == "" {
			root, loaded, _ := config.Load(worktree, needs)
			want = config.ProjectName(root, loaded, worktree)
		}
		root, loaded, _ := config.Load(inside, needs)
		if got := config.ProjectName(root, loaded, filepath.Join(inside, "Sources")); got != want {
			t.Fatalf("the copy's project is %q, the checkout's %q", got, want)
		}
		made.Remove()
	}
}

func TestCleanTestUsesCommittedConfigWhenLiveConfigIsUnavailable(t *testing.T) {
	for _, test := range []struct {
		name, live, project, want string
	}{
		{name: "invalid JSON", live: "{", want: "demo"},
		{name: "invalid test section", live: `{"jackstay_schema":3,"project":"live-name","test":true}`, want: "live-name"},
		{name: "missing config", want: "demo"},
		{name: "explicit project overrides invalid JSON", live: "{", project: "asked", want: "asked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := newCleanWorld(t)
			if test.live == "" {
				if err := os.Remove(filepath.Join(w.repo.Root, config.Name)); err != nil {
					t.Fatal(err)
				}
			} else {
				w.repo.Write(map[string]string{config.Name: test.live})
			}

			runner := w.tester()
			chosen := options{commit: "HEAD", project: test.project}
			made, inside, err := runner.makeCopy(chosen)
			if err != nil {
				t.Fatal(err)
			}
			defer made.Remove()
			runner.here = inside
			opened, err := runner.open(chosen)
			if err != nil {
				t.Fatal(err)
			}
			if opened.name != test.want || config.CopyProject(inside, inside) != test.want {
				t.Fatalf("opened project %q, marked %q; want %q", opened.name, config.CopyProject(inside, inside), test.want)
			}
		})
	}
}

func TestCleanCopyFindsTheNearestCommittedProject(t *testing.T) {
	w := newCleanWorld(t)
	w.repo.Commit("add a project below the root", map[string]string{
		"..nested/jackstay.json": `{"jackstay_schema":3,"project":"nested"}`,
		"..nested/deep/marker":   "checked-in",
	})
	w.repo.Write(map[string]string{"jackstay.json": "{", "..nested/jackstay.json": "{"})
	runner := w.tester()
	runner.here = filepath.Join(w.repo.Root, "..nested", "deep")
	made, inside, err := runner.makeCopy(options{commit: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	defer made.Remove()
	if want := filepath.Join(made.Tree, "..nested", "deep"); inside != want {
		t.Fatalf("copy working folder %s, want %s", inside, want)
	}
	if name := config.CopyProject("", inside); name != "nested" {
		t.Fatalf("the copy is marked as %q, want nested", name)
	}
}

func TestCleanScopeRunsAgainWithAReusableReceipt(t *testing.T) {
	w := newWorld(t, reuseSection(), reuseReceipts())
	w.commit()
	run := func(args ...string) int {
		runner := w.tester()
		// The world's launcher is a fake. Its result does not depend on access to the host table.
		runner.system = &proctree.System{Read: func() (map[int]procs.Process, error) { return nil, nil }}
		return runner.scope(args)
	}
	// Use Git's worktree mode on both sides before testing for a matching key.
	if err := os.Remove(filepath.Join(w.repo.Root, "Sources", "a.swift")); err != nil {
		t.Fatal(err)
	}
	w.repo.Git("checkout", "--", "Sources/a.swift")
	// A detached copy of work can reuse this key by the ordinary rules, because main
	// still points at the earlier fixture commit.
	w.expectStatus(run("full"), 0)
	w.expectStatus(run("full"), 0)
	w.expectLast("outcome=reused")
	w.expectStatus(run("full", "--commit", "HEAD"), 0)
	w.expectLast("outcome=passed")
	w.expectCounters("full", 2)
	if hit := w.lastRecord("would-hit"); hit["receipt"] != true || hit.Text("reason") != "--commit requests a fresh run" {
		t.Fatalf("the clean run did not bypass a matching receipt: %v", hit)
	}
}
