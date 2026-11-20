package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/homedir"
)

// Usage lines.
const (
	TestUsage     = "usage: jst test [--no-reuse] [--lock | --no-lock] [--project NAME] [--wait SECONDS] -- COMMAND [ARG ...]"
	lastUsage     = "usage: jst test last [--project NAME] [--scope S] [--json]"
	receiptsUsage = `usage: jst receipts key -- COMMAND [ARG ...]   the key and what went into it; nothing is run
       jst receipts stats [--project NAME] [--json]   runs, reuses and seconds saved, from the log
       jst receipts list [--project NAME]
       jst receipts export KEY
       jst receipts verify --sha256 DIGEST --head SHA --key KEY FILE
       jst receipts attest-ci --trust-root NAME --issuer ID --workflow ID --run ID --signing-key KEY RECEIPT
       jst receipts verify-ci --trust-root NAME --public-key KEY --issuer ID --workflow ID --run ID --head SHA --key KEY --manifest SHA RECEIPT ATTESTATION`
)

func init() {
	cli.Register(
		cli.Command{Name: "test", Summary: "run a test command, judged, recorded and keyed by the tree (`jst test -- COMMAND`)", Usage: testHelp, Run: TestCommand},
		cli.Command{Name: "test last", Summary: "the last run of this project: outcome, what went wrong, where its output is", Usage: lastHelp, Run: LastCommand},
		cli.Command{Name: "receipts", Summary: "the receipts: key, stats, list, export, verify and CI provenance", Usage: receiptsHelp, Run: receiptsCommand},
		cli.Command{Name: "receipts key", Summary: "the key of a command here and what went into it; nothing is run", Usage: keyHelp, Run: keyCommand},
		cli.Command{Name: "receipts stats", Summary: "runs, reuses and seconds saved, from the run log", Usage: statsHelp, Run: statsCommand},
		cli.Command{Name: "receipts list", Summary: "the stored receipts", Usage: listHelp, Run: listCommand},
	)
}

// IsRaw says whether the words after `jst test` are the raw form (options, then `--`), not a scope.
func IsRaw(args []string) bool {
	for index := 0; index < len(args); {
		switch args[index] {
		case "--":
			return true
		case "--no-reuse", "--no-lock", "--lock":
			index++
		case "--project", "--wait":
			index += 2
		default:
			return false
		}
	}
	return false
}

// parseRaw reads `[OPTIONS] -- COMMAND`; false when the words cannot be used.
func parseRaw(args []string) (request, bool) {
	var asked request
	for len(args) > 0 && args[0] != "--" {
		switch {
		case args[0] == "--no-reuse":
			asked.noReuse, args = true, args[1:]
		case args[0] == "--no-lock" || args[0] == "--lock":
			chosen := args[0] == "--lock"
			asked.lock, args = &chosen, args[1:]
		case args[0] == "--project" && len(args) > 1:
			asked.project, args = args[1], args[2:]
		case args[0] == "--wait" && len(args) > 1 && seconds.MatchString(args[1]):
			number, err := strconv.ParseFloat(args[1], 64)
			if err != nil {
				return asked, false
			}
			asked.wait, asked.bounded, args = time.Duration(number*float64(time.Second)), true, args[2:]
		default:
			return asked, false
		}
	}
	if len(args) < 2 {
		return asked, false
	}
	asked.argv = args[1:]
	return asked, true
}

// scopeCommand is `jst test <scope> …`, handed in by internal/testing (RegisterScopes); nil while
// no scope command is linked in.
var scopeCommand func(args []string, stdin io.Reader, stdout, stderr io.Writer) int

// RegisterScopes hands `jst test <scope> …` (every form of `jst test` that is not the raw one) to
// internal/testing, which imports this package: a hook, not an import back.
func RegisterScopes(run func(args []string, stdin io.Reader, stdout, stderr io.Writer) int) {
	scopeCommand = run
}

