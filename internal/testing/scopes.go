// Package scopes is `jst test <scope>`: one vocabulary of test scopes declared in the `test`
// section of a project's jackstay.json, a judge (internal/judge), a timeout that ends the whole run, per
// class durations and the `quick` scope's `{slow}`, a gate of steps with resume, marks and bars,
// and declared leftovers (docs/design/testing.md, "Details of the behaviour"; docs/design/implementation.md
// sections 5.2 and 9). Nothing here names a project.
//
// The folder is internal/testing; the package is `scopes`, so it never shadows the standard
// library's `testing` in a file that imports both.
//
// A scope runs in a session of its own without a terminal (supervise.Session), stopped as a
// session tree with a grace of 10 s, at its timeout, on a request to stop, and after a normal end
// for whatever it left.
package scopes

import (
	"context"
	"io"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/cleancopy"
	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/proctree"
	"github.com/zima-oss/jackstay/internal/receipts"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// The scopes' vocabulary (W-012): six names, and extras named `x-…`.
var six = []string{"focused", "changed", "quick", "full", "display", "gate"}

const extraPrefix = "x-"

// Exit statuses and outcomes of a scope's run that receipts does not name.
const (
	StatusTimedOut = 124
	statusRefused  = 2

	outcomeTimedOut = "timed-out"
	outcomeResumed  = "resumed"
	outcomeRefused  = "refused"
)

// Usage is how `jst test <scope>` and its siblings are called.
const Usage = "usage: jst test <scope> [ARGS…] [--no-reuse] [--wait S] [--base REF] [--lock|--no-lock] [--resume] [--keep-going] [--commit REV]\n" +
	"       jst test changed [BASE..REV] | jst test auto [--base REF] | jst test uncovered [--commit REV] | jst test ci | jst test list | jst test plan <scope> [ARGS…] | jst test last [--scope S] | jst test -- COMMAND …"

// unreadableVariable makes jackstay act as though the process table could not be read (tests only).
const unreadableVariable = "JACKSTAY_PROC_UNREADABLE"

func init() {
	receipts.RegisterScopes(Command)
	cli.Register(
		cli.Command{Name: "test list", Summary: "the project's test scopes: what each runs, and when the project runs it", Usage: listHelp, Run: ListCommand},
		cli.Command{Name: "test plan", Summary: "the words a scope would run, step by step; nothing is run", Usage: planHelp, Run: PlanCommand},
		cli.Command{Name: "test uncovered", Summary: "what CI does not cover on this system, on a clean copy of the commit", Usage: uncoveredHelp, Run: UncoveredCommand},
		cli.Command{Name: "test ci", Summary: "every scope CI covers on this system, once, in graph order (the CI job)", Usage: ciHelp, Run: CICommand},
	)
}

// options are the words after the scope.
type options struct {
	noReuse   bool
	lock      *bool
	resume    bool
	keepGoing bool
	project   string
	base      string
	commit    string // run on a clean copy of this commit (W-052)
	wait      float64
	hasWait   bool
}

// refusal is a scope that cannot be run as asked; nothing ran. Its text says why.
type refusal struct{ words string }

func (problem *refusal) Error() string { return problem.words }

func refused(words string) error { return &refusal{words: words} }

var secondsWord = regexp.MustCompile(`^\d+(\.\d+)?$`)

// parse reads `<scope> [ARGS…] [OPTIONS]` (options anywhere; `--` ends them).
func parse(args []string) (scope string, rest []string, chosen options, err error) {
	for index := 0; index < len(args); index++ {
		word := args[index]
		switch {
		case word == "--":
			rest = append(rest, args[index+1:]...)
			return scope, rest, chosen, nil
		case word == "--no-reuse":
			chosen.noReuse = true
		case word == "--lock" || word == "--no-lock":
			locked := word == "--lock"
			chosen.lock = &locked
		case word == "--resume":
			chosen.resume = true
		case word == "--keep-going":
			chosen.keepGoing = true
		case (word == "--project" || word == "--base" || word == "--wait" || word == "--commit") && index+1 < len(args):
			value := args[index+1]
			index++
			switch word {
			case "--wait":
				if !secondsWord.MatchString(value) {
					return "", nil, chosen, refused("--wait takes seconds: '" + value + "'")
				}
				chosen.wait, _ = strconv.ParseFloat(value, 64)
				chosen.hasWait = true
			case "--project":
				chosen.project = value
			case "--commit":
				chosen.commit = value
			default:
				chosen.base = value
			}
		case len(word) > 2 && word[:2] == "--":
			return "", nil, chosen, refused("unknown option " + word)
		case scope == "":
			scope = word
		default:
			rest = append(rest, word)
		}
	}
	return scope, rest, chosen, nil
}

// tester is one `jst test <scope>`, `list` or `plan`; every part that touches the machine can be
// replaced in tests.
type tester struct {
	pin            *gatePin
	stdout, stderr io.Writer
	clock          supervise.Clock
	getenv         func(string) string
	environ        []string
	here           string
	pid            int
	tmpdir         string
	launch         func(*supervise.Signals, launch) (launched, error)
	listen         func(context.Context, supervise.Clock) *supervise.Signals
	acquire        func(context.Context, flock.Request) (*flock.Held, error)
	endBy          func(syscall.Signal) int
	// firstStop is the first request to stop jackstay and when it came (Signals.First).
	firstStop func(*supervise.Signals) (syscall.Signal, time.Time, bool)
	// git runs git in a folder; alive says whether a pid lives; logPaths are the run log's files.
	git      func(ctx context.Context, dir string, args ...string) ([]byte, error)
	alive    func(pid int) bool
	logPaths func() []string
	// system is the process table and signals a run's stop uses (nil: the machine's).
	system *proctree.System
	// operatingSystem is this machine's, as Go names it (runtime.GOOS), for `ci` and `systems`.
	operatingSystem string
}

// lockedWriter writes one call at a time: the command's lines, supervise's words and jackstay's own
// reach jackstay's streams from several goroutines.
type lockedWriter struct {
	mutex *sync.Mutex
	out   io.Writer
}

func (writer lockedWriter) Write(data []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return writer.out.Write(data)
}

// guarded are the two streams behind one lock.
func guarded(stdout, stderr io.Writer) (io.Writer, io.Writer) {
	mutex := &sync.Mutex{}
	return lockedWriter{mutex: mutex, out: stdout}, lockedWriter{mutex: mutex, out: stderr}
}

func newTester(stdout, stderr io.Writer) (*tester, error) {
	here, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	stdout, stderr = guarded(stdout, stderr)
	base := &tester{stdout: stdout, stderr: stderr, clock: supervise.RealClock(), getenv: os.Getenv, environ: os.Environ(),
		here: here, pid: os.Getpid(), tmpdir: os.Getenv("TMPDIR"), launch: superviseLaunch,
		listen: supervise.Listen, acquire: flock.Acquire, endBy: supervise.EndBy, firstStop: (*supervise.Signals).First, git: runGit, alive: processAlive,
		logPaths: receipts.RunLogPaths, operatingSystem: runtime.GOOS}
	if base.tmpdir == "" {
		base.tmpdir = os.TempDir()
	}
	if base.getenv(unreadableVariable) == "1" {
		base.system = unreadableSystem()
	}
	return base, nil
}

// runGit is git's output in dir.
func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitcli.Git{Dir: dir}.Run(ctx, args...)
}

