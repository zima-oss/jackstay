package scopes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/judge"
	"github.com/zima-oss/jackstay/internal/receipts"
	"github.com/zima-oss/jackstay/internal/version"
)

// Folders whose untracked files are no change to the tree a step tests: agents' own, written all
// the time, and regenerable ones.
var (
	agentFolders       = []string{".claude", ".omc", ".omx", ".codex", ".cursor"}
	regenerableFolders = []string{".build", "DerivedData", "node_modules", "__pycache__", ".pytest_cache", ".mypy_cache", ".venv", "venv"}
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

// cleanCheckout is "" when the checkout is clean (no change, no untracked file, no skip-worktree
// or assume-unchanged entry), else why not.
func (proj *project) cleanCheckout() string {
	status, err := proj.git(proj.ctx, proj.root, "status", "--porcelain", "--untracked-files=all", "-z")
	if err != nil {
		return "git status could not be read"
	}
	if changed := zeroSeparated(status); len(changed) > 0 {
		return fmt.Sprintf("the checkout has changes (%s)", strings.TrimSpace(changed[0]))
	}
	// An untracked file hidden only by a rule outside the committed `.gitignore` files is still a
	// change to the tree.
	hidden, err := proj.git(proj.ctx, proj.root, "ls-files", "-z", "--others", "--exclude-per-directory=.gitignore")
	if err != nil {
		return "git ls-files could not be read"
	}
	for _, name := range zeroSeparated(hidden) {
		parts := strings.Split(name, "/")
		if slices.Contains(agentFolders, parts[0]) || parts[len(parts)-1] == ".DS_Store" {
			continue
		}
		regenerable := false
		for _, part := range parts[:len(parts)-1] {
			if slices.Contains(regenerableFolders, part) {
				regenerable = true
			}
		}
		if !regenerable {
			return fmt.Sprintf("an untracked file only a rule outside the committed .gitignore files hides (%s)", name)
		}
	}
	tags, err := proj.git(proj.ctx, proj.root, "ls-files", "-v", "-z")
	if err != nil {
		return "git ls-files could not be read"
	}
	for _, item := range zeroSeparated(tags) {
		if tag := item[:1]; tag != strings.ToUpper(tag) || tag == "S" {
			return fmt.Sprintf("git is told not to look at %s (assume-unchanged or skip-worktree)", item[min(2, len(item)):])
		}
	}
	return ""
}

// head is HEAD's commit, or "".
func (proj *project) head() string {
	return strings.TrimSpace(string(proj.gitText("rev-parse", "--verify", "--quiet", "HEAD^{commit}")))
}

func fileStamp(info fs.FileInfo) string {
	if status, ok := info.Sys().(*unix.Stat_t); ok {
		return fmt.Sprintf("%d:%d:%d:%d", status.Ino, status.Size, status.Mtim.Nano(), status.Ctim.Nano())
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

// checkoutStamps are every tracked file's identity, size and times, and `receipts.extra_files`
// with their content: equal before and after means nothing in the checkout was rewritten. nil
// when git cannot list the files.
func (proj *project) checkoutStamps() map[string]string {
	listed := proj.gitText("ls-files", "-z")
	if listed == nil {
		return nil
	}
	found := map[string]string{}
	for _, name := range zeroSeparated(listed) {
		info, err := os.Lstat(filepath.Join(proj.root, name))
		if err != nil {
			found[name] = "missing"
			continue
		}
		found[name] = fileStamp(info)
	}
	for _, name := range textsOf(proj.receipts["extra_files"]) {
		place := filepath.Join(proj.root, name)
		info, err := os.Lstat(place)
		data, readErr := os.ReadFile(place)
		if err != nil || readErr != nil {
			found["extra:"+name] = "missing"
			continue
		}
		digest := sha256.Sum256(data)
		found["extra:"+name] = fileStamp(info) + ":" + hex.EncodeToString(digest[:])
	}
	return found
}

// vetoesOf are the receipts entry's veto for the words, as a list.
func (proj *project) vetoesOf(argv []string) []string {
	if veto := textOf(receipts.CommandEntry(proj.receipts, argv)["veto"]); veto != "" {
		return []string{veto}
	}
	return []string{}
}

// stepKey is the key a step mark and a bar are filed under, or "" and why none can be made.
func (proj *project) stepKey(chosen leaf, argv []string, base, relative string) (string, string) {
	head := proj.head()
	if head == "" {
		return "", "HEAD is no commit"
	}
	entry := receipts.CommandEntry(proj.receipts, argv)
	wantedRefs := append(textsOf(proj.receipts["refs"]), textsOf(entry["refs"])...)
	wantedBases := append(textsOf(proj.receipts["merge_bases"]), textsOf(entry["merge_bases"])...)
	if base != "" {
		wantedRefs, wantedBases = append(wantedRefs, base), append(wantedBases, base)
	}
	refs, bases := map[string]string{}, map[string]string{}
	for _, name := range wantedRefs {
		commit := strings.TrimSpace(string(proj.gitText("rev-parse", "--verify", "--quiet", name+"^{commit}")))
		if commit == "" {
			return "", fmt.Sprintf("the ref %s cannot be resolved, so a move of it could not be seen", name)
		}
		refs[name] = commit
	}
	for _, name := range wantedBases {
		found := strings.TrimSpace(string(proj.gitText("merge-base", "HEAD", name)))
		if found == "" {
			return "", fmt.Sprintf("the merge base of HEAD and %s cannot be found", name)
		}
		bases[name] = found
	}
	extra := map[string]string{}
	for _, name := range textsOf(proj.receipts["extra_files"]) {
		data, err := os.ReadFile(filepath.Join(proj.root, name))
		if err != nil {
			extra[name] = "missing"
			continue
		}
		digest := sha256.Sum256(data)
		extra[name] = hex.EncodeToString(digest[:])
	}
	cwd := filepath.Clean(filepath.Join(proj.root, chosen.cwd))
	env := withEnv(commandEnv(proj.environ), chosen.env)
	facts, err := receipts.EnvironmentFacts(proj.ctx, proj.receipts, cwd, env)
	if err != nil {
		return "", err.Error()
	}
	requires := make([]map[string]any, 0, len(chosen.requires))
	for _, each := range chosen.requires {
		requires = append(requires, map[string]any{"run": each.run, "why": each.why, "timeout": each.timeout})
	}
	material := map[string]any{"project": proj.name, "jackstay": version.Version, "head": head,
		"definition": definition(chosen, argv, relative), "lock_wait": chosen.lockWait, "requires": requires,
		"leftovers": items(proj.section["leftovers"]), "vetoes": proj.vetoesOf(argv), "facts": facts, "refs": refs,
		"merge_bases": bases, "extra_files": extra}
	data, err := json.Marshal(material)
	if err != nil {
		return "", err.Error()
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), ""
}

func (proj *project) stateFolder(name string) string {
	folder, err := receipts.StateFolder(proj.name, name)
	if err != nil {
		return ""
	}
	return folder
}

func (proj *project) maxAge() time.Duration { return receipts.MaxAge(proj.receipts) }

// bar is a failed run's bar of a key.
type bar struct {
	Key     string  `json:"key"`
	Step    string  `json:"step"`
	Outcome string  `json:"outcome"`
	RunID   string  `json:"run_id"`
	At      string  `json:"at"`
	AtEpoch float64 `json:"at_epoch"`
	// why says what stands in for a bar that cannot be read (not stored).
	why string
}

// barOf is the newest bar of a key, or nil. A folder that cannot be listed is a bar: it cannot be
// shown that the step did not fail.
func (proj *project) barOf(key string) *bar {
	folder := proj.stateFolder("gate-bars")
	entries, err := os.ReadDir(folder)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &bar{why: fmt.Sprintf("the bars cannot be listed (%v)", err)}
	}
	var newest *bar
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), key+".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(folder, entry.Name()))
		var read bar
		if err != nil || json.Unmarshal(data, &read) != nil {
			newest = &bar{why: "a bar that cannot be read: " + entry.Name()}
			continue
		}
		newest = &read
	}
	return newest
}

