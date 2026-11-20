package scopes

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/judge"
	"github.com/zima-oss/jackstay/internal/lock"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/receipts"
	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/testreport"
)

// Variables every scope's command gets (testing.md section 6).
const (
	RunIDVariable  = "JACKSTAY_RUN_ID"
	RunTmpVariable = "JACKSTAY_RUN_TMP"
)

// result is how one leaf ended, for the gate.
type result struct {
	outcome string
	status  int
	seconds float64
	runID   string
	why     string
	counted int
	// signalled says jackstay was asked to stop: the caller ends at once with status, writing nothing more.
	signalled bool
}

// words are a leaf's words as they would run now, its base, and `{slow}`'s classes and note.
type words struct {
	argv          []string
	goSelection   *judge.GoSelection
	jestSelection *judge.JestSelection
	base          string
	classes       []slowClass
	note          string
	quick         string
}

// wordsOf is a leaf's words as they would run now; a refusal when the call cannot fill them.
func (proj *project) wordsOf(chosen leaf, args []string) (words, error) {
	found := words{base: proj.options.base}
	var selectedArgs []string
	switch chosen.selector {
	case goSelector:
		selection, err := parseGoSelection(args)
		if err != nil {
			return found, err
		}
		found.goSelection = &selection
		selectedArgs = goSelectionArgs(selection)
	case jestSelector:
		selection, err := parseJestSelection(args)
		if err != nil {
			return found, err
		}
		found.jestSelection = &selection
		selectedArgs = jestSelectionArgs(selection)
	}
	if found.base == "" {
		found.base = chosen.base
	}
	slow := ""
	if slices.Contains(placeholdersOf(chosen.run), "slow") {
		found.classes, found.note = slowClasses(proj.name, chosen.slow)
		slow = slowPattern(found.classes)
		switch {
		case found.note != "":
			found.quick = strings.TrimPrefix(found.note, "quick=")
		case len(found.classes) > 0:
			names := make([]string, 0, len(found.classes))
			for _, class := range found.classes {
				names = append(names, class.name)
			}
			found.quick = "skipped " + strings.Join(names, ",")
		default:
			found.quick = "everything (no class is slow by the rule)"
		}
	}
	argv, err := expand(chosen, args, found.base, slow, selectedArgs)
	found.argv = argv
	return found, err
}

// definition is what a leaf runs, as a key holds it.
func definition(chosen leaf, argv []string, relative string) map[string]any {
	var timeout, expect any
	if chosen.timeout > 0 {
		timeout = chosen.timeout
	}
	if chosen.judge == "swift-test" {
		expect = chosen.expect
	}
	var evidence, veto any
	if chosen.evidence != "" {
		evidence = chosen.evidence
	}
	if chosen.veto != "" {
		veto = chosen.veto
	}
	material := map[string]any{"name": chosen.name, "kind": chosen.kind, "run": chosen.run, "words": argv, "cwd": relative,
		"judge": chosen.judge, "evidence": evidence, "veto": veto, "timeout": timeout, "env": chosen.env, "expect": expect}
	if adapter := judge.AdapterVersion(judge.Kind(chosen.judge)); adapter != 0 {
		material["adapter_version"] = adapter
		if chosen.selector != "" {
			material["selector"] = chosen.selector
		}
		if chosen.durations && (chosen.judge == string(judge.GoJSON) || chosen.judge == string(judge.JUnit) || chosen.judge == string(judge.XCResultJSON)) {
			material["durations"] = true
		}
		if chosen.report != "" {
			material["report"], material["empty_exit"] = chosen.report, chosen.emptyExit
		}
		if len(chosen.xcresultBundles) != 0 {
			material["xcresult_bundles"] = chosen.xcresultBundles
		}
		if len(chosen.reports) != 0 {
			material["reports"], material["empty_exit"] = chosen.reports, chosen.emptyExit
		}
	}
	return material
}

// commandEnv is an environment less what jackstay tells the process around it (the lock's outcome
// file, its group wait, the sessions file) and the run's own variables, which are set anew.
func commandEnv(environ []string) []string {
	dropped := []string{lock.OutcomeFileVariable, lock.NoteWordsVariable, lock.GroupWaitVariable, supervise.SessionsFileVariable,
		supervise.RunIDVariable, supervise.RunProjectVariable, RunIDVariable, RunTmpVariable, testreport.PathVariable,
		testreport.DirectoryVariable}
	kept := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(dropped, name) {
			kept = append(kept, entry)
		}
	}
	return kept
}

