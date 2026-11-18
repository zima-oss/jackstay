package scopes

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/proctree"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// settle is how long a session has after its command ended by itself to empty on its own before
// what is left is stopped (testing.md "Details of the behaviour": it bounds a wait for an event and decides nothing by time).
const settle = time.Second

// retryStop is the pause before a stop the group could not take is asked again.
const retryStop = 50 * time.Millisecond

// launch is one command to run in a session of its own: the spec, its timeout (zero: none) and
// what to do when jackstay begins its own stop at the timeout (before the stop).
type launch struct {
	spec     supervise.Spec
	timeout  time.Duration
	stopping func()
}

// launched is how a launch ended: the outcome, whether jackstay stopped it at its timeout (before it
// ended by itself), and what that stop signalled (`name[pid],…`).
type launched struct {
	outcome  supervise.Outcome
	timedOut bool
	killed   string
}

// superviseLaunch runs the command under supervise; at the timeout the whole session is stopped
// as a request to stop would be (SIGTERM, SIGKILL after the grace).
func superviseLaunch(signals *supervise.Signals, order launch) (launched, error) {
	spec := order.spec
	clock := spec.Clock
	if clock == nil {
		clock = supervise.RealClock()
	}
	var result launched
	var mutex sync.Mutex
	var stoppedAt time.Time
	done := make(chan struct{})
	var watching errgroup.Group
	if order.timeout > 0 {
		spec.Control = func(control *supervise.Control) {
			watching.Go(func() error {
				select {
				case <-done:
					return nil
				case <-clock.After(order.timeout):
				}
				reachedAt := clock.Now()
				if order.stopping != nil {
					order.stopping()
				}
				group, others := control.Members()
				killed := describeMembers(spec.System, group, others)
				for !control.Stop() {
					select {
					case <-done:
						return nil
					case <-clock.After(retryStop):
					}
				}
				mutex.Lock()
				stoppedAt, result.killed = reachedAt, killed
				mutex.Unlock()
				return nil
			})
		}
	}
	outcome, err := supervise.Run(signals, spec)
	close(done)
	_ = watching.Wait()
	result.outcome = outcome
	mutex.Lock()
	defer mutex.Unlock()
	// A command that ended by itself before the timeout's stop was not timed out, whatever the
	// stop then cleared.
	result.timedOut = !stoppedAt.IsZero() && (outcome.Ended.IsZero() || !outcome.Ended.Before(stoppedAt))
	if !result.timedOut {
		result.killed = ""
	}
	return result, err
}

// describeMembers names a stop's targets as `name[pid],…` from the table (by id when it cannot be
// read).
func describeMembers(system *proctree.System, group int, others []int) string {
	read := procs.ReadTable
	if system != nil && system.Read != nil {
		read = system.Read
	}
	table, err := read()
	var rows []procs.Process
	if err != nil {
		rows = append(rows, procs.Process{PID: group, Name: "process group"})
		for _, pid := range others {
			rows = append(rows, procs.Process{PID: pid, Name: "process"})
		}
		return supervise.Names(rows)
	}
	wanted := map[int]bool{group: true}
	for _, pid := range others {
		wanted[pid] = true
	}
	for pid, row := range table {
		if !row.Zombie && (wanted[pid] || row.Group == group) {
			rows = append(rows, row)
		}
	}
	return supervise.Names(rows)
}

// unreadableSystem is the machine's, except that its process table cannot be read
// (JACKSTAY_PROC_UNREADABLE, tests only).
func unreadableSystem() *proctree.System {
	system := proctree.Real()
	system.Poll = supervise.DefaultPoll
	system.Read = func() (map[int]procs.Process, error) {
		return nil, &procs.TableError{Err: errors.New(unreadableVariable + " is set")}
	}
	return &system
}

// blindOf says the run's process table could not be read: its end cannot be confirmed.
func blindOf(system *proctree.System, outcome supervise.Outcome) bool {
	for _, problem := range outcome.Problems {
		var tableErr *procs.TableError
		if errors.As(problem, &tableErr) {
			return true
		}
	}
	read := procs.ReadTable
	if system != nil && system.Read != nil {
		read = system.Read
	}
	_, err := read()
	return err != nil
}

// survivorNames are survivors as `name[pid],…`.
func survivorNames(survivors []proctree.Survivor) string {
	rows := make([]procs.Process, 0, len(survivors))
	for _, survivor := range survivors {
		rows = append(rows, procs.Process{PID: survivor.PID, Name: survivor.Name})
	}
	return supervise.Names(rows)
}
