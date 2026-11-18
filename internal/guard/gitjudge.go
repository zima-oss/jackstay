package guard

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A gitJob is one git command being judged: the call read with the model, where it runs, and what
// the rules need to know of it.
type gitJob struct {
	reader     *reader
	inv        *invocation
	call       gitCall
	opts       options
	text       string
	wheres     []string // the folders it runs in; "" for one that cannot be named
	env        map[string]value
	redirected bool // GIT_DIR, GIT_WORK_TREE, core.worktree…: the repository is elsewhere
	scratch    bool // a throwaway checkout the command itself went to
	others     []string
}

// git judges `git ARGS…` (args are the words after `git`).
func (reader *reader) git(inv *invocation, words []arg) {
	call := readGit(words)
	if call.sub == "" {
		return
	}
	job := &gitJob{reader: reader, inv: inv, call: call, text: inv.shown(), env: reader.gitEnvironment(inv)}
	job.wheres = job.places()
	job.redirected = job.repositoryElsewhere()
	if why := job.configUnread(); why != "" {
		reader.refuse("git-config-alias", job.text, "", "")
		return
	}
	job.opts = parseOptions(call.rest, gitOptions[call.sub])
	job.scratch = !call.elsewhere && !job.redirected && len(job.wheres) > 0
	for _, where := range job.wheres {
		if where == "" || !reader.throwaway(where, inv.here) {
			job.scratch = false
		}
	}
	for _, check := range gitChecks[call.sub] {
		row := check.row
		check.judge(job, func(why, instead string) { reader.refuse(row, job.text, why, instead) })
	}
	if call.sub == "submodule" {
		job.submodule()
	}
}

// A report is a check's finding for its own row; the row is not the check's to choose.
type report func(why, instead string)

// A gitCheck judges one row on one git subcommand. Checks read the parsed invocation and git's
// answers, never another check's verdict: loosening one row (W-053, W-055) cannot hide another.
type gitCheck struct {
	row   string
	judge func(job *gitJob, found report)
}

// gitChecks are the rows each git subcommand is judged for, one check per row.
var gitChecks = map[string][]gitCheck{
	"reset":    {{"git-reset-hard", (*gitJob).resetHard}},
	"checkout": {{"git-branch-reset", (*gitJob).checkoutMovesBranch}, {"git-checkout-force", (*gitJob).checkoutForce}, {"git-checkout-path", (*gitJob).checkoutPath}},
	"switch":   {{"git-switch-discard", (*gitJob).switchDiscards}, {"git-branch-reset", (*gitJob).switchMovesBranch}},
	"restore":  {{"git-restore-worktree", (*gitJob).restore}},
	"clean":    {{"git-clean-force", (*gitJob).cleanForced}},
	"rm":       {{"git-rm-force", (*gitJob).rmForced}},
	"push": {{"git-push-force", (*gitJob).pushForce}, {"git-push-prune", (*gitJob).pushPrune},
		{"git-push-delete-protected", (*gitJob).pushDeletesProtected}, {"cards-push", (*gitJob).cardPushRefused},
		{"card-push-unreadable-config", (*gitJob).cardPushUnreadable}},
	"branch":         {{"git-branch-reset", (*gitJob).branchMoves}, {"git-branch-force-delete", (*gitJob).branchDeletes}},
	"stash":          {{"git-stash-drop", (*gitJob).stashDrop}, {"git-stash-clear", (*gitJob).stashClear}},
	"worktree":       {{"git-worktree-force", (*gitJob).worktree}},
	"update-ref":     {{"git-update-ref-delete", (*gitJob).updateRefDeletes}, {"git-update-ref-move", (*gitJob).updateRefMoves}, {"git-branch-reset", (*gitJob).updateRefMovesBranch}},
	"filter-branch":  {{"git-filter-branch", func(_ *gitJob, found report) { found("", "") }}},
	"filter-repo":    {{"git-filter-repo", (*gitJob).filterRepo}},
	"read-tree":      {{"git-read-tree-reset", (*gitJob).readTreeReset}},
	"checkout-index": {{"git-checkout-index-force", (*gitJob).checkoutIndex}},
	"config":         {{"git-config-worktree", (*gitJob).config}},
}

