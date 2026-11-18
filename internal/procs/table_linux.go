package procs

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// procRoot is where Linux shows its process table (a test's own folder in tests).
var procRoot = "/proc"

// ticksPerSecond is the unit of /proc/<pid>/stat's start time: USER_HZ, which Linux fixes at 100
// for every interface it exports to user space, whatever the kernel's own tick.
const ticksPerSecond = 100

// Table is the process table from /proc; nil when it cannot be read.
func Table() map[int]Process {
	table, err := ReadTable()
	if err != nil {
		return nil
	}
	return table
}

// ReadTable is the process table from /proc. Each row's session is left 0, as on macOS, where
// asking for it costs a call per process; ReadTableWithSessions fills it.
func ReadTable() (map[int]Process, error) { return readTable(false) }

// ReadTableWithSessions is ReadTable with each row's session.
func ReadTableWithSessions() (map[int]Process, error) { return readTable(true) }

// Row is the table's row of one process; found is false when there is no such process.
func Row(pid int) (process Process, found bool, err error) {
	if pid <= 0 {
		return Process{}, false, nil
	}
	boot, err := bootTime()
	if err != nil {
		return Process{}, false, &TableError{Err: err}
	}
	row, err := readRow(pid, boot, false)
	if ended(err) {
		return Process{}, false, nil
	}
	if err != nil {
		return Process{}, false, &TableError{Err: fmt.Errorf("process %d: %w", pid, err)}
	}
	return row, true, nil
}

func readTable(withSessions bool) (map[int]Process, error) {
	boot, err := bootTime()
	if err != nil {
		return nil, &TableError{Err: err}
	}
	return readTableWith(listPIDs, boot, withSessions)
}

// readTableWith is readTable with the listing given (a test replays a listing that changes).
func readTableWith(list func() ([]int, error), boot int64, withSessions bool) (map[int]Process, error) {
	// macOS gives the table in one snapshot; /proc is read a row at a time. A process can end
	// between the listing and its row, after starting another that the listing did not have yet:
	// the table would then hold neither, and a group could look empty while a member runs. So when
	// a process ended while its row was read, the listing is read again and the rows of the ids it
	// adds are read, until a reading has no process end under it: a process started by one that
	// ended unseen is in the next listing. A process read as a zombie counts as ended too: it may
	// have started a child after the listing and ended before its row was read, and a zombie is no
	// member of anything. A process started by one that was read running is no such gap: its parent
	// is in the table, as with any snapshot taken a moment earlier. (Asking only for a
	// listing that adds nothing never settled on a busy machine: parallel test runs start
	// processes all the time.)
	table := map[int]Process{}
	seen := map[int]bool{}
	for pass := 0; ; pass++ {
		if pass == maxListings {
			// Processes keep starting somewhere on the machine: no stable listing, so the table is
			// not proof of anything, and the caller keeps its own careful way.
			return nil, &TableError{Err: fmt.Errorf("processes kept ending while /proc was read, %d readings in a row", maxListings)}
		}
		pids, listErr := list()
		if listErr != nil {
			return nil, &TableError{Err: listErr}
		}
		vanished := false
		for _, pid := range pids {
			if seen[pid] {
				continue
			}
			seen[pid] = true
			row, rowErr := readRow(pid, boot, withSessions)
			if ended(rowErr) {
				// A process that ended between the listing and the read is not in the table; what
				// it started may not be in this listing either.
				vanished = true
				continue
			}
			if rowErr != nil {
				// Any other failure (a row this account may not read, say) leaves the table
				// unreadable: an absent row must mean an ended process, never an unread one.
				return nil, &TableError{Err: fmt.Errorf("process %d: %w", pid, rowErr)}
			}
			table[pid] = row
			if row.Zombie {
				vanished = true
			}
		}
		if !vanished {
			break
		}
	}
	if len(table) == 0 {
		return nil, &TableError{Err: errors.New("/proc listed no process")}
	}
	return table, nil
}

// maxListings bounds the readings of /proc's listing in one table: a machine where processes end
// under every reading gets no table rather than a read that holds up a stop. Each reading after
// the first reads only the ids it adds.
const maxListings = 32

// ended says a row's read failed because its process ended: its folder or files are gone.
func ended(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) || errors.Is(err, errGone)
}

