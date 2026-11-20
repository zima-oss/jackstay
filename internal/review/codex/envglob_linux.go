package codex

// Here Codex's sandbox expands a glob rule before the run, takes regular files only, and stops
// every sandboxed command when its scan meets a folder it cannot read; jackstay has no Linux to
// verify a design against. So on this system Codex gets no `.env` rule at all: the reviewer keeps
// the read-only profile and its credential-path denials, and `.env` files stay readable to it
// (docs/design/review.md section 3.4; the redesign is an issue of its own).

// envGlobRoots are none here.
func envGlobRoots(Request, string) []string { return nil }

// envLinks are none here.
func envLinks(Request) []string { return nil }