// TestCommand is `jst test [OPTIONS] -- COMMAND`, or a scope's run when the words are not the raw
// form and a scope command is registered. Asking how to use it is not a run: no outcome line,
// nothing in the log.
func TestCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(stdout, TestUsage)
		return 0
	}
	if path := UnwritableEvidence(""); path != "" {
		Say(stderr, "%s", EvidenceRefusal(path))
		Say(stderr, "outcome=not-started (nothing was run)")
		return StatusUnusable
	}
	if scopeCommand != nil && !IsRaw(args) {
		return scopeCommand(args, stdin, stdout, stderr)
	}
	asked, ok := parseRaw(args)
	if !ok {
		fmt.Fprintln(stderr, TestUsage)
		return StatusUnusable
	}
	runner, err := newTester(stdout, stderr)
	if err != nil {
		Say(stderr, "the working folder cannot be read: %v", err)
		Say(stderr, "outcome=not-started (jackstay.json cannot be used; nothing was run)")
		return StatusUnusable
	}
	return runner.raw(asked)
}

// UnwritableEvidence is the first piece of a project's evidence this process cannot write, "" when
// it can write them all: the run log (opened for appending, nothing written), and the project's
// receipts, gate marks and bars (a file made and removed in each, or where it would be made).
// Claude Code's sandbox refuses them once setup has left evidence read-only there (#59), and so may
// an owner's deny rule; a run that cannot write them could neither be found again nor, failing, undo
// an earlier pass, so every jst test that runs something checks them first. project "" checks the
// run log alone.
func UnwritableEvidence(project string) string {
	path, err := RunLogPath()
	if err != nil {
		return "the run log"
	}
	if _, statErr := os.Stat(path); statErr == nil {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0) // #nosec G304 -- jackstay's own run log
		if err != nil {
			return path
		}
		if err := file.Close(); err != nil {
			return path
		}
	} else if !canCreateIn(filepath.Dir(path)) {
		return path
	}
	if project == "" {
		return ""
	}
	for _, name := range []string{"receipts", "gate-marks", "gate-bars"} {
		folder, err := StateFolder(project, name)
		if err != nil || !canCreateIn(folder) {
			return folder
		}
	}
	return ""
}

// canCreateIn says a file can be made and removed in folder or, while it does not exist, in the
// nearest folder above it that does (where it would be made): nothing is left behind.
func canCreateIn(folder string) bool {
	for {
		info, err := os.Stat(folder)
		if err == nil {
			if !info.IsDir() {
				return false
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false
		}
		parent := filepath.Dir(folder)
		if parent == folder {
			return false
		}
		folder = parent
	}
	probe, err := os.CreateTemp(folder, ".jackstay-write-probe-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	closeErr := probe.Close()
	return os.Remove(name) == nil && closeErr == nil
}

// EvidenceRefusal is what a run that cannot write its evidence says.
func EvidenceRefusal(path string) string {
	return fmt.Sprintf("jst test records evidence (the run log, receipts, gate marks) and cannot write %s here: inside Claude "+
		"Code's sandbox it is read-only, so a run could neither be found again nor undo an earlier pass. Run it outside the sandbox.", path)
}

func receiptsCommand(_ []string, _ io.Reader, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, receiptsUsage)
	return StatusUnusable
}

// projectFlag reads `[--project NAME] [--json]`; false on any other word.
func projectFlag(args []string, allowJSON bool) (project string, asJSON, ok bool) {
	for len(args) > 0 {
		switch {
		case args[0] == "--project" && len(args) > 1:
			project, args = args[1], args[2:]
		case args[0] == "--json" && allowJSON:
			asJSON, args = true, args[1:]
		default:
			return "", false, false
		}
	}
	return project, asJSON, true
}

// keyCommand is `jst receipts key -- COMMAND`: the key and what went into it; nothing is run.
func keyCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "--" {
		fmt.Fprintln(stderr, "usage: jst receipts key -- COMMAND [ARG ...]")
		return StatusUnusable
	}
	runner, err := newTester(stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "jst receipts: the working folder cannot be read: %v\n", err)
		return StatusUnusable
	}
	return runner.showKey(args[1:])
}

