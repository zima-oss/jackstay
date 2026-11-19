// Package doctor is `jst doctor` (docs/design/implementation.md section 7.7): is jackstay alive on this machine — the
// layout install makes, the installed guard refusing, the guard's logs of the last 7 days, the
// projects it has been heard from (and those expected to be), Claude Code's sandbox settings, the
// lock's folder. Each check is a function of Inputs, so tests give a fixed clock and fixtures.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/adopt"
	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/expected"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/install"
	"github.com/zima-oss/jackstay/internal/layout"
	"github.com/zima-oss/jackstay/internal/prove"
	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/telemetry/query"
)

func init() {
	cli.Register(cli.Command{Name: "doctor", Summary: "is jackstay alive on this machine: the layout, the guard, its calls, the sandbox", Usage: help, Run: Run})
	cli.Register(cli.Command{Name: "setup", Summary: "plan and apply selected machine setup repairs", Usage: setupHelp, Run: RunSetup})
}

const usage = "usage: jst doctor [--json] [--prefix DIR] | jst doctor --expect NAME | jst doctor --forget NAME"

// The windows of the report.
const (
	logWindow   = 7 * 24 * time.Hour
	quietWindow = 3 * 24 * time.Hour
)

// Finding is one problem or note: a fixed kind, a sentence, and for a problem, what fixes it (W-078):
// the command to run, or what to change and where.
type Finding struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Fix  string `json:"fix,omitempty"`
}

// Report is what doctor found.
type Report struct {
	Facts    map[string]any `json:"facts"`
	Problems []Finding      `json:"problems"`
	Notes    []Finding      `json:"notes"`
}

// problem adds a problem: what is wrong, and its fix. Every call names a fix
// (TestEveryProblemNamesItsFix).
func (report *Report) problem(kind, text, fix string) {
	report.Problems = append(report.Problems, Finding{Kind: kind, Text: text, Fix: fix})
}

func (report *Report) note(kind, format string, values ...any) {
	report.Notes = append(report.Notes, Finding{Kind: kind, Text: fmt.Sprintf(format, values...)})
}

// Inputs are everything the checks read.
type Inputs struct {
	Prefix string
	// CheckPath distinguishes real machine checks from test fixtures that omit PATH.
	CheckPath bool
	// ActiveJst is the first jst resolved through PATH, or empty when none is found.
	ActiveJst string
	Now       time.Time
	// Version is the running binary's identity.
	Version string
	// Home is jackstay's home.
	Home string
	// Settings is Claude Code's settings file of the account.
	Settings string
	// GuardSpans are the guard's command spans of the window, every project's, from the journal.
	GuardSpans []journal.Span
	// Expected is the owner's list, or ExpectedErr when it cannot be read.
	Expected    []string
	ExpectedErr error
	// LockFolder is the folder of the machine-wide lock ("" with LockErr when it cannot be found).
	LockFolder string
	LockErr    error
	// Run asks a program a question (supervise.Capture; tests give their own).
	Run func(argv []string, input []byte) (supervise.Captured, error)
	// ProbeGuard proves the installed guard refuses, with a JACKSTAY_HOME of its own, so the probe leaves no
	// mark in the home it inspects (prove.Guard).
	ProbeGuard func(guard string) error
	// SourceCommit is the commit of a checkout (install.Commit).
	SourceCommit func(checkout string) (string, error)
	// BuildChanged says whether the build inputs changed since the installed commit (install.BuildChanged).
	BuildChanged func(checkout, installed string) (bool, error)
	// Mac says the signing checks apply (macOS); CodesignIdentity is the identity the install signs
	// with ("" when none is configured), CodesignSetting the file that names it (W-070).
	Mac                               bool
	CodesignIdentity, CodesignSetting string
	// CodesignErr is why the setting could not be read (install refuses then too).
	CodesignErr error
	// Adoption is jst adopt's check of the project around the current folder (adopt.Check;
	// docs/design/adopt.md section 10); nil, or inProject false, skips it.
	Adoption func() (inProject bool, problems, notes []string)
}

