package supervise

import (
	"errors"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// terminal is jackstay's controlling terminal while jackstay's group is the one it talks to: what the
// command's group is given, and what is taken back afterwards.
type terminal struct {
	file *os.File
	// ours is jackstay's own process group, given the terminal back after the end.
	ours int
}

// openTerminal is the controlling terminal when jackstay's group is in front of it; nil otherwise (no
// terminal, or jackstay runs in the background).
func openTerminal() *terminal {
	file, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil
	}
	front, err := unix.IoctlGetInt(int(file.Fd()), unix.TIOCGPGRP) // #nosec G115 -- a descriptor number
	ours := unix.Getpgrp()
	if err != nil || front != ours {
		_ = file.Close()
		return nil
	}
	return &terminal{file: file, ours: ours}
}

// attributes are the start's process attributes: the terminal given to the new group when there
// is one.
func (tty *terminal) attributes() *syscall.SysProcAttr {
	if tty == nil {
		return &syscall.SysProcAttr{Setpgid: true}
	}
	// Foreground's Ctty is a descriptor of this process (syscall's documentation): the child sets
	// its group in front of the terminal before exec, with SIGTTOU blocked.
	return &syscall.SysProcAttr{Foreground: true, Ctty: int(tty.file.Fd())} // #nosec G115 -- a descriptor number
}

// refusedTerminal says a start failed only because the terminal could not be given (the
// command never ran): it is made again without.
func refusedTerminal(err error) bool {
	return errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EOPNOTSUPP)
}

// giveBack makes jackstay's group the terminal's again, with SIGTTOU ignored meanwhile (jackstay is in the
// background until it is done), and closes the terminal.
func (tty *terminal) giveBack() {
	if tty == nil {
		return
	}
	wasIgnored := signal.Ignored(unix.SIGTTOU)
	signal.Ignore(unix.SIGTTOU)
	_ = unix.IoctlSetPointerInt(int(tty.file.Fd()), unix.TIOCSPGRP, tty.ours) // #nosec G115 -- a descriptor number
	if !wasIgnored {
		signal.Reset(unix.SIGTTOU)
	}
	_ = tty.file.Close()
}

func (tty *terminal) close() {
	if tty != nil {
		_ = tty.file.Close()
	}
}
