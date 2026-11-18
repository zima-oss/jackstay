package supervise

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/proctree"
)

// cleared is what the clearing after the command's end found.
type cleared struct {
	left      []procs.Process
	survivors []proctree.Survivor
	refused   []*proctree.SignalError
	foreign   []procs.Process
	// unconfirmed says the last look could not read the table: the group was signalled by its
	// id, but whether anything of it was left cannot be told.
	unconfirmed bool
}

// clearAfterEnd clears the group (and session) after the command's end, reaps the command, and
// puts what it found into the outcome.
func (run *running) clearAfterEnd(outcome *Outcome, asked bool, deadline time.Time) {
	result := run.clear(asked, deadline)
	outcome.Left = result.left
	outcome.Survivors = append(outcome.Survivors, result.survivors...)
	outcome.Refused = result.refused
	outcome.Foreign = result.foreign
	if result.unconfirmed {
		outcome.Unconfirmed = true
		outcome.Problems = append(outcome.Problems, fmt.Errorf("the process list cannot be read here: process group %d "+
			"was stopped by its id, but whether anything of it is left cannot be told", run.pid))
	}
}

// what names the group or session in the words ("process group 123").
func (run *running) what() string {
	if run.spec.Mode == Group {
		return fmt.Sprintf("process group %d", run.pid)
	}
	return fmt.Sprintf("session %d", run.pid)
}

// look reads the table now and names the members by the run's own ids (the leader is unreaped);
// known is false when the table cannot be read.
func (run *running) look() (table map[int]procs.Process, members []procs.Process, known bool) {
	table, err := run.spec.System.Read()
	if err != nil {
		return nil, nil, false
	}
	run.mutex.Lock()
	defer run.mutex.Unlock()
	return table, run.members(table), true
}

// signalNow reads the table and sends sig to the group by its id and, from that same read only,
// to the members outside the group; when the read fails, to the group alone. The refusals are
// kept once each.
func (run *running) signalNow(sig syscall.Signal, result *cleared) {
	table, err := run.spec.System.Read()
	run.mutex.Lock()
	_, refused := run.signalAll(table, err == nil, sig)
	run.mutex.Unlock()
	for _, problem := range refused {
		known := false
		for _, seen := range result.refused {
			known = known || (seen.PID == problem.PID && seen.Group == problem.Group)
		}
		if !known {
			result.refused = append(result.refused, problem)
		}
	}
}

// waitEmpty looks every poll until the table shows nothing of the group left (or nothing this
// account can signal: another account's processes are not waited for) or end has come; it returns
// the last look.
func (run *running) waitEmpty(end time.Time, onLook func(table map[int]procs.Process, living []procs.Process)) ([]procs.Process, bool) {
	_, living, known := run.look()
	for !known || anyOf(living, run.spec.System.UID) {
		now := run.spec.Clock.Now()
		if !now.Before(end) {
			break
		}
		run.spec.Clock.Sleep(min(run.spec.System.Poll, end.Sub(now)))
		var table map[int]procs.Process
		table, living, known = run.look()
		if known && onLook != nil {
			onLook(table, living)
		}
	}
	return living, known
}

// clear sees that nothing of the command's group or session runs on once the command has ended
// (docs/design/implementation.md section 5.2), and reaps it — last, after the final signal and its wait, whatever was
// found: until then the group's (and session's) id is the leader's, so signals by it reach only
// the run's processes, and nothing is signalled after the reap. asked says a stop had reached the
// group already (jackstay was asked to stop, or the command died by a signal): then what is left has
// until deadline to end in its own way and is killed after it. Otherwise what is left after the
// settle is told, sent SIGTERM, given the grace, and killed. Empty is a table read showing no
// living member by the leader's ids; without a table nothing can be shown empty, so the whole
// stop is made by the group's id and the result says it is unconfirmed.
func (run *running) clear(asked bool, deadline time.Time) cleared {
	var result cleared
	defer run.reap()
	until := run.spec.Clock.Now().Add(run.spec.Settle)
	if asked && deadline.After(until) {
		until = deadline
	}
	living, known := run.waitEmpty(until, nil)
	if known && len(living) == 0 {
		return result
	}
	if known && !anyOf(living, run.spec.System.UID) {
		result.foreign = living
		run.say("WARNING: %s of the command's %s cannot be signalled by this account (not permitted: they are "+
			"another account's). This run ends all the same: they go on beside whatever starts next.", Names(living), run.what())
		return result
	}
	if !asked {
		if known {
			result.left = living
			run.say("the command ended and left %s running in its %s; stopping them (SIGTERM).", Names(living), run.what())
		} else {
			run.say("the process list cannot be read here, so what the command left in its %s cannot be named; "+
				"stopping it all the same (SIGTERM).", run.what())
		}
		run.signalNow(unix.SIGTERM, &result)
		termed := map[int]bool{}
		for _, row := range living {
			termed[row.PID] = true
		}
		living, known = run.waitEmpty(run.spec.Clock.Now().Add(run.spec.Grace), func(_ map[int]procs.Process, fresh []procs.Process) {
			for _, row := range fresh {
				if termed[row.PID] {
					continue
				}
				// Started meanwhile: told too, by its id from the read just made.
				termed[row.PID] = true
				if err := run.spec.System.Signal(row.PID, unix.SIGTERM); err != nil && !errors.Is(err, unix.ESRCH) {
					result.refused = append(result.refused, refusal(row.PID, row.UID, run.spec.System.UID, err))
				}
			}
		})
		if known && len(living) == 0 {
			return result
		}
	}
	if known {
		run.say("what is left of the command's %s did not stop within %s; killing it (SIGKILL).", run.what(),
			Seconds(run.spec.Grace))
	}
	run.signalNow(unix.SIGKILL, &result)
	living, known = run.waitEmpty(run.spec.Clock.Now().Add(run.spec.KillWait), nil)
	switch {
	case !known:
		result.unconfirmed = true
	case len(living) > 0:
		for _, row := range living {
			result.survivors = append(result.survivors, proctree.Survivor{Member: proctree.Member{PID: row.PID,
				Start: row.StartMicros}, Name: row.Name})
		}
		run.say("WARNING: %s of the command's %s are STILL RUNNING after SIGKILL. This run ends all the same: they go "+
			"on beside whatever starts next.", Names(living), run.what())
	}
	for _, refused := range result.refused {
		run.say("WARNING: %v", refused)
	}
	return result
}

// anyOf says whether one of the processes is the account's.
func anyOf(found []procs.Process, uid int) bool {
	for _, row := range found {
		if row.UID == uid {
			return true
		}
	}
	return false
}

// Names are processes as `name[pid],…`, by id.
func Names(found []procs.Process) string {
	sorted := append([]procs.Process(nil), found...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].PID < sorted[right].PID })
	names := make([]string, 0, len(sorted))
	for _, row := range sorted {
		names = append(names, fmt.Sprintf("%s[%d]", row.Name, row.PID))
	}
	return strings.Join(names, ",")
}

// Seconds is a pause as people read it (30 s, 0.5 s).
func Seconds(pause time.Duration) string {
	return strconv.FormatFloat(pause.Seconds(), 'f', -1, 64) + " s"
}
