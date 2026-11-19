// Package config finds, reads and checks a project's jackstay.json (docs/design/implementation.md sections 3.2 and 4,
// testing.md; docs/design/implementation.md section 3). The file is the nearest jackstay.json at or above a folder;
// it is read strictly (one JSON object, no key twice, finite numbers) and checked against one
// table of keys and types. A section that cannot be used is left out of what Load returns; a
// needed one makes Load fail. Regular expressions are RE2 (W-036).
//
// Not yet here: the meaning checks of the `test` section (its types are checked); nothing in Go
// reads that section until `jst test` is written in Go.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/testreport"
	"github.com/zima-oss/jackstay/internal/version"
)

// Name is the project file's name.
const Name = "jackstay.json"

// Schema is the newest jackstay.json this jackstay reads.
const Schema = 2

// Stage1Sections need "jackstay_schema": 2.
var Stage1Sections = []string{"cards", "ids", "records", "changes", "procs", "release", "github", "review", "adopt"}

// Error says jackstay.json is there and cannot be used.
type Error struct{ Message string }

func (err *Error) Error() string { return err.Message }

// Config is a jackstay.json object: its sections by name.
type Config map[string]any

// Section is a section that is an object, or nil.
func (config Config) Section(name string) map[string]any {
	section, _ := config[name].(map[string]any)
	return section
}

// Find is the nearest jackstay.json at or above start: (its folder, its path), or ("", ""). Anything of
// that name stops the search, also a folder or a broken link: that project's file is there and
// cannot be read, so a project above must not stand in for it.
func Find(start string) (string, string) {
	folder, err := filepath.Abs(start)
	if err != nil {
		return "", ""
	}
	if real, err := filepath.EvalSymlinks(folder); err == nil {
		folder = real
	}
	for {
		candidate := filepath.Join(folder, Name)
		if _, err := os.Lstat(candidate); err == nil {
			return folder, candidate
		}
		parent := filepath.Dir(folder)
		if parent == folder {
			return "", ""
		}
		folder = parent
	}
}

// Parse reads jackstay.json's text strictly.
func Parse(data []byte) (Config, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := strictValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("more than one JSON value")
	}
	top, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("not a JSON object")
	}
	return Config(top), nil
}

// strictValue reads one value, refusing a key written twice in an object.
func strictValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			members := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key := keyToken.(string)
				if _, twice := members[key]; twice {
					return nil, fmt.Errorf("the key %q is written twice in one object", key)
				}
				value, err := strictValue(decoder)
				if err != nil {
					return nil, err
				}
				members[key] = value
			}
			_, err := decoder.Token()
			return members, err
		case '[':
			list := []any{}
			for decoder.More() {
				value, err := strictValue(decoder)
				if err != nil {
					return nil, err
				}
				list = append(list, value)
			}
			_, err := decoder.Token()
			return list, err
		}
	case json.Number:
		if _, err := typed.Float64(); err != nil {
			return nil, fmt.Errorf("%s is not a number jackstay.json takes", typed)
		}
		return typed, nil
	}
	return token, nil
}

// Load is (root, usable sections) of the project around start; ("", empty) without a jackstay.json.
// need names the sections the caller reads; nil means every one. The file's own keys are always needed.
func Load(start string, need []string) (string, Config, error) {
	root, path := Find(start)
	if path == "" {
		return "", Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, &Error{path + ": " + err.Error()}
	}
	parsed, err := Parse(data)
	if err != nil {
		return "", nil, &Error{path + ": " + err.Error()}
	}
	problems := SectionProblems(parsed)
	var found []string
	for _, section := range problems.order {
		if need == nil || section == "" || contains(need, section) {
			found = append(found, problems.found[section]...)
		}
	}
	if len(found) > 0 {
		return "", nil, &Error{path + ": " + strings.Join(found, "; ")}
	}
	usable := Config{}
	for key, value := range parsed {
		if len(problems.found[key]) == 0 {
			usable[key] = value
		}
	}
	return root, usable, nil
}

// Problems are what is wrong with a jackstay.json, by section ("" for the file's own keys).
type Problems struct {
	order []string
	found map[string][]string
}

// Of is the problems of one section.
func (problemSet Problems) Of(section string) []string { return problemSet.found[section] }

// Sections lists the sections with a problem, in the order found.
func (problemSet Problems) Sections() []string { return problemSet.order }

func (problemSet *Problems) add(section string, problems ...string) {
	if len(problems) == 0 {
		return
	}
	if _, ok := problemSet.found[section]; !ok {
		problemSet.order = append(problemSet.order, section)
	}
	problemSet.found[section] = append(problemSet.found[section], problems...)
}

func contains(list []string, wanted string) bool {
	for _, item := range list {
		if item == wanted {
			return true
		}
	}
	return false
}

// --- the table of keys ------------------------------------------------------------------------

type kind int

const (
	tStr  kind = iota // a non-empty string
	tText             // any string
	tBool
	tInt
	tNumber // above 0
	tRegex  // RE2
	tList
	tMap // any keys, values of one type
	tObject
)

type schemaType struct {
	class kind
	of    *schemaType
	keys  map[string]*schemaType
}

