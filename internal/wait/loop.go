package wait

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	readWindow = 64 * 1024
	maxLogLine = 1024 * 1024
)

var errNonRegular = errors.New("not a regular file")

// A follower reads the lines of a file as it grows; the part of a line not yet ended is kept
// until it ends. A file cut short, replaced by another (a new inode), or written over in place
// without growing is read again from its start.
type follower struct {
	path       string
	offset     int64
	snapshot   int64
	rest       []byte
	resetCount int
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

func stateOf(info fs.FileInfo) fileState {
	state := fileState{size: info.Size(), modified: info.ModTime().UnixNano()}
	if system, ok := info.Sys().(*syscall.Stat_t); ok {
		state.device, state.inode = fmt.Sprint(system.Dev), system.Ino
	}
	return state
}

// Open without blocking on a FIFO that appeared after Stat, then check the opened file.
func openRegular(path string) (*os.File, fs.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errNonRegular
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- the log the caller named
	if err != nil {
		return nil, nil, err
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errNonRegular
	}
	return file, info, nil
}

// newFollower starts at the file's start, or at its end when only new lines count.
func newFollower(path string, fromEnd bool, now time.Time) *follower {
	reader := &follower{path: path, size: -1, changedAt: now}
	if fromEnd {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			state := stateOf(info)
			reader.offset, reader.known = state.size, true
			reader.device, reader.inode, reader.modified = state.device, state.inode, state.modified
		}
	}
	return reader
}

// lines observes the file, and reads up to one window when there are patterns to match.
func (reader *follower) lines(now time.Time, scan bool) ([]string, error) {
	file, info, err := openRegular(reader.path)
	if err != nil {
		if reader.size >= 0 {
			reader.size, reader.changedAt = -1, now
			reader.offset, reader.snapshot, reader.rest = 0, 0, nil
			reader.resetCount++
		}
		return nil, nil
	}
	defer func() { _ = file.Close() }()
	state := stateOf(info)
	replaced := reader.known && (state.device != reader.device || state.inode != reader.inode)
	rewritten := reader.known && state.modified != reader.modified && state.size <= reader.offset
	if state.size != reader.size || replaced || (reader.known && state.modified != reader.modified) {
		reader.size, reader.changedAt = state.size, now
	}
	if state.size < reader.offset || replaced || rewritten {
		reader.offset, reader.snapshot, reader.rest = 0, 0, nil
		reader.resetCount++
	}
	reader.known = true
	reader.device, reader.inode, reader.modified = state.device, state.inode, state.modified
	if !scan || state.size == reader.offset {
		reader.snapshot = reader.offset
		return nil, nil
	}
	if reader.snapshot <= reader.offset || reader.snapshot > state.size {
		reader.snapshot = state.size
	}
	data, err := readAt(file, reader.offset, min(readWindow, reader.snapshot-reader.offset))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		// The file shrank while we were reading it. Do not judge a partial snapshot.
		reader.offset, reader.snapshot, reader.rest = 0, 0, nil
		reader.resetCount++
		return nil, nil
	}
	reader.offset += int64(len(data))
	pieces := bytes.Split(append(reader.rest, data...), []byte("\n"))
	for _, piece := range pieces {
		if len(piece) > maxLogLine {
			return nil, fmt.Errorf("a line in %s exceeds %d bytes", reader.path, maxLogLine)
		}
	}
	reader.rest = pieces[len(pieces)-1]
	found := make([]string, 0, len(pieces)-1)
	for _, piece := range pieces[:len(pieces)-1] {
		found = append(found, text(piece))
	}
	return found, nil
}

func (reader *follower) backlogged() bool {
	return reader.snapshot > reader.offset
}

// pending is the part of a line not yet ended, "" when there is none.
func (reader *follower) pending() string {
	if reader.backlogged() {
		return ""
	}
	return text(reader.rest)
}

func text(piece []byte) string {
	return strings.ToValidUTF8(string(piece), "�")
}

func readAt(file *os.File, offset, length int64) ([]byte, error) {
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
	file, info, err := openRegular(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	start := max(0, info.Size()-tailWindow)
	data, err := readAt(file, start, info.Size()-start)
	if err != nil {
		return nil
	}
	body := strings.TrimRight(text(data), "\n")
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
	process processWait
	match   *string // the first wanted line while checking the rest of a snapshot for failures
}

// processWait keeps the first known identity, including across temporarily unreadable rows.
type processWait struct {
	pid   int
	start int64
	ended bool
}

func (process *processWait) running(env world) bool {
	if process.ended {
		return false
	}
	now := env.Process(process.pid)
	if !now.running || process.start != 0 && now.start != 0 && process.start != now.start {
		process.ended = true
		return false
	}
	if process.start == 0 {
		process.start = now.start
	}
	return true
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

// inspect checks one window. When there is a fail pattern, the rest of the same file
// snapshot must be checked before a wanted line can win.
func (wait *waiting) inspect(final bool) (int, bool) {
	reader := wait.reader
	scanning := wait.asked.failing != nil || wait.asked.wanted != nil
	resetCount := reader.resetCount
	lines, err := reader.lines(wait.env.Now(), scanning)
	if err != nil {
		return wait.leave(1, "jst wait: "+err.Error()), true
	}
	if reader.resetCount != resetCount {
		wait.match = nil
	}
	rest := reader.pending()
	if rest != "" && (final || wait.asked.failing != nil && wait.asked.failing.MatchString(rest)) {
		lines = append(lines, rest)
	}
	if wait.asked.failing != nil {
		for _, line := range lines {
			if wait.asked.failing.MatchString(line) {
				if final && wait.asked.mode == modePID {
					return wait.leave(1, "jst wait: a line matched --fail: "+line), true
				}
				return wait.leave(1, fmt.Sprintf("jst wait: a line matched --fail after %s s: %s", wait.elapsed(), line)), true
			}
		}
	}
	if wait.asked.wanted != nil && wait.match == nil {
		for _, line := range lines {
			if wait.asked.wanted.MatchString(line) {
				wait.match = &line
				break
			}
		}
	}
	if wait.match != nil && (wait.asked.failing == nil || !reader.backlogged()) {
		fmt.Fprintln(wait.out, *wait.match)
		return wait.leave(0, fmt.Sprintf("jst wait: the line came after %s s", wait.elapsed())), true
	}
	return 0, false
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
		if code, done := wait.inspect(false); done || wait.reader.backlogged() {
			return code, done
		}
	}
	switch wait.asked.mode {
	case modePID:
		if !wait.process.running(wait.env) {
			if wait.reader != nil && wait.asked.failing != nil {
				if code, done := wait.inspect(true); done || wait.reader.backlogged() {
					return code, done
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
		if wait.asked.whilePID > 0 && !wait.process.running(wait.env) {
			// Looked at once more after the writer's end: what it did last still counts.
			if wait.reader != nil {
				if code, done := wait.inspect(true); done || wait.reader.backlogged() {
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
	if asked.mode == modePID {
		wait.process.pid = asked.pid
	} else if asked.mode != modeQuiet {
		wait.process.pid = asked.whilePID
	}
	if wait.process.pid != 0 {
		wait.process.running(env)
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
		if wait.reader != nil && wait.reader.backlogged() {
			continue
		}
		env.Sleep(min(pause, deadline.Sub(now)))
		pause = min(pause*3/2, longPause)
	}
}