func (base *tester) showKey(argv []string) int {
	root, loaded, err := config.Load(base.here, Needs)
	if err != nil {
		fmt.Fprintf(base.stderr, "jst receipts: jackstay.json cannot be used: %v\n", err)
		return StatusUnusable
	}
	run := &session{tester: base, asked: request{argv: argv}, ctx: context.Background(), root: root, loaded: loaded,
		section: Section(loaded)}
	run.project = config.ProjectName(root, loaded, base.here)
	run.entry = CommandEntry(run.section, argv)
	if _, err := NewEvidence(run.entry); err != nil {
		fmt.Fprintf(base.stderr, "jst receipts: %v\n", err)
		return StatusUnusable
	}
	key, err := run.key()
	if err != nil {
		fmt.Fprintf(base.stderr, "jst receipts: no key: %v\n", err)
		return 1
	}
	var receiptPath any
	why := "the receipt store cannot be found"
	if store, storeErr := OpenStore(run.project, run.section, base.clock); storeErr == nil {
		var receipt *Receipt
		if receipt, why = store.ReadCurrent(key); receipt != nil {
			receiptPath = receipt.Path
		}
	}
	refusal := run.refusal()
	report := map[string]any{"key": key.Value, "material": key.Material, "receipt": receiptPath, "no_receipt_because": nullable(why),
		"reuse_allowed": refusal == "", "reuse_refused_because": nullable(refusal)}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(base.stderr, "jst receipts: %v\n", err)
		return 1
	}
	fmt.Fprintln(base.stdout, string(data))
	return 0
}

// Tally is one project's counts in `jst receipts stats`.
type Tally struct {
	Runs             int     `json:"runs"`
	Passed           int     `json:"passed"`
	Exited0          int     `json:"exited_0"`
	Failed           int     `json:"failed"`
	NothingRun       int     `json:"nothing_run"`
	Unjudged         int     `json:"unjudged"`
	PassedOnRetry    int     `json:"passed_on_retry"`
	Stopped          int     `json:"stopped"`
	GaveUp           int     `json:"gave_up"`
	StoppedWaiting   int     `json:"stopped_waiting"`
	Unfinished       int     `json:"unfinished"`
	RunSeconds       float64 `json:"run_seconds"`
	Reused           int     `json:"reused"`
	SavedSeconds     float64 `json:"saved_seconds"`
	WouldHit         int     `json:"would_hit"`
	WouldSaveSeconds float64 `json:"would_save_seconds"`
	Forced           int     `json:"forced"`
	ReceiptsStored   int     `json:"receipts_stored"`
}

// Summarise counts per project: runs by outcome, reuses, would-hits and the seconds each stands
// for; lines `inside` another run are part of it and skipped. `passed` is counted runs only;
// `exited_0` has its own count; a would-hit a person forced is counted apart.
func Summarise(records []Record) map[string]*Tally {
	summary := map[string]*Tally{}
	begun, ended := map[string]map[string]bool{}, map[string]map[string]bool{}
	for _, record := range records {
		if record.Inside() {
			continue
		}
		project := record.Text("project")
		if project == "" {
			project = "unknown"
		}
		tally := summary[project]
		if tally == nil {
			tally = &Tally{}
			summary[project] = tally
		}
		seconds := func(name string) float64 { value, _ := record.Number(name); return value }
		switch record.Text("event") {
		case EventStart:
			addTo(begun, project, record.Text("run_id"))
		case EventRun:
			addTo(ended, project, record.Text("run_id"))
			countRun(tally, record, seconds("seconds"))
		case EventReused:
			tally.Reused++
			tally.SavedSeconds += seconds("saved_seconds")
		case EventWouldHit:
			if Forced(record) {
				tally.Forced++ // a person asked for the run: not a saving reuse would have made
				continue
			}
			tally.WouldHit++
			tally.WouldSaveSeconds += seconds("saved_seconds")
		}
	}
	for project, ids := range begun {
		for id := range ids {
			if !ended[project][id] {
				summary[project].Unfinished++ // started, and no line says how it ended
			}
		}
	}
	return summary
}