func str() *schemaType                 { return &schemaType{class: tStr} }
func text() *schemaType                { return &schemaType{class: tText} }
func boolean() *schemaType             { return &schemaType{class: tBool} }
func integer() *schemaType             { return &schemaType{class: tInt} }
func number() *schemaType              { return &schemaType{class: tNumber} }
func regex() *schemaType               { return &schemaType{class: tRegex} }
func list(of *schemaType) *schemaType  { return &schemaType{class: tList, of: of} }
func mapOf(of *schemaType) *schemaType { return &schemaType{class: tMap, of: of} }
func texts() *schemaType               { return list(str()) }
func object(fields ...any) *schemaType {
	result := &schemaType{class: tObject, keys: map[string]*schemaType{}}
	for index := 0; index < len(fields); index += 2 {
		result.keys[fields[index].(string)] = fields[index+1].(*schemaType)
	}
	return result
}

func (schema *schemaType) names() string {
	var out []string
	for key := range schema.keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

var testRun = object(
	"run", texts(), "steps", texts(), "cwd", str(), "judge", str(), "evidence", regex(), "veto", regex(),
	"timeout", number(), "lock_wait", number(), "lock", boolean(), "env", mapOf(text()), "when", str(),
	"base", str(), "slow", object("class_seconds", number(), "case_seconds", number()), "durations", boolean(),
	"expect", texts(), "resume", boolean(),
	"report", str(), "empty_exit", integer(),
	"requires", list(object("run", texts(), "why", str(), "timeout", number())),
)

// Table is the keys jackstay.json takes; a new key comes with the command that reads it, and projects use
// it after the switch to Go (docs/design/implementation.md, section 1).
var Table = object(
	"jackstay_schema", integer(),
	"project", str(),
	"guard", object(
		"regenerable", texts(),
		"deny", list(object("id", str(), "env", mapOf(regex()), "command", regex(), "unless", regex(), "why", str(), "instead", str())),
		// W-053, docs/design/discard-and-clean-gate.md section 9: a level per built-in rule.
		"rules", mapOf(object("level", str(), "paths", texts())),
	),
	"lock", object("enabled", boolean(), "also_hold", list(object("path", str(), "held_env", str()))),
	"receipts", object(
		"inputs", texts(), "extra_files", texts(), "exclude", texts(), "env", texts(), "refs", texts(), "merge_bases", texts(),
		"toolchain", list(texts()), "include_head", boolean(), "no_reuse_branches", texts(), "max_age_hours", number(),
		"commands", list(object("argv", texts(), "argv_prefix", texts(), "reuse", boolean(), "evidence", regex(), "veto", regex(),
			"refs", texts(), "merge_bases", texts(), "judge", str(), "report", str(), "empty_exit", integer())),
	),
	"baseline", object("kinds", list(object("name", str(), "pattern", regex(), "gate", boolean())),
		"merge_unit", str(), "branch", str(), "transcripts", texts()),
	"cards", object("dir", str(), "id", regex(), "unit", str(), "branch", str(), "since", str(), "first_new", str(),
		"exempt_branches", texts(), "snapshots", str()),
	"ids", mapOf(object("files", str(), "width", integer(), "also_in", texts())),
	"records", mapOf(object("dir", str(), "template", str(), "columns", texts(), "before", str())),
	"changes", object("dir", str(), "into", str(), "heading", str(), "template", str(), "newest", str(),
		"internal_into", str()),
	// docs/design/release.md, section 1; the full rules are internal/release's validator.
	"release", object("merge", str(), "branch", str(), "base", str(), "tag", str(), "first", str(),
		"files", list(object("path", str(), "match", regex())), "build", object("path", str(), "match", regex())),
	"procs", object("default_minutes", number(), "max_minutes", number()),
	"trash", object("keep_days", integer()),
	// docs/design/review.md, section 1; the meaning is internal/review's validator.
	"review", object(
		"reviewers", mapOf(object("command", str(), "model", text(), "effort", text(), "timeout_minutes", integer(),
			"isolation", texts())),
		"use", texts(), "brief", text(), "template", text(), "cite", texts(), "severities", texts(), "blocking", texts(),
		"triage", boolean(), "repeat_rounds", integer(), "repeat_severities", texts(), "round_limit", integer(),
		"max_concurrent", integer(), "queue_minutes", integer(), "store", text(), "keep_days", integer(),
	),
	// docs/design/implementation.md, section 8.1: the account jackstay acts as on GitHub; the meaning checks are
	// githubProblems'.
	"github", object("account", str(), "remote", str(), "repository", str(),
		"wait", object("timeout_seconds", number(), "interval_seconds", number(), "log_lines", integer())),
	// docs/design/adopt.md section 3.1: what `jst adopt` keeps in the project's file.
	"adopt", object("pin", str(), "profile", str(), "exceptions", mapOf(str())),
	// docs/design/observability.md, section 10: the journal's retention and how commands are kept.
	"telemetry", object("keep_days", integer(), "compress_after_days", integer(), "commands", str()),
	"test", object("judge", str(), "timeout", number(), "expect", texts(),
		"selection", object("order", texts(), "rules", list(object("glob", str(), "scope", str()))),
		"leftovers", list(object("glob", str(), "fail", boolean())),
		"scopes", mapOf(testRun), "steps", mapOf(testRun)),
)

func isInt(value any) bool {
	parsed, ok := value.(json.Number)
	return ok && !strings.ContainsAny(parsed.String(), ".eE")
}

func numberOf(value any) (float64, bool) {
	parsed, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	float, err := parsed.Float64()
	return float, err == nil
}

func sortedKeys(members map[string]any) []string {
	var out []string
	for key := range members {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func typeProblems(value any, wanted *schemaType, where string) []string {
	switch wanted.class {
	case tStr:
		if content, ok := value.(string); ok && strings.TrimSpace(content) != "" {
			return nil
		}
		return []string{fmt.Sprintf("`%s` must be a non-empty string", where)}
	case tText:
		if _, ok := value.(string); ok {
			return nil
		}
		return []string{fmt.Sprintf("`%s` must be a string", where)}
	case tBool:
		if _, ok := value.(bool); ok {
			return nil
		}
		return []string{fmt.Sprintf("`%s` must be true or false", where)}
	case tInt:
		if isInt(value) {
			return nil
		}
		return []string{fmt.Sprintf("`%s` must be a whole number", where)}
	case tNumber:
		if float, ok := numberOf(value); ok && float > 0 {
			return nil
		}
		return []string{fmt.Sprintf("`%s` must be a number above 0", where)}
	case tRegex:
		pattern, ok := value.(string)
		if !ok || pattern == "" {
			return []string{fmt.Sprintf("`%s` must be a regular expression (a non-empty string)", where)}
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return []string{fmt.Sprintf("`%s` is not a regular expression (RE2): %v", where, err)}
		}
		return nil
	case tList:
		items, ok := value.([]any)
		if !ok {
			return []string{fmt.Sprintf("`%s` must be a list", where)}
		}
		var found []string
		for index, item := range items {
			found = append(found, typeProblems(item, wanted.of, fmt.Sprintf("%s[%d]", where, index))...)
		}
		return found
	case tMap:
		members, ok := value.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("`%s` must be an object", where)}
		}
		var found []string
		for _, key := range sortedKeys(members) {
			found = append(found, typeProblems(members[key], wanted.of, where+"."+key)...)
		}
		return found
	}
	members, ok := value.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("`%s` must be an object", where)}
	}
	var found []string
	for _, key := range sortedKeys(members) {
		here := key
		if where != "" {
			here = where + "." + key
		}
		sub, known := wanted.keys[key]
		if !known {
			found = append(found, fmt.Sprintf("unknown key `%s` (the keys here are: %s)", here, wanted.names()))
			continue
		}
		found = append(found, typeProblems(members[key], sub, here)...)
	}
	return found
}

