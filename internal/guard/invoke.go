package guard

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// An invocation is one simple command as the rules see it: its words after expansion, with the
// wrappers that run another program peeled off.
type invocation struct {
	process  *shell
	here     *state
	args     []arg
	own      map[string]value // what the command itself is given: `A=1 cmd`, `env A=1 cmd`
	bodies   []body           // here-documents and here-strings it reads
	feeder   *syntax.Stmt     // the part of a pipeline that feeds it
	external bool             // run by a wrapper outside this shell: a `cd` or an assignment changes nothing here
	globbing bool             // zsh's patterns apply (not behind `noglob`)
}

// A body is the text of a here-document or here-string; known false when part of it is only known
// when the command runs.
type body struct {
	text  string
	known bool
}

func (inv *invocation) program() string {
	if len(inv.args) == 0 {
		return ""
	}
	text, ok := inv.args[0].single()
	if !ok {
		return ""
	}
	return path.Base(text)
}

// shown is the command for a refusal, as written.
func (inv *invocation) shown() string {
	parts := make([]string, 0, len(inv.args))
	for _, one := range inv.args {
		if one.opaque {
			continue
		}
		parts = append(parts, one.written)
	}
	text := strings.Join(parts, " ")
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

// texts of the args from `from` on: each its known text, else as written.
func (inv *invocation) values(from int) []string {
	found := make([]string, 0, len(inv.args))
	for _, one := range inv.args[min(from, len(inv.args)):] {
		found = append(found, one.value())
	}
	return found
}

var nameRule = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (process *shell) call(node *syntax.CallExpr, stmt *syntax.Stmt, before *state, how flow, feeder *syntax.Stmt) (*state, *state) {
	for _, assign := range node.Assigns {
		process.substitutions(assign, before)
	}
	for _, word := range node.Args {
		process.substitutions(word, before)
	}
	here := process.arithmeticInWords(node.Args, before)
	for _, assign := range node.Assigns {
		if assign.Value != nil {
			here = process.arithmeticInWords([]*syntax.Word{assign.Value}, here)
		}
	}
	if len(node.Args) == 0 {
		after := here
		for _, assign := range node.Assigns {
			if assign.Name == nil {
				continue
			}
			after = after.setVar(assign.Name.Value, process.assigned(assign, after))
		}
		return after, after
	}
	own := map[string]value{}
	for _, assign := range node.Assigns {
		if assign.Name != nil {
			own[assign.Name.Value] = process.assigned(assign, here)
		}
	}
	exp := expansion{reader: process.reader, here: here}
	inv := &invocation{process: process, here: here, args: exp.args(node.Args), own: own, bodies: process.bodies(stmt, here),
		feeder: feeder, globbing: true}
	return process.invoke(inv, how)
}

// assigned is what a variable set to an assignment's word holds.
func (process *shell) assigned(assign *syntax.Assign, here *state) value {
	if assign.Append || assign.Array != nil || assign.Index != nil {
		return unknownValue()
	}
	if assign.Value == nil {
		return certain("")
	}
	return process.valueOf(assign.Value, here)
}

func (process *shell) valueOf(word *syntax.Word, here *state) value {
	exp := expansion{reader: process.reader, here: here}
	options := exp.readings(word)
	texts := make([]string, 0, len(options))
	for _, option := range options {
		if !option.known() {
			return unknownValue()
		}
		texts = append(texts, option.text())
	}
	return oneOf(texts)
}

// bodies are the here-documents and here-strings a statement reads.
func (process *shell) bodies(stmt *syntax.Stmt, here *state) []body {
	var found []body
	for _, redirect := range stmt.Redirs {
		switch {
		case redirect.Hdoc != nil:
			if quotedDelimiter(redirect) {
				found = append(found, body{text: rawText(redirect.Hdoc), known: true})
				continue
			}
			found = append(found, process.expandedBody(redirect.Hdoc, here))
		case redirect.Op == syntax.WordHdoc && redirect.Word != nil:
			exp := expansion{reader: process.reader, here: here}
			options := exp.readings(redirect.Word)
			if len(options) == 1 && options[0].known() {
				found = append(found, body{text: options[0].text() + "\n", known: true})
			} else {
				found = append(found, body{})
			}
		}
	}
	return found
}

func rawText(word *syntax.Word) string {
	var built strings.Builder
	for _, part := range word.Parts {
		if literal, ok := part.(*syntax.Lit); ok {
			built.WriteString(literal.Value)
		}
	}
	return built.String()
}

// expandedBody is an unquoted here-document's body with what is known put in.
func (process *shell) expandedBody(word *syntax.Word, here *state) body {
	exp := expansion{reader: process.reader, here: here}
	found := []reading{{}}
	for _, part := range word.Parts {
		found = product(found, exp.part(part, true, false))
		if found == nil || len(found) > 1 {
			return body{}
		}
	}
	if !found[0].known() {
		return body{}
	}
	return body{text: found[0].text(), known: true}
}

// The wrappers the guard sees through (guard.md section 2a): which options take a value, how
// many positionals they consume, and whether they act in this shell.
type wrapper struct {
	valued      []string // options that take a value, attached (`-n5`) or as the next word
	positionals int      // words consumed before the command (`timeout`'s duration)
	inShell     bool     // the shell's own word: the command it runs acts in this shell
	runsScript  bool     // the rest of the words are a script for a shell (`watch`, `parallel`)
}

var wrappers = map[string]wrapper{
	"sudo":       {valued: []string{"-u", "-g", "-C", "-h", "-p", "-U", "-r", "-t", "-D", "-R", "-T"}},
	"doas":       {valued: []string{"-u", "-C", "-a"}},
	"command":    {inShell: true},
	"builtin":    {inShell: true},
	"exec":       {valued: []string{"-a"}, inShell: true},
	"noglob":     {inShell: true},
	"nocorrect":  {inShell: true},
	"time":       {valued: []string{"-o", "-f"}, inShell: true},
	"gtime":      {valued: []string{"-o", "-f"}},
	"nohup":      {},
	"nice":       {valued: []string{"-n"}},
	"caffeinate": {valued: []string{"-t", "-w"}},
	"taskpolicy": {valued: []string{"-c", "-d", "-g", "-t", "-l", "-s", "-p"}},
	"timeout":    {valued: []string{"-s", "-k", "--signal", "--kill-after"}, positionals: 1},
	"gtimeout":   {valued: []string{"-s", "-k", "--signal", "--kill-after"}, positionals: 1},
	"env":        {valued: []string{"-u", "-P", "-C", "--unset", "--chdir"}},
	"stdbuf":     {valued: []string{"-i", "-o", "-e"}},
	"xargs":      {valued: []string{"-I", "-n", "-P", "-L", "-s", "-E", "-J", "-R", "-S", "-d", "-a"}},
	"xcrun":      {valued: []string{"--sdk", "-sdk", "--toolchain", "-toolchain"}},
	"arch":       {valued: []string{"-arch", "-e", "-d"}},
	"script":     {valued: []string{"-t", "-T", "-E", "-I", "-O", "-B", "-o", "-m", "-c", "--command"}, positionals: 1},
	"watch":      {valued: []string{"-n", "--interval", "-d"}, runsScript: true},
	"parallel": {valued: []string{"-j", "--jobs", "-N", "-n", "-L", "-S", "--sshlogin", "-a", "--arg-file", "--delay",
		"--timeout", "--joblog", "--results", "-I", "--colsep", "-C", "-P"}, runsScript: true},
	"busybox": {},
	"toybox":  {},
}

// GNU tools under their Homebrew names are the same tools (W-017).
var gnuNames = map[string]string{"grm": "rm", "gxargs": "xargs", "gfind": "find", "gln": "ln", "genv": "env",
	"ggrep": "grep", "gtimeout": "timeout"}

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true}

