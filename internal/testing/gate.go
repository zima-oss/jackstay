package scopes

import (
	"bytes"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/receipts"
)

// gitText is git's output in the checkout, or nil when git failed.
func (proj *project) gitText(args ...string) []byte {
	out, err := proj.git(proj.ctx, proj.root, args...)
	if err != nil {
		return nil
	}
	return out
}

func zeroSeparated(listed []byte) []string {
	var found []string
	for _, name := range bytes.Split(listed, []byte{0}) {
		if len(name) > 0 {
			found = append(found, string(name))
		}
	}
	return found
}

// head is HEAD's commit, or "".
func (proj *project) head() string {
	return strings.TrimSpace(string(proj.gitText("rev-parse", "--verify", "--quiet", "HEAD^{commit}")))
}

// step is one node's line in the run's record.
type step map[string]any

// Outcomes only a graph has (docs/design/land.md section 7.2 and 7.4).
const (
	outcomeSkipped          = "skipped"
	outcomeNotRun           = "not-run"
	outcomeNeedsOtherSystem = "needs-other-system"
	outcomeLeftToCI         = "left-to-ci"

	statusSkipped          = 79
	statusNeedsOtherSystem = 80
)

// failureOrder is the order in which a node's outcome that is not a pass becomes the run's
// (section 7.2): the first the run has wins, with or without --keep-going.
var failureOrder = []string{receipts.OutcomeFailed, receipts.OutcomePassedOnRetry, outcomeTimedOut, receipts.OutcomeStopped,
	receipts.OutcomeStoppedWaiting, receipts.OutcomeUnjudged, receipts.OutcomeNothingRun, receipts.OutcomeGaveUp,
	receipts.OutcomeNotStarted}

// passing says whether a node that ended so lets the nodes that depend on it run.
func passing(outcome string) bool {
	switch outcome {
	case receipts.OutcomePassed, receipts.OutcomeExited0, outcomeSkipped:
		return true
	}
	return false
}

// partition is where `jst test changed` and `jst test uncovered` put each node with `run`
// (section 7.4): run here, left to CI, or needing another system.
type partition struct {
	command   string
	here      []string
	ci        []string
	elsewhere []string
	// where are the systems each node elsewhere can run on.
	where map[string][]string
}

// gateState is a run so far.
type gateState struct {
	steps    []step
	outcome  string
	status   int
	failedAt string
}

// fail keeps the first outcome that was not a pass.
func (state *gateState) fail(outcome string, status int, stepName string) {
	if state.outcome == "" {
		state.outcome, state.status, state.failedAt = outcome, status, stepName
	}
}

// gate runs the graph from the scopes asked for (section 7.2): each node once, after its deps, one
// at a time; the first failure ends the run unless --keep-going. label names the run in its line.
func (proj *project) gate(label string, asked []string, split *partition) int {
	nodes := newGraph(proj.section, proj.operatingSystem)
	nodes.withoutBase = label == uncoveredCommand
	order := nodes.walk(asked)
	gateID := receipts.RunID(proj.clock.Now(), proj.pid)
	began := proj.clock.Now()
	plural := "s"
	if len(order) == 1 {
		plural = ""
	}
	proj.say("%s: %d node%s: %s", label, len(order), plural, joinComma(order))
	startFields := map[string]any{"gate_run_id": gateID, "scope": label, "started_epoch": epochText(began), "steps": nonNil(order)}
	// A run stopped or killed while a node runs ends with that node (its own line says `stopped`);
	// this line, with no `gate` line of the same id, says the run did not end.
	if !proj.log("gate-start", []string{label}, "", startFields) {
		proj.notLogged("the start of this run")
	}
	state := &gateState{}
	if problem := proj.pinnedProblem(); problem != "" {
		proj.say("%s", problem)
		state.fail(receipts.OutcomeUnjudged, receipts.StatusUnjudged, "checkout validation")
		return proj.gateLine(label, gateID, began, state, split)
	}
	// A schema-2 list's own `requires` run once, before any node, and gate the whole run.
	for _, name := range order {
		if _, entry := find(proj.section, name); !isComposite(entry) || !nodes.eligible(name) {
			continue
		}
		why, signalled := proj.compositeRequires(name)
		if signalled != nil {
			return *signalled
		}
		if why != "" {
			proj.say("not started (%s)", why)
			state.fail(receipts.OutcomeNotStarted, statusRefused, name)
			return proj.gateLine(label, gateID, began, state, split)
		}
	}
	outcomes := map[string]string{}
	ended := false
	for _, name := range order {
		kind, entry := find(proj.section, name)
		var line step
		switch {
		case ended:
			line = step{"step": name, "outcome": outcomeNotRun, "seconds": 0}
		case !nodes.eligible(name):
			line = step{"step": name, "outcome": outcomeSkipped, "seconds": 0}
		case slices.ContainsFunc(depsOf(entry), func(dep string) bool { return !passing(outcomes[dep]) }):
			line = step{"step": name, "outcome": outcomeNotRun, "seconds": 0}
		case isComposite(entry):
			// A composite passes when every eligible dep passed; one that only exited 0 (a `test`
			// or undeclared scope that counted nothing) makes it `exited-0`, not a pass.
			outcome := receipts.OutcomePassed
			if slices.ContainsFunc(depsOf(entry), func(dep string) bool { return outcomes[dep] == receipts.OutcomeExited0 }) {
				outcome = receipts.OutcomeExited0
			}
			line = step{"step": name, "outcome": outcome, "seconds": 0, "composite": true}
		default:
			var signalled *int
			if line, signalled = proj.gateStep(name, kind, entry, gateID); signalled != nil {
				return *signalled
			}
		}
		outcome := textOf(line["outcome"])
		outcomes[name] = outcome
		state.steps = append(state.steps, line)
		if !passing(outcome) && outcome != outcomeNotRun && !proj.options.keepGoing {
			ended = true
		}
	}
	state.settle(asked, outcomes)
	if split != nil && !slices.Contains(failureOrder, state.outcome) {
		switch {
		case split.command == "changed" && len(split.elsewhere) > 0:
			state.outcome, state.status = outcomeNeedsOtherSystem, statusNeedsOtherSystem
		case len(split.here) == 0:
			state.outcome, state.status = outcomeLeftToCI, 0
		}
	}
	if problem := proj.pinnedProblem(); problem != "" && !slices.Contains(failureOrder, state.outcome) {
		proj.say("%s", problem)
		state.outcome, state.status, state.failedAt = receipts.OutcomeUnjudged, receipts.StatusUnjudged, "checkout validation"
	}
	return proj.gateLine(label, gateID, began, state, split)
}