// TypeProblems are the keys and types of the given sections against the table, nothing more: no
// required key, no rule across keys. For a fragment that is merged into a whole file before the
// whole is judged (a profile's defaults, docs/design/adopt.md section 6).
func TypeProblems(fragment Config) []string {
	var found []string
	for _, key := range sortedKeys(fragment) {
		sub, known := Table.keys[key]
		if !known {
			found = append(found, fmt.Sprintf("unknown key `%s` (the keys here are: %s)", key, Table.names()))
			continue
		}
		found = append(found, typeProblems(fragment[key], sub, key)...)
	}
	return found
}

// SectionProblems is what is wrong with a jackstay.json, section by section: each judged on its own,
// so that a broken section takes nothing away from a command that does not read it.
func SectionProblems(config Config) Problems {
	problems := Problems{found: map[string][]string{}}
	var top []string
	schema, hasSchema := config["jackstay_schema"]
	schemaNumber, _ := numberOf(schema)
	isOne := isInt(schema) && schemaNumber == 1 || schema == true // true counts as 1
	switch {
	case !hasSchema:
		top = append(top, fmt.Sprintf("`\"jackstay_schema\": %d` is missing (it says which jackstay the file was written for)", Schema))
	case isInt(schema) && schemaNumber != 1 && schemaNumber != 2:
		advice := "bring the file up to date"
		if schemaNumber > Schema {
			advice = "upgrade jackstay (`bin/jst install` in its checkout)"
		}
		top = append(top, fmt.Sprintf("`jackstay_schema` is %v and this jackstay (%s) reads 1 and 2: %s", schema, version.Version, advice))
	case isOne:
		var newer []string
		for _, key := range Stage1Sections {
			if _, ok := config[key]; ok {
				newer = append(newer, key)
			}
		}
		if len(newer) > 0 {
			top = append(top, fmt.Sprintf("`%s` need `\"jackstay_schema\": 2`", strings.Join(newer, "`, `")))
		}
	}
	for _, key := range sortedKeys(config) {
		sub, known := Table.keys[key]
		switch {
		case !known:
			top = append(top, fmt.Sprintf("unknown key `%s` (the keys here are: %s)", key, Table.names()))
		case key == "jackstay_schema" || key == "project":
			top = append(top, typeProblems(config[key], sub, key)...)
		}
	}
	problems.add("", top...)
	for _, topProblem := range top {
		if strings.Contains(topProblem, "jackstay_schema") {
			return problems // a file for another jackstay is not read further
		}
	}
	for _, key := range sortedKeys(config) {
		if sub, known := Table.keys[key]; known && key != "jackstay_schema" && key != "project" {
			problems.add(key, typeProblems(config[key], sub, key)...)
		}
	}
	for _, section := range []string{"receipts", "lock", "guard", "baseline", "test", "github", "telemetry"} {
		if _, ok := config[section]; ok && len(problems.found[section]) == 0 {
			problems.add(section, sectionCheck(section, config)...)
		}
	}
	var typed []string
	for _, key := range Stage1Sections {
		if _, ok := config[key]; ok && len(problems.found[key]) == 0 {
			typed = append(typed, key)
		}
	}
	if len(typed) > 0 {
		reduced := Config{}
		for _, key := range typed {
			reduced[key] = config[key]
		}
		for _, problem := range stage1Problems(reduced) {
			if contains(typed, problem.section) {
				problems.add(problem.section, problem.text)
				continue
			}
			for _, typedSection := range typed {
				problems.add(typedSection, problem.text)
			}
		}
	}
	return problems
}