// setEnv is env with name set to value.
func setEnv(env []string, name, value string) []string {
	kept := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if current, _, _ := strings.Cut(entry, "="); current != name {
			kept = append(kept, entry)
		}
	}
	return append(kept, name+"="+value)
}

// withEnv is env with every variable of a leaf's `env` set, by name.
func withEnv(env []string, chosen map[string]string) []string {
	for _, name := range sortedKeys(chosen) {
		env = setEnv(env, name, chosen[name])
	}
	return env
}

// leafRun is one leaf's run: what it is, and what was found on the way.
type leafRun struct {
	*project
	leaf    leaf
	words   words
	gate    string
	runCwd  string
	facts   map[string]any
	entry   map[string]any
	key     receipts.Key
	keyErr  error
	store   receipts.Store
	storeOK bool
	env     []string
	locked  bool
	keyed   receipts.Keying
}

// notStarted says why nothing ran and returns the result.
func (proj *project) notStarted(why string) result {
	proj.say("%s", why)
	proj.say("outcome=not-started (nothing was run)")
	return result{outcome: receipts.OutcomeNotStarted, status: statusRefused, why: why}
}

// runLeaf runs one leaf as `jst test <scope>` does: `requires` (unless checked), a receipt, the
// lock, the command in its session, the judge, the record. gate is the gate's run id ("" alone).
func (proj *project) runLeaf(chosen leaf, args []string, gate string, checked bool) result {
	found, err := proj.wordsOf(chosen, args)
	if err != nil {
		return proj.notStarted(err.Error())
	}
	run := &leafRun{project: proj, leaf: chosen, words: found, gate: gate,
		runCwd: filepath.Clean(filepath.Join(proj.root, chosen.cwd))}
	if info, statErr := os.Stat(run.runCwd); statErr != nil || !info.IsDir() {
		return proj.notStarted(fmt.Sprintf("`%s` runs in %s, which is not a folder of this checkout", chosen.name, chosen.cwd))
	}
	run.facts = map[string]any{}
	if chosen.scope != "" {
		run.facts["scope"] = chosen.scope
	}
	if gate != "" {
		run.facts["step"], run.facts["gate_run_id"] = chosen.name, gate
	}
	run.env = withEnv(commandEnv(proj.environ), chosen.env)
	if !checked {
		for _, each := range chosen.requires {
			if stopped, done := run.require(each); done {
				return proj.ended(stopped, nil)
			}
		}
	}
	relative, relErr := filepath.Rel(proj.root, run.runCwd)
	if relErr != nil {
		relative = chosen.cwd
	}
	material := definition(chosen, found.argv, relative)
	material["leftovers"] = items(proj.section["leftovers"])
	run.entry = receipts.CommandEntry(proj.receipts, found.argv)
	run.keyed = receipts.Keying{Root: proj.root, Config: proj.loaded, Project: proj.name, Command: found.argv, Dir: run.runCwd,
		Scope: material, Environ: run.env}
	run.store, err = receipts.OpenStore(proj.name, proj.receipts, proj.clock)
	run.storeOK = err == nil
	// A scope takes part in receipts (the lookups, would-hit) only when its words are a
	// receipts.commands entry with "reuse": true, and never inside a gate (testing.md "Details of the behaviour"). The lookup
	// under the lock measures a repeat even when reuse is not allowed (--no-reuse); it reuses
	// nothing then.
	measure := gate == "" && run.entry != nil && run.entry["reuse"] == true
	reuse := measure && !proj.options.noReuse
	run.locked = lockEnabled(proj) && chosen.lock
	if proj.options.lock != nil {
		run.locked = *proj.options.lock
	}
	if reuse && run.locked {
		// The lookup is made before the lock (a hit does not wait in line) and again under it.
		if key, keyErr := receipts.KeyOf(proj.ctx, run.keyed); keyErr == nil && run.lookUp(key, false) {
			return result{outcome: receipts.OutcomeReused}
		}
	}
	barKey := ""
	if gate == "" && chosen.resume {
		if dirty := proj.cleanCheckout(); dirty == "" {
			barKey, _ = proj.stepKey(chosen, found.argv, found.base, relative)
			if barKey != "" {
				// It runs again: a stop now must not leave the old pass standing.
				proj.removeMark(barKey)
			}
		}
	}
	asked := proj.clock.Now()
	var held *flock.Held
	if run.locked {
		var ended *result
		if held, ended = run.takeLock(); ended != nil {
			return proj.ended(*ended, nil)
		}
	}
	defer held.Release()
	run.key, run.keyErr = receipts.KeyOf(proj.ctx, run.keyed)
	if measure && run.keyErr == nil && run.lookUp(run.key, true) {
		return result{outcome: receipts.OutcomeReused}
	}
	ended := run.command(asked, held)
	switch {
	case barKey != "" && ended.outcome == receipts.OutcomeFailed:
		// The same step failed on the tree it started on (clean, keyed before it ran): a gate's
		// --resume must not stand on an earlier mark of it, whatever the run left in the checkout.
		proj.writeBar(barKey, chosen.name, ended)
	case barKey != "" && ended.outcome != receipts.OutcomePassed && ended.outcome != receipts.OutcomeExited0:
		proj.removeMark(barKey) // the latest run on this tree gave no verdict
	}
	return proj.ended(ended, held)
}