// Run is the command.
func Run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 2 && (args[0] == "--expect" || args[0] == "--forget") {
		return change(args[0] == "--expect", args[1], stdout, stderr)
	}
	asJSON, prefix := false, ""
	for index := 0; index < len(args); index++ {
		switch {
		case args[index] == "--json" && !asJSON:
			asJSON = true
		case args[index] == "--prefix" && prefix == "" && index+1 < len(args):
			index++
			prefix, _ = filepath.Abs(args[index])
		default:
			fmt.Fprintln(stderr, usage)
			return 2
		}
	}
	report := Diagnose(RealInputs(prefix))
	if asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(report)
	} else {
		writeText(stdout, report)
	}
	if len(report.Problems) > 0 {
		return 1
	}
	return 0
}

// RealInputs are this machine's inputs; prefix "" is the running binary's own prefix, else ~/.local.
func RealInputs(prefix string) Inputs {
	if prefix == "" {
		prefix = OwnPrefix()
	}
	now := time.Now()
	inputs := Inputs{Prefix: prefix, Now: now, Version: cli.Identity(), GuardSpans: guardSpans(now),
		CheckPath: true,
		Settings:  filepath.Join(homedir.AccountHome(), ".claude", "settings.json"),
		Run: func(argv []string, input []byte) (supervise.Captured, error) {
			ctx, cancel := context.WithTimeout(context.Background(), prove.Timeout)
			defer cancel()
			return supervise.Capture(ctx, argv, "/", os.Environ(), input)
		},
		ProbeGuard:   func(guard string) error { return prove.Guard(context.Background(), guard, nil) },
		SourceCommit: func(checkout string) (string, error) { return install.Commit(context.Background(), checkout) },
		BuildChanged: func(checkout, installed string) (bool, error) {
			return install.BuildChanged(context.Background(), checkout, installed)
		},
		Adoption: func() (bool, []string, []string) {
			here, err := os.Getwd()
			if err != nil {
				return false, nil, nil
			}
			return adopt.Check(here)
		}}
	inputs.Home, _ = homedir.Home()
	inputs.ActiveJst, _ = exec.LookPath("jst")
	inputs.Mac = runtime.GOOS == "darwin"
	inputs.CodesignIdentity, inputs.CodesignSetting, inputs.CodesignErr = install.SigningIdentity()
	inputs.Expected, inputs.ExpectedErr = expected.Read()
	if path, err := flock.Path(); err != nil {
		inputs.LockErr = err
	} else {
		inputs.LockFolder = filepath.Dir(path)
	}
	return inputs
}

// OwnPrefix is the prefix the running binary is installed under (it runs as P/lib/jackstay-*/jst), else
// the account's ~/.local.
func OwnPrefix() string {
	if prefix, ok := layout.RunningPrefix(); ok {
		return prefix
	}
	return install.AccountPrefix()
}

// Diagnose runs every check.
func Diagnose(inputs Inputs) Report {
	report := Report{Facts: map[string]any{"version": inputs.Version, "prefix": inputs.Prefix, "home": inputs.Home, "settings": inputs.Settings}, Problems: []Finding{}, Notes: []Finding{}}
	checkPath(&report, inputs)
	checkLayout(&report, inputs)
	checkLogs(&report, inputs)
	checkMarks(&report, inputs)
	if blocked, fix := SandboxBlocks(inputs.Settings, inputs.Home); blocked != "" {
		report.problem("sandbox-blocks-state", blocked, fix)
	}
	checkLock(&report, inputs)
	checkAdoption(&report, inputs)
	return report
}

// checkAdoption reports a project that is not up to date with jst: its conflicts, pending changes,
// a profile that cannot be read (problems) and the exceptions it keeps (notes).
func checkAdoption(report *Report, inputs Inputs) {
	if inputs.Adoption == nil {
		return
	}
	inProject, problems, notes := inputs.Adoption()
	if !inProject {
		return
	}
	for _, problem := range problems {
		report.problem("adoption", problem,
			"in the project, `jst adopt --plan`: it lists what is pending and how to settle each conflict; then `jst adopt --apply`")
	}
	for _, note := range notes {
		report.note("adoption", "%s", note)
	}
	if len(problems) == 0 {
		report.Facts["adoption"] = "up to date"
	} else {
		report.Facts["adoption"] = fmt.Sprintf("%d problem(s): run `jst adopt --plan`", len(problems))
	}
}

