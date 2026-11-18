package guard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/procs"
)

func init() {
	cli.Register(cli.Command{Name: "guard", Summary: "the Claude Code hook that refuses destructive commands; `check [--cwd DIR] -- COMMAND`",
		Usage: help, Run: command})
}

// The hook's bounds (guard.md section 7).
const (
	maxInputBytes  = 4 << 20
	maxConfigBytes = 1 << 20
	answerBy       = 5 * time.Second
	exitBy         = 8 * time.Second
)

const askTheOwner = "If the owner really wants exactly this, ask them to run it in their own terminal. " +
	"Do not rephrase the command to get around the guard."

func command(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "check" {
		return check(args[1:], stdout, stderr)
	}
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: jst guard                          the hook: a tool call on standard input\n"+
			"       jst guard check [--cwd DIR] -- COMMAND   exit 0 allowed, 2 refused (the reason on standard error)")
		return 64
	}
	return Hook(stdin, stdout, stderr)
}

// A project is what the hook knows of the project a call runs in.
type project struct {
	name    string
	root    string
	guard   map[string]any
	cards   map[string]any
	broken  bool   // it declares `cards` and the section cannot be used
	problem string // why its jackstay.json cannot be used, or ""
}

// projectOf reads the project around a folder: a broken section the guard does not read takes
// nothing away; a jackstay.json that cannot be used takes the project's own rules away, not the built-in ones.
func projectOf(folder string) project {
	found := project{}
	root, loaded, err := config.Load(folder, []string{"guard"})
	if err != nil {
		var problem *config.Error
		if errors.As(err, &problem) {
			found.problem = problem.Message
		} else {
			found.problem = err.Error()
		}
		root, _ = config.Find(folder)
		loaded = config.Config{}
	}
	found.root = root
	found.name = config.ProjectName(root, loaded, folder)
	found.guard = loaded.Section("guard")
	found.cards, found.broken = CardsOf(folder)
	return found
}

// CardsOf is the `cards` section of the project around a folder, and whether it is declared and
// cannot be used (C3: the Card rule then stays on and refuses what it cannot judge).
func CardsOf(folder string) (map[string]any, bool) {
	_, loaded, err := config.Load(folder, []string{"cards"})
	if err == nil {
		return loaded.Section("cards"), false
	}
	return nil, declaresCards(folder)
}

var cardsMember = regexp.MustCompile(`"cards"\s*:`)

// declaresCards says whether the jackstay.json around a folder has a `cards` section, or cannot be read
// far enough to say (a merge conflict in it: decided by its text).
func declaresCards(folder string) bool {
	_, place := config.Find(folder)
	if place == "" {
		return false
	}
	data, err := readBounded(place, maxConfigBytes)
	if err != nil {
		return true
	}
	parsed, err := config.Parse(data)
	if err != nil {
		return cardsMember.Match(data)
	}
	_, ok := parsed["cards"]
	return ok
}

func readBounded(place string, limit int64) ([]byte, error) {
	file, err := os.Open(place) // #nosec G304 -- the project's own jackstay.json
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("jackstay.json is larger than the guard reads")
	}
	return data, nil
}

func environment() map[string]string {
	found := map[string]string{}
	for _, pair := range os.Environ() {
		if name, given, ok := strings.Cut(pair, "="); ok {
			found[name] = given
		}
	}
	return found
}

func (found project) settingsFor(folder string, scratch []string) Settings {
	return Settings{Folder: folder, Environ: environment(), Scratch: scratch, Home: homedir.AccountHome(),
		ProjectRoot: found.root, Guard: found.guard, Cards: found.cards, CardsBroken: found.broken, CardsAt: CardsOf,
		Git: NewGit(CardRange)}
}

// Refusal is the text a refusal gives the agent.
func Refusal(findings []Finding) string {
	lines := []string{"jst guard refused this command; nothing was run."}
	corrections := true
	for _, finding := range findings {
		lines = append(lines, fmt.Sprintf("- `%s`: %s. Instead: %s.", finding.Command, finding.Why, finding.Instead))
		if finding.Rule != "zsh-glob" {
			corrections = false
		}
	}
	if corrections {
		lines = append(lines, "Quote the pattern and run the command again; this is about a pattern zsh would misread, "+
			"not about anything it destroys.")
	} else {
		lines = append(lines, askTheOwner)
	}
	return strings.Join(lines, "\n")
}

// Warned is the text a warned command gives the agent (W-053): it runs, and this says what may
// be wrong with it.
func Warned(findings []Finding) string {
	lines := []string{"jst guard let this command run, with a warning (the project's level for these rules is `warn`):"}
	for _, finding := range findings {
		lines = append(lines, fmt.Sprintf("- [%s] `%s`: %s. Instead: %s.", finding.Rule, finding.Command, finding.Why, finding.Instead))
	}
	return strings.Join(lines, "\n")
}

func warningIDs(warnings []Warning) []string {
	found := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		found = append(found, warning.ID)
	}
	return found
}

