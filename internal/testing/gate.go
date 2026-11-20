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
		place := proj.markPath(key)
		partial := fmt.Sprintf("%s.%d.partial", place, proj.pid)
		if err = os.WriteFile(partial, data, 0o600); err == nil {
			err = os.Rename(partial, place)
		}
		if err != nil {
			_ = os.Remove(partial)
		}
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

// step is one step's line in the gate's record.
type step map[string]any

// gateState is a gate's run so far.
type gateState struct {
	steps    []step
	outcome  string
	status   int
	failedAt string
}

// fail keeps the first step that was not a pass.
func (state *gateState) fail(outcome string, status int, stepName string) {
	if state.outcome == "" {
		state.outcome, state.status, state.failedAt = outcome, status, stepName
	}
}

// gate runs a scope that lists steps: leaf by leaf, in order, fail-fast; marks and bars for resume.
func (proj *project) gate(name string) int {
	paths, err := leafPaths(proj.section, name, nil)
	if err != nil {
		proj.notStarted(err.Error())
		return statusRefused
	}
	resume := proj.options.resume
	gateID := receipts.RunID(proj.clock.Now(), proj.pid)
	began := proj.clock.Now()
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, path.name)
	}
	plural := "s"
	if len(paths) == 1 {
		plural = ""
	}
	resuming := ""
	if resume {
		resuming = " (resuming passed steps)"
	}
	proj.say("gate %s: %d step%s: %s%s", name, len(paths), plural, joinComma(names), resuming)
	startFields := map[string]any{"gate_run_id": gateID, "scope": name, "started_epoch": epochText(began), "steps": names,
		"resume": resume}
	// A gate stopped or killed while a step runs ends with that step (its own line says `stopped`);
	// this line, with no `gate` line of the same id, says the gate did not end.
	if !proj.log("gate-start", []string{name}, "", startFields) {
		proj.notLogged("the start of this gate")
	}
	state := &gateState{}
	if problem := proj.pinnedProblem(); problem != "" {
		proj.say("%s", problem)
		state.fail(receipts.OutcomeUnjudged, receipts.StatusUnjudged, "checkout validation")
		return proj.gateLine(name, gateID, began, state)
	}
	checked := map[string]string{}
	for _, path := range paths {
		ended, stop := proj.gateStep(path, gateID, resume, checked, state)
		if ended != nil {
			return *ended
		}
		if stop {
			break
		}
	}
	if problem := proj.pinnedProblem(); problem != "" {
		proj.say("%s", problem)
		state.fail(receipts.OutcomeUnjudged, receipts.StatusUnjudged, "checkout validation")
	}
	if state.outcome == "" {
		state.outcome = outcomeResumed
		for _, each := range state.steps {
			if each["outcome"] != outcomeResumed {
				state.outcome = receipts.OutcomePassed
			}
		}
	}
	return proj.gateLine(name, gateID, began, state)
}

