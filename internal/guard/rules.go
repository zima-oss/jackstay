package guard

import (
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
)

// A Rule is one row of the table (guard.md section 4): what it refuses, the destructive form
// as words for the fallback scan, and what a refusal says. When it applies and what makes a
// command safe are the judge's named checks below, one per program.
type Rule struct {
	ID      string
	Program string
	Words   [][]string // each a form: words in order, `-x` a letter in a cluster, `a|b` either
	Unless  []string   // a word in the piece that makes the scanned form safe
	Why     string
	Instead string
}

const trashAdvice = "delete it with `jst trash <path>`, which logs what it deletes"

// discardAdvice is how uncommitted changes are dropped with a record of what went (W-052, W-107).
const discardAdvice = "`jst discard <path>` puts the path back to HEAD and logs the changes it drops " +
	"(kept to restore when trash.keep is on)"

// hostMergeWhy and hostMergeInstead are the words of the rows that refuse a merge on the host (land.md section 3.3).
const hostMergeWhy = "it merges the change on the host outside jst land, past the project's landing policy " +
	"(its required checks and the paths only a person lands)"

const hostMergeInstead = "`jst land` pushes, waits for the required checks and merges when the project's policy lets the agent; " +
	"a change for a person stops at jst land: ready for a person"

var table = []Rule{
	{ID: "git-reset-hard", Program: "git reset", Words: [][]string{{"git", "reset", "--hard"}},
		Why:     "it throws away every uncommitted change in the checkout",
		Instead: discardAdvice + "; `git reset --keep <commit>` moves the branch and refuses to lose local changes"},
	{ID: "git-checkout-path", Program: "git checkout",
		Words: [][]string{{"git", "checkout", "--"}, {"git", "checkout", "."}},
		Why:   "it overwrites the uncommitted changes in the paths it is given",
		Instead: discardAdvice + "; to take files from another commit, `jst discard <path>` first, then `git checkout <commit> -- <path>` " +
			"(a folder must also hold no untracked or ignored file: `git status --ignored <path>` lists them, `jst trash <path>` moves them); " +
			"to change branch, `git switch <branch>`"},
	{ID: "git-checkout-force", Program: "git checkout", Words: [][]string{{"git", "checkout", "-f|--force"}},
		Why: "it throws away local changes that are in the way", Instead: "`git stash push -u`, then `git switch <branch>`"},
	{ID: "git-switch-discard", Program: "git switch", Words: [][]string{{"git", "switch", "-f|--force|--discard-changes"}},
		Why: "it throws away local changes that are in the way", Instead: "`git stash push -u`, then `git switch <branch>`"},
	{ID: "git-restore-worktree", Program: "git restore", Words: [][]string{{"git", "restore"}}, Unless: []string{"--staged", "-S"},
		Why:     "it overwrites uncommitted changes in the working tree",
		Instead: "`git restore --staged <path>` only unstages; to drop the edits, " + discardAdvice},
	{ID: "git-clean-force", Program: "git clean", Words: [][]string{{"git", "clean", "-f|--force"}},
		Why:     "it deletes untracked files, which git cannot bring back",
		Instead: "`git clean -n` shows what would go; then `jst trash <path>` the ones you mean, or `git stash push -u`"},
	{ID: "git-rm-force", Program: "git rm", Words: [][]string{{"git", "rm", "-f|--force"}},
		Why:     "it deletes the files even where they hold uncommitted changes",
		Instead: "`git rm` without -f refuses to lose local changes; `git rm --cached` only takes the file out of the index"},
	{ID: "git-push-force", Program: "git push", Words: [][]string{{"git", "push", "-f|--force|--mirror"}},
		Why:     "it overwrites the remote branch whatever is on it now",
		Instead: "`git push --force-with-lease` (add `--force-if-includes`) with a plain refspec: it refuses when the remote moved since your last fetch"},
	{ID: "git-push-prune", Program: "git push",
		Why:     "it deletes every remote branch that has no local branch of the same name, for everyone",
		Instead: "`git push --prune --dry-run` shows what would go; delete one branch with `git push <remote> --delete <name>`"},
	{ID: "git-push-delete-protected", Program: "git push",
		Why:     "it deletes main or master on the remote, for everyone",
		Instead: "nothing: main and master are not deleted from a session; another branch may be deleted this way"},
	{ID: "git-branch-force-delete", Program: "git branch", Words: [][]string{{"git", "branch", "-D"}},
		Why:     "no other branch, tag or remote ref holds its commits, and merging it into the default branch would still change files",
		Instead: "`jst tidy --branch <name>` says whether its work is done and `--fix` deletes it when so; `git branch -d <name>` deletes only what is merged; `git log origin/main..<name>` (your default branch) shows what would be lost; after a squash merge `git fetch` first; or keep the commits reachable (`git tag archive/<name> <name>`) before deleting"},
	{ID: "git-branch-reset", Program: "git branch",
		Why:     "it moves a branch off commits that no other branch, tag or remote ref holds",
		Instead: "keep them reachable first: `git tag archive/<name> <name>`; or make a new branch with `-b` / `-c`"},
	{ID: "git-stash-drop", Program: "git stash", Words: [][]string{{"git", "stash", "drop"}},
		Why:     "a dropped stash is gone from every list and is hard to find again",
		Instead: "`git stash apply` keeps the stash; `git stash branch <name>` turns it into a branch; or leave it"},
	{ID: "git-stash-clear", Program: "git stash", Words: [][]string{{"git", "stash", "clear"}},
		Why:     "a cleared stash is gone from every list and is hard to find again",
		Instead: "`git stash apply` keeps the stash; `git stash branch <name>` turns it into a branch; or leave it"},
	{ID: "git-worktree-force", Program: "git worktree",
		Words:   [][]string{{"git", "worktree", "remove", "-f|--force"}, {"git", "worktree", "-f|--force", "remove"}},
		Why:     "it deletes a worktree that has, or may have, uncommitted or untracked files in it",
		Instead: "`jst tidy --worktree <path>` says whether its work is done and `--fix` removes it and its branch when so; or `git worktree remove <path>` without --force: git itself then refuses to lose them"},
	{ID: "git-update-ref-delete", Program: "git update-ref", Words: [][]string{{"git", "update-ref", "-d|--delete|--stdin"}},
		Why: "it deletes a ref with none of the checks `git branch -d` and `git tag -d` make", Instead: "`git branch -d <name>` (refuses what is not merged) or `git tag -d <name>`"},
	{ID: "git-update-ref-move", Program: "git update-ref",
		Why:     "it moves a ref (a branch, a tag, HEAD) with none of the checks `git branch -f` makes: the commits it pointed at may be left on no ref",
		Instead: "`git branch -f <name> <commit>` (refused when that loses commits), or `git tag -f` for a tag"},
	{ID: "git-filter-branch", Program: "git filter-branch", Words: [][]string{{"git", "filter-branch"}},
		Why:     "it rewrites the history of every branch it is given, and the old commits are easy to lose",
		Instead: "the rewrite is the owner's to run in their own terminal"},
	{ID: "git-filter-repo", Program: "git filter-repo", Words: [][]string{{"git", "filter-repo"}, {"git-filter-repo"}},
		Unless:  []string{"--analyze", "--dry-run"},
		Why:     "it rewrites the history of every branch it is given, and the old commits are easy to lose; filter-repo also removes the `origin` remote",
		Instead: "`git filter-repo --analyze` or `--dry-run` shows what it would do; the rewrite itself is the owner's to run in their own terminal"},
	{ID: "git-read-tree-reset", Program: "git read-tree",
		Why:     "it writes the tree over the working tree and throws away uncommitted changes",
		Instead: "`git read-tree -u -m <tree>` refuses to lose local changes; drop them first with `jst discard <path>`, then `git read-tree -u -m <tree>`"},
	{ID: "git-checkout-index-force", Program: "git checkout-index",
		Why:     "it writes the index over files with uncommitted changes",
		Instead: discardAdvice + "; `git checkout-index --prefix=<scratch>/ -a` writes a copy elsewhere"},
	{ID: "git-config-alias", Program: "git",
		Why:     "it sets git configuration on the command line (`-c alias.…`, an include, or GIT_CONFIG_* variables), and what that runs or reads is not seen",
		Instead: "run the git command itself, written out"},
	{ID: "git-config-worktree", Program: "git config",
		Why:     "it points this repository's work tree at another folder, so what git does here (a reset, a clean) happens to that folder",
		Instead: "clone into the folder you mean to work in instead"},
	{ID: "cards-push", Program: "git push",
		Why:     "it adds commits to the cards branch without valid `Card:` lines",
		Instead: "reword them (`git rebase -i`, `reword`) to carry `Card: <ID>` or `Card: none (why)` on a line of its own; `jst lint range origin/<branch>..HEAD` checks them"},
	{ID: "card-push-unreadable-config", Program: "git push",
		Why:     "the project declares `cards` in jackstay.json and the section cannot be used, so a push that may move a remote branch cannot be judged",
		Instead: "mend jackstay.json's `cards` section first (`jst check`)"},
	{ID: "rm-recursive", Program: "rm", Words: [][]string{{"rm", "-r|-R|--recursive"}},
		Why: "a recursive rm outside temporary and regenerable folders cannot be undone", Instead: trashAdvice},
	{ID: "find-delete", Program: "find", Words: [][]string{{"find", "-delete"}},
		Why: "it deletes every file it finds, which nothing brings back", Instead: "`find … -print` first to see what it finds; then `jst trash` what you mean"},
	{ID: "gh-pr-merge", Program: "gh pr", Words: [][]string{{"gh", "pr", "merge"}}, Unless: []string{"--help", "-h", "help"},
		Why:     hostMergeWhy,
		Instead: hostMergeInstead},
	{ID: "glab-mr-merge", Program: "glab mr", Words: [][]string{{"glab", "mr", "merge"}}, Unless: []string{"--help", "-h", "help"},
		Why:     hostMergeWhy,
		Instead: hostMergeInstead},
	{ID: "env-split-string", Program: "env",
		Why: "`env -S` runs the words of a string the guard cannot read", Instead: "run the command itself, without `env -S`"},
	{ID: "zsh-glob", Program: "zsh",
		Why: "zsh takes it for a file pattern and stops the command with `no matches found`", Instead: "quote the pattern"},
}