// processAlive says whether a process of that id exists (one of another account counts).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || err == unix.EPERM
}

func (base *tester) say(format string, values ...any) { receipts.Say(base.stderr, format, values...) }

// project is a checkout whose `test` section can be used, and the call's options.
type project struct {
	*tester
	ctx      context.Context
	root     string
	loaded   config.Config
	section  map[string]any
	receipts map[string]any
	name     string
	options  options
	signals  *supervise.Signals
	runLog   receipts.RunLog
	// argumentsFor is the scope asked for by name, the one node of its run given the command
	// line's test arguments; a dep is given none.
	argumentsFor string
	arguments    []string
}

// argumentsOf are the command line's test arguments for a node of the run: the scope asked for
// gets them, its deps none.
func (proj *project) argumentsOf(name string) []string {
	if name == proj.argumentsFor {
		return proj.arguments
	}
	return nil
}

// needs are the sections a scope reads.
var needs = []string{"lock", "receipts", "test"}

// open is the project around the working folder, or why its scopes cannot be used.
func (base *tester) open(chosen options) (*project, error) {
	root, loaded, err := config.Load(base.here, needs)
	if err != nil {
		return nil, refused("jackstay.json cannot be used: " + err.Error())
	}
	if root == "" {
		return nil, refused("there is no jackstay.json at or above " + base.here + ": scopes are declared in its `test` section")
	}
	section := loaded.Section("test")
	if problems := Problems(section); len(problems) > 0 {
		return nil, refused("jackstay.json cannot be used: " + joinProblems(problems))
	}
	if len(objectOf(section["scopes"])) == 0 {
		return nil, refused("jackstay.json declares no scopes (`test.scopes`); `jst test -- COMMAND` runs any command")
	}
	name := chosen.project
	if name == "" {
		name = config.ProjectName(root, loaded, base.here)
	}
	ctx := context.Background()
	logPath, _ := receipts.RunLogPath()
	opened := &project{tester: base, ctx: ctx, root: root, loaded: loaded, section: section, receipts: receipts.Section(loaded),
		name: name, options: chosen}
	opened.runLog = receipts.RunLog{Path: logPath, Project: name, Where: receipts.WhereOf(ctx, root),
		Inside: receipts.ParentRun(base.getenv, name), Clock: base.clock}
	return opened, nil
}

