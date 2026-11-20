package scopes

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/config"
)

type selectionMatch struct {
	pattern string
	scope   string
	paths   []string
}

type selectionResult struct {
	scope     string
	base      string
	matches   []selectionMatch
	unmatched []string
}

func selectionRange(word string) (string, string, error) {
	base, revision, found := strings.Cut(word, "..")
	if !found || base == "" || revision == "" || strings.Contains(revision, "..") || strings.HasPrefix(revision, ".") {
		return "", "", refused("range must be BASE..REV (two commit refs)")
	}
	return base, revision, nil
}

// selectionProblems checks a total, unambiguous ordering. A linear order cannot contain a cycle.
func selectionProblems(section map[string]any) []string {
	raw, present := section["selection"]
	if !present {
		return nil
	}
	selection := objectOf(raw)
	order := textsOf(selection["order"])
	rules := items(selection["rules"])
	scopes := objectOf(section["scopes"])
	var problems []string
	if len(order) == 0 || order[len(order)-1] != "gate" {
		problems = append(problems, "`test.selection.order` must end with `gate` (least to most strict)")
	}
	if !has(scopes, "gate") {
		problems = append(problems, "`test.selection` requires a declared `gate` scope")
	}
	seen := map[string]bool{}
	for _, name := range order {
		if seen[name] {
			problems = append(problems, fmt.Sprintf("`test.selection.order` repeats `%s`; strictness would be ambiguous", name))
		}
		seen[name] = true
		if !has(scopes, name) {
			problems = append(problems, fmt.Sprintf("`test.selection.order` names undeclared scope `%s`", name))
		}
		switch {
		case name == "focused":
			problems = append(problems, "`test.selection.order` cannot use `focused`: it needs test names")
		case textOf(objectOf(scopes[name])["selector"]) != "":
			problems = append(problems, fmt.Sprintf("`test.selection.order` cannot use `%s`: it needs a test ID", name))
		}
	}
	if len(rules) == 0 {
		problems = append(problems, "`test.selection.rules` needs at least one path rule")
	}
	for index, rawRule := range rules {
		rule := objectOf(rawRule)
		pattern, name := textOf(rule["glob"]), textOf(rule["scope"])
		if problem := config.PathGlobProblem(pattern); problem != "" {
			problems = append(problems, fmt.Sprintf("`test.selection.rules[%d].glob`: %s", index, problem))
		}
		if !seen[name] {
			problems = append(problems, fmt.Sprintf("`test.selection.rules[%d].scope` names `%s` outside `test.selection.order`", index, name))
		}
	}
	return problems
}

// selectionMatches treats ** as zero or more complete path segments. Other wildcards stay
// within one segment, so docs/* does not silently cover docs/nested/file.
func selectionMatches(pattern, file string) bool {
	parts := strings.Split(pattern, "/")
	pathParts := strings.Split(file, "/")
	type position struct {
		patternIndex int
		pathIndex    int
	}
	results := make(map[position]bool)
	var match func(int, int) bool
	match = func(patternIndex, pathIndex int) bool {
		current := position{patternIndex, pathIndex}
		if result, found := results[current]; found {
			return result
		}
		var matched bool
		if patternIndex == len(parts) {
			matched = pathIndex == len(pathParts)
		} else if parts[patternIndex] == "**" {
			matched = match(patternIndex+1, pathIndex) || pathIndex < len(pathParts) && match(patternIndex, pathIndex+1)
		} else if pathIndex < len(pathParts) {
			segmentMatches, _ := path.Match(parts[patternIndex], pathParts[pathIndex])
			matched = segmentMatches && match(patternIndex+1, pathIndex+1)
		}
		results[current] = matched
		return matched
	}
	return match(0, 0)
}

