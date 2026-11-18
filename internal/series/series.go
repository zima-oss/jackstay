// Package series is what jackstay knows of a numbered series (docs/design/implementation.md, sections 4.2
// and 4.3): which file names hold one of its numbers, how an ID is written, and the
// front matter of a card or a record (one JSON object between two `---` lines). `jst id` counts by
// these rules and `jst lint tree` finds duplicates by them, so the two never disagree.
package series

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"encoding/json/jsontext"

	"github.com/zima-oss/jackstay/internal/strictjson"
)

// Taken is what holds a number of a series whose files follow pattern (`docs/work/tasks/APP-{n}-*.md`):
// a path, written from the repository's root, in the pattern's folder, whose name begins as the
// pattern's does up to `{n}` (its `*` and `?` keep their meaning), then the number, then anything
// but a digit, or nothing. `APP-170.md`, `APP-170` and `APP-170-x.md.orig` all hold 170: a file
// named in another way still takes its number. Group 1 is the number.
func Taken(pattern string) (*regexp.Regexp, error) {
	if strings.Count(pattern, "{n}") != 1 {
		return nil, fmt.Errorf("the pattern %q must hold `{n}` once", pattern)
	}
	folder, name := path.Split(pattern)
	if strings.Contains(folder, "{n}") {
		return nil, fmt.Errorf("the pattern %q has `{n}` in a folder's name", pattern)
	}
	prefix, _, _ := strings.Cut(name, "{n}")
	var text strings.Builder
	text.WriteString("^")
	text.WriteString(regexp.QuoteMeta(folder))
	text.WriteString(glob(prefix))
	text.WriteString(`([0-9]+)(?:[^0-9/][^/]*)?$`)
	return regexp.Compile(text.String())
}

// glob is a file-name glob's `*` and `?` as RE2, the rest literal.
func glob(text string) string {
	var out strings.Builder
	for _, char := range text {
		switch char {
		case '*':
			out.WriteString("[^/]*")
		case '?':
			out.WriteString("[^/]")
		default:
			out.WriteString(regexp.QuoteMeta(string(char)))
		}
	}
	return out.String()
}

// Folder is the folder of a pattern, from the repository's root ("" for the root itself).
func Folder(pattern string) string {
	folder := path.Dir(pattern)
	if folder == "." {
		return ""
	}
	return folder
}

// MaxNumber is the largest number jackstay counts: a file holding a larger one makes the series
// uncountable (an error), never a number passed over.
const MaxNumber = 1_000_000_000_000

// Numbers are the numbers the paths hold by taken, each with the paths that hold it; an error names
// a path whose number is above MaxNumber.
func Numbers(paths []string, taken *regexp.Regexp) (map[int][]string, error) {
	found := map[int][]string{}
	for _, filePath := range paths {
		match := taken.FindStringSubmatch(filePath)
		if match == nil {
			continue
		}
		number, err := strconv.Atoi(match[1])
		if err != nil || number > MaxNumber {
			return nil, fmt.Errorf("%s holds a number above %d, which jackstay does not count", filePath, MaxNumber)
		}
		found[number] = append(found[number], filePath)
	}
	return found, nil
}

// Format is an ID: the series, a dash, the number padded with zeros to width digits.
func Format(name string, width, number int) string {
	return fmt.Sprintf("%s-%0*d", name, max(width, 1), number)
}

// NumberOf splits an ID into the text before its trailing number and the number ("APP-" and 170
// of "APP-170"); ok is false when it does not end in digits.
func NumberOf(id string) (prefix string, number int, ok bool) {
	end := len(id)
	start := end
	for start > 0 && id[start-1] >= '0' && id[start-1] <= '9' {
		start--
	}
	if start == end {
		return "", 0, false
	}
	number, err := strconv.Atoi(id[start:])
	if err != nil {
		return "", 0, false
	}
	return id[:start], number, true
}

