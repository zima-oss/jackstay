package guard

import (
	"strings"
)

// An option of git, as git's own parser takes it: its long name, its short letter, whether it
// takes a value. One model, one parser (guard.md section 5).
type option struct {
	long  string // "--hard"; "" for a letter only
	short byte   // 'f'; 0 for none
	value bool   // takes a value, attached (`--x=v`, `-sv`) or as the next word
}

func (opt option) name() string {
	if opt.long != "" {
		return opt.long
	}
	return "-" + string(opt.short)
}

func flag(long string, short byte) option   { return option{long: long, short: short} }
func valued(long string, short byte) option { return option{long: long, short: short, value: true} }

var gitGlobals = []option{
	valued("", 'C'), valued("", 'c'), valued("--config-env", 0), valued("--git-dir", 0), valued("--work-tree", 0),
	valued("--namespace", 0), valued("--super-prefix", 0), valued("--attr-source", 0), valued("--list-cmds", 0),
}

// The options of each subcommand the guard judges. Options not listed are taken as flags.
var gitOptions = map[string][]option{
	"reset": {flag("--hard", 0), flag("--soft", 0), flag("--mixed", 0), flag("--keep", 0), flag("--merge", 0),
		flag("--quiet", 'q'), flag("--patch", 'p'), valued("--pathspec-from-file", 0), flag("--intent-to-add", 'N')},
	"checkout": {valued("", 'b'), valued("", 'B'), valued("--orphan", 0), flag("--detach", 0), flag("--force", 'f'),
		flag("--patch", 'p'), flag("--ours", 0), flag("--theirs", 0), flag("--merge", 'm'), valued("--conflict", 0),
		valued("--pathspec-from-file", 0), flag("--ignore-skip-worktree-bits", 0), flag("--quiet", 'q'), flag("--track", 't'),
		flag("--overlay", 0), flag("--progress", 0), flag("--guess", 0)},
	"switch": {valued("--create", 'c'), valued("--force-create", 'C'), flag("--discard-changes", 0), flag("--force", 'f'),
		flag("--detach", 'd'), valued("--orphan", 0), flag("--track", 't'), flag("--merge", 'm'), valued("--conflict", 0),
		flag("--quiet", 'q'), flag("--guess", 0)},
	"restore": {valued("--source", 's'), flag("--staged", 'S'), flag("--worktree", 'W'), flag("--patch", 'p'),
		flag("--ours", 0), flag("--theirs", 0), flag("--merge", 'm'), valued("--conflict", 0),
		flag("--ignore-skip-worktree-bits", 0), valued("--pathspec-from-file", 0), flag("--overlay", 0),
		flag("--quiet", 'q'), flag("--progress", 0), valued("--unified", 'U')},
	"clean": {flag("--force", 'f'), flag("--dry-run", 'n'), flag("", 'd'), flag("", 'x'), flag("", 'X'),
		valued("--exclude", 'e'), flag("--quiet", 'q'), flag("--interactive", 'i')},
	"rm": {flag("--force", 'f'), flag("--cached", 0), flag("--dry-run", 'n'), flag("", 'r'), flag("--quiet", 'q'),
		flag("--ignore-unmatch", 0), flag("--sparse", 0), valued("--pathspec-from-file", 0)},
	"push": {flag("--force", 'f'), flag("--force-with-lease", 0), flag("--force-if-includes", 0), flag("--mirror", 0),
		flag("--prune", 0), flag("--dry-run", 'n'), flag("--delete", 'd'), flag("--all", 0), flag("--branches", 0),
		flag("--tags", 0), flag("--follow-tags", 0), valued("--repo", 0), valued("--push-option", 'o'),
		valued("--receive-pack", 0), valued("--exec", 0), flag("--set-upstream", 'u'), flag("--verbose", 'v'),
		flag("--quiet", 'q'), flag("--porcelain", 0), flag("--atomic", 0), flag("--no-verify", 0), flag("--thin", 0),
		flag("--progress", 0), flag("--signed", 0), flag("--recurse-submodules", 0), flag("--ipv4", '4'), flag("--ipv6", '6')},
	"branch": {flag("--delete", 'd'), flag("", 'D'), flag("--force", 'f'), flag("--move", 'm'), flag("", 'M'),
		flag("--copy", 'c'), flag("", 'C'), flag("--remotes", 'r'), flag("--all", 'a'), flag("--list", 'l'),
		flag("--quiet", 'q'), flag("--verbose", 'v'), valued("--set-upstream-to", 'u'), flag("--unset-upstream", 0),
		flag("--track", 't'), valued("--format", 0), valued("--sort", 0), valued("--points-at", 0),
		flag("--edit-description", 0), flag("--show-current", 0), flag("--create-reflog", 0)},
	"worktree": {flag("--force", 'f')},
	"update-ref": {flag("--delete", 'd'), flag("--stdin", 0), valued("", 'm'), flag("--no-deref", 0), flag("", 'z'),
		flag("--create-reflog", 0)},
	"filter-repo": {flag("--analyze", 0), flag("--dry-run", 0)},
	"read-tree": {flag("", 'u'), flag("--reset", 0), flag("", 'm'), flag("", 'i'), flag("--dry-run", 'n'),
		valued("--prefix", 0), valued("--index-output", 0), flag("", 'v'), flag("--trivial", 0),
		flag("--aggressive", 0), flag("--empty", 0), flag("--quiet", 'q')},
	"checkout-index": {flag("--force", 'f'), flag("--all", 'a'), flag("--index", 'u'), flag("--quiet", 'q'),
		flag("--no-create", 'n'), valued("--prefix", 0), valued("--stage", 0), flag("--temp", 0), flag("--stdin", 0),
		flag("", 'z')},
	"config": {valued("--file", 'f'), valued("--blob", 0), valued("--type", 't'), valued("--default", 0),
		valued("--comment", 0), valued("--value", 0), flag("--get", 0), flag("--get-all", 0), flag("--get-regexp", 0),
		flag("--get-urlmatch", 0), flag("--unset", 0), flag("--unset-all", 0), flag("--list", 'l'),
		flag("--show-origin", 0), flag("--show-scope", 0), flag("--name-only", 0), flag("--global", 0),
		flag("--system", 0), flag("--local", 0), flag("--worktree", 0)},
}

