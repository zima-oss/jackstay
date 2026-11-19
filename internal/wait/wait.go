// Package wait is `jst wait` (docs/design/implementation.md section 5.3, W-007): one way to wait for one
// thing, with a deadline, instead of a hand-made loop. On the way out it prints the matching line
// and the file's last lines, so that nobody reads the file again. Exit 0 the thing happened, 1 it
// cannot happen any more or a --fail line was seen, 2 the request was wrong, 3 the deadline came
// first.
package wait

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/procs"
)

// DefaultTimeout is the deadline when none is given: under the 10 minutes a tool call may take.
const DefaultTimeout = 540 * time.Second

const usage = `usage: jst wait --pid PID                    until the process has ended
       jst wait --file PATH [--nonempty]       until the file exists (and holds something)
       jst wait --gone PATH                    until the file or folder is gone
       jst wait --line REGEX --in PATH         until a line of the file matches (printed)
       jst wait --quiet SECONDS --in PATH      until the file has not grown for that long
  common: [--fail REGEX] [--while-pid PID] [--new] [--timeout SECONDS] [--tail N]`

// readyFileVariable names a file `jst wait` creates once it has taken its starting state: tests
// wait for that event, not for a time (W-013).
const readyFileVariable = "JACKSTAY_WAIT_READY_FILE"

func init() {
	cli.Register(cli.Command{Name: "wait", Summary: "wait for a process, a file, a line or quiet, with a deadline",
		Usage: help, Run: command})
}

// A requestError is a wrong request: exit 2.
type requestError struct{ words string }

func (problem *requestError) Error() string { return problem.words }

func refuse(format string, args ...any) error {
	return &requestError{words: fmt.Sprintf(format, args...)}
}

// mode is the one condition waited for.
type mode string

const (
	modePID   mode = "pid"
	modeFile  mode = "file"
	modeGone  mode = "gone"
	modeLine  mode = "line"
	modeQuiet mode = "quiet"
)

// request is what was asked.
type request struct {
	mode     mode
	pid      int
	file     string
	nonempty bool
	gone     string
	line     string
	wanted   *regexp.Regexp
	quiet    time.Duration
	within   string
	failing  *regexp.Regexp
	whilePID int
	fresh    bool
	timeout  time.Duration
	tail     int
}

// seconds reads a number of seconds; nan and infinity are no number of seconds.
func seconds(name, text string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, refuse("--%s must be a number of seconds, not %q", name, text)
	}
	return value, nil
}

func durationOf(value float64) time.Duration {
	return time.Duration(value * float64(time.Second))
}

// parse reads the arguments into a request.
func parse(args []string, stderr io.Writer) (request, error) {
	flags := flag.NewFlagSet("jst wait", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprintln(stderr, usage) }
	pid := flags.Int("pid", 0, "")
	file := flags.String("file", "", "")
	nonempty := flags.Bool("nonempty", false, "")
	gone := flags.String("gone", "", "")
	line := flags.String("line", "", "")
	quiet := flags.String("quiet", "", "")
	within := flags.String("in", "", "")
	failing := flags.String("fail", "", "")
	whilePID := flags.Int("while-pid", 0, "")
	fresh := flags.Bool("new", false, "")
	timeout := flags.String("timeout", "", "")
	tail := flags.Int("tail", 20, "")
	if err := flags.Parse(args); err != nil {
		return request{}, err
	}
	if flags.NArg() > 0 {
		return request{}, refuse("unexpected argument %q", flags.Arg(0))
	}
	given := map[string]bool{}
	flags.Visit(func(entry *flag.Flag) { given[entry.Name] = true })
	asked := request{pid: *pid, file: *file, nonempty: *nonempty, gone: *gone, line: *line, within: *within,
		whilePID: *whilePID, fresh: *fresh, timeout: DefaultTimeout, tail: *tail}
	var modes []mode
	for _, each := range []mode{modePID, modeFile, modeGone, modeLine, modeQuiet} {
		if given[string(each)] {
			modes = append(modes, each)
		}
	}
	if len(modes) != 1 {
		return request{}, refuse("say what to wait for: exactly one of --pid, --file, --gone, --line, --quiet")
	}
	asked.mode = modes[0]
	if asked.mode == modeFile && asked.file == "" || asked.mode == modeGone && asked.gone == "" {
		return request{}, refuse("--%s needs a path", asked.mode)
	}
	if (asked.mode == modeLine || asked.mode == modeQuiet) && asked.within == "" {
		return request{}, refuse("--%s needs --in PATH", asked.mode)
	}
	if *failing != "" && asked.within == "" {
		return request{}, refuse("--fail needs --in PATH")
	}
	if asked.mode == modePID && asked.pid <= 0 {
		return request{}, refuse("--pid must be a process id above 0")
	}
	if given["while-pid"] && asked.whilePID <= 0 {
		return request{}, refuse("--while-pid must be a process id above 0")
	}
	if given["timeout"] {
		value, err := seconds("timeout", *timeout)
		if err != nil || value < 0 {
			return request{}, refuse("--timeout must be a number of seconds, 0 or more")
		}
		asked.timeout = durationOf(value)
	}
	if asked.mode == modeQuiet {
		value, err := seconds("quiet", *quiet)
		if err != nil || value <= 0 {
			return request{}, refuse("--quiet must be a number of seconds above 0")
		}
		asked.quiet = durationOf(value)
	}
	var err error
	if asked.mode == modeLine {
		if asked.wanted, err = regexp.Compile(asked.line); err != nil {
			return request{}, refuse("not a regular expression: %v", err)
		}
	}
	if *failing != "" {
		if asked.failing, err = regexp.Compile(*failing); err != nil {
			return request{}, refuse("not a regular expression: %v", err)
		}
	}
	return asked, nil
}

func command(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	asked, err := parse(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	var wrong *requestError
	if errors.As(err, &wrong) {
		fmt.Fprintln(stderr, "jst wait:", wrong.words)
		return 2
	}
	if err != nil {
		// The flag package has said what was wrong.
		return 2
	}
	if asked.within != "" {
		if info, err := os.Stat(asked.within); err == nil && !info.Mode().IsRegular() {
			fmt.Fprintf(stderr, "jst wait: --in %s is not a regular file\n", asked.within)
			return 2
		}
	}
	return run(asked, realWorld{}, stdout)
}

// A world is what the wait looks at: the time, a pause, whether a process still runs, and a
// signal that it has taken its starting state.
type world interface {
	Now() time.Time
	Sleep(pause time.Duration)
	Process(pid int) processState
	Ready()
}

type realWorld struct{}

func (realWorld) Now() time.Time            { return time.Now() }
func (realWorld) Sleep(pause time.Duration) { time.Sleep(pause) }

type processState struct {
	running bool
	start   int64 // zero when the kernel will not give the process's identity
}

// Process reads liveness and identity together when the kernel provides a row. A process of
// another account is live when signal 0 can prove only its existence; a zombie has ended.
func (realWorld) Process(pid int) processState {
	row, found, err := procs.Row(pid)
	if err == nil {
		return processState{running: found && !row.Zombie, start: row.StartMicros}
	}
	status := unix.Kill(pid, 0)
	return processState{running: status == nil || errors.Is(status, unix.EPERM)}
}

// Ready creates the ready file, when a test asked for one.
func (realWorld) Ready() {
	if ready := os.Getenv(readyFileVariable); ready != "" {
		if created, err := os.Create(ready); err == nil { // #nosec G703 -- a path the test names for itself
			_ = created.Close()
		}
	}
}
