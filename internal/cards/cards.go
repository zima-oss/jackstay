// Package cards reads the `Card:` lines of a message and judges them (docs/design/implementation.md,
// section 4.1): which cards a change is work for, and whether its lines keep the convention. It is
// pure: the caller says which card files exist (from a tree, a checkout or a fake), so the guard's
// push rule and `jst lint` judge the same way and neither needs a second copy.
package cards

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Rule ids of what a message can break. They are what callers and the differential check
// compare; the sentences that go with them are each caller's own.
const (
	NoLine     = "card-line-missing"    // no line starts with "Card: "
	Unreadable = "card-line-unreadable" // a line that does not keep the form
	NoReason   = "card-none-reason"     // `none` without a reason in brackets, or not alone
	Unknown    = "card-unknown"         // an ID with no card file
)

// Rules are a project's `cards` section as far as the lines need it.
type Rules struct {
	Dir string         // the folder of the card files, relative to the repository
	ID  *regexp.Regexp // a card's ID, matched whole
	// start matches an ID at the start of a file name.
	start *regexp.Regexp
	// anywhere matches an ID with what may stand before and after it (group 1).
	anywhere *regexp.Regexp
}

// New is the rules of a `cards` section's dir and id (an RE2 pattern).
func New(dir, id string) (Rules, error) {
	if dir == "" || id == "" {
		return Rules{}, errors.New("cards needs dir and id")
	}
	whole, err := regexp.Compile(`^(?:` + id + `)$`)
	if err != nil {
		return Rules{}, fmt.Errorf("cards.id: %w", err)
	}
	if whole.MatchString("") {
		return Rules{}, errors.New("cards.id must match a nonempty ID")
	}
	return Rules{Dir: strings.Trim(path.Clean(dir), "/"), ID: whole, start: regexp.MustCompile(`^(?:` + id + `)`),
		anywhere: regexp.MustCompile(`[^A-Za-z0-9-](` + id + `)(?:[^A-Za-z0-9]|$)`)}, nil
}

// FromSection is the rules of a `cards` section as internal/config returns it.
func FromSection(section map[string]any) (Rules, error) {
	dir, _ := section["dir"].(string)
	id, _ := section["id"].(string)
	return New(dir, id)
}

// IDOfFile is the card ID a file name starts with ("APP-140" of "APP-140-empty-state.md"), or "".
// The ID must be followed by "-" or "." (so APP-1400 is not APP-140's) and the name must end in
// ".md": a card is a Markdown file.
func (rules Rules) IDOfFile(name string) string {
	if !strings.HasSuffix(name, ".md") {
		return ""
	}
	for _, loc := range rules.start.FindAllStringIndex(name, -1) {
		if loc[0] != 0 {
			continue
		}
		rest := name[loc[1]:]
		if rest != "" && (rest[0] == '-' || rest[0] == '.') {
			return name[:loc[1]]
		}
	}
	return ""
}

// Files are the card files among paths (from the repository's root), by ID: a Markdown file
// directly in the cards folder whose name starts with an ID.
func (rules Rules) Files(paths []string) map[string][]string {
	out := map[string][]string{}
	for _, name := range paths {
		if path.Dir(name) != rules.Dir {
			continue
		}
		if id := rules.IDOfFile(path.Base(name)); id != "" {
			out[id] = append(out[id], name)
		}
	}
	return out
}

// Known is the set of card IDs among paths (from the repository's root): the IDs of Files.
func (rules Rules) Known(paths []string) map[string]bool {
	out := map[string]bool{}
	for id := range rules.Files(paths) {
		out[id] = true
	}
	return out
}

// Line is one `Card:` line read.
type Line struct {
	Number int      // the line's number in the message, from 1
	Text   string   // the line as written
	IDs    []string // the cards it names
	None   string   // the reason of `none (…)`, when the line says none
	IsNone bool     // the line says none
}

// Finding is a rule a message breaks, with the line it is about (0 for the message as a whole)
// and the ID when it is about one card.
type Finding struct {
	Rule string
	Line int
	ID   string
	Text string
}

// Lines are the message's lines that start with "Card: " at the first column, line ends
// normalised first (a description edited on GitHub may carry carriage returns).
func Lines(message string) []Line {
	var out []Line
	message = strings.ReplaceAll(strings.ReplaceAll(message, "\r\n", "\n"), "\r", "\n")
	for index, text := range strings.Split(message, "\n") {
		if strings.HasPrefix(text, "Card: ") {
			out = append(out, Line{Number: index + 1, Text: text})
		}
	}
	return out
}

