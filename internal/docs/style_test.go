package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The checks of docs/STYLE.md that a program can make: no filler in the pages people read, and every
// jst command in them and in the text agents read names a command and flags jst has.

// peoplePages are the documents written for people: the guide, the README and the contributors' page.
func peoplePages(t *testing.T, root string) []string {
	t.Helper()
	var pages []string
	for _, file := range markdownFiles(t, root) {
		if strings.HasPrefix(file, "docs/guide/") || file == "README.md" || file == "CONTRIBUTING.md" {
			pages = append(pages, file)
		}
	}
	return pages
}

// examplePages are the documents whose jst commands must run: the people's pages, this repository's
// AGENTS.md and the AGENTS block's golden files.
func examplePages(t *testing.T, root string) []string {
	t.Helper()
	pages := peoplePages(t, root)
	for _, file := range markdownFiles(t, root) {
		if file == "AGENTS.md" || file == "docs/STYLE.md" || strings.HasPrefix(file, "internal/adopt/block/testdata/") {
			pages = append(pages, file)
		}
	}
	return pages
}

// filler are the words docs/STYLE.md rule 3 cuts. "just" is the word, not the tool: the tool is
// written as code (`just docs`) and code spans are not prose.
var filler = regexp.MustCompile(`(?i)\b(simply|just|basically|obviously|of course|actually|really|very|easily|in order to|note that|it is important to|seamless(ly)?|powerful|robust|leverage[sd]?)\b`)

// codeSpan is inline code, and linkTarget the address of a link: not prose.
var (
	codeSpan   = regexp.MustCompile("`[^`]*`")
	linkTarget = regexp.MustCompile(`\]\([^)]*\)|https?://\S+`)
)

// lineKind is what a line of a Markdown file is to the checks.
type lineKind int

const (
	prose        lineKind = iota
	fenceMarker           // a line opening or closing a fenced block
	runnableCode          // in a fenced block tagged sh, bash, shell, zsh or console: it runs as written
	otherCode             // in another fenced block, or an indented one: a synopsis, output, data
)

var listItem = regexp.MustCompile(`^\s*([-*+]|[0-9]+\.) `)

// classify returns the kind of each line of a Markdown file. An indented line is code only when it
// follows a blank line or other code outside a list (an indented line in a list continues the item).
func classify(lines []string) []lineKind {
	kinds := make([]lineKind, len(lines))
	fence, runnable, inList, previousBlank, previousCode := "", false, false, true, false
	for n, line := range lines {
		trimmed := strings.TrimSpace(line)
		if fence == "" && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
			fence = trimmed[:3]
			switch strings.TrimSpace(strings.TrimLeft(trimmed, fence[:1])) {
			case "sh", "bash", "shell", "zsh", "console":
				runnable = true
			default:
				runnable = false
			}
			kinds[n], previousBlank, previousCode = fenceMarker, false, false
			continue
		}
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence, kinds[n] = "", fenceMarker
				continue
			}
			kinds[n] = otherCode
			if runnable {
				kinds[n] = runnableCode
			}
			continue
		}
		switch {
		case trimmed == "":
			kinds[n] = prose
		case strings.HasPrefix(line, "    ") && !inList && (previousBlank || previousCode):
			kinds[n] = otherCode
		case listItem.MatchString(line):
			inList = true
		case !strings.HasPrefix(line, " "):
			inList = false
		}
		previousCode = kinds[n] == otherCode
		previousBlank = trimmed == ""
	}
	return kinds
}

// proseLines calls visit with each line of a Markdown file that is prose, its inline code and
// link addresses removed.
func proseLines(markdown string, visit func(number int, line string)) {
	lines := strings.Split(markdown, "\n")
	for n, kind := range classify(lines) {
		if kind == prose {
			visit(n+1, linkTarget.ReplaceAllString(codeSpan.ReplaceAllString(lines[n], ""), "]"))
		}
	}
}

func TestNoFiller(t *testing.T) {
	root := repoRoot(t)
	for _, file := range peoplePages(t, root) {
		data, err := os.ReadFile(filepath.Join(root, file)) // #nosec G304 -- a document of this repository
		if err != nil {
			t.Fatal(err)
		}
		proseLines(string(data), func(number int, line string) {
			if found := filler.FindString(line); found != "" {
				t.Errorf("%s:%d: %q is filler (docs/STYLE.md rule 3)", file, number, found)
			}
		})
	}
}

