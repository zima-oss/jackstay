package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/judge"
	"github.com/zima-oss/jackstay/internal/lock"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/testreport"
	"github.com/zima-oss/jackstay/internal/version"
)

// Needs are the jackstay.json sections `jst test --` and `jst receipts key` read; a broken `test` section
// does not stop them.
var Needs = []string{"lock", "receipts"}

// NoReuseVariable forces a run whatever the store holds, as --no-reuse does.
const NoReuseVariable = "JACKSTAY_NO_REUSE"

// request is what `jst test [OPTIONS] -- COMMAND` asked.
type request struct {
	noReuse bool
	// lock is the call's choice: nil leaves it to the project (lock.enabled).
	lock    *bool
	project string
	wait    time.Duration
	bounded bool
	argv    []string
}

// tester is one `jst test -- COMMAND`; every part that touches the machine can be replaced in tests.
type tester struct {
	stdout, stderr io.Writer
	clock          supervise.Clock
	getenv         func(string) string
	environ        []string
	here           string
	pid            int
	// grace and readBound are the stop's; zero leaves supervise's (and JACKSTAY_LOCK_GROUP_WAIT).
	grace     time.Duration
	readBound time.Duration
	run       func(*supervise.Signals, supervise.Spec) (supervise.Outcome, error)
	listen    func(context.Context, supervise.Clock) *supervise.Signals
	acquire   func(context.Context, flock.Request) (*flock.Held, error)
	endBy     func(syscall.Signal) int
}

func newTester(stdout, stderr io.Writer) (*tester, error) {
	here, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return &tester{stdout: stdout, stderr: stderr, clock: supervise.RealClock(), getenv: os.Getenv, environ: os.Environ(),
		here: here, pid: os.Getpid(), run: supervise.Run, listen: supervise.Listen, acquire: flock.Acquire,
		endBy: supervise.EndBy}, nil
}

// session is the state of one run of the flow.
type session struct {
	*tester
	asked     request
	ctx       context.Context
	root      string
	loaded    config.Config
	section   map[string]any
	entry     map[string]any
	project   string
	store     Store
	storeErr  error
	runLog    RunLog
	logPaths  []string
	unlogged  []string
	signals   *supervise.Signals
	lockedRun bool
	env       []string
}

func (run *session) say(format string, values ...any) { Say(run.stderr, format, values...) }

// notLogged is said every time a line of the record could not be written, just before the outcome.
func (run *session) notLogged(what string) {
	run.unlogged = append(run.unlogged, what)
}

func (run *session) sayUnlogged() {
	for _, what := range run.unlogged {
		run.say("WARNING: the log could not be written (%s): %s is NOT in the record.", run.runLog.Path, what)
	}
	run.unlogged = nil
}

func (run *session) notStarted(words string) int {
	run.say("%s", words)
	run.say("outcome=not-started (jackstay.json cannot be used; nothing was run)")
	return StatusUnusable
}

// raw runs `jst test -- COMMAND`.
func (base *tester) raw(asked request) int {
	run := &session{tester: base, asked: asked, ctx: context.Background()}
	root, loaded, err := config.Load(run.here, Needs)
	if err != nil {
		return run.notStarted(err.Error())
	}
	run.root, run.loaded, run.section = root, loaded, Section(loaded)
	if run.project = asked.project; run.project == "" {
		run.project = config.ProjectName(root, loaded, run.here)
	}
	run.entry = CommandEntry(run.section, asked.argv)
	evidence, err := NewEvidence(run.entry)
	if err != nil {
		return run.notStarted(err.Error())
	}
	run.store, run.storeErr = OpenStore(run.project, run.section, run.clock)
	logPath, _ := RunLogPath()
	run.logPaths = RunLogPaths()
	run.runLog = RunLog{Path: logPath, Project: run.project, Command: asked.argv, Where: WhereOf(run.ctx, root),
		Inside: ParentRun(run.getenv, run.project), Clock: run.clock}
	locked := lockEnabled(loaded)
	if asked.lock != nil {
		locked = *asked.lock
	}
	run.signals = run.listen(run.ctx, run.clock)
	defer run.signals.Close()
	run.env = commandEnv(run.environ)
	var held *flock.Held
	if locked {
		// The lookup is made before the lock (a hit does not wait in line) and again under it.
		if key, keyErr := run.key(); run.lookUp(key, keyErr, "before-lock") {
			return 0
		}
		var status int
		var done bool
		if held, status, done = run.takeLock(); done {
			return status
		}
		defer held.Release()
	}
	key, keyErr := run.key()
	if run.lookUp(key, keyErr, "under-lock") {
		return 0
	}
	return run.command(evidence, key, keyErr, held)
}

