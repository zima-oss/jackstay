package discard

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/testrepo"
	"github.com/zima-oss/jackstay/internal/trash"
)

type world struct {
	t      *testing.T
	delete bool // trash.keep off: what is dropped is deleted and logged
	repo   *testrepo.Repo
	trash  string
	out    bytes.Buffer
	errors bytes.Buffer
}

func newWorld(t *testing.T, files map[string]string) *world {
	repo := testrepo.New(t, files)
	return &world{t: t, repo: repo, trash: filepath.Join(repo.Home, ".trash")}
}

func (w *world) discard(working string, args ...string) int {
	w.out.Reset()
	w.errors.Reset()
	run := Run{Working: working, Trash: w.trash, Project: "p", Session: "s", KeepDays: 7, Keep: !w.delete, Now: time.Now, Out: &w.out,
		Errors: &w.errors}
	return run.Discard(context.Background(), args)
}

func (w *world) read(name string) string {
	data, err := os.ReadFile(filepath.Join(w.repo.Root, name))
	if err != nil {
		return "<missing>"
	}
	return string(data)
}

func (w *world) status() string { return w.repo.Git("status", "--porcelain", "--untracked-files=all") }

// kept is the content of each entry's item, by kind.
func (w *world) kept() map[string][]string {
	found := map[string][]string{}
	for _, entry := range trash.Entries(w.trash) {
		data, err := os.ReadFile(filepath.Join(w.trash, entry.ID, "item", filepath.Base(entry.Path)))
		if err != nil {
			target, _ := os.Readlink(filepath.Join(w.trash, entry.ID, "item", filepath.Base(entry.Path)))
			data = []byte("link:" + target)
		}
		found[entry.Kind] = append(found[entry.Kind], string(data))
	}
	return found
}

func (w *world) restore(args ...string) int {
	return trash.Command(args, nil, &w.out, &w.errors)
}

func TestAnEditIsKeptAndTheFileIsBackAtHead(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	w.repo.Write(map[string]string{"a.txt": "mine\n"})
	if code := w.discard(w.repo.Root, "a.txt"); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	if w.read("a.txt") != "head\n" || w.status() != "" {
		t.Fatalf("file %q status %q", w.read("a.txt"), w.status())
	}
	if kept := w.kept(); len(kept["file"]) != 1 || kept["file"][0] != "mine\n" {
		t.Fatalf("kept %v", kept)
	}
	if !strings.Contains(w.out.String(), "jst trash restore") {
		t.Fatalf("no way back said: %s", w.out.String())
	}
	id := trash.Entries(w.trash)[0].ID
	if code := w.restore("restore", id); code != 0 {
		t.Fatalf("restore over HEAD's version: %d %s", code, w.errors.String())
	}
	if w.read("a.txt") != "mine\n" {
		t.Fatalf("restored %q", w.read("a.txt"))
	}
}

func TestStagedAndUnstagedEditsAreBothKept(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	w.repo.Write(map[string]string{"a.txt": "staged\n"})
	w.repo.Git("add", "a.txt")
	w.repo.Write(map[string]string{"a.txt": "working\n"})
	if code := w.discard(w.repo.Root, "a.txt"); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	kept := w.kept()
	if len(kept["file"]) != 1 || kept["file"][0] != "working\n" || len(kept["staged"]) != 1 || kept["staged"][0] != "staged\n" {
		t.Fatalf("kept %v", kept)
	}
	if w.read("a.txt") != "head\n" || w.status() != "" {
		t.Fatalf("file %q status %q", w.read("a.txt"), w.status())
	}
}

func TestAStagedChangeWithTheSameFileOnDiskIsKeptOnce(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	w.repo.Write(map[string]string{"a.txt": "staged\n"})
	w.repo.Git("add", "a.txt")
	if code := w.discard(w.repo.Root, "a.txt"); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	if kept := w.kept(); len(kept["file"]) != 1 || len(kept["staged"]) != 0 {
		t.Fatalf("kept %v", kept)
	}
	if w.status() != "" {
		t.Fatalf("status %q", w.status())
	}
}

func TestANewStagedFileIsGoneFromDiskAndIndexAndKept(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	w.repo.Write(map[string]string{"new.txt": "new\n"})
	w.repo.Git("add", "new.txt")
	if code := w.discard(w.repo.Root, "new.txt"); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	if w.read("new.txt") != "<missing>" || w.status() != "" {
		t.Fatalf("file %q status %q", w.read("new.txt"), w.status())
	}
	if kept := w.kept(); len(kept["file"]) != 1 || kept["file"][0] != "new\n" {
		t.Fatalf("kept %v", kept)
	}
	if code := w.restore("restore", trash.Entries(w.trash)[0].ID); code != 0 || w.read("new.txt") != "new\n" {
		t.Fatalf("restore %d %q", code, w.read("new.txt"))
	}
}

