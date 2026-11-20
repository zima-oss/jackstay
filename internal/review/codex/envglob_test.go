package codex

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestEnvFilesAreDeniedUnderRootsThisPlatformTakes(t *testing.T) {
	request := Request{Tree: "/work/view", RepositoryRoot: "/work/checkouts/project"}
	roots := envGlobRoots(request, "/home/r")
	if runtime.GOOS == "darwin" {
		if !slices.Equal(roots, []string{"/"}) {
			t.Fatalf("macOS checks the pattern at every read, from the root: %v", roots)
		}
		return
	}
	if !slices.Equal(roots, []string{"/work/view", "/work/checkouts/project"}) {
		t.Fatalf("roots: %v (only the view and the checkout: Codex's scan stops on an unreadable folder)", roots)
	}
}

func TestALinkedEnvFileIsDeniedByItsPaths(t *testing.T) {
	checkout, shared := t.TempDir(), t.TempDir()
	target := filepath.Join(shared, "settings")
	if err := os.WriteFile(target, []byte("X=made-up\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(checkout, ".env")); err != nil {
		t.Fatal(err)
	}
	links := envLinks(Request{RepositoryRoot: checkout})
	if runtime.GOOS == "darwin" {
		if len(links) != 0 {
			t.Fatalf("macOS needs no literal links: %v", links)
		}
		return
	}
	resolved, _ := filepath.EvalSymlinks(target)
	if !slices.Contains(links, filepath.Join(checkout, ".env")) || !slices.Contains(links, resolved) {
		t.Fatalf("links: %v", links)
	}
}