// listPIDs is the ids /proc lists now.
func listPIDs() ([]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, entry := range entries {
		if pid, convErr := strconv.Atoi(entry.Name()); convErr == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// errGone says a process ended while its row was read.
var errGone = errors.New("the process ended while it was read")

// readRow reads /proc/<pid>/stat and the effective user from /proc/<pid>/status.
func readRow(pid int, boot int64, withSessions bool) (Process, error) {
	directory := procRoot + "/" + strconv.Itoa(pid)
	stat, err := os.ReadFile(directory + "/stat")
	if err != nil {
		return Process{}, err
	}
	process, session, err := parseStat(stat, boot)
	if err != nil {
		return Process{}, err
	}
	if process.PID != pid {
		return Process{}, errGone
	}
	status, err := os.ReadFile(directory + "/status")
	if err != nil {
		return Process{}, err
	}
	uid, threads, err := readStatus(status)
	if err != nil {
		return Process{}, err
	}
	process.UID = uid
	// A main thread that ended shows its process as a zombie while its other threads still run:
	// only a process with no thread left has ended.
	process.Zombie = process.Zombie && threads <= 1
	if withSessions {
		process.Session = session
	}
	return process, nil
}

// parseStat reads one /proc/<pid>/stat line: `pid (comm) state ppid pgrp session … starttime …`.
// The name may hold spaces and parentheses, so it ends at the line's last ')'.
func parseStat(stat []byte, boot int64) (process Process, session int, err error) {
	open := bytes.IndexByte(stat, '(')
	end := bytes.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return Process{}, 0, fmt.Errorf("unreadable stat %q", stat)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(stat[:open])))
	if err != nil {
		return Process{}, 0, fmt.Errorf("unreadable stat pid: %w", err)
	}
	// After the name: state(3) ppid(4) pgrp(5) session(6) … starttime(22), counted from 1.
	fields := strings.Fields(string(stat[end+1:]))
	const startField = 22 - 3
	if len(fields) <= startField {
		return Process{}, 0, fmt.Errorf("stat of %d has %d fields after the name", pid, len(fields))
	}
	numbers := make([]int64, 0, 4)
	for _, index := range []int{1, 2, 3, startField} {
		number, convErr := strconv.ParseInt(fields[index], 10, 64)
		if convErr != nil {
			return Process{}, 0, fmt.Errorf("stat of %d, field %d: %w", pid, index+3, convErr)
		}
		numbers = append(numbers, number)
	}
	ticks := numbers[3]
	micros := boot*1_000_000 + ticks*(1_000_000/ticksPerSecond)
	process = Process{
		PID:         pid,
		Parent:      int(numbers[0]),
		Group:       int(numbers[1]),
		Start:       micros / 1_000_000,
		StartMicros: micros,
		Name:        string(stat[open+1 : end]),
		Zombie:      fields[0] == "Z",
	}
	return process, int(numbers[2]), nil
}

// readStatus reads /proc/<pid>/status: the effective user (the second number of `Uid:`: real,
// effective, saved, fs) and the number of threads (`Threads:`).
func readStatus(status []byte) (uid, threads int, err error) {
	foundUID, foundThreads := false, false
	scanner := bufio.NewScanner(bytes.NewReader(status))
	for scanner.Scan() {
		line := scanner.Text()
		if value, ok := strings.CutPrefix(line, "Uid:"); ok {
			ids := strings.Fields(value)
			if len(ids) < 2 {
				return 0, 0, fmt.Errorf("unreadable Uid line %q", line)
			}
			if uid, err = strconv.Atoi(ids[1]); err != nil {
				return 0, 0, err
			}
			foundUID = true
		}
		if value, ok := strings.CutPrefix(line, "Threads:"); ok {
			if threads, err = strconv.Atoi(strings.TrimSpace(value)); err != nil {
				return 0, 0, err
			}
			foundThreads = true
		}
	}
	if !foundUID || !foundThreads {
		return 0, 0, errors.New("status has no Uid or Threads line")
	}
	return uid, threads, nil
}

var (
	bootOnce    sync.Once
	bootSeconds int64
	errBoot     error
)

// bootTime is a number of seconds fixed for this boot, from /proc/sys/kernel/random/boot_id: the
// base that turns a process's start in ticks since boot into a start that every jackstay process
// reads alike for as long as the process lives. /proc/stat's `btime` is not used: it moves when the
// clock is set, so two commands either side of a clock correction would see one process twice.
// On Linux a start is therefore an identity, not a time of day.
func bootTime() (int64, error) {
	bootOnce.Do(func() {
		data, err := os.ReadFile(procRoot + "/sys/kernel/random/boot_id")
		if err != nil {
			errBoot = err
			return
		}
		bootSeconds, errBoot = bootBase(string(data))
	})
	return bootSeconds, errBoot
}

// bootBase is the first eight hex digits of a boot id, as seconds (under 2^32).
func bootBase(bootID string) (int64, error) {
	digits := strings.ReplaceAll(strings.TrimSpace(bootID), "-", "")
	if len(digits) < 8 {
		return 0, fmt.Errorf("unreadable boot id %q", bootID)
	}
	return strconv.ParseInt(digits[:8], 16, 64)
}
