package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/homedir"
)

// The jackstay home holds two kinds of things. Some jst writes while an agent's Bash command
// runs inside Claude Code's sandbox: the journal, the heavy-suite lock with its queue and holder,
// session chat, background jobs, the brief, ids and the trash. The rest is evidence jackstay later
// trusts: receipts and failure notes, gate marks and bars, review rounds, the run log, the adopted
// delivery and the expected projects. The sandbox may write the first and only read the second,
// so an agent cannot forge a pass it did not run (#59, W-090). `jst test` and `jst review` record
// evidence, so they run outside the sandbox; `jst test` refuses to run inside it.
//
// Hooks and stdio MCP servers run outside the sandbox (the guard's guard-seen marks, `jst chat
// serve`), so they need no grant.

// sharedGrants are the paths under `<home>/.all` the sandbox may write.
var sharedGrants = []string{"telemetry", "procs", "chat", "session-chat", "context-brief", "heavy-suite.lock", "heavy-suite.lock.queue",
	"heavy-suite.lock.holder"}

// projectGrants are the paths under `<home>/<project>` the sandbox may write. Test outputs and
// durations are not among them: `jst test` runs outside the sandbox, since it records evidence.
var projectGrants = []string{"telemetry", "ids"}

// homeGrants are the paths directly under `<home>` the sandbox may write.
var homeGrants = []string{".trash"}

// sharedEvidence and projectEvidence are paths the sandbox must not write; doctor checks them.
var (
	sharedEvidence  = []string{"runs.jsonl", "reviews", "review-pool", "delivery", "expected-projects"}
	projectEvidence = []string{"receipts", "gate-marks", "gate-bars", "reviews"}
)

// SandboxProjects are the projects whose folders the grants name: each folder already in home (a
// project jackstay has written for) and each expected project, by marker name, sorted.
func SandboxProjects(home string, expectedProjects []string) []string {
	found := map[string]bool{}
	if entries, err := os.ReadDir(home); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				found[entry.Name()] = true
			}
		}
	}
	// The expected list holds marker names already (`jst doctor --expect` writes them so).
	for _, marker := range expectedProjects {
		if marker != "" {
			found[marker] = true
		}
	}
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SandboxGrants are the paths the sandbox may write, in a fixed order: shared, home, then each
// project's.
func SandboxGrants(home string, projects []string) []string {
	var grants []string
	for _, name := range sharedGrants {
		grants = append(grants, filepath.Join(home, homedir.Shared, name))
	}
	for _, name := range homeGrants {
		grants = append(grants, filepath.Join(home, name))
	}
	for _, project := range projects {
		for _, name := range projectGrants {
			grants = append(grants, filepath.Join(home, project, name))
		}
	}
	return grants
}

func sandboxEvidence(home string, projects []string) []string {
	var paths []string
	for _, name := range sharedEvidence {
		paths = append(paths, filepath.Join(home, homedir.Shared, name))
	}
	for _, project := range projects {
		for _, name := range projectEvidence {
			paths = append(paths, filepath.Join(home, project, name))
		}
	}
	return paths
}

// sandboxRules are the account's sandbox write rules; enabled is false when the sandbox is off
// or the file cannot be read as settings.
type sandboxRules struct {
	enabled     bool
	allow, deny []any
}

func readSandboxRules(settingsPath string) sandboxRules {
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- Claude Code's settings of the account
	if err != nil {
		return sandboxRules{}
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
		return sandboxRules{}
	}
	rules := sandboxRules{enabled: true}
	if filesystem := settings.Sandbox.Filesystem; filesystem != nil {
		rules.allow, rules.deny = filesystem.AllowWrite, filesystem.DenyWrite
	}
	return rules
}

