package scopes

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/judge"
)

// The judges and engines a scope names.
var (
	judges  = []string{"swift-test", "steps", "exit", string(judge.GoJSON), string(judge.JUnit)}
	engines = []string{"xctest", "swift-testing"}
	// placeholderNames are the placeholders of a run's words.
	placeholderNames = []string{"tests", "base", "slow", "report"}
)

const (
	kindScope = "scope"
	kindStep  = "step"
	// requiresTimeout is a `requires` check's timeout when it names none (seconds).
	requiresTimeout = 60
)

var (
	// placeholder is `{name}` of lower-case letters and `_`; one after `$` is not one (a shell's
	// `${VAR}`), and `{2}` or `{2,3}` in a filter pattern stay as they are.
	placeholder = regexp.MustCompile(`\{([a-z_]+)\}`)
	stepName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	variable    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// unlockedByDefault scopes take no lock unless they say so; durationsByDefault record durations.
	unlockedByDefault  = []string{"focused", "quick"}
	durationsByDefault = []string{"full", "display"}
	neverDurations     = []string{"focused", "changed", "quick"}
)

// placeholderSpans are the placeholders of a word: [start, end, name start, name end] each.
func placeholderSpans(word string) [][]int {
	var found [][]int
	for _, span := range placeholder.FindAllStringSubmatchIndex(word, -1) {
		if span[0] > 0 && word[span[0]-1] == '$' {
			continue
		}
		found = append(found, span)
	}
	return found
}

// placeholdersOf are the placeholder names the words hold, in order.
func placeholdersOf(words []string) []string {
	var names []string
	for _, word := range words {
		for _, span := range placeholderSpans(word) {
			names = append(names, word[span[2]:span[3]])
		}
	}
	return names
}

// replacePlaceholders is a word with each known placeholder replaced by its value.
func replacePlaceholders(word string, values map[string]string) string {
	var built strings.Builder
	last := 0
	for _, span := range placeholderSpans(word) {
		value, known := values[word[span[2]:span[3]]]
		if !known {
			continue
		}
		built.WriteString(word[last:span[0]])
		built.WriteString(value)
		last = span[1]
	}
	built.WriteString(word[last:])
	return built.String()
}

func objectOf(value any) map[string]any {
	members, _ := value.(map[string]any)
	return members
}

func has(members map[string]any, key string) bool {
	_, present := members[key]
	return present
}