func TestAStagedRenameKeepsTheNewNameAndBringsBackTheOld(t *testing.T) {
	w := newWorld(t, map[string]string{"old.txt": "head\n"})
	w.repo.Git("mv", "old.txt", "new.txt")
	if code := w.discard(w.repo.Root, "."); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	if w.read("old.txt") != "head\n" || w.read("new.txt") != "<missing>" || w.status() != "" {
		t.Fatalf("old %q new %q status %q", w.read("old.txt"), w.read("new.txt"), w.status())
	}
	if kept := w.kept(); len(kept["file"]) != 1 {
		t.Fatalf("kept %v", kept)
	}
}

func TestAFileMadeAgainAfterAStagedDeletionIsKept(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		w := newWorld(t, map[string]string{"a.txt": "head\n"})
		w.repo.Git("rm", "--cached", "-q", "a.txt")
		if ignored {
			w.repo.Write(map[string]string{".git/info/exclude": "a.txt\n"})
		}
		w.repo.Write(map[string]string{"a.txt": "made again\n"})
		if code := w.discard(w.repo.Root, "a.txt"); code != 0 {
			t.Fatalf("exit %d: %s", code, w.errors.String())
		}
		if kept := w.kept(); len(kept["file"]) != 1 || kept["file"][0] != "made again\n" {
			t.Fatalf("ignored %v: kept %v", ignored, kept)
		}
		if w.read("a.txt") != "head\n" {
			t.Fatalf("file %q", w.read("a.txt"))
		}
	}
}

