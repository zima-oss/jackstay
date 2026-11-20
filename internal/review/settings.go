// Package review is `jst review`: cold code reviews run by the framework in rounds, with
// W-048's triage and W-019's redesign rule kept by the tool (docs/design/review.md). New in Go,
// written as if the one switch had happened (W-051).
package review

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/review/claude"
	"github.com/zima-oss/jackstay/internal/review/runner"
)

// ReviewerSettings are one reviewer's keys (`review.reviewers.<name>`).
type ReviewerSettings struct {
	Name           string
	Command        string
	Model          string
	Effort         string
	TimeoutMinutes int
	Isolation      []string
}

// Settings are a project's `review` section with every default filled in.
type Settings struct {
	Reviewers        map[string]ReviewerSettings
	Use              []string
	Brief            string
	Template         string
	Cite             []string // nil: the default (AGENTS.md when the reviewed tree has it)
	Severities       []string
	Blocking         []string
	Triage           bool
	RepeatRounds     int
	RepeatSeverities []string
	RoundLimit       int
	MaxConcurrent    int
	QueueMinutes     int
	Store            string // empty: ~/.jackstay/<project>/reviews
	KeepDays         int
}

// DefaultIsolation is the default set of isolation flags (docs/design/review.md section 1).
var DefaultIsolation = []string{
	"--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check",
	"-c", "mcp_servers={}", "-c", "features.apps=false", "-c", "shell_environment_policy.inherit=none",
}

// KnownReviewers are the reviewers jackstay knows how to run.
var KnownReviewers = []string{"codex", claude.Name}

var efforts = []string{"minimal", "low", "medium", "high", "xhigh"}

// ownedWords are argv words jackstay sets itself and refuses in `isolation`.
var ownedWords = []string{
	"exec", "-a", "--ask-for-approval", "-s", "--sandbox", "--permissions", "--dangerously-bypass-approvals-and-sandbox",
	"--full-auto", "--approve-for-me", "--add-dir", "-C", "--cd", "-o", "--output-last-message",
	"--output-schema", "--json", "-m", "--model", "-p", "--profile", "--worktree", "-i", "--image", "-",
}

// ownedKeys are `-c` keys jackstay sets itself or that would undo read-only.
var ownedKeys = []string{"sandbox_mode", "sandbox_permissions", "approval_policy", "model", "model_reasoning_effort", "default_permissions", "permissions", "features.network_proxy", "project_doc_max_bytes"}

func defaultReviewer(name string) ReviewerSettings {
	if name == claude.Name {
		return ReviewerSettings{Name: name, Command: "claude", TimeoutMinutes: 20}
	}
	return ReviewerSettings{Name: name, Command: "codex", Effort: "medium", TimeoutMinutes: 20,
		Isolation: append([]string(nil), DefaultIsolation...)}
}

// DefaultReviewer is the reviewer a round runs when neither --reviewer nor `review.use` names one.
const DefaultReviewer = "codex"

// Defaults are the settings of a project with no `review` section.
func Defaults() Settings {
	reviewers := make(map[string]ReviewerSettings, len(KnownReviewers))
	for _, name := range KnownReviewers {
		reviewers[name] = defaultReviewer(name)
	}
	return Settings{
		Use:              []string{DefaultReviewer},
		Reviewers:        reviewers,
		Severities:       []string{"blocker", "major", "minor"},
		Blocking:         []string{"blocker"},
		Triage:           true,
		RepeatRounds:     2,
		RepeatSeverities: nil, // every severity but the last, filled in by SettingsOf
		RoundLimit:       6,
		MaxConcurrent:    4,
		QueueMinutes:     15,
		KeepDays:         30,
	}
}