func checkLayout(report *Report, inputs Inputs) {
	state, err := layout.Read(inputs.Prefix)
	if err != nil {
		report.problem("layout-unreadable", fmt.Sprintf("%s cannot be read: %v", inputs.Prefix, err),
			fmt.Sprintf("make %s readable by this account, or run `jst doctor --prefix DIR` for the prefix jackstay is installed under", inputs.Prefix))
		return
	}
	facts := map[string]any{"lib_jackstay": "absent"}
	for _, launcher := range state.Launchers {
		facts[launcher.Name] = launcher.Kind.String()
	}
	if state.Live != nil {
		facts["lib_jackstay"], facts["live"] = state.Live.Name, state.Live.Version
	}
	report.Facts["layout"] = facts
	for _, partial := range state.Partials {
		report.note("partial-left", "%s is left from a stopped install; the next install or uninstall removes it", partial)
	}
	if layout.LockHeld(inputs.Prefix) {
		report.note("install-running", "an install or uninstall is running in %s: what follows may change", inputs.Prefix)
	}
	for _, launcher := range state.Launchers {
		switch launcher.Kind {
		case layout.Absent:
			report.problem("launcher-missing", fmt.Sprintf("%s is missing: hooks that name it do not block anything.", launcher.Path),
				installAgain(inputs.Prefix))
		case layout.Foreign:
			report.problem("launcher-not-fixed", fmt.Sprintf("%s is not a launcher jackstay wrote: hooks that name it run something else.", launcher.Path),
				fmt.Sprintf("move %s away (jackstay never overwrites what it did not write), then %s", launcher.Path, installAgain(inputs.Prefix)))
		}
	}
	switch {
	case state.Link == layout.LinkAbsent:
		report.problem("link-missing", fmt.Sprintf("%s is missing: no version is live.", layout.Link(inputs.Prefix)), installAgain(inputs.Prefix))
	case state.Link != layout.LinkToFolder:
		report.problem("folder-incomplete", fmt.Sprintf("%s does not name a whole version folder of jackstay's (%s).", layout.Link(inputs.Prefix), state.LinkTarget),
			fmt.Sprintf("if %s is not jackstay's, move it away; then %s", layout.Link(inputs.Prefix), installAgain(inputs.Prefix)))
	}
	guard := filepath.Join(layout.Bin(inputs.Prefix), layout.Guard)
	if _, err := os.Stat(guard); err == nil {
		if err := inputs.ProbeGuard(guard); err != nil {
			report.problem("guard-does-not-refuse", fmt.Sprintf("%s did not refuse `git reset --hard`: the installed guard does not work.", guard),
				fmt.Sprintf("`%s check -- 'git reset --hard'` shows what it answers; %s", guard, replaceLive(inputs.Prefix)))
		}
	}
	program := filepath.Join(layout.Bin(inputs.Prefix), layout.Program)
	if _, err := os.Stat(program); err == nil && state.Live != nil {
		said, err := inputs.Run([]string{program, "version"}, nil)
		text := strings.TrimPrefix(strings.TrimSpace(string(said.Stdout)), "jackstay ")
		if err != nil || said.Status != 0 || text != state.Live.Version {
			report.problem("version-mismatch", fmt.Sprintf("%s says %q, and %s names %q.", program, text, layout.Link(inputs.Prefix), state.Live.Version),
				replaceLive(inputs.Prefix))
		}
	}
	if state.Live != nil {
		checkSource(report, inputs, state.Live)
	}
	checkSigning(report, inputs, state.Live)
}

// failure is why a program could not answer: its error, else its exit status and first line.
func failure(said supervise.Captured, err error) string {
	if err != nil {
		return err.Error()
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(said.Stderr)), "\n")
	return fmt.Sprintf("exit %d: %s", said.Status, line)
}

