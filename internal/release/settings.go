// Package release is `jst release`: the next version computed from typed entries, the release
// request, the release commit and its proof, prepare, and the workflow's two commands that keep
// the release pull request and publish (docs/design/release.md). Designed from that spec (W-036).
package release

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/config"
)

// VersionFile is a file whose text holds a version (or a build number) in a pattern's named group.
type VersionFile struct {
	Path  string
	Match *regexp.Regexp
	Group string // "version" or "build"
}

// Settings are a project's `release` section with its `changes` section.
type Settings struct {
	Changes changes.Settings
	Merge   string // "owner" or "agents"
	Branch  string // the release pull request's head branch
	Base    string // the branch releases land on
	Tag     string // the tag's name, with {version}
	First   Version
	Files   []VersionFile
	Build   *VersionFile
}

// BaseOf is the effective base branch used by release commands and generated workflows.
func BaseOf(loaded config.Config) string {
	if branch, _ := loaded.Section("release")["base"].(string); branch != "" {
		return branch
	}
	if branch, _ := loaded.Section("cards")["branch"].(string); branch != "" {
		return branch
	}
	return "main"
}

// SettingsOf reads the `release` and `changes` sections of a jackstay.json. Problems are the rules of
// docs/design/release.md section 1 that need no repository; the ones that do are Validate's.
func SettingsOf(loaded config.Config) (Settings, []string) {
	section := loaded.Section("release")
	if section == nil {
		return Settings{}, []string{"jackstay.json has no `release` section"}
	}
	var problems []string
	changeSettings, err := changes.SettingsOf(loaded)
	if err != nil {
		return Settings{}, []string{"`release` needs a `changes` section"}
	}
	text := func(key, fallback string) string {
		if value, ok := section[key].(string); ok && value != "" {
			return value
		}
		return fallback
	}
	settings := Settings{
		Changes: changeSettings,
		Merge:   text("merge", "owner"),
		Branch:  text("branch", "jackstay/release"),
		Base:    BaseOf(loaded),
		Tag:     text("tag", "v{version}"),
	}
	if settings.Merge != "owner" && settings.Merge != "agents" {
		problems = append(problems, fmt.Sprintf("`release.merge` is %q: \"owner\" or \"agents\"", settings.Merge))
	}
	if settings.Branch == settings.Base {
		problems = append(problems, "`release.branch` and `release.base` are the same branch")
	}
	if strings.Count(settings.Tag, "{version}") != 1 {
		problems = append(problems, "`release.tag` must hold `{version}` exactly once")
	}
	for key, name := range map[string]string{"release.tag": settings.Tag, "release.branch": settings.Branch, "release.base": settings.Base} {
		if strings.HasPrefix(name, "-") {
			problems = append(problems, fmt.Sprintf("`%s` must not begin with `-` (git would read it as an option)", key))
		}
	}
	first, err := ParseVersion(text("first", ""))
	if err != nil {
		problems = append(problems, "`release.first`: "+err.Error())
	}
	settings.First = first
	files, _ := section["files"].([]any)
	if len(files) == 0 {
		problems = append(problems, "`release.files` names no version file")
	}
	for index, raw := range files {
		file, problem := versionFile(raw, "version", fmt.Sprintf("release.files[%d]", index))
		if problem != "" {
			problems = append(problems, problem)
			continue
		}
		settings.Files = append(settings.Files, file)
	}
	if raw, ok := section["build"]; ok {
		file, problem := versionFile(raw, "build", "release.build")
		if problem != "" {
			problems = append(problems, problem)
		} else {
			settings.Build = &file
		}
	}
	problems = append(problems, pathProblems(settings)...)
	return settings, problems
}

// versionFile reads one {path, match} object; its pattern must have exactly one group named group.
func versionFile(raw any, group, where string) (VersionFile, string) {
	object, _ := raw.(map[string]any)
	filePath, _ := object["path"].(string)
	match, _ := object["match"].(string)
	if filePath == "" || match == "" {
		return VersionFile{}, fmt.Sprintf("`%s` needs path and match", where)
	}
	compiled, err := regexp.Compile("(?m)" + match)
	if err != nil {
		return VersionFile{}, fmt.Sprintf("`%s.match` is not an RE2 pattern: %v", where, err)
	}
	named := 0
	for _, name := range compiled.SubexpNames() {
		if name == group {
			named++
		}
	}
	if named != 1 {
		return VersionFile{}, fmt.Sprintf("`%s.match` must have exactly one group named %s, (?P<%s>…)", where, group, group)
	}
	return VersionFile{Path: path.Clean(filePath), Match: compiled, Group: group}, ""
}

