package changes

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"regexp"
	"strings"

	"github.com/zima-oss/jackstay/internal/strictjson"
)

// Types are the change types in the order a section renders them (docs/design/release.md, section 2).
var Types = []string{"breaking", "security", "deprecated", "feature", "fix", "note"}

// typeHeadings are the headings a section gives each type.
var typeHeadings = map[string]string{
	"breaking":   "Breaking changes",
	"security":   "Security",
	"deprecated": "Deprecated",
	"feature":    "Features",
	"fix":        "Fixes",
	"note":       "Notes",
}

// TypeHeading is the heading a section gives a type.
func TypeHeading(kind string) string { return typeHeadings[kind] }

// Audiences an entry can have.
const (
	Consumer = "consumer"
	Internal = "internal"
)

// OverrideName and RequestName are jackstay's own files in the entries' folder; they are not entries.
const (
	OverrideName = "_release.json"
	RequestName  = "_release-request.json"
)

// versionText is MAJOR.MINOR.PATCH.
var versionText = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)

// slugPattern is the rule for an entry's name without ".md".
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

// IsSlug says whether name is a slug an entry may have.
func IsSlug(name string) bool { return slugPattern.MatchString(name) }

// frontMatter is the JSON object between an entry's `---` lines.
type frontMatter struct {
	Type     string `json:"type"`
	Audience string `json:"audience"`
	Card     string `json:"card,omitzero"`
	Yanks    string `json:"yanks,omitzero"`
}

// Entry is one changelog entry as read from its file.
type Entry struct {
	Name      string // the file's name, `<slug>.md`
	Typed     bool   // it has a front matter
	Type      string
	Audience  string
	Card      string
	Yanks     string
	Title     string // without the leading "# "
	Body      string // the text after the title, the migration section taken out, trimmed
	Migration string // the text of `## Migration`, trimmed; "" when there is none
	Text      string // the whole file, for an untyped entry's rendering
}

// Slug is the entry's name without ".md".
func (entry Entry) Slug() string { return strings.TrimSuffix(entry.Name, ".md") }

// Bump is how far the entry moves the version: "major", "minor", "patch" or "" (none).
func (entry Entry) Bump() string {
	switch entry.Type {
	case "breaking":
		return "major"
	case "feature", "deprecated":
		return "minor"
	case "fix", "security":
		return "patch"
	}
	return ""
}

// Parse reads an entry file. An untyped entry (no front matter) is returned with Typed false;
// problems are what makes the file an invalid entry, each a sentence.
func Parse(name string, data []byte) (Entry, []string) {
	text := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
	entry := Entry{Name: name, Text: strings.TrimSpace(text)}
	if !strings.HasPrefix(text, "---\n") {
		return entry, nil
	}
	rest := text[len("---\n"):]
	closing := strings.Index(rest, "\n---\n")
	if closing < 0 {
		if strings.HasSuffix(rest, "\n---") {
			closing = len(rest) - len("\n---")
		} else {
			return entry, []string{"its front matter has no closing `---` line"}
		}
	}
	entry.Typed = true
	var matter frontMatter
	var problems []string
	readable := true
	if err := strictjson.Unmarshal([]byte(rest[:closing]), &matter); err != nil {
		problems = append(problems, "its front matter is not one JSON object of type, audience, card and yanks: "+err.Error())
		readable = false
	}
	entry.Type, entry.Audience, entry.Card, entry.Yanks = matter.Type, matter.Audience, matter.Card, matter.Yanks
	if _, known := typeHeadings[entry.Type]; readable && !known {
		problems = append(problems, fmt.Sprintf("its type %q is not one of %s", entry.Type, strings.Join(Types, ", ")))
	}
	if readable && entry.Audience != Consumer && entry.Audience != Internal {
		problems = append(problems, fmt.Sprintf("its audience %q is not %s or %s", entry.Audience, Consumer, Internal))
	}
	if entry.Yanks != "" && entry.Type != "note" {
		problems = append(problems, "only a note may name a version it yanks")
	}
	if entry.Yanks != "" && !versionText.MatchString(entry.Yanks) {
		problems = append(problems, fmt.Sprintf("its yanks %q is not a version", entry.Yanks))
	}
	body := ""
	if after := closing + len("\n---\n"); after <= len(rest) {
		body = rest[after:]
	}
	title, remainder, found := cutTitle(body)
	if !found {
		problems = append(problems, "it has no title: the first line after the front matter must be `# ` and the title")
	}
	entry.Title = title
	entry.Body, entry.Migration = cutMigration(remainder)
	if entry.Type == "breaking" && entry.Migration == "" {
		problems = append(problems, "a breaking entry needs a `## Migration` section with at least one line")
	}
	return entry, problems
}