// checkSigning reports the live binary's signature (its identifier and authority, as facts) and,
// with an identity configured (W-070): that identity missing from the keychain or expired, and a
// live binary not signed by it (installed before the setting, or by another identity).
func checkSigning(report *Report, inputs Inputs, live *layout.Folder) {
	if !inputs.Mac {
		return
	}
	facts := map[string]any{"identity": inputs.CodesignIdentity}
	report.Facts["signature"] = facts
	if inputs.CodesignErr != nil {
		report.problem("codesign-setting-unreadable", fmt.Sprintf("the signing setting cannot be read (%v): `jst install` refuses until it can.", inputs.CodesignErr),
			"make ~/.jackstay/.all/codesign-identity a readable file holding one identity's name, as `security find-identity -v -p codesigning` prints it, or remove it to sign nothing")
	}
	if inputs.CodesignIdentity != "" {
		listed, err := inputs.Run([]string{"security", "find-identity", "-v", "-p", "codesigning"}, nil)
		if err != nil || listed.Status != 0 {
			report.problem("codesign-uninspected", fmt.Sprintf("the keychain could not be asked whether %q is valid (`security find-identity -v -p codesigning`: %s).",
				inputs.CodesignIdentity, failure(listed, err)),
				"run `security find-identity -v -p codesigning` in your own terminal (unlock the keychain if it asks), then `jst doctor` again")
		} else if !install.IdentityValid(string(listed.Stdout), inputs.CodesignIdentity) {
			report.problem("codesign-identity-invalid", fmt.Sprintf("%s names %q, which is not a valid code-signing identity in the keychain (missing or expired): an install that has to sign (a binary you built) is refused until it is.",
				inputs.CodesignSetting, inputs.CodesignIdentity),
				fmt.Sprintf("write into %s a name `security find-identity -v -p codesigning` lists as valid, or remove the file to sign nothing", inputs.CodesignSetting))
		}
	}
	if live == nil {
		return
	}
	binary := filepath.Join(live.Path, layout.Program)
	said, err := inputs.Run(install.DisplayArgs(binary), nil)
	if err != nil || said.Status != 0 {
		report.problem("codesign-uninspected", fmt.Sprintf("the signature of %s could not be read (`codesign --display`: %s).", binary, failure(said, err)),
			fmt.Sprintf("run `codesign --display --verbose=2 %s` in your own terminal to see why", binary))
		return
	}
	signature := install.ReadSignature(string(said.Stderr) + string(said.Stdout))
	facts["identifier"], facts["authority"] = signature.Identifier, signature.Authority
	if signature.AdHoc {
		facts["authority"] = "ad-hoc"
	}
	// The live binary is judged by install's own rule (install.KeepsItsSignature): a problem exactly
	// when the next install would sign it again.
	keeps := true
	if inputs.CodesignIdentity != "" {
		run := func(argv []string) (supervise.Captured, error) { return inputs.Run(argv, nil) }
		kept, _, err := install.KeepsItsSignature(run, binary)
		keeps = err == nil && kept
	}
	if !keeps {
		report.problem("binary-not-signed", fmt.Sprintf("%s does not keep its signature, so the next install replaces it (identifier %q, authority %q): unsigned or ad-hoc (installed before the setting), an identifier other than %s, or a signature that does not verify strictly. The binary that replaces it is signed with %q unless it keeps its own signature (a release's).",
			binary, signature.Identifier, facts["authority"], install.SigningIdentifier, inputs.CodesignIdentity),
			"`jst install` replaces it: run `jst install --binary FILE` with a binary whose `jst version` prints exactly what the live one prints, or install a newer version")
	}
}

// checkSource compares the live version with its checkout, when it came from one: an install is
// due only when what goes into the binary changed since the installed commit (W-020), not when
// the checkout moved on in its docs, recipes or release files.
func checkSource(report *Report, inputs Inputs, live *layout.Folder) {
	source := layout.ReadSource(live.Path)
	if !filepath.IsAbs(source) || install.FindCheckout(source) != source {
		return
	}
	now, err := inputs.SourceCommit(source)
	if err != nil {
		return
	}
	report.Facts["checkout"] = now
	if strings.HasSuffix(live.Version, "("+now+")") {
		return
	}
	_, installed, _ := strings.Cut(strings.TrimSuffix(live.Version, ")"), " (")
	changed := true
	if !strings.HasSuffix(now, "+changes") && installed != "" && !strings.HasSuffix(installed, "+changes") {
		if differs, err := inputs.BuildChanged(source, installed); err == nil {
			changed = differs
		}
	}
	if !changed {
		report.Facts["checkout_build_inputs"] = "unchanged since " + installed
		return
	}
	report.problem("checkout-moved", fmt.Sprintf("the installed copy is %s and the checkout %s is at %s with changes to what goes into the binary.", live.Version, source, now),
		fmt.Sprintf("`jst install` in %s, when that change is meant to be used", source))
}

// guardSpans are the guard's command spans of every project that end in the window before now.
func guardSpans(now time.Time) []journal.Span {
	folders, err := query.Folders(query.Scope{All: true})
	if err != nil {
		return nil
	}
	spans, _ := query.Load(folders, now.Add(-logWindow), now.Add(time.Second), now)
	var found []journal.Span
	for _, span := range spans {
		if query.IsCommand(span) && span.Text(telemetry.AttrCommand) == "guard" {
			found = append(found, span)
		}
	}
	return found
}