var noneLine = regexp.MustCompile(`^none\s*\((.*)\)\s*\.?$`)

// Parse reads one line's value strictly: one or more cards separated by commas, each an ID or the
// path of its file in Dir, backquotes around it and a full stop after it allowed; or `none
// (REASON)` alone. Each problem of the line is a finding.
func (rules Rules) Parse(line Line) (Line, []Finding) {
	value := strings.TrimSpace(strings.TrimPrefix(line.Text, "Card: "))
	if strings.HasPrefix(value, "none") {
		match := noneLine.FindStringSubmatch(value)
		if match == nil || strings.TrimSpace(match[1]) == "" {
			return line, []Finding{{Rule: NoReason, Line: line.Number, Text: line.Text}}
		}
		line.None, line.IsNone = strings.TrimSpace(match[1]), true
		return line, nil
	}
	if value == "" {
		return line, []Finding{{Rule: Unreadable, Line: line.Number, Text: line.Text}}
	}
	var found []Finding
	for _, item := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" || strings.HasPrefix(trimmed, "none") {
			rule := Unreadable
			if trimmed != "" {
				rule = NoReason
			}
			found = append(found, Finding{Rule: rule, Line: line.Number, Text: line.Text})
			continue
		}
		id := rules.item(trimmed)
		if id == "" {
			found = append(found, Finding{Rule: Unreadable, Line: line.Number, Text: line.Text})
			continue
		}
		if !slices.Contains(line.IDs, id) {
			line.IDs = append(line.IDs, id)
		}
	}
	return line, found
}

// item is the ID one comma-separated item names, or "" when it is not a card of this project.
func (rules Rules) item(candidate string) string {
	if strings.HasSuffix(candidate, ".") {
		candidate = strings.TrimRightFunc(candidate[:len(candidate)-1], unicode.IsSpace)
	}
	if len(candidate) >= 2 && strings.HasPrefix(candidate, "`") && strings.HasSuffix(candidate, "`") {
		candidate = strings.TrimSpace(candidate[1 : len(candidate)-1])
	}
	if rules.ID.MatchString(candidate) {
		return candidate
	}
	if !strings.Contains(candidate, "/") && !strings.HasSuffix(candidate, ".md") {
		return ""
	}
	folder, name := path.Split(candidate)
	folder = strings.Trim(folder, "/")
	folder = strings.TrimPrefix(folder, "./")
	if folder != rules.Dir {
		return ""
	}
	return rules.IDOfFile(name)
}

// Check judges a message: at least one `Card:` line, each read strictly, every ID with a card
// file among known, and `none` alone. The findings are in the order of the lines.
func (rules Rules) Check(message string, known map[string]bool) []Finding {
	lines := Lines(message)
	if len(lines) == 0 {
		return []Finding{{Rule: NoLine}}
	}
	var out []Finding
	var named []string
	nones := 0
	for _, line := range lines {
		read, problems := rules.Parse(line)
		out = append(out, problems...)
		if read.IsNone {
			nones++
		}
		for _, id := range read.IDs {
			if !slices.Contains(named, id) {
				named = append(named, id)
				if !known[id] {
					out = append(out, Finding{Rule: Unknown, Line: line.Number, ID: id, Text: line.Text})
				}
			}
		}
	}
	if nones > 0 && len(named) > 0 {
		out = append(out, Finding{Rule: NoReason, Text: "`Card: none (…)` stands alone: this message also names " + strings.Join(named, ", ")})
	}
	return out
}

// Anywhere are the IDs of this project written anywhere in text (a line from before adoption, a
// subject), in order and once each: an ID not glued to a letter, digit or dash before it, nor to
// a letter or digit after it.
func (rules Rules) Anywhere(text string) []string {
	var found []string
	// A space before the text stands for its start; each later search starts at the last character
	// of the previous ID, which then stands for what is before the next.
	rest := " " + text
	for {
		match := rules.anywhere.FindStringSubmatchIndex(rest)
		if match == nil {
			return found
		}
		id := rest[match[2]:match[3]]
		if id == "" {
			rest = rest[match[3]:]
			continue
		}
		if !slices.Contains(found, id) {
			found = append(found, id)
		}
		rest = rest[match[3]-1:]
	}
}