func objectOf(value any) map[string]any {
	members, _ := value.(map[string]any)
	if members == nil {
		return map[string]any{}
	}
	return members
}

func items(value any) []any { elements, _ := value.([]any); return elements }

func stringAt(members map[string]any, key string) string {
	content, _ := members[key].(string)
	return content
}

func has(members map[string]any, key string) bool { _, ok := members[key]; return ok }

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func sectionCheck(section string, config Config) []string {
	var found []string
	switch section {
	case "receipts":
		receipts := objectOf(config["receipts"])
		reuse := false
		for index, rawEntry := range items(receipts["commands"]) {
			entry := objectOf(rawEntry)
			here := fmt.Sprintf("receipts.commands[%d]", index)
			kind := stringAt(entry, "judge")
			structured := kind == "go-json" || kind == "junit"
			if kind != "" && !structured {
				found = append(found, fmt.Sprintf("`%s.judge` must be go-json or junit", here))
			}
			found = append(found, TestAdapterProblems(here, kind, entry)...)
			if has(entry, "argv") == has(entry, "argv_prefix") {
				found = append(found, fmt.Sprintf("`%s` must have exactly one of `argv` and `argv_prefix`", here))
			}
			if len(items(entry["argv"])) == 0 && len(items(entry["argv_prefix"])) == 0 {
				found = append(found, fmt.Sprintf("`%s` names no command (an empty list)", here))
			}
			if entry["reuse"] == true {
				reuse = true
				if !has(entry, "argv") {
					found = append(found, fmt.Sprintf("`%s` has `\"reuse\": true` and so must name the command by `argv`, all its words", here))
				}
				if !has(entry, "evidence") && !structured {
					found = append(found, fmt.Sprintf("`%s` has `\"reuse\": true` and no `evidence` pattern: nothing would show that tests ran", here))
				}
			}
			if has(entry, "evidence") {
				if compiled, err := regexp.Compile(stringAt(entry, "evidence")); err == nil && compiled.NumSubexp() < 1 {
					found = append(found, fmt.Sprintf("`%s.evidence` has no group: the pattern must capture the number of tests", here))
				}
			}
		}
		if reuse && !has(receipts, "toolchain") {
			found = append(found, "`receipts.toolchain` must be written out when a command has `\"reuse\": true`: the commands whose output names the toolchain (`[[\"go\", \"version\"]]`), part of the receipt's key, or [] for none")
		}
		if reuse && !has(receipts, "env") {
			found = append(found, "`receipts.env` must be written out (it may be empty) when a command has `\"reuse\": true`")
		}
	case "lock":
		for index, rawEntry := range items(objectOf(config["lock"])["also_hold"]) {
			entry := objectOf(rawEntry)
			here := fmt.Sprintf("lock.also_hold[%d]", index)
			for _, key := range []string{"path", "held_env"} {
				if !has(entry, key) {
					found = append(found, fmt.Sprintf("`%s.%s` is missing", here, key))
				}
			}
			if has(entry, "path") && !filepath.IsAbs(homedir.Expand(stringAt(entry, "path"))) {
				found = append(found, fmt.Sprintf("`%s.path` must be absolute (or begin `~/`)", here))
			}
			if has(entry, "held_env") && !envName.MatchString(stringAt(entry, "held_env")) {
				found = append(found, fmt.Sprintf("`%s.held_env` must be the name of an environment variable", here))
			}
		}
	case "guard":
		found = append(found, guardRulesProblems(objectOf(objectOf(config["guard"])["rules"]))...)
		for index, rawEntry := range items(objectOf(config["guard"])["regenerable"]) {
			content, _ := rawEntry.(string)
			switch strings.TrimSpace(content) {
			case "*", "**", ".", "/", "..", "*/*", "**/*":
				found = append(found, fmt.Sprintf("`guard.regenerable[%d]` (%q) would make everything deletable", index, content))
			}
		}
	case "baseline":
		baseline := objectOf(config["baseline"])
		seen := map[string]bool{}
		for _, kindEntry := range items(baseline["kinds"]) {
			name := stringAt(objectOf(kindEntry), "name")
			if seen[name] {
				found = append(found, "`baseline.kinds` has two kinds of one name")
				break
			}
			seen[name] = true
		}
		if unit, ok := baseline["merge_unit"]; ok && unit != "pr" && unit != "commit" {
			found = append(found, "`baseline.merge_unit` must be \"pr\" or \"commit\"")
		}
	case "github":
		found = githubProblems(objectOf(config["github"]))
	case "telemetry":
		found = telemetryProblems(objectOf(config["telemetry"]))
	case "test":
		scopes := objectOf(objectOf(config["test"])["scopes"])
		for _, scopeName := range sortedKeys(scopes) {
			if has(objectOf(scopes[scopeName]), "steps") && !has(objectOf(config["receipts"]), "toolchain") {
				found = append(found, "`receipts.toolchain` must be written out when a scope lists steps: the commands whose output names the toolchain (`[[\"go\", \"version\"]]`), part of each step's key, or [] for none")
				break
			}
		}
	}
	return found
}

