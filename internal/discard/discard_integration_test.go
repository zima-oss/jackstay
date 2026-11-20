//go:build integration

package discard

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/trash"
)

func TestRestoreAuthorizationUsesTheInstalledGitOutput(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n", ".gitattributes": "a.txt filter=prefix\n"})
	w.repo.Git("config", "filter.prefix.smudge", "sed 's/^/checked: /'")
	w.repo.Git("config", "filter.prefix.clean", "sed 's/^checked: //'")
	w.repo.Write(map[string]string{"a.txt": "checked: saved earlier\n"})

	installReplacement = func(source, target string, expected trash.Replaced) error {
		if err := trash.InstallDiscardReplacement(source, target, expected); err != nil {
			return err
		}
		if got := w.read("a.txt"); got != "checked: head\n" {
			t.Fatalf("worktree lost Git's checkout conversion: %q", got)
		}
		return os.WriteFile(target, []byte("checked: newer edit\n"), 0o600)
	}
	t.Cleanup(func() { installReplacement = trash.InstallDiscardReplacement })
	if code := w.discard(w.repo.Root, "a.txt"); code != 0 {
		t.Fatalf("discard exit %d: %s", code, w.errors.String())
	}
	entries := trash.Entries(w.trash)
	digest := sha256.Sum256([]byte("checked: head\n"))
	fingerprint := trash.Replaced{Kind: "file", SHA256: hex.EncodeToString(digest[:])}
	if len(entries) != 1 || entries[0].Replaced == nil || *entries[0].Replaced != fingerprint {
		t.Fatalf("trash entry was not bound to the staged Git output: %+v", entries)
	}
	if code := w.restore("restore", entries[0].ID); code != 1 || w.read("a.txt") != "checked: newer edit\n" {
		t.Fatalf("the newer edit was not preserved: restore %d, file %q", code, w.read("a.txt"))
	}
	recovered := filepath.Join(t.TempDir(), "a.txt")
	if code := w.restore("restore", entries[0].ID, "--to", recovered); code != 0 {
		t.Fatalf("restore to another path exit %d: %s", code, w.errors.String())
	}
	if data, err := os.ReadFile(recovered); err != nil || string(data) != "checked: saved earlier\n" {
		t.Fatalf("the original was not recoverable: %q, %v", data, err)
	}
}

func TestDiscardPreservesAFileRecreatedBeforeInstall(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	w.repo.Write(map[string]string{"a.txt": "staged earlier\n"})
	w.repo.Git("add", "a.txt")
	w.repo.Write(map[string]string{"a.txt": "saved earlier\n"})
	installReplacement = func(source, target string, expected trash.Replaced) error {
		if err := os.WriteFile(target, []byte("newer edit\n"), 0o600); err != nil {
			return err
		}
		return trash.InstallDiscardReplacement(source, target, expected)
	}
	t.Cleanup(func() { installReplacement = trash.InstallDiscardReplacement })
	if code := w.discard(w.repo.Root, "a.txt"); code != 1 || w.read("a.txt") != "newer edit\n" {
		t.Fatalf("discard did not leave the recreated file alone: exit %d, file %q, %s", code, w.read("a.txt"), w.errors.String())
	}
	if !strings.Contains(w.errors.String(), "the index was restored") || w.repo.Git("show", ":a.txt") != "head" {
		t.Fatalf("partial outcome is not clear: %s", w.errors.String())
	}
	entries := trash.Entries(w.trash)
	if len(entries) != 2 {
		t.Fatalf("want the previously staged and working files: %+v", entries)
	}
	for _, entry := range entries {
		if !entry.Held || entry.Replaced != nil {
			t.Fatalf("the previous versions were not held safely for recovery: %+v", entries)
		}
	}
	kept := w.kept()
	if len(kept["staged"]) != 1 || kept["staged"][0] != "staged earlier\n" || len(kept["file"]) != 1 || kept["file"][0] != "saved earlier\n" {
		t.Fatalf("the previous versions changed: %+v", kept)
	}
}

func TestDiscardRestoresAStagedFileInARemovedDirectory(t *testing.T) {
	w := newWorld(t, map[string]string{"nested/dir/a.txt": "committed\n"})
	w.repo.Write(map[string]string{"nested/dir/a.txt": "staged\n"})
	w.repo.Git("add", "nested/dir/a.txt")
	for _, name := range []string{"nested/dir/a.txt", "nested/dir", "nested"} {
		if err := os.Remove(filepath.Join(w.repo.Root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if code := w.discard(w.repo.Root, "nested/dir/a.txt"); code != 0 {
		t.Fatalf("discard could not recreate the missing directory: %d, %s", code, w.errors.String())
	}
	if w.read("nested/dir/a.txt") != "committed\n" || w.status() != "" {
		t.Fatalf("discard did not restore HEAD's working tree: %q %s", w.read("nested/dir/a.txt"), w.status())
	}
	kept := w.kept()
	if len(kept["staged"]) != 1 || kept["staged"][0] != "staged\n" {
		t.Fatalf("the staged version was lost: %+v", kept)
	}
}

func TestDiscardPreservesAMissingFileRecreatedBeforeInstall(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	if err := os.Remove(filepath.Join(w.repo.Root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	installReplacement = func(source, target string, expected trash.Replaced) error {
		if err := os.WriteFile(target, []byte("recreated by an editor\n"), 0o600); err != nil {
			return err
		}
		return trash.InstallDiscardReplacement(source, target, expected)
	}
	t.Cleanup(func() { installReplacement = trash.InstallDiscardReplacement })
	if code := w.discard(w.repo.Root, "a.txt"); code != 1 || w.read("a.txt") != "recreated by an editor\n" {
		t.Fatalf("discard overwrote the recreated file: exit %d, file %q, %s", code, w.read("a.txt"), w.errors.String())
	}
	if !strings.Contains(w.errors.String(), "the index was restored") {
		t.Fatalf("partial outcome is not clear: %s", w.errors.String())
	}
}

func TestDiscardRestoresAMissingFileWithNothingToKeep(t *testing.T) {
	w := newWorld(t, map[string]string{"a.txt": "head\n"})
	if err := os.Remove(filepath.Join(w.repo.Root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if code := w.discard(w.repo.Root, "a.txt"); code != 0 {
		t.Fatalf("discard exit %d: %s", code, w.errors.String())
	}
	if w.read("a.txt") != "head\n" || w.status() != "" || len(trash.Entries(w.trash)) != 0 {
		t.Fatalf("the missing file was not written back alone: %q %s %+v", w.read("a.txt"), w.status(), trash.Entries(w.trash))
	}
}