// commandHelp is what one command's --help says it takes.
type commandHelp struct {
	// flags are every long flag the help names.
	flags []string
	// actions are the words a command's usage lines all start with (`jst review pool inspect`), each
	// with the flags of its own usage lines; none when the usage has no such word.
	actions map[string][]string
}

// commandHelps reads internal/cli/testdata/help.txt, every command's --help as jst prints it (cmd/jst's
// TestEveryCommandHelp keeps it current): what each command takes.
func commandHelps(t *testing.T, root string) map[string]commandHelp {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "internal", "cli", "testdata", "help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]string{}
	name := ""
	for line := range strings.Lines(string(data)) {
		if rest, ok := strings.CutPrefix(line, "==> jst "); ok {
			name = strings.TrimSuffix(strings.TrimSpace(rest), " --help")
			continue
		}
		texts[name] += line
	}
	delete(texts, "")
	helps := map[string]commandHelp{}
	for name, text := range texts {
		helps[name] = parseHelp(name, text)
	}
	return helps
}

// parseHelp reads a command's flags and actions from its --help.
func parseHelp(name, text string) commandHelp {
	help := commandHelp{flags: flagNames(text)}
	actions := map[string][]string{}
	usage, every := false, true
	for line := range strings.Lines(text) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "usage: ") {
			usage, trimmed = true, strings.TrimPrefix(trimmed, "usage: ")
		} else if usage && trimmed == "" {
			break
		}
		if !usage {
			continue
		}
		rest, found := strings.CutPrefix(trimmed, "jst "+name+" ")
		if !found {
			continue // a line continuing the one before
		}
		word, _, _ := strings.Cut(rest, " ")
		if !actionWord.MatchString(word) {
			every = false
			continue
		}
		actions[word] = append(actions[word], flagNames(rest)...)
	}
	if every && len(actions) > 1 {
		help.actions = actions
	}
	return help
}

var (
	// actionWord is a literal word of a usage line: an action, not a flag or a placeholder.
	actionWord = regexp.MustCompile(`^[a-z][a-z-]*$`)
	// flagWord is a long flag of a command, whole.
	flagWord = regexp.MustCompile(`(?:^|[\s\[(=|,])(--[a-z][a-z0-9-]*)`)
)

// flagNames are the long flags named in a text, whole.
func flagNames(text string) []string {
	var flags []string
	for _, match := range flagWord.FindAllStringSubmatch(text, -1) {
		flags = append(flags, match[1])
	}
	return flags
}

var (
	// commandLine is a line of a code block that runs jst, after an optional prompt.
	commandLine = regexp.MustCompile(`^(\$ )?jst( |$)`)
	// outputLine is a line jst prints ("jst test: outcome=passed"), not a command.
	outputLine = regexp.MustCompile(`^jst [a-z-]+( [a-z-]+)?:( |$)`)
	// inlineCommand is a jst command written as inline code in a sentence.
	inlineCommand = regexp.MustCompile("`(jst [^`]*)`")
	// placeholder is what a reader must replace or choose: <name>, a word in capitals (NAME, ID),
	// an ellipsis, or an optional part in brackets ([--all]).
	placeholder = regexp.MustCompile(`<[^>]+>|(^|[^$\w])[A-Z][A-Z_]+\b|…|\.\.\.|\[[^\]]*\]`)
)

// notPlaceholders are words in capitals that are values, not names to replace.
var notPlaceholders = []string{"HEAD", "FETCH_HEAD", "ORIG_HEAD", "JSON", "UTC"}

// placeholderIn returns the first placeholder in a command, or "".
func placeholderIn(command string) string {
	for _, found := range placeholder.FindAllString(command, -1) {
		word := strings.TrimLeftFunc(found, notWordStart)
		if trimmed := strings.TrimLeft(word, "."); trimmed != "" {
			word = trimmed // ..HEAD: the dots are a range's, not an ellipsis
		}
		if !slices.Contains(notPlaceholders, word) {
			return word
		}
	}
	return ""
}

// notWordStart says whether a rune is the character before a placeholder that the pattern matched.
func notWordStart(r rune) bool {
	return r != '<' && r != '…' && r != '.' && r != '[' && (r < 'A' || r > 'Z')
}