// key is the key of this run, or why there is none.
func (run *session) key() (Key, error) {
	env := run.env
	if env == nil {
		env = commandEnv(run.environ)
	}
	return KeyOf(run.ctx, Keying{Root: run.root, Config: run.loaded, Project: run.project, Command: run.asked.argv,
		Dir: run.here, Environ: env})
}

// refusal is why a receipt may not stand in for a run here, ("" when it may).
func (run *session) refusal() string {
	switch {
	case run.asked.noReuse:
		return "--no-reuse"
	case run.getenv(NoReuseVariable) != "":
		return NoReuseVariable + " is set"
	case run.entry == nil:
		return "the command is not listed in receipts.commands"
	case run.entry["reuse"] != true:
		return "the command is not opted in (reuse is not true)"
	}
	if text, _ := run.entry["evidence"].(string); text == "" && judge.AdapterVersion(run.adapterKind()) == 0 {
		return noEvidence
	}
	if _, written := run.section["env"].([]any); !written {
		return "jackstay.json does not write out receipts.env (the variables the tests read; it may be an empty list)"
	}
	branches := DefaultNoReuseBranches
	if listed, present := run.section["no_reuse_branches"]; present {
		branches = texts(listed)
	}
	git := gitcli.Git{Dir: run.root}
	branch, _ := git.Line(run.ctx, "symbolic-ref", "--short", "-q", "HEAD")
	if slices.Contains(branches, strings.TrimSpace(branch)) {
		return fmt.Sprintf("the branch is %s, where the command always runs", strings.TrimSpace(branch))
	}
	if tip := headIsTipOf(run.ctx, git, branches); tip != "" {
		return fmt.Sprintf("HEAD is the commit %s points at, where the command always runs", tip)
	}
	return ""
}

// HeadIsTipOf is headIsTipOf for internal/testing: the branch (or remote branch) of branches whose
// commit HEAD is, or "".
func HeadIsTipOf(ctx context.Context, git gitcli.Git, branches []string) string {
	return headIsTipOf(ctx, git, branches)
}

// headIsTipOf is the first branch whose commit is HEAD's, here or as any remote has it (a detached
// HEAD at main or at origin/main is main, also where the local main is behind), or "".
func headIsTipOf(ctx context.Context, git gitcli.Git, branches []string) string {
	commit := func(name string) string {
		said, err := git.Line(ctx, "rev-parse", "--verify", "--quiet", name+"^{commit}")
		if err != nil {
			return ""
		}
		return strings.TrimSpace(said)
	}
	head := commit("HEAD")
	if head == "" {
		return ""
	}
	listed, _ := git.Run(ctx, "for-each-ref", "--format=%(refname)", "refs/remotes")
	remotes := strings.Fields(string(listed))
	for _, branch := range branches {
		names := []string{"refs/heads/" + branch, "refs/remotes/origin/" + branch}
		for _, ref := range remotes {
			if parts := strings.SplitN(ref, "/", 4); len(parts) == 4 && parts[3] == branch {
				names = append(names, ref)
			}
		}
		for _, name := range names {
			if commit(name) == head {
				if short, ok := strings.CutPrefix(name, "refs/heads/"); ok {
					return short
				}
				return strings.TrimPrefix(name, "refs/remotes/")
			}
		}
	}
	return ""
}