// A setting is one option on the line, in order: its name, and whether it was negated (`--no-…`).
type setting struct {
	name    string
	value   string
	negated bool
}

// An options record is what one parser makes of a subcommand's words.
type options struct {
	settings []setting
	operands []arg // the words that are not options, in order
	dashAt   int   // the index in operands where `--` stood; -1 when none
	unknown  bool  // an option nobody can name (`-$F`, `--$mode`, one of a few texts)
	longs    []string
}

// on says whether an option is set by its last word (a later `--no-x` undoes `--x`).
func (opts options) on(name string) bool {
	found := false
	for _, one := range opts.settings {
		if one.name == name {
			found = !one.negated
		}
	}
	return found
}

func (opts options) any(names ...string) bool {
	for _, name := range names {
		if opts.on(name) {
			return true
		}
	}
	return false
}

func (opts options) value(name string) (string, bool) {
	found, ok := "", false
	for _, one := range opts.settings {
		if one.name == name && !one.negated {
			found, ok = one.value, true
		}
	}
	return found, ok
}

// beforeDash and afterDash are the operands on each side of `--`.
func (opts options) beforeDash() []arg {
	if opts.dashAt < 0 {
		return opts.operands
	}
	return opts.operands[:opts.dashAt]
}

func (opts options) afterDash() []arg {
	if opts.dashAt < 0 {
		return nil
	}
	return opts.operands[opts.dashAt:]
}

// matchLong finds the options a long word names: written out, or cut short as git takes it. An
// abbreviation that fits several is read as all of them (the dangerous one among them).
func matchLong(word string, model []option) []option {
	for _, opt := range model {
		if opt.long == word {
			return []option{opt}
		}
	}
	if len(word) < 3 {
		return nil
	}
	var found []option
	for _, opt := range model {
		if opt.long != "" && strings.HasPrefix(opt.long, word) {
			found = append(found, opt)
		}
	}
	return found
}

// parseOptions reads a subcommand's words with its model.
func parseOptions(words []arg, model []option) options {
	opts := options{dashAt: -1}
	for index := 0; index < len(words); index++ {
		one := words[index]
		if opts.dashAt >= 0 {
			opts.operands = append(opts.operands, one)
			continue
		}
		if one.unknownOption() {
			opts.unknown = true
			continue
		}
		text, known := one.single()
		if !known || !strings.HasPrefix(text, "-") || text == "-" {
			opts.operands = append(opts.operands, one)
			continue
		}
		if text == "--" {
			opts.dashAt = len(opts.operands)
			continue
		}
		if strings.HasPrefix(text, "--") {
			index = opts.long(words, index, text, model)
			continue
		}
		index = opts.short(words, index, text, model)
	}
	return opts
}

