// Package owner is `jst owner`: what the owner actually said in a Claude Code session, and with
// --stats where a session's tool calls went (W-006, docs/design/implementation.md section 8.6).
//
// An owner's message reaches a transcript in three shapes, and the obvious extractor silently
// misses two: a plain user message (typed); a queue-operation enqueue record (queued: typed while
// the assistant worked); and the tool_result answering an AskUserQuestion call (widget, in two
// spellings; a declined one is its own kind). Finding nothing is a failure, never an answer.
//
// Exit codes: 0 read (filters that match nothing are still 0, and say so), 1 nothing to read,
// 2 a bad request, 5 the transcript format or location changed, 6 an unknown widget-answer spelling.
package owner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/homedir"
)

const usage = `usage: jst owner [--session S|all] [--kind typed|queued|widget|declined] [--grep RE] [--since D] [--max-chars N]
       jst owner --stats [--session S]

--session is a substring of a session file's name, or "all"; without it, the newest session.
--grep is a case-insensitive regular expression (RE2). --since is an ISO timestamp prefix, e.g. 2026-09-11.`

// Exit codes of the contract.
const (
	exitNothing      = 1
	exitBadRequest   = 2
	exitFormat       = 5
	exitWidgetFormat = 6
)

// pairedWithin is how close after a queued message a typed copy with the same words must be to
// count as the same message (W-006: 22 ms in the one case measured).
const pairedWithin = 5 * time.Second

// Message kinds.
const (
	kindTyped    = "typed"
	kindQueued   = "queued"
	kindWidget   = "widget"
	kindDeclined = "declined"
)

const (
	askTool       = "AskUserQuestion"
	declinedWords = "doesn't want to proceed"
)

var (
	widgetPrefixes  = []string{"The user answered:", "Your questions have been answered:"}
	skipPrefixes    = []string{"<task-notification>", "<command-name>", "<local-command", "Caveat:"}
	reminderPattern = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	widgetTail      = regexp.MustCompile(`(?s)\s*(?:Read the answers carefully.*?what they actually say\.|You can now continue with these answers in mind\.)\s*$`)
	// What the Bash commands of a session went on, by pattern (the patterns overlap).
	bashPatterns = []struct {
		label   string
		pattern *regexp.Regexp
	}{
		{"gh pr checks", regexp.MustCompile(`\bgh pr checks\b`)},
		{"gh pr merge/create/view", regexp.MustCompile(`\bgh pr (merge|create|view)\b`)},
		{"gh run *", regexp.MustCompile(`\bgh run \b`)},
		{"git read", regexp.MustCompile(`\bgit (status|log|diff|show|branch)\b`)},
		{"git write", regexp.MustCompile(`\bgit (commit|add|push|checkout|merge|rebase)\b`)},
		{"python3 heredoc", regexp.MustCompile(`\bpython3\b`)},
		{"waiting (sleep/until)", regexp.MustCompile(`\b(sleep|until)\b`)},
	}
)

// Tests point these at a fixture; they never read the real ~/.claude.
var (
	// configDir is where Claude Code keeps its state: CLAUDE_CONFIG_DIR, else `.claude` in the
	// account's home (never HOME).
	configDir = func() string {
		if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			return dir
		}
		return filepath.Join(homedir.AccountHome(), ".claude")
	}
	// projectRoot is the checkout around the working folder, or the working folder outside git.
	projectRoot = func() string {
		output, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if top := strings.TrimSpace(string(output)); err == nil && top != "" {
			return top
		}
		working, _ := os.Getwd()
		return working
	}
)

func init() {
	cli.Register(cli.Command{Name: "owner", Summary: "what the owner said in a Claude Code session (typed, queued, widget answers); --stats: where its tool calls went",
		Usage: help, Run: run})
}

// failure is a refusal to continue that carries the exit code it wants.
type failure struct {
	message string
	code    int
}

func (problem *failure) Error() string { return problem.message }

func fail(code int, format string, args ...any) error {
	return &failure{message: fmt.Sprintf(format, args...), code: code}
}

type message struct {
	timestamp string
	kind      string
	text      string
}

func (found message) line() string {
	return fmt.Sprintf("[%s] (%s) %s", prefix(found.timestamp, 19), found.kind, found.text)
}