// lookUp stands a receipt in for the run when one exists and reuse is allowed (printed and
// logged); under the lock (the run's own lookup) a repeat of an earlier pass is logged as
// `would-hit` whenever the receipt is not used.
func (run *session) lookUp(key Key, keyErr error, where string) bool {
	if keyErr != nil {
		return false
	}
	var receipt *Receipt
	whyNot := "the receipt store cannot be found"
	if run.storeErr == nil {
		receipt, whyNot = run.store.Read(key.Value)
	}
	refusal := run.refusal()
	if refusal != "" || receipt == nil {
		if where != "under-lock" || run.entry == nil {
			return false
		}
		earlier, found := EarlierPass(ReadRecords(run.logPaths), run.project, key.Value, MaxAge(run.section), run.clock.Now())
		if !found {
			return false
		}
		reason := refusal
		if reason == "" {
			reason = fmt.Sprintf("no usable receipt (%s)", whyNot)
		}
		forced := run.asked.noReuse || run.getenv(NoReuseVariable) != ""
		if !run.runLog.Write(EventWouldHit, key.Value, map[string]any{"reason": reason, "saved_seconds": earlier["seconds"],
			"receipt": receipt != nil, "forced": forced}) {
			run.say("WARNING: the log could not be written (%s): that this run repeats an earlier pass (would-hit) is NOT in the record.",
				run.runLog.Path)
		}
		return false
	}
	if !run.runLog.Write(EventReused, key.Value, map[string]any{"saved_seconds": receipt.Seconds, "lookup": where,
		"receipt_of": receipt.Head}) {
		// Every reuse is counted, or it does not happen.
		run.say("a receipt exists, but the reuse could not be logged (%s); the command runs.", run.runLog.Path)
		return false
	}
	run.printReuse(receipt)
	run.say("outcome=reused")
	return true
}

func (run *session) printReuse(receipt *Receipt) {
	branch := receipt.Branch
	if branch == "" {
		branch = "none"
	}
	run.say("REUSED: nothing was run. An identical tree passed this command before; this is that run's receipt.")
	run.say("  command: %s", strings.Join(run.asked.argv, " "))
	run.say("  passed:  %s, took %g s, tests counted: %d", receipt.Ended, receipt.Seconds, receipt.EvidenceCount)
	run.say("  where:   %s at %s (branch %s)", receipt.Worktree, cut(receipt.Head, 12), branch)
	run.say("  receipt: %s", receipt.Path)
	run.say("  to run it anyway: jst test --no-reuse -- ...")
}

// lockEnabled is lock.enabled (W-005: off unless the project turns it on).
func lockEnabled(loaded config.Config) bool { return loaded.Section("lock")["enabled"] == true }

// alsoHold are the older locks the project's jackstay.json names.
func alsoHold(loaded config.Config) []flock.Older {
	var older []flock.Older
	entries, _ := loaded.Section("lock")["also_hold"].([]any)
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		place, _ := entry["path"].(string)
		variable, _ := entry["held_env"].(string)
		older = append(older, flock.Older{Path: homedir.Expand(place), Variable: variable})
	}
	return older
}