// checkLogs reads the guard's spans of the last 7 days from the journal: calls let through on its
// own error, projects whose jackstay.json could not be used, tools it was called for and ignored.
func checkLogs(report *Report, inputs Inputs) {
	since := inputs.Now.Add(-logWindow)
	failed, configErrors, ignored := map[string]int{}, map[string]int{}, map[string]int{}
	for _, span := range inputs.GuardSpans {
		if span.End.Before(since) {
			continue
		}
		project := query.Project(span)
		if project == "" {
			project = "unknown"
		}
		// Only the hook's decisions carry an outcome; a span without one is the command used by hand
		// (`jst guard check`, `--help`), never a call let through.
		switch span.Text(telemetry.AttrGuardOutcome) {
		case "failed_open":
			failed[project]++
		case "ignored_tool":
			tool := span.Text(telemetry.AttrGuardTool)
			if tool == "" {
				tool = "?"
			}
			ignored[tool]++
		}
		if span.Text(telemetry.AttrGuardConfigError) != "" {
			configErrors[project]++
		}
	}
	report.Facts["guard_last_7_days"] = map[string]any{"failed_open": failed, "config_error": configErrors, "ignored_tools": ignored}
	for _, project := range sortedKeys(failed) {
		report.problem("failed-open", fmt.Sprintf("the guard let %d calls through on its own error in %s (last 7 days).", failed[project], project),
			"`jst stats guard --all-projects --by project --since 7d` names each failure with its stack; report it as a bug, with that output")
	}
	for _, project := range sortedKeys(configErrors) {
		report.problem("config-error", fmt.Sprintf("%s's jackstay.json could not be used in %d guard calls (last 7 days; built-in rules still applied).", project, configErrors[project]),
			fmt.Sprintf("`jst check` in %s names each problem of its jackstay.json; fix them there", project))
	}
}

// checkMarks reads each project's guard-seen mark.
func checkMarks(report *Report, inputs Inputs) {
	marks := map[string]time.Time{}
	if entries, err := os.ReadDir(inputs.Home); err == nil {
		for _, entry := range entries {
			if entry.Name() == homedir.Shared {
				continue
			}
			if info, err := os.Stat(filepath.Join(inputs.Home, entry.Name(), "guard-seen")); err == nil && info.Mode().IsRegular() {
				marks[entry.Name()] = info.ModTime()
			}
		}
	}
	seen := map[string]float64{}
	for _, name := range sortedKeys(marks) {
		age := inputs.Now.Sub(marks[name])
		seen[name] = float64(int64(age.Hours()*10+0.5)) / 10
		if age > quietWindow {
			report.note("guard-quiet", "the guard has not been called in %s for %.1f days: no sessions there, or its hook is off "+
				"(an untrusted folder, an unapproved hook, a removed settings file). `jst doctor --forget NAME` retires a project.", name, age.Hours()/24)
		}
	}
	report.Facts["guard_last_call_hours_ago"] = seen
	report.Facts["expected_projects"] = append([]string{}, inputs.Expected...)
	if inputs.ExpectedErr != nil {
		report.problem("expected-unreadable", fmt.Sprintf("the list of expected projects cannot be read: %v", inputs.ExpectedErr),
			"`jst doctor --expect NAME` writes the list again; first move the unreadable file named above away")
	}
	for _, name := range inputs.Expected {
		if _, found := seen[name]; !found {
			report.problem("expected-never-called", fmt.Sprintf("%s is expected to have a working guard and the guard has never been called there: its hook is not "+
				"installed, its folder is not trusted, or (Codex) the hook is not approved.", name),
				"in the project, `jst adopt --apply` writes the hook; accept Claude Code's trust dialog (or approve the hook in Codex's /hooks), run one shell command in a session, then `jst doctor`; "+
					fmt.Sprintf("`jst doctor --forget %s` if it no longer needs one", name))
		}
	}
	if len(marks) == 0 && len(inputs.Expected) == 0 {
		report.problem("guard-never-called", "the guard has never been called on this machine: no project has a working hook yet.",
			"in a project, `jst adopt --apply` and commit; open a Claude Code session there, accept its trust dialog and run one shell command; then `jst doctor`")
	}
}