var zshPath = regexp.MustCompile(`^=[A-Za-z0-9_.+-]+$`)

// invoke peels the wrappers and hands the command to what judges it.
func (process *shell) invoke(inv *invocation, how flow) (*state, *state) {
	for {
		if len(inv.args) == 0 {
			return inv.here, inv.here
		}
		head := inv.args[0]
		if !head.known() {
			process.reader.projectRules(inv)
			return inv.here, inv.here
		}
		if len(head.texts) > 1 {
			// One of a few commands (`for c in rm echo; do $c -rf src; done`): each is judged.
			process.reader.projectRules(inv)
			for _, text := range head.texts {
				each := *inv
				each.args = append(splitArgs(text), inv.args[1:]...)
				each.external = true
				process.invoke(&each, how)
			}
			return inv.here, inv.here
		}
		name := head.texts[0]
		if zshPath.MatchString(name) {
			name = name[1:] // zsh: `=rm` is the path of rm
		}
		base := path.Base(name)
		if real, ok := gnuNames[base]; ok {
			base = real
		}
		if base != head.texts[0] {
			inv.args = append([]arg{{written: head.written, texts: []string{base}, parts: head.parts}}, inv.args[1:]...)
		}
		spec, ok := wrappers[base]
		if !ok {
			break
		}
		if base == "command" && hasAnyFlag(inv.values(1), "v", "V") {
			return inv.here, inv.here // `command -v x` looks a command up and runs nothing
		}
		if base == "noglob" {
			inv.globbing = false
		}
		inv.external = inv.external || !spec.inShell
		rest, done := process.peel(inv, base, spec)
		if done {
			return inv.here, inv.here
		}
		inv.args = rest
		if spec.runsScript {
			process.runsScript(inv, base)
			return inv.here, inv.here
		}
		if base == "xargs" && len(inv.args) > 0 {
			inv.args = append(inv.args, opaqueArg("<what xargs supplies>"))
		}
	}
	if inv.globbing && inv.process.zsh && !inv.here.globsOff {
		process.reader.zshPatterns(inv)
	}
	return process.dispatch(inv, how)
}