// takeLock waits for the machine-wide lock (and the project's older ones). done is set when the
// run ends here: it gave up, the lock cannot be used, or it was stopped while it waited.
func (run *session) takeLock() (held *flock.Held, status int, done bool) {
	path, err := flock.Path()
	if err != nil {
		return nil, run.lockUnusable(err.Error()), true
	}
	self := run.pid
	table := procs.Table()
	run.lockedRun = true
	if flock.UnderHolder(path, run.getenv(flock.HeldVariable), table, self) {
		// Under a holder that still holds it: its turn is ours.
		return nil, 0, false
	}
	plan, err := flock.PlanOlder(path, alsoHold(run.loaded), run.getenv, table, self)
	if err != nil {
		return nil, run.lockUnusable(err.Error()), true
	}
	began := run.clock.Now()
	who := fmt.Sprintf("project %s: %s in %s", run.project, strings.Join(strings.Fields(strings.Join(run.asked.argv, " ")), " "), run.here)
	held, err = run.acquire(run.signals.Context(), flock.Request{Lock: path, Older: plan.Older,
		Words: fmt.Sprintf("%s (process %d), since %s", who, self, began.Format("2006-01-02 15:04:05")),
		Wait:  run.asked.wait, Bounded: run.asked.bounded, Out: run.stderr})
	var gaveUp *flock.GaveUp
	var notUsable *flock.Unusable
	var stopped *supervise.StopRequest
	waited := secondsBetween(began, run.clock.Now())
	switch {
	case errors.As(err, &gaveUp):
		if !run.runLog.Write(EventRun, "", map[string]any{"outcome": OutcomeGaveUp, "waited_seconds": waited}) {
			run.notLogged("this run, which gave up waiting")
		}
		run.sayUnlogged()
		run.say("outcome=gave-up (nothing was run)")
		return nil, StatusGaveUp, true
	case errors.As(err, &notUsable):
		return nil, run.lockUnusable(notUsable.Words), true
	case errors.As(err, &stopped):
		name := supervise.SignalName(stopped.Signal)
		if !run.runLog.Write(EventRun, "", map[string]any{"outcome": OutcomeStoppedWaiting, "signal": name,
			"status": 128 + int(stopped.Signal), "waited_seconds": waited}) {
			run.notLogged("this run, stopped while it waited")
		}
		run.sayUnlogged()
		run.say("outcome=stopped-waiting (%s after %g s; nothing was run)", name, waited)
		run.signals.Close()
		return nil, run.endBy(stopped.Signal), true
	case err != nil:
		return nil, run.lockUnusable(err.Error()), true
	}
	held.WriteNote(flock.NoteWords(who, "about to start", self, began))
	run.env = setEnv(run.env, flock.HeldVariable, fmt.Sprintf("%s:%d", flock.RealPath(path), self))
	for _, variable := range sortedKeys(plan.Variables) {
		run.env = setEnv(run.env, variable, plan.Variables[variable])
	}
	return held, 0, false
}

func (run *session) lockUnusable(words string) int {
	run.say("%s", words)
	run.say("outcome=not-started (the lock could not be used; nothing was run)")
	return StatusUnusable
}

// commandEnv is the command's environment: jackstay's own less the lock's and the run's variables, which
// the run sets for its command itself (an inherited JACKSTAY_LOCK_HELD is passed on, so a nested lock
// knows its holder).
func commandEnv(environ []string) []string {
	dropped := []string{lock.OutcomeFileVariable, lock.NoteWordsVariable, lock.GroupWaitVariable,
		supervise.SessionsFileVariable, supervise.RunIDVariable, supervise.RunProjectVariable, testreport.PathVariable}
	kept := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(dropped, name) {
			kept = append(kept, entry)
		}
	}
	return kept
}

// setEnv is env with name set to value (an earlier entry of that name taken out).
func setEnv(env []string, name, value string) []string {
	kept := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if current, _, _ := strings.Cut(entry, "="); current != name {
			kept = append(kept, entry)
		}
	}
	return append(kept, name+"="+value)
}

// seconds is a --wait or JACKSTAY_LOCK_GROUP_WAIT value: digits, a dot and digits.
var seconds = regexp.MustCompile(`^\d+(\.\d+)?$`)

// groupWait is the grace after a request to stop: JACKSTAY_LOCK_GROUP_WAIT's seconds, else 30 s.
func (base *tester) groupWait() time.Duration {
	if base.grace > 0 {
		return base.grace
	}
	chosen := base.getenv(lock.GroupWaitVariable)
	if seconds.MatchString(chosen) {
		if number, err := strconv.ParseFloat(chosen, 64); err == nil && number > 0 {
			return time.Duration(number * float64(time.Second))
		}
	}
	return supervise.DefaultGrace
}