func TestAModeOnlyChangeIsKeptAndRestoredWithItsMode(t *testing.T) {
	w := newWorld(t, map[string]string{"run.sh": "echo\n"})
	if err := os.Chmod(filepath.Join(w.repo.Root, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := w.discard(w.repo.Root, "run.sh"); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	info, _ := os.Stat(filepath.Join(w.repo.Root, "run.sh"))
	if info.Mode().Perm()&0o111 != 0 || w.status() != "" {
		t.Fatalf("mode %v status %q", info.Mode(), w.status())
	}
	if kept := w.kept(); len(kept["file"]) != 1 {
		t.Fatalf("kept %v", kept)
	}
	// A later chmod changes what discard wrote: the restore refuses to replace it.
	if err := os.Chmod(filepath.Join(w.repo.Root, "run.sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code := w.restore("restore", trash.Entries(w.trash)[0].ID); code != 1 {
		t.Fatalf("a changed mode was replaced: %d", code)
	}
}

func TestALinkIsComparedAsALink(t *testing.T) {
	w := newWorld(t, map[string]string{"target.txt": "t\n"})
	if err := os.Symlink("target.txt", filepath.Join(w.repo.Root, "link")); err != nil {
		t.Fatal(err)
	}
	w.repo.Git("add", "link")
	w.repo.Git("commit", "-qm", "link")
	if err := os.Remove(filepath.Join(w.repo.Root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", filepath.Join(w.repo.Root, "link")); err != nil {
		t.Fatal(err)
	}
	if code := w.discard(w.repo.Root, "link"); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	if target, _ := os.Readlink(filepath.Join(w.repo.Root, "link")); target != "target.txt" {
		t.Fatalf("link %q", target)
	}
	if kept := w.kept(); len(kept["file"]) != 0 && kept["link"] == nil || len(kept["link"]) != 1 || kept["link"][0] != "link:elsewhere" {
		t.Fatalf("kept %v", kept)
	}
}

func TestADeletedFileIsWrittenBackWithNothingKept(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	if err := os.Remove(filepath.Join(w.repo.Root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if code := w.discard(w.repo.Root, "a.txt"); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	if w.read("a.txt") != "head\n" || len(trash.Entries(w.trash)) != 0 {
		t.Fatalf("file %q entries %v", w.read("a.txt"), trash.Entries(w.trash))
	}
}

func TestAFolderTakesEveryChangeAndLeavesUntrackedFiles(t *testing.T) {
	w := newWorld(t, map[string]string{"src/a.txt": "a\n", "src/b.txt": "b\n", "other.txt": "o\n"})
	w.repo.Write(map[string]string{"src/a.txt": "a2\n", "src/b.txt": "b2\n", "src/new.txt": "untracked\n", "other.txt": "o2\n"})
	if code := w.discard(filepath.Join(w.repo.Root, "src"), "."); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	if w.read("src/a.txt") != "a\n" || w.read("src/b.txt") != "b\n" || w.read("other.txt") != "o2\n" ||
		w.read("src/new.txt") != "untracked\n" {
		t.Fatalf("a %q b %q other %q new %q", w.read("src/a.txt"), w.read("src/b.txt"), w.read("other.txt"), w.read("src/new.txt"))
	}
	if !strings.Contains(w.out.String(), "src/new.txt is untracked") {
		t.Fatalf("the untracked file is not named: %s", w.out.String())
	}
}

func TestRefusedBeforeAnythingIsChanged(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n", "b.txt": "b\n"})
	w.repo.Write(map[string]string{"a.txt": "mine\n"})
	outside := t.TempDir()
	if code := w.discard(w.repo.Root, "a.txt", outside); code != 2 {
		t.Fatalf("a path outside the tree: %d", code)
	}
	if code := w.discard(outside, "x"); code != 2 {
		t.Fatalf("outside a work tree: %d", code)
	}
	if code := w.discard(w.repo.Root); code != 2 {
		t.Fatalf("no path: %d", code)
	}
	if code := w.discard(w.repo.Root, ".git/config"); code != 2 {
		t.Fatalf("git's own folder: %d", code)
	}
	if w.read("a.txt") != "mine\n" || len(trash.Entries(w.trash)) != 0 {
		t.Fatalf("something changed: %q %v", w.read("a.txt"), trash.Entries(w.trash))
	}
}

func TestAConflictIsRefused(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "base\n"})
	w.repo.Git("checkout", "-q", "-b", "side")
	w.repo.Commit("side", map[string]string{"a.txt": "side\n"})
	w.repo.Git("checkout", "-q", "main")
	w.repo.Commit("main", map[string]string{"a.txt": "main\n"})
	command := []string{"merge", "-q", "side"}
	_ = runQuiet(w.repo.Root, command...)
	before := w.read("a.txt")
	if code := w.discard(w.repo.Root, "a.txt"); code != 2 || !strings.Contains(w.errors.String(), "merge conflict") {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	if w.read("a.txt") != before {
		t.Fatal("the conflicted file changed")
	}
}

func TestNothingUncommittedIsSaid(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	if code := w.discard(w.repo.Root, "a.txt"); code != 0 || !strings.Contains(w.out.String(), "nothing uncommitted in a.txt") {
		t.Fatalf("exit %d: %s", code, w.out.String())
	}
}

func TestALossyCleanFilterCannotHideADifference(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "code\n", ".gitattributes": "a.txt filter=strip\n"})
	// The filter drops output lines: the working file below cleans to HEAD's content, so a
	// filtered comparison would call it "the same as HEAD" and lose the outputs.
	w.repo.Git("config", "filter.strip.clean", "sed '/^#out/d'")
	w.repo.Write(map[string]string{"a.txt": "code2\n"})
	w.repo.Git("add", "a.txt")
	w.repo.Write(map[string]string{"a.txt": "code\n#out results that matter\n"})
	if code := w.discard(w.repo.Root, "a.txt"); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	if kept := w.kept(); len(kept["file"]) != 1 || kept["file"][0] != "code\n#out results that matter\n" {
		t.Fatalf("kept %v (out %s)", kept, w.out.String())
	}
}

func TestARestoreOverAChangedFileIsRefused(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	w.repo.Write(map[string]string{"a.txt": "mine\n"})
	if code := w.discard(w.repo.Root, "a.txt"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	w.repo.Write(map[string]string{"a.txt": "edited after\n"})
	if code := w.restore("restore", trash.Entries(w.trash)[0].ID); code != 1 || w.read("a.txt") != "edited after\n" {
		t.Fatalf("restore %d %q", code, w.read("a.txt"))
	}
}

func runQuiet(folder string, args ...string) error {
	command := exec.Command("git", append([]string{"-C", folder}, args...)...)
	return command.Run()
}

func TestAPathThroughALinkedFolderIsRefused(t *testing.T) {
	w := newWorld(t, map[string]string{"src/a.txt": "head\n"})
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "a.txt"), []byte("someone else's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(w.repo.Root, "src")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(w.repo.Root, "src")); err != nil {
		t.Fatal(err)
	}
	if code := w.discard(w.repo.Root, "."); code != 2 || !strings.Contains(w.errors.String(), "through the link src") {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	if data, _ := os.ReadFile(filepath.Join(outside, "a.txt")); string(data) != "someone else's\n" || len(trash.Entries(w.trash)) != 0 {
		t.Fatalf("outside %q entries %v", data, trash.Entries(w.trash))
	}
}

func TestTheRootFolderIsNotTheTree(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	w.repo.Write(map[string]string{"a.txt": "mine\n"})
	if code := w.discard(w.repo.Root, "/"); code != 2 || w.read("a.txt") != "mine\n" {
		t.Fatalf("exit %d, file %q", code, w.read("a.txt"))
	}
}

func TestAWorkingFileChangedAfterPlanningIsLeftAlone(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	w.repo.Write(map[string]string{"a.txt": "staged\n"})
	w.repo.Git("add", "a.txt")
	w.repo.Write(map[string]string{"a.txt": "head\n"})
	run := Run{Working: w.repo.Root, Trash: w.trash, Project: "p", Session: "s", KeepDays: 7, Keep: true, Now: time.Now,
		Out: &w.out, Errors: &w.errors, repo: repository{top: w.repo.Root}}
	ctx := context.Background()
	planned, _, problem := run.plan(ctx, []string{"a.txt"})
	if problem != "" || len(planned) != 1 {
		t.Fatalf("plan = %+v, %q, want one change", planned, problem)
	}
	w.repo.Write(map[string]string{"a.txt": "another session\n"})
	if _, err := run.putBack(ctx, planned[0]); err == nil {
		t.Fatal("discard accepted a file that changed after its plan")
	}
	if disk := w.read("a.txt"); disk != "another session\n" {
		t.Fatalf("working file = %q, want the concurrent edit", disk)
	}
	if staged := w.repo.Git("show", ":a.txt"); staged != "staged" {
		t.Fatalf("staged file = %q, want the existing staged edit", staged)
	}
}

func TestAWorkingFileChangedDuringTheMoveKeepsBothVersions(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	w.repo.Write(map[string]string{"a.txt": "staged\n"})
	w.repo.Git("add", "a.txt")
	run := Run{Working: w.repo.Root, Trash: w.trash, Project: "p", Session: "s", KeepDays: 0, Keep: true,
		Now: func() time.Time {
			w.repo.Write(map[string]string{"a.txt": "another session\n"})
			return time.Now()
		}, Out: &w.out, Errors: &w.errors, repo: repository{top: w.repo.Root}}
	ctx := context.Background()
	planned, _, problem := run.plan(ctx, []string{"a.txt"})
	if problem != "" || len(planned) != 1 {
		t.Fatalf("plan = %+v, %q, want one change", planned, problem)
	}
	if _, err := run.putBack(ctx, planned[0]); err == nil || !strings.Contains(err.Error(), "changed during the move") {
		t.Fatalf("discard error = %v, want a refused move with a recovery path", err)
	}
	if staged := w.repo.Git("show", ":a.txt"); staged != "staged" {
		t.Fatalf("index = %q, want the previous staged version", staged)
	}
	if removed := trash.Prune(w.trash, time.Now().Add(time.Hour)); removed != 0 {
		t.Fatalf("pruned %d entries, want the concurrent edit held for recovery", removed)
	}
	if kept := w.kept()["file"]; len(kept) != 1 || kept[0] != "another session\n" {
		t.Fatalf("trash = %q, want the concurrent edit", kept)
	}
}

// With trash.keep off, the edit and the staged version are deleted once the file is back, and each
// deletion is logged with its size and digest.
func TestWithoutKeepWhatIsDroppedIsDeletedAndLogged(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	w.delete = true
	w.repo.Write(map[string]string{"a.txt": "staged\n"})
	w.repo.Git("add", "a.txt")
	w.repo.Write(map[string]string{"a.txt": "mine\n"})
	if code := w.discard(w.repo.Root, "a.txt"); code != 0 {
		t.Fatalf("exit %d: %s", code, w.errors.String())
	}
	if w.read("a.txt") != "head\n" || w.status() != "" {
		t.Fatalf("file %q status %q", w.read("a.txt"), w.status())
	}
	if entries := trash.Entries(w.trash); len(entries) != 0 {
		t.Fatalf("kept %+v", entries)
	}
	deletions := trash.Deletions(w.trash)
	kinds := map[string]trash.Deletion{}
	for _, deletion := range deletions {
		kinds[deletion.Kind] = deletion
	}
	if len(deletions) != 2 || kinds["file"].Size != 5 || kinds["staged"].Size != 7 || kinds["file"].Command != "jst discard" {
		t.Fatalf("deletions %+v", deletions)
	}
	if !strings.Contains(w.out.String(), "what was on disk deleted") || strings.Contains(w.out.String(), "trash restore") {
		t.Fatalf("out %s", w.out.String())
	}
}