// TestAdapterProblems checks the settings shared by scope and direct structured judges.
func TestAdapterProblems(where string, kind string, entry map[string]any) []string {
	var problems []string
	if kind == "junit" {
		if !testreport.ValidReportName(stringAt(entry, "report")) {
			problems = append(problems, fmt.Sprintf("`%s.report` must be a single portable file name for the junit judge", where))
		}
		if value, present := entry["empty_exit"]; present {
			number, ok := numberOf(value)
			if !ok || number < 1 || number > 125 {
				problems = append(problems, fmt.Sprintf("`%s.empty_exit` must be an exit status from 1 to 125", where))
			}
		}
	} else if has(entry, "report") || has(entry, "empty_exit") {
		problems = append(problems, fmt.Sprintf("`%s.report` and `%s.empty_exit` require the junit judge", where, where))
	}
	if (kind == "go-json" || kind == "junit") && has(entry, "evidence") {
		problems = append(problems, fmt.Sprintf("`%s.evidence` cannot be combined with a structured judge", where))
	}
	return problems
}

// telemetryProblems are the bounds of the `telemetry` section (docs/design/observability.md, section 10).
func telemetryProblems(section map[string]any) []string {
	var found []string
	days := map[string]float64{"keep_days": 180, "compress_after_days": 14}
	for _, key := range []string{"keep_days", "compress_after_days"} {
		if value, ok := numberOf(section[key]); ok {
			if value < 1 || value > 3650 {
				found = append(found, fmt.Sprintf("`telemetry.%s` must be from 1 to 3650", key))
			}
			days[key] = value
		}
	}
	if days["keep_days"] < days["compress_after_days"] {
		found = append(found, "`telemetry.keep_days` must not be less than `telemetry.compress_after_days`")
	}
	if mode, ok := section["commands"].(string); ok && mode != "masked" && mode != "skeleton" {
		found = append(found, "`telemetry.commands` must be \"masked\" or \"skeleton\"")
	}
	return found
}

type sectionProblem struct{ section, text string }

var (
	seriesName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	dateRE     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	inlineFlag = regexp.MustCompile(`\(\?[aiLmsux]`)
	endsNumber = regexp.MustCompile(`[0-9]+$`)
)