// ended is a leaf's result; one that jackstay was asked to stop ends jackstay by that signal, the lock given
// back and the signals let go first, so the signal has its default effect.
func (proj *project) ended(ended result, held *flock.Held) result {
	if !ended.signalled {
		return ended
	}
	held.Release()
	proj.signals.Close()
	ended.status = proj.endBy(ended.signal())
	return ended
}

// signal is the request to stop a signalled result ended by.
func (ended result) signal() syscall.Signal {
	return syscall.Signal(ended.status - 128) // #nosec G115 -- a status of 128 and a signal's number
}

// require runs one `requires` check; done when it did not pass (the run is over: its result).
func (run *leafRun) require(each check) (result, bool) {
	checked := run.runCheck(each, run.runCwd, run.env)
	if checked.passed() {
		return result{}, false
	}
	why := checkWhy(each, checked)
	for _, line := range checked.tail {
		run.say("| %s", line)
	}
	fields := map[string]any{"outcome": receipts.OutcomeNotStarted, "why": why, "requires": each.run,
		"requires_session": nullableInt(checked.session), "requires_killed": nullableText(checked.killed),
		"requires_survivors": nullableText(checked.survivors)}
	for name, value := range run.facts {
		fields[name] = value
	}
	if !run.log(receipts.EventRun, run.words.argv, "", fields) {
		run.notLogged("this run, which did not start")
	}
	run.say("outcome=not-started (exit 2: %s; nothing ran)", why)
	if checked.asked != 0 {
		return result{outcome: receipts.OutcomeNotStarted, status: 128 + int(checked.asked), why: why, signalled: true}, true
	}
	return result{outcome: receipts.OutcomeNotStarted, status: statusRefused, why: why}, true
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// lockEnabled is lock.enabled (W-005: off unless the project turns it on).
func lockEnabled(proj *project) bool { return proj.loaded.Section("lock")["enabled"] == true }

// takeLock waits for the machine-wide lock and the project's older ones; a result when the run
// ends here (it gave up, the lock cannot be used, or it was stopped while it waited).
func (run *leafRun) takeLock() (*flock.Held, *result) {
	path, err := flock.Path()
	if err != nil {
		ended := run.notStarted(err.Error())
		return nil, &ended
	}
	table := procs.Table()
	if flock.UnderHolder(path, run.getenv(flock.HeldVariable), table, run.pid) {
		return nil, nil // under a holder that still holds it: its turn is ours
	}
	var older []flock.Older
	for _, raw := range items(run.loaded.Section("lock")["also_hold"]) {
		entry := objectOf(raw)
		older = append(older, flock.Older{Path: expandHome(textOf(entry["path"])), Variable: textOf(entry["held_env"])})
	}
	plan, err := flock.PlanOlder(path, older, run.getenv, table, run.pid)
	if err != nil {
		ended := run.notStarted(err.Error())
		return nil, &ended
	}
	began := run.clock.Now()
	who := fmt.Sprintf("project %s: %s in %s", run.name, joinSpace(run.words.argv), run.root)
	wait, bounded := run.leaf.lockWait, run.leaf.lockWait > 0
	if run.options.hasWait {
		wait, bounded = run.options.wait, true
	}
	held, err := run.acquire(run.signals.Context(), flock.Request{Lock: path, Older: plan.Older,
		Words: fmt.Sprintf("%s (process %d), since %s", who, run.pid, began.Format("2006-01-02 15:04:05")),
		Wait:  time.Duration(wait * float64(time.Second)), Bounded: bounded, Out: run.stderr})
	waited := secondsBetween(began, run.clock.Now())
	var gaveUp *flock.GaveUp
	var notUsable *flock.Unusable
	var stopped *supervise.StopRequest
	switch {
	case errors.As(err, &gaveUp):
		run.lockLine(receipts.OutcomeGaveUp, map[string]any{"waited_seconds": waited}, "this run, which gave up waiting")
		run.say("outcome=gave-up (nothing was run)")
		return nil, &result{outcome: receipts.OutcomeGaveUp, status: receipts.StatusGaveUp}
	case errors.As(err, &notUsable):
		ended := run.notStarted(notUsable.Words)
		return nil, &ended
	case errors.As(err, &stopped):
		name := supervise.SignalName(stopped.Signal)
		run.lockLine(receipts.OutcomeStoppedWaiting, map[string]any{"signal": name, "status": 128 + int(stopped.Signal),
			"waited_seconds": waited}, "this run, stopped while it waited")
		run.say("outcome=stopped-waiting (%s after %s s; nothing was run)", name, formatSeconds(waited))
		return nil, &result{outcome: receipts.OutcomeStoppedWaiting, status: 128 + int(stopped.Signal), signalled: true}
	case err != nil:
		ended := run.notStarted(err.Error())
		return nil, &ended
	}
	held.WriteNote(flock.NoteWords(who, "about to start", run.pid, began))
	run.env = setEnv(run.env, flock.HeldVariable, fmt.Sprintf("%s:%d", flock.RealPath(path), run.pid))
	for _, name := range sortedKeys(plan.Variables) {
		run.env = setEnv(run.env, name, plan.Variables[name])
	}
	return held, nil
}

// lockLine writes the run line of a run that ended at the lock.
func (run *leafRun) lockLine(outcome string, fields map[string]any, what string) {
	fields["outcome"] = outcome
	for name, value := range run.facts {
		fields[name] = value
	}
	if !run.log(receipts.EventRun, run.words.argv, "", fields) {
		run.notLogged(what)
	}
}

func expandHome(place string) string {
	if place == "~" || strings.HasPrefix(place, "~/") {
		return expandGlob(place, "", "", "")
	}
	return place
}

// secondsBetween is a span in seconds to the tenth, as the record holds it.
func secondsBetween(start, end time.Time) float64 {
	return float64(end.Sub(start).Round(100*time.Millisecond)) / float64(time.Second)
}

// epochText is a moment as the log writes it: seconds since 1970, to the millisecond.
func epochText(moment time.Time) float64 {
	return float64(moment.Round(time.Millisecond).UnixMilli()) / 1000
}

// sink passes the command's output on to jackstay's streams after its judge has read it, and keeps it.
type sink struct {
	stdout, stderr io.Writer
	kept           *receipts.Kept
	reader         *judge.Reader
}

func (out *sink) Line(stream supervise.Stream, text string) error {
	_ = out.reader.Line(stream, text) // judged before it is passed on: a line anyone saw, the judge read
	target := out.stdout
	if stream == supervise.Stderr {
		target = out.stderr
	}
	_, _ = io.WriteString(target, text+"\n") // a closed reader does not stop the run
	out.kept.Line(text)
	return nil
}

// judgeOptions are the leaf's judge: its kind, engines, evidence, and its own veto with the
// receipts entry's.
func (run *leafRun) judgeOptions() judge.Options {
	options := judge.Options{Kind: judge.Kind(run.leaf.judge), Evidence: run.leaf.evidence, EmptyExit: run.leaf.emptyExit}
	if options.Kind == judge.GoJSON {
		options.GoSelection = run.words.goSelection
		options.GoDurations = run.leaf.durations
	}
	options.JUnitDurations = options.Kind == judge.JUnit && run.leaf.durations
	options.XCResultDurations = options.Kind == judge.XCResultJSON && run.leaf.durations
	options.XCResultBundles = run.leaf.xcresultBundles
	if selected := run.words.jestSelection; selected != nil {
		// Processes see the physical working directory even when the configured cwd uses a link.
		root := run.runCwd
		if physical, err := filepath.EvalSymlinks(root); err == nil {
			root = physical
		}
		options.JestSelection = &judge.JestSelection{
			File: filepath.Join(root, selected.File),
			Name: selected.Name,
		}
	}
	if run.leaf.judge == "swift-test" {
		for _, engine := range run.leaf.expect {
			options.Expect = append(options.Expect, judge.Engine(engine))
		}
	}
	if run.leaf.veto != "" {
		options.Vetoes = append(options.Vetoes, run.leaf.veto)
	}
	if veto := textOf(run.entry["veto"]); veto != "" {
		options.Vetoes = append(options.Vetoes, veto)
	}
	return options
}

// watchStops tells the reader when a request to stop jackstay comes, until done is closed.
func watchStops(signals *supervise.Signals, reader *judge.Reader, done <-chan struct{}) *errgroup.Group {
	var watching errgroup.Group
	watching.Go(func() error {
		select {
		case <-signals.Context().Done():
			reader.Stopping()
		case <-done:
		}
		return nil
	})
	return &watching
}
