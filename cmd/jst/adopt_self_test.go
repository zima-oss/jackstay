package main

import (
	"path/filepath"
	"testing"

	"github.com/zima-oss/jackstay/internal/adopt"
	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/version"
)

// jackstay adopts itself first (docs/design/adopt.md section 13): its own checkout, planned with the real
// command table, has nothing to change and no conflict. When a command is added or an item of the
// block changes, this fails until `jst adopt --apply` is run in jackstay's checkout and its AGENTS.md
// committed with the change.
func TestJackstayIsAdoptedByItself(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := adopt.Make(adopt.Options{Dir: root, WorkingDir: root, Home: t.TempDir(), Has: cli.Has, Version: version.Version})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range plan.Changes {
		t.Errorf("pending: %s %s: %s (run `jst adopt --apply` in jackstay's checkout and commit the result)", change.ID, change.Path, change.What)
	}
	for _, conflict := range plan.Conflicts {
		t.Errorf("conflict: %s: %s", conflict.ID, conflict.What)
	}
}
