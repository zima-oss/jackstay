// Package proctree is a tree of processes remembered by their session and parent links, and the
// one way jackstay stops one (docs/design/implementation.md section 5.2). A process is known by its id and its
// start: an id the kernel gives again to a later process is never signalled. A group or a session
// is signalled only while a remembered member is still in it.
package proctree

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/procs"
)

// A Member is a process the tree remembers: its id and its start in microseconds.
type Member struct {
	PID   int
	Start int64
}

// A Tree is the processes remembered as one command's: those named to it, the descendants found
// by parent links and the processes of the sessions it follows.
type Tree struct {
	members  map[int]int64
	sessions map[int]bool
}

// New is an empty tree.
func New() *Tree {
	return &Tree{members: map[int]int64{}, sessions: map[int]bool{}}
}

// Remember adds a process by its id and its start in microseconds (procs.Process.StartMicros).
func (tree *Tree) Remember(pid int, start int64) {
	if pid > 0 {
		tree.members[pid] = start
	}
}

// FollowSession makes every process of the session a member when the tree is sampled, while a
// remembered member is still in it.
func (tree *Tree) FollowSession(session int) {
	if session > 0 {
		tree.sessions[session] = true
	}
}

// Members are the remembered processes, by id.
func (tree *Tree) Members() []Member {
	found := make([]Member, 0, len(tree.members))
	for pid, start := range tree.members {
		found = append(found, Member{PID: pid, Start: start})
	}
	sort.Slice(found, func(left, right int) bool { return found[left].PID < found[right].PID })
	return found
}

// running says whether the table's row of pid is the remembered member and still runs.
func (tree *Tree) running(table map[int]procs.Process, pid int) (procs.Process, bool) {
	start, remembered := tree.members[pid]
	row, present := table[pid]
	if !remembered || !present || row.StartMicros != start || row.Zombie {
		return procs.Process{}, false
	}
	return row, true
}

// Living are the remembered members that still run, as the table shows them, by id.
func (tree *Tree) Living(table map[int]procs.Process) []procs.Process {
	var living []procs.Process
	for pid := range tree.members {
		if row, ok := tree.running(table, pid); ok {
			living = append(living, row)
		}
	}
	sort.Slice(living, func(left, right int) bool { return living[left].PID < living[right].PID })
	return living
}

// Sample remembers what the table shows of the tree now: each running child of a running member,
// and each process of a followed session a running member is still in, until nothing is added.
func (tree *Tree) Sample(table map[int]procs.Process) {
	for {
		occupied := map[int]bool{}
		for _, row := range tree.Living(table) {
			if row.Session > 0 && tree.sessions[row.Session] {
				occupied[row.Session] = true
			}
		}
		added := false
		for pid, row := range table {
			if row.Zombie || pid <= 1 {
				continue
			}
			if _, known := tree.members[pid]; known {
				continue
			}
			_, parentRuns := tree.running(table, row.Parent)
			if parentRuns || (row.Session > 0 && occupied[row.Session]) {
				tree.members[pid] = row.StartMicros
				added = true
			}
		}
		if !added {
			return
		}
	}
}

// Targets are what a stop signals.
type Targets struct {
	// Groups are signalled whole, each while a running remembered member is in it.
	Groups []int
	// Members has every running remembered member signalled by its own id.
	Members bool
}

// A Refusal says why a signal was not delivered.
type Refusal int

const (
	// SandboxForbids is a process of this account the caller may not signal: the agent sandbox.
	SandboxForbids Refusal = iota + 1
	// NotThisAccount is a process of another account (sudo, another user).
	NotThisAccount
	// OtherFailure is any other refusal, kept in Err.
	OtherFailure
)

// A SignalError is a signal the kernel refused.
type SignalError struct {
	PID int
	// Group says PID is a process group's id.
	Group bool
	Kind  Refusal
	Err   error
}

func (problem *SignalError) Error() string {
	what := fmt.Sprintf("process %d", problem.PID)
	if problem.Group {
		what = fmt.Sprintf("process group %d", problem.PID)
	}
	switch problem.Kind {
	case SandboxForbids:
		return what + ": the sandbox cannot signal it from here: run outside the sandbox"
	case NotThisAccount:
		return what + ": not this account's"
	default:
		return fmt.Sprintf("%s: cannot be signalled: %v", what, problem.Err)
	}
}

func (problem *SignalError) Unwrap() error { return problem.Err }

// A Survivor is a member still running when the stop gave up.
type Survivor struct {
	Member
	Name string
}

// A Result is what a stop left: the members still running and the signals refused.
type Result struct {
	Survivors []Survivor
	Refused   []*SignalError
	// KillWithheld says KILL was due but the table could not be read just before it, so no target
	// could be told from a later process of its id: nothing was killed and the stop is not
	// complete (Survivors are as last seen).
	KillWithheld bool
}

// A Clock tells the time and pauses; tests give one that moves only when told.
type Clock interface {
	Now() time.Time
	Sleep(pause time.Duration)
}

type realClock struct{}