func (found *bar) when() string {
	if found.At != "" {
		return found.At
	}
	return found.why
}

func (proj *project) timeText() string { return proj.clock.Now().Format("2006-01-02T15:04:05-0700") }

// writeBar files the bar of a key a step failed on; bars live as long as any mark can be used
// (receipts.max_age_hours) and a day more.
func (proj *project) writeBar(key, step string, ended result) {
	folder := proj.stateFolder("gate-bars")
	now := proj.clock.Now()
	data, err := json.Marshal(bar{Key: key, Step: step, Outcome: ended.outcome, RunID: ended.runID, At: proj.timeText(),
		AtEpoch: epochSeconds(now)})
	if err == nil {
		err = os.MkdirAll(folder, 0o700)
	}
	if err == nil {
		err = createOnce(filepath.Join(folder, fmt.Sprintf("%s.%020d-%d", key, now.UnixNano(), proj.pid)), data)
	}
	if err != nil {
		proj.say("the bar of the failed step %s could not be written (%v): a later --resume may not know it failed", step, err)
	}
	proj.prune(folder, proj.maxAge()+24*time.Hour)
}

// createOnce makes a file that must not exist yet.
func createOnce(place string, data []byte) error {
	file, err := os.OpenFile(place, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr
}

// mark is a step's pass on a key, which `--resume` may stand on.
type mark struct {
	Key            string  `json:"key"`
	Step           string  `json:"step"`
	Outcome        string  `json:"outcome"`
	RunID          string  `json:"run_id"`
	Head           string  `json:"head"`
	Seconds        float64 `json:"seconds"`
	At             string  `json:"at"`
	AtEpoch        float64 `json:"at_epoch"`
	Jackstay       string  `json:"jackstay"`
	AdapterVersion int     `json:"adapter_version,omitempty"`
	Counted        int     `json:"counted,omitempty"`
}

func (proj *project) markPath(key string) string {
	return filepath.Join(proj.stateFolder("gate-marks"), key+".json")
}

// writeMark files a step's pass; a key with a bar gets none (a mark could outlive its bar).
func (proj *project) writeMark(key, step string, ended result, head string, adapterVersion int) bool {
	if adapterVersion != 0 && (ended.outcome != receipts.OutcomePassed || ended.counted <= 0) {
		return false
	}
	if proj.barOf(key) != nil {
		proj.say("step %s: no mark (this step failed on this same tree before; its bar stands)", step)
		return false
	}
	folder := proj.stateFolder("gate-marks")
	data, err := json.Marshal(mark{Key: key, Step: step, Outcome: ended.outcome, RunID: ended.runID, Head: head,
		Seconds: ended.seconds, At: proj.timeText(), AtEpoch: epochSeconds(proj.clock.Now()), Jackstay: version.Version,
		AdapterVersion: adapterVersion, Counted: ended.counted})
	if err == nil {
		err = os.MkdirAll(folder, 0o700)
	}
	if err == nil {
		err = durable.WriteAtomic(proj.markPath(key), data, 0o600, 0)
	}
	if err != nil {
		proj.say("the mark of step %s could not be written (%v); a later --resume runs it again", step, err)
		return false
	}
	proj.prune(folder, max(proj.maxAge(), 24*time.Hour))
	return true
}

// removeMark takes a key's mark away: a run of the step on it ended with no verdict, or runs now.
func (proj *project) removeMark(key string) { _ = os.Remove(proj.markPath(key)) }

// readMark is a mark usable now for the key, or why not.
func (proj *project) readMark(key, step, head string, adapterVersion int) (*mark, string) {
	data, err := os.ReadFile(proj.markPath(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "no mark"
	}
	var found mark
	if err != nil || json.Unmarshal(data, &found) != nil {
		return nil, "the mark cannot be read"
	}
	if found.Key != key || found.Step != step || found.Head != head || found.RunID == "" || found.Jackstay != version.Version {
		return nil, "the mark is not one for this key"
	}
	if found.Outcome != receipts.OutcomePassed && found.Outcome != receipts.OutcomeExited0 {
		return nil, "the mark is not a successful step"
	}
	if found.AdapterVersion != adapterVersion || (adapterVersion != 0 && (found.Outcome != receipts.OutcomePassed || found.Counted <= 0)) {
		return nil, "the mark has no counted pass for the current adapter"
	}
	if found.AtEpoch == 0 {
		return nil, "the mark's time cannot be read"
	}
	age := epochSeconds(proj.clock.Now()) - found.AtEpoch
	if age < -300 || age > proj.maxAge().Seconds() {
		return nil, fmt.Sprintf("the mark is older than receipts.max_age_hours (%s h)", formatSeconds(proj.maxAge().Hours()))
	}
	return &found, ""
}

// prune removes a folder's files older than age.
func (proj *project) prune(folder string, age time.Duration) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return
	}
	before := proj.clock.Now().Add(-age)
	for _, entry := range entries {
		place := filepath.Join(folder, entry.Name())
		if info, err := os.Lstat(place); err == nil && info.ModTime().Before(before) {
			_ = os.Remove(place)
		}
	}
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
	receipts.OutcomeStoppedWaiting, receipts.OutcomeUnjudged, receipts.OutcomeNothingRun, receipts.OutcomeGaveUp, outcomeRefused,
	receipts.OutcomeNotStarted}

