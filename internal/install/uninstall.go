package install

import (
	"fmt"
	"io"
	"strings"

	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/layout"
)

// RunUninstall is `jst uninstall [--prefix DIR]` (docs/design/implementation.md section 7.6): the launchers
// jackstay wrote first (a hook then gets the shell's 127, never a half-removed folder), then lib/jackstay,
// every folder of jackstay's and leftover partial names. The lock file and ~/.jackstay are left.
func RunUninstall(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	prefix := ""
	switch {
	case len(args) == 0:
		prefix = AccountPrefix()
	case len(args) == 2 && args[0] == "--prefix":
		prefix = absolute(args[len(args)-1])
	default:
		fmt.Fprintln(stderr, "usage: jst uninstall [--prefix DIR]")
		return 2
	}
	held, err := layout.Lock(prefix)
	if err != nil {
		fmt.Fprintf(stderr, "jst uninstall: %v; nothing was changed\n", err)
		return 2
	}
	state, err := layout.Read(prefix)
	if err != nil {
		held.Release()
		fmt.Fprintf(stderr, "jst uninstall: %s cannot be read: %v; nothing was changed\n", prefix, err)
		return 2
	}
	removed, problems := layout.RemoveAll(state)
	// The lock file stays: removing it after the release would let two installs each lock a file of
	// its own.
	held.Release()
	for _, launcher := range state.Launchers {
		if launcher.Kind == layout.Foreign {
			fmt.Fprintf(stdout, "jst uninstall: kept %s: jackstay did not write it\n", launcher.Path)
		}
	}
	if state.Link == layout.LinkElsewhere || state.Link == layout.LinkNotALink {
		fmt.Fprintf(stdout, "jst uninstall: kept %s: it is not a link to a folder of jackstay's\n", layout.Link(prefix))
	}
	home, _ := homedir.Home()
	what := "nothing (it was not installed here)"
	if len(removed) > 0 {
		what = strings.Join(removed, ", ")
	}
	fmt.Fprintf(stdout, "jst uninstall: removed %s.\nLeft in place: %s (the lock, the logs, the receipts). Projects whose hooks or "+
		"justfile name the launchers now run WITHOUT the guard (a missing hook command does not block) and their suites do not "+
		"start: revert their adoption commits, or install again.\n", what, home)
	for _, problem := range problems {
		fmt.Fprintf(stderr, "jst uninstall: could not remove: %v\n", problem)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}
