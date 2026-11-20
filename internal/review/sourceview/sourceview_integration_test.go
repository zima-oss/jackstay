//go:build integration

package sourceview

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/testrepo"
)

func TestProjectionUsesPinnedBlobsAndInertLinks(t *testing.T) {
	repo := testrepo.New(t, map[string]string{".gitattributes": "*.bin filter=sideeffect\n", "tracked.txt": "base\n", "remove.txt": "old\n", "rename-old.txt": "renamed content\n"})
	base := repo.Git("rev-parse", "HEAD")
	previous := repo.Commit("previous", map[string]string{"tracked.txt": "previous\n"})
	large := bytes.Repeat([]byte("\x00binary data\n"), 200000)
	if err := os.WriteFile(filepath.Join(repo.Root, "large.bin"), large, 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo.Root, "link")); err != nil {
		t.Fatal(err)
	}
	repo.Write(map[string]string{"tracked.txt": "pinned\n", "new.txt": "new\n"})
	if err := os.Remove(filepath.Join(repo.Root, "remove.txt")); err != nil {
		t.Fatal(err)
	}
	repo.Git("mv", "rename-old.txt", "rename-new.txt")
	head := repo.Commit("head", nil)
	filterMarker := filepath.Join(t.TempDir(), "filter-ran")
	repo.Git("config", "filter.sideeffect.smudge", "touch '"+filterMarker+"'; cat")
	repo.Git("config", "filter.sideeffect.clean", "cat")
	repo.Write(map[string]string{"tracked.txt": "dirty working tree\n", "untracked.txt": "untracked\n"})
	destination := filepath.Join(t.TempDir(), "projection")
	if err := Build(context.Background(), repo.Root, destination, Revisions{Base: base, Head: head, Previous: previous}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filterMarker); !os.IsNotExist(err) {
		t.Fatalf("checkout filter was invoked: %v", err)
	}
	for path, want := range map[string]string{
		"head/tracked.txt": "pinned\n", "base/tracked.txt": "base\n", "previous/tracked.txt": "previous\n",
		"head/link": outside, "head/new.txt": "new\n",
	} {
		got, err := os.ReadFile(filepath.Join(destination, path))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q, %v; want %q", path, got, err, want)
		}
	}
	got, err := os.ReadFile(filepath.Join(destination, "head/large.bin"))
	if err != nil || !bytes.Equal(got, large) {
		t.Fatalf("large binary blob: size %d, %v", len(got), err)
	}
	for _, path := range []string{"head/untracked.txt", "head/remove.txt", "head/.git", "base/link", "previous/new.txt"} {
		if _, err := os.Lstat(filepath.Join(destination, path)); !os.IsNotExist(err) {
			t.Fatalf("%s should not exist: %v", path, err)
		}
	}
	if info, err := os.Lstat(filepath.Join(destination, "head/link")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("link was not inert: %v, %v", info, err)
	}
	patch, err := os.ReadFile(filepath.Join(destination, "changes/base.patch"))
	if err != nil || !bytes.Contains(patch, []byte("+pinned")) || !bytes.Contains(patch, []byte("-base")) {
		t.Fatalf("base patch: %v, %v", string(patch), err)
	}
	patch, err = os.ReadFile(filepath.Join(destination, "changes/previous.patch"))
	if err != nil || !bytes.Contains(patch, []byte("-previous")) {
		t.Fatalf("previous patch: %v, %v", string(patch), err)
	}
	paths, err := os.ReadFile(filepath.Join(destination, "changes/base.paths"))
	if err != nil {
		t.Fatal(err)
	}
	found, deleted, renamed := false, false, false
	for _, line := range strings.Split(strings.TrimSpace(string(paths)), "\n") {
		var path changedPath
		if err := json.Unmarshal([]byte(line), &path); err != nil {
			t.Fatal(err)
		}
		if path.New == "tracked.txt" && path.Status == "M" {
			found = true
		}
		if path.Old == "remove.txt" && path.Status == "D" {
			deleted = true
		}
		if path.Old == "rename-old.txt" && path.New == "rename-new.txt" && strings.HasPrefix(path.Status, "R") {
			renamed = true
		}
	}
	if !found || !deleted || !renamed {
		t.Fatalf("changed paths omit modification, deletion or rename: %s", paths)
	}
}

func TestProjectionUsesTheObjectIDWithoutReplacementRefs(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"tracked.txt": "base\n"})
	base := repo.Git("rev-parse", "HEAD")
	head := repo.Commit("head", map[string]string{"tracked.txt": "pinned\n"})
	replacement := repo.Commit("replacement", map[string]string{"tracked.txt": "replacement\n"})
	repo.Git("replace", head, replacement)
	destination := filepath.Join(t.TempDir(), "projection")
	if err := Build(context.Background(), repo.Root, destination, Revisions{Base: base, Head: head}); err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(filepath.Join(destination, "head/tracked.txt"))
	if err != nil || string(file) != "pinned\n" {
		t.Fatalf("wrong pinned object: %q, %v", file, err)
	}
	patch, err := os.ReadFile(filepath.Join(destination, "changes/base.patch"))
	if err != nil || !bytes.Contains(patch, []byte("+pinned")) || bytes.Contains(patch, []byte("replacement")) {
		t.Fatalf("wrong pinned patch: %s, %v", patch, err)
	}
}

