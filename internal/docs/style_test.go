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

// helpTexts reads internal/cli/testdata/help.txt, every command's --help as jst prints it (cmd/jst's
// TestEveryCommandHelp keeps it current): the commands' names, each with its help.
func helpTexts(t *testing.T, root string) map[string]string {
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
	return texts
}

var (
	// commandLine is a line of a code block that runs jst, after an optional prompt.
	commandLine = regexp.MustCompile(`^(\$ )?jst( |$)`)
	// outputLine is a line jst prints ("jst test: outcome=passed"), not a command.
	outputLine = regexp.MustCompile(`^jst [a-z-]+( [a-z-]+)?:( |$)`)
	// inlineCommand is a jst command written as inline code in a sentence.
	inlineCommand = regexp.MustCompile("`(jst [^`]*)`")
	// flagWord is a long flag of a command.
	flagWord = regexp.MustCompile(`(?:^|[\s\[(=])(--[a-z][a-z0-9-]*)`)
	// placeholder is a word a reader must replace: <name>, or a word in capitals (COMMAND, PATH).
	placeholder = regexp.MustCompile(`<[^>]+>|(^|[^$\w])[A-Z][A-Z_]{2,}\b|…`)
	// separator splits a shell line into the commands it runs.
	separator = regexp.MustCompile(`&&|\|\||;|\|`)
)

// notPlaceholders are words in capitals that are values, not names to replace.
var notPlaceholders = []string{"HEAD", "FETCH_HEAD", "ORIG_HEAD", "JSON", "UTC"}

// placeholderIn returns the first placeholder in a command, or "".
func placeholderIn(command string) string {
	for _, found := range placeholder.FindAllString(command, -1) {
		if word := strings.TrimLeftFunc(found, notWordStart); !slices.Contains(notPlaceholders, word) {
			return word
		}
	}
	return ""
}

// notWordStart says whether a rune is the character before a placeholder that the pattern matched.
func notWordStart(r rune) bool { return r != '<' && r != '…' && (r < 'A' || r > 'Z') }

// globalFlags are the flags jst takes in place of a command.
var globalFlags = []string{"--help", "--version"}

// checkCommand returns what is wrong with one jst command (its words after "jst"), or "".
func checkCommand(command string, helps map[string]string, names []string) string {
	command = strings.TrimSpace(command)
	before, _, _ := strings.Cut(command, " -- ") // what follows -- is another program's
	words := strings.Fields(before)
	if len(words) < 2 {
		return ""
	}
	if slices.Contains(globalFlags, words[1]) {
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
	for _, match := range flagWord.FindAllStringSubmatch(before, -1) {
		flag := match[1]
		if slices.Contains(globalFlags, flag) {
			continue
		}
		if !regexp.MustCompile(regexp.QuoteMeta(flag) + `\b`).MatchString(helps[name]) {
			return "jst " + name + " has no " + flag
		}
	}
	return ""
}

// TestCommandExamplesRun: docs/STYLE.md rule 6.
func TestCommandExamplesRun(t *testing.T) {
	root := repoRoot(t)
	helps := helpTexts(t, root)
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
		lines := strings.Split(string(data), "\n")
		kinds := classify(lines)
		for n, line := range lines {
			trimmed := strings.TrimSpace(line)
			if kinds[n] == fenceMarker {
				continue
			}
			code := kinds[n] == runnableCode || kinds[n] == otherCode
			var commands []string
			if code {
				trimmed = strings.TrimPrefix(trimmed, "$ ")
				if !commandLine.MatchString(trimmed) || outputLine.MatchString(trimmed) {
					continue
				}
				shell, _, _ := strings.Cut(trimmed, " #") // a comment
				for _, part := range separator.Split(shell, -1) {
					if part = strings.TrimSpace(part); strings.HasPrefix(part, "jst ") || part == "jst" {
						commands = append(commands, part)
						if found := placeholderIn(part); found != "" && kinds[n] == runnableCode {
							t.Errorf("%s:%d: %q is a placeholder in a code block; write a value that runs (docs/STYLE.md rule 6)", file, n+1, found)
						}
					}
				}
			} else {
				for _, match := range inlineCommand.FindAllStringSubmatch(line, -1) {
					if outputLine.MatchString(match[1]) {
						continue
					}
					for _, part := range separator.Split(match[1], -1) {
						if part = strings.TrimSpace(part); strings.HasPrefix(part, "jst ") {
							commands = append(commands, part)
						}
					}
				}
			}
			for _, command := range commands {
				if problem := checkCommand(command, helps, names); problem != "" {
					t.Errorf("%s:%d: %s: %s (docs/STYLE.md rule 6)", file, n+1, command, problem)
				}
			}
		}
	}
}

func TestCheckCommand(t *testing.T) {
	helps := map[string]string{
		"test":      "usage: jst test [--commit REV] SCOPE\n",
		"test last": "usage: jst test last [--json]\n",
	}
	names := []string{"test last", "test"}
	for command, want := range map[string]string{
		"jst test gate --commit HEAD":   "",
		"jst test last --json":          "",
		"jst test gate --from STEP":     "jst test has no --from",
		"jst land --apply":              "no command jst land",
		"jst COMMAND --help":            "",
		"jst --version":                 "",
		"jst test -- go test --race ./": "",
		"jst test last --commit HEAD":   "jst test last has no --commit",
	} {
		if got := checkCommand(command, helps, names); got != want {
			t.Errorf("checkCommand(%q) = %q, want %q", command, got, want)
		}
	}
}
