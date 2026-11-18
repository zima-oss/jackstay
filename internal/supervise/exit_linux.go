package supervise

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// The si_code values of a child's end (siginfo.h).
const (
	childKilled = 2 // CLD_KILLED
	childDumped = 3 // CLD_DUMPED
)

// awaitExit returns once the process has ended, without reaping it (waitid with WNOWAIT): its id,
// and so its group's and session's, stays its own until Process.Wait.
func awaitExit(pid int) (exitSeen, error) {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return exitSeen{}, fmt.Errorf("waitid: %w", err)
		}
		return exitSeen{signaled: info.Code == childKilled || info.Code == childDumped, known: true}, nil
	}
}

// exitedNow says, without waiting and without reaping, whether the process has ended.
func exitedNow(pid int) (bool, error) {
	var info unix.Siginfo
	err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
	if err != nil {
		return false, fmt.Errorf("waitid: %w", err)
	}
	// With WNOHANG and nothing to report, the kernel leaves si_signo 0.
	return info.Signo != 0, nil
}