func (job *gitJob) resetHard(found report) {
	if (job.opts.on("--hard") || job.opts.unknown) && !job.scratch {
		found("", "")
	}
}

func (job *gitJob) cleanForced(found report) {
	forced := job.forced() || job.configOff("clean.requireforce")
	if forced && !job.opts.any("--dry-run") && !job.scratch {
		found("", "")
	}
}

func (job *gitJob) rmForced(found report) {
	if job.forced() && !job.opts.any("--cached", "--dry-run") && !job.scratch {
		found("", "")
	}
}

func (job *gitJob) stashSub() string {
	if len(job.call.rest) > 0 {
		if sub, ok := job.call.rest[0].single(); ok {
			return sub
		}
	}
	return ""
}

func (job *gitJob) stashDrop(found report) {
	if job.stashSub() == "drop" {
		found("", "")
	}
}

func (job *gitJob) stashClear(found report) {
	if job.stashSub() == "clear" {
		found("", "")
	}
}

func (job *gitJob) filterRepo(found report) {
	if !job.opts.any("--analyze", "--dry-run") || job.opts.unknown {
		found("", "")
	}
}

func (job *gitJob) readTreeReset(found report) {
	if job.opts.on("-u") && job.opts.on("--reset") && !job.scratch {
		found("", "")
	}
}

func (job *gitJob) forced() bool { return job.opts.on("--force") || job.opts.unknown }

// gitEnvironment is the environment a git command runs with that the line set: what it exported
// and its own assignments.
func (reader *reader) gitEnvironment(inv *invocation) map[string]value {
	env := map[string]value{}
	for name, held := range inv.here.exported {
		env[name] = held
	}
	for name, held := range inv.own {
		env[name] = held
	}
	return env
}

// places are the folders git runs in: where the command is, then each `-C` in turn (a relative
// one is taken inside the one before, as git does). "" is a folder not known.
func (job *gitJob) places() []string {
	here := job.inv.here
	current := []string{""}
	if here.places != nil {
		current = slices.Clone(here.places)
	}
	for _, place := range job.call.places {
		if place == nil {
			return []string{""}
		}
		named, ok := place.single()
		if !ok {
			return []string{""}
		}
		if path.IsAbs(named) {
			current = []string{flat(named)}
			continue
		}
		next := make([]string, 0, len(current))
		for _, folder := range current {
			if folder == "" {
				return []string{""}
			}
			next = append(next, flat(path.Join(folder, named)))
		}
		current = next
	}
	return current
}

func (job *gitJob) repositoryElsewhere() bool {
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR"} {
		if held, ok := job.env[name]; ok && (!held.known() || !slices.Equal(held.texts, []string{""})) {
			return true
		}
		if job.reader.environ[name] != "" {
			return true
		}
	}
	for _, entry := range job.call.configs {
		key := strings.ToLower(entry.key)
		if entry.known && (key == "core.worktree" || key == "core.bare") {
			return true
		}
	}
	return false
}

// Keys that, given through GIT_CONFIG_COUNT or GIT_CONFIG_PARAMETERS, change what the guard reads:
// an alias, an include, where the repository is, a protection switched off, where a push goes.
var envUnread = []string{"alias.", "include.", "includeif.", "core.worktree", "core.bare", "clean.requireforce",
	"remote.", "branch.", "push.", "url."}

func unreadKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, prefix := range envUnread {
		if strings.HasSuffix(prefix, ".") && strings.HasPrefix(key, prefix) || key == prefix {
			return true
		}
	}
	return false
}

