package doctor

import "golang.org/x/sys/unix"

func isTerminal(fileDescriptor uintptr) bool {
	_, err := unix.IoctlGetTermios(int(fileDescriptor), unix.TIOCGETA)
	return err == nil
}
