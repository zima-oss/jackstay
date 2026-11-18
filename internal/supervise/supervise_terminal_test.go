//go:build integration && terminal

// The terminal tests: the agent sandbox refuses /dev/ptmx, so they are their own scope
// (`just terminal`), run outside it before an install, not in the gate.

package supervise

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPseudoTerminal is a pseudo-terminal's two sides, by /dev/ptmx and its ioctls.
func openPseudoTerminal(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("no pseudo-terminal (the agent sandbox refuses /dev/ptmx: run the integration tests outside it): %v", err)
	}
	descriptor := master.Fd()
	for _, request := range []uint{unix.TIOCPTYGRANT, unix.TIOCPTYUNLK} {
		if err := unix.IoctlSetInt(int(descriptor), request, 0); err != nil {
			t.Fatalf("ioctl %x: %v", request, err)
		}
	}
	// x/sys has no wrapper that hands back the slave's name (TIOCPTYGNAME fills a buffer).
	name := make([]byte, 128)
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, descriptor, uintptr(unix.TIOCPTYGNAME), //nolint:staticcheck // see above
		uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		t.Fatalf("ioctl TIOCPTYGNAME: %v", errno)
	}
	slave, err = os.OpenFile(string(bytes.TrimRight(name, "\x00")), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	return master, slave
}

func TestTheGroupHasTheTerminalWhileItRunsAndGivesItBack(t *testing.T) {
	master, slave := openPseudoTerminal(t)
	defer master.Close()
	go func() {
		buffer := make([]byte, 4096)
		for {
			if _, err := master.Read(buffer); err != nil {
				return
			}
		}
	}()
	answer := filepath.Join(t.TempDir(), "answer")
	command := helper(t, "tty-session")
	command.Env = append(command.Env, fileVariable+"="+answer)
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	slave.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(answer)
	if err != nil || string(data) != "lent=0 back=0" {
		t.Fatalf("answer %q (%v)", data, err)
	}
}
