package baseline

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// What became of a call.
const (
	fateRan        = "ran"
	fateRefused    = "refused"    // a PreToolUse hook denied it: nothing ran
	fateDenied     = "denied"     // another permission layer denied it: nothing ran
	fateUnanswered = "unanswered" // no result at all
)

var (
	notification = regexp.MustCompile(`(?s)<tool-use-id>([^<]+)</tool-use-id>.*?<status>([^<]*)</status>.*?(?:exit code (-?\d+))?</summary>`)
	// How a call that did not run looks in its tool_result (measured on recorded transcripts); the
	// record's own fields are read first.
	hookText   = regexp.MustCompile(`^(?:Error: )?(?:PreToolUse:\S+ hook |jst guard refused)`)
	deniedText = regexp.MustCompile(`^(?:Error: )?(?:Permission for this (?:action|command) was denied|The user doesn't want to proceed|` +
		`The server-side auto mode classifier gave no )`)
	denialKindsNotRun = []string{"permission-rule", "automode-blocked", "automode-unavailable", "user-rejected"}
	stampPattern      = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(\.\d+)?Z`)
)

// A call is one Bash tool call of the transcripts.
type call struct {
	id         string
	at         string // the call's timestamp as written
	command    string
	cwd        string
	background bool
	ended      string // when its result (or, in the background, its notification) came; "" unknown
	failed     bool
	fate       string
}

// calls are the calls in the order first seen.
type calls struct {
	order []string
	byID  map[string]*call
}

type result struct {
	at         string
	isError    bool
	fate       string
	background bool
}

type notice struct {
	at, status, code string
	hasCode          bool
}

// stamp is a transcript time as seconds since the epoch (UTC).
func stamp(text string) (float64, bool) {
	match := stampPattern.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	moment, err := time.ParseInLocation("2006-01-02T15:04:05", match[1], time.UTC)
	if err != nil {
		return 0, false
	}
	seconds := float64(moment.Unix())
	if match[2] != "" {
		var fraction float64
		if json.Unmarshal([]byte("0"+match[2]), &fraction) == nil {
			seconds += fraction
		}
	}
	return seconds, true
}

func dayOf(seconds float64) string {
	return time.Unix(int64(seconds), 0).UTC().Format(time.DateOnly)
}

// truthy is whether a JSON value counts as yes where a field is a flag.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}
	return true
}

func textOf(value any) string {
	text, _ := value.(string)
	return text
}

// matchesGlob says whether a folder name matches a shell pattern (`*`, `?`, `[…]`, `[!…]`).
func matchesGlob(name, glob string) bool {
	var built strings.Builder
	built.WriteString("^")
	for index := 0; index < len(glob); index++ {
		char := glob[index]
		switch char {
		case '*':
			built.WriteString(".*")
		case '?':
			built.WriteString(".")
		case '[':
			end := index + 1
			if end < len(glob) && glob[end] == '!' {
				end++
			}
			if end < len(glob) && glob[end] == ']' {
				end++
			}
			for end < len(glob) && glob[end] != ']' {
				end++
			}
			if end >= len(glob) {
				built.WriteString(`\[`)
				continue
			}
			inside := glob[index+1 : end]
			if strings.HasPrefix(inside, "!") {
				inside = "^" + inside[1:]
			} else if strings.HasPrefix(inside, "^") {
				inside = `\` + inside
			}
			built.WriteString("[" + strings.ReplaceAll(inside, `\`, `\\`) + "]")
			index = end
		default:
			built.WriteString(regexp.QuoteMeta(string(char)))
		}
	}
	built.WriteString("$")
	pattern, err := regexp.Compile("(?s)" + built.String())
	return err == nil && pattern.MatchString(name)
}

// transcriptFiles are the transcripts in the folders of projectsDir whose names match a pattern.
func transcriptFiles(globs []string, projectsDir string) []string {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	var found []string
	for _, entry := range entries {
		if !entry.IsDir() || !slices.ContainsFunc(globs, func(glob string) bool { return matchesGlob(entry.Name(), glob) }) {
			continue
		}
		_ = filepath.WalkDir(filepath.Join(projectsDir, entry.Name()), func(place string, item fs.DirEntry, walkErr error) error {
			if walkErr == nil && !item.IsDir() && strings.HasSuffix(item.Name(), ".jsonl") {
				found = append(found, place)
			}
			return nil
		})
	}
	return found
}

// resultText is the text of a tool_result.
func resultText(block map[string]any) string {
	switch content := block["content"].(type) {
	case string:
		return content
	case []any:
		var parts []string
		for _, part := range content {
			if object, isObject := part.(map[string]any); isObject {
				if text, isText := object["text"].(string); isText {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// fateOf is what a tool_result says became of its call. A command that only printed such words
// ran: the text must start with them.
func fateOf(record, block map[string]any) string {
	text := resultText(block)
	if decision, isObject := record["permissionDecision"].(map[string]any); isObject && decision["decision"] == "reject" {
		if decision["source"] == "hook" || decision["reasonType"] == "hook" {
			return fateRefused
		}
		return fateDenied
	}
	isError := truthy(block["is_error"])
	if isError && hookText.MatchString(text) {
		return fateRefused
	}
	kind, isText := record["toolDenialKind"].(string)
	if (isText && slices.Contains(denialKindsNotRun, kind)) || (isError && deniedText.MatchString(text)) {
		return fateDenied
	}
	return fateRan
}

// readCalls are the Bash calls of the transcripts. A tool_use id seen again (a resumed or forked
// session copies its history; a request blob repeats the call) is one call: the first call and
// the first result seen are kept.
func readCalls(paths []string) calls {
	found := calls{byID: map[string]*call{}}
	results, resultOrder := map[string]result{}, []string{}
	ends := map[string]notice{}
	noteEnds := func(text, when string) {
		for _, match := range notification.FindAllStringSubmatchIndex(text, -1) {
			id := text[match[2]:match[3]]
			if _, seen := ends[id]; seen {
				continue
			}
			end := notice{at: when, status: text[match[4]:match[5]]}
			if match[6] >= 0 {
				end.code, end.hasCode = text[match[6]:match[7]], true
			}
			ends[id] = end
		}
	}
	for _, place := range paths {
		eachRecord(place, func(record map[string]any) {
			kind, when := textOf(record["type"]), textOf(record["timestamp"])
			if kind == "queue-operation" {
				if content, isText := record["content"].(string); isText {
					noteEnds(content, when)
				}
				return
			}
			if kind != "assistant" && kind != "user" {
				return
			}
			message, _ := record["message"].(map[string]any)
			if content, isText := message["content"].(string); isText && kind == "user" && strings.Contains(content, "<task-notification>") {
				noteEnds(content, when)
				return
			}
			blocks, _ := message["content"].([]any)
			for _, item := range blocks {
				block, isObject := item.(map[string]any)
				if !isObject {
					continue
				}
				if kind == "assistant" && block["type"] == "tool_use" && block["name"] == "Bash" {
					given, _ := block["input"].(map[string]any)
					command, hasCommand := given["command"].(string)
					id, hasID := block["id"].(string)
					if hasCommand && hasID && found.byID[id] == nil {
						found.order = append(found.order, id)
						found.byID[id] = &call{id: id, at: when, command: command, cwd: textOf(record["cwd"]),
							background: truthy(given["run_in_background"]), fate: fateUnanswered}
					}
				} else if kind == "user" && block["type"] == "tool_result" {
					id, hasID := block["tool_use_id"].(string)
					if _, seen := results[id]; !hasID || seen {
						continue
					}
					outcome, _ := record["toolUseResult"].(map[string]any)
					results[id] = result{at: when, isError: truthy(block["is_error"]), fate: fateOf(record, block),
						background: truthy(outcome["backgroundTaskId"])}
					resultOrder = append(resultOrder, id)
				}
			}
		})
	}
	for _, id := range resultOrder {
		answered, known := found.byID[id]
		if !known {
			continue
		}
		got := results[id]
		answered.fate = got.fate
		if got.background {
			answered.background = true
		} else {
			answered.ended, answered.failed = got.at, got.isError
		}
	}
	for id, end := range ends {
		ended, known := found.byID[id]
		if known && ended.background && (ended.fate == fateRan || ended.fate == fateUnanswered) {
			ended.ended = end.at
			ended.failed = end.status != "completed" || (end.hasCode && end.code != "0")
		}
	}
	return found
}

// eachRecord visits each line of a transcript that is a JSON object and may hold a call, a result
// or a notification.
func eachRecord(place string, visit func(record map[string]any)) {
	file, err := os.Open(place)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"tool_use"`)) || bytes.Contains(line, []byte(`"tool_result"`)) ||
			bytes.Contains(line, []byte("task-notification")) {
			var record map[string]any
			if json.Unmarshal(bytes.ToValidUTF8(line, []byte("\uFFFD")), &record) == nil && record != nil {
				visit(record)
			}
		}
		if readErr != nil {
			return
		}
	}
}