// passing says whether a node that ended so lets the nodes that depend on it run.
func passing(outcome string) bool {
	switch outcome {
	case receipts.OutcomePassed, receipts.OutcomeExited0, outcomeSkipped, outcomeResumed:
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
	order := nodes.walk(asked)
	gateID := receipts.RunID(proj.clock.Now(), proj.pid)
	began := proj.clock.Now()
	plural := "s"
	if len(order) == 1 {
		plural = ""
	}
	resuming := ""
	if proj.options.resume {
		resuming = " (resuming passed steps)"
	}
	proj.say("%s: %d node%s: %s%s", label, len(order), plural, joinComma(order), resuming)
	startFields := map[string]any{"gate_run_id": gateID, "scope": label, "started_epoch": epochText(began), "steps": nonNil(order),
		"resume": proj.options.resume}
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
			line = step{"step": name, "outcome": receipts.OutcomePassed, "seconds": 0, "composite": true}
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
		case state.outcome == receipts.OutcomeExited0:
			// The split's here passes as a composite does: every node passed or ended exited-0.
			state.outcome = receipts.OutcomePassed
		}
	}
	if problem := proj.pinnedProblem(); problem != "" && !slices.Contains(failureOrder, state.outcome) {
		proj.say("%s", problem)
		state.outcome, state.status, state.failedAt = receipts.OutcomeUnjudged, receipts.StatusUnjudged, "checkout validation"
	}
	return proj.gateLine(label, gateID, began, state, split)
}

