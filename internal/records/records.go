// Package records is `jst new` and `jst view`: one file per record of a series (docs/design/implementation.md,
// section 4.3). `new` takes the number from the counter (internal/ids), copies
// the template with `id` (and `card`) filled in and writes nothing else; `view` prints the table of
// the records' files and writes nothing — a committed table would be the shared file again.
package records

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/cards"
	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/ids"
	"github.com/zima-oss/jackstay/internal/project"
	"github.com/zima-oss/jackstay/internal/series"
)

const (
	newUsage  = "usage: jst new SERIES --slug SLUG [--card ID] [--set KEY=VALUE]..."
	viewUsage = "usage: jst view SERIES [--where KEY=VALUE]... [--format table|list|json]"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

func init() {
	cli.Register(
		cli.Command{Name: "new", Summary: "a numbered record as a file of its own", Usage: newHelp, Run: runNew},
		cli.Command{Name: "view", Summary: "print a series' records as a table", Usage: viewHelp, Run: runView},
	)
}

func report(name string, err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	if err.Error() != "" {
		fmt.Fprintf(stderr, "jst %s: %v\n", name, err)
	}
	var failure *project.Problem
	if errors.As(err, &failure) {
		return failure.Code
	}
	return 2
}

// options are a command's options: one value each for single, any number for repeated.
type options struct {
	single   map[string]string
	repeated map[string][]string
}

func parse(args []string, single, repeated []string) (options, error) {
	found := options{single: map[string]string{}, repeated: map[string][]string{}}
	isSingle := func(arg string) bool { return contains(single, arg) }
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case isSingle(arg) || contains(repeated, arg):
			_, given := found.single[arg]
			if index+1 >= len(args) || (isSingle(arg) && given) {
				return options{}, project.Fail("%s needs one value", arg)
			}
			if isSingle(arg) {
				found.single[arg] = args[index+1]
			} else {
				found.repeated[arg] = append(found.repeated[arg], args[index+1])
			}
			index++
		default:
			return options{}, project.Fail("unknown argument %q", arg)
		}
	}
	return found, nil
}

func contains(list []string, wanted string) bool {
	for _, item := range list {
		if item == wanted {
			return true
		}
	}
	return false
}

// Entry is one entry of jackstay.json's `records`.
type Entry struct {
	Dir      string
	Template string
	Columns  []string
	Before   string
}

// EntryOf is the `records` entry of a series.
func EntryOf(proj project.Project, name string) (Entry, error) {
	section := proj.Config.Section("records")
	entry, ok := section[name].(map[string]any)
	if !ok {
		known := make([]string, 0, len(section))
		for key := range section {
			known = append(known, key)
		}
		sort.Strings(known)
		listed := strings.Join(known, ", ")
		if listed == "" {
			listed = "none"
		}
		return Entry{}, project.Fail("jackstay.json has no `records.%s` (the series kept as files here: %s)", name, listed)
	}
	found := Entry{}
	found.Dir, _ = entry["dir"].(string)
	found.Template, _ = entry["template"].(string)
	found.Before, _ = entry["before"].(string)
	columns, _ := entry["columns"].([]any)
	for _, column := range columns {
		if text, ok := column.(string); ok {
			found.Columns = append(found.Columns, text)
		}
	}
	if len(found.Columns) == 0 {
		found.Columns = []string{"id"}
	}
	return found, nil
}

func runNew(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	return report("new", newRecord(args, stdout), stderr)
}