// ran is what the command did, for the run's line.
type ran struct {
	key        Key
	keyErr     error
	runID      string
	started    time.Time
	ended      time.Time
	kept       *Kept
	judge      *Evidence
	structured *judge.Reader
	facts      map[string]any
	setAside   bool
	outcome    supervise.Outcome
	firstStop  time.Time
}

// command runs the command for the key in its group with the terminal, judges it, records it and
// stores a receipt for a counted pass.
func (run *session) command(evidenceJudge *Evidence, key Key, keyErr error, held *flock.Held) int {
	did := &ran{key: key, keyErr: keyErr, judge: evidenceJudge, runID: RunID(run.clock.Now(), run.pid)}
	var decoder supervise.Sink = evidenceJudge
	kind := run.adapterKind()
	if kind != "" {
		options := judge.Options{Kind: kind}
		if veto := evidenceJudge.Veto(); veto != nil {
			options.Vetoes = []string{veto.String()}
		}
		if value, ok := run.entry["empty_exit"].(json.Number); ok {
			numeric, _ := value.Int64()
			options.EmptyExit = int(numeric)
		}
		var err error
		did.structured, err = judge.New(options)
		if err != nil {
			return run.notStarted(err.Error())
		}
		decoder = did.structured
	}
	env := setEnv(setEnv(run.env, supervise.RunIDVariable, did.runID), supervise.RunProjectVariable, run.project)
	var report *testreport.Report
	if kind == judge.JUnit {
		folder, err := os.MkdirTemp("", "jackstay-test-report.")
		if err != nil {
			return run.notStarted("the private report directory could not be made: " + err.Error())
		}
		defer func() { _ = os.RemoveAll(folder) }()
		name, _ := run.entry["report"].(string)
		report, err = testreport.OpenReport(folder, name)
		if err != nil {
			return run.notStarted("the private JUnit report could not be prepared: " + err.Error())
		}
		defer func() { _ = report.Close() }()
		env = setEnv(env, testreport.PathVariable, report.Path())
	}
	if keyErr == nil && run.storeErr == nil {
		// The command is about to run for this key: its receipt stops speaking for the tree now, so
		// a run that is stopped or fails leaves none behind.
		removed, err := run.store.Remove(key.Value)
		if err != nil {
			run.say("%v", err)
		}
		did.setAside = removed
	}
	kept, err := KeepOutput(run.project, did.runID, run.logPaths, run.clock.Now())
	if err != nil {
		run.say("the output of this run is not kept (%v); `jst test last` will not have it.", err)
	}
	did.kept = kept
	var evidence any
	if evidenceJudge.pattern != nil {
		evidence = evidenceJudge.pattern.String()
	}
	var output any
	if kept != nil {
		output = kept.Path
	}
	did.facts = map[string]any{"run_id": did.runID, "pid": run.pid, "locked": run.lockedRun || run.getenv(flock.HeldVariable) != "",
		"evidence": evidence, "receipt_removed": did.setAside, "output": output}
	if kind != "" {
		did.facts["judge"] = string(kind)
	}
	sessions, err := os.CreateTemp("", "jackstay-test-sessions.")
	sessionsFile := ""
	if err == nil {
		sessionsFile = sessions.Name()
		_ = sessions.Close()
		defer func() { _ = os.Remove(sessionsFile) }()
	}
	if sessionsFile != "" {
		env = setEnv(env, supervise.SessionsFileVariable, sessionsFile)
	}
	did.started = run.clock.Now()
	startFields := map[string]any{"run_id": did.runID, "pid": run.pid,
		"started_epoch": epochOf(did.started), "locked": did.facts["locked"]}
	if kind != "" {
		startFields["judge"] = string(kind)
	}
	if !run.runLog.Write(EventStart, did.keyValue(), startFields) {
		run.notLogged("the start of this run")
	}
	outcome, runErr := run.run(run.signals, supervise.Spec{Argv: run.asked.argv, Env: env, Dir: run.here,
		Mode: supervise.Group, Terminal: true, Stdin: os.Stdin,
		Output: &tee{stdout: run.stdout, stderr: run.stderr, kept: kept, judge: decoder},
		Grace:  run.groupWait(), ReadBound: run.readBound, SessionsFile: sessionsFile,
		Say: run.stderr, Who: "jst test", Clock: run.clock,
		Started: func(group int) {
			if held != nil {
				held.WriteNote(flock.NoteWords(fmt.Sprintf("project %s: %s in %s", run.project,
					strings.Join(run.asked.argv, " "), run.here), fmt.Sprintf("the command's process group %d", group), run.pid, did.started))
			}
		}})
	did.ended = run.clock.Now()
	did.outcome = outcome
	if _, at, asked := run.signals.First(); asked {
		did.firstStop = at
	}
	var startErr *supervise.StartError
	if errors.As(runErr, &startErr) {
		run.say("%v", startErr)
		return run.notAPass(did, OutcomeFailed, StatusNoStart, map[string]any{"status": StatusNoStart},
			fmt.Sprintf("outcome=failed (exit %d, %g s)", StatusNoStart, secondsBetween(did.started, did.ended)))
	}
	if report != nil && outcome.StoppedBy == 0 && run.signals.Context().Err() == nil {
		did.structured.ReadReport(report)
	}
	return run.judged(did, held)
}