func rowOf(id string) Rule {
	for _, row := range table {
		if row.ID == id {
			return row
		}
	}
	return Rule{ID: id, Why: "the guard refuses it", Instead: "ask the owner"}
}

// refuse adds a finding of a table row: its own words unless the judge says more.
func (reader *reader) refuse(id, command, why, instead string) {
	row := rowOf(id)
	if why == "" {
		why = row.Why
	}
	if instead == "" {
		instead = row.Instead
	}
	reader.add(Finding{Rule: id, Command: command, Why: why, Instead: instead})
}

func (reader *reader) refuseEnvSplit(inv *invocation) {
	reader.refuse("env-split-string", inv.shown(), "", "")
}

// rm refuses a recursive rm of anything that is not disposable.
func (reader *reader) rm(inv *invocation) {
	recursive, unknown := false, false
	var targets []arg
	optionsOver := false
	for _, one := range inv.args[1:] {
		text, known := one.single()
		switch {
		case !optionsOver && known && text == "--":
			optionsOver = true
		case !optionsOver && one.unknownOption():
			unknown = true
		case !optionsOver && known && strings.HasPrefix(text, "--"):
			recursive = recursive || (len(text) >= 4 && strings.HasPrefix("--recursive", text))
		case !optionsOver && known && strings.HasPrefix(text, "-") && text != "-":
			recursive = recursive || strings.ContainsAny(text[1:], "rR")
		default:
			targets = append(targets, one)
		}
	}
	if !recursive && !unknown {
		return
	}
	var bad []string
	for _, one := range targets {
		switch {
		case one.braced:
			bad = append(bad, one.written+" (a brace list: write each path out)")
		case one.spreads:
			bad = append(bad, one.written+" (unquoted, a shell splits it into several paths)")
		case !one.known():
			if !reader.regenerableTail(one) {
				bad = append(bad, one.written+" (its path is only known when the command runs)")
			}
		default:
			for _, text := range one.texts {
				if !reader.disposable(text, inv.here) && !reader.excepted("rm-recursive", text, inv.here) {
					bad = append(bad, one.written)
					break
				}
			}
		}
	}
	if len(bad) > 0 {
		reader.refuse("rm-recursive", inv.shown(), "a recursive rm outside temporary and regenerable folders cannot be undone: "+
			truncate(strings.Join(bad, ", "), 300), trashAdvice+"; or delete the named files without -r. Free to delete: what is under "+
			"the session's own scratch folder, a folder mktemp made in the same command (/tmp and $TMPDIR are every session's), and "+
			"regenerable folders ("+strings.Join(reader.regenerable, ", ")+"; where the folder is not known, write the name last and "+
			"without a trailing slash)")
	}
}