// configUnread is why the configuration given to this git command cannot be read, or "": an alias
// or an include on the line, a key not known, or GIT_CONFIG_* the guard cannot read key by key.
func (job *gitJob) configUnread() string {
	for _, entry := range job.call.configs {
		if !entry.known {
			return "a configuration entry that is not known"
		}
		key := strings.ToLower(entry.key)
		if strings.HasPrefix(key, "alias.") || strings.HasPrefix(key, "include") {
			return "an alias or an include"
		}
	}
	if params, ok := job.env["GIT_CONFIG_PARAMETERS"]; ok {
		if !params.known() || len(params.texts) != 1 {
			return "GIT_CONFIG_PARAMETERS"
		}
		if params.texts[0] != "" {
			keys, ok := parametersKeys(params.texts[0])
			if !ok {
				return "GIT_CONFIG_PARAMETERS"
			}
			for _, key := range keys {
				if unreadKey(key) {
					return "GIT_CONFIG_PARAMETERS"
				}
			}
		}
	}
	if count, ok := job.env["GIT_CONFIG_COUNT"]; ok {
		if !count.known() || len(count.texts) != 1 {
			return "GIT_CONFIG_COUNT"
		}
		text := strings.TrimSpace(count.texts[0])
		if text != "" {
			number, err := strconv.Atoi(text)
			if err != nil || number < 0 || number > 999 {
				return "GIT_CONFIG_COUNT"
			}
			for index := range number {
				key, ok := job.env["GIT_CONFIG_KEY_"+strconv.Itoa(index)]
				if !ok || !key.known() || len(key.texts) != 1 {
					return "GIT_CONFIG_KEY"
				}
				if _, ok := job.env["GIT_CONFIG_VALUE_"+strconv.Itoa(index)]; !ok {
					return "GIT_CONFIG_VALUE"
				}
				if unreadKey(key.texts[0]) {
					return "GIT_CONFIG_KEY"
				}
			}
		}
	}
	for _, name := range []string{"GIT_CONFIG", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		if held, ok := job.env[name]; ok && (!held.known() || len(held.texts) != 1 || held.texts[0] != "/dev/null") {
			return name
		}
	}
	return ""
}

var parameter = regexp.MustCompile(`^'((?:[^']|'\\'')*)'(?:=('(?:[^']|'\\'')*'))?$`)

// parametersKeys reads the keys of GIT_CONFIG_PARAMETERS (git's own quoting: `'key'='value' …`).
func parametersKeys(text string) ([]string, bool) {
	var keys []string
	for _, item := range strings.Fields(text) {
		match := parameter.FindStringSubmatch(item)
		if match == nil {
			return nil, false
		}
		keys = append(keys, strings.ReplaceAll(match[1], `'\''`, "'"))
	}
	return keys, true
}

// configOff says whether `-c key=false` (or 0, no, off, empty) or `--config-env key=VAR` may switch
// a protection off on this line.
func (job *gitJob) configOff(key string) bool {
	for _, entry := range job.call.configs {
		if strings.ToLower(entry.key) != key {
			continue
		}
		if entry.fromVar || entry.valueUnknown {
			return true
		}
		if entry.hasValue && slices.Contains([]string{"false", "0", "no", "off", ""}, strings.ToLower(strings.TrimSpace(entry.value))) {
			return true
		}
	}
	return false
}

// knownTexts are the paths words name, each text a word may be among them (a brace list names
// several); false when one is only known when the command runs.
func knownTexts(words []arg) ([]string, bool) {
	texts := make([]string, 0, len(words))
	for _, one := range words {
		if !one.known() {
			return nil, false
		}
		texts = append(texts, one.texts...)
	}
	return texts, true
}

// loses is why writing over paths is refused, or "" when git says there is nothing in them to lose.
func (job *gitJob) loses(paths []arg, mode string) string {
	texts, known := knownTexts(paths)
	if len(paths) == 0 || !known {
		shown := make([]string, 0, len(paths))
		for _, one := range paths {
			shown = append(shown, one.written)
		}
		named := truncate(strings.Join(shown, " "), 120)
		if named == "" {
			named = "the named paths"
		}
		return "it overwrites whatever is uncommitted in " + named
	}
	for _, where := range job.wheres {
		found, ok := []string(nil), false
		if where != "" {
			found, ok = job.reader.questions.changed(where, texts, mode)
		}
		if !ok {
			return "git could not say that nothing is uncommitted in " + truncate(strings.Join(texts, " "), 120) + ", which it overwrites"
		}
		if len(found) > 0 {
			trimmed := make([]string, 0, len(found))
			for _, entry := range found {
				trimmed = append(trimmed, strings.TrimSpace(entry))
			}
			return "it overwrites the uncommitted changes in " + truncate(strings.Join(trimmed, ", "), 160)
		}
	}
	return ""
}