func warningText(warnings []Warning) string {
	parts := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		parts = append(parts, "["+warning.ID+"] "+warning.Text)
	}
	return "jst guard: " + strings.Join(parts, "; ")
}

// call is the tool call a hook is given.
type call struct {
	fields map[string]any
}

func (given call) text(key string) string {
	text, _ := given.fields[key].(string)
	return text
}

// markSeen touches `<jackstay home>/<project>/guard-seen`: `jst doctor` tells a quiet guard from one that is off.
func markSeen(projectName string) {
	place, err := homedir.ProjectPath(projectName, "guard-seen")
	if err != nil {
		return
	}
	if err := os.MkdirAll(dirOf(place), 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(place, os.O_WRONLY|os.O_CREATE, 0o600) // #nosec G304 -- jackstay's own mark
	if err != nil {
		return
	}
	_ = file.Close()
	now := time.Now()
	_ = os.Chtimes(place, now, now)
}

func dirOf(place string) string {
	if cut := strings.LastIndexByte(place, '/'); cut > 0 {
		return place[:cut]
	}
	return "/"
}

var throughBg = regexp.MustCompile(`(?:^|[\s;&|(])(?:jst|jackstay)\s+bg\b`)

// A reply is what the hook writes on standard output, once.
type reply struct {
	deny    string
	message string // shown to the person
	context string // given to the agent (additionalContext); with a refusal it joins the reason
}

func (given reply) write(stdout io.Writer) {
	fields := map[string]any{}
	if given.message != "" {
		fields["systemMessage"] = given.message
	}
	switch {
	case given.deny != "":
		reason := given.deny
		if given.context != "" {
			reason += "\n" + given.context
		}
		fields["hookSpecificOutput"] = map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "deny",
			"permissionDecisionReason": reason}
	case given.context != "":
		// No permissionDecision: the call goes through the person's own permission flow unchanged;
		// additionalContext reaches the agent (code.claude.com/docs/en/hooks, read 2026-11-17).
		fields["hookSpecificOutput"] = map[string]any{"hookEventName": "PreToolUse", "additionalContext": given.context}
	}
	if len(fields) == 0 {
		return
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(fields) == nil {
		_, _ = stdout.Write(buffer.Bytes())
	}
}

const failedOpen = "jst guard failed and did NOT check this command (%s). The command runs unchecked; tell the jackstay project."

// Hook is the PreToolUse hook: a tool call on standard input, a decision on standard output, exit
// 0 always. Input it cannot read is refused; evidence it cannot get makes the rule that needed it
// refuse; only a fault of its own fails open (section 7).
func Hook(stdin io.Reader, stdout, stderr io.Writer) int {
	started := time.Now()
	done := make(chan func(), 1)
	go func() {
		var after func()
		defer func() {
			if fault := recover(); fault != nil {
				stack := debug.Stack()
				after = func() {
					recordFault(fault, stack)
				}
				message := fmt.Sprintf(failedOpen, fmt.Sprint(fault))
				reply{message: message}.write(stdout)
				fmt.Fprintln(stderr, message)
			}
			done <- after
		}()
		after = decideCall(stdin, stdout, stderr)
	}()
	var after func()
	select {
	case after = <-done:
	case <-time.After(answerBy):
		message := fmt.Sprintf(failedOpen, "internal-error: it took longer than "+answerBy.String())
		reply{message: message}.write(stdout)
		fmt.Fprintln(stderr, message)
		record(call{fields: map[string]any{}}, "", "", judged{outcome: outcomeFailedOpen, why: "it took longer than " + answerBy.String()})
		return 0
	}
	answeredIn := time.Since(started)
	if after != nil {
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			defer func() { _ = recover() }() // a log or a mark is never worth failing the hook for
			recordAnswerTime(answeredIn)
			after()
		}()
		select {
		case <-finished:
		case <-time.After(exitBy - time.Since(started)):
		}
	}
	return 0
}