// prefix is the first count characters (not bytes) of text.
func prefix(text string, count int) string {
	if utf8.RuneCountInString(text) <= count {
		return text
	}
	return string([]rune(text)[:count])
}

type request struct {
	stats    bool
	session  string
	kind     string
	grep     string
	since    string
	maxChars int
}

func run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	var asked request
	flags := flag.NewFlagSet("jst owner", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolVar(&asked.stats, "stats", false, "")
	flags.StringVar(&asked.session, "session", "", "")
	flags.StringVar(&asked.kind, "kind", "", "")
	flags.StringVar(&asked.grep, "grep", "", "")
	flags.StringVar(&asked.since, "since", "", "")
	flags.IntVar(&asked.maxChars, "max-chars", 0, "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(stdout, usage)
			return 0
		}
		fmt.Fprintf(stderr, "jst owner: %v\n%s\n", err, usage)
		return exitBadRequest
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "jst owner: unexpected argument %q\n%s\n", flags.Arg(0), usage)
		return exitBadRequest
	}
	switch asked.kind {
	case "", kindTyped, kindQueued, kindWidget, kindDeclined:
	default:
		fmt.Fprintf(stderr, "jst owner: --kind must be typed, queued, widget or declined, not %q\n", asked.kind)
		return exitBadRequest
	}
	if asked.maxChars < 0 {
		fmt.Fprintln(stderr, "jst owner: --max-chars is a count of characters, not negative")
		return exitBadRequest
	}
	if asked.stats && (asked.kind != "" || asked.grep != "" || asked.since != "" || asked.maxChars != 0) {
		fmt.Fprintln(stderr, "jst owner: --stats takes only --session")
		return exitBadRequest
	}
	var pattern *regexp.Regexp
	if asked.grep != "" {
		var err error
		if pattern, err = regexp.Compile("(?i)" + asked.grep); err != nil {
			fmt.Fprintf(stderr, "jst owner: --grep is not a regular expression (RE2): %v\n", err)
			return exitBadRequest
		}
	}
	root, config := projectRoot(), configDir()
	var err error
	if asked.stats {
		err = stats(root, config, asked.session, stdout)
	} else {
		err = show(root, config, asked, pattern, stdout)
	}
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "jst owner: %v\n", err)
	var refusal *failure
	if errors.As(err, &refusal) {
		return refusal.code
	}
	return exitNothing
}

// --- where the transcripts are ---

// mangle replaces every character that is not an ASCII letter or digit with `-` (measured on this
// Mac, 2026-11-15: `/`, `_` and `.` all become `-`).
func mangle(path string) string {
	var mangled strings.Builder
	for _, character := range path {
		if character < 128 && (character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9') {
			mangled.WriteRune(character)
		} else {
			mangled.WriteByte('-')
		}
	}
	return mangled.String()
}

func realPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

func transcriptDir(root, config string) string {
	return filepath.Join(config, "projects", mangle(realPath(root)))
}

const (
	statePresent = "present"
	stateDrifted = "drifted"
	stateNone    = "none"
)

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// sessionDirState is "present" when the derived folder exists; "drifted" (with the folder names)
// when it does not but folders carrying this checkout's parent and name under another mangling do:
// the layout changed; "none" when nothing resembles this checkout. Two path components, not one: a
// basename alone made `mail` a lookalike of any `<name>-mail` project.
func sessionDirState(root, config string) (state, folder string, lookalikes []string) {
	folder = transcriptDir(root, config)
	if isDir(folder) {
		return statePresent, folder, nil
	}
	var parts []string
	for _, part := range strings.Split(realPath(root), string(filepath.Separator)) {
		if part != "" {
			parts = append(parts, mangle(part))
		}
	}
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	suffix := "-" + strings.Join(parts, "-")
	parent := filepath.Dir(folder)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return stateNone, folder, nil
	}
	for _, entry := range entries {
		name := entry.Name()
		if isDir(filepath.Join(parent, name)) && strings.HasSuffix(mangle(name), suffix) && name != filepath.Base(folder) {
			lookalikes = append(lookalikes, name)
		}
	}
	sort.Strings(lookalikes)
	if len(lookalikes) > 0 {
		return stateDrifted, folder, lookalikes
	}
	return stateNone, folder, nil
}