// settle is the run's outcome from its nodes' (section 7.2): the first failure in failureOrder;
// else `skipped` when a scope asked for is not eligible here; `exited-0` when a scope asked for
// ended so; else `passed`.
func (state *gateState) settle(asked []string, outcomes map[string]string) {
	if state.outcome != "" {
		return
	}
	for _, wanted := range failureOrder {
		for _, line := range state.steps {
			if line["outcome"] != wanted {
				continue
			}
			status, _ := line["status"].(int)
			if status == 0 {
				status = 1
			}
			state.fail(wanted, status, textOf(line["step"]))
			return
		}
	}
	switch {
	case slices.ContainsFunc(asked, func(name string) bool { return outcomes[name] == outcomeSkipped }):
		state.outcome, state.status = outcomeSkipped, statusSkipped
	case slices.ContainsFunc(asked, func(name string) bool { return outcomes[name] == receipts.OutcomeExited0 }):
		state.outcome = receipts.OutcomeExited0
	default:
		state.outcome = receipts.OutcomePassed
	}
}

func nonNil(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}

// gateStep runs one node with `run`: its `requires`, then the command. signalled is set when
// jackstay was asked to stop: the status it ends with.
func (proj *project) gateStep(name, kind string, entry map[string]any, gateID string) (step, *int) {
	chosen := resolve(proj.section, name, kind, entry)
	found, err := proj.wordsOf(chosen, proj.argumentsOf(name))
	if err != nil {
		proj.say("%v", err)
		return step{"step": name, "outcome": receipts.OutcomeNotStarted, "seconds": 0, "status": statusRefused}, nil
	}
	whyNot := ""
	cwd := filepath.Clean(filepath.Join(proj.root, chosen.cwd))
	for _, each := range chosen.requires {
		result := proj.runCheck(each, cwd, withEnv(commandEnv(proj.environ), chosen.env))
		if result.asked != 0 {
			status := proj.ended(result.stoppedResult(), nil).status
			return nil, &status
		}
		if !result.passed() {
			whyNot = name + ": " + checkWhy(each, result)
			break
		}
	}
	if problem := proj.pinnedProblem(); problem != "" {
		whyNot = problem
	}
	if whyNot != "" {
		proj.say("step %s: not started (%s)", name, whyNot)
		return step{"step": name, "outcome": receipts.OutcomeNotStarted, "seconds": 0, "why": whyNot, "status": statusRefused}, nil
	}
	proj.say("step %s: %s", name, shellWords(found.argv))
	ended := proj.runLeaf(chosen, proj.argumentsOf(name), gateID, true)
	if ended.signalled {
		return nil, &ended.status
	}
	return step{"step": name, "outcome": ended.outcome, "seconds": ended.seconds, "run_id": nullableText(ended.runID),
		"status": ended.status}, nil
}

