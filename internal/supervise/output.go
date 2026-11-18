package supervise

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
)

// A Stream is one of the command's two outputs.
type Stream int

const (
	// Stdout is the command's standard output.
	Stdout Stream = iota + 1
	// Stderr is the command's standard error.
	Stderr
)

func (stream Stream) String() string {
	if stream == Stderr {
		return "stderr"
	}
	return "stdout"
}

// A Sink hears the command's output line by line, escape sequences removed, from one stream at a
// time (calls never overlap). The judge and the kept output plug in here. Line must return
// promptly: the readers call it themselves, so a slow Line holds the command's output back, and
// the run joins the readers after the bound (closing the pipes ends a read, not a Line call). An
// error is kept as a read problem; the stream is still read to its end.
type Sink interface {
	Line(stream Stream, text string) error
}

// escapes are the terminal's escape sequences: CSI, OSC (ended by BEL or ST, or cut off), and the
// two-character ones.
var escapes = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)?|[@-Z\\^_])`)

// Clean is a line of output without its escape sequences and its end.
func Clean(line string) string {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return escapes.ReplaceAllString(line, "")
}

// maxLine bounds one line given to the sink: a longer line is given in pieces of maxLine bytes,
// and the run's output then counts as not wholly read (OverlongLine), since a pattern may span
// two pieces.
const maxLine = 1 << 20

// lineReader reads one stream to its end into the sink, or, with raw, into raw unchanged.
type lineReader struct {
	file   *os.File
	stream Stream
	sink   *lockedSink
	raw    io.Writer
	// overlong says a line was longer than maxLine and was given in pieces (read once the reader
	// is joined).
	overlong bool
}

// read gives each line to the sink (or the bytes to raw) until the end of the stream; a closed
// file (the bound after the stop was over) is an error.
func (reader *lineReader) read() error {
	if reader.raw != nil {
		return reader.copyRaw()
	}
	buffered := bufio.NewReaderSize(reader.file, 64<<10)
	var line []byte
	for {
		chunk, err := buffered.ReadSlice('\n')
		line = append(line, chunk...)
		complete := !errors.Is(err, bufio.ErrBufferFull)
		body := len(line)
		if complete {
			body = len(bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r")))
		}
		for ; body > maxLine; body -= maxLine {
			reader.overlong = true
			reader.sink.line(reader.stream, Clean(string(line[:maxLine])))
			line = append(line[:0], line[maxLine:]...)
		}
		if !complete {
			continue
		}
		if len(line) > 0 && (err == nil || errors.Is(err, io.EOF)) {
			reader.sink.line(reader.stream, Clean(string(line)))
		}
		line = line[:0]
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// copyRaw gives the stream's bytes to raw as they come; raw's first error is kept and the rest of
// the stream is read and dropped, so the command is never held by a full pipe.
func (reader *lineReader) copyRaw() error {
	buffer := make([]byte, 32<<10)
	failed := false
	for {
		count, err := reader.file.Read(buffer)
		if count > 0 && !failed {
			if _, writeErr := reader.raw.Write(buffer[:count]); writeErr != nil {
				failed = true
				reader.sink.keep(writeErr)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// lockedSink makes the two streams' calls one at a time and keeps the sink's first error.
type lockedSink struct {
	mutex   sync.Mutex
	sink    Sink
	problem error
}

func (locked *lockedSink) line(stream Stream, text string) {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	if err := locked.sink.Line(stream, text); err != nil && locked.problem == nil {
		locked.problem = err
	}
}

// keep keeps err when it is the first problem.
func (locked *lockedSink) keep(err error) {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	if locked.problem == nil {
		locked.problem = err
	}
}

func (locked *lockedSink) firstProblem() error {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.problem
}