func splitArgs(text string) []arg {
	var found []arg
	for _, piece := range strings.Fields(text) {
		found = append(found, literalArg(piece))
	}
	return found
}

// peel takes a wrapper and its options off the words; done when the wrapper itself was judged
// (env -S, script -c).
func (process *shell) peel(inv *invocation, base string, spec wrapper) ([]arg, bool) {
	words := inv.args
	index := 1
	for index < len(words) {
		text, known := words[index].single()
		if base == "env" && !known && words[index].dynamic && len(words[index].parts) > 0 && words[index].parts[0].known {
			if name, _, found := strings.Cut(words[index].parts[0].text, "="); found && nameRule.MatchString(name) {
				inv.own[name] = unknownValue()
				index++
				continue
			}
		}
		if !known {
			break
		}
		switch {
		case base == "env" && (envSplit(text) || text == "--split-string" || strings.HasPrefix(text, "--split-string=")):
			return process.envSplitString(inv, index), true
		case base == "env" && strings.Contains(text, "=") && nameRule.MatchString(strings.SplitN(text, "=", 2)[0]):
			pair := strings.SplitN(text, "=", 2)
			inv.own[pair[0]] = certain(pair[1])
			index++
			continue
		case base == "env" && words[index].dynamic && strings.Contains(words[index].written, "="):
			pair := strings.SplitN(words[index].written, "=", 2)
			if nameRule.MatchString(pair[0]) {
				inv.own[pair[0]] = unknownValue()
				index++
				continue
			}
		case base == "script" && (text == "-c" || text == "--command") && index+1 < len(words):
			process.childScript(inv, words[index+1], false, "sh")
			return nil, true
		case base == "arch" && text == "-e" && index+1 < len(words):
			if pair, ok := words[index+1].single(); ok && strings.Contains(pair, "=") {
				parts := strings.SplitN(pair, "=", 2)
				inv.own[parts[0]] = certain(parts[1])
			}
			index += 2
			continue
		}
		if !words[index].known() {
			break
		}
		if text == "--" {
			index++
			break
		}
		if slices.Contains(spec.valued, text) {
			index += 2
			continue
		}
		if strings.HasPrefix(text, "--") && strings.Contains(text, "=") {
			index++
			continue
		}
		if strings.HasPrefix(text, "-") && text != "-" {
			if takesValueInCluster(text, spec.valued) && len(text) == 2 {
				index += 2
				continue
			}
			index++
			continue
		}
		break
	}
	index += spec.positionals
	if index > len(words) {
		return nil, true
	}
	return words[index:], false
}

// envSplit says whether an option word of env holds `-S` (`-S`, `-iS…`, `-vS`): only flags that take
// no value may stand before it; in `-uSX` the S is the start of the name `-u` takes.
func envSplit(text string) bool {
	if !strings.HasPrefix(text, "-") || strings.HasPrefix(text, "--") {
		return false
	}
	for _, letter := range text[1:] {
		switch letter {
		case 'S':
			return true
		case '0', 'i', 'v':
			continue
		default:
			return false
		}
	}
	return false
}

func takesValueInCluster(text string, valued []string) bool {
	return slices.Contains(valued, text[:2])
}

// envSplitString reads `env -S STRING` in env's own split-string language and judges the command
// it makes; a string it cannot split, or one with `${…}`, is refused (env-split-string).
func (process *shell) envSplitString(inv *invocation, index int) []arg {
	text, _ := inv.args[index].single()
	given := ""
	taken := 1
	switch {
	case strings.HasPrefix(text, "--split-string="):
		given = strings.TrimPrefix(text, "--split-string=")
	case text == "--split-string":
		taken = 2
	default:
		given = text[strings.IndexByte(text, 'S')+1:]
		if given == "" {
			taken = 2
		}
	}
	if taken == 2 {
		if index+1 >= len(inv.args) {
			process.reader.refuseEnvSplit(inv)
			return nil
		}
		one, ok := inv.args[index+1].single()
		if !ok {
			process.reader.refuseEnvSplit(inv)
			return nil
		}
		given = one
	}
	pieces, ok := splitEnvString(given)
	if !ok {
		process.reader.refuseEnvSplit(inv)
		return nil
	}
	made := *inv
	made.args = append(splitPieces(pieces), inv.args[index+taken:]...)
	process.invoke(&made, flow{})
	return nil
}