func truncate(text string, limit int) string {
	if len(text) > limit {
		return text[:limit]
	}
	return text
}

// hostValuedOptions are the options of gh and glab that take a value, which may stand before the
// command group (`gh -R owner/repo pr merge 3`).
var hostValuedOptions = []string{"-R", "--repo"}

// hostMerge refuses `gh pr merge` and `glab mr merge` (row), however the repository is named before
// the group; a help request for it is harmless.
func (reader *reader) hostMerge(inv *invocation, group, row string) {
	var words []string
	for index := 1; index < len(inv.args); index++ {
		text := inv.args[index].value()
		switch {
		case text == "--help" || text == "-h":
			return
		case slices.Contains(hostValuedOptions, text):
			index++
		case strings.HasPrefix(text, "-"):
		default:
			words = append(words, text)
		}
	}
	if len(words) >= 2 && words[0] == group && words[1] == "merge" {
		reader.refuse(row, inv.shown(), "", "")
	}
}

// find refuses `-delete` outside what may be removed whole, and reads what `-exec` runs.
func (reader *reader) find(inv *invocation) {
	values := inv.values(1)
	var names []string
	plain := true
	for index, text := range values {
		if text == "-name" && index+1 < len(values) {
			names = append(names, values[index+1])
		}
		if slices.Contains([]string{"-o", "-or", "!", "-not", "(", ")", ","}, text) {
			plain = false
		}
	}
	regenerable := len(names) > 0 && plain
	for _, name := range names {
		if !reader.regenerableName(name, "") || (strings.ContainsAny(name, "*?[") && !slices.Contains(reader.regenerable, name)) {
			regenerable = false
		}
	}
	if slices.Contains(values, "-delete") && !regenerable {
		var starts []arg
		rest := inv.args[1:]
		for len(rest) > 0 {
			text, ok := rest[0].single()
			if !ok || !slices.Contains([]string{"-H", "-L", "-P", "-E", "-X", "-d", "-s", "-x"}, text) {
				break
			}
			rest = rest[1:]
		}
		for _, one := range rest {
			if text, ok := one.single(); ok && (strings.HasPrefix(text, "-") || text == "(" || text == "!" || text == ")") {
				break
			}
			starts = append(starts, one)
		}
		safe := len(starts) > 0
		// -L, -H and -follow follow links below the start, which the start's resolution does not see.
		following := slices.ContainsFunc(values, func(text string) bool { return text == "-L" || text == "-H" || text == "-follow" })
		for _, one := range starts {
			text, ok := one.single()
			if !ok || (!reader.disposable(text, inv.here) && (following || !reader.excepted("find-delete", text, inv.here))) {
				safe = false
			}
		}
		if !safe {
			reader.refuse("find-delete", inv.shown(), "", "")
		}
	}
	for index, text := range values {
		if text != "-exec" && text != "-execdir" && text != "-ok" && text != "-okdir" {
			continue
		}
		end := len(values)
		for later := index + 1; later < len(values); later++ {
			if values[later] == ";" || values[later] == "+" {
				end = later
				break
			}
		}
		var inner []arg
		for _, one := range inv.args[index+2 : end+1] {
			if one.value() != "{}" {
				inner = append(inner, one)
			}
		}
		if len(inner) == 0 {
			continue
		}
		if regenerable && path.Base(inner[0].value()) == "rm" && allOptions(inner[1:]) {
			continue
		}
		run := &invocation{process: inv.process, here: inv.here, args: append(inner, opaqueArg("<what find found>")), own: map[string]value{},
			external: true, globbing: false}
		inv.process.invoke(run, flow{})
	}
}