// cutTitle takes the first non-empty line, which must be "# TITLE", from body.
func cutTitle(body string) (title, rest string, found bool) {
	trimmed := strings.TrimLeft(body, "\n")
	line, rest, _ := strings.Cut(trimmed, "\n")
	if !strings.HasPrefix(line, "# ") || strings.TrimSpace(line[2:]) == "" {
		return "", body, false
	}
	return strings.TrimSpace(line[2:]), rest, true
}

// cutMigration takes the `## Migration` section out of text: the lines after that heading up to
// the next heading or the end.
func cutMigration(text string) (body, migration string) {
	lines := strings.Split(text, "\n")
	start := -1
	var fence fenceState
	for index, line := range lines {
		if fence.step(line) {
			continue
		}
		if strings.TrimSpace(line) == "## Migration" {
			start = index
			break
		}
	}
	if start < 0 {
		return strings.TrimSpace(text), ""
	}
	end := len(lines)
	var fenced fenceState
	for index := start + 1; index < len(lines); index++ {
		if fenced.step(lines[index]) {
			continue
		}
		if isHeading(lines[index]) {
			end = index
			break
		}
	}
	migration = strings.TrimSpace(strings.Join(lines[start+1:end], "\n"))
	kept := append(append([]string{}, lines[:start]...), lines[end:]...)
	return strings.TrimSpace(strings.Join(kept, "\n")), migration
}

// fenceState follows Markdown code fences (CommonMark): a fence opens with three or more backticks
// or tildes and closes with a line of the same character, at least as long, and nothing else.
type fenceState struct {
	char  byte
	width int
}

// step reads one line and says whether it is inside a fence or is a fence line itself.
func (fence *fenceState) step(line string) bool {
	trimmed := strings.TrimSpace(line)
	run := 0
	if trimmed != "" && (trimmed[0] == '`' || trimmed[0] == '~') {
		for run < len(trimmed) && trimmed[run] == trimmed[0] {
			run++
		}
	}
	if fence.width == 0 {
		if run >= 3 {
			fence.char, fence.width = trimmed[0], run
			return true
		}
		return false
	}
	if run >= fence.width && trimmed[0] == fence.char && strings.TrimSpace(trimmed[run:]) == "" {
		fence.width = 0
	}
	return true
}

// isHeading says whether a line is a Markdown ATX heading: one to six #, then a space or nothing.
func isHeading(line string) bool {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	return level >= 1 && level <= 6 && (level == len(line) || line[level] == ' ')
}

// Format writes a typed entry's file: front matter, title, body, migration.
func Format(entry Entry) []byte {
	var out bytes.Buffer
	out.WriteString("---\n")
	out.WriteString(frontMatterLine(entry))
	out.WriteString("\n---\n# ")
	out.WriteString(entry.Title)
	out.WriteString("\n")
	if entry.Body != "" {
		out.WriteString("\n")
		out.WriteString(entry.Body)
		out.WriteString("\n")
	}
	if entry.Migration != "" {
		out.WriteString("\n## Migration\n\n")
		out.WriteString(entry.Migration)
		out.WriteString("\n")
	}
	return out.Bytes()
}

// frontMatterLine is the front matter as one JSON line, keys in a fixed order.
func frontMatterLine(entry Entry) string {
	parts := []string{fmt.Sprintf(`"type": %s`, quote(entry.Type)), fmt.Sprintf(`"audience": %s`, quote(entry.Audience))}
	if entry.Card != "" {
		parts = append(parts, fmt.Sprintf(`"card": %s`, quote(entry.Card)))
	}
	if entry.Yanks != "" {
		parts = append(parts, fmt.Sprintf(`"yanks": %s`, quote(entry.Yanks)))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// quote is a JSON string literal.
func quote(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		panic(err) // a Go string always encodes
	}
	return string(encoded)
}
