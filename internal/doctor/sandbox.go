package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/zima-oss/jackstay/internal/homedir"
)

// SandboxBlocks says what to tell the owner when Claude Code's Bash sandbox is on (in the
// account's settings file) and may not write home: a `jst test` run in a sandboxed session is then
// not recorded (W-008), and the fix for that case (W-078). "" when it may, or when it cannot be told.
func SandboxBlocks(settingsPath, home string) (blocked, fix string) {
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- Claude Code's settings of the account
	if err != nil {
		return "", ""
	}
	var settings struct {
		Sandbox *struct {
			Enabled    any `json:"enabled"`
			Filesystem *struct {
				AllowWrite []any `json:"allowWrite"`
				DenyWrite  []any `json:"denyWrite"`
			} `json:"filesystem"`
		} `json:"sandbox"`
	}
	if json.Unmarshal(data, &settings) != nil || settings.Sandbox == nil || settings.Sandbox.Enabled != true {
		return "", ""
	}
	var allow, deny []any
	if filesystem := settings.Sandbox.Filesystem; filesystem != nil {
		allow, deny = filesystem.AllowWrite, filesystem.DenyWrite
	}
	real := realPath(home)
	if entry := covering(deny, real); entry != "" {
		return fmt.Sprintf("Claude Code's Bash sandbox denies writing %s (%q in sandbox.filesystem.denyWrite of %s, which wins over "+
				"allowWrite): a `jst test` run in a sandboxed session is not recorded (W-008).", home, entry, settingsPath),
			denied(entry, home, settingsPath)
	}
	if entry := inside(deny, real); entry != "" {
		return fmt.Sprintf("Claude Code's Bash sandbox denies writing %s, inside %s (in sandbox.filesystem.denyWrite of %s): a `jst test` "+
				"run in a sandboxed session is not recorded (W-008).", entry, home, settingsPath),
			denied(entry, home, settingsPath)
	}
	if covering(allow, real) != "" {
		return "", ""
	}
	return fmt.Sprintf("Claude Code's Bash sandbox is on and %s is not in sandbox.filesystem.allowWrite of %s (a project's own settings "+
			"may still allow it; only this file is read): a `jst test` run in a sandboxed session is not recorded (W-008).",
			home, settingsPath),
		fmt.Sprintf("add %q to sandbox.filesystem.allowWrite in %s", home, settingsPath)
}

// denied is the fix when a denyWrite entry stops jackstay's state. An entry may protect more than
// that state (an ancestor, a pattern matching other accounts), which doctor cannot judge, so it never
// advises removing one: the owner changes it so it leaves home writable, or moves jackstay's state.
func denied(entry, home, settingsPath string) string {
	return fmt.Sprintf("change the denyWrite entry %q in %s yourself so that it leaves %s writable and still protects what else it is for; "+
		"or set JACKSTAY_HOME to an absolute folder no denyWrite entry covers and add that folder to sandbox.filesystem.allowWrite there",
		entry, settingsPath, home)
}

// covering is the first entry that covers the folder real (the folder itself or one of its
// ancestors matches it), "" when none does.
func covering(entries []any, real string) string {
	for _, raw := range entries {
		entry, isText := raw.(string)
		if !isText {
			continue
		}
		pattern := resolvedPattern(strings.TrimSuffix(homedir.Expand(entry), "/**"))
		if pattern == "" {
			continue
		}
		for folder := real; ; folder = filepath.Dir(folder) {
			if matches(pattern, folder) {
				return entry
			}
			if folder == "/" {
				break
			}
		}
	}
	return ""
}

// inside is a denied entry that names a folder inside real (`~/.jackstay/.all`, `/Users/*/.jackstay/.all/**`): writing
// there is refused too. A pattern reaches inside real when its elements match all of real's with
// some left over (a literal prefix would miss a `*` above real).
func inside(entries []any, real string) string {
	for _, raw := range entries {
		entry, isText := raw.(string)
		if !isText {
			continue
		}
		pattern := resolvedPattern(homedir.Expand(entry))
		if pattern == "" {
			continue
		}
		if reachesBelow(strings.Split(strings.Trim(pattern, "/"), "/"), strings.Split(strings.Trim(real, "/"), "/")) {
			return entry
		}
	}
	return ""
}

// reachesBelow says whether pattern can match a path strictly below name.
func reachesBelow(pattern, name []string) bool {
	if len(name) == 0 {
		return len(pattern) > 0
	}
	if len(pattern) == 0 {
		return false
	}
	if pattern[0] == "**" {
		return true
	}
	matched, err := path.Match(pattern[0], name[0])
	return err == nil && matched && reachesBelow(pattern[1:], name[1:])
}

// matches matches a path against a pattern element by element: `**` stands for any number of
// elements, every other element is matched by path.Match.
func matches(pattern, name string) bool {
	return matchElements(strings.Split(strings.Trim(pattern, "/"), "/"), strings.Split(strings.Trim(name, "/"), "/"))
}

func matchElements(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		for skip := 0; skip <= len(name); skip++ {
			if matchElements(pattern[1:], name[skip:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	matched, err := path.Match(pattern[0], name[0])
	return err == nil && matched && matchElements(pattern[1:], name[1:])
}

// realPath is the path with its links resolved as far as it exists (/tmp is /private/tmp).
func realPath(given string) string {
	cleaned := filepath.Clean(given)
	for tail := ""; ; {
		if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
			return filepath.Join(resolved, tail)
		}
		parent := filepath.Dir(cleaned)
		if parent == cleaned {
			return filepath.Join(given)
		}
		tail = filepath.Join(filepath.Base(cleaned), tail)
		cleaned = parent
	}
}

// resolvedPattern is an absolute pattern with its part before the first `*?[` resolved as a real
// path (/tmp is /private/tmp); "" for a relative one.
func resolvedPattern(pattern string) string {
	if !filepath.IsAbs(pattern) {
		return ""
	}
	cut := strings.IndexAny(pattern, "*?[")
	if cut < 0 {
		return realPath(pattern)
	}
	head := pattern[:strings.LastIndex(pattern[:cut], "/")+1]
	return strings.TrimSuffix(realPath(head), "/") + "/" + pattern[len(head):]
}
