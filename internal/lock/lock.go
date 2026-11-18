// Package lock is `jst lock -- COMMAND` and `jst lock status` (docs/design/implementation.md section 5.4): a
// heavy command run holding the machine-wide lock (internal/flock) and every older lock its project
// names, in a process group of its own given the terminal (internal/supervise), the lock freed
// only once nothing of the group is left. Exit: the command's status; 2 a lock, jackstay.json or the
// arguments cannot be used; 75 gave up waiting ("Nothing was run"); 127 the command cannot start;
// stopped by a signal: ends by that signal.
package lock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// Statuses of the lock's own.
const (
	statusUnusable    = 2
	statusGaveUp      = 75 // EX_TEMPFAIL: waited as long as it was told to, and ran nothing
	statusCannotStart = 127
)

// Variables the lock reads and does not pass on to the command.
const (
	// OutcomeFileVariable names a file the lock empties at its start and writes one word into at
	// its end: ran, gave-up, unusable or cannot-start (empty: stopped while it waited).
	OutcomeFileVariable = "JACKSTAY_LOCK_OUTCOME_FILE"
	// NoteWordsVariable stands for the command in the holder note.
	NoteWordsVariable = "JACKSTAY_LOCK_NOTE_WORDS"
	// GroupWaitVariable replaces the 30 s GROUP_WAIT (seconds).
	GroupWaitVariable = "JACKSTAY_LOCK_GROUP_WAIT" // #nosec G101 -- a variable's name
)

// Outcome words of OutcomeFileVariable.
const (
	OutcomeRan         = "ran"
	OutcomeGaveUp      = "gave-up"
	OutcomeUnusable    = "unusable"
	OutcomeCannotStart = "cannot-start"
)

const usage = `usage: jst lock [--project NAME] [--wait SECONDS] [--also-hold PATH=VARIABLE]... [--note-words WORDS] -- COMMAND [ARG ...]
       jst lock status [--json]`

func init() {
	cli.Register(
		cli.Command{Name: "lock", Summary: "run a heavy command holding the machine-wide lock, in its turn", Usage: commandHelp, Run: command},
		cli.Command{Name: "lock status", Summary: "who runs under the machine-wide lock and who waits", Usage: statusHelp, Run: statusCommand},
	)
}

// seconds is a --wait or GROUP_WAIT value: digits, a dot and digits.
var seconds = regexp.MustCompile(`^\d+(\.\d+)?$`)

// request is what was asked.
type request struct {
	project   string
	wait      time.Duration
	bounded   bool
	noteWords string
	alsoHold  []flock.Older
	argv      []string
}

// parse reads the options before `--`; false when the words cannot be used.
func parse(args []string) (request, bool) {
	var asked request
	for len(args) > 0 && args[0] != "--" {
		if len(args) < 2 {
			return asked, false
		}
		value := args[1]
		switch args[0] {
		case "--project":
			asked.project = value
		case "--wait":
			if !seconds.MatchString(value) {
				return asked, false
			}
			number, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return asked, false
			}
			asked.wait, asked.bounded = time.Duration(number*float64(time.Second)), true
		case "--note-words":
			asked.noteWords = value
		case "--also-hold":
			cut := strings.LastIndex(value, "=")
			if cut <= 0 {
				return asked, false
			}
			older, variable := value[:cut], value[cut+1:]
			if variable != "" && !flock.IsVariableName(variable) {
				return asked, false
			}
			asked.alsoHold = append(asked.alsoHold, flock.Older{Path: homedir.Expand(older), Variable: variable})
		default:
			return asked, false
		}
		args = args[2:]
	}
	if len(args) < 2 {
		return asked, false
	}
	asked.argv = args[1:]
	return asked, true
}

// project is the project around the working folder: its root, its configuration (the `lock`
// section usable) and the folder ("" when the folder is gone).
type project struct {
	root   string
	config config.Config
	here   string
}

func openProject() (project, error) {
	here, err := os.Getwd()
	if err == nil {
		// macOS still names a removed working folder: it is gone all the same.
		_, err = os.Stat(here)
	}
	if err != nil {
		// Started in a folder that is gone: the run goes on, without a project file.
		return project{}, nil
	}
	root, loaded, err := config.Load(here, []string{"lock"})
	if err != nil {
		return project{}, err
	}
	return project{root: root, config: loaded, here: here}, nil
}

