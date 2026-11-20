package codex

// envGlobRoots are the folders under which Codex denies `.env` files: on macOS, the whole
// filesystem, its sandbox checking each read against the pattern.
func envGlobRoots(Request, string) []string { return []string{"/"} }
