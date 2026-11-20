package choices

import (
	"os"

	"golang.org/x/sys/unix"
)

// IsTerminal says the file is a terminal: a person may be asked there.
func IsTerminal(file *os.File) bool {
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS) // #nosec G115 -- a descriptor number
	return err == nil
}
