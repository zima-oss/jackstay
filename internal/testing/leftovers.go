package scopes

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/receipts"
)

// leftoverRule is one `test.leftovers` entry.
type leftoverRule struct {
	glob string
	fail bool
}

func leftoverRules(section map[string]any) []leftoverRule {
	var rules []leftoverRule
	for _, raw := range items(section["leftovers"]) {
		entry := objectOf(raw)
		rules = append(rules, leftoverRule{glob: textOf(entry["glob"]), fail: entry["fail"] == true})
	}
	return rules
}

// attributed says a glob names the run's own token: whatever it matches is this run's.
func (rule leftoverRule) attributed() bool {
	return strings.Contains(rule.glob, "{run_id}") || strings.Contains(rule.glob, "{run_tmp}")
}

// expandGlob is a glob with `{run_id}`, `{run_tmp}`, `$TMPDIR` and `~` expanded.
func expandGlob(pattern, runID, runTmp, tmpdir string) string {
	text := strings.ReplaceAll(pattern, "{run_id}", runID)
	text = strings.ReplaceAll(text, "{run_tmp}", runTmp)
	folder := strings.TrimRight(tmpdir, "/")
	text = strings.ReplaceAll(text, "${TMPDIR}", folder)
	text = strings.ReplaceAll(text, "$TMPDIR", folder)
	return homedir.Expand(text)
}

func hasMagic(part string) bool { return strings.ContainsAny(part, "*?[") }

