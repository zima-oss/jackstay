// Package cli is the binary's command table (docs/design/implementation.md, section 2). Every command of jackstay is
// a package under internal/ that registers itself here; there is no other implementation to hand a
// command to (W-040). The binary is `jst`, also called as `jackstay`; called as `jst-guard`,
// it is `jst guard`.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/zima-oss/jackstay/internal/telemetry"
	_ "github.com/zima-oss/jackstay/internal/telemetry/genai" // the conversation id on every span of a Claude session
	"github.com/zima-oss/jackstay/internal/version"
)

// Command is one entry of the table.
type Command struct {
	// Name is one word (`guard`) or two (`lint pr`); the longest entry that matches wins.
	Name string
	// Summary is the line `jst help` shows for it.
	Summary string
	// Usage is what `jst <name> --help` prints after the summary (docs/design/adopt.md, section 9):
	// the usage lines, every option with what it does, the exit statuses beyond 0, 1 and 2, and an
	// example.
	Usage string
	// Run runs it with the words after Name; its result is the exit status.
	Run func(args []string, stdin io.Reader, stdout, stderr io.Writer) int
	// PrivateArgs omits the command's arguments from its telemetry span.
	PrivateArgs bool
	// SkipTelemetry keeps a read-only or private command out of the local journal.
	SkipTelemetry bool
}

// commands is the table, filled by Register from each command's package (cmd/jst imports them).
var commands = map[string]Command{}

// Register adds commands to the table. Two commands of one name are a programming error.
func Register(entries ...Command) {
	for _, entry := range entries {
		if _, taken := commands[entry.Name]; taken {
			panic("jst: two commands named " + entry.Name)
		}
		commands[entry.Name] = entry
	}
}

func init() {
	Register(
		Command{Name: "version", Summary: "the version and the commit it was built from", Run: versionCommand,
			Usage: "usage: jst version\n       jst --version (or -V), the same\n\nIt takes no options.\n\nexit: 0 printed; 2 any word after it.\n\nexample: jst --version\n"},
		Command{Name: "help", Summary: "the commands", Run: helpCommand,
			Usage: "usage: jst help                  every command with its summary\n" +
				"       jst help COMMAND [WORD]   the same as `jst COMMAND [WORD] --help`\n\n" +
				"`jst COMMAND --help` (or -h), given as the only word after a command's name, prints\n" +
				"its summary and usage and runs nothing. For the first word of two-word commands\n" +
				"(`jst changes`), it lists those commands.\n\n" +
				"exit: 0 printed; 2 more than two words, or no command of that name (the usage on standard\n" +
				"error).\n\n" +
				"example: jst help test last\n"},
	)
}

// Has says whether a command of exactly that name is in the table.
func Has(name string) bool {
	_, found := commands[name]
	return found
}

// Names are the table's command names, sorted.
func Names() []string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Find is the entry for args and how many words its name took.
func Find(args []string) (entry Command, words int, found bool) {
	if len(args) >= 2 {
		if entry, found = commands[args[0]+" "+args[1]]; found {
			return entry, 2, true
		}
	}
	if len(args) >= 1 {
		if entry, found = commands[args[0]]; found {
			return entry, 1, true
		}
	}
	return Command{}, 0, false
}

// Main runs jackstay with its arguments (argv[0] already dropped) and returns the exit status.
func Main(programPath string, args []string) int {
	if filepath.Base(programPath) == "jst-guard" {
		// The projects' hooks name `jst-guard`: it is `jst guard`.
		args = append([]string{"guard"}, args...)
	}
	args = withCommandWords(args)
	return run(args, func() int { return Dispatch(args, os.Stdin, os.Stdout, os.Stderr) })
}

// withCommandWords is args with the options most tools take in place of a command, given as the first
// word, turned into that command: `--help` or `-h` is `help`, `--version` or `-V` is `version`.
func withCommandWords(args []string) []string {
	if len(args) == 0 {
		return args
	}
	switch args[0] {
	case "-h", "--help":
		return append([]string{"help"}, args[1:]...)
	case "-V", "--version":
		return append([]string{"version"}, args[1:]...)
	}
	return args
}

// run runs the binary's command inside its span (docs/design/observability.md): begun before it
// runs, ended with its exit status; a panic is recorded with its stack and then goes on as it would
// have. Only Main records: Dispatch, which tests call in-process, does not.
func run(args []string, dispatch func() int) (status int) {
	name, rest := "", args
	if entry, words, found := Find(args); found {
		if entry.SkipTelemetry {
			return dispatch()
		}
		name, rest = entry.Name, args[words:]
		if entry.PrivateArgs {
			rest = nil
		}
	} else if len(args) > 0 {
		name, rest = args[0], args[1:]
	}
	if name != "" {
		telemetry.Begin(name, rest)
	}
	defer func() {
		if fault := recover(); fault != nil {
			telemetry.Panicked(fault, debug.Stack())
			panic(fault)
		}
		telemetry.Finish(status)
	}()
	return dispatch()
}