// decideCall reads the call, writes the answer, and gives what is to be logged after it.
func decideCall(stdin io.Reader, stdout, stderr io.Writer) func() {
	data, err := io.ReadAll(io.LimitReader(stdin, maxInputBytes+1))
	given := call{fields: map[string]any{}}
	if err == nil && len(data) > maxInputBytes {
		reply{deny: "jst guard: the call is larger than the guard reads, so the command cannot be judged; nothing was run."}.write(stdout)
		return func() {
			record(given, "", "", judged{outcome: outcomeRefused, rules: []string{ruleUnreadable}, why: "the call is too large"})
		}
	}
	if err != nil || json.Unmarshal(data, &given.fields) != nil || given.fields == nil {
		message := fmt.Sprintf(failedOpen, "standard input is not a JSON object")
		reply{message: message}.write(stdout)
		fmt.Fprintln(stderr, message)
		return func() {
			record(given, "", "", judged{outcome: outcomeFailedOpen,
				why: fmt.Sprintf("standard input is not a JSON object (%d bytes)", len(data))})
		}
	}
	folder := given.text("cwd")
	if folder == "" {
		folder, _ = os.Getwd()
	}
	found := projectOf(folder)
	var later []func()
	if given.text("session_id") != "" {
		later = append(later, func() { markSeen(found.name) })
	}
	run := func() {
		for _, step := range later {
			step()
		}
	}
	if given.fields["tool_name"] != "Bash" {
		later = append(later, func() {
			record(given, found.name, found.root, judged{outcome: outcomeIgnoredTool})
		})
		return run
	}
	input := given.fields["tool_input"]
	if text, ok := input.(string); ok {
		var parsed any
		if json.Unmarshal([]byte(text), &parsed) == nil {
			input = parsed
		} else {
			input = nil
		}
	}
	fields, isObject := input.(map[string]any)
	commandValue, hasCommand := fields["command"]
	commandText, isText := commandValue.(string)
	unreadable := (!isObject && given.fields["tool_input"] != nil && given.fields["tool_input"] != "") ||
		(hasCommand && !isText)
	if unreadable {
		reply{deny: "jst guard: the call's tool_input cannot be read, so the command cannot be judged; nothing was run."}.write(stdout)
		later = append(later, func() {
			record(given, found.name, found.root, judged{outcome: outcomeRefused, rules: []string{ruleUnreadable},
				why: "the call's tool_input cannot be read"})
		})
		return run
	}
	if strings.TrimSpace(commandText) == "" {
		later = append(later, func() { record(given, found.name, found.root, judged{outcome: outcomeAllowed}) })
		return run
	}
	later = append(later, func() {
		procs.NoteSession(given.text("session_id"), procs.Table())
	})
	var scratch []string
	if place := given.text("scratchpad_dir"); place != "" {
		scratch = []string{place}
	}
	decision := Decide(commandText, found.settingsFor(folder, scratch))
	if found.problem != "" {
		decision.Warnings = append([]Warning{{ID: "config-unusable",
			Text: "jackstay.json cannot be used, so only the built-in rules were applied (" + found.problem + ")"}}, decision.Warnings...)
	}
	answered := reply{}
	var told []string
	if len(decision.Warnings) > 0 {
		answered.message = warningText(decision.Warnings)
		told = append(told, answered.message)
	}
	if len(decision.Warned) > 0 {
		told = append(told, Warned(decision.Warned))
		if answered.message != "" {
			answered.message += "; "
		}
		answered.message += "jst guard warned: " + strings.Join(warnedRules(decision.Warned), ", ")
	}
	answered.context = strings.Join(told, "\n")
	if len(decision.Findings) > 0 {
		answered.deny = Refusal(decision.Findings)
	}
	answered.write(stdout)
	later = append(later, func() {
		others := len(decision.Warnings)
		if found.problem != "" {
			others--
		}
		decided := judged{outcome: outcomeAllowed, command: commandText, warnings: warningIDs(decision.Warnings), configError: found.problem,
			background: fields["run_in_background"] == true && !throughBg.MatchString(commandText)}
		switch {
		case len(decision.Findings) > 0:
			decided.outcome, decided.rules = outcomeRefused, decision.Rules()
		case others > 0 || len(decision.Warned) > 0:
			decided.outcome, decided.rules = outcomeWarned, warnedRules(decision.Warned)
		}
		record(given, found.name, found.root, decided)
	})
	return run
}

// check is `jst guard check [--cwd DIR] -- COMMAND`: exit 0 allowed, 2 refused.
func check(args []string, stdout, stderr io.Writer) int {
	folder, _ := os.Getwd()
	if len(args) >= 2 && args[0] == "--cwd" {
		folder, args = args[1], args[2:]
	}
	if len(args) != 2 || args[0] != "--" {
		fmt.Fprintln(stderr, "usage: jst guard check [--cwd DIR] -- 'COMMAND'")
		return 64
	}
	found := projectOf(folder)
	decision := Decide(args[1], found.settingsFor(folder, nil))
	if found.problem != "" {
		fmt.Fprintln(stderr, "jst guard: jackstay.json cannot be used, so only the built-in rules are applied ("+found.problem+")")
	}
	for _, warning := range decision.Warnings {
		fmt.Fprintln(stderr, "jst guard: ["+warning.ID+"] "+warning.Text)
	}
	if len(decision.Warned) > 0 {
		fmt.Fprintln(stderr, Warned(decision.Warned))
	}
	if len(decision.Findings) > 0 {
		fmt.Fprintln(stderr, Refusal(decision.Findings))
		return 2
	}
	_ = stdout
	return 0
}

// warnedRules are the rule ids of warned findings, each once.
func warnedRules(findings []Finding) []string {
	found := make([]string, 0, len(findings))
	for _, finding := range findings {
		if !slices.Contains(found, finding.Rule) {
			found = append(found, finding.Rule)
		}
	}
	return found
}
