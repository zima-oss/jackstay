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
	if len(roots) != 0 {
		t.Fatalf("roots: %v (no .env rule off macOS until it is redesigned)", roots)
	}
}

func TestNoEnvRuleIsGivenOffMacOS(t *testing.T) {
	checkout, shared := t.TempDir(), t.TempDir()
	target := filepath.Join(shared, "settings")
	if err := os.WriteFile(target, []byte("X=made-up\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(checkout, ".env")); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(checkout, "service", "deep")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".env.local"), []byte("Y=made-up\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(checkout, "data")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	links := envLinks(Request{RepositoryRoot: checkout})
	if runtime.GOOS == "darwin" {
		if len(links) != 0 {
			t.Fatalf("macOS needs no literal links: %v", links)
		}
		return
	}
	if len(links) != 0 {
		t.Fatalf("links: %v (no .env rule off macOS until it is redesigned)", links)
	}
}