func textsOf(value any) []string {
	elements, _ := value.([]any)
	out := make([]string, 0, len(elements))
	for _, element := range elements {
		if text, ok := element.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func textOf(value any) string {
	text, _ := value.(string)
	return text
}

// numberOf is a number of jackstay.json (a json.Number) or of a decoded line.
func numberOf(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	}
	return 0, false
}

func sortedKeys[Value any](members map[string]Value) []string {
	keys := make([]string, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func joinComma(items []string) string { return strings.Join(items, ", ") }
func joinSpace(items []string) string { return strings.Join(items, " ") }

func joinProblems(problems []string) string { return strings.Join(problems, "; ") }

// isScopeName says whether a scope may be called so: one of the six, or `x-` and a step's name.
func isScopeName(name string) bool {
	if slices.Contains(six, name) {
		return true
	}
	rest, extra := strings.CutPrefix(name, extraPrefix)
	return extra && stepName.MatchString(rest)
}

// Problems are what is wrong with the meaning of a `test` section (its types are internal/config's
// table), as sentences: any one makes every scope `not-started`. `jst check` reports them too.
func Problems(section map[string]any) []string {
	var found []string
	scopes, steps := objectOf(section["scopes"]), objectOf(section["steps"])
	if judge, present := section["judge"]; present && !slices.Contains(judges, textOf(judge)) {
		found = append(found, "`test.judge` must be one of "+joinComma(judges))
	}
	for _, name := range sortedKeys(scopes) {
		if !isScopeName(name) {
			found = append(found, fmt.Sprintf("`test.scopes.%s`: a scope is one of %s, or an extra named `x-…`", name, joinComma(six)))
		}
	}
	for _, name := range sortedKeys(steps) {
		if !stepName.MatchString(name) {
			found = append(found, fmt.Sprintf("`test.steps.%s`: a step's name is letters, digits, `.`, `_` and `-`", name))
		}
		if has(scopes, name) {
			found = append(found, fmt.Sprintf("`%s` is both a scope and a step: a gate's list could mean either", name))
		}
	}
	for index, raw := range items(section["leftovers"]) {
		entry := objectOf(raw)
		if !has(entry, "glob") {
			found = append(found, fmt.Sprintf("`test.leftovers[%d].glob` is missing", index))
			continue
		}
		for _, name := range placeholdersOf([]string{textOf(entry["glob"])}) {
			if name != "run_id" && name != "run_tmp" {
				found = append(found, fmt.Sprintf("`test.leftovers[%d].glob` holds {%s}: only {run_id} and {run_tmp} are known there", index, name))
				break
			}
		}
		if !has(entry, "fail") {
			found = append(found, fmt.Sprintf("`test.leftovers[%d].fail` is missing (true: a leftover fails the run; false: it is listed)", index))
		}
	}
	if expect, present := section["expect"]; present {
		found = append(found, expectProblems(textsOf(expect), "test.expect")...)
	}
	for _, table := range []struct {
		kind    string
		entries map[string]any
	}{{"scopes", scopes}, {"steps", steps}} {
		for _, name := range sortedKeys(table.entries) {
			found = append(found, definitionProblems(section, table.kind, name, objectOf(table.entries[name]))...)
		}
	}
	for _, name := range append(sortedKeys(scopes), sortedKeys(steps)...) {
		entry := objectOf(scopes[name])
		if entry == nil {
			entry = objectOf(steps[name])
		}
		if has(entry, "steps") {
			if _, err := leafPaths(section, name, nil); err != nil {
				found = append(found, err.Error())
			}
		}
	}
	return found
}

func items(value any) []any {
	elements, _ := value.([]any)
	return elements
}

func expectProblems(values []string, where string) []string {
	if len(values) == 0 {
		return []string{fmt.Sprintf("`%s` names no engine (one or both of %s)", where, joinComma(engines))}
	}
	var found []string
	for _, value := range values {
		if !slices.Contains(engines, value) {
			found = append(found, fmt.Sprintf("`%s`: '%s' is not an engine (%s)", where, value, joinComma(engines)))
		}
	}
	return found
}

// listOnlyKeys are the keys that belong to a step running a command, never to a list of steps.
var listOnlyKeys = []string{"judge", "evidence", "veto", "env", "base", "slow", "expect", "cwd", "durations", "timeout", "report", "empty_exit",
	"lock_wait", "lock", "resume"}

func definitionProblems(section map[string]any, kind, name string, entry map[string]any) []string {
	here := fmt.Sprintf("test.%s.%s", kind, name)
	if has(entry, "run") == has(entry, "steps") {
		return []string{fmt.Sprintf("`%s` must have exactly one of `run` and `steps`", here)}
	}
	found := requiresProblems(here, entry)
	if has(entry, "steps") {
		if len(textsOf(entry["steps"])) == 0 {
			found = append(found, fmt.Sprintf("`%s.steps` names no step", here))
		}
		for _, key := range listOnlyKeys {
			if has(entry, key) {
				found = append(found, fmt.Sprintf("`%s.%s` belongs to a step that runs a command, not to a list of steps (each step has its own)", here, key))
			}
		}
		return found
	}
	words := textsOf(entry["run"])
	if len(words) == 0 {
		return append(found, fmt.Sprintf("`%s.run` names no command (an empty list)", here))
	}
	judgeName := judgeOf(section, entry)
	if !slices.Contains(judges, judgeName) {
		found = append(found, fmt.Sprintf("`%s.judge` must be one of %s", here, joinComma(judges)))
	}
	found = append(found, config.TestAdapterProblems(here, judgeName, entry)...)
	used := map[string]bool{}
	for _, token := range placeholdersOf(words) {
		used[token] = true
		if !slices.Contains(placeholderNames, token) {
			found = append(found, fmt.Sprintf("`%s.run` holds {%s}: the placeholders are {tests}, {base}, {slow} and {report}", here, token))
		}
	}
	if used["report"] && (judgeName != string(judge.JUnit) || slices.Contains(placeholdersOf(words[:1]), "report")) {
		found = append(found, fmt.Sprintf("`%s.run` allows {report} only in arguments to a junit producer", here))
	}
	slow, hasSlow := entry["slow"]
	switch {
	case used["slow"] && !hasSlow:
		found = append(found, fmt.Sprintf("`%s.run` holds {slow} and the scope has no `slow` rule (class_seconds and/or case_seconds)", here))
	case hasSlow && len(objectOf(slow)) == 0:
		found = append(found, fmt.Sprintf("`%s.slow` names no rule (class_seconds and/or case_seconds)", here))
	}
	if hasSlow && !used["slow"] {
		found = append(found, fmt.Sprintf("`%s.slow` is a rule for {slow}, which its words do not hold", here))
	}
	if has(entry, "base") && !used["base"] {
		found = append(found, fmt.Sprintf("`%s.base` is for {base}, which its words do not hold", here))
	}
	if judgeName == "steps" {
		evidence := textOf(entry["evidence"])
		if evidence == "" {
			found = append(found, fmt.Sprintf("`%s` has judge `steps` and no `evidence` pattern: it must name the line that counts its tests", here))
		} else if pattern, err := regexp.Compile(evidence); err == nil && pattern.NumSubexp() < 1 {
			found = append(found, fmt.Sprintf("`%s.evidence` has no group: the pattern must capture the number of tests", here))
		}
	}
	if judgeName == "swift-test" && slices.Contains(words, "--parallel") {
		found = append(found, fmt.Sprintf("`%s` runs `swift test --parallel` under the `swift-test` judge: SwiftPM then hides the lines a pass "+
			"is proven by (judge `exit` remains possible: its outcome is exited-0, never passed)", here))
	}
	if expect, present := entry["expect"]; present {
		found = append(found, expectProblems(textsOf(expect), here+".expect")...)
		if judgeName != "swift-test" {
			found = append(found, fmt.Sprintf("`%s.expect` is for the `swift-test` judge", here))
		}
	}
	if cwd, present := entry["cwd"]; present {
		folder := textOf(cwd)
		if filepath.IsAbs(folder) || slices.Contains(strings.Split(folder, "/"), "..") {
			found = append(found, fmt.Sprintf("`%s.cwd` must be a folder inside the checkout, relative to it", here))
		}
	}
	for _, name := range sortedKeys(objectOf(entry["env"])) {
		if !variable.MatchString(name) {
			found = append(found, fmt.Sprintf("`%s.env`: '%s' is not the name of an environment variable", here, name))
		}
		if strings.HasPrefix(name, "JACKSTAY_") {
			found = append(found, fmt.Sprintf("`%s.env`: %s is jackstay's own", here, name))
		}
	}
	if entry["durations"] == true && slices.Contains(neverDurations, name) {
		found = append(found, fmt.Sprintf("`%s.durations`: a `%s` run holds part of a class, so its times never count", here, name))
	}
	return found
}

func requiresProblems(here string, entry map[string]any) []string {
	var found []string
	for index, raw := range items(entry["requires"]) {
		check := objectOf(raw)
		if len(textsOf(check["run"])) == 0 {
			found = append(found, fmt.Sprintf("`%s.requires[%d].run` is missing", here, index))
		}
		if !has(check, "why") {
			found = append(found, fmt.Sprintf("`%s.requires[%d].why` is missing (it is what the run says when the check fails)", here, index))
		}
	}
	return found
}

func judgeOf(section, entry map[string]any) string {
	if judge, present := entry["judge"]; present {
		return textOf(judge)
	}
	if judge, present := section["judge"]; present {
		return textOf(judge)
	}
	return "exit"
}

// find is (kind, entry) of a name a gate lists: a step, else a scope; ("", nil) without one.
func find(section map[string]any, name string) (string, map[string]any) {
	if entry := objectOf(objectOf(section["steps"])[name]); entry != nil {
		return kindStep, entry
	}
	if entry := objectOf(objectOf(section["scopes"])[name]); entry != nil {
		return kindScope, entry
	}
	return "", nil
}

// leafPath is one leaf step of a list (one with `run`) and the lists it was reached through.
type leafPath struct {
	name  string
	kind  string
	entry map[string]any
	trail []string
}

// leafPaths are the leaf steps of a scope or step, in order, expanded recursively. An error on a
// cycle, an unknown name, or a leaf that needs names on the command line (a gate cannot give them).
func leafPaths(section map[string]any, name string, trail []string) ([]leafPath, error) {
	kind, entry := find(section, name)
	path := strings.Join(append(slices.Clone(trail), name), " → ")
	if entry == nil {
		return nil, fmt.Errorf("`test`: %s names '%s', which is neither a step nor a scope", path, name)
	}
	if slices.Contains(trail, name) {
		return nil, fmt.Errorf("`test`: the steps go round in a circle: %s", path)
	}
	if !has(entry, "steps") {
		if len(trail) > 0 && slices.Contains(placeholdersOf(textsOf(entry["run"])), "tests") {
			return nil, fmt.Errorf("`test`: %s: a step whose words hold {tests} cannot be in a list of steps (nothing gives it names)", path)
		}
		return []leafPath{{name: name, kind: kind, entry: entry, trail: slices.Clone(trail)}}, nil
	}
	var found []leafPath
	for _, each := range textsOf(entry["steps"]) {
		more, err := leafPaths(section, each, append(slices.Clone(trail), name))
		if err != nil {
			return nil, err
		}
		found = append(found, more...)
	}
	return found, nil
}

// check is a `requires` check.
type check struct {
	run     []string
	why     string
	timeout float64
}

// slowRule is a `quick` scope's rule for `{slow}`.
type slowRule struct {
	classSeconds, caseSeconds       float64
	hasClassSeconds, hasCaseSeconds bool
}

// leaf is a step or scope that runs a command, its section's defaults filled in.
type leaf struct {
	name, kind string
	// scope is the name when the leaf is a scope ("" for a step).
	scope     string
	run       []string
	cwd       string
	judge     string
	evidence  string
	veto      string
	report    string
	emptyExit int
	// timeout and lockWait are seconds; zero is none.
	timeout   float64
	lockWait  float64
	lock      bool
	env       map[string]string
	base      string
	slow      slowRule
	durations bool
	expect    []string
	resume    bool
	requires  []check
	when      string
}

func secondsOf(value any) float64 {
	number, _ := numberOf(value)
	return number
}

// resolve is a leaf's settings with the section's defaults.
func resolve(section map[string]any, name, kind string, entry map[string]any) leaf {
	resolved := leaf{name: name, kind: kind, run: textsOf(entry["run"]), cwd: ".", judge: judgeOf(section, entry),
		evidence: textOf(entry["evidence"]), veto: textOf(entry["veto"]), lock: !slices.Contains(unlockedByDefault, name),
		env: map[string]string{}, base: textOf(entry["base"]), resume: true, when: textOf(entry["when"])}
	resolved.report = textOf(entry["report"])
	resolved.emptyExit = int(secondsOf(entry["empty_exit"]))
	if kind == kindScope {
		resolved.scope = name
	}
	if cwd, present := entry["cwd"]; present {
		resolved.cwd = textOf(cwd)
	}
	resolved.timeout = secondsOf(section["timeout"])
	if timeout, present := entry["timeout"]; present {
		resolved.timeout = secondsOf(timeout)
	}
	resolved.lockWait = resolved.timeout
	if wait, present := entry["lock_wait"]; present {
		resolved.lockWait = secondsOf(wait)
	}
	if locked, present := entry["lock"].(bool); present {
		resolved.lock = locked
	}
	for variableName, value := range objectOf(entry["env"]) {
		resolved.env[variableName] = textOf(value)
	}
	rule := objectOf(entry["slow"])
	resolved.slow.classSeconds, resolved.slow.hasClassSeconds = numberOf(rule["class_seconds"])
	resolved.slow.caseSeconds, resolved.slow.hasCaseSeconds = numberOf(rule["case_seconds"])
	if resolved.judge == "swift-test" {
		resolved.durations = slices.Contains(durationsByDefault, name)
		if chosen, present := entry["durations"].(bool); present {
			resolved.durations = chosen
		}
	}
	resolved.expect = textsOf(entry["expect"])
	if len(resolved.expect) == 0 {
		resolved.expect = textsOf(section["expect"])
	}
	if len(resolved.expect) == 0 {
		resolved.expect = []string{"xctest"}
	}
	if resume, present := entry["resume"].(bool); present {
		resolved.resume = resume
	}
	resolved.requires = checksOf(entry)
	return resolved
}

func checksOf(entry map[string]any) []check {
	var found []check
	for _, raw := range items(entry["requires"]) {
		member := objectOf(raw)
		timeout := float64(requiresTimeout)
		if chosen, ok := numberOf(member["timeout"]); ok {
			timeout = chosen
		}
		found = append(found, check{run: textsOf(member["run"]), why: textOf(member["why"]), timeout: timeout})
	}
	return found
}

// median of a list that is not empty.
func median(values []float64) float64 {
	ordered := slices.Sorted(slices.Values(values))
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle]
	}
	return (ordered[middle-1] + ordered[middle]) / 2
}
