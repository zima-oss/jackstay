package supervise

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/procs"
)

// awaitExit returns once the process has ended, without reaping it: its id (and so its group's and
// session's) stays its own until Process.Wait. NOTE_EXITSTATUS gives the wait status of a child,
// so how it ended is known before the reap.
func awaitExit(pid int) (exitSeen, error) {
	queue, err := unix.Kqueue()
	if err != nil {
		return exitSeen{}, fmt.Errorf("kqueue: %w", err)
	}
	defer func() { _ = unix.Close(queue) }()
	var change unix.Kevent_t
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT | unix.NOTE_EXITSTATUS
	events := make([]unix.Kevent_t, 1)
	for {
		count, err := unix.Kevent(queue, []unix.Kevent_t{change}, events, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ESRCH):
			// Ended already: a process not yet reaped that the queue no longer watches.
			return exitSeen{}, nil
		case err != nil:
			return exitSeen{}, fmt.Errorf("kevent: %w", err)
		}
		if count == 1 && events[0].Flags&unix.EV_ERROR != 0 {
			if syscall.Errno(events[0].Data) == unix.ESRCH { // #nosec G115 -- an errno
				return exitSeen{}, nil
			}
			return exitSeen{}, fmt.Errorf("kevent: %w", syscall.Errno(events[0].Data)) // #nosec G115 -- an errno
		}
		if count == 1 {
			status := syscall.WaitStatus(events[0].Data) // #nosec G115 -- a wait status
			return exitSeen{signaled: status.Signaled(), known: true}, nil
		}
	}
}

// exitedNow says, without waiting and without reaping, whether the process has ended: the table
// shows it a zombie, or no longer at all.
func exitedNow(pid int) (bool, error) {
	row, found, err := procs.Row(pid)
	if err != nil {
		return false, err
	}
	return !found || row.Zombie, nil
}