func splitPieces(pieces []string) []arg {
	found := make([]arg, 0, len(pieces))
	for _, piece := range pieces {
		found = append(found, literalArg(piece))
	}
	return found
}

// splitEnvString splits a string by env's -S rules: blanks separate, `\_` is a space, single and
// double quotes group, `\c` ends the string; `${…}` (env expands it itself) cannot be read.
func splitEnvString(text string) ([]string, bool) {
	var found []string
	var current strings.Builder
	started := false
	quote := byte(0)
	for index := 0; index < len(text); index++ {
		char := text[index]
		switch {
		case quote == '\'':
			if char == '\'' {
				quote = 0
			} else {
				current.WriteByte(char)
			}
		case char == '$':
			return nil, false
		case char == '\\' && index+1 < len(text):
			index++
			switch text[index] {
			case '_':
				if quote == 0 {
					if started {
						found = append(found, current.String())
						current.Reset()
						started = false
					}
					continue
				}
				current.WriteByte(' ')
			case 'c':
				if started {
					found = append(found, current.String())
				}
				return found, quote == 0
			case 'n':
				current.WriteByte('\n')
			case 't':
				current.WriteByte('\t')
			default:
				current.WriteByte(text[index])
			}
			started = true
		case quote == '"':
			if char == '"' {
				quote = 0
			} else {
				current.WriteByte(char)
			}
		case char == '\'' || char == '"':
			quote = char
			started = true
		case char == ' ' || char == '\t' || char == '\n':
			if started {
				found = append(found, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteByte(char)
			started = true
		}
	}
	if quote != 0 {
		return nil, false
	}
	if started {
		found = append(found, current.String())
	}
	return found, true
}

// runsScript: `watch` and `parallel` hand their words to a shell; `parallel` with no command runs
// each line it reads.
func (process *shell) runsScript(inv *invocation, base string) {
	words := inv.args
	if base == "parallel" {
		for index, one := range words {
			if text, ok := one.single(); ok && (text == ":::" || text == "::::" || text == ":::+" || text == "::::+") {
				words = words[:index]
				break
			}
		}
	}
	if len(words) == 0 {
		if base == "parallel" && inv.feeder != nil {
			process.reader.scan("it is piped into `parallel`, which runs each line it reads")
		}
		return
	}
	parts := make([]string, 0, len(words)+1)
	for _, one := range words {
		parts = append(parts, one.value())
	}
	if base == "parallel" {
		parts = append(parts, `"$JACKSTAY_GUARD_WHAT_PARALLEL_SUPPLIES"`)
	}
	process.readChild(strings.Join(parts, " "), inv, false, "sh", false)
}

// dispatch hands a command, its wrappers peeled, to the shell's own words or to the rules.
func (process *shell) dispatch(inv *invocation, how flow) (*state, *state) {
	program := inv.program()
	here := inv.here
	if !inv.external {
		switch program {
		case "export", "local", "declare", "typeset", "readonly":
			after := process.declareWords(program, inv.args[1:], here)
			return after, after
		case "cd":
			return process.changeFolder(inv), here
		case "pushd", "popd":
			moved := here.clone()
			moved.places = nil
			return moved, here
		case "read", "unset", "mapfile", "readarray", "getopts":
			after := here
			for _, one := range inv.args[1:] {
				if text, ok := one.single(); ok && nameRule.MatchString(text) {
					after = after.setVar(text, unknownValue())
				}
			}
			return after, after
		case "set":
			after := process.setOptions(inv.values(1), here)
			for _, one := range inv.args[1:] {
				if !one.known() {
					after.globsOff = true // `set -- $p`: it may be `set -f`
				}
			}
			return after, after
		case "setopt", "unsetopt", "emulate":
			after := here.clone()
			after.globsOff = true
			return after, after
		case "exit":
			return nil, nil
		case "return":
			if len(process.calls) > 0 {
				frame := process.calls[len(process.calls)-1]
				frame.returns = join(frame.returns, here, false)
			}
			return nil, nil
		case "break", "continue":
			if len(process.loops) == 0 {
				return here, here
			}
			frame := process.loops[len(process.loops)-1]
			if program == "break" {
				frame.breaks = join(frame.breaks, here, false)
			} else {
				frame.continues = join(frame.continues, here, false)
			}
			return nil, nil
		case "true", ":":
			return here, nil
		case "false":
			return nil, here
		case "trap":
			after := process.trap(inv)
			return after, after
		case "eval":
			after := process.eval(inv)
			return after, after
		case "source", ".":
			after := process.source(inv)
			return after, after
		case "let":
			return here, here
		}
		if program == "printf" && len(inv.args) > 2 {
			if flag, ok := inv.args[1].single(); ok && flag == "-v" {
				if name, ok := inv.args[2].single(); ok {
					after := here.setVar(name, unknownValue())
					return after, after
				}
			}
		}
	}
	process.reader.projectRules(inv)
	if body, ok := process.functions[program]; ok && !inv.external {
		return process.callFunction(program, body, here, how)
	}
	switch {
	case program == "git":
		process.reader.git(inv, inv.args[1:])
	case program == "git-filter-repo":
		process.reader.git(inv, append([]arg{literalArg("filter-repo")}, inv.args[1:]...))
	case program == "rm":
		process.reader.rm(inv)
	case program == "ln":
		after := process.reader.link(inv)
		return after, after
	case program == "find":
		process.reader.find(inv)
	case shells[program]:
		process.shellProgram(inv, program)
	}
	return here, here
}

func hasAnyFlag(values []string, letters ...string) bool {
	for _, text := range values {
		if !strings.HasPrefix(text, "-") || strings.HasPrefix(text, "--") {
			continue
		}
		for _, letter := range letters {
			if strings.Contains(text[1:], letter) {
				return true
			}
		}
	}
	return false
}

// changeFolder is where a `cd` leads when it succeeds.
func (process *shell) changeFolder(inv *invocation) *state {
	moved := inv.here.clone()
	var target *arg
	for index := 1; index < len(inv.args); index++ {
		one := inv.args[index]
		text, ok := one.single()
		if ok && text == "--" {
			continue
		}
		if ok && strings.HasPrefix(text, "-") && text != "-" {
			if strings.Contains(text, "P") {
				// `cd -P` follows links before a `..`: where it lands is not read (W-053).
				moved.places = nil
				return moved
			}
			continue
		}
		target = &inv.args[index]
		break
	}
	if target == nil {
		moved.places = []string{process.reader.home}
		return moved
	}
	text, ok := target.single()
	if !ok || text == "-" || (strings.HasPrefix(text, "~") && text != "~" && !strings.HasPrefix(text, "~/")) {
		moved.places = nil
		return moved
	}
	if path.IsAbs(text) {
		moved.places = []string{flat(text)}
		return moved
	}
	if moved.places == nil {
		return moved
	}
	next := make([]string, 0, len(moved.places))
	for _, folder := range moved.places {
		next = append(next, flat(path.Join(folder, text)))
	}
	moved.places = placesOf(next)
	return moved
}

// setOptions reads `set`: errexit in a shell of its own; `-f`, `-o …` change how zsh globs.
func (process *shell) setOptions(values []string, here *state) *state {
	after := here.clone()
	for index, text := range values {
		if (text == "-o" || text == "+o") && index+1 < len(values) {
			after.globsOff = true
			if values[index+1] == "errexit" {
				after.errexit = text == "-o" && process.real
			}
			continue
		}
		if len(text) > 1 && (text[0] == '-' || text[0] == '+') {
			letters := text[1:]
			if strings.ContainsAny(letters, "fF") || letters == "o" {
				after.globsOff = true
			}
			if strings.Contains(letters, "e") {
				after.errexit = text[0] == '-' && process.real
			}
		}
	}
	return after
}

// declare reads `export`, `local`, `declare`, `typeset`, `readonly` as the parser gives them.
func (process *shell) declare(node *syntax.DeclClause, before *state) *state {
	process.substitutions(node, before)
	words := make([]arg, 0, len(node.Args))
	exp := expansion{reader: process.reader, here: before}
	for _, assign := range node.Args {
		switch {
		case assign.Name == nil && assign.Value != nil:
			words = append(words, exp.argsOf(assign.Value)...)
		case assign.Name != nil && assign.Naked:
			words = append(words, literalArg(assign.Name.Value))
		case assign.Name != nil:
			held := process.assigned(assign, before)
			one := arg{written: assign.Name.Value + "=", texts: []string{assign.Name.Value + "="}}
			if held.known() && len(held.texts) == 1 {
				one.texts = []string{assign.Name.Value + "=" + held.texts[0]}
			} else {
				one.texts = nil
				one.dynamic = true
				one.parts = []segment{{text: assign.Name.Value + "=", known: true}, {}}
			}
			words = append(words, one)
		}
	}
	return process.declareWords(node.Variant.Value, words, before)
}

func (process *shell) declareWords(variant string, words []arg, before *state) *state {
	after := before.clone()
	flags := ""
	for _, one := range words {
		if text, ok := one.single(); ok && (strings.HasPrefix(text, "-") || strings.HasPrefix(text, "+")) {
			flags += text[1:]
		}
	}
	exporting := variant == "export" || (variant != "local" && strings.Contains(flags, "x"))
	scoped := (variant == "local" || variant == "declare" || variant == "typeset") && !strings.Contains(flags, "g")
	for _, one := range words {
		name, held, hasValue := declared(one)
		if name == "" {
			continue
		}
		if scoped && len(process.calls) > 0 {
			frame := process.calls[len(process.calls)-1]
			if _, saved := frame.saved[name]; !saved {
				if old, ok := after.vars[name]; ok {
					frame.saved[name] = &old
				} else {
					frame.saved[name] = nil
				}
			}
		}
		switch {
		case hasValue:
			after.vars[name] = held
			if exporting {
				after.exported[name] = held
				after.sure[name] = true
			}
		case exporting:
			switch {
			case after.vars[name].known() || hasKey(after.vars, name):
				after.exported[name] = after.vars[name]
			case after.blind:
				after.exported[name] = unknownValue()
			default:
				after.exported[name] = certain(process.reader.environ[name])
			}
			after.sure[name] = true
		case scoped:
			after.vars[name] = unknownValue()
		}
	}
	return after
}

func hasKey(vars map[string]value, name string) bool {
	_, ok := vars[name]
	return ok
}

// declared reads one word of `export`/`local`…: a name, and the value given it.
func declared(one arg) (string, value, bool) {
	if text, ok := one.single(); ok {
		if strings.HasPrefix(text, "-") || strings.HasPrefix(text, "+") {
			return "", value{}, false
		}
		if name, given, found := strings.Cut(text, "="); found {
			if nameRule.MatchString(name) {
				return name, certain(given), true
			}
			return "", value{}, false
		}
		if nameRule.MatchString(text) {
			return text, value{}, false
		}
		return "", value{}, false
	}
	if one.known() {
		if name, _, found := strings.Cut(one.texts[0], "="); found && nameRule.MatchString(name) {
			texts := make([]string, 0, len(one.texts))
			for _, text := range one.texts {
				_, given, _ := strings.Cut(text, "=")
				texts = append(texts, given)
			}
			return name, oneOf(texts), true
		}
		return "", value{}, false
	}
	if len(one.parts) > 0 && one.parts[0].known {
		if name, _, found := strings.Cut(one.parts[0].text, "="); found && nameRule.MatchString(name) {
			return name, unknownValue(), true
		}
	}
	return "", value{}, false
}

// callFunction reads a function where it is called, with the state the call finds.
func (process *shell) callFunction(name string, body *syntax.Stmt, here *state, how flow) (*state, *state) {
	process.called[name] = true
	if len(process.calls) >= maxDepth {
		process.reader.warn(warnTooDeep, "the function "+name+" calls deeper than the guard reads; from there on nothing is taken as known")
		forgotten := here.forget()
		return forgotten, forgotten
	}
	frame := &callFrame{saved: map[string]*value{}}
	process.calls = append(process.calls, frame)
	savedLoops := process.loops
	process.loops = nil
	succeeded, failed := process.stmt(body, here, how)
	process.loops = savedLoops
	process.calls = process.calls[:len(process.calls)-1]
	end := join(join(succeeded, failed, false), frame.returns, false)
	if end == nil {
		return nil, nil
	}
	restored := end.clone()
	for name, old := range frame.saved {
		if old == nil {
			delete(restored.vars, name)
		} else {
			restored.vars[name] = *old
		}
	}
	return restored, restored
}

// trap registers a script that runs later, at any point after it: judged at the end with every
// state it may find; what it writes is unknown from here on.
func (process *shell) trap(inv *invocation) *state {
	if len(inv.args) < 3 {
		return inv.here
	}
	text, ok := inv.args[1].single()
	if !ok || text == "" || text == "-" || strings.HasPrefix(text, "-") {
		return inv.here
	}
	file := process.reader.parse(text)
	if file == nil {
		return inv.here
	}
	process.reader.enter(process.depth + 1)
	probe := &shell{reader: process.reader, zsh: process.zsh, real: process.real, depth: process.depth + 1, functions: process.functions,
		called: process.called}
	succeeded, failed := probe.list(file.Stmts, inv.here, flow{})
	after := unknownWrites(inv.here, join(join(succeeded, failed, false), inv.here, false))
	process.traps = append(process.traps, &trapNote{text: text, states: inv.here})
	return after
}

// soleSubstitution is the script of a word that is one substitution and nothing else: `$(…)`,
// a backtick pair, `<(…)`, quoted or not.
func soleSubstitution(one arg) []*syntax.Stmt {
	if one.word == nil || len(one.word.Parts) != 1 {
		return nil
	}
	part := one.word.Parts[0]
	if quoted, ok := part.(*syntax.DblQuoted); ok && len(quoted.Parts) == 1 {
		part = quoted.Parts[0]
	}
	switch node := part.(type) {
	case *syntax.CmdSubst:
		return node.Stmts
	case *syntax.ProcSubst:
		return node.Stmts
	}
	return nil
}

// eval runs its text in this shell: what it changes stays. Text that cannot be read may change
// anything, so after it nothing is known.
func (process *shell) eval(inv *invocation) *state {
	words := inv.args[1:]
	if len(words) == 1 {
		if stmts := soleSubstitution(words[0]); stmts != nil {
			return process.readProduced(stmts, inv.here, "`eval` runs what it writes")
		}
	}
	texts := make([]string, 0, len(words))
	for _, one := range words {
		text, ok := one.single()
		if !ok {
			return inv.here.forget()
		}
		texts = append(texts, text)
	}
	succeeded, failed := process.text(strings.Join(texts, " "), inv.here, process.real)
	after := join(succeeded, failed, false)
	if after == nil {
		return inv.here
	}
	return after
}

// readProduced reads in this shell what a producer writes (`eval "$(echo …)"`, `source <(…)`):
// each text it may write, joined; or, when it cannot be modelled, the fallback scan, and nothing
// known after.
func (process *shell) readProduced(stmts []*syntax.Stmt, here *state, why string) *state {
	var texts []string
	if len(stmts) == 1 {
		texts = process.reader.produced(stmts[0], here)
	}
	if texts == nil {
		process.reader.scan(why + ", and that could not be read as the script it makes")
		return here.forget()
	}
	var after *state
	for _, text := range texts {
		succeeded, failed := process.text(text, here, process.real)
		after = join(after, join(succeeded, failed, false), false)
	}
	if after == nil {
		return here
	}
	return after
}

// source reads a file the guard does not read: after it nothing is known. `source <(…)` of a
// producer that can be modelled is read in this shell.
func (process *shell) source(inv *invocation) *state {
	var given []arg
	for _, one := range inv.args[1:] {
		if one.startsWithDash() {
			continue
		}
		given = append(given, one)
	}
	if len(given) > 0 {
		if stmts := soleSubstitution(given[0]); stmts != nil {
			return process.readProduced(stmts, inv.here, "it is sourced")
		}
	}
	if len(inv.bodies) > 0 && len(given) == 0 {
		after := inv.here
		for _, fed := range inv.bodies {
			if !fed.known {
				return inv.here.forget()
			}
			succeeded, failed := process.text(fed.text, after, process.real)
			after = join(succeeded, failed, false)
		}
		return after
	}
	return inv.here.forget()
}

// shellProgram reads what a shell program runs: `bash -c TEXT`, a script fed to it, `bash <(…)`.
func (process *shell) shellProgram(inv *invocation, program string) {
	strict, readsInput := false, false
	var script *arg
	var named []arg
	words := inv.args[1:]
	for index := 0; index < len(words); index++ {
		one := words[index]
		text, ok := one.single()
		if ok && strings.HasPrefix(text, "-") && !strings.HasPrefix(text, "--") && len(text) > 1 {
			letters := text[1:]
			readsInput = readsInput || strings.Contains(letters, "s")
			strict = strict || strings.Contains(letters, "e")
			if letters == "o" && index+1 < len(words) {
				if option, ok := words[index+1].single(); ok && option == "errexit" {
					strict = true
				}
				index++
				continue
			}
			if strings.Contains(letters, "c") {
				for rest := index + 1; rest < len(words); rest++ {
					if dash, ok := words[rest].single(); ok && dash == "--" {
						continue
					}
					script = &words[rest]
					break
				}
				break
			}
			continue
		}
		if ok && strings.HasPrefix(text, "--") {
			continue
		}
		named = append(named, one)
	}
	switch {
	case script != nil:
		if stmts := soleSubstitution(*script); stmts != nil {
			process.producedToChild(stmts, inv, strict, program)
			return
		}
		process.childScript(inv, *script, strict, program)
	case len(named) > 0 && soleSubstitution(named[0]) != nil:
		process.producedToChild(soleSubstitution(named[0]), inv, strict, program)
	case readsInput || len(named) == 0:
		for _, fed := range inv.bodies {
			if !fed.known {
				process.reader.scan("a here-document fed to a shell holds a part only known when it runs")
				continue
			}
			process.readChild(fed.text, inv, strict, program, true)
		}
		if inv.feeder != nil {
			texts := process.reader.produced(inv.feeder, inv.here)
			if texts == nil {
				process.reader.scan("it is piped into a shell, and what is piped could not be read as the script it makes")
				return
			}
			for _, text := range texts {
				process.readChild(text, inv, strict, program, true)
			}
		}
	}
}

// producedToChild reads what a producer writes as the script of a new shell (`sh -c "$(printf …)"`,
// `bash <(echo …)`).
func (process *shell) producedToChild(stmts []*syntax.Stmt, inv *invocation, strict bool, program string) {
	var texts []string
	if len(stmts) == 1 {
		texts = process.reader.produced(stmts[0], inv.here)
	}
	if texts == nil {
		process.reader.scan("a shell runs what it writes, and that could not be read as the script it makes")
		return
	}
	for _, text := range texts {
		process.readChild(text, inv, strict, program, true)
	}
}

// childScript reads the script word of `bash -c`, `script -c`: each text it may be, or the
// fallback scan when part of it is only known when it runs.
func (process *shell) childScript(inv *invocation, script arg, strict bool, program string) {
	if !script.known() {
		process.reader.scan("a shell runs a script part of which is only known when it runs")
		return
	}
	for _, text := range script.texts {
		process.readChild(text, inv, strict, program, true)
	}
}

// readChild reads a script in a new shell process: it starts with what was exported on every path
// and the command's own assignments; nothing it does comes back.
func (process *shell) readChild(text string, inv *invocation, strict bool, program string, real bool) {
	file := process.reader.parse(text)
	if file == nil {
		return
	}
	process.reader.enter(process.depth + 1)
	child := process.child(program == "zsh", real)
	start := inv.here.clone()
	for name := range start.vars {
		if !start.sure[name] {
			start.vars[name] = unknownValue()
		}
	}
	for name, held := range inv.own {
		start.vars[name] = held
	}
	start.errexit = strict && real
	start.globsOff = false
	child.script(file, start)
}

// produced is what a command writes to a pipe or a substitution when that can be modelled: `echo`
// (with its escapes read and not: shells differ), `printf` (its format applied), `cat` of a
// here-document. nil when it cannot be.
func (reader *reader) produced(stmt *syntax.Stmt, here *state) []string {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Negated || len(call.Args) == 0 || len(call.Assigns) > 0 {
		return nil
	}
	exp := expansion{reader: reader, here: here}
	words := exp.args(call.Args)
	values := make([]string, 0, len(words))
	for _, one := range words {
		text, known := one.single()
		if !known {
			return nil
		}
		values = append(values, text)
	}
	switch path.Base(values[0]) {
	case "echo":
		rest := values[1:]
		for len(rest) > 0 && regexp.MustCompile(`^-[neE]+$`).MatchString(rest[0]) {
			rest = rest[1:]
		}
		plain := strings.Join(rest, " ") + "\n"
		read := ansiC(plain)
		if read == plain {
			return []string{plain}
		}
		return []string{read, plain}
	case "printf":
		if len(values) > 1 && values[1] == "-v" {
			return nil
		}
		rest := values[1:]
		if len(rest) > 0 && rest[0] == "--" {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return nil
		}
		return []string{printed(rest[0], rest[1:])}
	case "cat":
		for _, text := range values[1:] {
			if !strings.HasPrefix(text, "-") {
				return nil
			}
		}
		process := &shell{reader: reader}
		fed := process.bodies(stmt, here)
		if len(fed) == 0 {
			return nil
		}
		texts := make([]string, 0, len(fed))
		for _, one := range fed {
			if !one.known {
				return nil
			}
			texts = append(texts, one.text)
		}
		return texts
	}
	return nil
}

var printfDirective = regexp.MustCompile(`%(%|[-+ #0]*(?:\*|\d+)?(?:\.(?:\*|\d+))?([a-zA-Z]))|\\(?:x[0-9A-Fa-f]{1,2}|[0-7]{1,3}|.)`)

// printed is what `printf FORMAT ARGS…` writes: the format's escapes and directives, used again
// while arguments are left.
func printed(format string, rest []string) string {
	var out strings.Builder
	used := 0
	for {
		start := used
		out.WriteString(printfDirective.ReplaceAllStringFunc(format, func(found string) string {
			if strings.HasPrefix(found, "\\") {
				return ansiC(found)
			}
			if found == "%%" {
				return "%"
			}
			given := ""
			if used < len(rest) {
				given = rest[used]
			}
			used++
			if strings.HasSuffix(found, "b") {
				return ansiC(given)
			}
			return given
		}))
		if used >= len(rest) || used == start {
			return out.String()
		}
	}
}
