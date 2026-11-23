package guard

import (
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"github.com/zima-oss/jackstay/internal/config"
)

// Stable identifiers of the rules and warnings that are not rows of the table (section 7).
const (
	ruleUnparsable  = "unparsable"
	ruleTooDeep     = "too-deep"
	ruleTooLong     = "too-long"
	ruleUnreadable  = "unreadable-call"
	warnNUL         = "nul-byte"
	warnProducer    = "producer-scanned"
	warnTooDeep     = "too-deep"
	warnRegenerable = "regenerable-ignored"
	warnRuleBroken  = "project-rule-unreadable"
	readingDeadline = 2 * time.Second
)

// A Finding is one reason to refuse.
type Finding struct {
	Rule    string
	Command string
	Why     string
	Instead string
}

// A Warning is something the guard could not apply, with its stable identifier.
type Warning struct {
	ID   string
	Text string
}

// Settings are what a decision depends on besides the line.
type Settings struct {
	Folder      string                                     // where the call runs
	Environ     map[string]string                          // the hook's environment: SHELL, TMPDIR, GIT_DIR, the project rules' variables
	Scratch     []string                                   // the session's own scratch folders
	Home        string                                     // the account's home folder
	ProjectRoot string                                     // the folder of the project's jackstay.json ("" when none)
	Guard       map[string]any                             // jackstay.json's `guard` section (nil when none or unusable)
	Cards       map[string]any                             // jackstay.json's `cards` section (nil when none); Broken when it cannot be read
	CardsBroken bool                                       // the project declares `cards` and the section cannot be used
	CardsAt     func(folder string) (map[string]any, bool) // the `cards` section of the project a folder is in
	Git         Questions                                  // the questions to git
	Deadline    time.Duration                              // the reading's budget; zero is readingDeadline
	TrashKeep   bool                                       // the project keeps what jst trash and jst discard remove (`trash.keep` on)
}

// A Decision is the findings and warnings for one call.
type Decision struct {
	Findings []Finding
	Warnings []Warning
	// Warned are findings of rows the project, or the default, sets to `warn` (W-053): the
	// command runs and the agent is told.
	Warned []Finding
}

// Rules is the set of rule ids of the findings, in order.
func (decision Decision) Rules() []string {
	found := make([]string, 0, len(decision.Findings))
	for _, finding := range decision.Findings {
		if !slices.Contains(found, finding.Rule) {
			found = append(found, finding.Rule)
		}
	}
	return found
}

// Decide reads a command line and says what in it would destroy work. A NUL byte, which no shell
// takes, makes it read twice: without it and with a space in its place.
func Decide(command string, settings Settings) (decision Decision) {
	if len(command) > maxCommandBytes {
		decision.Findings = append(decision.Findings, Finding{Rule: ruleTooLong, Command: command[:200],
			Why: "the command is longer than the guard reads (1 MiB)", Instead: "write it plainly, one command per line"})
		return decision
	}
	readings := []string{command}
	if strings.Contains(command, "\x00") {
		readings = []string{strings.ReplaceAll(command, "\x00", ""), strings.ReplaceAll(command, "\x00", " ")}
		decision.Warnings = append(decision.Warnings, Warning{ID: warnNUL,
			Text: "the command holds a NUL byte, which no shell takes; it was read without it and with a space in its place"})
	}
	defer func() {
		decision = applyLevels(decision, settings.Guard)
		decision.Findings = keepWording(decision.Findings, settings.TrashKeep)
		decision.Warned = keepWording(decision.Warned, settings.TrashKeep)
	}()
	for _, line := range readings {
		reader := newReader(line, settings)
		reader.read()
		for _, finding := range reader.findings {
			if !slices.ContainsFunc(decision.Findings, func(known Finding) bool {
				return known.Rule == finding.Rule && known.Command == finding.Command
			}) {
				decision.Findings = append(decision.Findings, finding)
			}
		}
		for _, warning := range reader.warnings {
			if !slices.Contains(decision.Warnings, warning) {
				decision.Warnings = append(decision.Warnings, warning)
			}
		}
	}
	return decision
}