func (proj *project) selectScope() (selectionResult, error) {
	selection := objectOf(proj.section["selection"])
	if selection == nil {
		return selectionResult{}, refused("`jst test auto` needs `test.selection` in jackstay.json")
	}
	base := proj.options.base
	if base == "" {
		defaultRef, err := proj.git(proj.ctx, proj.root, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
		if err != nil {
			return selectionResult{}, refused("no known default ref; use `--base REF` for `jst test auto`")
		}
		base = strings.TrimSpace(string(defaultRef))
	}
	if strings.HasPrefix(base, "-") || strings.Contains(base, "..") {
		return selectionResult{}, refused("--base must name one commit, without a range or option prefix")
	}
	resolved, err := proj.git(proj.ctx, proj.root, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return selectionResult{}, refused("base `" + base + "` is not a commit")
	}
	commit := strings.TrimSpace(string(resolved))
	status, err := proj.git(proj.ctx, proj.root, "diff", "--name-status", "-z", "--find-renames", commit+"...HEAD", "--")
	if err != nil {
		return selectionResult{}, refused("cannot compare base `" + base + "` with HEAD: " + err.Error())
	}
	paths, err := changedPaths(status)
	if err != nil {
		return selectionResult{}, refused("cannot read changed paths: " + err.Error())
	}
	if proj.options.commit == "" {
		// The run uses the working tree, so its uncommitted and untracked paths count too.
		porcelain, err := proj.git(proj.ctx, proj.root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
		if err != nil {
			return selectionResult{}, refused("cannot read uncommitted paths: " + err.Error())
		}
		uncommitted, err := statusPaths(porcelain)
		if err != nil {
			return selectionResult{}, refused("cannot read uncommitted paths: " + err.Error())
		}
		for _, file := range uncommitted {
			if !slices.Contains(paths, file) {
				paths = append(paths, file)
			}
		}
	}
	result := selectionResult{scope: "gate", base: base}
	order := textsOf(selection["order"])
	chosenRank := -1
	for _, rawRule := range items(selection["rules"]) {
		rule := objectOf(rawRule)
		matched := selectionMatch{pattern: textOf(rule["glob"]), scope: textOf(rule["scope"])}
		for _, file := range paths {
			if selectionMatches(matched.pattern, file) {
				matched.paths = append(matched.paths, file)
			}
		}
		if len(matched.paths) > 0 {
			result.matches = append(result.matches, matched)
			rank := slices.Index(order, matched.scope)
			if rank > chosenRank {
				result.scope, chosenRank = matched.scope, rank
			}
		}
	}
	for _, file := range paths {
		matched := false
		for _, rule := range result.matches {
			if slices.Contains(rule.paths, file) {
				matched = true
				break
			}
		}
		if !matched {
			result.unmatched = append(result.unmatched, file)
		}
	}
	if len(paths) == 0 || len(result.unmatched) > 0 {
		result.scope = "gate"
	}
	return result, nil
}

// changedPaths reads Git's -z name-status stream, including both paths of copies and renames.
func changedPaths(status []byte) ([]string, error) {
	if len(status) == 0 {
		return nil, nil
	}
	if status[len(status)-1] != 0 {
		return nil, fmt.Errorf("incomplete NUL-delimited Git status")
	}
	fields := strings.Split(string(status[:len(status)-1]), "\x00")
	var paths []string
	for index := 0; index < len(fields); {
		change := fields[index]
		index++
		count := 1
		if strings.HasPrefix(change, "R") || strings.HasPrefix(change, "C") {
			count = 2
		}
		if change == "" || index+count > len(fields) {
			return nil, fmt.Errorf("incomplete Git name-status entry")
		}
		for _, file := range fields[index : index+count] {
			if file == "" {
				return nil, fmt.Errorf("empty path in Git name-status entry")
			}
			paths = append(paths, file)
		}
		index += count
	}
	return paths, nil
}

// statusPaths reads `git status --porcelain=v1 -z --no-renames`: one "XY path" entry per path.
func statusPaths(status []byte) ([]string, error) {
	if len(status) == 0 {
		return nil, nil
	}
	if status[len(status)-1] != 0 {
		return nil, fmt.Errorf("incomplete NUL-delimited Git status")
	}
	var paths []string
	for _, entry := range strings.Split(string(status[:len(status)-1]), "\x00") {
		if len(entry) < 4 || entry[2] != ' ' {
			return nil, fmt.Errorf("unexpected Git status entry %q", entry)
		}
		paths = append(paths, entry[3:])
	}
	return paths, nil
}

func (proj *project) printSelection(choice selectionResult) {
	fmt.Fprintf(proj.stdout, "selection: scope=%s base=%s\n", choice.scope, choice.base)
	for _, rule := range choice.matches {
		fmt.Fprintf(proj.stdout, "  rule: %s -> %s (%s)\n", rule.pattern, rule.scope, joinComma(rule.paths))
	}
	for _, file := range choice.unmatched {
		fmt.Fprintf(proj.stdout, "  unmatched: %s -> gate\n", file)
	}
	if len(choice.matches) == 0 && len(choice.unmatched) == 0 {
		fmt.Fprintln(proj.stdout, "  no changed path -> gate")
	}
	if when := textOf(objectOf(objectOf(proj.section["scopes"])[choice.scope])["when"]); when != "" {
		fmt.Fprintf(proj.stdout, "  when: %s\n", when)
	}
}
