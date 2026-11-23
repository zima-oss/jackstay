package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/adopt"
	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
)

// jackstay adopts itself first (docs/design/adopt.md section 13): its own checkout, planned with the real
// command table, has nothing to change and no conflict. When a command is added or an item of the
// block changes, this fails until `jst adopt --apply` is run in jackstay's checkout and its AGENTS.md
// committed with the change. The checkout is managed by its pinned release, not by the version being
// built (W-108): the plan is made as that release would make it, so a version bump asks for nothing
// until the configuration that adopts the new release moves the pin.
func TestJackstayIsAdoptedByItself(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	_, loaded, err := config.Load(root, []string{"adopt"})
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := loaded.Section("adopt")["pin"].(string)
	if pin == "" {
		t.Fatal("jackstay.json has no adopt.pin: the release that manages this checkout must be declared (W-108)")
	}
	plan, err := adopt.Make(adopt.Options{Dir: root, WorkingDir: root, Home: t.TempDir(), Has: cli.Has,
		Version: strings.TrimPrefix(pin, "v")})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range plan.Changes {
		t.Errorf("pending: %s %s: %s (run `jst adopt --apply` in jackstay's checkout and commit the result)", change.ID, change.Path, change.What)
	}
	for _, conflict := range plan.Conflicts {
		t.Errorf("conflict: %s: %s", conflict.ID, conflict.What)
	}
	for _, blocker := range plan.Blockers {
		t.Errorf("blocker: %s (an adoption at the pin left part way)", blocker)
	}
}