// Level is a row's level for a project: its `guard.rules` entry, else the row's default; a
// project's own deny rules and the reader's own refusals always refuse.
func Level(rule string, section map[string]any) string {
	known, ok := config.GuardRules[rule]
	if !ok || !known.Configurable {
		return config.LevelRefuse
	}
	rules, _ := section["rules"].(map[string]any)
	entry, _ := rules[rule].(map[string]any)
	if level, ok := entry["level"].(string); ok {
		return level
	}
	return known.Default
}

// applyLevels drops the findings of rows set `off` and turns those set `warn` into Warned. It
// runs on the complete findings: every check has reported its own row (W-055).
func applyLevels(decision Decision, section map[string]any) Decision {
	kept := decision.Findings[:0:0]
	for _, finding := range decision.Findings {
		switch Level(finding.Rule, section) {
		case config.LevelOff:
		case config.LevelWarn:
			decision.Warned = append(decision.Warned, finding)
		default:
			kept = append(kept, finding)
		}
	}
	decision.Findings = kept
	return decision
}

// A reader reads one call: the line, what the session and the project tell about it, and what
// it found.
type reader struct {
	raw         string
	settings    Settings
	home        string
	tmpdir      string
	environ     map[string]string
	folder      string
	origins     []string // where the session is: never a throwaway checkout
	scratch     []string
	exact       []string // temporary inside, never removed themselves
	shared      []string // every session's temporary folders: what is right inside must be named
	projectRoot string
	regenerable []string
	declared    []string
	deny        []denyRule
	exceptions  map[string][]exception // guard.rules' path exceptions, by rule
	alone       bool                   // the line is one plain rm or find call: path exceptions may apply
	questions   *answers
	findings    []Finding
	warnings    []Warning
	steps       int
	deadline    time.Time
	scanned     bool
}

var (
	temporaryRoots = []string{"/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp"}
	deepRoots      = []string{"/var/folders", "/private/var/folders"}
	builtIn        = []string{".build", "DerivedData", "node_modules", "__pycache__", ".pytest_cache", ".mypy_cache",
		".ruff_cache", ".tox", ".venv", "venv", "htmlcov", "*.egg-info"}
)

// unknownTmpdir stands for $TMPDIR where the hook is not told it: still a temporary path.
const unknownTmpdir = "/private/tmp/jst-guard-tmpdir"

func newReader(line string, settings Settings) *reader {
	budget := settings.Deadline
	if budget == 0 {
		budget = readingDeadline
	}
	reader := &reader{raw: line, settings: settings, home: settings.Home, environ: settings.Environ,
		deadline: time.Now().Add(budget), questions: newAnswers(settings.Git)}
	if reader.environ == nil {
		reader.environ = map[string]string{}
	}
	if reader.home == "" {
		reader.home = "/"
	}
	reader.folder = flat(settings.Folder)
	if reader.folder == "" || !path.IsAbs(reader.folder) {
		reader.folder = "/"
	}
	reader.origins = []string{reader.folder, realPath(reader.folder)}
	reader.tmpdir = strings.TrimRight(reader.environ["TMPDIR"], "/")
	if reader.environ["TMPDIR"] == "" {
		reader.tmpdir = unknownTmpdir
	}
	if reader.tmpdir == "" {
		reader.tmpdir = "/"
	}
	reader.exact = []string{freshFolder}
	reader.shared = slices.Clone(temporaryRoots)
	for _, form := range forms(reader.tmpdir) {
		reader.exact = append(reader.exact, form)
		reader.shared = append(reader.shared, form)
	}
	for _, folder := range settings.Scratch {
		for _, form := range forms(folder) {
			reader.exact = append(reader.exact, form)
			reader.scratch = append(reader.scratch, form)
		}
	}
	if settings.ProjectRoot != "" {
		reader.projectRoot = realPath(settings.ProjectRoot)
	}
	reader.regenerable = slices.Clone(builtIn)
	reader.loadGuard(settings.Guard)
	reader.alone = len(reader.exceptions) > 0 && aloneCommand(line)
	return reader
}

