package background

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/proctree"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// machine is what the commands read and signal through; tests give a fake one (W-013).
type machine struct {
	clock supervise.Clock
	// read is the process table with each row's session.
	read func() (map[int]procs.Process, error)
	// row is one process's row; found is false when there is none.
	row func(pid int) (process procs.Process, found bool, err error)
	// signal sends a signal to a process, or to a group when target is negative (kill(2)).
	signal func(target int, sig syscall.Signal) error
	// held are the files the group and the other processes hold open for writing; looked is false
	// when lsof gave no answer.
	held func(group int, others []int) (files []string, looked bool)
	uid  int
	pid  int
	// poll is the pause between two reads of the table while a stop waits.
	poll time.Duration
}

func realMachine() machine {
	return machine{clock: supervise.RealClock(), read: procs.ReadTableWithSessions, row: procs.Row, signal: unix.Kill,
		held: heldFiles, uid: os.Getuid(), pid: os.Getpid(), poll: supervise.DefaultPoll}
}

func (host machine) system() proctree.System {
	return proctree.System{Read: host.read, Signal: host.signal, Clock: host.clock, Poll: host.poll,
		Settle: supervise.DefaultKillWait, UID: host.uid}
}

// table is the process table, nil when it cannot be read.
func (host machine) table() map[int]procs.Process {
	table, err := host.read()
	if err != nil {
		return nil
	}
	return table
}

// sameProcess says whether the table shows pid running with that start in whole seconds; known is
// false without a table.
func sameProcess(table map[int]procs.Process, pid int, start int64) (same, known bool) {
	if table == nil {
		return false, false
	}
	row, present := table[pid]
	return present && !row.Zombie && row.Start == start, true
}

