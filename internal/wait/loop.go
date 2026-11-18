package wait

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

const (
	firstPause = 200 * time.Millisecond
	longPause  = 5 * time.Second
	// tailWindow is how much of the file's end is read for its last lines.
	tailWindow = 64 * 1024
)

// A follower reads the lines of a file as it grows; the part of a line not yet ended is kept
// until it ends. A file cut short, replaced by another (a new inode), or written over in place
// without growing is read again from its start.
type follower struct {
	path   string
	offset int64
	rest   []byte
	// size is the size last seen, -1 before the file was seen.
	size int64
	// changedAt is when the file was last seen to change (size, identity or modification time).
	changedAt time.Time
	known     bool
	device    string // the file system's device, compared for equality only: its type differs by platform
	inode     uint64
	modified  int64
}

type fileState struct {
	size     int64
	device   string
	inode    uint64
	modified int64
}

func stateOf(path string) (fileState, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return fileState{}, false
	}
	state := fileState{size: info.Size(), modified: info.ModTime().UnixNano()}
	if system, ok := info.Sys().(*syscall.Stat_t); ok {
		state.device, state.inode = fmt.Sprint(system.Dev), system.Ino
	}
	return state, true
}

// newFollower starts at the file's start, or at its end when only new lines count.
func newFollower(path string, fromEnd bool, now time.Time) *follower {
	reader := &follower{path: path, size: -1, changedAt: now}
	if fromEnd {
		if state, ok := stateOf(path); ok {
			reader.offset, reader.known = state.size, true
			reader.device, reader.inode, reader.modified = state.device, state.inode, state.modified
		}
	}
	return reader
}

// lines are the lines ended since the last call.
func (reader *follower) lines(now time.Time) []string {
	state, ok := stateOf(reader.path)
	if !ok {
		return nil
	}
	replaced := reader.known && (state.device != reader.device || state.inode != reader.inode)
	rewritten := reader.known && state.modified != reader.modified && state.size <= reader.offset
	if state.size != reader.size || replaced || (reader.known && state.modified != reader.modified) {
		reader.size, reader.changedAt = state.size, now
	}
	if state.size < reader.offset || replaced || rewritten {
		reader.offset, reader.rest = 0, nil
	}
	reader.known = true
	reader.device, reader.inode, reader.modified = state.device, state.inode, state.modified
	if state.size == reader.offset {
		return nil
	}
	data, err := readAt(reader.path, reader.offset, state.size-reader.offset)
	if err != nil {
		return nil
	}
	reader.offset += int64(len(data))
	pieces := bytes.Split(append(reader.rest, data...), []byte("\n"))
	reader.rest = pieces[len(pieces)-1]
	found := make([]string, 0, len(pieces)-1)
	for _, piece := range pieces[:len(pieces)-1] {
		found = append(found, text(piece))
	}
	return found
}

// pending is the part of a line not yet ended, "" when there is none.
func (reader *follower) pending() string {
	return text(reader.rest)
}

func text(piece []byte) string {
	return strings.ToValidUTF8(string(piece), "�")
}