// alsoHold are the older locks the project's jackstay.json names.
func (found project) alsoHold() []flock.Older {
	var older []flock.Older
	entries, _ := found.config.Section("lock")["also_hold"].([]any)
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		path, _ := entry["path"].(string)
		variable, _ := entry["held_env"].(string)
		older = append(older, flock.Older{Path: homedir.Expand(path), Variable: variable})
	}
	return older
}

func (found project) name() string {
	if found.here == "" {
		return "unknown"
	}
	return config.ProjectName(found.root, found.config, found.here)
}

// writeOutcome writes one word into the outcome file (empties it for ""); without the variable,
// nothing. A file that cannot be written is said and changes nothing else.
func writeOutcome(word string, stderr io.Writer) {
	target := os.Getenv(OutcomeFileVariable)
	if target == "" {
		return
	}
	text := ""
	if word != "" {
		text = word + "\n"
	}
	if err := os.WriteFile(target, []byte(text), 0o600); err != nil { // #nosec G703 -- the file the caller names
		fmt.Fprintf(stderr, "jst lock: cannot write the outcome to %s: %v\n", target, err)
	}
}

func statusCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	asJSON := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--json":
		asJSON = true
	default:
		fmt.Fprintln(stderr, usage)
		return statusUnusable
	}
	found, err := openProject()
	if err != nil {
		fmt.Fprintf(stderr, "jst lock: %v\n", err)
		return statusUnusable
	}
	path, err := flock.Path()
	if err != nil {
		fmt.Fprintf(stderr, "jst lock: %v\n", err)
		return statusUnusable
	}
	var older []string
	for _, each := range found.alsoHold() {
		older = append(older, each.Path)
	}
	report, err := flock.Status(path, older)
	if err != nil {
		fmt.Fprintf(stderr, "jst lock: %v\n", err)
		return statusUnusable
	}
	if !asJSON {
		_, _ = io.WriteString(stdout, report.Text())
		return 0
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "jst lock: %v\n", err)
		return statusUnusable
	}
	fmt.Fprintln(stdout, string(data))
	return 0
}

func command(args []string, _ io.Reader, _, stderr io.Writer) int {
	writeOutcome("", stderr)
	unusable := func(words string) int {
		fmt.Fprintln(stderr, words)
		writeOutcome(OutcomeUnusable, stderr)
		return statusUnusable
	}
	found, err := openProject()
	if err != nil {
		return unusable("jst lock: " + err.Error())
	}
	asked, ok := parse(args)
	if !ok {
		return unusable(usage)
	}
	if asked.project == "" {
		asked.project = found.name()
	}
	if asked.noteWords == "" {
		asked.noteWords = os.Getenv(NoteWordsVariable)
	}
	path, err := flock.Path()
	if err != nil {
		return unusable("jst lock: " + err.Error())
	}
	runner := &locked{asked: asked, lock: path, older: append(found.alsoHold(), asked.alsoHold...), here: found.here,
		stderr: stderr, clock: supervise.RealClock()}
	return runner.run()
}

// locked is one `jst lock -- COMMAND`.
type locked struct {
	asked  request
	lock   string
	older  []flock.Older
	here   string
	stderr io.Writer
	clock  supervise.Clock
}

