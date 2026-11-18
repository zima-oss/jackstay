package supervise

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// CaptureGrace is how long a captured command's group has between TERM and KILL at its deadline,
// how long what it left in its group may run on, and how long its output may still be read.
const CaptureGrace = 5 * time.Second

// CaptureCap is the most of each stream a capture keeps; the rest is read and dropped.
const CaptureCap = 16 << 20

// ErrDeadline is a captured command still running at its context's end: its group was stopped.
var ErrDeadline = errors.New("did not end before its deadline")

// Captured is what a captured command printed and how it ended.
type Captured struct {
	Stdout, Stderr []byte
	// Status is the status as a shell gives it (the exit code, or 128 and the signal).
	Status int
	// Incomplete says why the streams kept are not all the command wrote ("" when they are): a
	// stream over CaptureCap, or output not read to its end (Outcome.Unread). A caller that
	// trusts what a command said, and not only what it did, refuses an incomplete answer.
	Incomplete string
}

// Capture runs argv in a process group of its own (Group mode) under ctx: at the context's end
// the whole group is stopped (TERM, KILL after CaptureGrace) and ErrDeadline is returned; both
// streams are kept, each up to CaptureCap. Input, when not nil, is given on standard input and
// then closed; otherwise standard input is /dev/null. This is the one way a command that only
// asks a question of another program (a build, a probe, git) is run (docs/design/implementation.md
// section 7.3).
func Capture(ctx context.Context, argv []string, dir string, env []string, input []byte) (Captured, error) {
	stdout, stderr := &cappedBuffer{limit: CaptureCap}, &cappedBuffer{limit: CaptureCap}
	spec := Spec{Argv: argv, Dir: dir, Env: env, Mode: Group, Input: input, Context: ctx,
		Grace: CaptureGrace, KillWait: CaptureGrace, ReadBound: CaptureGrace,
		StdoutTo: stdout, StderrTo: stderr, Who: "jst"}
	outcome, err := Run(nil, spec)
	captured := Captured{Stdout: stdout.bytes(), Stderr: stderr.bytes(), Status: outcome.Status(), Incomplete: outcome.Unread}
	if captured.Incomplete == "" && (stdout.dropped() || stderr.dropped()) {
		captured.Incomplete = fmt.Sprintf("more than %d bytes written to a stream", CaptureCap)
	}
	if err != nil {
		return captured, err
	}
	if ctx.Err() != nil {
		return captured, ErrDeadline
	}
	return captured, nil
}

// cappedBuffer keeps the first limit bytes written to it and drops the rest without an error.
type cappedBuffer struct {
	mutex   sync.Mutex
	buffer  bytes.Buffer
	limit   int
	overrun bool
}

func (capped *cappedBuffer) Write(data []byte) (int, error) {
	capped.mutex.Lock()
	defer capped.mutex.Unlock()
	room := max(capped.limit-capped.buffer.Len(), 0)
	capped.buffer.Write(data[:min(room, len(data))])
	if len(data) > room {
		capped.overrun = true
	}
	return len(data), nil
}

// dropped says whether anything written was dropped.
func (capped *cappedBuffer) dropped() bool {
	capped.mutex.Lock()
	defer capped.mutex.Unlock()
	return capped.overrun
}

func (capped *cappedBuffer) bytes() []byte {
	capped.mutex.Lock()
	defer capped.mutex.Unlock()
	return bytes.Clone(capped.buffer.Bytes())
}