func (job *gitJob) sided() bool {
	return job.opts.any("--ours", "--theirs", "--merge", "--conflict")
}

// takesSide: taking one side of a conflicted path is how a conflict is resolved; on a path that is
// not in conflict the same words throw its edits away. A merge with conflicts must be under way,
// and every edited path under the pathspec must be in conflict.
func (job *gitJob) takesSide() bool {
	if !job.sided() || job.opts.on("--pathspec-from-file") {
		return false
	}
	paths := job.opts.operands
	if job.opts.dashAt >= 0 {
		paths = job.opts.afterDash()
	}
	texts, known := knownTexts(paths)
	if !known {
		return false
	}
	for _, where := range job.wheres {
		if where == "" || !job.reader.questions.conflicts(where) {
			return false
		}
		found, ok := job.reader.questions.unrelated(where, texts)
		if !ok {
			job.others = append(job.others, "(git could not say which paths are edited)")
			return false
		}
		if len(found) > 0 {
			job.others = append(job.others, found...)
			return false
		}
	}
	return true
}

func (job *gitJob) oneSideRefused() string {
	if len(job.others) > 0 {
		return "it also overwrites uncommitted edits in paths that are not in conflict: " + truncate(strings.Join(job.others, ", "), 200)
	}
	return "no merge is under way here, so taking one side overwrites the uncommitted changes in the path"
}

const hiddenEdits = "it writes over files git was told not to look at (skip-worktree), and git does not report the edits in them"

func (job *gitJob) checkoutMovesBranch(found report) {
	if name, ok := job.opts.value("-B"); ok {
		job.movedBranch(name, found)
	}
}

func (job *gitJob) checkoutForce(found report) {
	if !job.scratch && job.forced() {
		found("", "")
	}
}

func (job *gitJob) checkoutPath(found report) {
	opts := job.opts
	if job.scratch {
		return
	}
	if opts.on("--ignore-skip-worktree-bits") {
		found(hiddenEdits, "")
		return
	}
	if opts.on("-B") || job.takesSide() {
		return
	}
	if (opts.on("-b") || opts.on("--orphan") || opts.on("--detach")) && !job.sided() {
		return
	}
	if job.sided() {
		found(job.oneSideRefused(), rowOf("git-checkout-path").Instead+"; while a merge is under way, name only the paths that are in conflict")
		return
	}
	plain := opts.beforeDash()
	if opts.dashAt >= 0 {
		paths := opts.afterDash()
		if len(paths) > 0 {
			mode := modeIndex
			if len(plain) > 0 {
				mode = modeTree
			}
			if why := job.loses(paths, mode); why != "" {
				found(why, "")
			}
		}
		return
	}
	var paths []arg
	if len(plain) > 1 {
		paths = plain[1:]
	}
	if len(plain) == 1 {
		name := plain[0].value()
		there := false
		allBranches := true
		for _, where := range job.wheres {
			if where != "" && fileExists(path.Join(where, name)) {
				there = true
				if job.reader.questions.branchExists(where, name) != answerYes {
					allBranches = false
				}
			}
		}
		if name == "." || strings.HasPrefix(name, ":") || strings.ContainsAny(name, "*?[") || (there && !allBranches) {
			paths = plain
		}
	}
	switch {
	case opts.on("--pathspec-from-file") || opts.on("--patch"):
		found("", "")
	case len(paths) > 0:
		mode := modeIndex
		if len(plain) > len(paths) {
			mode = modeTree
		}
		if why := job.loses(paths, mode); why != "" {
			found(why, "")
		}
	}
}

func (job *gitJob) switchDiscards(found report) {
	if (job.opts.on("--discard-changes") || job.forced()) && !job.scratch {
		found("", "")
	}
}

func (job *gitJob) switchMovesBranch(found report) {
	if job.opts.on("--force-create") {
		name, _ := job.opts.value("--force-create")
		job.movedBranch(name, found)
	}
}