// gateStep runs or resumes one leaf of a gate. ended is set when the gate ends at once (jackstay was
// asked to stop); stop when the gate goes no further.
func (proj *project) gateStep(path leafPath, gateID string, resume bool, checked map[string]string, state *gateState) (*int, bool) {
	keepGoing := proj.options.keepGoing
	chosen := resolve(proj.section, path.name, path.kind, path.entry)
	adapterVersion := judge.AdapterVersion(judge.Kind(chosen.judge))
	found, err := proj.wordsOf(chosen, nil)
	if err != nil {
		state.steps = append(state.steps, step{"step": path.name, "outcome": receipts.OutcomeNotStarted, "seconds": 0})
		state.fail(receipts.OutcomeNotStarted, statusRefused, path.name)
		proj.say("%v", err)
		return nil, !keepGoing
	}
	// Every `requires` that applies, before the step may be resumed: a mark says the step passed,
	// not that what it needs is there now. The lists' first.
	whyNot := ""
	for _, composite := range path.trail {
		if _, seen := checked[composite]; !seen {
			why, signalled := proj.compositeRequires(composite)
			if signalled != nil {
				return signalled, true
			}
			checked[composite] = why
		}
		if whyNot == "" {
			whyNot = checked[composite]
		}
	}
	cwd := filepath.Clean(filepath.Join(proj.root, chosen.cwd))
	if whyNot == "" {
		for _, each := range chosen.requires {
			result := proj.runCheck(each, cwd, withEnv(commandEnv(proj.environ), chosen.env))
			if result.asked != 0 {
				status := proj.ended(result.stoppedResult(), nil).status
				return &status, true
			}
			if !result.passed() {
				whyNot = path.name + ": " + checkWhy(each, result)
				break
			}
		}
	}
	if problem := proj.pinnedProblem(); problem != "" {
		whyNot = problem
	}
	if whyNot != "" {
		proj.say("step %s: not started (%s)", path.name, whyNot)
		state.steps = append(state.steps, step{"step": path.name, "outcome": receipts.OutcomeNotStarted, "seconds": 0, "why": whyNot})
		state.fail(receipts.OutcomeNotStarted, statusRefused, path.name)
		return nil, !keepGoing
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
			proj.say("step %s: no mark can be kept (%s)", path.name, whyNoKey)
		}
	case resume && dirty != "":
		proj.say("step %s: runs (%s: only a clean checkout is resumed)", path.name, dirty)
	}
	switch {
	case resume && key != "":
		if found := proj.barOf(key); found != nil {
			proj.say("step %s: nothing has changed since this step failed (%s); change the tree or run the gate without --resume",
				path.name, found.when())
			state.steps = append(state.steps, step{"step": path.name, "outcome": outcomeRefused, "seconds": 0})
			state.outcome, state.status, state.failedAt = outcomeRefused, statusRefused, path.name
			return nil, true
		}
		if found, _ := proj.readMark(key, path.name, head, adapterVersion); found != nil {
			proj.say("step %s: resumed (it %s at %s on this same tree; run %s)", path.name, found.Outcome, found.At, found.RunID)
			state.steps = append(state.steps, step{"step": path.name, "outcome": outcomeResumed, "seconds": 0, "mark_of": found.RunID})
			return nil, false
		}
	case resume && !chosen.resume:
		proj.say("step %s: runs (\"resume\": false)", path.name)
	}
	proj.say("step %s: %s", path.name, shellWords(found.argv))
	if key != "" {
		// The step runs again: its old pass no longer speaks for this tree, also if this run is
		// stopped before it can say anything. A pass writes a new mark.
		proj.removeMark(key)
	}
	ended := proj.runLeaf(chosen, nil, gateID, true)
	if ended.signalled {
		return &ended.status, true
	}
	state.steps = append(state.steps, step{"step": path.name, "outcome": ended.outcome, "seconds": ended.seconds,
		"run_id": nullableText(ended.runID), "status": ended.status})
	passed := ended.outcome == receipts.OutcomePassed || ended.outcome == receipts.OutcomeExited0
	switch {
	case key != "" && passed:
		proj.markAfter(path.name, key, head, stamps, chosen, found, relative, ended)
	case key != "" && ended.outcome == receipts.OutcomeFailed:
		proj.writeBar(key, path.name, ended)
	case key != "":
		proj.removeMark(key)
	}
	if !passed {
		status := ended.status
		if status == 0 {
			status = 1
		}
		state.fail(ended.outcome, status, path.name)
		return nil, !keepGoing
	}
	return nil, false
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
	for _, each := range checksOf(proj.section, entry) {
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

// gateLine writes the gate's line (each step's outcome, seconds and run id) and says its outcome.
func (proj *project) gateLine(name, gateID string, began time.Time, state *gateState) int {
	endedAt := proj.clock.Now()
	seconds := secondsBetween(began, endedAt)
	why := ""
	if state.failedAt != "" {
		why = "failed at " + state.failedAt
	}
	steps := state.steps
	if steps == nil {
		steps = []step{}
	}
	fields := map[string]any{"gate_run_id": gateID, "scope": name, "outcome": state.outcome, "status": state.status,
		"seconds": seconds, "steps": steps, "started_epoch": epochText(began), "ended_epoch": epochText(endedAt)}
	if why != "" {
		fields["why"] = why
	}
	pinnedPass := proj.pin != nil && state.outcome == receipts.OutcomePassed
	if pinnedPass {
		fields["pinned_candidate"] = proj.pin.commit
	}
	if !proj.log(receipts.EventGate, []string{name}, "", fields) {
		proj.notLogged("this gate")
		if pinnedPass {
			state.outcome, state.status = receipts.OutcomeUnjudged, receipts.StatusUnjudged
			why = "could not record the pinned gate"
		}
	} else if pinnedPass {
		proj.pin.runID = gateID
	}
	each := make([]string, 0, len(steps))
	for _, entry := range steps {
		text := fmt.Sprintf("%v=%v", entry["step"], entry["outcome"])
		if spent, ok := entry["seconds"].(float64); ok && spent != 0 {
			text += " " + formatSeconds(spent) + " s"
		}
		each = append(each, text)
	}
	switch state.outcome {
	case outcomeResumed:
		proj.say("outcome=resumed (every step was resumed: nothing ran, and this is not counted as a pass; %s)", joinComma(each))
	case receipts.OutcomePassed:
		proj.say("outcome=passed (gate %s, %s s: %s)", name, formatSeconds(seconds), joinComma(each))
	default:
		proj.say("outcome=%s (gate %s %s, exit %d, %s s: %s)", state.outcome, name, why, state.status, formatSeconds(seconds),
			joinComma(each))
	}
	return state.status
}
