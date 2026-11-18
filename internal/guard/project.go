package guard

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/zima-oss/jackstay/internal/config"
)

// A denyRule is one of a project's `guard.deny` rules: refused where its variables hold what it
// names and its command pattern matches, unless its `unless` pattern does (RE2, W-036).
type denyRule struct {
	id      string
	env     map[string]*regexp.Regexp
	command *regexp.Regexp
	unless  *regexp.Regexp
	why     string
	instead string
}

// RegenerableProblem says why a `guard.regenerable` entry cannot be used, or "": it names one
// folder, and must hold enough of a name to match only what it means (`[a-v]*`, `?*`, `*` match
// nearly every name).
func RegenerableProblem(entry string) string {
	name := strings.TrimRight(strings.TrimSpace(entry), "/")
	switch {
	case name == "" || name == "." || name == "..":
		return "names no folder"
	case strings.Contains(name, "/"):
		return "holds a slash, and one folder name never does"
	}
	literal := regexp.MustCompile(`\[[^\]]*\]|[*?]`).ReplaceAllString(name, "")
	letters := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(literal, "")
	if (strings.HasPrefix(name, "*") && strings.HasSuffix(name, "*")) || len(letters) < 2 ||
		matchName(name, "jst-guard-any-name") || matchName(name, ".env") || matchName(name, "Sources") || matchName(name, "src") {
		return "would match nearly every name, and so make everything deletable"
	}
	return ""
}

// loadGuard reads the project's `guard` section: its regenerable names and its deny rules. One
// entry that cannot be applied is passed over and said; the rest holds.
func (reader *reader) loadGuard(section map[string]any) {
	if section == nil {
		return
	}
	entries, _ := section["regenerable"].([]any)
	for _, raw := range entries {
		entry, ok := raw.(string)
		if !ok {
			continue
		}
		name := strings.TrimRight(strings.TrimSpace(entry), "/")
		if problem := RegenerableProblem(name); problem != "" {
			reader.warn(warnRegenerable, fmt.Sprintf("jackstay.json: the guard.regenerable entry %q %s, and is not applied", entry, problem))
			continue
		}
		reader.declared = append(reader.declared, name)
	}
	reader.loadExceptions(section)
	rules, _ := section["deny"].([]any)
	for _, raw := range rules {
		members, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rule, problem := denyRuleOf(members)
		if problem != "" {
			reader.warn(warnRuleBroken, fmt.Sprintf("jackstay.json: the guard rule %q cannot be read (%s) and is not applied", rule.id, problem))
			continue
		}
		if rule.env == nil && rule.command == nil {
			continue
		}
		reader.deny = append(reader.deny, rule)
	}
}

func denyRuleOf(members map[string]any) (denyRule, string) {
	rule := denyRule{id: "rule", why: "this project refuses it (jackstay.json)", instead: "ask the owner"}
	if id, ok := members["id"].(string); ok {
		rule.id = id
	}
	if why, ok := members["why"].(string); ok {
		rule.why = why
	}
	if instead, ok := members["instead"].(string); ok {
		rule.instead = instead
	}
	compile := func(pattern string, whole bool) (*regexp.Regexp, error) {
		if whole {
			pattern = `^(?:` + pattern + `)$`
		}
		return regexp.Compile(pattern)
	}
	if env, ok := members["env"].(map[string]any); ok && len(env) > 0 {
		rule.env = map[string]*regexp.Regexp{}
		for name, raw := range env {
			text := fmt.Sprint(raw)
			compiled, err := compile(text, true)
			if err != nil {
				return rule, err.Error()
			}
			rule.env[name] = compiled
		}
	}
	for key, target := range map[string]**regexp.Regexp{"command": &rule.command, "unless": &rule.unless} {
		text, ok := members[key].(string)
		if !ok || text == "" {
			continue
		}
		compiled, err := compile(text, false)
		if err != nil {
			return rule, err.Error()
		}
		*target = compiled
	}
	return rule, ""
}

// heldInEnvironment is what a variable may hold in the environment of a command: its texts (none:
// certainly unset), or false when its value cannot be known. What the command itself was given;
// else what the line exported; where it did not export it for certain, also what the hook's own
// environment holds. After something unread ran here, what the line did not export is unknown.
func (reader *reader) heldInEnvironment(name string, inv *invocation) ([]string, bool) {
	if held, ok := inv.own[name]; ok {
		return held.texts, held.known()
	}
	var found []string
	here := inv.here
	if held, ok := here.exported[name]; ok {
		if !held.known() {
			return nil, false
		}
		found = append(found, held.texts...)
		if here.sure[name] {
			return found, true
		}
	}
	if here.blind {
		return nil, false
	}
	if environed, ok := reader.environ[name]; ok {
		if held, set := here.vars[name]; set {
			if !held.known() {
				return nil, false
			}
			found = append(found, held.texts...)
		} else {
			found = append(found, environed)
		}
	}
	return found, true
}