func addTo(sets map[string]map[string]bool, project, id string) {
	if sets[project] == nil {
		sets[project] = map[string]bool{}
	}
	sets[project][id] = true
}

func countRun(tally *Tally, record Record, seconds float64) {
	switch record.Text("outcome") {
	case OutcomeGaveUp:
		tally.GaveUp++ // nothing was started: not a run
		return
	case OutcomeStoppedWaiting:
		tally.StoppedWaiting++
		return
	case OutcomeNotStarted:
		return
	case OutcomePassed:
		tally.Passed++
	case OutcomeExited0:
		tally.Exited0++
	case OutcomeFailed:
		tally.Failed++
	case OutcomeNothingRun:
		tally.NothingRun++
	case OutcomeUnjudged:
		tally.Unjudged++
	case OutcomePassedOnRetry:
		tally.PassedOnRetry++
	case OutcomeStopped:
		tally.Stopped++
	}
	tally.Runs++
	tally.RunSeconds += seconds
	if stored, _ := record["stored"].(bool); stored {
		tally.ReceiptsStored++
	}
}

func hours(seconds float64) float64 { return math.Round(seconds/3600*100) / 100 }

// statsCommand is `jst receipts stats [--project NAME] [--json]`.
func statsCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	project, asJSON, ok := projectFlag(args, true)
	if !ok {
		fmt.Fprintln(stderr, "usage: jst receipts stats [--project NAME] [--json]")
		return StatusUnusable
	}
	var chosen []Record
	for _, record := range ReadRecords(RunLogPaths()) {
		if project == "" || record.Text("project") == project {
			chosen = append(chosen, record)
		}
	}
	summary := Summarise(chosen)
	if asJSON {
		data, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "jst receipts: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	if len(summary) == 0 {
		fmt.Fprintln(stdout, "jst receipts: the log is empty; no run went through `jst test` yet.")
		return 0
	}
	for _, name := range sortedKeys(summary) {
		tally := summary[name]
		fmt.Fprintf(stdout, "%s: %d runs (%d passed, %d passed only on retry, %d failed, %d ran nothing, %d ended 0 uncounted, "+
			"%d unjudged, %d stopped), %g h; %d runs stored a receipt; %d started and never ended (killed, or running now)\n",
			name, tally.Runs, tally.Passed, tally.PassedOnRetry, tally.Failed, tally.NothingRun, tally.Exited0, tally.Unjudged, tally.Stopped,
			hours(tally.RunSeconds), tally.ReceiptsStored, tally.Unfinished)
		fmt.Fprintf(stdout, "  reused %d times, saving %g h; would have hit %d more (%g h) where reuse is off; "+
			"%d runs forced over a receipt; gave up waiting %d, stopped while waiting %d\n",
			tally.Reused, hours(tally.SavedSeconds), tally.WouldHit, hours(tally.WouldSaveSeconds), tally.Forced,
			tally.GaveUp, tally.StoppedWaiting)
	}
	return 0
}

// listCommand is `jst receipts list [--project NAME]`: every stored receipt.
func listCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	project, _, ok := projectFlag(args, false)
	if !ok {
		fmt.Fprintln(stderr, "usage: jst receipts list [--project NAME]")
		return StatusUnusable
	}
	home, err := homedir.Home()
	if err != nil {
		fmt.Fprintf(stderr, "jst receipts: %v\n", err)
		return StatusUnusable
	}
	var folders []string
	if project != "" {
		folders = []string{homedir.MarkerName(project)}
	} else if entries, err := os.ReadDir(home); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && entry.Name() != homedir.Shared {
				folders = append(folders, entry.Name())
			}
		}
		sort.Strings(folders)
	}
	for _, marker := range folders {
		store := filepath.Join(home, marker, "receipts")
		entries, err := os.ReadDir(store)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(store, name))
			var receipt Receipt
			if err != nil || json.Unmarshal(data, &receipt) != nil {
				continue
			}
			fmt.Fprintf(stdout, "%s  %s  %g s  %s  %s  %s\n", marker, receipt.Ended, receipt.Seconds,
				strings.Join(receipt.Command, " "), cut(receipt.Head, 12), cut(name, 16))
		}
	}
	return 0
}

