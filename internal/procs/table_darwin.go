package procs

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// zombieState is a process that has ended and waits for its parent to reap it (SZOMB in macOS's
// <sys/proc.h>, the row's p_stat).
const zombieState = 5

// Table is the process table from the kernel (readable inside the agent sandbox, where `ps` is
// not); nil when it cannot be read.
func Table() map[int]Process {
	table, err := ReadTable()
	if err != nil {
		return nil
	}
	return table
}

// ReadTable is the process table from the kernel's `kern.proc.all`, each row with its session.
func ReadTable() (map[int]Process, error) {
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, &TableError{Err: err}
	}
	if len(rows) == 0 {
		return nil, &TableError{Err: errors.New("the kernel gave no rows")}
	}
	table := make(map[int]Process, len(rows))
	for index := range rows {
		process := fromRow(&rows[index])
		table[process.PID] = process
	}
	return table, nil
}

// Row is the table's row of one process; found is false when there is no such process.
func Row(pid int) (process Process, found bool, err error) {
	if pid <= 0 {
		return Process{}, false, nil
	}
	row, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, unix.EIO) {
		// The kernel answered with no row: there is no such process.
		return Process{}, false, nil
	}
	if err != nil {
		return Process{}, false, &TableError{Err: fmt.Errorf("process %d: %w", pid, err)}
	}
	if int(row.Proc.P_pid) != pid {
		return Process{}, false, nil
	}
	return fromRow(row), true, nil
}

func fromRow(row *unix.KinfoProc) Process {
	pid := int(row.Proc.P_pid)
	started := row.Proc.P_starttime
	process := Process{
		PID:         pid,
		Parent:      int(row.Eproc.Ppid),
		Group:       int(row.Eproc.Pgid),
		UID:         int(row.Eproc.Ucred.Uid),
		Start:       started.Sec,
		StartMicros: started.Sec*1_000_000 + int64(started.Usec),
		Name:        unix.ByteSliceToString(row.Proc.P_comm[:]),
		Zombie:      row.Proc.P_stat == zombieState,
	}
	return process
}

// ReadTableWithSessions is ReadTable with each row's session (getsid), which the tree stop needs
// and the guard's hook does not: one more call per process, so it is asked only here.
func ReadTableWithSessions() (map[int]Process, error) {
	table, err := ReadTable()
	if err != nil {
		return nil, err
	}
	for pid, process := range table {
		if session, sessionErr := unix.Getsid(pid); sessionErr == nil {
			process.Session = session
			table[pid] = process
		}
	}
	return table, nil
}