func (realClock) Now() time.Time            { return time.Now() }
func (realClock) Sleep(pause time.Duration) { time.Sleep(pause) }

// A System is what a stop reads and signals through.
type System struct {
	// Read is the process table.
	Read func() (map[int]procs.Process, error)
	// Signal sends a signal to a process, or to a process group when target is negative (kill(2)).
	Signal func(target int, signal syscall.Signal) error
	Clock  Clock
	// Poll is the pause between two reads of the table while waiting.
	Poll time.Duration
	// Settle is how long the stop waits after KILL for the killed to leave the table.
	Settle time.Duration
	// UID is the account this process runs as.
	UID int
}

// Real is the system of this machine.
func Real() System {
	return System{Read: procs.ReadTableWithSessions, Signal: unix.Kill, Clock: realClock{}, Poll: 100 * time.Millisecond,
		Settle: 2 * time.Second, UID: os.Getuid()}
}

// Stop sends TERM to the targets, reads the table again while it waits for them to end, sends
// KILL after the grace to the targets a fresh read shows still running, and names the members
// still running. It fails only when the table cannot be read at its start: without it no member
// can be told from a later process of its id. When the read before KILL fails, no KILL is sent
// (Result.KillWithheld).
func (system System) Stop(tree *Tree, targets Targets, grace time.Duration) (Result, error) {
	table, err := system.Read()
	if err != nil {
		return Result{}, err
	}
	stop := &stopping{system: system, tree: tree, targets: targets, refused: map[string]*SignalError{},
		termed: map[int]bool{}}
	tree.Sample(table)
	stop.signal(table, unix.SIGTERM)
	table = stop.waitWhileRunning(table, grace, unix.SIGTERM)
	result := Result{}
	if len(tree.Living(table)) > 0 {
		// The last table may be older than a failed read: an id it shows may since belong to a
		// later process. KILL goes only by a table read now.
		fresh, err := system.Read()
		if err != nil {
			result.KillWithheld = true
		} else {
			table = fresh
			tree.Sample(table)
			stop.signal(table, unix.SIGKILL)
			table = stop.waitWhileRunning(table, system.Settle, 0)
		}
	}
	for _, row := range tree.Living(table) {
		result.Survivors = append(result.Survivors, Survivor{Member: Member{PID: row.PID, Start: row.StartMicros}, Name: row.Name})
	}
	keys := make([]string, 0, len(stop.refused))
	for key := range stop.refused {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result.Refused = append(result.Refused, stop.refused[key])
	}
	return result, nil
}

type stopping struct {
	system  System
	tree    *Tree
	targets Targets
	refused map[string]*SignalError
	// termed are the members TERM was sent to, so a member found later is sent it once too.
	termed map[int]bool
}

// waitWhileRunning reads the table again until no member runs or the pause is over; with a
// signal, each member found meanwhile is sent it once. It returns the last table read.
func (stop *stopping) waitWhileRunning(table map[int]procs.Process, pause time.Duration,
	signal syscall.Signal) map[int]procs.Process {
	deadline := stop.system.Clock.Now().Add(pause)
	for len(stop.tree.Living(table)) > 0 {
		now := stop.system.Clock.Now()
		if !now.Before(deadline) {
			break
		}
		stop.system.Clock.Sleep(min(stop.system.Poll, deadline.Sub(now)))
		fresh, err := stop.system.Read()
		if err != nil {
			// The table could not be read this once: keep the last view and look again.
			continue
		}
		table = fresh
		stop.tree.Sample(table)
		if signal != 0 {
			stop.signal(table, signal)
		}
	}
	return table
}

// signal sends sig to the targets the table shows still running; TERM goes to each member once.
func (stop *stopping) signal(table map[int]procs.Process, sig syscall.Signal) {
	living := stop.tree.Living(table)
	for _, group := range stop.targets.Groups {
		if group <= 1 {
			continue
		}
		for _, row := range living {
			if row.Group != group {
				continue
			}
			if sig == unix.SIGTERM && stop.termed[-group] {
				break
			}
			stop.termed[-group] = true
			stop.send(-group, row.UID, sig)
			break
		}
	}
	if !stop.targets.Members {
		return
	}
	for _, row := range living {
		if sig == unix.SIGTERM && stop.termed[row.PID] {
			continue
		}
		stop.termed[row.PID] = true
		stop.send(row.PID, row.UID, sig)
	}
}

// send delivers one signal; owner is the account of the process (or of the group's member).
func (stop *stopping) send(target, owner int, sig syscall.Signal) {
	err := stop.system.Signal(target, sig)
	if err == nil || errors.Is(err, unix.ESRCH) {
		return
	}
	problem := &SignalError{PID: target, Kind: OtherFailure, Err: err}
	if target < 0 {
		problem.PID, problem.Group = -target, true
	}
	if errors.Is(err, unix.EPERM) {
		problem.Kind = NotThisAccount
		if owner == stop.system.UID {
			problem.Kind = SandboxForbids
		}
	}
	stop.refused[fmt.Sprintf("%t/%d", problem.Group, problem.PID)] = problem
}
