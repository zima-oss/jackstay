//go:build !darwin && !linux

package codex

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Here Codex expands a glob rule before the run, takes regular files only, refuses one rooted at
// the filesystem's root and stops every sandboxed command when its scan meets a folder it cannot
// read. So jackstay gives it no `.env` glob: it lists the `.env` files and links of the source view
// and the live checkout itself, past folders it cannot read, and denies each by its literal path
// (a link by its own path and its target's).

// envGlobRoots are none here: see envLinks.
func envGlobRoots(Request, string) []string { return nil }

// envScan bounds the listing: how deep, and how many entries at most in each root.
const (
	envScanDepth  = 12
	envScanVisits = 200000
)

// envSkipped are folders of dependencies and Git's own data, where no project's `.env` lives.
var envSkipped = []string{".git", "node_modules", "vendor", ".venv"}

// envLinks are the `.env` files and links in the source view and the live checkout, and the
// targets of the links. It is a snapshot taken when the run starts.
func envLinks(request Request) []string {
	var found []string
	for _, root := range []string{request.Tree, request.RepositoryRoot} {
		if root == "" {
			continue
		}
		visited := 0
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if visited++; visited > envScanVisits {
				return filepath.SkipAll
			}
			if err != nil { // a folder it cannot read is passed over, never fatal
				if entry != nil && entry.IsDir() && path != root {
					return filepath.SkipDir
				}
				return nil
			}
			relative, _ := filepath.Rel(root, path)
			if entry.IsDir() {
				if path != root && (slices.Contains(envSkipped, entry.Name()) || strings.Count(relative, string(filepath.Separator)) >= envScanDepth) {
					return filepath.SkipDir
				}
				return nil
			}
			if name := entry.Name(); name != ".env" && !strings.HasPrefix(name, ".env.") {
				return nil
			}
			found = append(found, path)
			if entry.Type()&os.ModeSymlink != 0 {
				if target, err := filepath.EvalSymlinks(path); err == nil {
					found = append(found, target)
				}
			}
			return nil
		})
	}
	return found
}
