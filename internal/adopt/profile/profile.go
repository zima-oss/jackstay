// Package profile reads an organisation's profile for `jst adopt` (docs/design/adopt.md section 6;
// docs/profiles/README.md): one JSON file, data and never code, read strictly. Files it names are
// relative to its own folder and may not leave it.
package profile

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/zima-oss/jackstay/internal/adopt/block"
	"github.com/zima-oss/jackstay/internal/adopt/templates"
	"github.com/zima-oss/jackstay/internal/config"
)

// Format is the profile format this jackstay reads.
const Format = 1

// FileRule is a path the profile requires or forbids.
type FileRule struct {
	Path, Why, Instead string
}

// Profile is a profile as read and checked.
type Profile struct {
	// Path is the file read.
	Path      string
	Name      string
	About     string
	Required  []FileRule
	Forbidden []FileRule
	// Scopes the project must declare, and the programs a scope may start with.
	RequiredScopes []string
	RunWith        []string
	// RequiredKeys are dotted jackstay.json keys; Defaults a jackstay.json object whose keys are added where missing.
	RequiredKeys []string
	Defaults     map[string]any
	// Workflows are the profile's templates, sorted by name.
	Workflows []templates.Template
	Values    map[string]string
	Rules     []string
	LeaveOut  []string
}

// Load reads a profile or an offline bundle saved by jst profile export.
func Load(path string) (*Profile, error) {
	data, err := readProfileFile(path, maxBundleBytes)
	if err != nil {
		return nil, err
	}
	parsed, err := config.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if _, bundled := parsed[bundleVersionKey]; bundled {
		return loadBundle(path, data)
	}
	return readProfile(path, parsed, templatesFromFolder(filepath.Dir(path)))
}

func readProfile(path string, parsed config.Config, readTemplate func(string) ([]byte, error)) (*Profile, error) {
	reader := &reader{readTemplate: readTemplate}
	result := reader.read(parsed)
	result.Path = path
	if len(reader.problems) > 0 {
		return nil, fmt.Errorf("%s: %s", path, strings.Join(reader.problems, "; "))
	}
	return result, nil
}

type reader struct {
	readTemplate func(string) ([]byte, error)
	problems     []string
}

func (reader *reader) fail(format string, values ...any) {
	reader.problems = append(reader.problems, fmt.Sprintf(format, values...))
}

// object checks value is an object with only the allowed keys.
func (reader *reader) object(value any, where string, allowed ...string) map[string]any {
	members, ok := value.(map[string]any)
	if !ok {
		reader.fail("`%s` must be an object", where)
		return nil
	}
	for _, key := range sortedKeys(members) {
		if !slices.Contains(allowed, key) {
			reader.fail("unknown key `%s` (the keys here are: %s)", join(where, key), strings.Join(allowed, ", "))
		}
	}
	return members
}

func (reader *reader) text(value any, where string, required bool) string {
	if value == nil && !required {
		return ""
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		reader.fail("`%s` must be a non-empty string", where)
		return ""
	}
	return text
}

func (reader *reader) texts(value any, where string) []string {
	if value == nil {
		return nil
	}
	list, ok := value.([]any)
	if !ok {
		reader.fail("`%s` must be a list of strings", where)
		return nil
	}
	var out []string
	for index, item := range list {
		out = append(out, reader.text(item, fmt.Sprintf("%s[%d]", where, index), true))
	}
	return out
}

// relative checks a path is relative, clean and does not leave its base.
func (reader *reader) relative(path, where string) bool {
	if path == "" {
		return false
	}
	if filepath.IsAbs(path) || filepath.Clean(path) != path || path == ".." || strings.HasPrefix(path, "../") {
		reader.fail("`%s`: %q must be a clean relative path that stays inside its folder", where, path)
		return false
	}
	return true
}

