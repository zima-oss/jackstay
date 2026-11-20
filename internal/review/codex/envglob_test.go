package codex

import (
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
	if !slices.Equal(roots, []string{"/work/view", "/work/checkouts/project", "/work/checkouts"}) {
		t.Fatalf("roots: %v (Codex refuses a glob whose prefix is the root here)", roots)
	}
	if beside := envGlobRoots(Request{RepositoryRoot: "/home/r/project"}, "/home/r"); slices.Contains(beside, "/home/r") {
		t.Fatalf("the account's home is walked: %v", beside)
	}
}
