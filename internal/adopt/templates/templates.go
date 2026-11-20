// Package templates holds the workflow templates jackstay embeds and the rules every template follows,
// a profile's included (docs/design/adopt.md sections 3.4 and 7): actions pinned by a full hash
// (W-037), values written `@@name@@`, and the first line that marks a file as jackstay's.
package templates

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

//go:embed files/*.yml
var files embed.FS

// Repository is where jackstay's releases are downloaded from: jackstay's own home.
const Repository = "zima-oss/jackstay"

// Template is one workflow: its name (the file is .github/workflows/<name>.yml), its text, and the
// jackstay.json sections of which any one enables it (none: always).
type Template struct {
	Name string
	Text string
	When []string
}

// Embedded are jackstay's own templates, by name.
func Embedded() []Template {
	return []Template{
		{Name: "card-line", Text: embedded("card-line"), When: []string{"cards", "changes", "land"}},
		{Name: "release", Text: embedded("release"), When: []string{"release"}},
	}
}

func embedded(name string) string {
	data, err := files.ReadFile("files/" + name + ".yml")
	if err != nil {
		panic("jst: the embedded template " + name + " is missing")
	}
	return string(data)
}

// ValueNames are the values jackstay defines for every template.
var ValueNames = []string{"release_base", "jackstay_repository", "jackstay_version"}

var (
	placeholderPattern = regexp.MustCompile(`@@([A-Za-z0-9_]+)@@`)
	usesPattern        = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(\S+)(.*)$`)
	pinnedAction       = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_./-]+)?@[0-9a-f]{40}$`)
	pinnedImage        = regexp.MustCompile(`^docker://[^@\s]+@sha256:[0-9a-f]{64}$`)
	versionComment     = regexp.MustCompile(`^\s+#\s*v[0-9]`)
)

// Placeholders are the `@@name@@` names the text uses, sorted, each once.
func Placeholders(text string) []string {
	seen := map[string]bool{}
	var names []string
	for _, match := range placeholderPattern.FindAllStringSubmatch(text, -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			names = append(names, match[1])
		}
	}
	sort.Strings(names)
	return names
}

// PinProblems are the `uses:` lines not pinned: an action by a full commit hash followed by a
// `# vX…` comment, a local `./path`, or an image by its sha256.
func PinProblems(text string) []string {
	var problems []string
	for number, line := range strings.Split(text, "\n") {
		match := usesPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		target := strings.Trim(match[1], `"'`)
		switch {
		case strings.HasPrefix(target, "./"):
		case strings.HasPrefix(target, "docker://"):
			if !pinnedImage.MatchString(target) {
				problems = append(problems, fmt.Sprintf("line %d: %s is not an image pinned by its sha256", number+1, target))
			}
		default:
			if !pinnedAction.MatchString(target) || !versionComment.MatchString(match[2]) {
				problems = append(problems, fmt.Sprintf("line %d: %s is not pinned by a full commit hash with a `# vX.Y.Z` comment", number+1, target))
			}
		}
	}
	return problems
}

// Render puts each value in place of its `@@name@@`; a name with no value is an error.
func Render(text string, values map[string]string) (string, error) {
	for _, name := range Placeholders(text) {
		if _, ok := values[name]; !ok {
			return "", fmt.Errorf("no value for @@%s@@", name)
		}
	}
	return placeholderPattern.ReplaceAllStringFunc(text, func(found string) string {
		return values[strings.Trim(found, "@")]
	}), nil
}

// Hash is the first 12 hex digits of the text's SHA-256: what a marker or a header carries.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:12]
}

const headerPrefix = "# jackstay-adopt: "

const headerTail = " — written by `jst adopt`; change jackstay.json or the profile, not this file"

// Marked is the file jackstay writes for a template: its header line, then the body.
func Marked(name, body string) string {
	return headerPrefix + name + " sha256:" + Hash(body) + headerTail + "\n" + body
}

var headerPattern = regexp.MustCompile(`^# jackstay-adopt: ([A-Za-z0-9_.-]+) sha256:([0-9a-f]{12})(?: |$)`)

// Header reads a file jackstay may have written: whether its first line is a jackstay-adopt header, the
// name and hash it carries (empty when the line cannot be read), and whether the body still
// matches the hash.
func Header(content string) (marked bool, name string, intact bool) {
	firstLine, body, _ := strings.Cut(content, "\n")
	if !strings.HasPrefix(firstLine, headerPrefix) {
		return false, "", false
	}
	match := headerPattern.FindStringSubmatch(firstLine)
	if match == nil {
		return true, "", false
	}
	return true, match[1], Hash(body) == match[2]
}