func (opts *options) long(words []arg, index int, text string, model []option) int {
	name, given, hasValue := strings.Cut(text, "=")
	opts.longs = append(opts.longs, name)
	negated := false
	matches := matchLong(name, model)
	if len(matches) == 0 && strings.HasPrefix(name, "--no-") {
		matches = matchLong("--"+name[len("--no-"):], model)
		negated = len(matches) > 0
	}
	if len(matches) == 0 {
		opts.settings = append(opts.settings, setting{name: name, value: given})
		return index
	}
	for _, opt := range matches {
		value := given
		if opt.value && !hasValue && !negated && len(matches) == 1 && index+1 < len(words) {
			value = words[index+1].value()
			index++
		}
		opts.settings = append(opts.settings, setting{name: opt.name(), value: value, negated: negated})
	}
	return index
}

func (opts *options) short(words []arg, index int, text string, model []option) int {
	letters := text[1:]
	if isNumber(letters) {
		opts.operands = append(opts.operands, words[index])
		return index
	}
	for position := 0; position < len(letters); position++ {
		letter := letters[position]
		opt := option{short: letter}
		for _, known := range model {
			if known.short == letter {
				opt = known
				break
			}
		}
		if opt.value {
			value := letters[position+1:]
			if value == "" && index+1 < len(words) {
				value = words[index+1].value()
				index++
			}
			opts.settings = append(opts.settings, setting{name: opt.name(), value: value})
			return index
		}
		opts.settings = append(opts.settings, setting{name: opt.name()})
	}
	return index
}

func isNumber(text string) bool {
	if text == "" {
		return false
	}
	for _, char := range text {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// A configEntry is one setting of git's configuration given on the line or in the environment.
type configEntry struct {
	key, value   string
	known        bool // the key and value are known text
	hasValue     bool // `-c key` without `=` is true to git
	fromVar      bool // `--config-env key=VAR`: its value is a variable's
	valueUnknown bool // the value is only known when the command runs
}

// A gitCall is `git GLOBALS SUBCOMMAND WORDS…` read with the model.
type gitCall struct {
	sub       string
	rest      []arg
	places    []*arg // each -C, in order: git applies a relative one inside the one before
	configs   []configEntry
	elsewhere bool  // --git-dir / --work-tree given
	repoWords []arg // the values of --git-dir and --work-tree
}

// readGit reads the words after `git`: its global options, then the subcommand.
func readGit(words []arg) gitCall {
	var call gitCall
	for index := 0; index < len(words); index++ {
		one := words[index]
		text, known := one.single()
		if !known {
			if one.unknownOption() || one.startsWithDash() || one.dynamic {
				continue
			}
		}
		next := func() *arg {
			if index+1 < len(words) {
				index++
				return &words[index]
			}
			return nil
		}
		switch {
		case text == "-C":
			call.places = append(call.places, next())
		case text == "-c":
			if given := next(); given != nil {
				call.configs = append(call.configs, configOf(*given, false))
			}
		case strings.HasPrefix(text, "-c") && !strings.HasPrefix(text, "--"):
			call.configs = append(call.configs, configOf(literalArg(text[2:]), false))
		case text == "--config-env":
			if given := next(); given != nil {
				call.configs = append(call.configs, configOf(*given, true))
			}
		case strings.HasPrefix(text, "--config-env="):
			call.configs = append(call.configs, configOf(literalArg(strings.TrimPrefix(text, "--config-env=")), true))
		case text == "--git-dir" || text == "--work-tree":
			call.elsewhere = true
			if given := next(); given != nil {
				call.repoWords = append(call.repoWords, *given)
			}
		case strings.HasPrefix(text, "--git-dir=") || strings.HasPrefix(text, "--work-tree="):
			call.elsewhere = true
			_, given, _ := strings.Cut(text, "=")
			call.repoWords = append(call.repoWords, literalArg(given))
		case strings.HasPrefix(text, "-"):
			for _, opt := range gitGlobals {
				if opt.long != "" && opt.long == text && opt.value {
					next()
				}
			}
		case known:
			call.sub = text
			call.rest = words[index+1:]
			return call
		}
	}
	return call
}

func configOf(given arg, fromVar bool) configEntry {
	text, known := given.single()
	if !known {
		// A key written out and a value only known when it runs (`-c "http.extraHeader=$H"`): the
		// key is what the guard reads.
		if len(given.parts) > 0 && given.parts[0].known {
			if key, _, found := strings.Cut(given.parts[0].text, "="); found && key != "" {
				return configEntry{key: key, known: true, hasValue: true, valueUnknown: true, fromVar: fromVar}
			}
		}
		return configEntry{fromVar: fromVar}
	}
	key, value, hasValue := strings.Cut(text, "=")
	return configEntry{key: key, value: value, known: true, hasValue: hasValue, fromVar: fromVar}
}
