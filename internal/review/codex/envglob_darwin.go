package codex

// envGlobRoots are the folders under which Codex denies `.env` files: on macOS, the whole
// filesystem, its sandbox checking each read against the pattern, links included.
func envGlobRoots(Request, string) []string { return []string{"/"} }

// envLinks are `.env` links to deny by literal path: none here, the read-time check covers them.
func envLinks(Request) []string { return nil }