// log writes one line of the run log for a command's words; false when it could not be written.
func (proj *project) log(event string, command []string, key string, fields map[string]any) bool {
	writer := proj.runLog
	writer.Command = command
	return writer.Write(event, key, fields)
}

// notLogged says the record has a hole, before the outcome line.
func (proj *project) notLogged(what string) {
	proj.say("WARNING: the log could not be written (%s): %s is NOT in the record.", proj.runLog.Path, what)
}

// Command is `jst test <scope> [ARGS…] [OPTIONS]`.
func Command(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	base, err := newTester(stdout, stderr)
	if err != nil {
		receipts.Say(stderr, "the working folder cannot be read: %v", err)
		receipts.Say(stderr, "outcome=not-started (nothing was run)")
		return statusRefused
	}
	return base.scope(args)
}

// scope runs `jst test <scope> …`.
func (base *tester) scope(args []string) int {
	name, rest, chosen, err := parse(args)
	selectChanged := false
	selectedRange := false
	var rangeArgument string
	if err == nil && name == "changed" && len(rest) == 1 && strings.Contains(rest[0], "..") {
		if chosen.base != "" || chosen.commit != "" {
			err = refused("`jst test changed BASE..REV` takes one range, without --base or --commit")
		} else {
			rangeArgument = rest[0]
			chosen.base, chosen.commit, err = selectionRange(rest[0])
			rest = nil
			selectedRange = err == nil
		}
	}
	if err == nil && base.pin != nil && chosen.commit == "" {
		err = refused("the pinned gate requires an exact commit")
	}
	if err == nil && name == "" {
		err = refused("which scope? " + Usage)
	}
	if err == nil && chosen.commit != "" && chosen.resume {
		err = refused("--commit is a whole run on a clean copy of the commit: it takes no --resume")
	}
	var signals *supervise.Signals
	if err == nil && chosen.commit != "" {
		chosen.noReuse = true
		// Listening before the copy is made: a stop while it is made removes it too.
		signals = base.listen(context.Background(), base.clock)
		defer signals.Close()
		var made *cleancopy.Copy
		var inside string
		made, inside, err = base.makeCopy(chosen)
		if err == nil {
			defer made.Remove()
			endBy := base.endBy
			// jackstay ends itself by a stop's signal, which runs no deferred call: the copy goes first.
			base.endBy = func(sig syscall.Signal) int {
				made.Remove()
				return endBy(sig)
			}
			if sig, _, stopped := base.firstStop(signals); stopped {
				made.Remove()
				base.say("stopped while the clean copy was made; it is removed")
				base.say("outcome=not-started (nothing was run)")
				return base.endBy(sig)
			}
			base.say("clean copy of %s at %s (removed when this run ends)", chosen.commit, made.Tree)
			base.here = inside
		}
	}
	var proj *project
	if err == nil {
		proj, err = base.open(chosen)
	}
	if err == nil && name == "changed" {
		selectChanged = has(proj.section, "selection")
		if selectedRange && !selectChanged {
			rest = []string{rangeArgument}
		}
	}
	split := false
	if err == nil && (name == "auto" || selectChanged) {
		if len(rest) > 0 {
			err = refused("`auto` takes no test names")
		} else {
			var choice selectionResult
			choice, err = proj.selectScope()
			if err == nil {
				name = choice.scope
				chosen.base = choice.base
				proj.options = chosen
				proj.printSelection(choice)
				// A project that says where its scopes run splits what `changed` selects between
				// here, CI and other systems (docs/design/land.md section 7.4); `auto` runs the
				// selected scope.
				split = selectChanged && declaresWhere(proj.section)
			}
		}
	}
	if err == nil && name == ciCommand && base.pin == nil {
		if len(rest) > 0 {
			err = refused("`jst test ci` takes no test names")
		} else if len(ciScopes(proj.section, base.operatingSystem)) == 0 {
			err = refused("no scope of " + proj.name + " names " + base.operatingSystem + " in its `ci`: `jst test ci` has nothing to run here")
		}
	}
	if err == nil && (split || name == uncoveredCommand && base.pin != nil || name == ciCommand) {
		proj.listenUnless(signals)
		defer proj.signals.Close()
		switch {
		case split:
			return proj.runSplit("changed", []string{name})
		case name == uncoveredCommand:
			return proj.runSplit(uncoveredCommand, proj.uncoveredSelection())
		default:
			return proj.gate(ciCommand, ciScopes(proj.section, proj.operatingSystem), nil)
		}
	}
	var entry map[string]any
	if err == nil {
		scopes := objectOf(proj.section["scopes"])
		entry = objectOf(scopes[name])
		switch {
		case entry == nil:
			err = refused("`" + name + "` is not a scope of " + proj.name + "; its scopes: " + joinComma(sortedKeys(scopes)) +
				" (`jst test list`)")
		case base.pin != nil && !isComposite(entry):
			err = refused("the pinned gate must list its steps")
		case len(depsOf(entry)) > 0 && namesRefusal(name, entry, rest) != nil:
			err = namesRefusal(name, entry, rest)
		case len(depsOf(entry)) == 0 && (chosen.resume || chosen.keepGoing):
			err = refused("--resume and --keep-going are for a scope with deps (or that lists steps)")
		}
	}
	if err != nil {
		base.say("%v", err)
		base.say("outcome=not-started (nothing was run)")
		return statusRefused
	}
	proj.listenUnless(signals)
	defer proj.signals.Close()
	if len(depsOf(entry)) > 0 || !runsOn(entry, proj.operatingSystem) {
		proj.argumentsFor, proj.arguments = name, rest
		return proj.gate(name, []string{name}, nil)
	}
	return proj.runLeaf(resolve(proj.section, name, kindScope, entry), rest, "", false).status
}

// namesRefusal is why a scope refuses the test names given, or nil: a composite takes none, nor
// does a scope whose words need none. `jst test` and `jst test plan` both check it before the walk.
func namesRefusal(name string, entry map[string]any, rest []string) error {
	switch {
	case len(rest) == 0 || !isComposite(entry) && needsTestArguments(entry):
		return nil
	case len(depsOf(entry)) > 0:
		return refused("`" + name + "` has deps (or lists steps) and takes no names; given: " + joinSpace(rest))
	default:
		return refused("`" + name + "` takes no names (its words hold no {tests}); given: " + joinSpace(rest))
	}
}

// listenUnless listens for requests to stop, unless the clean copy's listener already does.
func (proj *project) listenUnless(signals *supervise.Signals) {
	proj.signals = signals
	if signals == nil {
		proj.signals = proj.listen(proj.ctx, proj.clock)
	}
}
