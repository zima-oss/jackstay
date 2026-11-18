package supervise

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// sayCapacity bounds the lines of supervise's own waiting to be written.
const sayCapacity = 256

// sayBound is how long the lines still queued at a run's end may take to be written.
const sayBound = 2 * time.Second

// sayQueue writes supervise's own lines from a goroutine of its own: the command may share the
// stream, and when nobody reads it a write blocks, which must never hold up a stop. A line that
// finds the queue full is dropped and counted.
type sayQueue struct {
	mutex   sync.Mutex
	lines   chan string
	closed  bool
	dropped int
	done    chan struct{}
}

func newSayQueue(out io.Writer) *sayQueue {
	queue := &sayQueue{lines: make(chan string, sayCapacity), done: make(chan struct{})}
	go func() {
		defer close(queue.done)
		for line := range queue.lines {
			if out != nil {
				_, _ = io.WriteString(out, line)
			}
		}
	}()
	return queue
}

// Write queues one line; it never blocks.
func (queue *sayQueue) Write(data []byte) (int, error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	if queue.closed {
		queue.dropped++
		return len(data), nil
	}
	select {
	case queue.lines <- string(data):
	default:
		queue.dropped++
	}
	return len(data), nil
}

// close takes no more lines, waits until bound fires for the queued ones to be written (a
// WARNING comes before the caller's outcome line), then stops waiting: a stream nobody reads must
// not hold the run's end, and the lock it holds, for ever — the lines still queued then are lost
// when the process ends. It says how many were dropped, counting those.
func (queue *sayQueue) close(bound <-chan time.Time) int {
	queue.mutex.Lock()
	if !queue.closed {
		queue.closed = true
		close(queue.lines)
	}
	queue.mutex.Unlock()
	select {
	case <-queue.done:
	case <-bound:
		queue.mutex.Lock()
		queue.dropped += len(queue.lines)
		queue.mutex.Unlock()
	}
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	return queue.dropped
}

// sayer writes one line, prefixed, to out (nil: nowhere).
func sayer(out io.Writer, prefix string) func(format string, values ...any) {
	return func(format string, values ...any) {
		if out == nil {
			return
		}
		_, _ = io.WriteString(out, fmt.Sprintf(prefix+": "+format+"\n", values...))
	}
}