func newRecord(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return project.Fail(newUsage)
	}
	name := args[0]
	found, err := parse(args[1:], []string{"--slug", "--card"}, []string{"--set"})
	if err != nil {
		return err
	}
	need := []string{"ids", "records"}
	if _, given := found.single["--card"]; given {
		need = append(need, "cards")
	}
	proj, err := project.Here(need)
	if err != nil {
		return project.Fail("%v", err)
	}
	entry, err := EntryOf(proj, name)
	if err != nil {
		return err
	}
	slug := found.single["--slug"]
	if !slugPattern.MatchString(slug) {
		return project.Fail("--slug takes lowercase letters, digits and dashes (`login-after-update`)")
	}
	card, given := found.single["--card"]
	if given {
		rules, err := cards.FromSection(proj.Config.Section("cards"))
		if err != nil {
			return project.Fail("--card needs a usable `cards` section: %v", err)
		}
		if !rules.ID.MatchString(card) {
			return project.Fail("%s is not a card ID of this project (they match %s)", card, proj.Config.Section("cards")["id"])
		}
	}
	type setting struct{ key, value string }
	var settings []setting
	for _, item := range found.repeated["--set"] {
		key, value, ok := strings.Cut(item, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || key == "id" {
			return project.Fail("--set takes KEY=VALUE (and not id): %q", item)
		}
		settings = append(settings, setting{key, value})
	}
	repo := project.Repo{Root: proj.Root}
	text, err := repo.ReadFile(entry.Template)
	if errors.Is(err, fs.ErrNotExist) {
		return project.Fail("the template %s is not there", entry.Template)
	}
	if err != nil {
		return project.Fail("the template %s cannot be read (%v)", entry.Template, err)
	}
	matter, err := series.ReadFrontMatter(string(text))
	if err != nil {
		return project.Fail("the template %s: %v", entry.Template, err)
	}
	if _, err := repo.Path(entry.Dir); err != nil {
		return project.Fail("%v", err)
	}
	if !repo.IsDir(entry.Dir) {
		return project.Fail("%s (records.%s.dir) is not a folder in %s", entry.Dir, name, proj.Root)
	}
	handed, err := ids.Take(context.Background(), proj, name, 1)
	if err != nil {
		return err
	}
	identifier := handed[0]
	if err := matter.Set("id", identifier); err != nil {
		return project.Fail("%v", err)
	}
	if given {
		if err := matter.Set("card", card); err != nil {
			return project.Fail("%v", err)
		}
	}
	for _, item := range settings {
		if err := matter.Set(item.key, item.value); err != nil {
			return project.Fail("%v", err)
		}
	}
	relative := filepath.Join(entry.Dir, identifier+"-"+slug+".md")
	place, err := repo.Path(relative)
	if err != nil {
		return project.Fail("%v; %s is spent (a gap)", err, identifier)
	}
	err = repo.Create(relative, []byte(matter.String()))
	if errors.Is(err, fs.ErrExist) {
		return project.Fail("%s exists already; %s is spent (a gap): run `jst new` again", place, identifier)
	}
	if err != nil {
		return project.Fail("%s cannot be written (%v); %s is spent (a gap)", place, err, identifier)
	}
	shown := place
	if workDir, err := os.Getwd(); err == nil {
		if realWork, err := filepath.EvalSymlinks(workDir); err == nil {
			if relative, err := filepath.Rel(realWork, place); err == nil {
				shown = relative
			}
		}
	}
	fmt.Fprintln(stdout, shown)
	return nil
}

// Row is one record file read for the table.
type Row struct {
	Number  int
	Matter  series.FrontMatter
	Summary string
	Path    string
}

// Rows are the record files of a series by number, and the files that could not be read with why.
// No folder yet is an empty table.
func Rows(proj project.Project, name string, entry Entry) ([]Row, [][2]string, error) {
	repo := project.Repo{Root: proj.Root}
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `-([0-9]+)-.+\.md$`)
	listing, err := repo.ReadDir(entry.Dir)
	if err != nil {
		return nil, nil, project.Fail("cannot list %s: %v", entry.Dir, err)
	}
	var rows []Row
	var unreadable [][2]string
	for _, item := range listing {
		match := pattern.FindStringSubmatch(item.Name())
		if match == nil {
			continue
		}
		relative := filepath.Join(entry.Dir, item.Name())
		text, err := repo.ReadFile(relative)
		if err != nil {
			unreadable = append(unreadable, [2]string{relative, err.Error()})
			continue
		}
		matter, err := series.ReadFrontMatter(string(text))
		if err != nil {
			unreadable = append(unreadable, [2]string{relative, err.Error()})
			continue
		}
		number, _ := strconv.Atoi(match[1])
		rows = append(rows, Row{Number: number, Matter: matter, Summary: firstLine(matter.Body), Path: relative})
	}
	sort.SliceStable(rows, func(left, right int) bool { return rows[left].Number < rows[right].Number })
	return rows, unreadable, nil
}

// firstLine is the body's first line with text, a heading's `#` taken off.
func firstLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		}
	}
	return ""
}

// cell is a front matter value as a table shows it: a string as it is, nothing for null or absent,
// anything else as JSON on one line; a `|` escaped and line ends made spaces.
func cell(raw []byte) string {
	text := ""
	var value any
	if raw != nil && json.Unmarshal(raw, &value) == nil {
		switch typed := value.(type) {
		case string:
			text = typed
		case nil:
		default:
			text = string(series.OneLine(raw))
		}
	}
	return plain(text)
}

