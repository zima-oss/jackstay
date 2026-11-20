package codex

import "path/filepath"

// envGlobRoots are the folders under which Codex denies `.env` files. Codex expands a glob before
// the run here, and refuses one whose literal prefix is the filesystem's root, so the roots are the
// source view, the live checkout and the folder beside it (the neighbouring checkouts), unless that
// folder is the account's home.
func envGlobRoots(request Request, home string) []string {
	roots := []string{}
	for _, root := range []string{request.Tree, request.RepositoryRoot} {
		if root != "" {
			roots = append(roots, root)
		}
	}
	if beside := filepath.Dir(request.RepositoryRoot); request.RepositoryRoot != "" && beside != home && beside != "/" {
		roots = append(roots, beside)
	}
	return roots
}