func allOptions(words []arg) bool {
	for _, one := range words {
		text, ok := one.single()
		if !ok || !strings.HasPrefix(text, "-") {
			return false
		}
	}
	return true
}

// link remembers a link `ln -s` makes: it is not on disk yet when the guard reads the line, so
// `rm -r link/` is judged by where it leads.
func (reader *reader) link(inv *invocation) *state {
	symbolic := false
	var names []arg
	for _, one := range inv.args[1:] {
		text, ok := one.single()
		if ok && strings.HasPrefix(text, "--") {
			symbolic = symbolic || text == "--symbolic"
			continue
		}
		if ok && strings.HasPrefix(text, "-") && text != "-" {
			symbolic = symbolic || strings.Contains(text, "s")
			continue
		}
		names = append(names, one)
	}
	if !symbolic || len(names) < 2 {
		return inv.here
	}
	after := inv.here.clone()
	into, known := names[len(names)-1].single()
	if !known {
		return after
	}
	for _, source := range names[:len(names)-1] {
		target, targetKnown := source.single()
		if after.places == nil {
			if !path.IsAbs(into) {
				after.links[path.Clean(into)] = nil
				continue
			}
		}
		folders := after.places
		if path.IsAbs(into) || folders == nil {
			folders = []string{"/"}
		}
		for _, folder := range folders {
			made := flat(path.Join(folder, into))
			for _, place := range []string{made, path.Join(made, path.Base(strings.TrimRight(target, "/")))} {
				if !targetKnown {
					after.links[place] = nil
					continue
				}
				leads := flat(path.Join(path.Dir(place), target))
				if path.IsAbs(target) {
					leads = flat(target)
				}
				after.links[place] = &leads
			}
		}
	}
	return after
}