// Mentions are the numbers of the series written anywhere in text (`CHK-12` in an old ledger's
// rows), not glued to a letter, digit or dash before or to a digit after; an error for one above
// MaxNumber.
func Mentions(name, text string) ([]int, error) {
	pattern := regexp.MustCompile(regexp.QuoteMeta(name) + `-([0-9]+)`)
	var found []int
	for _, loc := range pattern.FindAllStringSubmatchIndex(text, -1) {
		if loc[0] > 0 && glued(text[loc[0]-1]) {
			continue
		}
		if loc[1] < len(text) && text[loc[1]] >= '0' && text[loc[1]] <= '9' {
			continue
		}
		number, err := strconv.Atoi(text[loc[2]:loc[3]])
		if err != nil || number > MaxNumber {
			return nil, fmt.Errorf("%s-%s is above %d, which jackstay does not count", name, text[loc[2]:loc[3]], MaxNumber)
		}
		found = append(found, number)
	}
	return found, nil
}

func glued(char byte) bool {
	return char == '-' || (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

// Member is one member of a front matter object, its value as written (JSON).
type Member = strictjson.Member

// FrontMatter is a file read as front matter and body.
type FrontMatter struct {
	// Members are the object's members in the order written.
	Members []Member
	// Body is everything after the closing `---` line.
	Body string
	// OneLine says the object was written on one line (and is written back so).
	OneLine bool
}

// ErrNoFrontMatter is a text that does not start with a `---` line, one JSON object and a `---` line.
var ErrNoFrontMatter = errors.New("its front matter is not one JSON object between `---` lines")

// ReadFrontMatter reads text that starts with a line `---`, one JSON object (no member twice) and a
// line `---`. Line ends are normalised first.
func ReadFrontMatter(text string) (FrontMatter, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return FrontMatter{}, ErrNoFrontMatter
	}
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) != "---" {
			continue
		}
		members, err := objectMembers([]byte(strings.Join(lines[1:index], "\n")))
		if err != nil {
			return FrontMatter{}, ErrNoFrontMatter
		}
		return FrontMatter{Members: members, Body: strings.Join(lines[index+1:], "\n"), OneLine: index == 2}, nil
	}
	return FrontMatter{}, ErrNoFrontMatter
}

// objectMembers reads one JSON object strictly: jackstay's one strict reading of its own formats.
func objectMembers(data []byte) ([]Member, error) {
	return strictjson.Members(data)
}

// Get is a member's value decoded (numbers as json.Number), and whether it is there.
func (matter FrontMatter) Get(name string) (any, bool) {
	for _, member := range matter.Members {
		if member.Name == name {
			var value any
			decoder := json.NewDecoder(bytes.NewReader(member.Value))
			decoder.UseNumber()
			if decoder.Decode(&value) != nil {
				return nil, false
			}
			return value, true
		}
	}
	return nil, false
}

// Set gives a member a value, in its place when it is there, else at the end.
func (matter *FrontMatter) Set(name string, value any) error {
	encoded, err := encode(value)
	if err != nil {
		return err
	}
	for index := range matter.Members {
		if matter.Members[index].Name == name {
			matter.Members[index].Value = encoded
			return nil
		}
	}
	matter.Members = append(matter.Members, Member{Name: name, Value: encoded})
	return nil
}

func encode(value any) (jsontext.Value, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return jsontext.Value(bytes.TrimRight(buffer.Bytes(), "\n")), nil
}

// String is the file again: `---`, the object (on one line when it was, else indented by two),
// `---`, the body. On one line members are separated by ", " and names from values by ": ".
func (matter FrontMatter) String() string {
	var object bytes.Buffer
	object.WriteString("{")
	for index, member := range matter.Members {
		if index > 0 {
			object.WriteString(", ")
		}
		name, _ := encode(member.Name)
		object.Write(name)
		object.WriteString(": ")
		object.Write(OneLine(member.Value))
	}
	object.WriteString("}")
	head := object.Bytes()
	if !matter.OneLine {
		var indented bytes.Buffer
		if json.Indent(&indented, head, "", "  ") == nil {
			head = indented.Bytes()
		}
	}
	return "---\n" + string(head) + "\n---\n" + matter.Body
}

// OneLine is a JSON value on one line, with ", " and ": " as separators.
func OneLine(value jsontext.Value) []byte {
	var compact bytes.Buffer
	if json.Compact(&compact, value) != nil {
		return value
	}
	var out bytes.Buffer
	inString, escaped := false, false
	for _, char := range compact.Bytes() {
		out.WriteByte(char)
		switch {
		case inString && escaped:
			escaped = false
		case inString && char == '\\':
			escaped = true
		case char == '"':
			inString = !inString
		case !inString && (char == ',' || char == ':'):
			out.WriteByte(' ')
		}
	}
	return out.Bytes()
}