func (job *gitJob) restore(found report) {
	opts := job.opts
	staged, worktree := opts.on("--staged"), opts.on("--worktree")
	bits := opts.on("--ignore-skip-worktree-bits")
	if job.scratch || (staged && !worktree && !opts.unknown) || (!bits && job.takesSide()) {
		return
	}
	why := "it overwrites uncommitted changes in the working tree"
	if bits {
		why = hiddenEdits
	}
	if !bits && !job.sided() && !opts.unknown && !opts.any("--pathspec-from-file", "--patch") {
		mode := modeIndex
		if _, source := opts.value("--source"); staged || source {
			mode = modeTree
		}
		why = job.loses(opts.operands, mode)
		if why == "" {
			return
		}
	}
	if job.sided() && !bits {
		why = job.oneSideRefused()
	}
	found(why, "")
}

// mayExist says whether a branch of that name may already be there.
func (job *gitJob) mayExist(name string) bool {
	for _, where := range job.wheres {
		if where == "" || job.reader.questions.branchExists(where, name) != answerNo {
			return true
		}
	}
	return false
}

// movedBranch finds a branch made to point elsewhere (`-B`, `-C`, `branch -f`) when it exists
// and nothing else holds its commits.
func (job *gitJob) movedBranch(name string, found report) {
	if name == "" {
		return
	}
	for _, where := range job.wheres {
		held := job.reader.questions.held(where, name)
		if held == answerNo || (held == answerUnsure && job.reader.questions.branchExists(where, name) != answerNo) {
			found("it moves the branch "+name+" off commits that no other branch, tag or remote ref holds",
				"keep them reachable first: `git tag archive/"+name+" "+name+"`; or make a new branch with `-b` / `-c`")
			return
		}
	}
}

func (job *gitJob) branchNames() ([]string, map[string]bool) {
	var names []string
	unnamed := map[string]bool{}
	for _, one := range job.opts.operands {
		names = append(names, one.value())
		if !one.known() || len(one.texts) > 1 {
			unnamed[one.value()] = true
		}
	}
	return names, unnamed
}

func (job *gitJob) branchDeleting() bool {
	opts := job.opts
	return opts.on("-D") || (opts.on("--delete") && job.forced()) || opts.unknown
}

func (job *gitJob) branchMoves(found report) {
	opts := job.opts
	if opts.on("--remotes") || job.branchDeleting() {
		return
	}
	names, _ := job.branchNames()
	renaming := opts.any("--move", "-M", "--copy", "-C")
	switch {
	case job.forced() && !renaming && len(names) > 0:
		job.movedBranch(names[0], found)
	case len(names) > 0 && (opts.any("-M", "-C") || (renaming && job.forced())) && job.mayExist(names[len(names)-1]):
		job.movedBranch(names[len(names)-1], found)
	}
}

func (job *gitJob) branchDeletes(found report) {
	opts := job.opts
	names, unnamed := job.branchNames()
	if opts.on("--remotes") || !job.branchDeleting() {
		return
	}
	var lost []string
	for _, name := range names {
		if unnamed[name] {
			lost = append(lost, name)
			continue
		}
		for _, where := range job.wheres {
			if job.reader.questions.held(where, name) != answerYes && !job.reader.questions.addsNothing(where, name) {
				lost = append(lost, name)
				break
			}
		}
	}
	if len(lost) > 0 || len(names) == 0 {
		shown := strings.Join(lost, ", ")
		if shown == "" {
			shown = "that branch"
		}
		found("no other branch, tag or remote ref holds the commits of "+
			truncate(shown, 160)+", and merging it into the default branch (origin/main, origin/master, main, master) would still change files", "")
	}
}

func (job *gitJob) worktree(found report) {
	if len(job.call.rest) == 0 {
		return
	}
	if sub, ok := job.call.rest[0].single(); !ok || sub != "remove" {
		return
	}
	opts := parseOptions(job.call.rest[1:], gitOptions["worktree"])
	if !opts.on("--force") && !opts.unknown {
		return
	}
	clean := len(opts.operands) > 0
	for _, target := range opts.operands {
		named, ok := target.single()
		for _, where := range job.wheres {
			there := ""
			switch {
			case !ok:
			case path.IsAbs(named):
				there = flat(named)
			case where != "":
				there = flat(path.Join(where, named))
			}
			if there == "" {
				clean = false
				continue
			}
			found, answered := job.reader.questions.changed(there, nil, modeWorktree)
			clean = clean && answered && len(found) == 0
		}
	}
	if !clean {
		found("", "")
	}
}