// SettingsOf reads the `review` section of a jackstay.json (types already checked by internal/config)
// and judges its meaning; the problems are every rule of section 1 that needs no tree.
func SettingsOf(loaded config.Config) (Settings, []string) {
	settings := Defaults()
	section := loaded.Section("review")
	var problems []string
	problem := func(format string, values ...any) { problems = append(problems, fmt.Sprintf(format, values...)) }
	if section == nil {
		settings.RepeatSeverities = allButLast(settings.Severities)
		return settings, nil
	}
	if raw, ok := section["reviewers"].(map[string]any); ok {
		for name, rawEntry := range raw {
			if !slices.Contains(KnownReviewers, name) {
				problem("`review.reviewers.%s`: jackstay knows how to run only %s", name, strings.Join(KnownReviewers, ", "))
				continue
			}
			entry, _ := rawEntry.(map[string]any)
			reviewer := settings.Reviewers[name]
			if value, ok := entry["command"].(string); ok {
				reviewer.Command = value
			}
			if value, ok := entry["model"].(string); ok {
				reviewer.Model = value
			}
			if value, ok := entry["effort"].(string); ok {
				reviewer.Effort = value
			}
			if value, ok := integerOf(entry["timeout_minutes"]); ok {
				reviewer.TimeoutMinutes = value
			}
			if value, ok := entry["isolation"]; ok {
				reviewer.Isolation = textsOf(value)
			}
			settings.Reviewers[name] = reviewer
		}
	}
	if value, ok := section["use"]; ok {
		settings.Use = textsOf(value)
	}
	if value, ok := section["brief"].(string); ok {
		settings.Brief = value
	}
	if value, ok := section["template"].(string); ok {
		settings.Template = value
	}
	if value, ok := section["cite"]; ok {
		settings.Cite = textsOf(value)
		if settings.Cite == nil {
			settings.Cite = []string{}
		}
	}
	if value, ok := section["severities"]; ok {
		settings.Severities = textsOf(value)
	}
	if value, ok := section["blocking"]; ok {
		settings.Blocking = textsOf(value)
	}
	if value, ok := section["triage"].(bool); ok {
		settings.Triage = value
	}
	numbers := []struct {
		key      string
		target   *int
		low, top int
	}{
		{"repeat_rounds", &settings.RepeatRounds, 2, 100},
		{"round_limit", &settings.RoundLimit, 2, 1000},
		{"max_concurrent", &settings.MaxConcurrent, 1, MaxSlots},
		{"queue_minutes", &settings.QueueMinutes, 1, 120},
		{"keep_days", &settings.KeepDays, 1, 3650},
	}
	for _, number := range numbers {
		if value, ok := integerOf(section[number.key]); ok {
			*number.target = value
		}
		if *number.target < number.low || *number.target > number.top {
			problem("`review.%s` is %d: %d–%d", number.key, *number.target, number.low, number.top)
		}
	}
	if value, ok := section["repeat_severities"]; ok {
		settings.RepeatSeverities = textsOf(value)
	} else {
		settings.RepeatSeverities = allButLast(settings.Severities)
	}
	if value, ok := section["store"].(string); ok {
		settings.Store = value
	}
	problems = append(problems, settings.problems()...)
	return settings, problems
}

// problems are the rules between keys (also run after command-line overrides).
func (settings Settings) problems() []string {
	var found []string
	problem := func(format string, values ...any) { found = append(found, fmt.Sprintf(format, values...)) }
	for _, name := range sortedNames(settings.Reviewers) {
		reviewer := settings.Reviewers[name]
		if reviewer.Command == "" {
			problem("`review.reviewers.%s.command` is empty", name)
		} else if strings.Contains(reviewer.Command, "/") && !filepath.IsAbs(reviewer.Command) {
			problem("`review.reviewers.%s.command` is a relative path: a name on PATH or an absolute path", name)
		}
		allowedEfforts := efforts
		if name == claude.Name {
			allowedEfforts = []string{"low", "medium", "high", "xhigh", "max", "ultracode"}
		}
		if reviewer.Effort != "" && !slices.Contains(allowedEfforts, reviewer.Effort) {
			problem("`review.reviewers.%s.effort` is %q: one of %s", name, reviewer.Effort, strings.Join(allowedEfforts, ", "))
		}
		if reviewer.TimeoutMinutes < 1 || reviewer.TimeoutMinutes > 120 {
			problem("`review.reviewers.%s.timeout_minutes` is %d: 1–120", name, reviewer.TimeoutMinutes)
		}
		if name == claude.Name {
			if len(reviewer.Isolation) > 0 {
				problem("`review.reviewers.%s.isolation` is fixed by jackstay", name)
			}
		} else {
			found = append(found, isolationProblems(name, reviewer.Isolation)...)
		}
	}
	seen := map[string]bool{}
	for _, name := range settings.Use {
		if seen[name] {
			problem("`review.use` names %s twice", name)
		}
		seen[name] = true
		if _, ok := settings.Reviewers[name]; !ok {
			problem("unknown reviewer %q: choose %s", name, strings.Join(KnownReviewers, " or "))
		}
	}
	if len(settings.Severities) == 0 {
		problem("`review.severities` is empty")
	}
	if duplicate := firstDuplicate(settings.Severities); duplicate != "" {
		problem("`review.severities` names %q twice", duplicate)
	}
	for _, word := range settings.Severities {
		if word == "" || strings.ContainsAny(word, " \t\n") {
			problem("`review.severities` has %q: one word each", word)
		}
	}
	for key, list := range map[string][]string{"blocking": settings.Blocking, "repeat_severities": settings.RepeatSeverities} {
		for _, word := range list {
			if !slices.Contains(settings.Severities, word) {
				problem("`review.%s` names %q, which is not in `review.severities`", key, word)
			}
		}
	}
	if len(settings.Blocking) == 0 {
		problem("`review.blocking` is empty: no finding could ever block a round")
	}
	if settings.Store != "" && !filepath.IsAbs(homedir.Expand(settings.Store)) {
		problem("`review.store` must be absolute (or begin `~/`)")
	}
	for _, path := range append([]string{settings.Brief, settings.Template}, settings.Cite...) {
		if path != "" && (filepath.IsAbs(path) || slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..")) {
			problem("`review` path %q must be inside the repository (relative, no `..`)", path)
		}
	}
	slices.Sort(found)
	return found
}