// settle is the run's outcome from its nodes' (section 7.2): the first failure in failureOrder;
// else `skipped` when a scope asked for is not eligible here; `resumed` when every node with `run`
// was resumed; `exited-0` when a scope asked for ended so; else `passed`.
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
	resumed := 0
	ran := 0
	for _, line := range state.steps {
		if line["composite"] == nil && line["outcome"] != outcomeSkipped {
			ran++
			if line["outcome"] == outcomeResumed {
				resumed++
			}
		}
	}
	switch {
	case slices.ContainsFunc(asked, func(name string) bool { return outcomes[name] == outcomeSkipped }):
		state.outcome, state.status = outcomeSkipped, statusSkipped
	case ran > 0 && resumed == ran:
		state.outcome = outcomeResumed
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

// gateStep runs or resumes one node with `run`: its `requires`, its mark or bar under --resume,
// then the command. signalled is set when jackstay was asked to stop: the status it ends with.
func (proj *project) gateStep(name, kind string, entry map[string]any, gateID string) (step, *int) {
	resume := proj.options.resume
	chosen := resolve(proj.section, name, kind, entry)
	adapterVersion := judge.AdapterVersion(judge.Kind(chosen.judge))
	found, err := proj.wordsOf(chosen, nil)
	if err != nil {
		proj.say("%v", err)
		return step{"step": name, "outcome": receipts.OutcomeNotStarted, "seconds": 0, "status": statusRefused}, nil
	}
	// Every `requires`, before the step may be resumed: a mark says the step passed, not that what
	// it needs is there now.
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
	relative, relErr := filepath.Rel(proj.root, cwd)
	if relErr != nil {
		relative = chosen.cwd
	}
	key, head := "", ""
	var stamps map[string]string
	dirty := proj.cleanCheckout()
	switch {
	case dirty == "" && chosen.resume:
		var whyNoKey string
		key, whyNoKey = proj.stepKey(chosen, found.argv, found.base, relative)
		head, stamps = proj.head(), proj.checkoutStamps()
		if key == "" {
			proj.say("step %s: no mark can be kept (%s)", name, whyNoKey)
		}
	case resume && dirty != "":
		proj.say("step %s: runs (%s: only a clean checkout is resumed)", name, dirty)
	}
	switch {
	case resume && key != "":
		if found := proj.barOf(key); found != nil {
			proj.say("step %s: nothing has changed since this step failed (%s); change the tree or run the gate without --resume",
				name, found.when())
			return step{"step": name, "outcome": outcomeRefused, "seconds": 0, "status": statusRefused}, nil
		}
		if found, _ := proj.readMark(key, name, head, adapterVersion); found != nil {
			proj.say("step %s: resumed (it %s at %s on this same tree; run %s)", name, found.Outcome, found.At, found.RunID)
			return step{"step": name, "outcome": outcomeResumed, "seconds": 0, "mark_of": found.RunID}, nil
		}
	case resume && !chosen.resume:
		proj.say("step %s: runs (\"resume\": false)", name)
	}
	proj.say("step %s: %s", name, shellWords(found.argv))
	if key != "" {
		// The step runs again: its old pass no longer speaks for this tree, also if this run is
		// stopped before it can say anything. A pass writes a new mark.
		proj.removeMark(key)
	}
	ended := proj.runLeaf(chosen, nil, gateID, true)
	if ended.signalled {
		return nil, &ended.status
	}
	passed := ended.outcome == receipts.OutcomePassed || ended.outcome == receipts.OutcomeExited0
	switch {
	case key != "" && passed:
		proj.markAfter(name, key, head, stamps, chosen, found, relative, ended)
	case key != "" && ended.outcome == receipts.OutcomeFailed:
		proj.writeBar(key, name, ended)
	case key != "":
		proj.removeMark(key)
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

// markAfter writes a passed step's mark when the tree is the same as when it started: clean, the
// same key and HEAD, and no file of the checkout rewritten.
func (proj *project) markAfter(name, key, head string, stamps map[string]string, chosen leaf, found words, relative string, ended result) {
	if dirty := proj.cleanCheckout(); dirty != "" {
		proj.say("step %s: no mark (%s after it ran)", name, dirty)
		return
	}
	after, _ := proj.stepKey(chosen, found.argv, found.base, relative)
	if after != key || proj.head() != head {
		proj.say("step %s: no mark (its key changed while it ran: HEAD, a ref, the toolchain or the environment)", name)
		return
	}
	now := proj.checkoutStamps()
	var changed []string
	for _, file := range sortedKeys(mergeKeys(now, stamps)) {
		if now[file] != stamps[file] {
			changed = append(changed, file)
		}
	}
	if stamps == nil || now == nil || len(changed) > 0 {
		proj.say("step %s: no mark (a file of the checkout was rewritten while it ran: %s)", name,
			joinComma(changed[:min(3, len(changed))]))
		return
	}
	proj.writeMark(key, name, ended, head, judge.AdapterVersion(judge.Kind(chosen.judge)))
}

func mergeKeys(first, second map[string]string) map[string]bool {
	merged := map[string]bool{}
	for name := range first {
		merged[name] = true
	}
	for name := range second {
		merged[name] = true
	}
	return merged
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
	case outcomeResumed:
		proj.say("outcome=resumed (every step was resumed: nothing ran, and this is not counted as a pass; %s)", joinComma(each))
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
