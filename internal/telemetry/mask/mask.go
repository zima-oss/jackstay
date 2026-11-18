// Package mask is what the journal keeps of a command and of a message (docs/design/observability.md,
// section 7): secrets — known by an option, a command's position or a shape — are always
// replaced by `***`; known content positions (echo's arguments, here-documents, prompts, scripts,
// request bodies) by `<N bytes>`; in `skeleton` mode every word but a program, its subcommand and
// option names is replaced. A command text is reduced through the shell's own parse
// (mvdan.cc/sh), so operators and redirections stay where they were.
package mask

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"mvdan.cc/sh/v3/syntax"
)

// Mode is how much of a command's typed text is kept.
type Mode string

const (
	// Masked keeps the command's text less its secrets and known content (the default, W-057).
	Masked Mode = "masked"
	// Skeleton keeps a program, its subcommand and option names only.
	Skeleton Mode = "skeleton"
)

// Secret is what a secret is replaced by.
const Secret = "***"

// TextLimit is the most of a free text kept.
const TextLimit = 4096

// longWordBytes is the size from which a word is content whatever its place.
const longWordBytes = 200

// Payload is what content of n bytes is replaced by.
func Payload(size int) string { return fmt.Sprintf("<%d bytes>", size) }

// Argv reduces an argument list, element by element, as the words of one command.
func Argv(argv []string, mode Mode) []string {
	if len(argv) == 0 {
		return nil
	}
	words := make([]word, len(argv))
	for index, value := range argv {
		words[index] = word{value: value, known: true}
	}
	actions := classify(words, mode)
	reduced := make([]string, len(argv))
	for index, value := range argv {
		reduced[index] = sweep(actions[index].apply(value, mode))
	}
	return reduced
}

// sweep is the mask's last word on what it writes, whatever syntax brought it there: every token
// of the reduced text shaped like a secret is replaced whole. The rules before it read the input
// (options, positions, assignments) and can be outrun by a form they do not know; this reads the
// output. What it cannot see is a short secret with no
// shape in a place no rule names — the threat model of section 7.
func sweep(text string) string {
	return compiled().token.ReplaceAllStringFunc(text, func(token string) string {
		if token != Secret && secretShaped(token) {
			return Secret
		}
		return token
	})
}

// Command reduces a command text through its parse; a text that does not parse is replaced whole.
func Command(text string, mode Mode) string {
	file, err := parse(text)
	if err != nil {
		return fmt.Sprintf("<unparsed %d bytes>", len(text))
	}
	return sweep(reduce(text, file, mode))
}

func reduce(text string, file *syntax.File, mode Mode) string {
	var edits []edit
	syntax.Walk(file, func(node syntax.Node) bool {
		switch typed := node.(type) {
		case *syntax.CallExpr:
			edits = append(edits, callEdits(text, typed, mode)...)
		case *syntax.Assign:
			// Every assignment, wherever the parse puts it (before a command, `export`, `local`,
			// `declare`, alone): one rule, by its name and its value.
			edits = append(edits, assignEdits(text, typed, mode)...)
		case *syntax.Redirect:
			if typed.Hdoc != nil {
				edits = append(edits, payloadEdit(text, typed.Hdoc))
			}
			if typed.Op == syntax.WordHdoc && typed.Word != nil {
				edits = append(edits, payloadEdit(text, typed.Word))
			}
		}
		return true
	})
	return applyEdits(text, edits)
}