// SandboxBlocks says what to tell the owner when Claude Code's Bash sandbox is on (in the
// account's settings file) and may not write one of the grants: a `jst test` run in a sandboxed
// session then keeps no output or journal, `jst id next`, `jst bg` or `jst lock` fail (W-008),
// and the fix for that case (W-078). "" when it may, or when it cannot be told.
func SandboxBlocks(settingsPath, home string, projects []string) (blocked, fix string) {
	rules := readSandboxRules(settingsPath)
	if !rules.enabled {
		return "", ""
	}
	var missing []string
	for _, grant := range SandboxGrants(home, projects) {
		real := realPath(grant)
		if entry := covering(rules.deny, real); entry != "" {
			return fmt.Sprintf("Claude Code's Bash sandbox denies writing %s (%q in sandbox.filesystem.denyWrite of %s, which wins over "+
					"allowWrite): jst cannot keep its logs, locks, chat or jobs in a sandboxed session (W-008).", grant, entry, settingsPath),
				denied(entry, grant, settingsPath)
		}
		if entry := inside(rules.deny, real); entry != "" {
			return fmt.Sprintf("Claude Code's Bash sandbox denies writing %s, inside %s (in sandbox.filesystem.denyWrite of %s): jst "+
					"cannot keep its logs, locks, chat or jobs in a sandboxed session (W-008).", entry, grant, settingsPath),
				denied(entry, grant, settingsPath)
		}
		if covering(rules.allow, real) == "" {
			missing = append(missing, grant)
		}
	}
	if len(missing) == 0 {
		return "", ""
	}
	return fmt.Sprintf("Claude Code's Bash sandbox is on and sandbox.filesystem.allowWrite of %s does not cover %s (a project's own "+
			"settings may still allow it; only this file is read): jst cannot keep its logs, locks, chat or jobs in a sandboxed "+
			"session (W-008).", settingsPath, strings.Join(missing, ", ")),
		fmt.Sprintf("`jst setup` adds them to sandbox.filesystem.allowWrite in %s", settingsPath)
}

// SandboxWritesEvidence says what to tell the owner when the sandbox may write jackstay's
// evidence (an allowWrite entry covers the whole home, say): an agent could then forge a gate
// mark or a receipt (#59). The entries that cover it are returned too. "" when it may not.
func SandboxWritesEvidence(settingsPath, home string, projects []string) (text, fix string, entries []string) {
	rules := readSandboxRules(settingsPath)
	if !rules.enabled {
		return "", "", nil
	}
	var exposed []string
	seen := map[string]bool{}
	for _, evidence := range sandboxEvidence(home, projects) {
		real := realPath(evidence)
		entry := exposing(rules.allow, real)
		if entry == "" || covering(rules.deny, real) != "" {
			continue
		}
		exposed = append(exposed, evidence)
		if !seen[entry] {
			seen[entry] = true
			entries = append(entries, entry)
		}
	}
	if len(exposed) == 0 {
		return "", "", nil
	}
	quoted := make([]string, len(entries))
	for index, entry := range entries {
		quoted[index] = strconv.Quote(entry)
	}
	return fmt.Sprintf("Claude Code's Bash sandbox may write jackstay's evidence (%s), through %s in sandbox.filesystem.allowWrite "+
			"of %s: an agent inside the sandbox could forge a gate mark or a receipt.", strings.Join(exposed, ", "),
			strings.Join(quoted, ", "), settingsPath),
		fmt.Sprintf("replace those entries with the folders jst needs; `jst setup` does it when an entry names the jackstay home "+
			"itself, else narrow the entry in %s yourself", settingsPath),
		entries
}

// exposing is the first allow entry that lets the sandbox write the evidence at real: one that
// covers it, or one that reaches inside it (a pattern for its files).
func exposing(allow []any, real string) string {
	if entry := covering(allow, real); entry != "" {
		return entry
	}
	return inside(allow, real)
}

// denied is the fix when a denyWrite entry stops jackstay's state. An entry may protect more than
// that state (an ancestor, a pattern matching other accounts), which doctor cannot judge, so it never
// advises removing one: the owner changes it so it leaves home writable, or moves jackstay's state.
func denied(entry, grant, settingsPath string) string {
	return fmt.Sprintf("change the denyWrite entry %q in %s yourself so that it leaves %s writable and still protects what else it is for; "+
		"or set JACKSTAY_HOME to an absolute folder no denyWrite entry covers and run `jst setup` again",
		entry, settingsPath, grant)
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
