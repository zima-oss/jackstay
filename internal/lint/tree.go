package lint

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/cards"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/series"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

// Rule ids of the tree's rules (docs/design/implementation.md, sections 4.1 to 4.4).
const (
	RuleDuplicateNumber = "duplicate-number"
	RuleNewCardCommits  = "new-card-commits"
	RuleSnapshotChanged = "snapshot-changed"
	RuleSnapshotDeleted = "snapshot-deleted"
	RuleRecordPlace     = "record-out-of-place"
	RuleFrozenLedger    = "frozen-ledger-row"
	RuleNoCardFolder    = "no-card-files"
)

// Finding is one rule broken: its id and a sentence about it.
type Finding struct {
	Rule string
	Text string
}

// Files is a tree the rules read: every path in it, and a file's content.
type Files interface {
	Paths() ([]string, error)
	Read(filePath string) (data []byte, exists bool, err error)
}

// History answers what the tree's rules ask of the commits behind the tree.
type History interface {
	// Added are the paths ever added under dir in the history of the revisions.
	Added(dir string) ([]string, error)
	// FirstAdded is the commit that first added a path ("" when none did) and the file then.
	FirstAdded(filePath string) (commit string, data []byte, err error)
}

// snapshotName is a snapshot file's name (`jst cards snapshot`).
var snapshotName = regexp.MustCompile(`^landed-[0-9]{4}-[0-9]{2}-[0-9]{2}(?:-[0-9]+)?\.json$`)

// TreeRules judges a tree by the tree rules (docs/design/implementation.md section 4.1). A missing cards folder is an error (nothing is
// guessed), not a finding.
func TreeRules(loaded config.Config, files Files, history History) ([]Finding, error) {
	paths, err := files.Paths()
	if err != nil {
		return nil, err
	}
	var found []Finding
	found = append(found, duplicateNumbers(loaded, paths)...)
	if section := loaded.Section("cards"); section != nil {
		rules, err := cards.FromSection(section)
		if err != nil {
			return nil, err
		}
		if !hasFileUnder(paths, rules.Dir) {
			return nil, fmt.Errorf("there are no card files in %s (cards.dir): nothing is guessed", rules.Dir)
		}
		newCards, err := newCardsWithCommits(section, rules, files, paths)
		if err != nil {
			return nil, err
		}
		found = append(found, newCards...)
		snapshots, err := changedSnapshots(section, files, paths, history)
		if err != nil {
			return nil, err
		}
		found = append(found, snapshots...)
	}
	records, err := recordsInPlace(loaded, files, paths, history)
	if err != nil {
		return nil, err
	}
	return append(found, records...), nil
}

func hasFileUnder(paths []string, dir string) bool {
	if dir == "." {
		return len(paths) > 0
	}
	for _, filePath := range paths {
		if strings.HasPrefix(filePath, dir+"/") {
			return true
		}
	}
	return false
}

func duplicateNumbers(loaded config.Config, paths []string) []Finding {
	ids := loaded.Section("ids")
	names := make([]string, 0, len(ids))
	for name := range ids {
		names = append(names, name)
	}
	sort.Strings(names)
	var found []Finding
	for _, name := range names {
		entry, _ := ids[name].(map[string]any)
		pattern, _ := entry["files"].(string)
		if pattern == "" {
			continue
		}
		width := 1
		if number, ok := entry["width"].(interface{ String() string }); ok {
			if parsed, err := strconv.Atoi(number.String()); err == nil {
				width = parsed
			}
		}
		matcher, err := series.Taken(pattern)
		if err != nil {
			continue // jackstay.json's own check names a bad pattern
		}
		holders, err := series.Numbers(paths, matcher)
		if err != nil {
			// A number too large to count cannot be judged for duplicates: red, the path named.
			found = append(found, Finding{Rule: RuleDuplicateNumber, Text: err.Error()})
			continue
		}
		numbers := make([]int, 0, len(holders))
		for number := range holders {
			numbers = append(numbers, number)
		}
		sort.Ints(numbers)
		for _, number := range numbers {
			if len(holders[number]) > 1 {
				sort.Strings(holders[number])
				found = append(found, Finding{Rule: RuleDuplicateNumber, Text: fmt.Sprintf("%s is the number of %d files: %s (one is renumbered by hand: `jst id next %s`)",
					series.Format(name, width, number), len(holders[number]), strings.Join(holders[number], ", "), name)})
			}
		}
	}
	return found
}

func newCardsWithCommits(section map[string]any, rules cards.Rules, files Files, paths []string) ([]Finding, error) {
	firstNew, _ := section["first_new"].(string)
	if firstNew == "" {
		return nil, nil
	}
	prefix, firstNumber := splitNumber(firstNew)
	var found []Finding
	seen := map[string]bool{}
	for _, filePath := range paths {
		if path.Dir(filePath) != rules.Dir {
			continue
		}
		id := rules.IDOfFile(path.Base(filePath))
		if id == "" || seen[id] {
			continue
		}
		idPrefix, number := splitNumber(id)
		if idPrefix != prefix || number < firstNumber {
			continue
		}
		seen[id] = true
		data, _, err := files.Read(filePath)
		if err != nil {
			return nil, err
		}
		matter, ok := frontMatter(data)
		if !ok {
			return nil, fmt.Errorf("%s: its front matter is not one JSON object between `---` lines", filePath)
		}
		var object map[string]any
		if err := strictjson.Unmarshal(matter, &object); err != nil {
			return nil, fmt.Errorf("%s: its front matter is not one JSON object: %w", filePath, err)
		}
		commits, isList := object["commits"].([]any)
		if !isList || len(commits) > 0 {
			found = append(found, Finding{Rule: RuleNewCardCommits, Text: fmt.Sprintf("%s: a new card (%s, from %s on) keeps `\"commits\": []`; the `Card:` line in the change says what landed for it", filePath, id, firstNew)})
		}
	}
	return found, nil
}