// projectRules applies the project's `guard.deny` rules to one command.
func (reader *reader) projectRules(inv *invocation) {
	if len(reader.deny) == 0 || len(inv.args) == 0 {
		return
	}
	joined := strings.Join(inv.values(0), " ")
	for _, rule := range reader.deny {
		var prefix, unknown []string
		matched := true
		names := make([]string, 0, len(rule.env))
		for name := range rule.env {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			texts, known := reader.heldInEnvironment(name, inv)
			if !known {
				unknown = append(unknown, name)
				prefix = append(prefix, name+"=?")
				continue
			}
			prefix = append(prefix, name+"="+strings.Join(texts, "|"))
			hit := slices.ContainsFunc(texts, rule.env[name].MatchString)
			if !hit {
				matched = false
			}
		}
		if !matched || (rule.command != nil && !rule.command.MatchString(joined)) ||
			(rule.unless != nil && rule.unless.MatchString(joined)) {
			continue
		}
		why, instead := rule.why, rule.instead
		if len(unknown) > 0 {
			why += " (what " + strings.Join(unknown, ", ") + " holds cannot be known at this point of the line, so the rule is applied)"
			instead += "; or give the value on the command itself (`" + unknown[0] + "=… command`)"
		}
		reader.add(Finding{Rule: "project:" + rule.id, Command: strings.TrimSpace(strings.Join(prefix, " ") + " " + inv.shown()),
			Why: why, Instead: instead})
	}
}

// scan is the fallback where a producer the guard cannot model feeds a shell (section 2): every
// row's destructive form is searched as words in the whole raw text of the call, and so are the
// project's deny patterns. A form found refuses, with its row's rule.
func (reader *reader) scan(why string) {
	if reader.scanned {
		return
	}
	reader.scanned = true
	pieces := regexp.MustCompile(`[\n;|&]+`).Split(reader.raw, -1)
	found := false
	for _, piece := range pieces {
		tokens := scanTokens(piece)
		for _, row := range table {
			if len(row.Words) == 0 || slices.ContainsFunc(row.Unless, func(word string) bool { return slices.Contains(tokens, word) }) {
				continue
			}
			for _, form := range row.Words {
				if containsForm(tokens, form) {
					reader.refuse(row.ID, truncate(oneLine(piece), 200), why+", and it holds this destructive form",
						"write the command plainly, one command per line, so it can be checked")
					found = true
					break
				}
			}
		}
	}
	for _, rule := range reader.deny {
		if rule.command != nil && rule.command.MatchString(reader.raw) && (rule.unless == nil || !rule.unless.MatchString(reader.raw)) {
			reader.add(Finding{Rule: "project:" + rule.id, Command: truncate(oneLine(reader.raw), 200), Why: rule.why, Instead: rule.instead})
			found = true
		}
	}
	if found {
		reader.warn(warnProducer, "a producer the guard cannot model feeds a shell; the whole call was searched for destructive forms")
	}
}

// scanTokens splits raw text into words: quotes and the shell's brackets taken off.
func scanTokens(piece string) []string {
	cleaned := strings.Map(func(char rune) rune {
		if strings.ContainsRune("'\"`(){}$<>\\", char) {
			return ' '
		}
		return char
	}, piece)
	words := strings.Fields(cleaned)
	for index, word := range words {
		word = strings.TrimPrefix(word, "=")
		if strings.Contains(word, "/") && !strings.HasPrefix(word, "-") {
			word = path.Base(word)
		}
		if real, ok := gnuNames[word]; ok {
			word = real
		}
		words[index] = word
	}
	return words
}

// containsForm says whether the words hold a form's words in order.
func containsForm(tokens, form []string) bool {
	here := 0
	for _, token := range tokens {
		if here < len(form) && tokenMatches(token, form[here]) {
			here++
		}
	}
	return here == len(form)
}

func tokenMatches(token, wanted string) bool {
	for _, alternative := range strings.Split(wanted, "|") {
		switch {
		case strings.HasPrefix(alternative, "--"):
			name, _, _ := strings.Cut(token, "=")
			if name == alternative {
				return true
			}
		case strings.HasPrefix(alternative, "-") && len(alternative) == 2:
			if strings.HasPrefix(token, "-") && !strings.HasPrefix(token, "--") && strings.Contains(token[1:], alternative[1:]) {
				return true
			}
		default:
			if token == alternative {
				return true
			}
		}
	}
	return false
}

// RegenerableNames are the folders the guard takes as regenerable without a project's word
// (`.build`, `node_modules`, …): jst tidy removes a worktree whose ignored files are only these.
func RegenerableNames() []string { return slices.Clone(builtIn) }

// An exception is one of a project's `guard.rules.<id>.paths` (W-053): absolute, with its fixed
// folder (the part before the first wildcard).
type exception struct {
	pattern, anchor string
}

const warnException = "guard-exception-not-applied"

