package scopes

import (
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/receipts"
)

// PinnedGate runs the gate in a clean copy of an exact commit for the merge queue.
// It returns the recorded gate ID only when tracked inputs stayed pinned and the gate passed.
func PinnedGate(commit string, stdout, stderr io.Writer) (gateRunID string, status int) {
	base, err := newTester(stdout, stderr)
	if err != nil {
		receipts.Say(stderr, "the working folder cannot be read: %v", err)
		return "", statusRefused
	}
	return base.pinnedGate(commit)
}

func (base *tester) pinnedGate(commit string) (string, int) {
	base.pin = &gatePin{commit: commit}
	status := base.scope([]string{"gate", "--commit", commit})
	return base.pin.runID, status
}

// gatePin belongs to one gate. Stamps detect a tracked rewrite even when bytes are restored
// before a command returns. A failed verification invalidates the entire gate.
type gatePin struct {
	commit  string
	stamps  map[string]unix.Stat_t
	problem string
	runID   string
}

func (proj *project) pinnedProblem() string {
	if proj.pin == nil {
		return ""
	}
	if proj.pin.problem == "" {
		if why := proj.checkPinnedTree(); why != "" {
			proj.pin.problem = "pinned gate checkout: " + why
		}
	}
	return proj.pin.problem
}

func (proj *project) checkPinnedTree() string {
	if head := proj.head(); head != proj.pin.commit {
		return "HEAD differs from the selected commit or cannot be read"
	}
	status, err := proj.git(proj.ctx, proj.root, "-c", "core.fsmonitor=false", "-c", "core.fileMode=true",
		"status", "--porcelain=v1", "--untracked-files=no", "--ignore-submodules=none", "-z")
	if err != nil {
		return "Git status could not be read"
	}
	if changes := zeroSeparated(status); len(changes) != 0 {
		return fmt.Sprintf("tracked files or the index changed (%s)", changes[0])
	}
	listed := proj.gitText("ls-files", "-v", "-z")
	if listed == nil {
		return "Git could not list the tracked files"
	}
	stamps := map[string]unix.Stat_t{}
	for _, entry := range zeroSeparated(listed) {
		tag, name, ok := strings.Cut(entry, " ")
		if !ok || tag != "H" || !filepath.IsLocal(name) {
			return "Git did not verify every tracked entry"
		}
		var info unix.Stat_t
		if err := unix.Lstat(filepath.Join(proj.root, name), &info); err != nil {
			return fmt.Sprintf("tracked file %q could not be read", name)
		}
		// Store identity and change times only. Reading a file may update its access time.
		stamps[name] = unix.Stat_t{Dev: info.Dev, Ino: info.Ino, Mode: info.Mode, Size: info.Size, Mtim: info.Mtim, Ctim: info.Ctim}
	}
	if proj.pin.stamps == nil {
		proj.pin.stamps = stamps
	} else if !maps.Equal(proj.pin.stamps, stamps) {
		return "a tracked file was rewritten during the gate"
	}
	return ""
}