// forms are a folder as written and where it really is.
func forms(folder string) []string {
	if folder == "" || !path.IsAbs(folder) {
		return nil
	}
	written := flat(folder)
	real := realPath(written)
	if real == written {
		return []string{written}
	}
	return []string{written, real}
}

// realPath is where a path really is: the links of the part of it that exists followed.
func realPath(folder string) string {
	return realPathOfExisting(flat(folder))
}

// flat is a path made plain: `.` and `..` worked out, one leading slash.
func flat(folder string) string {
	if folder == "" {
		return ""
	}
	return path.Clean(folder)
}

func (reader *reader) read() {
	defer func() {
		if found := recover(); found != nil {
			stopped, ok := found.(stop)
			if !ok {
				panic(found)
			}
			reader.refuseWhole(stopped.rule)
		}
	}()
	file := reader.parse(reader.raw)
	top := &shell{reader: reader, zsh: path.Base(reader.environ["SHELL"]) == "zsh", functions: map[string]*syntax.Stmt{},
		called: map[string]bool{}}
	top.script(file, startState(reader.folder))
}

// parse reads a script as bash, then as zsh; one neither reads is refused (W-039). A here-document
// left open runs to the end of the text, as bash reads it (with a warning).
func (reader *reader) parse(text string) *syntax.File {
	for _, attempt := range []string{text, spacedSubshells(text)} {
		for _, language := range []syntax.LangVariant{syntax.LangBash, syntax.LangZsh} {
			candidate := attempt
			for range 8 {
				file, err := syntax.NewParser(syntax.Variant(language)).Parse(strings.NewReader(candidate), "")
				if err == nil {
					return file
				}
				delimiter := openHeredoc.FindStringSubmatch(err.Error())
				if delimiter == nil {
					break
				}
				candidate += "\n" + delimiter[1]
			}
		}
	}
	panic(stop{rule: ruleUnparsable})
}

var openHeredoc = regexp.MustCompile("unclosed here-document `([^`]*)`")

// spacedSubshells is the line with `((` read as two subshells, as bash reads `((cmd) && (x))` when
// it is not arithmetic; tried only when the line does not parse as written.
func spacedSubshells(text string) string {
	var built strings.Builder
	for index := range len(text) {
		built.WriteByte(text[index])
		if text[index] == '(' && index+1 < len(text) && text[index+1] == '(' && (index == 0 || text[index-1] != '$') {
			built.WriteByte(' ')
		}
	}
	return built.String()
}

func (reader *reader) refuseWhole(rule string) {
	shown := reader.raw
	if len(shown) > 200 {
		shown = shown[:200]
	}
	why := map[string]string{
		ruleUnparsable: "it could not be read as shell",
		ruleTooDeep:    "its scripts are nested deeper than the guard reads (8 levels)",
		ruleTooLong:    "it is longer than the guard reads in full (20,000 commands, or the reading's time)",
	}[rule]
	reader.add(Finding{Rule: rule, Command: oneLine(shown), Why: why,
		Instead: "write it plainly, one command per line, so it can be checked"})
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func (reader *reader) add(finding Finding) {
	for _, known := range reader.findings {
		if known.Rule == finding.Rule && (known.Command == finding.Command || strings.HasPrefix(finding.Rule, "project:")) {
			return
		}
	}
	reader.findings = append(reader.findings, finding)
}

func (reader *reader) warn(id, text string) {
	warning := Warning{ID: id, Text: text}
	if !slices.Contains(reader.warnings, warning) {
		reader.warnings = append(reader.warnings, warning)
	}
}

// fileExists says whether something is at a path (a link counts, wherever it leads).
func fileExists(name string) bool {
	_, err := os.Lstat(name)
	return err == nil
}