func isolationProblems(name string, words []string) []string {
	var found []string
	for index := 0; index < len(words); index++ {
		word := words[index]
		flag, _, _ := strings.Cut(word, "=")
		switch {
		case slices.Contains(ownedWords, flag), attachedShortOption(word):
			found = append(found, fmt.Sprintf("`review.reviewers.%s.isolation` has %q, which jackstay sets itself or which would undo read-only", name, word))
		case !strings.HasPrefix(word, "-"):
			found = append(found, fmt.Sprintf("`review.reviewers.%s.isolation` has %q, which is not an option (a prompt?)", name, word))
		case word == "-c" || word == "--config":
			if index+1 >= len(words) {
				found = append(found, fmt.Sprintf("`review.reviewers.%s.isolation` ends with %q and no value", name, word))
				continue
			}
			index++
			key, _, _ := strings.Cut(words[index], "=")
			key = strings.TrimSpace(key)
			if slices.Contains(ownedKeys, key) || strings.HasPrefix(key, "sandbox") || strings.HasPrefix(key, "permissions.") {
				found = append(found, fmt.Sprintf("`review.reviewers.%s.isolation` sets `%s`, which jackstay sets itself or which would undo read-only", name, key))
			}
		case strings.HasPrefix(word, "--config="), strings.HasPrefix(word, "-c") && len(word) > 2:
			found = append(found, fmt.Sprintf("`review.reviewers.%s.isolation` has %q: write `-c` and its value as two words", name, word))
		}
	}
	return found
}

// attachedShortOption reports a short owned flag written with its value attached (`-sVALUE`,
// `-pPROFILE`, `-mMODEL`), which the cut at `=` above does not see.
func attachedShortOption(word string) bool {
	return len(word) > 2 && word[0] == '-' && word[1] != '-' && slices.Contains(ownedWords, word[:2])
}

// policyID records the configuration that decides what a round reviews and what blocks.
// Scheduling, storage and per-run reviewer overrides do not change that policy.
func (settings Settings) policyID() string {
	policy := struct {
		Brief, Template                              string
		Cite, Severities, Blocking, RepeatSeverities []string
		Triage                                       bool
		RepeatRounds, RoundLimit                     int
	}{
		Brief: settings.Brief, Template: settings.Template, Cite: settings.Cite,
		Severities: settings.Severities, Blocking: settings.Blocking, RepeatSeverities: settings.RepeatSeverities,
		Triage: settings.Triage, RepeatRounds: settings.RepeatRounds, RoundLimit: settings.RoundLimit,
	}
	data, _ := json.Marshal(policy) // This value has only scalar and string slice fields.
	return fmt.Sprintf("v1:%x", sha256.Sum256(data))
}

func completedReviewers(runs []ReviewerRun) map[string]bool {
	completed := make(map[string]bool, len(runs))
	for _, run := range runs {
		if run.Outcome == runner.OutcomeDone {
			completed[run.Reviewer] = true
		}
	}
	return completed
}

func sameReviewers(runs []ReviewerRun, names []string) bool {
	completed := completedReviewers(runs)
	if len(completed) != len(names) {
		return false
	}
	for _, name := range names {
		if !completed[name] {
			return false
		}
	}
	return true
}

// Blocks is the one predicate of section 4: whether a finding blocks a round while it is open.
func (settings Settings) Blocks(severity, likelihood, consequence, ordinaryPath string) bool {
	if !slices.Contains(settings.Blocking, severity) {
		return false
	}
	return !settings.Triage || !IsRare(likelihood, consequence, ordinaryPath)
}

// IsRare is W-048's rare, as W-094 amends it: unlikely in ordinary work, and either mild or severe
// with no ordinary-work path named by which its setup arises by accident.
func IsRare(likelihood, consequence, ordinaryPath string) bool {
	return likelihood == "unusual" && (consequence == "mild" || strings.TrimSpace(ordinaryPath) == "")
}

// LongestTimeout is the longest timeout of the reviewers named, in minutes.
func (settings Settings) LongestTimeout(names []string) int {
	longest := 0
	for _, name := range names {
		longest = max(longest, settings.Reviewers[name].TimeoutMinutes)
	}
	return longest
}

// Budget is the whole call's time in minutes (section 6): the queue, the longest run, and 2.
func (settings Settings) Budget(names []string) int {
	return settings.QueueMinutes + settings.LongestTimeout(names) + 2
}

func allButLast(list []string) []string {
	if len(list) <= 1 {
		return append([]string(nil), list...)
	}
	return append([]string(nil), list[:len(list)-1]...)
}

func integerOf(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	if err != nil {
		return 0, false
	}
	return int(parsed), true
}

func textsOf(value any) []string {
	items, _ := value.([]any)
	var out []string
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func firstDuplicate(list []string) string {
	seen := map[string]bool{}
	for _, item := range list {
		if seen[item] {
			return item
		}
		seen[item] = true
	}
	return ""
}

func sortedNames[Value any](entries map[string]Value) []string {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