func (run *session) adapterKind() judge.Kind {
	kind, _ := run.entry["judge"].(string)
	return judge.Kind(kind)
}

func (did *ran) keyValue() string {
	if did.keyErr != nil {
		return ""
	}
	return did.key.Value
}

// judged turns how the command ended and what its output said into the outcome.
func (run *session) judged(did *ran, held *flock.Held) int {
	outcome := did.outcome
	reading := did.judge.Reading()
	end := judge.End{Status: outcome.Status(), ReadProblem: unjudgedWhy(outcome, reading)}
	if outcome.StoppedBy != 0 {
		end.Signal = supervise.SignalName(outcome.StoppedBy)
	}
	var verdict judge.Verdict
	if did.structured != nil {
		end.LeftRunning = supervise.Names(outcome.Left)
		verdict = did.structured.VerdictWithPolicy(end, judge.DirectPolicy)
		reading.Veto, reading.Counted = verdict.VetoLine, verdict.Counted
		if verdict.Counting {
			did.facts["counted"], did.facts["skipped"] = verdict.Counted, verdict.Skipped
		}
	} else {
		facts := judge.Facts{Kind: judge.Exit, Counts: judge.Counts{Counted: reading.Counted, Counting: reading.Counts},
			Vetoed: reading.Vetoed, VetoLine: reading.Veto}
		if reading.Counts {
			facts.Kind = judge.Steps
		}
		verdict = judge.Evaluate(facts, end, judge.DirectPolicy)
	}
	took := secondsBetween(did.started, did.ended)
	if verdict.Outcome == judge.Stopped {
		name := supervise.SignalName(outcome.StoppedBy)
		ended := !did.firstStop.IsZero() && did.firstStop.After(did.ended)
		state := "the command was running"
		if ended {
			state = "the command had ended and was being judged"
		}
		run.notAPass(did, OutcomeStopped, 0, map[string]any{"signal": name, "status": 128 + int(outcome.StoppedBy),
			"command_ended": ended}, fmt.Sprintf("outcome=stopped (%s after %g s; %s; no receipt)", name, took, state))
		if held != nil {
			held.Release()
		}
		run.signals.Close()
		return run.endBy(outcome.StoppedBy)
	}
	if status := outcome.Status(); verdict.Outcome == judge.Failed && status != 0 {
		return run.notAPass(did, OutcomeFailed, status, map[string]any{"status": status},
			fmt.Sprintf("outcome=failed (exit %d, %g s)", status, took))
	}
	if verdict.Outcome == judge.Failed {
		if did.structured != nil && verdict.Failure != "" {
			return run.notAPass(did, OutcomeFailed, StatusVeto,
				map[string]any{"status": StatusVeto, "command_status": 0, "why": verdict.Why},
				fmt.Sprintf("outcome=failed (exit %d: %s, %g s)", StatusVeto, verdict.Why, took))
		}
		run.say("the command ended 0, but its output holds a line its veto pattern names: %q. That is a failure, whatever the status says.",
			reading.Veto)
		return run.notAPass(did, OutcomeFailed, StatusVeto,
			map[string]any{"status": StatusVeto, "command_status": 0, "veto_line": reading.Veto},
			fmt.Sprintf("outcome=failed (exit %d: a veto line in the output of a command that ended 0, %g s)", StatusVeto, took))
	}
	if verdict.Outcome == judge.Unjudged {
		why := verdict.Why
		run.say("the command ended 0, but %s. This is not a pass.", why)
		return run.notAPass(did, OutcomeUnjudged, StatusUnjudged,
			map[string]any{"status": StatusUnjudged, "command_status": 0, "why": why},
			fmt.Sprintf("outcome=unjudged (exit %d: the output of a command that ended 0 could not be judged, %g s)", StatusUnjudged, took))
	}
	if verdict.Outcome == judge.NothingRun {
		if did.structured != nil {
			run.say("the structured report shows no executed tests.")
		} else {
			evidence, _ := run.entry["evidence"].(string)
			run.say("the command ended 0 but its output shows no test (%q counted nothing). This is not a pass.", evidence)
		}
		return run.notAPass(did, OutcomeNothingRun, StatusNothingRun, nil, fmt.Sprintf("outcome=nothing-run (%g s)", took))
	}
	if verdict.Outcome == judge.ExitedZero {
		written := run.line(did, OutcomeExited0, map[string]any{"evidence_count": nil, "stored": false,
			"not_stored": reading.NoCount, "not_stored_kind": NotStoredNoEvidence})
		run.say("the command ended 0; nothing counted its tests, so this is not a pass, and no receipt stored: %s.", reading.NoCount)
		run.last(did, written, fmt.Sprintf("outcome=exited-0 (no test count: not proof that tests ran), %g s", took))
		return 0
	}
	stored, whyNot, kind := run.store1(did, reading.Counted)
	fields := map[string]any{"evidence_count": reading.Counted, "stored": stored != "", "not_stored": nullable(whyNot)}
	if kind != "" {
		fields["not_stored_kind"] = kind
	}
	written := run.line(did, OutcomePassed, fields)
	if stored != "" {
		run.say("passed; receipt stored: %s", stored)
	} else {
		run.say("passed; no receipt stored: %s.", whyNot)
	}
	run.last(did, written, fmt.Sprintf("outcome=passed (%g s, tests counted: %d)", took, reading.Counted))
	return 0
}