// stoppedResult is the result of a check during which jackstay was asked to stop.
func (result checkResult) stoppedResult() result {
	return resultOf(receipts.OutcomeNotStarted, 128+int(result.asked), true)
}

func resultOf(outcome string, status int, signalled bool) result {
	return result{outcome: outcome, status: status, signalled: signalled}
}

// compositeRequires is why a list of steps may not run now (its first failing `requires`), or "".
// signalled is set when jackstay was asked to stop meanwhile: the status jackstay ends with.
func (proj *project) compositeRequires(composite string) (string, *int) {
	_, entry := find(proj.section, composite)
	for _, each := range checksOf(entry) {
		result := proj.runCheck(each, proj.root, commandEnv(proj.environ))
		if result.asked != 0 {
			status := proj.ended(result.stoppedResult(), nil).status
			return "", &status
		}
		if !result.passed() {
			for _, line := range result.tail {
				proj.say("| %s", line)
			}
			return composite + ": " + checkWhy(each, result), nil
		}
	}
	return "", nil
}

// gateLine writes the run's line (each node's outcome, seconds and run id) and says its outcome.
func (proj *project) gateLine(label, gateID string, began time.Time, state *gateState, split *partition) int {
	endedAt := proj.clock.Now()
	seconds := secondsBetween(began, endedAt)
	failure := slices.Contains(failureOrder, state.outcome)
	why := ""
	if failure && state.failedAt != "" {
		why = "failed at " + state.failedAt
	}
	steps := state.steps
	if steps == nil {
		steps = []step{}
	}
	fields := map[string]any{"gate_run_id": gateID, "scope": label, "outcome": state.outcome, "status": state.status,
		"seconds": seconds, "steps": steps, "started_epoch": epochText(began), "ended_epoch": epochText(endedAt),
		"system": proj.operatingSystem}
	if why != "" {
		fields["why"] = why
	}
	if split != nil {
		fields["here"], fields["ci"], fields["elsewhere"] = nonNil(split.here), nonNil(split.ci), nonNil(split.elsewhere)
	}
	pinnedPass := proj.pin != nil && (state.outcome == receipts.OutcomePassed || state.outcome == outcomeLeftToCI)
	if pinnedPass {
		fields["pinned_candidate"] = proj.pin.commit
	}
	if !proj.log(receipts.EventGate, []string{label}, "", fields) {
		proj.notLogged("this run")
		if pinnedPass {
			state.outcome, state.status = receipts.OutcomeUnjudged, receipts.StatusUnjudged
			why = "could not record the pinned run"
		}
	} else if pinnedPass {
		proj.pin.runID = gateID
	}
	// A composite that passed says nothing of its own; every other node is named.
	each := []string{}
	ran := map[string]bool{}
	for _, entry := range steps {
		ran[textOf(entry["step"])] = true
		if entry["composite"] != nil && entry["outcome"] == receipts.OutcomePassed {
			continue
		}
		text := fmt.Sprintf("%v=%v", entry["step"], entry["outcome"])
		if spent, ok := entry["seconds"].(float64); ok && spent != 0 {
			text += " " + formatSeconds(spent) + " s"
		}
		each = append(each, text)
	}
	detail := label
	if why != "" {
		detail += fmt.Sprintf(" %s, exit %d", why, state.status)
	}
	detail += ", " + formatSeconds(seconds) + " s"
	if split == nil {
		detail += ": " + joinComma(each)
	} else {
		var leftToCI []string
		for _, name := range split.ci {
			if !ran[name] {
				leftToCI = append(leftToCI, name)
			}
		}
		detail += "; here: " + orNone(joinComma(each)) + "; ci: " + orNone(joinComma(leftToCI)) + "; elsewhere: " + orNone(split.elsewhereText())
	}
	switch state.outcome {
	case outcomeNeedsOtherSystem:
		proj.say("outcome=%s (%s; %s)", state.outcome, split.elsewhereText(), detail)
	default:
		proj.say("outcome=%s (%s)", state.outcome, detail)
	}
	return state.status
}

func orNone(text string) string {
	if text == "" {
		return "none"
	}
	return text
}

// elsewhereText is the nodes elsewhere by the system they run on: "darwin: x-ios".
func (split *partition) elsewhereText() string {
	var parts []string
	for _, system := range append(slices.Clone(operatingSystems), "") {
		var names []string
		for _, name := range split.elsewhere {
			systems := split.where[name]
			if system == "" && len(systems) == 0 || system != "" && slices.Contains(systems, system) {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			continue
		}
		if system == "" {
			system = "no system"
		}
		parts = append(parts, system+": "+joinComma(names))
	}
	return strings.Join(parts, "; ")
}