// supervisorAlive says whether the `jst bg` that wrote the entry still runs. With its start
// written, the table must show that very process (an id is given out again); without one, or
// without a table, its id is all there is.
func (host machine) supervisorAlive(table map[int]procs.Process, entry Entry) bool {
	if entry.Supervisor <= 1 {
		return false
	}
	if table != nil {
		row, present := table[entry.Supervisor]
		if !present || row.Zombie {
			return false
		}
		return entry.SupervisorStart == nil || row.Start == *entry.SupervisorStart
	}
	err := host.signal(entry.Supervisor, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

// supervisorGone says the `jst bg` that wrote the entry is proven gone: the table shows no such
// process (its id absent, a zombie, or another start), or without a table its id answers ESRCH.
func (host machine) supervisorGone(table map[int]procs.Process, entry Entry) bool {
	if entry.Supervisor <= 1 {
		return true
	}
	if table != nil {
		return !host.supervisorAlive(table, entry)
	}
	return errors.Is(host.signal(entry.Supervisor, 0), unix.ESRCH)
}

// groupAlive says whether anything runs in the group (or the session it leads).
func (host machine) groupAlive(table map[int]procs.Process, group int) bool {
	if group <= 1 {
		return false
	}
	if table != nil {
		for _, row := range table {
			if !row.Zombie && (row.Group == group || row.Session == group) {
				return true
			}
		}
		return false
	}
	err := host.signal(-group, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

// refusal is why a signal to pid would be refused (0: it would not be): the sandbox, when the
// process is this account's; another account, when not. Asked with signal 0, so nothing is sent.
func (host machine) refusal(table map[int]procs.Process, pid int) proctree.Refusal {
	err := host.signal(pid, 0)
	switch {
	case err == nil || errors.Is(err, unix.ESRCH):
		return 0
	case errors.Is(err, unix.EPERM) && table[pid].UID == host.uid:
		return proctree.SandboxForbids
	case errors.Is(err, unix.EPERM):
		return proctree.NotThisAccount
	default:
		return proctree.OtherFailure
	}
}

// groupTree is the tree stopGroup stops: every process of the group and of the session its leader
// leads, and (sampled) their descendants.
func groupTree(table map[int]procs.Process, group int) *proctree.Tree {
	tree := proctree.New()
	tree.FollowSession(group)
	for pid, row := range table {
		if !row.Zombie && (row.Group == group || row.Session == group) {
			tree.Remember(pid, row.StartMicros)
		}
	}
	tree.Sample(table)
	return tree
}

// stopSet are what stopGroup signals besides the group itself, as the table shows them now: the
// members of its tree in other groups, by id.
func stopSet(table map[int]procs.Process, group int) []int {
	var others []int
	for _, row := range groupTree(table, group).Living(table) {
		if row.Group != group {
			others = append(others, row.PID)
		}
	}
	return others
}

// stopGroup stops a `jst bg` group whose leader the caller has proved: every process of the group
// and of the session it leads (TERM, the grace, KILL). gone is true when nothing of it runs after.
func (host machine) stopGroup(table map[int]procs.Process, group int) (gone bool, err error) {
	tree := groupTree(table, group)
	result, err := host.system().Stop(tree, proctree.Targets{Groups: []int{group}, Members: true}, stopGrace)
	if err != nil {
		return false, err
	}
	return len(result.Survivors) == 0 && !result.KillWithheld, nil
}

// temporaryFolders hold nothing a stop could leave half written that anyone keeps.
var temporaryFolders = []string{"/tmp/", "/private/tmp/", "/var/folders/", "/private/var/folders/", "/var/tmp/",
	"/private/var/tmp/", "/dev/"}

// lsofBound ends an lsof that does not answer.
const lsofBound = 20 * time.Second

// heldFiles asks lsof what the group and the other processes hold open for writing (lsof's
// selections are or-ed); looked is false when it gave no answer.
func heldFiles(group int, others []int) (files []string, looked bool) {
	bound, cancel := context.WithTimeout(context.Background(), lsofBound)
	defer cancel()
	args := []string{"-n", "-P", "-g", strconv.Itoa(group)}
	if len(others) > 0 {
		ids := make([]string, 0, len(others))
		for _, pid := range others {
			ids = append(ids, strconv.Itoa(pid))
		}
		args = append(args, "-p", strings.Join(ids, ","))
	}
	output, err := exec.CommandContext(bound, "lsof", append(args, "-F", "pfatn")...).Output() // #nosec G204 -- lsof with ids
	var exited *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exited) && exited.ExitCode() == 1 && strings.TrimSpace(string(output)) != "":
		// 1 with rows: some of what was asked could not be shown; what was is the answer.
	default:
		return nil, false
	}
	return parseHeld(string(output), temporaryPrefixes(os.Getenv("TMPDIR"))), true
}

// temporaryPrefixes are the temporary folders, with the account's own TMPDIR.
func temporaryPrefixes(tmpdir string) []string {
	prefixes := append([]string(nil), temporaryFolders...)
	if tmpdir != "" {
		prefixes = append(prefixes, strings.TrimRight(tmpdir, "/")+"/")
	}
	return prefixes
}

// parseHeld reads lsof's `-F pfatn` fields: the regular files open for writing from descriptor 3
// on (standard output and error are where a watcher's output goes), outside the temporary folders;
// each path once, sorted.
func parseHeld(output string, temporary []string) []string {
	seen := map[string]bool{}
	descriptor, access, kind := "", "", ""
	for line := range strings.SplitSeq(output, "\n") {
		if line == "" {
			continue
		}
		tag, value := line[0], line[1:]
		switch tag {
		case 'f':
			descriptor, access, kind = value, "", ""
		case 'a':
			access = value
		case 't':
			kind = value
		case 'n':
			number, err := strconv.Atoi(descriptor)
			if err != nil || number < 3 || (access != "w" && access != "u") || kind != "REG" || under(value, temporary) {
				continue
			}
			seen[value] = true
		}
	}
	files := make([]string, 0, len(seen))
	for path := range seen {
		files = append(files, path)
	}
	sort.Strings(files)
	return files
}

func under(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