// unjudgedWhy is why a run that ended 0 cannot be judged ("" when it can): its output was not read
// to its end, what it left could not be confirmed gone, or the evidence matched no number.
func unjudgedWhy(outcome supervise.Outcome, reading Reading) string {
	if outcome.Unread != "" {
		return outcome.Unread
	}
	if len(outcome.Problems) > 0 {
		return fmt.Sprintf("the end of the command's process group cannot be confirmed: %v", outcome.Problems[0])
	}
	return reading.Unjudged
}

// store1 writes the receipt of a counted pass when every rule allows it: (path, why not, kind).
func (run *session) store1(did *ran, counted int) (stored, whyNot, kind string) {
	if did.keyErr != nil {
		return "", did.keyErr.Error(), NotStoredNoKey
	}
	if run.storeErr != nil {
		return "", fmt.Sprintf("the receipt could not be written: %v", run.storeErr), NotStoredWriteFailed
	}
	after, err := run.key()
	if err != nil {
		return "", err.Error(), NotStoredNoKey
	}
	if after.Value != did.key.Value {
		return "", "the tree changed while the command ran", NotStoredTreeChanged
	}
	var rewritten []string
	for name, stamp := range after.Stamps {
		if did.key.Stamps[name] != stamp {
			rewritten = append(rewritten, name)
		}
	}
	if len(rewritten) > 0 {
		sort.Strings(rewritten)
		more := ""
		if len(rewritten) > 3 {
			more = fmt.Sprintf(" and %d more", len(rewritten)-3)
		}
		return "", "an input file was rewritten while the command ran (its content is the same now): " +
			strings.Join(rewritten[:min(3, len(rewritten))], ", ") + more, NotStoredInputRewritten
	}
	if failed, found := run.store.FailureTime(did.key.Value); found && !failed.Before(did.started) {
		return "", "another run of this same tree failed after this one had started", NotStoredFailedSince
	}
	receipt := Receipt{Schema: Schema, Key: did.key.Value, Outcome: OutcomePassed, Project: run.project,
		Command: slices.Clone(run.asked.argv), Seconds: secondsBetween(did.started, did.ended), EvidenceCount: counted,
		Started: timeText(did.started), StartedEpoch: epochOf(did.started), Ended: timeText(did.ended),
		EndedEpoch: epochOf(did.ended), Material: did.key.Material, Jackstay: version.Version, RunID: did.runID,
		Worktree: run.runLog.Where.Worktree, Head: run.runLog.Where.Head, Branch: run.runLog.Where.Branch}
	place, err := run.store.Write(receipt)
	if err != nil {
		return "", fmt.Sprintf("the receipt could not be written: %v", err), NotStoredWriteFailed
	}
	return place, "", ""
}

