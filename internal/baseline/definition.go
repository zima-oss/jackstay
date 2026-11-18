package baseline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/config"
)

// A Kind is a name and a pattern over one simple command; gate kinds stand before a merge.
type Kind struct {
	Name    string
	Pattern *regexp.Regexp
	Gate    bool
}

// A Definition is what is counted for one project.
type Definition struct {
	Project     string
	Kinds       []Kind
	MergeUnit   string // "pr" or "commit"
	Branch      string
	Transcripts []string // patterns over folder names under the projects folder
	Source      string   // the file the section came from, or "none"
}

// Flags are what the command line says in place of the section.
type Flags struct {
	Kinds       []string // NAME=REGEX
	Gates       []string
	MergeUnit   string
	Branch      string
	Transcripts []string
}

// DefinitionError is a definition (jackstay.json, --config or the flags) that cannot be used.
type DefinitionError struct{ Message string }

func (problem *DefinitionError) Error() string { return problem.Message }

func definitionError(format string, values ...any) error {
	return &DefinitionError{Message: fmt.Sprintf(format, values...)}
}

var sectionKeys = []string{"kinds", "merge_unit", "branch", "transcripts"}

// checkSection says what is wrong with a `baseline` section, or nil.
func checkSection(raw any, where string) (map[string]any, error) {
	section, isObject := raw.(map[string]any)
	if !isObject {
		return nil, definitionError("%s: `baseline` is not an object", where)
	}
	var unknown []string
	for key := range section {
		if !slices.Contains(sectionKeys, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return nil, definitionError("%s: `baseline` has keys that mean nothing here: %s", where, strings.Join(unknown, ", "))
	}
	kinds, isList := listOf(section, "kinds")
	if !isList {
		return nil, definitionError("%s: `baseline.kinds` is not a list", where)
	}
	names := map[string]bool{}
	for _, entry := range kinds {
		kind, isObject := entry.(map[string]any)
		name, named := kind["name"].(string)
		pattern, patterned := kind["pattern"].(string)
		if !isObject || !named || strings.TrimSpace(name) == "" || !patterned {
			return nil, definitionError("%s: every entry of `baseline.kinds` needs a `name` and a `pattern`, both strings: %v", where, entry)
		}
		gate, hasGate := kind["gate"]
		_, gateIsBool := gate.(bool)
		for key := range kind {
			if !slices.Contains([]string{"name", "pattern", "gate"}, key) || (hasGate && !gateIsBool) {
				return nil, definitionError("%s: a kind holds `name`, `pattern` and `gate` (true or false): %v", where, entry)
			}
		}
		if names[name] {
			return nil, definitionError("%s: the kind `%s` is listed twice", where, name)
		}
		names[name] = true
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, definitionError("%s: the pattern of `%s` is not a regular expression (RE2): %v", where, name, err)
		}
	}
	if unit, given := section["merge_unit"]; given && unit != "pr" && unit != "commit" {
		return nil, definitionError("%s: `baseline.merge_unit` is \"pr\" or \"commit\"", where)
	}
	if branch, given := section["branch"]; given {
		if text, isText := branch.(string); !isText || strings.TrimSpace(text) == "" {
			return nil, definitionError("%s: `baseline.branch` is a branch's name", where)
		}
	}
	if globs, given := section["transcripts"]; given {
		list, isList := globs.([]any)
		for _, glob := range list {
			if text, isText := glob.(string); !isText || text == "" || strings.Contains(text, "/") {
				isList = false
			}
		}
		if !isList {
			return nil, definitionError("%s: `baseline.transcripts` is a list of patterns over folder names (no `/`)", where)
		}
	}
	return section, nil
}

// listOf is a key's list ([] when absent).
func listOf(section map[string]any, key string) ([]any, bool) {
	value, given := section[key]
	if !given {
		return nil, true
	}
	list, isList := value.([]any)
	return list, isList
}

func textsOf(value any) []string {
	list, _ := value.([]any)
	texts := make([]string, 0, len(list))
	for _, item := range list {
		if text, isText := item.(string); isText {
			texts = append(texts, text)
		}
	}
	return texts
}

// FolderName is the name Claude Code gives a working folder's sessions under its projects folder.
func FolderName(folder string) string {
	absolute, err := filepath.Abs(folder)
	if err != nil {
		absolute = folder
	}
	var built strings.Builder
	for _, char := range absolute {
		if char < 128 && (char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9') {
			built.WriteRune(char)
		} else {
			built.WriteByte('-')
		}
	}
	return built.String()
}

// joined is a file name after a folder, as the folder was written.
func joined(folder, name string) string {
	if folder == "" || strings.HasSuffix(folder, "/") {
		return folder + name
	}
	return folder + "/" + name
}