func parse(text string) (*syntax.File, error) {
	var firstErr error
	for _, language := range []syntax.LangVariant{syntax.LangBash, syntax.LangZsh, syntax.LangPOSIX} {
		file, err := syntax.NewParser(syntax.Variant(language)).Parse(strings.NewReader(text), "")
		if err == nil {
			return file, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// An edit replaces text[start:end].
type edit struct {
	start, end int
	with       string
}

func payloadEdit(text string, node syntax.Node) edit {
	start, end := int(node.Pos().Offset()), int(node.End().Offset())
	end = min(end, len(text))
	return edit{start: start, end: end, with: Payload(end - start)}
}

// applyEdits makes the edits; one inside another is dropped (the outer one already replaced it).
func applyEdits(text string, edits []edit) string {
	sort.SliceStable(edits, func(left, right int) bool {
		if edits[left].start != edits[right].start {
			return edits[left].start < edits[right].start
		}
		return edits[left].end > edits[right].end
	})
	var builder strings.Builder
	done := 0
	for _, change := range edits {
		if change.start < done || change.start > len(text) {
			continue
		}
		builder.WriteString(text[done:change.start])
		builder.WriteString(change.with)
		done = change.end
	}
	builder.WriteString(text[done:])
	return builder.String()
}

func assignEdits(text string, assign *syntax.Assign, mode Mode) []edit {
	if assign.Name == nil {
		return nil
	}
	if assign.Array != nil || assign.Index != nil {
		// NAME=(…): an array is written as `***` whole, whatever it holds — its keys and elements
		// can name a secret or carry a command in any form.
		if assign.Array != nil {
			return []edit{nodeEdit(text, assign.Array, Secret)}
		}
		if assign.Value != nil {
			// NAME[KEY]=value: an element of an array is written as one is.
			return []edit{nodeEdit(text, assign.Value, Secret)}
		}
		return nil
	}
	if assign.Value == nil {
		return nil
	}
	value := wordOf(assign.Value)
	original := text[int(assign.Value.Pos().Offset()):min(int(assign.Value.End().Offset()), len(text))]
	switch {
	case secretName(assign.Name.Value) || secretShaped(value.value) || secretShaped(original):
		return []edit{nodeEdit(text, assign.Value, Secret)}
	case mode == Skeleton || len(value.value) > longWordBytes || strings.Contains(value.value, "\n"):
		return []edit{nodeEdit(text, assign.Value, Payload(len(original)))}
	}
	return nil
}

func callEdits(text string, call *syntax.CallExpr, mode Mode) []edit {
	var edits []edit
	words := make([]word, len(call.Args))
	for index, argument := range call.Args {
		words[index] = wordOf(argument)
	}
	actions := classify(words, mode)
	for index, argument := range call.Args {
		action := actions[index]
		if action.kind == keep {
			continue
		}
		if action.kind == nested {
			// sh -c TEXT: TEXT reduced as a command of its own, put back quoted.
			reduced := Command(words[index].value, mode)
			edits = append(edits, nodeEdit(text, argument, "'"+strings.ReplaceAll(reduced, "'", `'\''`)+"'"))
			continue
		}
		original := text[int(argument.Pos().Offset()):min(int(argument.End().Offset()), len(text))]
		if (action.kind == secretValue || action.kind == contentValue) && words[index].known {
			// The prefix kept is counted in the unquoted word: written back unquoted.
			original = words[index].value
		}
		edits = append(edits, nodeEdit(text, argument, action.apply(original, mode)))
	}
	return edits
}

func nodeEdit(text string, node syntax.Node, with string) edit {
	return edit{start: int(node.Pos().Offset()), end: min(int(node.End().Offset()), len(text)), with: with}
}

// A word is an argument's value, when the parse can say it (no expansion in it).
type word struct {
	value string
	known bool
}

func wordOf(argument *syntax.Word) word {
	var builder strings.Builder
	known := true
	var visit func(parts []syntax.WordPart)
	visit = func(parts []syntax.WordPart) {
		for _, part := range parts {
			switch typed := part.(type) {
			case *syntax.Lit:
				builder.WriteString(typed.Value)
			case *syntax.SglQuoted:
				builder.WriteString(typed.Value)
			case *syntax.DblQuoted:
				visit(typed.Parts)
			default:
				known = false
			}
		}
	}
	visit(argument.Parts)
	return word{value: builder.String(), known: known}
}

// --- classification of one command's words ----------------------------------------------------

type actionKind int

const (
	keep actionKind = iota
	secret
	content
	// secretValue keeps an `--option=` or `NAME=` prefix and replaces what follows it.
	secretValue
	// contentValue keeps an `--option=` prefix and replaces what follows it with its length.
	contentValue
	nested
)

type action struct {
	kind   actionKind
	prefix int // for the *Value kinds: how many bytes of the word are kept
}

func (taken action) apply(original string, mode Mode) string {
	switch taken.kind {
	case secret:
		return Secret
	case content:
		return Payload(len(original))
	case secretValue:
		return original[:taken.prefix] + Secret
	case contentValue:
		return original[:taken.prefix] + Payload(len(original)-taken.prefix)
	case nested:
		return Command(original, mode)
	}
	return original
}

// classify decides each word of one command (its argv), wrappers unwrapped.
func classify(words []word, mode Mode) []action {
	actions := make([]action, len(words))
	classifyFrom(words, 0, mode, actions)
	return actions
}

func classifyFrom(words []word, start int, mode Mode, actions []action) {
	index := start
	// Leading NAME=value words (an argv given to env, or after a wrapper).
	for index < len(words) && isAssignment(words[index].value) {
		decideAssignment(words[index], index, mode, actions)
		index++
	}
	if index >= len(words) {
		return
	}
	if strings.HasPrefix(words[index].value, "-") {
		// Arguments without their program (a command's own arguments): decided as arguments.
		decideArguments("", "", words, index, mode, actions)
		return
	}
	program := path.Base(words[index].value)
	switch {
	case secretShaped(words[index].value):
		actions[index] = action{kind: secret}
	case mode == Masked && words[index].known && len(words[index].value) <= longWordBytes && !strings.Contains(words[index].value, "\n"):
		// A program's path is the command's own, not content (masked mode keeps it).
	case !words[index].known || !structural(words[index].value):
		actions[index] = action{kind: content}
	}
	index++
	if rest, ok := unwrap(program, words, index, mode, actions); ok {
		if rest >= 0 {
			classifyFrom(words, rest, mode, actions)
		}
		return
	}
	if subcommands[program] && index < len(words) && !strings.HasPrefix(words[index].value, "-") {
		if !words[index].known || !structural(words[index].value) {
			actions[index] = action{kind: content}
		}
		index++
	}
	subcommand := ""
	if index-1 > start && subcommands[program] {
		subcommand = words[index-1].value
	}
	decideArguments(program, subcommand, words, index, mode, actions)
}

func isAssignment(value string) bool {
	name, _, ok := strings.Cut(value, "=")
	return ok && compiled().assignmentName.MatchString(name)
}

func decideAssignment(given word, index int, mode Mode, actions []action) {
	name, value, _ := strings.Cut(given.value, "=")
	switch {
	case secretName(name) || secretShaped(value):
		actions[index] = action{kind: secretValue, prefix: len(name) + 1}
	case mode == Skeleton || len(value) > longWordBytes || strings.Contains(value, "\n"):
		actions[index] = action{kind: contentValue, prefix: len(name) + 1}
	}
}

// unwrap handles a wrapper: its own options are decided here and the command it runs starts at the
// returned index (-1: nothing more to classify). false when program is not a wrapper.
func unwrap(program string, words []word, index int, mode Mode, actions []action) (int, bool) {
	switch program {
	case "env":
		for index < len(words) {
			value := words[index].value
			switch {
			case value == "-u" || value == "-C" || value == "-S":
				index += 2
			case strings.HasPrefix(value, "-"):
				index++
			case isAssignment(value):
				decideAssignment(words[index], index, mode, actions)
				index++
			default:
				return index, true
			}
		}
		return -1, true
	case "nohup", "time", "nice", "exec", "command", "sudo", "caffeinate":
		for index < len(words) && strings.HasPrefix(words[index].value, "-") {
			if (program == "nice" && words[index].value == "-n") || (program == "sudo" && words[index].value == "-u") {
				index++
			}
			index++
		}
		return index, true
	case "timeout", "gtimeout":
		for index < len(words) && strings.HasPrefix(words[index].value, "-") {
			index++
		}
		return index + 1, true // the duration, then the command
	case "xargs":
		for index < len(words) && strings.HasPrefix(words[index].value, "-") {
			if xargsValued[words[index].value] {
				index++
			}
			index++
		}
		return index, true
	case "sh", "bash", "zsh", "dash":
		for scan := index; scan < len(words); scan++ {
			if words[scan].value == "-c" && scan+1 < len(words) {
				if words[scan+1].known {
					actions[scan+1] = action{kind: nested}
				} else {
					actions[scan+1] = action{kind: content}
				}
				for after := scan + 2; after < len(words); after++ {
					decideWord(words[after], after, mode, actions)
				}
				return -1, true
			}
		}
		return 0, false
	case "jst", "jackstay":
		if index < len(words) && (words[index].value == "bg" || words[index].value == "test" || words[index].value == "lock") {
			for scan := index + 1; scan < len(words); scan++ {
				if words[scan].value == "--" {
					for before := index + 1; before < scan; before++ {
						decideWord(words[before], before, mode, actions)
					}
					return scan + 1, true
				}
			}
		}
	}
	return 0, false
}

var xargsValued = map[string]bool{"-I": true, "-n": true, "-P": true, "-L": true, "-d": true, "-E": true, "-s": true, "-a": true, "-J": true, "-R": true}

// subcommands are the programs whose second word is a subcommand (kept in skeleton mode).
var subcommands = map[string]bool{"git": true, "gh": true, "go": true, "just": true, "jst": true, "jackstay": true, "npm": true,
	"pnpm": true, "yarn": true, "cargo": true, "docker": true, "podman": true, "kubectl": true, "swift": true,
	"xcodebuild": true, "codex": true, "brew": true, "uv": true, "pip": true, "pip3": true}

// agents are the programs whose positional words are prompts.
var agents = map[string]bool{"codex": true, "claude": true, "llm": true, "aider": true}

// decideArguments decides the words after a program (and its subcommand).
func decideArguments(program, subcommand string, words []word, index int, mode Mode, actions []action) {
	if mode == Skeleton || namesSecret(program, subcommand, words[index:]) {
		strictArguments(program, subcommand, words, index, actions)
		return
	}
	scriptTaken := false
	for index < len(words) {
		current := words[index]
		value := current.value
		name, _, _ := optionParts(value)
		next := index + 1
		hasNext := next < len(words)
		if value == "--" {
			index++
			continue
		}
		if taken, consumed := optionsOf(program, subcommand).decide(value); taken.kind != keep || consumed != keep {
			actions[index] = taken
			if consumed != keep && hasNext {
				actions[next] = action{kind: consumed}
				index++
			}
			index++
			continue
		}
		switch {
		case program == "sed" && value == "-i" && hasNext && (words[next].value == "" || strings.HasPrefix(words[next].value, ".")):
			index++ // macOS sed's backup suffix
		case program == "git" && value == "-c" && hasNext:
			if key, _, ok := strings.Cut(words[next].value, "="); ok && gitSecretKey(key) {
				actions[next] = action{kind: secretValue, prefix: len(key) + 1}
			}
			index++
		case name != "":
			decideWord(current, index, mode, actions)
		default:
			switch {
			case program == "echo" || program == "printf":
				actions[index] = action{kind: content}
			case agents[program] && !scriptTaken:
				actions[index] = action{kind: content}
			case scriptPrograms[program] && !scriptTaken && !scriptGivenByOption(program, words[:index]):
				actions[index] = action{kind: content}
				scriptTaken = true
			default:
				decideWord(current, index, mode, actions)
			}
		}
		index++
	}
}

// namesSecret says a command names a secret somewhere: a program whose options carry credentials,
// or a word that is a secret-named option or assignment. Such a command is written strictly
// (strictArguments): finding where its secret lies in arbitrary syntax cannot be done reliably;
// knowing that it names one is easy.
func namesSecret(program, subcommand string, words []word) bool {
	known := optionsOf(program, subcommand)
	if known.secretShort != "" || len(known.secretLong) > 0 || len(known.secretSingle) > 0 {
		return true
	}
	for _, given := range words {
		if wordNamesSecret(given.value) {
			return true
		}
	}
	return false
}

// wordNamesSecret is `--password…`, `-storepass`, `--api-token=…` or `API_KEY=…`.
func wordNamesSecret(value string) bool {
	if strings.HasPrefix(value, "-") {
		name, _, _ := strings.Cut(value, "=")
		return secretName(name)
	}
	if isAssignment(value) {
		name, _, _ := strings.Cut(value, "=")
		return secretName(name)
	}
	return false
}

// strictArguments keeps of a command's arguments only their structure: a long option's name
// (its value replaced), a two-character short option; every other word — a short-option cluster,
// a value, a positional word — is replaced. In a command that names a secret, its value goes as
// `***`; in skeleton mode, as its length.
func strictArguments(program, subcommand string, words []word, index int, actions []action) {
	replaced := content
	if namesSecret(program, subcommand, words[index:]) {
		replaced = secret
	}
	for ; index < len(words); index++ {
		value := words[index].value
		switch {
		case value == "--":
		case replaced == secret && words[index].known && plainURL(value):
			// A URL with no user information (the sweep would catch one with a shape) is where the
			// command went, not a secret.
		case strings.HasPrefix(value, "--") && strings.Contains(value, "=") && words[index].known:
			if name, _, hasValue := strings.Cut(value, "="); hasValue {
				kind := contentValue
				if replaced == secret {
					kind = secretValue
				}
				actions[index] = action{kind: kind, prefix: len(name) + 1}
			}
		case strings.HasPrefix(value, "--") && words[index].known:
		case len(value) == 2 && value[0] == '-' && words[index].known:
		case isAssignment(value) && words[index].known && replaced != secret:
			// In skeleton mode, NAME=value keeps its name; in a command that names a secret a word
			// that looks like one is replaced whole (a password can hold `=`).
			name, _, _ := strings.Cut(value, "=")
			actions[index] = action{kind: contentValue, prefix: len(name) + 1}
		default:
			actions[index] = action{kind: replaced}
		}
	}
}

// plainURL is scheme://host… with no user information and no secret shape.
func plainURL(value string) bool {
	scheme, rest, found := strings.Cut(value, "://")
	return found && scheme != "" && !strings.ContainsAny(scheme, "/=") && !strings.Contains(strings.SplitN(rest, "/", 2)[0], "@") &&
		!secretShaped(value)
}

// scriptPrograms take their script as the first positional word unless an option gave it.
var scriptPrograms = map[string]bool{"sed": true, "awk": true, "gawk": true, "jq": true}

func scriptGivenByOption(program string, before []word) bool {
	for _, earlier := range before {
		if earlier.value == "-e" || earlier.value == "-f" || (program == "awk" && earlier.value == "-f") {
			return true
		}
	}
	return false
}

// decideWord decides a word no position named: a secret by shape, content by size or mode.
func decideWord(given word, index int, mode Mode, actions []action) {
	if actions[index].kind != keep {
		return
	}
	value := given.value
	name, attached, hasValue := optionParts(value)
	switch {
	case secretShaped(value):
		if hasValue && !secretShaped(name) {
			actions[index] = action{kind: secretValue, prefix: len(value) - len(attached)}
		} else {
			actions[index] = action{kind: secret}
		}
	case isAssignment(value):
		decideAssignment(given, index, mode, actions)
	case len(value) > longWordBytes || strings.Contains(value, "\n"):
		actions[index] = action{kind: content}
	case mode == Skeleton && name != "" && hasValue:
		actions[index] = action{kind: contentValue, prefix: len(value) - len(attached)}
	case mode == Skeleton && name == "":
		actions[index] = action{kind: content}
	case !given.known && mode == Skeleton:
		actions[index] = action{kind: content}
	}
}

// optionParts splits `--name=value` (name "--name", value "value", true) or `-x` (name "-x").
func optionParts(value string) (name, attached string, hasValue bool) {
	if !strings.HasPrefix(value, "-") || value == "-" || value == "--" {
		return "", "", false
	}
	if name, attached, ok := strings.Cut(value, "="); ok {
		return name, attached, true
	}
	return value, "", false
}

// secretName says a name (an option's or a variable's) names a secret.
func secretName(name string) bool {
	plain := strings.NewReplacer("-", "", "_", "").Replace(strings.TrimLeft(name, "-"))
	return compiled().secretWords.MatchString(plain)
}

func gitSecretKey(key string) bool {
	lower := strings.ToLower(key)
	return strings.Contains(lower, "extraheader") || strings.HasPrefix(lower, "credential.") || secretName(key)
}

// --- options -----------------------------------------------------------------------------------

// options is what the mask knows of a program's options: which carry a secret or content as their
// value. A short-option word is read as getopt reads a cluster (`-suUSER:PW` is -s, then -u with
// the value USER:PW), from the left: everything after the first letter whose value is a secret (or
// content) is that value, else the next word is. A letter the mask does not know is taken for a
// flag, so a value-taking option it does not know can only make it mask more, never less.
type options struct {
	secretShort, contentShort string
	secretLong, contentLong   []string // exact names, or prefixes ending in `*`
	secretSingle              []string // single-dash long options (`openssl -passin`)
}

func optionsOf(program, subcommand string) options {
	switch program {
	case "curl", "http", "https", "xh":
		return options{secretShort: "uHbE", secretLong: []string{"--user", "--header", "--cookie", "--oauth2-bearer", "--cert", "--proxy-user"},
			contentShort: "dF", contentLong: []string{"--data*", "--form*", "--json", "--url-query"}}
	case "wget":
		return options{secretLong: []string{"--user", "--header", "--http-user", "--ftp-user"}, contentLong: []string{"--post-data", "--body-data"}}
	case "sshpass", "mysql", "mysqladmin", "mysqldump":
		return options{secretShort: "p"}
	case "docker", "podman", "helm":
		// Login can follow global options (`docker --config D login -p X`): every command of these
		// programs is taken to carry credentials, wherever its subcommand stands.
		return options{secretShort: "p", secretLong: []string{"--password"}}
	case "openssl":
		return options{secretSingle: []string{"-passin", "-passout", "-pass", "-k", "-K"}}
	case "git":
		switch subcommand {
		case "commit", "tag", "notes", "stash":
			return options{contentShort: "mF", contentLong: []string{"--message", "--file"}}
		}
	case "gh":
		return options{contentShort: "bt", contentLong: []string{"--body", "--title", "--notes"}}
	case "sed":
		return options{contentShort: "e", contentLong: []string{"--expression"}}
	case "perl", "ruby", "node":
		return options{contentShort: "eE", contentLong: []string{"--eval"}}
	case "python", "python3":
		return options{contentShort: "c"}
	case "claude":
		return options{contentShort: "p", contentLong: []string{"--print", "--append-system-prompt", "--system-prompt"}}
	}
	return options{}
}

func matchesLong(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name || strings.HasSuffix(candidate, "*") && strings.HasPrefix(name, strings.TrimSuffix(candidate, "*")) {
			return true
		}
	}
	return false
}

// decide is what an option word is (its own action) and what the next word is (keep when it is
// not the option's value).
func (known options) decide(value string) (action, actionKind) {
	if !strings.HasPrefix(value, "-") || value == "-" {
		return action{}, keep
	}
	for _, single := range known.secretSingle {
		if value == single {
			return action{}, secret
		}
	}
	if strings.HasPrefix(value, "--") {
		name, attached, hasValue := strings.Cut(value, "=")
		kind := keep
		switch {
		case secretName(name) || matchesLong(known.secretLong, name):
			kind = secret
		case matchesLong(known.contentLong, name):
			kind = content
		}
		switch {
		case kind == keep:
			return action{}, keep
		case hasValue && kind == secret:
			return action{kind: secretValue, prefix: len(value) - len(attached)}, keep
		case hasValue:
			return action{kind: contentValue, prefix: len(value) - len(attached)}, keep
		}
		return action{}, kind
	}
	for position := 1; position < len(value); position++ {
		letter := value[position]
		kind := keep
		switch {
		case strings.IndexByte(known.secretShort, letter) >= 0:
			kind = secret
		case strings.IndexByte(known.contentShort, letter) >= 0:
			kind = content
		}
		if kind == keep {
			continue
		}
		if position+1 == len(value) {
			return action{}, kind // the value is the next word
		}
		if kind == secret {
			return action{kind: secretValue, prefix: position + 1}, keep
		}
		return action{kind: contentValue, prefix: position + 1}, keep
	}
	return action{}, keep
}

// structural is a word the journal can keep as it is in any mode: short, one token, no secret.
func structural(value string) bool {
	return len(value) > 0 && len(value) <= 64 && compiled().structuralWord.MatchString(value) && !secretShaped(value)
}

// patternSet holds the masks' patterns, compiled on first use: every jackstay process loads this
// package, and only one that writes a span needs them (the guard's budget, section 8).
type patternSet struct {
	token                                                       *regexp.Regexp
	assignmentName, secretWords, structuralWord, candidateRun   *regexp.Regexp
	headerValue, schemeValue, assignedValue, userInfo, pemBlock *regexp.Regexp
	shapes                                                      []*regexp.Regexp
}

var compiled = sync.OnceValue(func() *patternSet {
	return &patternSet{
		token:          regexp.MustCompile(`\S+`),
		assignmentName: regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`),
		secretWords:    regexp.MustCompile(`(?i)token|secret|passw|pass|key|auth|credential|cookie`),
		structuralWord: regexp.MustCompile(`^[A-Za-z0-9._/~:@%+,=-]+$`),
		candidateRun:   regexp.MustCompile(`[A-Za-z0-9+/_=-]{24,}`),
		headerValue:    regexp.MustCompile(`(?i)\b((?:proxy-)?authorization|cookie|set-cookie|x-api-key|api-key)\s*[:=]\s*[^\s,;]+(?:\s+[^\s,;]+)?`),
		schemeValue:    regexp.MustCompile(`(?i)\b(bearer|basic|token)\s+[A-Za-z0-9._~+/=-]{6,}`),
		assignedValue:  regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)=("[^"]*"|'[^']*'|\S+)`),
		userInfo:       regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@]*@`),
		pemBlock:       regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
		shapes: []*regexp.Regexp{
			regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/\s@]*@`),                          // URL with user information
			regexp.MustCompile(`(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{8,}`),                      // GitHub tokens
			regexp.MustCompile(`github_pat_[A-Za-z0-9_]{8,}`),                                  // GitHub fine-grained
			regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`),                                       // OpenAI, Anthropic
			regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{8,}`),                                // Slack
			regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                         // AWS access key
			regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{8,}`),                                    // GitLab
			regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`), // JWT
			regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),                           // PEM
		},
	}
})