// splitNumber splits "APP-170" into "APP-" and 170 (-1 without a trailing number).
func splitNumber(id string) (string, int) {
	end := len(id)
	start := end
	for start > 0 && id[start-1] >= '0' && id[start-1] <= '9' {
		start--
	}
	if start == end {
		return id, -1
	}
	number, _ := strconv.Atoi(id[start:end])
	return id[:start], number
}

// frontMatter is the text between a file's first two `---` lines.
func frontMatter(data []byte) ([]byte, bool) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, false
	}
	matter, _, found := strings.Cut(text[len("---\n"):], "\n---")
	return []byte(matter), found
}

func changedSnapshots(section map[string]any, files Files, paths []string, history History) ([]Finding, error) {
	dir, _ := section["snapshots"].(string)
	if dir == "" || history == nil {
		return nil, nil
	}
	dir = path.Clean(dir)
	present := map[string]bool{}
	for _, filePath := range paths {
		present[filePath] = true
	}
	var found []Finding
	added, err := history.Added(dir)
	if err != nil {
		return nil, err
	}
	for _, filePath := range added {
		if path.Dir(filePath) == dir && snapshotName.MatchString(path.Base(filePath)) && !present[filePath] {
			found = append(found, Finding{Rule: RuleSnapshotDeleted, Text: filePath + " was deleted: a snapshot is written once and kept"})
		}
	}
	for _, filePath := range paths {
		if path.Dir(filePath) != dir || !snapshotName.MatchString(path.Base(filePath)) {
			continue
		}
		commit, then, err := history.FirstAdded(filePath)
		if err != nil {
			return nil, err
		}
		if commit == "" {
			continue // added by this change
		}
		now, _, err := files.Read(filePath)
		if err != nil {
			return nil, err
		}
		if string(now) != string(then) {
			found = append(found, Finding{Rule: RuleSnapshotChanged, Text: fmt.Sprintf("%s was changed after the commit that added it (%.10s): a snapshot is written once and never edited; a later rewrite adds another file", filePath, commit)})
		}
	}
	return found, nil
}

func recordsInPlace(loaded config.Config, files Files, paths []string, history History) ([]Finding, error) {
	records := loaded.Section("records")
	series := make([]string, 0, len(records))
	for name := range records {
		series = append(series, name)
	}
	sort.Strings(series)
	var found []Finding
	for _, name := range series {
		entry, _ := records[name].(map[string]any)
		dir, _ := entry["dir"].(string)
		dir = strings.Trim(dir, "/")
		recordName := regexp.MustCompile("^" + regexp.QuoteMeta(name) + `-([0-9]+)(?:-.+)?\.md$`)
		var numbers []int
		for _, filePath := range paths {
			parts := recordName.FindStringSubmatch(path.Base(filePath))
			if parts == nil {
				continue
			}
			if path.Dir(filePath) != dir {
				found = append(found, Finding{Rule: RuleRecordPlace, Text: fmt.Sprintf("%s is a %s record outside %s, where its series keeps them", filePath, name, dir)})
				continue
			}
			number, _ := strconv.Atoi(parts[1])
			numbers = append(numbers, number)
		}
		before, _ := entry["before"].(string)
		if before == "" {
			continue
		}
		if history != nil {
			added, err := history.Added(dir)
			if err != nil {
				return nil, err
			}
			for _, filePath := range added {
				if parts := recordName.FindStringSubmatch(path.Base(filePath)); parts != nil && path.Dir(filePath) == dir {
					number, _ := strconv.Atoi(parts[1])
					numbers = append(numbers, number)
				}
			}
		}
		if len(numbers) == 0 {
			continue // no record file ever: the ledger is not frozen yet
		}
		first := numbers[0]
		for _, number := range numbers {
			first = min(first, number)
		}
		text, _, err := files.Read(before)
		if err != nil {
			return nil, err
		}
		mention := regexp.MustCompile(`(?:^|[^A-Za-z0-9-])` + regexp.QuoteMeta(name) + `-([0-9]+)(?:[^0-9]|$)`)
		rows := map[string]bool{}
		for _, line := range strings.Split(string(text), "\n") {
			if !strings.HasPrefix(strings.TrimLeft(line, " \t"), "|") {
				continue
			}
			for _, parts := range mention.FindAllStringSubmatch(line, -1) {
				if number, _ := strconv.Atoi(parts[1]); number >= first {
					rows[name+"-"+parts[1]] = true
				}
			}
		}
		width := 1
		if ids, ok := loaded.Section("ids")[name].(map[string]any); ok {
			if number, ok := ids["width"].(interface{ String() string }); ok {
				width, _ = strconv.Atoi(number.String())
			}
		}
		listed := make([]string, 0, len(rows))
		for row := range rows {
			listed = append(listed, row)
		}
		sort.Strings(listed)
		for _, row := range listed {
			found = append(found, Finding{Rule: RuleFrozenLedger, Text: fmt.Sprintf("%s has a row %s, and the old ledger is frozen: rows from %s-%0*d on are files in %s (`jst new %s --slug …`)",
				before, row, name, width, first, dir, name)})
		}
	}
	return found, nil
}
