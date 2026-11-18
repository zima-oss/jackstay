package supervise

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/telemetry"
)

// Watched are the requests to stop that a run passes on: INT, TERM, HUP and QUIT.
var Watched = []syscall.Signal{unix.SIGINT, unix.SIGTERM, unix.SIGHUP, unix.SIGQUIT}

// A StopRequest is the cause of a run's cancelled context: the first signal jackstay was asked to stop with.
type StopRequest struct {
	Signal syscall.Signal
}

func (request *StopRequest) Error() string {
	return "asked to stop (" + SignalName(request.Signal) + ")"
}

// SignalName is the signal's name as people write it (SIGTERM).
func SignalName(sig syscall.Signal) string {
	if name := unix.SignalName(sig); name != "" {
		return name
	}
	return "signal " + sig.String()
}

// Signals is the one loop that owns the requests to stop a run: each watched signal not ignored
// when jackstay started is caught (an ignored one stays ignored, here and in the command); the first is
// kept and cancels the context with a StopRequest; every one is passed on to the command once it
// exists (held until then), and a later one does not shorten any wait.
type Signals struct {
	incoming chan os.Signal
	context  context.Context
	cancel   context.CancelCauseFunc
	done     chan struct{}
	finished chan struct{}

	mutex   sync.Mutex
	first   syscall.Signal
	firstAt time.Time
	pending []syscall.Signal
	forward func(syscall.Signal)
	clock   Clock
}

// Listen starts the loop: signal.Notify (buffered) for each watched signal that signal.Ignored
// does not report as ignored. Close ends it.
func Listen(parent context.Context, clock Clock) *Signals {
	var caught []os.Signal
	for _, sig := range Watched {
		if !signal.Ignored(sig) {
			caught = append(caught, sig)
		}
	}
	signals := newSignals(parent, make(chan os.Signal, 16), clock)
	if len(caught) > 0 {
		signal.Notify(signals.incoming, caught...)
	}
	go signals.loop()
	return signals
}

// newSignals is the loop over a channel of its caller's (tests deliver into it).
func newSignals(parent context.Context, incoming chan os.Signal, clock Clock) *Signals {
	if clock == nil {
		clock = RealClock()
	}
	runContext, cancel := context.WithCancelCause(parent)
	return &Signals{incoming: incoming, context: runContext, cancel: cancel, done: make(chan struct{}),
		finished: make(chan struct{}), clock: clock}
}

func (signals *Signals) loop() {
	defer close(signals.finished)
	for {
		select {
		case received := <-signals.incoming:
			if sig, ok := received.(syscall.Signal); ok {
				signals.receive(sig)
			}
		case <-signals.done:
			return
		}
	}
}

// receive keeps the first request, cancels the context with it, and passes the signal on.
func (signals *Signals) receive(sig syscall.Signal) {
	signals.mutex.Lock()
	if signals.first == 0 {
		signals.first = sig
		signals.firstAt = signals.clock.Now()
		signals.cancel(&StopRequest{Signal: sig})
	}
	forward := signals.forward
	if forward == nil {
		signals.pending = append(signals.pending, sig)
	}
	signals.mutex.Unlock()
	if forward != nil {
		forward(sig)
	}
}

// Context is cancelled, with a *StopRequest as its cause, by the first request to stop.
func (signals *Signals) Context() context.Context { return signals.context }

// First is the first signal received and when it came; ok is false while none has.
func (signals *Signals) First() (sig syscall.Signal, at time.Time, ok bool) {
	signals.mutex.Lock()
	defer signals.mutex.Unlock()
	return signals.first, signals.firstAt, signals.first != 0
}

// attach makes forward hear every later signal, and at once those held while there was none.
func (signals *Signals) attach(forward func(syscall.Signal)) {
	signals.mutex.Lock()
	signals.forward = forward
	held := signals.pending
	signals.pending = nil
	signals.mutex.Unlock()
	for _, sig := range held {
		forward(sig)
	}
}

// detach stops passing signals on: what they were for is gone.
func (signals *Signals) detach() {
	signals.mutex.Lock()
	signals.forward = nil
	signals.mutex.Unlock()
}

// Close stops catching the signals and ends the loop. A signal that comes afterwards has its
// default effect.
func (signals *Signals) Close() {
	signal.Stop(signals.incoming)
	select {
	case <-signals.done:
	default:
		close(signals.done)
	}
	<-signals.finished
}

// endWait bounds the wait for a signal sent to this process to end it.
const endWait = 5 * time.Second

// EndBy ends this process by the signal it was asked to stop with, as a process that had not
// caught it: for INT, TERM and HUP the signal's default action is restored and the signal sent to
// this process (its caller sees 130, 143, 129). QUIT cannot be raised again without the Go
// runtime's dump, so its status is returned: 131. The returned status is the fallback should the
// signal not end the process.
func EndBy(sig syscall.Signal) int {
	// The process's spans end before it dies by the signal (observability.md, section 2).
	telemetry.FinishBySignal(sig)
	if sig == unix.SIGQUIT || sig <= 0 {
		return 128 + int(sig)
	}
	signal.Reset(sig)
	if err := unix.Kill(os.Getpid(), sig); err == nil {
		time.Sleep(endWait)
	}
	return 128 + int(sig)
}