var (
	findPatterns = []string{"-name", "-iname", "-path", "-ipath", "-wholename", "-iwholename", "-lname", "-ilname"}
	grepPatterns = []string{"--include", "--exclude", "--exclude-dir"}
	patternOpts  = map[string][]string{"find": findPatterns, "grep": grepPatterns, "egrep": grepPatterns,
		"fgrep": grepPatterns, "rg": {"-g", "--glob", "--iglob"}}
	notGlobbed = []string{"[[", "case", "noglob", "alias", "unalias"}
)

// zshPatterns: zsh stops a command whose unquoted pattern matches no file (`no matches found`),
// W-007. Refused, with the word quoted, when the word is an option, the pattern a program is
// given, or a pattern that matches no file where the command runs.
func (reader *reader) zshPatterns(inv *invocation) {
	program := inv.program()
	if slices.Contains(notGlobbed, program) {
		return
	}
	for index := 1; index < len(inv.args); index++ {
		one := inv.args[index]
		if one.dynamic || one.opaque || !one.globbed || strings.HasSuffix(one.written, ")") {
			continue
		}
		if one.braced {
			if empty := reader.bracedEmpty(one, inv.here); empty != "" {
				reader.refuse("zsh-glob", inv.shown(), fmt.Sprintf("zsh expands %s and the pattern %s in it matches no file here: "+
					"it stops the command with `no matches found`", one.written, empty),
					"drop that part, or add (N) after it so that it may match nothing")
				return
			}
			continue
		}
		text := one.value()
		before := inv.args[index-1].value()
		var why string
		switch {
		case strings.HasPrefix(text, "-"):
			why = "it is an option, which no file name matches"
		case slices.Contains(patternOpts[program], before):
			why = "it is the pattern " + before + " is given, and the shell would put file names in its place"
		default:
			if strings.Contains(text, "**") || inv.here.places == nil || matchesSomewhere(text, inv.here.places) {
				continue
			}
			why = "it matches no file here"
		}
		fixed := make([]string, 0, len(inv.args))
		for position, word := range inv.args {
			if position == index {
				fixed = append(fixed, "'"+strings.ReplaceAll(text, "'", `'\''`)+"'")
			} else if !word.opaque {
				fixed = append(fixed, word.written)
			}
		}
		reader.refuse("zsh-glob", inv.shown(), "zsh takes "+one.written+" for a file pattern and "+why+
			": it stops the command with `no matches found` (or puts file names there), so the command does not do what it says",
			"quote the pattern in this part of the command: `"+truncate(strings.Join(fixed, " "), 300)+"`")
		return
	}
}