func (run *locked) run() int {
	unusable := func(words string) int {
		fmt.Fprintln(run.stderr, words)
		writeOutcome(OutcomeUnusable, run.stderr)
		return statusUnusable
	}
	signals := supervise.Listen(context.Background(), run.clock)
	defer signals.Close()
	env := passedOn(os.Environ())
	self := os.Getpid()
	table := procs.Table()
	if flock.UnderHolder(run.lock, os.Getenv(flock.HeldVariable), table, self) {
		// Under a holder that still holds it: its turn is ours, and no older lock is taken either.
		return run.command(signals, env, nil, func(int) {})
	}
	plan, err := flock.PlanOlder(run.lock, run.older, os.Getenv, table, self)
	if err != nil {
		return unusable("jst lock: " + err.Error())
	}
	// Ctrl-Z while waiting would keep this run's place, and everyone's behind it, for as long as it
	// stayed stopped: ignored from here on, as it is while the command runs.
	if !signal.Ignored(unix.SIGTSTP) {
		signal.Ignore(unix.SIGTSTP)
		defer signal.Reset(unix.SIGTSTP)
	}
	who := whoWords(run.asked.project, run.asked.argv, run.asked.noteWords, run.here)
	held, err := flock.Acquire(signals.Context(), flock.Request{Lock: run.lock, Older: plan.Older,
		Words: fmt.Sprintf("%s (process %d), since %s", who, self, time.Now().Format("2006-01-02 15:04:05")),
		Wait:  run.asked.wait, Bounded: run.asked.bounded, Out: run.stderr})
	var gaveUp *flock.GaveUp
	var notUsable *flock.Unusable
	var stopped *supervise.StopRequest
	switch {
	case errors.As(err, &gaveUp):
		writeOutcome(OutcomeGaveUp, run.stderr)
		return statusGaveUp
	case errors.As(err, &notUsable):
		return unusable("jst lock: " + notUsable.Words)
	case errors.As(err, &stopped):
		// Stopped while it waited: nothing ran, the outcome file stays empty.
		signals.Close()
		return supervise.EndBy(stopped.Signal)
	case err != nil:
		return unusable("jst lock: " + err.Error())
	}
	defer held.Release()
	since := time.Now()
	held.WriteNote(flock.NoteWords(who, "about to start", self, since))
	env = append(env, fmt.Sprintf("%s=%s:%d", flock.HeldVariable, flock.RealPath(run.lock), self))
	for variable, claim := range plan.Variables {
		env = append(env, variable+"="+claim)
	}
	return run.command(signals, env, held, func(group int) {
		held.WriteNote(flock.NoteWords(who, fmt.Sprintf("the command's process group %d", group), self, since))
	})
}

// command runs the command in its group and ends as it did: its status, or by the signal jackstay was
// asked to stop with (the locks given back first).
func (run *locked) command(signals *supervise.Signals, env []string, held *flock.Held, started func(int)) int {
	outcome, err := supervise.Run(signals, supervise.Spec{
		Argv: run.asked.argv, Env: env, Mode: supervise.Group, Terminal: true, Stdin: os.Stdin,
		Grace: groupWait(), SessionsFile: os.Getenv(supervise.SessionsFileVariable),
		Say: run.stderr, Who: "jst lock", Clock: run.clock,
		Started: func(group int) {
			started(group)
			writeOutcome(OutcomeRan, run.stderr)
		},
	})
	var startErr *supervise.StartError
	if errors.As(err, &startErr) {
		fmt.Fprintf(run.stderr, "jst lock: %v\n", startErr)
		writeOutcome(OutcomeCannotStart, run.stderr)
		return statusCannotStart
	}
	for _, problem := range outcome.Problems {
		fmt.Fprintf(run.stderr, "jst lock: %v\n", problem)
	}
	if outcome.StoppedBy != 0 {
		held.Release()
		signals.Close()
		return supervise.EndBy(outcome.StoppedBy)
	}
	return outcome.Status()
}

// passedOn is the environment without the lock's own variables.
func passedOn(environ []string) []string {
	kept := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if name == OutcomeFileVariable || name == NoteWordsVariable || name == GroupWaitVariable {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// groupWait is GROUP_WAIT, or JACKSTAY_LOCK_GROUP_WAIT's seconds.
func groupWait() time.Duration {
	chosen := os.Getenv(GroupWaitVariable)
	if seconds.MatchString(chosen) {
		if number, err := strconv.ParseFloat(chosen, 64); err == nil && number > 0 {
			return time.Duration(number * float64(time.Second))
		}
	}
	return supervise.DefaultGrace
}

// whoWords say who a run is, for the holder note and the ticket: the project, the command (or the
// caller's words for it, on one line) and the folder.
func whoWords(project string, argv []string, noteWords, here string) string {
	said := strings.Join(strings.Fields(noteWords), " ")
	if said == "" {
		said = strings.Join(strings.Fields(strings.Join(argv, " ")), " ")
	}
	if here == "" {
		here = "a folder that is gone"
	}
	return fmt.Sprintf("project %s: %s in %s", project, said, here)
}