// plain is text made fit for a table cell.
func plain(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "|", `\|`), "\n", " ")
}

func (row Row) cell(column string) string {
	for _, member := range row.Matter.Members {
		if member.Name == column {
			return cell(member.Value)
		}
	}
	return ""
}

func runView(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	return report("view", view(args, stdout, stderr), stderr)
}

func view(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return project.Fail(viewUsage)
	}
	name := args[0]
	found, err := parse(args[1:], []string{"--format"}, []string{"--where"})
	if err != nil {
		return err
	}
	proj, err := project.Here([]string{"records"})
	if err != nil {
		return project.Fail("%v", err)
	}
	entry, err := EntryOf(proj, name)
	if err != nil {
		return err
	}
	shape, given := found.single["--format"]
	if !given {
		shape = "table"
	}
	if shape != "table" && shape != "list" && shape != "json" {
		return project.Fail("--format is table, list or json")
	}
	type condition struct{ key, value string }
	var wanted []condition
	for _, item := range found.repeated["--where"] {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			return project.Fail("--where takes KEY=VALUE: %q", item)
		}
		wanted = append(wanted, condition{key, value})
	}
	all, unreadable, err := Rows(proj, name, entry)
	if err != nil {
		return err
	}
	var rows []Row
	for _, row := range all {
		keep := true
		for _, condition := range wanted {
			if row.cell(condition.key) != condition.value {
				keep = false
			}
		}
		if keep {
			rows = append(rows, row)
		}
	}
	switch shape {
	case "json":
		if err := writeJSON(stdout, rows); err != nil {
			return project.Fail("%v", err)
		}
	case "list":
		for _, row := range rows {
			cells := make([]string, 0, len(entry.Columns))
			for _, column := range entry.Columns {
				cells = append(cells, row.cell(column))
			}
			line := strings.Join(cells, "  ")
			if row.Summary != "" {
				line += "  " + row.Summary
			}
			fmt.Fprintf(stdout, "%s  (%s)\n", line, row.Path)
		}
	default:
		fmt.Fprintf(stdout, "| %s | summary |\n", strings.Join(entry.Columns, " | "))
		fmt.Fprintf(stdout, "|%s\n", strings.Repeat("---|", len(entry.Columns)+1))
		for _, row := range rows {
			cells := make([]string, 0, len(entry.Columns))
			for _, column := range entry.Columns {
				cells = append(cells, row.cell(column))
			}
			fmt.Fprintf(stdout, "| %s | %s |\n", strings.Join(cells, " | "), plain(row.Summary))
		}
	}
	if entry.Before != "" && shape != "json" {
		fmt.Fprintf(stdout, "\n(older rows: %s, frozen; not read here)\n", entry.Before)
	}
	for _, item := range unreadable {
		fmt.Fprintf(stderr, "jst view: %s is not in the table: %s\n", item[0], item[1])
	}
	if len(unreadable) > 0 {
		return &project.Problem{Code: 1} // each file is named above
	}
	return nil
}

// writeJSON is the rows as JSON: each record's front matter with its summary and file.
func writeJSON(stdout io.Writer, rows []Row) error {
	var text strings.Builder
	text.WriteString("[")
	for index, row := range rows {
		if index > 0 {
			text.WriteString(",")
		}
		matter := row.Matter
		members := append([]series.Member(nil), matter.Members...)
		matter.Members = members
		if err := matter.Set("summary", row.Summary); err != nil {
			return err
		}
		if err := matter.Set("file", row.Path); err != nil {
			return err
		}
		text.WriteString("\n  {")
		for position, member := range matter.Members {
			if position > 0 {
				text.WriteString(",")
			}
			name, _ := json.Marshal(member.Name)
			fmt.Fprintf(&text, "\n    %s: %s", name, compact(member.Value))
		}
		text.WriteString("\n  }")
	}
	if len(rows) > 0 {
		text.WriteString("\n")
	}
	text.WriteString("]\n")
	_, err := io.WriteString(stdout, text.String())
	return err
}

func compact(value []byte) string {
	var out bytes.Buffer
	if json.Compact(&out, value) != nil {
		return string(value)
	}
	return out.String()
}
