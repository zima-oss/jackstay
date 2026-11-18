package supervise

import (
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/proctree"
)

// fakeClock moves only when slept on (W-013): After fires once the time has reached it.
type fakeClock struct {
	mutex  sync.Mutex
	now    time.Time
	timers []fakeTimer
	// waiting hears each timer set, so a test moves the time only once a wait is in place.
	waiting chan struct{}
}

type fakeTimer struct {
	at      time.Time
	channel chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_800_000_000, 0), waiting: make(chan struct{}, 256)}
}

func (c *fakeClock) Now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(pause time.Duration) { c.advance(pause) }

func (c *fakeClock) After(pause time.Duration) <-chan time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	channel := make(chan time.Time, 1)
	if pause <= 0 {
		channel <- c.now
		return channel
	}
	c.timers = append(c.timers, fakeTimer{at: c.now.Add(pause), channel: channel})
	select {
	case c.waiting <- struct{}{}:
	default:
	}
	return channel
}

func (c *fakeClock) advance(pause time.Duration) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.now = c.now.Add(pause)
	kept := c.timers[:0]
	for _, timer := range c.timers {
		if !timer.at.After(c.now) {
			timer.channel <- c.now
			continue
		}
		kept = append(kept, timer)
	}
	c.timers = kept
}

// fakeProcess is one process of the fake world.
type fakeProcess struct {
	row          procs.Process
	ignoresTerm  bool
	unkillable   bool
	alive        bool
	signalsHeard []syscall.Signal
	// zombie is a process that ended and is not reaped: in the table, deaf to signals.
	zombie bool
	// replacement comes alive when this process ends by a signal: what a leftover starts on its way out.
	replacement *fakeProcess
}

// world is a fake machine: its table and what signals do to it.
type world struct {
	mutex     sync.Mutex
	processes map[int]*fakeProcess
	sent      []sent
	clock     *fakeClock
	uid       int
	readFails bool
	reaped    []int
}

type sent struct {
	target int
	sig    syscall.Signal
	at     time.Time
}

func newWorld(clock *fakeClock) *world {
	return &world{processes: map[int]*fakeProcess{}, clock: clock, uid: 501}
}

func (w *world) add(pid, parent, group, session int, name string) *fakeProcess {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	process := &fakeProcess{alive: true, row: procs.Process{PID: pid, Parent: parent, Group: group, Session: session,
		UID: w.uid, Start: 1000 + int64(pid), StartMicros: (1000 + int64(pid)) * 1_000_000, Name: name}}
	w.processes[pid] = process
	return process
}

func (w *world) read() (map[int]procs.Process, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	if w.readFails {
		return nil, &procs.TableError{Err: unix.EIO}
	}
	table := map[int]procs.Process{}
	for pid, process := range w.processes {
		switch {
		case process.alive:
			table[pid] = process.row
		case process.zombie:
			row := process.row
			row.Zombie = true
			table[pid] = row
		}
	}
	return table, nil
}

func (w *world) signal(target int, sig syscall.Signal) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.sent = append(w.sent, sent{target: target, sig: sig, at: w.clock.Now()})
	var hit []*fakeProcess
	zombies := 0
	for pid, process := range w.processes {
		if !process.alive && !process.zombie {
			continue
		}
		if (target < 0 && process.row.Group == -target) || pid == target {
			if process.zombie {
				zombies++
				continue
			}
			hit = append(hit, process)
		}
	}
	if len(hit) == 0 && zombies == 0 {
		return unix.ESRCH
	}
	refused := false
	for _, process := range hit {
		if process.row.UID != w.uid {
			refused = true
			continue
		}
		process.signalsHeard = append(process.signalsHeard, sig)
		ends := (sig == unix.SIGKILL && !process.unkillable) ||
			(sig != unix.SIGKILL && sig != 0 && !process.ignoresTerm && !process.unkillable)
		if ends {
			process.alive = false
			if process.replacement != nil {
				process.replacement.alive = true
				w.processes[process.replacement.row.PID] = process.replacement
			}
		}
	}
	if refused {
		return unix.EPERM
	}
	return nil
}

func (w *world) sentSignals() []sent {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return append([]sent(nil), w.sent...)
}

func (w *world) system() *proctree.System {
	return &proctree.System{Read: w.read, Signal: w.signal, Clock: w.clock, Poll: 100 * time.Millisecond,
		Settle: 5 * time.Second, UID: w.uid}
}

// end makes a process a zombie: ended, not reaped.
func (w *world) end(pid int) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.processes[pid].alive = false
	w.processes[pid].zombie = true
}

// reap takes a zombie out of the table; living says what of its group still ran then.
func (w *world) reap(pid int) (living []int) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	group := w.processes[pid].row.Group
	delete(w.processes, pid)
	w.reaped = append(w.reaped, pid)
	for other, process := range w.processes {
		if process.alive && process.row.Group == group {
			living = append(living, other)
		}
	}
	return living
}

// later is a process that is not alive yet: a replacement to come.
func (w *world) later(pid, parent, group, session int, name string) *fakeProcess {
	return &fakeProcess{row: procs.Process{PID: pid, Parent: parent, Group: group, Session: session, UID: w.uid,
		Start: 1000 + int64(pid), StartMicros: (1000 + int64(pid)) * 1_000_000, Name: name}}
}