// fnmatchPattern is a glob part as a regular expression, as Python's fnmatch reads it: `*`, `?`,
// `[…]` with `!` for "not", and everything else itself.
func fnmatchPattern(part string) *regexp.Regexp {
	var built strings.Builder
	built.WriteString("^")
	runes := []rune(part)
	for index := 0; index < len(runes); index++ {
		char := runes[index]
		switch char {
		case '*':
			built.WriteString("(?s:.*)")
		case '?':
			built.WriteString("(?s:.)")
		case '[':
			end := index + 1
			if end < len(runes) && runes[end] == '!' {
				end++
			}
			if end < len(runes) && runes[end] == ']' {
				end++
			}
			for end < len(runes) && runes[end] != ']' {
				end++
			}
			if end >= len(runes) {
				built.WriteString(`\[`)
				continue
			}
			inside := string(runes[index+1 : end])
			index = end
			negate := strings.HasPrefix(inside, "!")
			inside = strings.TrimPrefix(inside, "!")
			inside = strings.ReplaceAll(inside, `\`, `\\`)
			if negate {
				built.WriteString("[^" + inside + "]")
			} else {
				built.WriteString("[" + inside + "]")
			}
		default:
			built.WriteString(regexp.QuoteMeta(string(char)))
		}
	}
	built.WriteString("$")
	compiled, err := regexp.Compile(built.String())
	if err != nil {
		return regexp.MustCompile("^" + regexp.QuoteMeta(part) + "$")
	}
	return compiled
}

// absent says an error means nothing is there (ENOENT, ENOTDIR), not an open question.
func absent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// reasonOf is an error's own words (its errno's text).
func reasonOf(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}

// pathKind is what is at a path ("dir", "other", or "" for nothing), or why it cannot be known.
func pathKind(place string, follow bool) (string, string) {
	var info fs.FileInfo
	var err error
	if follow {
		info, err = os.Stat(place)
	} else {
		info, err = os.Lstat(place)
	}
	switch {
	case err != nil && absent(err):
		return "", ""
	case err != nil:
		return "", fmt.Sprintf("%s cannot be looked at (%s)", place, reasonOf(err))
	case info.IsDir():
		return "dir", ""
	}
	return "other", ""
}

func joinPath(folder, name string) string {
	switch {
	case folder == ".":
		return name
	case strings.HasSuffix(folder, "/"):
		return folder + name
	}
	return folder + "/" + name
}

// globPaths are what a glob matches, part by part, listing every folder it goes through; a folder
// that exists and cannot be listed is a problem, not "nothing there". A name with a leading dot is
// matched only by a part that begins with one.
func globPaths(pattern string) ([]string, string) {
	parts := strings.Split(pattern, "/")
	current := []string{"."}
	if strings.HasPrefix(pattern, "/") {
		current = []string{"/"}
	}
	for index, part := range parts {
		if part == "" && index == 0 {
			continue
		}
		last := index == len(parts)-1
		var following []string
		for _, folder := range current {
			if !hasMagic(part) {
				place := joinPath(folder, part)
				kind, problem := pathKind(place, !last)
				if problem != "" {
					return nil, problem
				}
				if kind != "" && (last || kind == "dir") {
					following = append(following, place)
				}
				continue
			}
			entries, err := os.ReadDir(folder)
			if err != nil {
				if absent(err) {
					continue
				}
				return nil, fmt.Sprintf("%s cannot be listed (%s)", folder, reasonOf(err))
			}
			matcher := fnmatchPattern(part)
			for _, entry := range entries {
				name := entry.Name()
				if strings.HasPrefix(name, ".") && !strings.HasPrefix(part, ".") || !matcher.MatchString(name) {
					continue
				}
				place := joinPath(folder, name)
				if last {
					following = append(following, place)
					continue
				}
				kind, problem := pathKind(place, true)
				if problem != "" {
					return nil, problem
				}
				if kind == "dir" {
					following = append(following, place)
				}
			}
		}
		current = following
		if len(current) == 0 {
			return nil, ""
		}
	}
	return current, ""
}

// listGlob is what a glob matches now with each path's birth, or why it cannot be known.
func listGlob(pattern string) (map[string]time.Time, string) {
	paths, problem := globPaths(pattern)
	if problem != "" {
		return nil, problem
	}
	found := map[string]time.Time{}
	for _, place := range paths {
		info, err := os.Lstat(place)
		if err != nil {
			if absent(err) {
				continue
			}
			return found, fmt.Sprintf("%s cannot be read (%s)", place, reasonOf(err))
		}
		found[place] = bornAt(info)
	}
	return found, ""
}

// leftoverFindings are what a run left behind, by what follows from each.
type leftoverFindings struct {
	failed, unattributed, listed, unreadable, overlap []string
}

// record is the findings as the run's line holds them (nil when there are none).
func (found leftoverFindings) record() map[string]any {
	fields := map[string]any{}
	for name, list := range map[string][]string{"failed": found.failed, "unattributed": found.unattributed, "listed": found.listed,
		"unreadable": found.unreadable, "overlap": found.overlap} {
		if len(list) > 0 {
			fields[name] = list
		}
	}
	if len(found.failed)+len(found.unattributed)+len(found.listed)+len(found.unreadable) == 0 {
		return nil
	}
	return fields
}

// leftovers are the declared globs, looked at before and after one run.
type leftovers struct {
	rules                 []leftoverRule
	runID, runTmp, tmpdir string
	before                map[int]map[string]time.Time
	problemsBefore        map[int]string
	overlapping           func(started, ended time.Time) []string
}

// newLeftovers looks at every glob without the run's token before the run.
func newLeftovers(rules []leftoverRule, runID, runTmp, tmpdir string, overlapping func(started, ended time.Time) []string) *leftovers {
	seen := &leftovers{rules: rules, runID: runID, runTmp: runTmp, tmpdir: tmpdir, before: map[int]map[string]time.Time{},
		problemsBefore: map[int]string{}, overlapping: overlapping}
	for index, rule := range rules {
		if rule.attributed() {
			continue
		}
		found, problem := listGlob(expandGlob(rule.glob, runID, runTmp, tmpdir))
		seen.before[index] = found
		if problem != "" {
			seen.problemsBefore[index] = problem
		}
	}
	return seen
}

// judge is what the run left: an attributed glob's matches are the run's; another glob's new
// entries born inside the run are the run's when no other run overlapped, else unattributed.
func (seen *leftovers) judge(started, ended time.Time) leftoverFindings {
	var found leftoverFindings
	var overlap []string
	looked := false
	for index, rule := range seen.rules {
		matches, problem := listGlob(expandGlob(rule.glob, seen.runID, seen.runTmp, seen.tmpdir))
		if problem == "" {
			problem = seen.problemsBefore[index]
		}
		if problem != "" {
			if rule.fail {
				found.unreadable = append(found.unreadable, rule.glob+": "+problem)
			} else {
				found.listed = append(found.listed, rule.glob+": "+problem)
			}
			continue
		}
		var fresh []string
		for place, born := range matches {
			if rule.attributed() {
				fresh = append(fresh, place)
				continue
			}
			if _, old := seen.before[index][place]; !old && !born.Before(started.Add(-time.Second)) && !born.After(ended.Add(time.Second)) {
				fresh = append(fresh, place)
			}
		}
		sort.Strings(fresh)
		if len(fresh) == 0 {
			continue
		}
		if !rule.attributed() && !looked {
			overlap, looked = seen.overlapping(started, ended), true
			found.overlap = overlap
		}
		switch {
		case rule.attributed() || len(overlap) == 0:
			if rule.fail {
				found.failed = append(found.failed, fresh...)
			} else {
				found.listed = append(found.listed, fresh...)
			}
		case rule.fail:
			found.unattributed = append(found.unattributed, fresh...)
		default:
			found.listed = append(found.listed, fresh...)
		}
	}
	return found
}

// holdsRunTmp says a finding is in the run's own folder, which is then kept.
func (seen *leftovers) holdsRunTmp(found leftoverFindings) bool {
	prefix := strings.TrimRight(seen.runTmp, "/") + "/"
	for _, list := range [][]string{found.failed, found.listed, found.unattributed} {
		for _, place := range list {
			if place == seen.runTmp || strings.HasPrefix(place, prefix) {
				return true
			}
		}
	}
	return false
}

// overlappingRuns are the record's other runs (any project, any session) whose time overlaps
// [started, ended], as `project:run_id`. A run with a start and no end overlaps while its process
// lives; parent is the run this one is nested in.
func overlappingRuns(records []receipts.Record, runID, parent string, started, ended, now time.Time, alive func(int) bool) []string {
	begun, finished := map[string]receipts.Record{}, map[string]receipts.Record{}
	var order []string
	for _, record := range records {
		other := record.Text("run_id")
		if other == "" || other == runID || other == parent || record.Text("inside") == runID {
			continue
		}
		switch record.Text("event") {
		case receipts.EventStart:
			if _, seen := begun[other]; !seen {
				order = append(order, other)
			}
			begun[other] = record
		case receipts.EventRun:
			finished[other] = record
		}
	}
	from, until := epochSeconds(started), epochSeconds(ended)
	var found []string
	for _, other := range order {
		start := begun[other]
		beginning, ok := epochOf(start, "started_epoch")
		if !ok {
			continue
		}
		end := beginning
		if record, done := finished[other]; done {
			if ending, ok := epochOf(record, "ended_epoch"); ok {
				end = ending
			}
		} else if pid, ok := start.Number("pid"); ok && alive(int(pid)) {
			end = epochSeconds(now)
		}
		if beginning < until && end > from {
			found = append(found, start.Text("project")+":"+other)
		}
	}
	return found
}

// epochOf is a record's moment in seconds: the named field, else its `epoch`.
func epochOf(record receipts.Record, name string) (float64, bool) {
	if value, ok := record.Number(name); ok && value != 0 {
		return value, true
	}
	return record.Number("epoch")
}

func epochSeconds(moment time.Time) float64 { return float64(moment.UnixNano()) / 1e9 }