func readAt(path string, offset, length int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data := make([]byte, length)
	count, err := file.ReadAt(data, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return data[:count], nil
}

// lastLines are the file's last count lines.
func lastLines(path string, count int) []string {
	if count <= 0 || path == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	start := max(0, info.Size()-tailWindow)
	data := make([]byte, info.Size()-start)
	read, err := file.ReadAt(data, start)
	if err != nil && err != io.EOF {
		return nil
	}
	body := strings.TrimRight(text(data[:read]), "\n")
	if body == "" {
		return nil
	}
	lines := strings.Split(body, "\n")
	return lines[max(0, len(lines)-count):]
}

// waiting is one wait under way.
type waiting struct {
	asked   request
	env     world
	out     io.Writer
	reader  *follower
	started time.Time
}

func (wait *waiting) elapsed() string {
	return fmt.Sprintf("%.0f", wait.env.Now().Sub(wait.started).Seconds())
}

// leave says why the wait ended and the file's last lines, and gives the exit code.
func (wait *waiting) leave(code int, words string) int {
	fmt.Fprintln(wait.out, words)
	for _, line := range lastLines(wait.asked.within, wait.asked.tail) {
		fmt.Fprintln(wait.out, "  | "+line)
	}
	return code
}

// judge looks at lines read together: a --fail line anywhere among them wins over a wanted line
// beside it.
func (wait *waiting) judge(lines []string) (int, bool) {
	if wait.asked.failing != nil {
		for _, line := range lines {
			if wait.asked.failing.MatchString(line) {
				return wait.leave(1, fmt.Sprintf("jst wait: a line matched --fail after %s s: %s", wait.elapsed(), line)), true
			}
		}
	}
	if wait.asked.wanted != nil {
		for _, line := range lines {
			if wait.asked.wanted.MatchString(line) {
				fmt.Fprintln(wait.out, line)
				return wait.leave(0, fmt.Sprintf("jst wait: the line came after %s s", wait.elapsed())), true
			}
		}
	}
	return 0, false
}

// lastWords are the lines not yet read and the line not yet ended: what a process that has ended
// wrote last.
func (wait *waiting) lastWords() []string {
	lines := wait.reader.lines(wait.env.Now())
	if rest := wait.reader.pending(); rest != "" {
		lines = append(lines, rest)
	}
	return lines
}

func (wait *waiting) exists() bool {
	info, err := os.Stat(wait.asked.file)
	return err == nil && (!wait.asked.nonempty || info.Size() > 0)
}

func (wait *waiting) gone() bool {
	_, err := os.Lstat(wait.asked.gone)
	return os.IsNotExist(err)
}

// reached says whether a file condition holds, and the words for it.
func (wait *waiting) reached() (string, bool) {
	switch {
	case wait.asked.mode == modeFile && wait.exists():
		return fmt.Sprintf("jst wait: %s is there (%s s)", wait.asked.file, wait.elapsed()), true
	case wait.asked.mode == modeGone && wait.gone():
		return fmt.Sprintf("jst wait: %s is gone (%s s)", wait.asked.gone, wait.elapsed()), true
	}
	return "", false
}

func (wait *waiting) missed() string {
	timeout := fmt.Sprintf("%.0f", wait.asked.timeout.Seconds())
	switch wait.asked.mode {
	case modePID:
		return fmt.Sprintf("jst wait: process %d still runs after %s s", wait.asked.pid, timeout)
	case modeFile:
		return fmt.Sprintf("jst wait: %s is not there after %s s", wait.asked.file, timeout)
	case modeGone:
		return fmt.Sprintf("jst wait: %s is still there after %s s", wait.asked.gone, timeout)
	case modeLine:
		return fmt.Sprintf("jst wait: no line matched '%s' after %s s", wait.asked.line, timeout)
	default:
		return fmt.Sprintf("jst wait: %s still grows after %s s", wait.asked.within, timeout)
	}
}

// look is one look at everything; done says the wait is over with code.
func (wait *waiting) look() (code int, done bool) {
	if wait.reader != nil {
		lines := wait.reader.lines(wait.env.Now())
		// A --fail line counts as soon as its words are there, ended or not: a wanted line just
		// before it must not win while it waits for its line end.
		if rest := wait.reader.pending(); rest != "" && wait.asked.failing != nil && wait.asked.failing.MatchString(rest) {
			lines = append(lines, rest)
		}
		if code, done := wait.judge(lines); done {
			return code, true
		}
	}
	switch wait.asked.mode {
	case modePID:
		if !wait.env.Running(wait.asked.pid) {
			if wait.reader != nil && wait.asked.failing != nil {
				for _, line := range wait.lastWords() {
					if wait.asked.failing.MatchString(line) {
						return wait.leave(1, "jst wait: a line matched --fail: "+line), true
					}
				}
			}
			return wait.leave(0, fmt.Sprintf("jst wait: process %d has ended (%s s)", wait.asked.pid, wait.elapsed())), true
		}
	case modeQuiet:
		// Not cut short by --while-pid: once the writer is gone the file stops growing.
		if wait.reader.size >= 0 && wait.env.Now().Sub(wait.reader.changedAt) >= wait.asked.quiet {
			return wait.leave(0, fmt.Sprintf("jst wait: %s has not grown for %.0f s", wait.asked.within,
				wait.asked.quiet.Seconds())), true
		}
	default:
		if words, ok := wait.reached(); ok {
			return wait.leave(0, words), true
		}
		if wait.asked.whilePID > 0 && !wait.env.Running(wait.asked.whilePID) {
			// Looked at once more after the writer's end: what it did last still counts.
			if wait.reader != nil {
				if code, done := wait.judge(wait.lastWords()); done {
					return code, true
				}
			}
			if words, ok := wait.reached(); ok {
				return wait.leave(0, words), true
			}
			return wait.leave(1, fmt.Sprintf("jst wait: process %d ended before the %s it was waited for",
				wait.asked.whilePID, wait.asked.mode)), true
		}
	}
	return 0, false
}

// run waits as asked and gives the exit code.
func run(asked request, env world, out io.Writer) int {
	wait := &waiting{asked: asked, env: env, out: out}
	if asked.within != "" {
		wait.reader = newFollower(asked.within, asked.fresh, env.Now())
	}
	env.Ready()
	wait.started = env.Now()
	deadline := wait.started.Add(asked.timeout)
	pause := firstPause
	for {
		if code, done := wait.look(); done {
			return code
		}
		now := env.Now()
		if !now.Before(deadline) {
			return wait.leave(3, wait.missed())
		}
		env.Sleep(min(pause, deadline.Sub(now)))
		pause = min(pause*3/2, longPause)
	}
}
