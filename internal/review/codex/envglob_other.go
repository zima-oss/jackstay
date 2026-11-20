//go:build !darwin && !linux

package codex

import (
	"os"
	"path/filepath"
	"strings"
)

// envGlobRoots are the folders under which Codex denies `.env` files. Here Codex expands a glob
// before the run, refuses one rooted at the filesystem's root, and stops every sandboxed command
// when its scan meets a folder it cannot read: so the roots are only the source view and the live
// checkout, never a folder of other projects.
func envGlobRoots(request Request, _ string) []string {
	roots := []string{}
	for _, root := range []string{request.Tree, request.RepositoryRoot} {
		if root != "" {
			roots = append(roots, root)
		}
	}
	return roots
}

// envLinks are the `.env` symbolic links at the top of the checkout and their targets: Codex's scan
// here takes regular files only, so a linked `.env` is denied by its literal paths.
func envLinks(request Request) []string {
	var found []string
	if request.RepositoryRoot == "" {
		return nil
	}
	listed, err := os.ReadDir(request.RepositoryRoot)
	if err != nil {
		return nil
	}
	for _, entry := range listed {
		name := entry.Name()
		if entry.Type()&os.ModeSymlink == 0 || name != ".env" && !strings.HasPrefix(name, ".env.") {
			continue
		}
		link := filepath.Join(request.RepositoryRoot, name)
		found = append(found, link)
		if target, err := filepath.EvalSymlinks(link); err == nil {
			found = append(found, target)
		}
	}
	return found
}