func (reader *reader) read(parsed map[string]any) *Profile {
	result := &Profile{Values: map[string]string{}}
	top := reader.object(parsed, "", "jackstay_profile", "name", "about", "files", "scopes", "jackstay_json", "workflows", "values", "agents")
	if top == nil {
		return result
	}
	if number, ok := top["jackstay_profile"].(interface{ String() string }); !ok || number.String() != fmt.Sprint(Format) {
		reader.fail("`jackstay_profile` must be %d (the profile format this jackstay reads)", Format)
	}
	result.Name = reader.text(top["name"], "name", true)
	result.About = reader.text(top["about"], "about", false)
	if files, ok := top["files"]; ok {
		members := reader.object(files, "files", "required", "forbidden")
		result.Required = reader.rules(members["required"], "files.required", false)
		result.Forbidden = reader.rules(members["forbidden"], "files.forbidden", true)
	}
	if scopes, ok := top["scopes"]; ok {
		members := reader.object(scopes, "scopes", "required", "run_with")
		result.RequiredScopes = reader.texts(members["required"], "scopes.required")
		result.RunWith = reader.texts(members["run_with"], "scopes.run_with")
	}
	if jackstayJSON, ok := top["jackstay_json"]; ok {
		members := reader.object(jackstayJSON, "jackstay_json", "required", "defaults")
		result.RequiredKeys = reader.texts(members["required"], "jackstay_json.required")
		if defaults, ok := members["defaults"]; ok {
			result.Defaults = reader.defaults(defaults)
		}
	}
	if values, ok := top["values"]; ok {
		members := reader.object(values, "values", sortedKeys(objectOf(values))...)
		for _, name := range sortedKeys(members) {
			if slices.Contains(templates.ValueNames, name) {
				reader.fail("`values.%s` is a name jackstay defines itself", name)
			}
			result.Values[name] = reader.text(members[name], "values."+name, true)
		}
	}
	if workflows, ok := top["workflows"]; ok {
		members := reader.object(workflows, "workflows", sortedKeys(objectOf(workflows))...)
		for _, name := range sortedKeys(members) {
			result.Workflows = append(result.Workflows, reader.workflow(name, members[name], result.Values))
		}
	}
	if agents, ok := top["agents"]; ok {
		members := reader.object(agents, "agents", "rules", "leave_out")
		result.Rules = reader.texts(members["rules"], "agents.rules")
		result.LeaveOut = reader.texts(members["leave_out"], "agents.leave_out")
		for _, id := range result.LeaveOut {
			if id != "" && !slices.Contains(block.ItemIDs(), id) {
				reader.fail("`agents.leave_out` names %q, which is no item of the block (the items: %s)", id, strings.Join(block.ItemIDs(), ", "))
			}
		}
	}
	return result
}

func (reader *reader) rules(value any, where string, forbidden bool) []FileRule {
	if value == nil {
		return nil
	}
	list, ok := value.([]any)
	if !ok {
		reader.fail("`%s` must be a list", where)
		return nil
	}
	var out []FileRule
	for index, item := range list {
		here := fmt.Sprintf("%s[%d]", where, index)
		allowed := []string{"path", "why"}
		if forbidden {
			allowed = append(allowed, "instead")
		}
		members := reader.object(item, here, allowed...)
		rule := FileRule{
			Path: reader.text(members["path"], here+".path", true),
			Why:  reader.text(members["why"], here+".why", true),
		}
		if forbidden {
			rule.Instead = reader.text(members["instead"], here+".instead", true)
		}
		reader.relative(rule.Path, here+".path")
		out = append(out, rule)
	}
	return out
}

func (reader *reader) defaults(value any) map[string]any {
	members, ok := value.(map[string]any)
	if !ok {
		reader.fail("`jackstay_json.defaults` must be an object")
		return nil
	}
	for _, key := range sortedKeys(members) {
		if key == "$schema" || key == "jackstay_schema" || key == "project" || key == "adopt" {
			reader.fail("`jackstay_json.defaults.%s`: a profile does not set %s", key, key)
		}
	}
	// Keys and types only: completeness and rules across keys are judged on the merged file
	// (docs/design/adopt.md section 4), since a default may fill in part of a section the project has.
	for _, problem := range config.TypeProblems(config.Config(members)) {
		reader.fail("`jackstay_json.defaults`: %s", problem)
	}
	return members
}

func (reader *reader) workflow(name string, value any, values map[string]string) templates.Template {
	where := "workflows." + name
	members := reader.object(value, where, "file", "when")
	file := reader.text(members["file"], where+".file", true)
	template := templates.Template{Name: name, When: reader.texts(members["when"], where+".when")}
	for _, section := range template.When {
		if !slices.Contains(config.Sections(), section) {
			reader.fail("`%s.when` names %q, which is no section of jackstay.json", where, section)
		}
	}
	if !validName(name) {
		reader.fail("`%s`: a workflow's name is letters, digits, `-` and `_`", where)
	}
	if !reader.relative(file, where+".file") {
		return template
	}
	data, err := reader.readTemplate(file)
	if err != nil {
		reader.fail("`%s.file`: %v", where, err)
		return template
	}
	template.Text = string(data)
	for _, problem := range templates.PinProblems(template.Text) {
		reader.fail("`%s.file` %s", where, problem)
	}
	for _, placeholder := range templates.Placeholders(template.Text) {
		if _, ok := values[placeholder]; !ok && !slices.Contains(templates.ValueNames, placeholder) {
			reader.fail("`%s.file` uses @@%s@@, which neither jackstay nor `values` defines", where, placeholder)
		}
	}
	return template
}

var workflowName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validName(name string) bool {
	if name == "" {
		return false
	}
	return workflowName.MatchString(name)
}

func objectOf(value any) map[string]any {
	members, _ := value.(map[string]any)
	return members
}

func sortedKeys(members map[string]any) []string {
	keys := make([]string, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func join(where, key string) string {
	if where == "" {
		return key
	}
	return where + "." + key
}