// insideRepo: a path written from the repository's root that stays inside it.
func insideRepo(repoPath string) bool {
	if filepath.IsAbs(repoPath) || strings.Trim(repoPath, "/ ") == "" {
		return false
	}
	for _, part := range strings.Split(strings.ReplaceAll(repoPath, "\\", "/"), "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func stage1Problems(config Config) []sectionProblem {
	var found []sectionProblem
	add := func(section, format string, args ...any) {
		found = append(found, sectionProblem{section, fmt.Sprintf(format, args...)})
	}
	if raw, ok := config["cards"]; ok {
		cards := objectOf(raw)
		for _, key := range []string{"dir", "id", "unit", "since"} {
			if !has(cards, key) {
				add("cards", "`cards.%s` is missing", key)
			}
		}
		if unit, ok := cards["unit"]; ok && unit != "pr" && unit != "commit" {
			add("cards", "`cards.unit` must be \"pr\" or \"commit\"")
		}
		if since, ok := cards["since"].(string); ok {
			if _, err := time.Parse("2006-01-02", since); err != nil || !dateRE.MatchString(since) {
				add("cards", "`cards.since` must be a date, YYYY-MM-DD: %q", since)
			}
		}
		if id := stringAt(cards, "id"); id != "" {
			if inlineFlag.MatchString(id) {
				add("cards", "`cards.id` is used inside larger patterns: write it without inline flags such as (?i)")
				return found
			}
			if compiled, err := regexp.Compile(`^(?:` + id + `)$`); err == nil {
				if compiled.MatchString("") {
					add("cards", "`cards.id` must match a nonempty ID")
				}
				if first := stringAt(cards, "first_new"); first != "" && !compiled.MatchString(first) {
					add("cards", "`cards.first_new` (%q) does not match `cards.id`", first)
				}
			}
		}
		if first, ok := cards["first_new"].(string); ok && !endsNumber.MatchString(first) {
			add("cards", "`cards.first_new` must end in its number")
		}
		for _, key := range []string{"dir", "snapshots"} {
			if repoPath, ok := cards[key].(string); ok && !insideRepo(repoPath) {
				add("cards", "`cards.%s` must be a folder inside the repository, written from its root", key)
			}
		}
	}
	ids := objectOf(config["ids"])
	for _, series := range sortedKeys(ids) {
		entry := objectOf(ids[series])
		here := "ids." + series
		if !seriesName.MatchString(series) {
			add("ids", "`%s`: a series is named by letters and digits, starting with a letter", here)
		}
		files, hasFiles := entry["files"].(string)
		dir := filepath.Dir(files)
		switch {
		case !hasFiles:
			add("ids", "`%s.files` is missing", here)
		case strings.Count(files, "{n}") != 1:
			add("ids", "`%s.files` must hold `{n}` exactly once (where the number is)", here)
		case !insideRepo(files) || strings.Contains(dir, "{n}"):
			add("ids", "`%s.files` must be a path inside the repository with `{n}` in the file's name", here)
		case strings.ContainsAny(dir, "*?["):
			add("ids", "`%s.files` names one folder: a pattern is allowed in the file's name only", here)
		}
		if width, ok := numberOf(entry["width"]); has(entry, "width") && (!ok || width < 1 || width > 9) {
			add("ids", "`%s.width` must be 1 to 9", here)
		}
		for _, alsoIn := range items(entry["also_in"]) {
			if alsoPath, _ := alsoIn.(string); !insideRepo(alsoPath) {
				add("ids", "`%s.also_in` must be a path inside the repository, written from its root: %q", here, alsoPath)
			}
		}
	}
	records := objectOf(config["records"])
	for _, series := range sortedKeys(records) {
		entry := objectOf(records[series])
		here := "records." + series
		if !has(ids, series) {
			add("records", "`%s` has no `ids.%s` entry: a record takes its number from the counter", here, series)
		}
		for _, key := range []string{"dir", "template"} {
			if !has(entry, key) {
				add("records", "`%s.%s` is missing", here, key)
			}
		}
		if files, ok := objectOf(ids[series])["files"].(string); ok && has(entry, "dir") {
			want := strings.Trim(stringAt(entry, "dir"), "/") + "/" + series + "-{n}-*.md"
			if files != want {
				add("ids", "`ids.%s.files` must be `%s`, the name `jst new` writes (it is %s)", series, want, files)
			}
		}
		for _, key := range []string{"dir", "template", "before"} {
			if repoPath, ok := entry[key].(string); ok && !insideRepo(repoPath) {
				add("records", "`%s.%s` must be a path inside the repository, written from its root: %q", here, key, repoPath)
			}
		}
	}
	if raw, ok := config["changes"]; ok {
		changes := objectOf(raw)
		for _, key := range []string{"dir", "into", "heading"} {
			if !has(changes, key) {
				add("changes", "`changes.%s` is missing", key)
			}
		}
		if heading, ok := changes["heading"].(string); ok && !strings.Contains(heading, "{version}") {
			add("changes", "`changes.heading` must hold `{version}`")
		}
		if newest, ok := changes["newest"]; ok && newest != "first" && newest != "last" {
			add("changes", "`changes.newest` must be \"first\" or \"last\"")
		}
		for _, key := range []string{"dir", "into", "template"} {
			if repoPath, ok := changes[key].(string); ok && !insideRepo(repoPath) {
				add("changes", "`changes.%s` must be a path inside the repository, written from its root: %q", key, repoPath)
			}
		}
	}
	procs := objectOf(config["procs"])
	defaultMinutes, okDefault := numberOf(procs["default_minutes"])
	maxMinutes, okMax := numberOf(procs["max_minutes"])
	if okDefault && okMax && defaultMinutes > maxMinutes {
		add("procs", "`procs.default_minutes` is above `procs.max_minutes`")
	}
	return found
}

// ProjectName is the project's name: jackstay.json's `project`, else the main checkout's folder name (a
// worktree's .git file names it), else the folder's own name.
func ProjectName(root string, config Config, start string) string {
	if marked := CopyProject(root, start); marked != "" {
		return marked
	}
	if name, ok := config["project"].(string); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	folder := root
	if folder == "" {
		folder = start
	}
	if data, err := os.ReadFile(filepath.Join(folder, ".git")); err == nil {
		words := strings.TrimSpace(string(data))
		if gitdir, ok := strings.CutPrefix(words, "gitdir:"); ok {
			if main, _, found := strings.Cut(strings.TrimSpace(gitdir), "/.git/worktrees/"); found {
				if base := filepath.Base(main); base != "" && base != "/" {
					return base
				}
				return "unknown"
			}
		}
	}
	real := folder
	if absolute, err := filepath.Abs(folder); err == nil {
		real = absolute // a relative "." names the folder it is
	}
	if resolved, err := filepath.EvalSymlinks(real); err == nil {
		real = resolved
	}
	if base := filepath.Base(real); base != "" && base != "/" && base != "." {
		return base
	}
	return "unknown"
}

// GuardRule is what a project may set of one of the guard's built-in rules (W-053, W-055).
type GuardRule struct {
	// Configurable is false for the rows that say a command cannot be judged.
	Configurable bool
	// Paths says the rule takes path exceptions (the deleting rules).
	Paths bool
	// Default is the level when the project does not set one.
	Default string
}

// Guard levels.
const (
	LevelRefuse = "refuse"
	LevelWarn   = "warn"
	LevelOff    = "off"
)

// GuardRules are the guard's built-in rows by id; the guard's own test pins this list to its table.
var GuardRules = func() map[string]GuardRule {
	rules := map[string]GuardRule{}
	for _, id := range []string{"git-reset-hard", "git-checkout-path", "git-checkout-force", "git-switch-discard",
		"git-restore-worktree", "git-clean-force", "git-rm-force", "git-push-force", "git-push-prune",
		"git-push-delete-protected", "git-branch-force-delete", "git-branch-reset", "git-stash-drop", "git-stash-clear",
		"git-worktree-force", "git-update-ref-delete", "git-update-ref-move", "git-filter-branch", "git-filter-repo",
		"git-read-tree-reset", "git-checkout-index-force", "cards-push"} {
		rules[id] = GuardRule{Configurable: true, Default: LevelRefuse}
	}
	for _, id := range []string{"rm-recursive", "find-delete"} {
		rules[id] = GuardRule{Configurable: true, Paths: true, Default: LevelRefuse}
	}
	rules["zsh-glob"] = GuardRule{Configurable: true, Default: LevelWarn}
	for _, id := range []string{"git-config-alias", "git-config-worktree", "env-split-string", "card-push-unreadable-config"} {
		rules[id] = GuardRule{Default: LevelRefuse}
	}
	return rules
}()

// PatternProblem says why a path exception cannot be used, or "": its fixed folder (the part
// before the first wildcard) must name a folder below the project's top, or below `~/`.
func PatternProblem(pattern string) string {
	text := strings.TrimSpace(pattern)
	if text == "" {
		return "is empty"
	}
	anchored := strings.HasPrefix(text, "~/")
	rest := strings.TrimPrefix(text, "~/")
	if !anchored && strings.HasPrefix(text, "/") {
		return "is absolute: write it from the project's top, or beginning `~/`"
	}
	var fixed []string
	for _, part := range strings.Split(rest, "/") {
		if strings.ContainsAny(part, "*?[") {
			break
		}
		fixed = append(fixed, part)
	}
	for _, part := range fixed {
		if part == "" || part == "." || part == ".." {
			return "has an empty, `.` or `..` part before its first wildcard"
		}
	}
	switch {
	case len(fixed) == 0 && anchored:
		return "names no folder below `~/`: it would exempt everything in the home folder"
	case len(fixed) == 0:
		return "names no folder below the project's top: it would exempt everything in it"
	case !anchored && fixed[0] == ".git":
		return "is inside `.git`"
	}
	return ""
}

func guardRulesProblems(rules map[string]any) []string {
	var found []string
	for _, id := range sortedKeys(rules) {
		here := "guard.rules." + id
		rule, known := GuardRules[id]
		switch {
		case !known:
			found = append(found, fmt.Sprintf("`%s`: no built-in rule has this id", here))
			continue
		case !rule.Configurable:
			found = append(found, fmt.Sprintf("`%s`: this rule says a command cannot be judged, so its level is not set", here))
			continue
		}
		entry := objectOf(rules[id])
		if level, ok := entry["level"]; ok && level != LevelRefuse && level != LevelWarn && level != LevelOff {
			found = append(found, fmt.Sprintf("`%s.level` must be \"refuse\", \"warn\" or \"off\"", here))
		}
		if _, ok := entry["paths"]; ok && !rule.Paths {
			found = append(found, fmt.Sprintf("`%s.paths`: only rm-recursive and find-delete take path exceptions", here))
		}
		for index, raw := range items(entry["paths"]) {
			text, _ := raw.(string)
			if problem := PatternProblem(text); problem != "" {
				found = append(found, fmt.Sprintf("`%s.paths[%d]` (%q) %s", here, index, text, problem))
			}
		}
	}
	return found
}

// CopyMarker is the file in a clean copy's git folder that names the project it is a copy of
// (W-052, docs/design/discard-and-clean-gate.md section 3): every jackstay command run inside the copy
// takes that name, as in the checkout it was made from.
const CopyMarker = "jackstay-project"

// CopyProject is the name a clean copy's marker gives, or "" when the folder is not one.
func CopyProject(root, start string) string {
	folder := root
	if folder == "" {
		folder = start
	}
	if folder == "" {
		return ""
	}
	if absolute, err := filepath.Abs(folder); err == nil {
		folder = absolute
	}
	for {
		gitFolder := filepath.Join(folder, ".git")
		if info, err := os.Lstat(gitFolder); err == nil {
			if !info.IsDir() {
				return ""
			}
			data, err := os.ReadFile(filepath.Join(gitFolder, CopyMarker)) // #nosec G304 -- a marker jackstay writes
			if err != nil || len(data) > 256 {
				return ""
			}
			return strings.TrimSpace(string(data))
		}
		parent := filepath.Dir(folder)
		if parent == folder {
			return ""
		}
		folder = parent
	}
}

// NotRE2 lists every pattern of a jackstay.json that RE2 (Go's dialect, W-036) does not take, with
// where it is and why; whatever else is wrong with the file is not its concern.
func NotRE2(config Config) []string {
	var found []string
	var walk func(value any, schema *schemaType, where string)
	walk = func(value any, schema *schemaType, where string) {
		switch schema.class {
		case tRegex:
			if pattern, ok := value.(string); ok && pattern != "" {
				if _, err := regexp.Compile(pattern); err != nil {
					found = append(found, fmt.Sprintf("%s: %q: %v", where, pattern, err))
				}
			}
		case tList:
			for index, item := range items(value) {
				walk(item, schema.of, fmt.Sprintf("%s[%d]", where, index))
			}
		case tMap:
			members := objectOf(value)
			for _, key := range sortedKeys(members) {
				walk(members[key], schema.of, where+"."+key)
			}
		case tObject:
			members := objectOf(value)
			for _, key := range sortedKeys(members) {
				if sub, ok := schema.keys[key]; ok {
					here := key
					if where != "" {
						here = where + "." + key
					}
					walk(members[key], sub, here)
				}
			}
		}
	}
	walk(map[string]any(config), Table, "")
	return found
}

// --- the github section (docs/design/implementation.md, section 8.1) ----------------------------------------

var (
	// githubLogin is a GitHub login: letters, digits and single hyphens, neither first nor last.
	githubLogin = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9])*$`)
	// githubRepositoryName is the name half of owner/name.
	githubRepositoryName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// remoteName is a name git takes for a remote (git check-ref-format's rules, conservatively).
	remoteName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]*$`)
)