func checkLock(report *Report, inputs Inputs) {
	if inputs.LockErr != nil {
		report.problem("lock-unwritable", fmt.Sprintf("the lock cannot be found: %v", inputs.LockErr),
			"set JACKSTAY_HOME to an absolute folder this account can write, or unset it")
		return
	}
	report.Facts["lock"] = filepath.Join(inputs.LockFolder, flock.FileName)
	if !canWriteLockFolder(inputs.LockFolder) {
		report.problem("lock-unwritable", fmt.Sprintf("%s is not writable here: no heavy run can start (inside an agent sandbox this is expected).", inputs.LockFolder),
			fmt.Sprintf("let this account write %s; inside Claude Code's sandbox, add \"~/.jackstay\" to sandbox.filesystem.allowWrite in %s", inputs.LockFolder, inputs.Settings))
	}
}

func canWriteLockFolder(folder string) bool {
	for {
		info, err := os.Stat(folder)
		if err == nil {
			return info.IsDir() && unix.Access(folder, unix.W_OK|unix.X_OK) == nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		parent := filepath.Dir(folder)
		if parent == folder {
			return false
		}
		folder = parent
	}
}

func sortedKeys[Value any](values map[string]Value) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeText(out io.Writer, report Report) {
	for _, note := range report.Notes {
		fmt.Fprintln(out, "note: "+note.Text)
	}
	for _, key := range sortedKeys(report.Facts) {
		value := report.Facts[key]
		if text, isText := value.(string); isText {
			fmt.Fprintf(out, "%s: %s\n", key, text)
			continue
		}
		encoded, _ := json.Marshal(value)
		fmt.Fprintf(out, "%s: %s\n", key, encoded)
	}
	if len(report.Problems) == 0 {
		fmt.Fprintln(out, "jst doctor: nothing needs the owner.")
	}
	for _, problem := range report.Problems {
		fmt.Fprintln(out, "PROBLEM: "+problem.Text)
		fmt.Fprintln(out, "  fix: "+problem.Fix)
	}
}

// quickInstall is the download half of the documented one-liner (README.md; internal/installscript
// holds them to the same text).
const quickInstall = "curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh"

// installInto is the one-liner that installs the newest release into prefix: the prefix, as one
// shell word, is given to the `sh` that runs the script, so the install keeps the prefix the
// hooks name.
func installInto(prefix string) string {
	return fmt.Sprintf(`script=$(%s) && JACKSTAY_PREFIX=%s sh -c "$script"`, quickInstall, shellWord(prefix))
}

// installAgain is the fix of a broken install under prefix: the newest release installed into the
// same prefix (a stopped install is finished by running it again). The command is POSIX shell, and
// says so: a shell of another syntax (fish) cannot run it as written.
func installAgain(prefix string) string {
	return "in sh, bash or zsh, install into the same prefix: `" + installInto(prefix) + "`"
}

// replaceLive is the fix of a live version whose binary is wrong: an install into the same prefix
// (a newer version, or the same one signed again, repairs it), then doctor asked about that prefix
// alone. It never advises an uninstall: which install a later report is about, and whether the
// repair ran at all, are not known here, and an uninstall removes the version before too.
func replaceLive(prefix string) string {
	return installAgain(prefix) + "; when that succeeds, `jst doctor --prefix " + shellWord(prefix) +
		"`; if it still reports this problem, report it as a bug with that output (docs/guide/install.md#what-can-go-wrong)"
}

// shellWord is text as one word for a POSIX shell: in single quotes, each ' written as '\”.
func shellWord(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// change is `--expect NAME` / `--forget NAME`.
func change(add bool, name string, stdout, stderr io.Writer) int {
	given := strings.TrimSpace(name)
	if given == "" {
		fmt.Fprintf(stderr, "jst doctor: %q is not a project's name\n", name)
		return 2
	}
	marker := homedir.MarkerName(given)
	names, err := expected.Change(func(names map[string]bool) {
		if add {
			names[marker] = true
			return
		}
		delete(names, marker)
		delete(names, name)
	})
	if err != nil {
		fmt.Fprintf(stderr, "jst doctor: the list of expected projects cannot be changed: %v\n", err)
		return 1
	}
	if !add {
		if path, err := homedir.ProjectPath(given, "guard-seen"); err == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				fmt.Fprintf(stderr, "jst doctor: warning: %v\n", err)
			}
		}
	}
	list := "none"
	if len(names) > 0 {
		list = strings.Join(names, ", ")
	}
	fmt.Fprintln(stdout, "jst doctor: expected projects: "+list)
	return 0
}