// readFile is the whole file at path, or the `baseline` section of it, with where it came from.
func readFile(path string, given bool) (whole config.Config, section map[string]any, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, definitionError("%s: %v", path, err)
	}
	whole, err = config.Parse(data)
	if err != nil {
		return nil, nil, definitionError("%s: %v", path, err)
	}
	_, hasSchema := whole["jackstay_schema"]
	if !given || hasSchema {
		// The file's own keys and the section read here are judged; another section is not this
		// command's concern (the file is judged per section, docs/design/implementation.md section 3.2).
		problems := config.SectionProblems(whole)
		found := append(slices.Clone(problems.Of("")), problems.Of("baseline")...)
		if len(found) > 0 {
			return nil, nil, definitionError("%s: %s", path, strings.Join(found, "; "))
		}
	}
	if raw, present := whole["baseline"]; present {
		section, err = checkSection(raw, path)
		return whole, section, err
	}
	_, hasKinds := whole["kinds"]
	_, hasTranscripts := whole["transcripts"]
	if given && (hasKinds || hasTranscripts) {
		section, err = checkSection(map[string]any(whole), path) // a file that is the section itself
		return config.Config{}, section, err
	}
	if given {
		return nil, nil, definitionError("%s: no `baseline` section", path)
	}
	return whole, nil, nil
}

// Define is what to count for a project: the section of --config, else of <repo>/jackstay.json, else
// none; flags replace what they name.
func Define(project, repo, configFile string, flags Flags) (Definition, error) {
	path := configFile
	if path == "" {
		path = joined(repo, config.Name)
	}
	whole, section, source := config.Config{}, map[string]any(nil), "none"
	if _, err := os.Lstat(path); configFile != "" || err == nil {
		var readErr error
		whole, section, readErr = readFile(path, configFile != "")
		if readErr != nil {
			return Definition{}, readErr
		}
		if section != nil {
			source = path
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Definition{}, definitionError("%s: %v", path, err)
	}
	if project == "" {
		// config.ProjectName takes the base name of the folder as written: an absolute path names it.
		folder, err := filepath.Abs(repo)
		if err != nil {
			folder = repo
		}
		project = config.ProjectName(folder, whole, folder)
	}
	if section == nil {
		section = map[string]any{}
	}
	kinds := copiedObjects(section["kinds"])
	if len(flags.Kinds) > 0 {
		kinds = kinds[:0]
		for _, word := range flags.Kinds {
			name, pattern, found := strings.Cut(word, "=")
			if !found || name == "" || pattern == "" {
				return Definition{}, definitionError("--kind takes NAME=REGEX: %q", word)
			}
			kinds = append(kinds, map[string]any{"name": name, "pattern": pattern})
		}
	}
	if len(flags.Gates) > 0 {
		var missing []string
		for _, gate := range flags.Gates {
			if !slices.ContainsFunc(kinds, func(kind map[string]any) bool { return kind["name"] == gate }) && !slices.Contains(missing, gate) {
				missing = append(missing, gate)
			}
		}
		if len(missing) > 0 {
			slices.Sort(missing)
			return Definition{}, definitionError("--gate names no kind: %s", strings.Join(missing, ", "))
		}
		for _, kind := range kinds {
			kind["gate"] = slices.Contains(flags.Gates, kind["name"].(string))
		}
	}
	chosen := Definition{Project: project, MergeUnit: "pr", Branch: "main", Source: source}
	if unit, isText := section["merge_unit"].(string); isText {
		chosen.MergeUnit = unit
	}
	if flags.MergeUnit != "" {
		chosen.MergeUnit = flags.MergeUnit
	}
	if branch, isText := section["branch"].(string); isText {
		chosen.Branch = branch
	}
	if flags.Branch != "" {
		chosen.Branch = flags.Branch
	}
	chosen.Transcripts = flags.Transcripts
	if len(chosen.Transcripts) == 0 {
		chosen.Transcripts = textsOf(section["transcripts"])
	}
	if len(chosen.Transcripts) == 0 {
		chosen.Transcripts = []string{FolderName(repo), FolderName(repo) + "--claude-worktrees-*"}
	}
	where := source
	if len(flags.Kinds) > 0 || len(flags.Transcripts) > 0 {
		where = "the flags"
	}
	listed := make([]any, 0, len(kinds))
	for _, kind := range kinds {
		listed = append(listed, kind)
	}
	globs := make([]any, 0, len(chosen.Transcripts))
	for _, glob := range chosen.Transcripts {
		globs = append(globs, glob)
	}
	if _, err := checkSection(map[string]any{"kinds": listed, "merge_unit": chosen.MergeUnit, "branch": chosen.Branch,
		"transcripts": globs}, where); err != nil {
		return Definition{}, err
	}
	for _, kind := range kinds {
		gate, _ := kind["gate"].(bool)
		chosen.Kinds = append(chosen.Kinds, Kind{Name: kind["name"].(string), Pattern: regexp.MustCompile(kind["pattern"].(string)), Gate: gate})
	}
	return chosen, nil
}

// copiedObjects is a list's objects, each copied (the flags may mark them).
func copiedObjects(value any) []map[string]any {
	list, _ := value.([]any)
	found := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if object, isObject := item.(map[string]any); isObject {
			copied := make(map[string]any, len(object))
			for key, member := range object {
				copied[key] = member
			}
			found = append(found, copied)
		}
	}
	return found
}