func (job *gitJob) updateRefDeletes(found report) {
	opts := job.opts
	if opts.on("--delete") || opts.on("--stdin") || opts.unknown {
		found("", "")
	}
}

// updateRefTarget is the ref update-ref moves, when it moves one named on the line.
func (job *gitJob) updateRefTarget() (string, bool) {
	opts := job.opts
	if opts.on("--delete") || opts.on("--stdin") || opts.unknown || len(opts.operands) == 0 {
		return "", false
	}
	return opts.operands[0].single()
}

func (job *gitJob) updateRefMoves(found report) {
	opts := job.opts
	if opts.on("--stdin") || opts.unknown {
		found("", "") // what --stdin moves is not on the line
		return
	}
	if opts.on("--delete") {
		return
	}
	if len(opts.operands) == 0 {
		found("", "")
		return
	}
	target, known := job.updateRefTarget()
	switch {
	case known && strings.HasPrefix(target, "refs/heads/"):
	case known && strings.HasPrefix(target, "refs/tags/") && job.newTag(target):
	case !known || (!strings.HasPrefix(target, "refs/remotes/") && !strings.HasPrefix(target, "refs/jackstay-")):
		found("", "")
	}
}

func (job *gitJob) updateRefMovesBranch(found report) {
	target, known := job.updateRefTarget()
	if known && strings.HasPrefix(target, "refs/heads/") {
		name := strings.TrimPrefix(target, "refs/heads/")
		if job.mayExist(name) {
			job.movedBranch(name, found)
		}
	}
}

func (job *gitJob) newTag(ref string) bool {
	for _, where := range job.wheres {
		if where == "" || job.reader.questions.refExists(where, ref) != answerNo {
			return false
		}
	}
	return true
}

func (job *gitJob) checkoutIndex(found report) {
	prefixes := 0
	elsewhereOnly := true
	for _, one := range job.opts.settings {
		if one.name != "--prefix" {
			continue
		}
		prefixes++
		if one.value == "" || strings.Contains(one.value, "$") || !job.reader.disposable(one.value, job.inv.here) {
			elsewhereOnly = false
		}
	}
	if job.forced() && !job.scratch && (prefixes == 0 || !elsewhereOnly) {
		found("", "")
	}
}

func (job *gitJob) config(found report) {
	opts := job.opts
	if opts.any("--get", "--get-all", "--get-regexp", "--get-urlmatch", "--unset", "--unset-all", "--list",
		"--show-origin", "--show-scope", "--name-only") {
		return
	}
	words := opts.operands
	if len(words) > 0 {
		if first, ok := words[0].single(); ok && slices.Contains([]string{"set", "unset", "get", "list"}, first) {
			if first != "set" {
				return
			}
			words = words[1:]
		}
	}
	if len(words) < 2 {
		return
	}
	key, known := words[0].single()
	if !known || strings.EqualFold(key, "core.worktree") || strings.EqualFold(key, "core.bare") {
		found("", "")
	}
}

func (job *gitJob) submodule() {
	var rest []arg
	for _, one := range job.call.rest {
		if text, ok := one.single(); ok && (text == "--quiet" || text == "-q" || text == "--recursive") {
			continue
		}
		rest = append(rest, one)
	}
	if len(rest) < 2 {
		return
	}
	if sub, ok := rest[0].single(); !ok || sub != "foreach" {
		return
	}
	script := rest[1:]
	if len(script) == 1 {
		job.inv.process.childScript(job.inv, script[0], false, "sh")
		return
	}
	texts := make([]string, 0, len(script))
	for _, one := range script {
		texts = append(texts, one.value())
	}
	job.inv.process.childScript(job.inv, literalArg(strings.Join(texts, " ")), false, "sh")
}