// pathProblems are the rules about the outputs' paths: none named twice, none in the entries'
// folder, never jackstay.json or anything under .github.
func pathProblems(settings Settings) []string {
	var problems []string
	seen := map[string]string{}
	name := func(filePath, key string) {
		if filePath == "" {
			return
		}
		clean := path.Clean(filePath)
		if other, taken := seen[clean]; taken {
			problems = append(problems, fmt.Sprintf("`%s` and `%s` name the same file %s", other, key, clean))
		}
		seen[clean] = key
		if strings.HasPrefix(clean, "../") || clean == ".." || path.IsAbs(clean) {
			problems = append(problems, fmt.Sprintf("`%s` (%s) is not a path inside the repository", key, clean))
		}
		if clean == config.Name || clean == ".github" || strings.HasPrefix(clean, ".github/") {
			problems = append(problems, fmt.Sprintf("`%s` (%s): a release changes no configuration and no workflow", key, clean))
		}
		if dir := path.Clean(settings.Changes.Dir); clean == dir || strings.HasPrefix(clean, dir+"/") {
			problems = append(problems, fmt.Sprintf("`%s` (%s) is inside `changes.dir`", key, clean))
		}
	}
	for index, file := range settings.Files {
		name(file.Path, fmt.Sprintf("release.files[%d]", index))
	}
	if settings.Build != nil {
		name(settings.Build.Path, "release.build")
	}
	name(settings.Changes.Into, "changes.into")
	name(settings.Changes.InternalInto, "changes.internal_into")
	return problems
}

// TagName is the tag of a version.
func (settings Settings) TagName(version Version) string {
	return strings.Replace(settings.Tag, "{version}", version.String(), 1)
}

// VersionOfTag is the version a tag names, when it has the tag's form.
func (settings Settings) VersionOfTag(tag string) (Version, bool) {
	prefix, suffix, _ := strings.Cut(settings.Tag, "{version}")
	if !strings.HasPrefix(tag, prefix) || !strings.HasSuffix(tag, suffix) || len(tag) <= len(prefix)+len(suffix) {
		return Version{}, false
	}
	version, err := ParseVersion(tag[len(prefix) : len(tag)-len(suffix)])
	return version, err == nil
}

// RequestPath is the request file's path; OverridePath the override's.
func (settings Settings) RequestPath() string {
	return path.Join(settings.Changes.Dir, changes.RequestName)
}

// OverridePath is the override file's path.
func (settings Settings) OverridePath() string {
	return path.Join(settings.Changes.Dir, changes.OverrideName)
}

// Outputs are every path a release writes: the version files, the build file, the changelogs.
func (settings Settings) Outputs() []string {
	var out []string
	for _, file := range settings.Files {
		out = append(out, file.Path)
	}
	if settings.Build != nil {
		out = append(out, settings.Build.Path)
	}
	out = append(out, settings.Changes.Into)
	if settings.Changes.InternalInto != "" {
		out = append(out, settings.Changes.InternalInto)
	}
	return out
}

// captures reads a version file's group: exactly one match of the pattern, its group's text.
func (file VersionFile) captures(data []byte) (string, error) {
	matches := file.Match.FindAllSubmatchIndex(data, -1)
	if len(matches) != 1 {
		return "", fmt.Errorf("%s: the pattern matches %d times, not once", file.Path, len(matches))
	}
	index := file.Match.SubexpIndex(file.Group)
	if matches[0][2*index] < 0 {
		return "", fmt.Errorf("%s: the pattern matches but its %s group takes no part", file.Path, file.Group)
	}
	return string(data[matches[0][2*index]:matches[0][2*index+1]]), nil
}

// replace puts value in place of the group's text, then reads the result back with the pattern.
func (file VersionFile) replace(data []byte, value string) ([]byte, error) {
	matches := file.Match.FindAllSubmatchIndex(data, -1)
	if len(matches) != 1 {
		return nil, fmt.Errorf("%s: the pattern matches %d times, not once", file.Path, len(matches))
	}
	index := file.Match.SubexpIndex(file.Group)
	start, end := matches[0][2*index], matches[0][2*index+1]
	if start < 0 {
		return nil, fmt.Errorf("%s: the pattern matches but its %s group takes no part", file.Path, file.Group)
	}
	out := append(append(append([]byte{}, data[:start]...), value...), data[end:]...)
	back, err := file.captures(out)
	if err != nil || back != value {
		return nil, fmt.Errorf("%s: the pattern does not read its own result (%s) back", file.Path, value)
	}
	return out, nil
}

// Captured is the text a version file's pattern captures in data: exactly one match, its group.
func Captured(file VersionFile, data []byte) (string, error) { return file.captures(data) }