// githubLoginLength is the longest login GitHub gives.
const githubLoginLength = 39

// IsGitHubLogin says whether a name can be a GitHub login.
func IsGitHubLogin(name string) bool {
	return len(name) <= githubLoginLength && githubLogin.MatchString(name)
}

// IsGitHubRepository says whether a name is owner/name.
func IsGitHubRepository(name string) bool {
	owner, repository, found := strings.Cut(name, "/")
	return found && IsGitHubLogin(owner) && githubRepositoryName.MatchString(repository) &&
		repository != "." && repository != ".."
}

func githubProblems(section map[string]any) []string {
	var found []string
	account, hasAccount := section["account"].(string)
	switch {
	case !has(section, "account"):
		found = append(found, "`github.account` is missing (the GitHub login jackstay acts as on this project)")
	case hasAccount && !IsGitHubLogin(account):
		found = append(found, fmt.Sprintf("`github.account` is not a GitHub login (letters, digits, single hyphens, at most %d): %q", githubLoginLength, account))
	}
	if remote, ok := section["remote"].(string); ok {
		if !remoteName.MatchString(remote) || strings.Contains(remote, "..") || strings.HasSuffix(remote, "/") ||
			strings.HasSuffix(remote, ".lock") || strings.Contains(remote, "//") {
			found = append(found, fmt.Sprintf("`github.remote` is not a remote's name: %q", remote))
		}
	}
	if repository, ok := section["repository"].(string); ok && !IsGitHubRepository(repository) {
		found = append(found, fmt.Sprintf("`github.repository` must be owner/name: %q", repository))
	}
	if lines, ok := numberOf(objectOf(section["wait"])["log_lines"]); ok && lines < 1 {
		found = append(found, "`github.wait.log_lines` must be a whole number above 0")
	}
	return found
}

