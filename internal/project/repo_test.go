package project

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRepoKeepsPathsInside(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "docs"), 0o750))
	must(os.WriteFile(filepath.Join(root, "docs/a.md"), []byte("a"), 0o600))
	must(os.WriteFile(filepath.Join(outside, "b.md"), []byte("b"), 0o600))
	must(os.Symlink("a.md", filepath.Join(root, "docs/inside.md")))
	must(os.Symlink(filepath.Join(outside, "b.md"), filepath.Join(root, "docs/out.md")))
	must(os.Symlink(outside, filepath.Join(root, "away")))
	repo := Repo{Root: root}
	if data, err := repo.ReadFile("docs/inside.md"); err != nil || string(data) != "a" {
		t.Errorf("a link inside is read: %q %v", data, err)
	}
	for _, name := range []string{"docs/out.md", "away/b.md", "../x"} {
		if _, err := repo.ReadFile(name); err == nil {
			t.Errorf("%s read", name)
		}
	}
	if _, err := repo.ReadDir("away"); err == nil {
		t.Error("a folder outside listed")
	}
	if err := repo.Create("away/new.md", []byte("x")); err == nil {
		t.Error("a file made outside")
	}
	if entries, err := repo.ReadDir("none"); err != nil || entries != nil {
		t.Errorf("a folder not there lists nothing: %v %v", entries, err)
	}
	must(repo.Create("docs/new.md", []byte("x")))
	if err := repo.Create("docs/new.md", []byte("y")); !errors.Is(err, fs.ErrExist) {
		t.Errorf("a second create: %v", err)
	}
	if !repo.IsFile("docs/a.md") || repo.IsFile("docs") || !repo.IsDir("docs") || repo.IsFile("docs/out.md") {
		t.Error("IsFile / IsDir")
	}
}