// transcripts are the session files, newest first. A missing folder is an error, not an empty
// list: "no sessions" and "looking in the wrong place" must not look alike.
func transcripts(root, config string) ([]string, error) {
	state, folder, lookalikes := sessionDirState(root, config)
	switch state {
	case stateDrifted:
		return nil, fail(exitFormat, "no transcript folder at %s, but %s carry this checkout's name under another mangling: Claude Code's storage layout changed and mangle in internal/owner needs updating (or this is a second checkout of the same repository under a parent of the same name)",
			folder, strings.Join(lookalikes, ", "))
	case stateNone:
		return nil, fail(exitNothing, "no transcript folder at %s: no Claude Code session has run in %s", folder, root)
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, err
	}
	type file struct {
		path     string
		modified time.Time
	}
	var files []file
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		files = append(files, file{filepath.Join(folder, entry.Name()), info.ModTime()})
	}
	if len(files) == 0 {
		return nil, fail(exitNothing, "%s exists but holds no .jsonl session files", folder)
	}
	sort.SliceStable(files, func(first, second int) bool { return files[first].modified.After(files[second].modified) })
	paths := make([]string, len(files))
	for index, entry := range files {
		paths[index] = entry.path
	}
	return paths, nil
}

func pick(root, config, session string) ([]string, error) {
	files, err := transcripts(root, config)
	if err != nil {
		return nil, err
	}
	switch session {
	case "":
		return files[:1], nil
	case "all":
		return files, nil
	}
	var matches []string
	for _, path := range files {
		if strings.Contains(filepath.Base(path), session) {
			matches = append(matches, path)
		}
	}
	if len(matches) == 0 {
		return nil, fail(exitNothing, "no session file matching %q in %s", session, transcriptDir(root, config))
	}
	return matches, nil
}

// --- the three shapes ---

// records calls visit with every JSON object line of a transcript and returns how many lines the
// file has (blank and unreadable ones included).
func records(path string, visit func(record map[string]any)) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReader(file)
	lines := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lines++
			var decoded any
			if json.Unmarshal(bytes.TrimSpace(line), &decoded) == nil {
				if record, isObject := decoded.(map[string]any); isObject {
					visit(record)
				}
			}
		}
		if readErr == io.EOF {
			return lines, nil
		}
		if readErr != nil {
			return lines, readErr
		}
	}
}

func object(value any) map[string]any {
	found, _ := value.(map[string]any)
	return found
}

func text(value any) string {
	found, _ := value.(string)
	return found
}

func contentOf(record map[string]any) any { return object(record["message"])["content"] }

// clean strips system reminders; "" when nothing of the owner's is left.
func clean(raw any) string {
	words, isText := raw.(string)
	if !isText {
		return ""
	}
	words = strings.TrimSpace(reminderPattern.ReplaceAllString(words, ""))
	if words == "" || strings.Contains(prefix(words, 200), "This session is being continued") {
		return ""
	}
	for _, skipped := range skipPrefixes {
		if strings.HasPrefix(words, skipped) {
			return ""
		}
	}
	return words
}

// asksIn are the AskUserQuestion calls in one assistant record: call id -> its questions.
func asksIn(record map[string]any) map[string][]string {
	found := map[string][]string{}
	blocks, _ := contentOf(record).([]any)
	for _, raw := range blocks {
		block := object(raw)
		if block["type"] != "tool_use" || block["name"] != askTool || text(block["id"]) == "" {
			continue
		}
		questions := []string{}
		asked, _ := object(block["input"])["questions"].([]any)
		for _, rawQuestion := range asked {
			if question := object(rawQuestion); question != nil {
				questions = append(questions, text(question["question"]))
			}
		}
		found[text(block["id"])] = questions
	}
	return found
}

func hasWidgetPrefix(words string) bool {
	for _, known := range widgetPrefixes {
		if strings.HasPrefix(words, known) {
			return true
		}
	}
	return false
}