// GitHub is a usable `github` section.
type GitHub struct {
	Account    string
	Remote     string // "" when not set
	Repository string // owner/name; "" when not set
	Wait       GitHubWait
}

// GitHubWait is `github.wait`; a zero field was not set.
type GitHubWait struct {
	TimeoutSeconds  float64
	IntervalSeconds float64
	LogLines        int
}

// GitHub is the `github` section as Load left it: (the section, true), or false when the file has
// none (Load leaves an unusable section out; a caller that reads it names it in need).
func (config Config) GitHub() (GitHub, bool) {
	section := config.Section("github")
	if section == nil {
		return GitHub{}, false
	}
	wait := objectOf(section["wait"])
	settings := GitHub{Account: stringAt(section, "account"), Remote: stringAt(section, "remote"),
		Repository: stringAt(section, "repository")}
	settings.Wait.TimeoutSeconds, _ = numberOf(wait["timeout_seconds"])
	settings.Wait.IntervalSeconds, _ = numberOf(wait["interval_seconds"])
	if lines, ok := numberOf(wait["log_lines"]); ok {
		settings.Wait.LogLines = int(lines)
	}
	return settings, true
}

// Sections is every section jackstay.json takes, for a caller that needs all but some (Load's need).
func Sections(except ...string) []string {
	var out []string
	for key := range Table.keys {
		if key != "jackstay_schema" && key != "project" && !contains(except, key) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}