// splitCommands splits a shell line into the commands it runs, at &&, ||, ; and |, but not at a |
// inside brackets or parentheses, where it separates the choices of a synopsis.
func splitCommands(line string) []string {
	var parts []string
	depth, start := 0, 0
	for index := 0; index < len(line); index++ {
		switch character := line[index]; {
		case character == '[' || character == '(':
			depth++
		case character == ']' || character == ')':
			depth = max(depth-1, 0)
		case depth == 0 && (character == ';' || character == '|' || character == '&' && index+1 < len(line) && line[index+1] == '&'):
			parts = append(parts, line[start:index])
			if index+1 < len(line) && (line[index+1] == '|' || line[index+1] == '&') {
				index++
			}
			start = index + 1
		}
	}
	return append(parts, line[start:])
}

// isRemovedReference says whether a command written in a sentence names, exactly, a command jst
// removed and still answers with its replacement (internal/cli's removed list): a page may name one
// where it says what replaces it. Its words up to the first flag must be the removed name whole, so
// `jst gh pr-land` passes and `jst gh pr-lnad` does not. A code block never gets this pass.
func isRemovedReference(command string, removed []string) bool {
	var words []string
	for _, word := range strings.Fields(command)[1:] {
		if strings.HasPrefix(word, "-") {
			break
		}
		words = append(words, word)
	}
	return slices.Contains(removed, strings.Join(words, " "))
}

// removedEntry is one key of internal/cli's removed list.
var removedEntry = regexp.MustCompile(`(?m)^\t"([a-z][a-z -]*)":\s+"`)