// loadExceptions reads the path exceptions of the deleting rules. A pattern whose fixed folder is
// the project's top or above it is passed over and said.
func (reader *reader) loadExceptions(section map[string]any) {
	rules, _ := section["rules"].(map[string]any)
	for id, raw := range rules {
		entry, _ := raw.(map[string]any)
		patterns, _ := entry["paths"].([]any)
		for _, rawPattern := range patterns {
			text, _ := rawPattern.(string)
			if config.PatternProblem(text) != "" || !config.GuardRules[id].Paths {
				continue
			}
			var full, base string
			switch {
			case strings.HasPrefix(text, "~/"):
				base = realPath(reader.home)
				full = path.Join(base, strings.TrimPrefix(text, "~/"))
			case reader.projectRoot != "":
				base = reader.projectRoot
				full = path.Join(base, text)
			default:
				continue
			}
			var fixed []string
			for _, part := range strings.Split(full, "/") {
				if strings.ContainsAny(part, "*?[") {
					break
				}
				fixed = append(fixed, part)
			}
			anchor := strings.Join(fixed, "/")
			if anchor == "" || (reader.projectRoot != "" && atOrUnder(reader.projectRoot, anchor)) ||
				atOrUnder(realPath(reader.home), anchor) {
				reader.warn(warnException, fmt.Sprintf("jackstay.json: the guard.rules.%s pattern %q covers the project's top or "+
					"the home folder, and is not applied", id, text))
				continue
			}
			if reader.exceptions == nil {
				reader.exceptions = map[string][]exception{}
			}
			reader.exceptions[id] = append(reader.exceptions[id], exception{pattern: full, anchor: anchor})
		}
	}
}

// excepted says whether a deleting rule's path exceptions cover a target, wherever the command may
// run. Only a target whose written path is its real path is matched (W-053, section 9; redesigned
// after two code review rounds, W-019): no pattern character (the shell would put other paths in
// its place), no `..`, no link the line made at or above it, and no symbolic link anywhere on disk
// from the root down to and including the target. Then the path written is the path deleted.
//
// And only when the whole line is that one command, `rm …` or `find …`, run in the session's own
// folder (redesigned after code rounds 4 and 5, W-019: a `cd` through links, `set -P`, a link made
// earlier on the line or in a subshell — each let the folder or the path the guard models differ
// from the real one; a line with nothing else on it leaves nothing to model).
func (reader *reader) excepted(rule, target string, here *state) bool {
	patterns := reader.exceptions[rule]
	if len(patterns) == 0 || target == "" || strings.ContainsAny(target, "*?[") || len(here.links) > 0 ||
		!reader.alone || !slices.Equal(here.places, []string{reader.folder}) {
		return false
	}
	for _, part := range strings.Split(target, "/") {
		if part == ".." {
			return false
		}
	}
	var candidates []string
	if path.IsAbs(target) {
		candidates = []string{flat(target)}
	} else {
		if here.places == nil {
			return false
		}
		for _, folder := range here.places {
			candidates = append(candidates, flat(path.Join(folder, target)))
		}
	}
	for _, candidate := range candidates {
		if !exceptedAt(patterns, candidate, here) {
			return false
		}
	}
	return true
}

func exceptedAt(patterns []exception, place string, here *state) bool {
	for made := range here.links {
		if atOrUnder(place, made) {
			return false
		}
	}
	for _, each := range patterns {
		// A literal pattern names one path; a pattern with a wildcard what is below its fixed folder.
		inside := under(place, each.anchor) || (place == each.anchor && each.pattern == each.anchor)
		if !inside || !matchPath(each.pattern, place) {
			continue
		}
		return realPathOfExisting(place) == place
	}
	return false
}

// matchPath matches a path against a pattern of `/`-separated parts: `**` any number of parts,
// other parts as path.Match.
func matchPath(pattern, name string) bool {
	return matchParts(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchParts(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			for skip := 0; skip <= len(name); skip++ {
				if matchParts(pattern[1:], name[skip:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if matched, err := path.Match(pattern[0], name[0]); err != nil || !matched {
			return false
		}
		pattern, name = pattern[1:], name[1:]
	}
	return len(name) == 0
}

// aloneCommand says whether a line is one plain `rm` or `find` call and nothing else: one
// statement, not in the background, no assignment, and words that run nothing (no substitution).
func aloneCommand(line string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil || len(file.Stmts) != 1 {
		return false
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Coprocess || stmt.Negated || len(call.Assigns) > 0 || len(call.Args) == 0 {
		return false
	}
	if name := call.Args[0].Lit(); name != "rm" && name != "find" {
		return false
	}
	for _, word := range call.Args[1:] {
		switch word.Lit() {
		case "-exec", "-execdir", "-ok", "-okdir":
			return false // find runs other commands: the line is not that one deletion
		}
	}
	plain := true
	syntax.Walk(stmt, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ArithmExp, *syntax.ParamExp:
			plain = false
		}
		return plain
	})
	return plain
}