// resultText is a tool_result's content as one stripped string.
func resultText(block map[string]any) string {
	switch content := block["content"].(type) {
	case string:
		return strings.TrimSpace(content)
	case []any:
		var parts []string
		for _, raw := range content {
			if item := object(raw); item != nil {
				parts = append(parts, text(item["text"]))
			}
		}
		// Stripped: an image block before the text joins as a leading space.
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	return ""
}

func fromUserRecord(record map[string]any, asks map[string][]string) ([]message, error) {
	var found []message
	timestamp := text(record["timestamp"])
	content := contentOf(record)
	if words, isText := content.(string); isText {
		if cleaned := clean(words); cleaned != "" {
			return []message{{timestamp, kindTyped, cleaned}}, nil
		}
		return nil, nil
	}
	blocks, _ := content.([]any)
	for _, raw := range blocks {
		block := object(raw)
		switch block["type"] {
		case "text":
			if cleaned := clean(block["text"]); cleaned != "" {
				found = append(found, message{timestamp, kindTyped, cleaned})
			}
		case "tool_result":
			words := resultText(block)
			isError, _ := block["is_error"].(bool)
			questions, answersACall := asks[text(block["tool_use_id"])]
			if !answersACall {
				// Not an answer to a call seen in this file. A known lead-in is still read (a
				// resumed session, a renamed tool); anything else is never looked at.
				if hasWidgetPrefix(words) && !isError {
					found = append(found, message{timestamp, kindWidget, strings.TrimSpace(widgetTail.ReplaceAllString(words, ""))})
				}
				continue
			}
			if isError {
				// The owner refusing the question is a decision with no words. Any other error (a
				// validation failure, a hook block) means the owner never saw it: not theirs at all.
				if strings.Contains(words, declinedWords) {
					found = append(found, message{timestamp, kindDeclined, strings.Join(questions, " | ")})
				}
				continue
			}
			if !hasWidgetPrefix(words) {
				return nil, fail(exitWidgetFormat, "the answer to an %s call at %s begins %q, a spelling this tool does not know. Teach widgetPrefixes in internal/owner that lead-in: reading on without it would drop the owner's answers silently",
					askTool, prefix(timestamp, 19), prefix(words, 80))
			}
			found = append(found, message{timestamp, kindWidget, strings.TrimSpace(widgetTail.ReplaceAllString(words, ""))})
		}
	}
	return found, nil
}

// secondsBetween is the time from one ISO timestamp to another; false when either cannot be read.
func secondsBetween(earlier, later string) (time.Duration, bool) {
	const layout = "2006-01-02T15:04:05.000"
	first, firstErr := time.Parse(layout, strings.TrimRight(prefix(earlier, 23), "Z"))
	second, secondErr := time.Parse(layout, strings.TrimRight(prefix(later, 23), "Z"))
	if firstErr != nil || secondErr != nil {
		return 0, false
	}
	return second.Sub(first), true
}

// read is every owner message of one transcript, in time order, all three shapes.
func read(path string) ([]message, error) {
	var found []message
	seenQueued := map[[2]string]bool{}
	seenAnswers := map[message]bool{}
	asks := map[string][]string{}
	var firstErr error
	lines, err := records(path, func(record map[string]any) {
		if firstErr != nil {
			return
		}
		switch record["type"] {
		case "assistant":
			for id, questions := range asksIn(record) {
				asks[id] = questions
			}
		case "user":
			if meta, _ := record["isMeta"].(bool); meta {
				return
			}
			fromUser, err := fromUserRecord(record, asks)
			if err != nil {
				firstErr = err
				return
			}
			for _, one := range fromUser {
				// The same record is sometimes written twice: show it once.
				if one.kind == kindWidget || one.kind == kindDeclined {
					if seenAnswers[one] {
						continue
					}
					seenAnswers[one] = true
				}
				found = append(found, one)
			}
		case "queue-operation":
			if record["operation"] != "enqueue" {
				return
			}
			timestamp, words := text(record["timestamp"]), clean(record["content"])
			if words != "" && !seenQueued[[2]string{timestamp, words}] {
				seenQueued[[2]string{timestamp, words}] = true
				found = append(found, message{timestamp, kindQueued, words})
			}
		}
	})
	if firstErr != nil {
		return nil, firstErr
	}
	if err != nil {
		return nil, err
	}
	if lines == 0 {
		return nil, fail(exitNothing, "%s is empty", path)
	}
	if len(found) == 0 {
		return nil, fail(exitFormat, "parsed %d records from %s and found no owner message in any of the three known shapes. That is almost certainly a changed transcript format, not a silent session: internal/owner is the thing to fix",
			lines, filepath.Base(path))
	}
	return pairQueuedWithTyped(found), nil
}

// pairQueuedWithTyped drops the queued record of a message that was also delivered as typed: the
// same words within pairedWithin, the typed copy after the queued one. Anything further apart is
// two messages; showing one twice is the safe side, losing one is not.
func pairQueuedWithTyped(found []message) []message {
	// At one moment the queued copy comes first.
	sort.SliceStable(found, func(first, second int) bool {
		if found[first].timestamp != found[second].timestamp {
			return found[first].timestamp < found[second].timestamp
		}
		return found[first].kind == kindQueued && found[second].kind != kindQueued
	})
	dropped := map[int]bool{}
	for index, typed := range found {
		if typed.kind != kindTyped {
			continue
		}
		for earlier := index - 1; earlier >= 0; earlier-- {
			other := found[earlier]
			gap, readable := secondsBetween(other.timestamp, typed.timestamp)
			if !readable || gap > pairedWithin {
				break
			}
			if other.kind == kindQueued && other.text == typed.text && !dropped[earlier] {
				dropped[earlier] = true
				break
			}
		}
	}
	kept := make([]message, 0, len(found))
	for index, one := range found {
		if !dropped[index] {
			kept = append(kept, one)
		}
	}
	sort.SliceStable(kept, func(first, second int) bool { return kept[first].timestamp < kept[second].timestamp })
	return kept
}

func show(root, config string, asked request, pattern *regexp.Regexp, stdout io.Writer) error {
	paths, err := pick(root, config, asked.session)
	if err != nil {
		return err
	}
	total := 0
	for _, path := range paths {
		messages, err := read(path)
		if err != nil {
			return err
		}
		var rows []message
		for _, one := range messages {
			if (asked.kind == "" || one.kind == asked.kind) && (asked.since == "" || one.timestamp >= asked.since) &&
				(pattern == nil || pattern.MatchString(one.text)) &&
				(asked.maxChars == 0 || utf8.RuneCountInString(one.text) <= asked.maxChars) {
				rows = append(rows, one)
			}
		}
		fmt.Fprintf(stdout, "=== %s  (%d of %d messages)\n", filepath.Base(path), len(rows), len(messages))
		for _, one := range rows {
			fmt.Fprintln(stdout, one.line())
			fmt.Fprintln(stdout)
		}
		total += len(rows)
	}
	if total == 0 {
		fmt.Fprintln(stdout, "(no message matched the filters — the filters, not the transcript: the transcript parsed)")
	}
	return nil
}

// stats is what a session spent its calls on: the input to any tooling decision.
func stats(root, config, session string, stdout io.Writer) error {
	paths, err := pick(root, config, session)
	if err != nil {
		return err
	}
	for _, path := range paths {
		var names []string // in the order first seen, which breaks ties
		counts := map[string]int{}
		var commands []string
		total := 0
		lines, err := records(path, func(record map[string]any) {
			if record["type"] != "assistant" {
				return
			}
			blocks, _ := contentOf(record).([]any)
			for _, raw := range blocks {
				block := object(raw)
				if block["type"] != "tool_use" {
					continue
				}
				name := text(block["name"])
				if name == "" {
					name = "(unnamed)"
				}
				if counts[name] == 0 {
					names = append(names, name)
				}
				counts[name]++
				total++
				if name == "Bash" {
					commands = append(commands, text(object(block["input"])["command"]))
				}
			}
		})
		if err != nil {
			return err
		}
		if total == 0 {
			return fail(exitFormat, "%s: %d records, no tool calls found — the transcript format has probably changed", filepath.Base(path), lines)
		}
		fmt.Fprintf(stdout, "=== %s  (%d records)\n%d tool calls\n", filepath.Base(path), lines, total)
		sort.SliceStable(names, func(first, second int) bool { return counts[names[first]] > counts[names[second]] })
		for _, name := range names {
			fmt.Fprintf(stdout, "  %5d  %s\n", counts[name], name)
		}
		if len(commands) > 0 {
			fmt.Fprintf(stdout, "\n%d Bash commands, by pattern (overlapping):\n", len(commands))
			for _, entry := range bashPatterns {
				matched := 0
				for _, command := range commands {
					if entry.pattern.MatchString(command) {
						matched++
					}
				}
				if matched > 0 {
					fmt.Fprintf(stdout, "  %5d  %s\n", matched, entry.label)
				}
			}
		}
	}
	return nil
}