// readRemovedCommands reads the keys of the `removed` map in internal/cli/cli.go.
func readRemovedCommands(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "internal", "cli", "cli.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "var removed = map[string]string{")
	if start < 0 {
		t.Fatal("internal/cli/cli.go has no removed list")
	}
	block, _, _ := strings.Cut(text[start:], "\n}")
	var names []string
	for _, match := range removedEntry.FindAllStringSubmatch(block, -1) {
		names = append(names, match[1])
	}
	return names
}

// namesReplacement says whether a line's commands include one that jst has, besides the removed
// ones: the replacement a migration line gives (`jst gh ci-wait` | `jst checks wait`).
func namesReplacement(commands, removed []string, helps map[string]commandHelp, names []string) bool {
	for _, command := range commands {
		if !isRemovedReference(command, removed) && checkCommand(command, helps, names) == "" {
			return true
		}
	}
	return false
}

// globalFlags are the flags jst takes in place of a command.
var globalFlags = []string{"--help", "--version"}

// checkCommand returns what is wrong with one jst command (its words after "jst"), or "".
func checkCommand(command string, helps map[string]commandHelp, names []string) string {
	command = strings.TrimSpace(command)
	before, _, _ := strings.Cut(command, " -- ") // what follows -- is another program's
	words := strings.Fields(before)
	if len(words) < 2 || slices.Contains(globalFlags, words[1]) {
		return ""
	}
	name := ""
	for _, candidate := range names {
		parts := strings.Fields(candidate)
		if len(words) > len(parts) && slices.Equal(words[1:1+len(parts)], parts) {
			name = candidate
			break
		}
	}
	if name == "" {
		if strings.HasPrefix(words[1], "<") || words[1] == strings.ToUpper(words[1]) {
			return "" // jst COMMAND --help: a placeholder for any command
		}
		return "no command " + strings.Join(words[:2], " ")
	}
	help := helps[name]
	actionFlags := []string(nil)
	if next := 1 + len(strings.Fields(name)); help.actions != nil && next < len(words) && !slices.Contains(globalFlags, words[next]) {
		flags, known := help.actions[words[next]]
		if !known {
			return "jst " + name + " has no action " + words[next]
		}
		actionFlags = flags
	}
	for _, flag := range flagNames(before) {
		switch {
		case slices.Contains(globalFlags, flag):
		case !slices.Contains(help.flags, flag):
			return "jst " + name + " has no " + flag
		case actionFlags != nil && !slices.Contains(actionFlags, flag) && inAnyAction(help.actions, flag):
			return "jst " + name + " " + words[1+len(strings.Fields(name))] + " takes no " + flag
		}
	}
	return ""
}

// inAnyAction says whether a flag belongs to one of a command's actions (rather than to them all).
func inAnyAction(actions map[string][]string, flag string) bool {
	for _, flags := range actions {
		if slices.Contains(flags, flag) {
			return true
		}
	}
	return false
}

// joinContinuations joins each code line ending in a backslash with the lines it continues, so a
// command is checked whole: the joined line keeps the number of its first, and the lines it took in
// are left empty and marked as no code.
func joinContinuations(lines []string, kinds []lineKind) ([]string, []lineKind) {
	joined, joinedKinds := slices.Clone(lines), slices.Clone(kinds)
	for n := 0; n < len(lines); n++ {
		if kinds[n] == prose || kinds[n] == fenceMarker {
			continue
		}
		last := n
		for strings.HasSuffix(strings.TrimSpace(joined[n]), `\`) && last+1 < len(lines) && kinds[last+1] == kinds[n] {
			last++
			joined[n] = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(joined[n]), `\`)) + " " + strings.TrimSpace(lines[last])
			joined[last], joinedKinds[last] = "", fenceMarker
		}
		n = last
	}
	return joined, joinedKinds
}

// TestCommandExamplesRun: docs/STYLE.md rule 6.
func TestCommandExamplesRun(t *testing.T) {
	root := repoRoot(t)
	helps := commandHelps(t, root)
	removed := readRemovedCommands(t, root)
	if !slices.Contains(removed, "gh pr-land") {
		t.Fatalf("the removed list read from internal/cli/cli.go lacks gh pr-land: %v", removed)
	}
	names := make([]string, 0, len(helps))
	for name := range helps {
		names = append(names, name)
	}
	// The longest name first, so `jst test last` is not read as `jst test` with the word last.
	slices.SortFunc(names, func(a, b string) int { return len(strings.Fields(b)) - len(strings.Fields(a)) })
	for _, file := range examplePages(t, root) {
		data, err := os.ReadFile(filepath.Join(root, file)) // #nosec G304 -- a document of this repository
		if err != nil {
			t.Fatal(err)
		}
		original := strings.Split(string(data), "\n")
		lines, kinds := joinContinuations(original, classify(original))
		for n, line := range lines {
			var commands []string
			switch kinds[n] {
			case fenceMarker:
				continue
			case runnableCode, otherCode:
				trimmed := strings.TrimPrefix(strings.TrimSpace(line), "$ ")
				if !commandLine.MatchString(trimmed) || outputLine.MatchString(trimmed) {
					continue
				}
				shell, _, _ := strings.Cut(trimmed, " #") // a comment
				for _, part := range splitCommands(shell) {
					if part = strings.TrimSpace(part); strings.HasPrefix(part, "jst ") || part == "jst" {
						commands = append(commands, part)
						if found := placeholderIn(part); found != "" && kinds[n] == runnableCode {
							t.Errorf("%s:%d: %q is a placeholder in an sh block; write a value that runs, or tag the block text (docs/STYLE.md rule 6)", file, n+1, found)
						}
					}
				}
			default:
				for _, match := range inlineCommand.FindAllStringSubmatch(line, -1) {
					if outputLine.MatchString(match[1]) {
						continue
					}
					for _, part := range splitCommands(match[1]) {
						if part = strings.TrimSpace(part); strings.HasPrefix(part, "jst ") {
							commands = append(commands, part)
						}
					}
				}
			}
			for _, command := range commands {
				if kinds[n] == prose && isRemovedReference(command, removed) && namesReplacement(commands, removed, helps, names) {
					continue
				}
				if problem := checkCommand(command, helps, names); problem != "" {
					t.Errorf("%s:%d: %s: %s (docs/STYLE.md rule 6)", file, n+1, command, problem)
				}
			}
		}
	}
}

func TestCheckCommand(t *testing.T) {
	helps := map[string]commandHelp{
		"test":      parseHelp("test", "usage: jst test [--commit REV] [--no-lock] SCOPE\n"),
		"test last": parseHelp("test last", "usage: jst test last [--json]\n"),
		"review pool": parseHelp("review pool", "usage: jst review pool list [--json]\n"+
			"       jst review pool inspect [--json]\n       jst review pool prune [--apply] [--json]\n\nmore\n"),
	}
	names := []string{"test last", "review pool", "test"}
	for command, want := range map[string]string{
		"jst test gate --commit HEAD":        "",
		"jst test last --json":               "",
		"jst test gate --from STEP":          "jst test has no --from",
		"jst test gate --no":                 "jst test has no --no",
		"jst land --apply":                   "no command jst land",
		"jst COMMAND --help":                 "",
		"jst --version":                      "",
		"jst test -- go test --race ./":      "",
		"jst test last --commit HEAD":        "jst test last has no --commit",
		"jst review pool inspect --json":     "",
		"jst review pool prune --apply":      "",
		"jst review pool inspec":             "jst review pool has no action inspec",
		"jst review pool inspect --apply":    "jst review pool inspect takes no --apply",
		"jst review pool --help":             "",
		"jst test [--no-lock | --lock] gate": "jst test has no --lock",
	} {
		if got := checkCommand(command, helps, names); got != want {
			t.Errorf("checkCommand(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestOnlyAWholeRemovedNameIsAReference(t *testing.T) {
	removed := []string{"gh", "gh pr-land", "queue"}
	for command, want := range map[string]bool{
		"jst gh pr-land":        true,
		"jst gh pr-land --pr 3": true,
		"jst gh":                true,
		"jst queue":             true,
		"jst gh pr-lnad":        false,
		"jst gh foo --x":        false,
		"jst land":              false,
	} {
		if got := isRemovedReference(command, removed); got != want {
			t.Errorf("isRemovedReference(%q) = %v, want %v", command, got, want)
		}
	}
}

func TestARemovedCommandNeedsItsReplacementOnTheLine(t *testing.T) {
	helps := map[string]commandHelp{"land": parseHelp("land", "usage: jst land [--change N]\n")}
	names, removed := []string{"land"}, []string{"gh pr-land"}
	if !namesReplacement([]string{"jst gh pr-land", "jst land --change 3"}, removed, helps, names) {
		t.Error("a migration line naming jst land is refused")
	}
	if namesReplacement([]string{"jst gh pr-land --pr 3"}, removed, helps, names) {
		t.Error("an instruction to run a removed command passes")
	}
}

func TestPlaceholdersAndSplitting(t *testing.T) {
	for command, want := range map[string]string{
		"jst procs stop --all ID":       "ID",
		"jst test [--no-reuse] gate":    "[--no-reuse]",
		"jst test gate ...":             "...",
		"jst test gate --commit HEAD":   "",
		"jst wait --timeout 30 --for X": "",
		"jst log --session NAME":        "NAME",
	} {
		if got := placeholderIn(command); got != want {
			t.Errorf("placeholderIn(%q) = %q, want %q", command, got, want)
		}
	}
	got := splitCommands("jst test [--lock | --no-lock] gate && jst test last | head; jst doctor")
	want := []string{"jst test [--lock | --no-lock] gate ", " jst test last ", " head", " jst doctor"}
	if !slices.Equal(got, want) {
		t.Errorf("splitCommands = %q, want %q", got, want)
	}
	lines := []string{"```sh", `jst receipts verify-ci \`, "  --repositroy x", "```"}
	joined, kinds := joinContinuations(lines, classify(lines))
	if joined[1] != "jst receipts verify-ci --repositroy x" || kinds[2] != fenceMarker {
		t.Errorf("joinContinuations = %q %v", joined, kinds)
	}
}

// commitHash is an abbreviated or full commit hash written as code (`2407f44`): digits and letters
// a-f, at least one of each.
var commitHash = regexp.MustCompile("`([0-9a-f]{7,40})`")

// TestNoCommitHashesInPublicPages: docs/STYLE.md rule 8. A hash names a commit of someone's history,
// which a reader of the guide or the roadmap cannot use; name the release, the pull request or the
// decision instead.
func TestNoCommitHashesInPublicPages(t *testing.T) {
	root := repoRoot(t)
	for _, file := range markdownFiles(t, root) {
		if !strings.HasPrefix(file, "docs/guide/") && !strings.HasPrefix(file, "docs/roadmap/") && file != "README.md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, file)) // #nosec G304 -- a document of this repository
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		for n, kind := range classify(lines) {
			if kind != prose {
				continue
			}
			for _, match := range commitHash.FindAllStringSubmatch(lines[n], -1) {
				if strings.ContainsAny(match[1], "abcdef") && strings.ContainsAny(match[1], "0123456789") {
					t.Errorf("%s:%d: %s looks like a commit hash (docs/STYLE.md rule 8)", file, n+1, match[1])
				}
			}
		}
	}
}