// Dispatch is Main once the program's name is read, with its streams given (tests pass their own).
func Dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return 2
	}
	args = withCommandWords(args)
	if replacement, gone := removedCommand(args); gone {
		fmt.Fprintf(stderr, "jst: %s\n", replacement)
		return 2
	}
	entry, words, found := Find(args)
	if !found {
		if len(args) == 2 && isHelpWord(args[1]) && writeGroup(stdout, args[0]) {
			return 0
		}
		fmt.Fprintf(stderr, "jst: no command %q\n", args[0])
		writeUsage(stderr)
		return 2
	}
	if rest := args[words:]; len(rest) == 1 && isHelpWord(rest[0]) {
		// Asking how to use a command runs nothing of it.
		writeHelp(stdout, entry)
		return 0
	}
	return entry.Run(args[words:], stdin, stdout, stderr)
}

// removed are the commands 0.6.0 took out, with what replaces each (docs/design/land.md section
// 6). A removed command is unknown: the error names its replacement and nothing runs.
var removed = map[string]string{
	"gh":                 "use `jst checks` (wait, status, run-wait) and `jst land` (status, find)",
	"gh pr-land":         "use `jst land` (`--pr` is `--change`, `--dry-run` is `--plan`, `--no-merge` is `--prepare`)",
	"gh pr-status":       "use `jst land status`",
	"gh pr-find":         "use `jst land find`",
	"gh ci-wait":         "use `jst checks wait`",
	"gh ci-status":       "use `jst checks status`",
	"gh run-wait":        "use `jst checks run-wait`",
	"queue":              "use `jst land`, which uses the host's own merge queue where the base requires one",
	"receipts attest-ci": "provenance is verified by `jst release` and the install only",
	"receipts verify-ci": "provenance is verified by `jst release` and the install only",
}

// removedCommand is the message for a removed command's words, longest match first.
func removedCommand(args []string) (string, bool) {
	for words := min(2, len(args)); words > 0; words-- {
		name := strings.Join(args[:words], " ")
		if replacement, gone := removed[name]; gone {
			return fmt.Sprintf("`jst %s` was removed in 0.6.0: %s", name, replacement), true
		}
	}
	return "", false
}

func isHelpWord(word string) bool { return word == "--help" || word == "-h" }

// writeHelp is `jst <name> --help`: the summary, the usage and the two-word commands under it.
func writeHelp(out io.Writer, entry Command) {
	var text strings.Builder
	fmt.Fprintf(&text, "jst %s: %s\n\n", entry.Name, entry.Summary)
	text.WriteString(strings.TrimRight(entry.Usage, "\n") + "\n")
	if under := commandsUnder(entry.Name); len(under) > 0 {
		text.WriteString("\ncommands under it (each answers --help):\n")
		writeList(&text, under)
	}
	_, _ = io.WriteString(out, text.String())
}

// writeGroup lists the two-word commands whose first word is first; false when there are none.
func writeGroup(out io.Writer, first string) bool {
	under := commandsUnder(first)
	if len(under) == 0 {
		return false
	}
	var text strings.Builder
	fmt.Fprintf(&text, "jst %s: these commands (each answers --help)\n\n", first)
	writeList(&text, under)
	fmt.Fprintf(&text, "\nexit: 0 this list; 2 `jst %s` alone, or with a word that is no command under it (nothing\n"+
		"is run); each command's --help names its own.\n", first)
	_, _ = io.WriteString(out, text.String())
	return true
}

// commandsUnder are the two-word commands whose first word is first, sorted.
func commandsUnder(first string) []string {
	var under []string
	for _, name := range Names() {
		if strings.HasPrefix(name, first+" ") {
			under = append(under, name)
		}
	}
	return under
}

func writeList(text *strings.Builder, names []string) {
	width := 0
	for _, name := range names {
		width = max(width, len(name))
	}
	for _, name := range names {
		fmt.Fprintf(text, "  jst %-*s  %s\n", width, name, commands[name].Summary)
	}
}

// Identity is the version and the commit the binary was built from.
func Identity() string {
	return version.Version + " (" + version.BuildCommit() + ")"
}

func versionCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: jst version")
		return 2
	}
	fmt.Fprintln(stdout, "jackstay "+Identity())
	return 0
}

// helpCommand is `jst help`: every command, or `jst help X [Y]`, the same as `jst X [Y] --help`.
func helpCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stdout)
		return 0
	}
	if len(args) > 2 {
		fmt.Fprintln(stderr, "usage: jst help [COMMAND [WORD]]")
		return 2
	}
	entry, words, found := Find(args)
	switch {
	case found && words == len(args):
		writeHelp(stdout, entry)
		return 0
	case len(args) == 1 && writeGroup(stdout, args[0]):
		return 0
	}
	fmt.Fprintf(stderr, "jst help: no command %q\n", strings.Join(args, " "))
	writeUsage(stderr)
	return 2
}

func writeUsage(out io.Writer) {
	names := Names()
	width := 0
	for _, name := range names {
		width = max(width, len(name))
	}
	var text strings.Builder
	text.WriteString("usage: jst COMMAND [ARGS...]\n\n")
	for _, name := range names {
		fmt.Fprintf(&text, "  jst %-*s  %s\n", width, name, commands[name].Summary)
	}
	_, _ = io.WriteString(out, text.String())
}