// LastCommand is `jst test last [--project NAME] [--scope S] [--json]`: the last run of a project as
// `jst test` said it, running nothing.
func LastCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	var project, scope string
	asJSON := false
	for len(args) > 0 {
		switch {
		case args[0] == "--project" && len(args) > 1:
			project, args = args[1], args[2:]
		case args[0] == "--scope" && len(args) > 1:
			scope, args = args[1], args[2:]
		case args[0] == "--json":
			asJSON, args = true, args[1:]
		default:
			fmt.Fprintln(stderr, lastUsage)
			return StatusUnusable
		}
	}
	here, _ := os.Getwd()
	root, loaded, loadErr := config.Load(here, []string{"receipts"})
	if project == "" {
		if loadErr != nil {
			fmt.Fprintf(stderr, "jst test last: %v\n", loadErr)
			return StatusUnusable
		}
		project = config.ProjectName(root, loaded, here)
	}
	shower := lastShower{stdout: stdout, stderr: stderr, project: project, scope: scope, asJSON: asJSON}
	if loadErr == nil && config.ProjectName(root, loaded, here) == project {
		// This checkout's veto, only for its own project: another project's run gets the general lines.
		shower.section = Section(loaded)
	}
	return shower.show(ReadRecords(RunLogPaths()))
}

// lastShower says the last run again.
type lastShower struct {
	stdout, stderr io.Writer
	project, scope string
	asJSON         bool
	// section is this checkout's receipts section when it is the project asked about.
	section map[string]any
}

func (shower lastShower) show(records []Record) int {
	var runs []Record
	for _, record := range records {
		event := record.Text("event")
		if (event != EventRun && event != EventGate) || record.Text("project") != shower.project || record.Inside() {
			continue
		}
		if outcome := record.Text("outcome"); outcome == OutcomeGaveUp || outcome == OutcomeStoppedWaiting {
			continue
		}
		if shower.scope != "" && record.Text("scope") != shower.scope || shower.scope == "" && event != EventRun {
			continue
		}
		runs = append(runs, record)
	}
	if len(runs) == 0 {
		where := ""
		if shower.scope != "" {
			where = " in the scope " + shower.scope
		}
		logPath, _ := RunLogPath()
		fmt.Fprintf(shower.stderr, "jst test last: no run of %s%s is in the record (%s)\n", shower.project, where, logPath)
		return 1
	}
	// The latest by its end, not the last line: the old folder's lines are read first.
	latest := runs[0]
	for _, record := range runs[1:] {
		if endOf(record) >= endOf(latest) {
			latest = record
		}
	}
	if latest.Text("event") == EventGate {
		return shower.gate(latest)
	}
	return shower.run(latest)
}

// whenOf is when a line was written, "?" when it does not say.
func whenOf(record Record) string {
	if written := record.Text("at"); written != "" {
		return written
	}
	return "?"
}

func endOf(record Record) float64 {
	if ended, ok := record.Number("ended_epoch"); ok && ended != 0 {
		return ended
	}
	epoch, _ := record.Number("epoch")
	return epoch
}

func (shower lastShower) writeJSON(value any) int {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintf(shower.stderr, "jst test last: %v\n", err)
		return 1
	}
	fmt.Fprintln(shower.stdout, string(data))
	return 0
}