func matchesSomewhere(pattern string, places []string) bool {
	// `*/` matches folders only. fnmatch's `[!x]` is Go's `[^x]`.
	folders := strings.HasSuffix(pattern, "/")
	pattern = strings.ReplaceAll(strings.TrimRight(pattern, "/"), "[!", "[^")
	for _, folder := range places {
		full := pattern
		if !path.IsAbs(pattern) {
			full = strings.TrimRight(folder, "/") + "/" + pattern // `..` through a folder that is gone leads nowhere
		}
		if matched, readable := globMatches(full, folders); matched || !readable {
			return true
		}
	}
	return false
}

// globMatches says whether a pattern matches a name on disk as zsh matches it: a pattern part
// that does not start with a dot does not match a hidden name. readable is false when the pattern
// cannot be read, which is taken as a match (the guard does not refuse what it cannot judge).
func globMatches(pattern string, folders bool) (bool, bool) {
	parts := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	current := []string{"/"}
	for _, part := range parts {
		var next []string
		if !strings.ContainsAny(part, "*?[") {
			for _, folder := range current {
				candidate := strings.TrimRight(folder, "/") + "/" + part
				if fileExists(candidate) {
					next = append(next, candidate)
				}
			}
		} else {
			if _, err := path.Match(part, ""); err != nil {
				return false, false
			}
			for _, folder := range current {
				entries, err := os.ReadDir(folder)
				if err != nil {
					continue
				}
				for _, entry := range entries {
					name := entry.Name()
					if strings.HasPrefix(name, ".") && !strings.HasPrefix(part, ".") {
						continue
					}
					if matched, _ := path.Match(part, name); matched {
						next = append(next, strings.TrimRight(folder, "/")+"/"+name)
					}
				}
			}
		}
		if len(next) == 0 {
			return false, true
		}
		current = next
	}
	if folders {
		for _, found := range current {
			if info, err := os.Stat(found); err == nil && info.IsDir() {
				return true, true
			}
		}
		return false, true
	}
	return true, true
}

// bracedEmpty is the first pattern a brace word expands to that matches no file where the command runs.
func (reader *reader) bracedEmpty(one arg, here *state) string {
	if here.places == nil || one.word == nil {
		return ""
	}
	exp := expansion{reader: reader, here: here}
	for _, option := range exp.readings(one.word) {
		text := option.text()
		if !option.known() || !strings.ContainsAny(text, "*?[") || strings.Contains(text, "**") {
			continue
		}
		if !matchesSomewhere(text, here.places) {
			return text
		}
	}
	return ""
}

// matchName matches one name against a jackstay.json pattern (fnmatch's `[!x]` is `[^x]`).
func matchName(pattern, name string) bool {
	matched, err := path.Match(strings.ReplaceAll(pattern, "[!", "[^"), name)
	return err == nil && matched
}