// --- shapes ----------------------------------------------------------------------------------

// candidateRun is where a long hex or base64 run may be; longRun judges it.

// longRun says a run is shaped like a key: a part of it (between slashes, so a path is not one)
// of 32 or more hex digits, or of 24 or more base64 characters mixing digits and both cases.
func longRun(run string) bool {
	for _, part := range strings.Split(run, "/") {
		part = strings.TrimRight(part, "=")
		if len(part) >= 32 && strings.Trim(part, "0123456789abcdefABCDEF") == "" {
			return true
		}
		if len(part) >= 24 {
			digits, upper, lower := 0, false, false
			for _, char := range part {
				switch {
				case char >= '0' && char <= '9':
					digits++
				case char >= 'A' && char <= 'Z':
					upper = true
				case char >= 'a' && char <= 'z':
					lower = true
				}
			}
			if digits >= 3 && upper && lower {
				return true
			}
		}
	}
	return false
}

// secretShaped says a text holds something shaped like a secret.
func secretShaped(text string) bool {
	for _, shape := range compiled().shapes {
		if shape.MatchString(text) {
			return true
		}
	}
	for _, run := range compiled().candidateRun.FindAllString(text, -1) {
		if longRun(run) {
			return true
		}
	}
	return false
}

// Text masks a free text by the shapes of secrets and cuts it to TextLimit.
func Text(text string) string {
	text = compiled().pemBlock.ReplaceAllString(text, Secret)
	text = compiled().userInfo.ReplaceAllString(text, "${1}"+Secret+"@")
	text = compiled().headerValue.ReplaceAllString(text, "${1}: "+Secret)
	text = compiled().schemeValue.ReplaceAllString(text, "${1} "+Secret)
	text = compiled().assignedValue.ReplaceAllStringFunc(text, func(found string) string {
		name, _, _ := strings.Cut(found, "=")
		if secretName(name) {
			return name + "=" + Secret
		}
		return found
	})
	for _, shape := range compiled().shapes[1:] {
		text = shape.ReplaceAllString(text, Secret)
	}
	text = compiled().candidateRun.ReplaceAllStringFunc(text, func(run string) string {
		if longRun(run) {
			return Secret
		}
		return run
	})
	if len(text) > TextLimit {
		text = text[:TextLimit] + "…"
	}
	return text
}