func (shower lastShower) gate(record Record) int {
	if shower.asJSON {
		return shower.writeJSON(map[string]any{"run": record})
	}
	fmt.Fprintf(shower.stdout, "%s: %s  gate %s\n", shower.project, whenOf(record), record.Text("scope"))
	steps, _ := record["steps"].([]any)
	for _, raw := range steps {
		step := Record{}
		if members, ok := raw.(map[string]any); ok {
			step = members
		}
		line := fmt.Sprintf("  %v: %v", step["step"], step["outcome"])
		if spent, ok := step.Number("seconds"); ok && spent != 0 {
			line += fmt.Sprintf(" (%g s)", spent)
		}
		if id := step.Text("run_id"); id != "" {
			line += "  run " + id
		}
		fmt.Fprintln(shower.stdout, line)
	}
	tail := ""
	if why := record.Text("why"); why != "" {
		tail = ", " + why
	}
	fmt.Fprintf(shower.stdout, "outcome=%s (%s s%s)\n", record.Text("outcome"), valueText(record["seconds"]), tail)
	return 0
}

func (shower lastShower) run(record Record) int {
	output := record.Text("output")
	there := false
	if output != "" {
		if _, err := os.Stat(output); err == nil {
			there = true
		}
	}
	var lines []string
	more := 0
	if there && record.Text("outcome") != OutcomePassed {
		var veto *regexp.Regexp
		if shower.section != nil {
			if text, _ := CommandEntry(shower.section, texts(record["command"]))["veto"].(string); text != "" {
				veto, _ = regexp.Compile(text)
			}
		}
		lines, more = NotableLines(output, veto)
	}
	if shower.asJSON {
		var kept any
		if there {
			kept = output
		}
		if lines == nil {
			lines = []string{}
		}
		return shower.writeJSON(map[string]any{"run": record, "notable": lines, "more": more, "output": kept})
	}
	var quoted []string
	for _, word := range texts(record["command"]) {
		quoted = append(quoted, shellQuote(word))
	}
	out := shower.stdout
	fmt.Fprintf(out, "%s: %s  %s\n", shower.project, whenOf(record), strings.Join(quoted, " "))
	if veto := record.Text("veto_line"); veto != "" {
		fmt.Fprintf(out, "veto line: %s\n", veto)
	}
	if why := record.Text("why"); why != "" {
		fmt.Fprintf(out, "why it could not be judged: %s\n", why)
	}
	WriteNotable(out, "", lines, more)
	if there {
		fmt.Fprintf(out, "the whole output: %s\n", output)
	} else {
		fmt.Fprintf(out, "the output was not kept (a run from before jackstay kept outputs, or one of the older ones beyond %d)\n", KeepOutputs)
	}
	if record.Text("scope") != "" || record.Text("step") != "" {
		var parts []string
		for _, name := range []string{"scope", "step", "gate_run_id", "judge", "counted", "bundles", "swift_testing",
			"skipped", "timed_out", "survivors", "left_running"} {
			if value, present := record[name]; present && value != nil {
				parts = append(parts, name+"="+valueText(value))
			}
		}
		fmt.Fprintf(out, "scope: %s\n", strings.Join(parts, " "))
	}
	if leftovers, present := record["leftovers"]; present && leftovers != nil && leftovers != false {
		data, _ := json.Marshal(leftovers)
		fmt.Fprintf(out, "leftovers: %s\n", data)
	}
	tail := ""
	if count, present := record["evidence_count"]; present && count != nil {
		tail += ", tests counted: " + valueText(count)
	}
	if status, present := record["status"]; present && status != nil {
		tail += ", exit " + valueText(status)
	}
	fmt.Fprintf(out, "outcome=%s (%s s%s)\n", record.Text("outcome"), valueText(record["seconds"]), tail)
	return 0
}

// valueText is a log value as people read it.
func valueText(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "True"
		}
		return "False"
	}
	data, _ := json.Marshal(value)
	return string(data)
}

// plainWord is a word a shell reads back as it is.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./_-]+$`)

// shellQuote is a word as a shell reads it back.
func shellQuote(word string) string {
	if plainWord.MatchString(word) {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'"'"'`) + "'"
}