// notAPass records a run that did not pass: the note first (a pass of the same key ending now must
// see it), the receipt removed again, the line, and the last lines.
func (run *session) notAPass(did *ran, outcome string, status int, fields map[string]any, words string) int {
	if did.keyErr == nil && run.storeErr == nil {
		run.store.NoteFailure(did.key.Value, outcome)
		if removed, err := run.store.Remove(did.key.Value); err == nil && removed {
			did.setAside = true
		}
	}
	did.facts["receipt_removed"] = did.setAside
	written := run.line(did, outcome, fields)
	if did.setAside {
		run.say("this tree had a receipt; it is removed, since the same tree has now not passed.")
	}
	run.last(did, written, words)
	return status
}

// line writes the run's line; whether it was written.
func (run *session) line(did *ran, outcome string, more map[string]any) bool {
	fields := map[string]any{"outcome": outcome, "started_epoch": epochOf(did.started), "ended_epoch": epochOf(did.ended),
		"seconds": secondsBetween(did.started, did.ended)}
	for name, value := range did.facts {
		fields[name] = value
	}
	if len(did.outcome.Left) > 0 {
		fields["left_running"] = supervise.Names(did.outcome.Left)
	}
	if len(did.outcome.Survivors) > 0 {
		var named []string
		for _, survivor := range did.outcome.Survivors {
			named = append(named, fmt.Sprintf("%s[%d]", survivor.Name, survivor.PID))
		}
		fields["survivors"] = strings.Join(named, ",")
	}
	if did.outcome.Killed {
		fields["killed"] = true
	}
	for name, value := range more {
		fields[name] = value
	}
	return run.runLog.Write(EventRun, did.keyValue(), fields)
}

// last says, in this order: what in the output went wrong (when the run did not pass), where the
// whole output is, that the record has a hole (when it has), and the outcome line, last.
func (run *session) last(did *ran, written bool, words string) {
	whole := did.kept.Close()
	if did.kept != nil && !strings.HasPrefix(words, "outcome=passed") {
		lines, more := NotableLines(did.kept.Path, did.judge.Veto())
		WriteNotable(run.stderr, "jst test: ", lines, more)
	}
	switch {
	case did.kept != nil && !whole:
		run.say("the kept output is not whole (writing it failed): %s", did.kept.Path)
	case did.kept != nil:
		run.say("the whole output: %s  (`jst test last` shows this again)", did.kept.Path)
	}
	if !written {
		run.notLogged("this run")
	}
	run.sayUnlogged()
	run.say("%s", words)
}