func TestProjectionRejectsUnsafeInputsAndCleansFailures(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"tracked.txt": "base\n"})
	head := repo.Git("rev-parse", "HEAD")
	for _, revisions := range []Revisions{
		{Base: "--danger", Head: head},
		{Base: head, Head: "HEAD"},
		{Base: head, Head: head, Previous: "deadbeef"},
	} {
		destination := filepath.Join(t.TempDir(), "invalid")
		if err := Build(context.Background(), repo.Root, destination, revisions); err == nil {
			t.Fatalf("accepted %+v", revisions)
		}
		if _, err := os.Stat(destination); !os.IsNotExist(err) {
			t.Fatalf("invalid input left projection: %v", err)
		}
	}
	missing := strings.Repeat("a", 40)
	destination := filepath.Join(t.TempDir(), "missing")
	if err := Build(context.Background(), repo.Root, destination, Revisions{Base: missing, Head: head}); err == nil {
		t.Fatal("accepted missing object")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("failed projection left behind: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	destination = filepath.Join(t.TempDir(), "canceled")
	if err := Build(canceled, repo.Root, destination, Revisions{Base: head, Head: head}); err == nil {
		t.Fatal("accepted canceled context")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("canceled projection left behind: %v", err)
	}
}

func TestProjectionSupportsEmptyTreeBase(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"tracked.txt": "first\n"})
	head := repo.Git("rev-parse", "HEAD")
	empty := repo.Git("hash-object", "-t", "tree", os.DevNull)
	destination := filepath.Join(t.TempDir(), "projection")
	if err := Build(context.Background(), repo.Root, destination, Revisions{Base: empty, Head: head}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadDir(filepath.Join(destination, "base"))
	if err != nil || len(contents) != 0 {
		t.Fatalf("empty base: %v, %v", contents, err)
	}
	patch, err := os.ReadFile(filepath.Join(destination, "changes/base.patch"))
	if err != nil || !bytes.Contains(patch, []byte("+first")) {
		t.Fatalf("first commit patch: %s, %v", patch, err)
	}
}

func TestPreviousMayEqualBase(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"tracked.txt": "base\n"})
	base := repo.Git("rev-parse", "HEAD")
	head := repo.Commit("head", map[string]string{"tracked.txt": "head\n"})
	destination := filepath.Join(t.TempDir(), "projection")
	if err := Build(context.Background(), repo.Root, destination, Revisions{Base: base, Head: head, Previous: base}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"patch", "paths"} {
		baseData, err := os.ReadFile(filepath.Join(destination, "changes/base."+suffix))
		if err != nil {
			t.Fatal(err)
		}
		previousData, err := os.ReadFile(filepath.Join(destination, "changes/previous."+suffix))
		if err != nil || !bytes.Equal(baseData, previousData) {
			t.Fatalf("previous %s differs from base: %v", suffix, err)
		}
	}
}

func TestPatchIgnoresUntrackedWorktreeAttributes(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"tracked.txt": "base\n"})
	base := repo.Git("rev-parse", "HEAD")
	head := repo.Commit("head", map[string]string{"tracked.txt": "pinned\n"})
	if err := os.WriteFile(filepath.Join(repo.Root, ".gitattributes"), []byte("*.txt -diff\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "projection")
	if err := Build(context.Background(), repo.Root, destination, Revisions{Base: base, Head: head}); err != nil {
		t.Fatal(err)
	}
	patch, err := os.ReadFile(filepath.Join(destination, "changes/base.patch"))
	if err != nil || !bytes.Contains(patch, []byte("+pinned")) || !bytes.Contains(patch, []byte("-base")) {
		t.Fatalf("worktree attributes affected pinned patch: %s, %v", patch, err)
	}
}

func TestThePatchesHoldNoEnvFileContents(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"app.go": "package app\n", ".env": "TOKEN=made-up-base\n", "svc/.env.production": "KEY=made-up-one\n"})
	base := repo.Git("rev-parse", "HEAD")
	repo.Write(map[string]string{"app.go": "package app // changed\n", ".env": "TOKEN=made-up-head\n", "svc/.env.production": "KEY=made-up-two\n"})
	head := repo.Commit("change", nil)
	destination := filepath.Join(t.TempDir(), "view")
	if err := Build(context.Background(), repo.Root, destination, Revisions{Base: base, Head: head}); err != nil {
		t.Fatal(err)
	}
	patch, err := os.ReadFile(filepath.Join(destination, "changes", "base.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(patch), "made-up") || !strings.Contains(string(patch), "app.go") {
		t.Fatalf("the patch holds an .env file's contents, or lost the rest:\n%s", patch)
	}
	paths, err := os.ReadFile(filepath.Join(destination, "changes", "base.paths"))
	if err != nil || !strings.Contains(string(paths), ".env") {
		t.Fatalf("the change index no longer names the .env file: %s, %v", paths, err)
	}
}
