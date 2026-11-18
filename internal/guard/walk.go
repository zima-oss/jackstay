package guard

import (
	"regexp"
	"slices"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

// A shell is one shell process being read: the tool's own line, or a child (`bash -c`, a script
// fed to `sh`). What it knows of functions, loops and traps is its own.
type shell struct {
	reader    *reader
	zsh       bool // zsh's rules for patterns apply to what this shell runs
	real      bool // a shell process of its own: there `set -e` does stop at a failed command
	depth     int  // scripts inside scripts
	functions map[string]*syntax.Stmt
	called    map[string]bool
	loops     []*loopFrame
	calls     []*callFrame
	traps     []*trapNote
	lastSeen  *state // the last state a statement was reached in: what code nothing reaches is read with
}

type loopFrame struct {
	breaks, continues *state
}

type callFrame struct {
	returns *state
	saved   map[string]*value // the caller's values of the function's locals (nil: was not set)
}

// A trapNote is a `trap TEXT` the line registered: TEXT runs when the signal comes, at any point
// after it, so it is judged with the join of every state from the trap to the end of the line.
type trapNote struct {
	text   string
	states *state
}

// flow says whether `set -e` may end the script at a failing command here: not inside a
// condition, an `&&`/`||` list, a negation or a pipeline.
type flow struct {
	certain bool
}

func (process *shell) child(zsh, real bool) *shell {
	// A new shell process knows none of this one's functions (bash does not pass them on unless
	// exported, zsh never): what it calls by such a name is another program.
	return &shell{reader: process.reader, zsh: zsh, real: real, depth: process.depth + 1,
		functions: map[string]*syntax.Stmt{}, called: map[string]bool{}}
}

// script reads a whole script in this shell from `in`: what it changes stays (the line, `eval`,
// `source`). Traps and functions never called are read at its end.
func (process *shell) script(file *syntax.File, before *state) (*state, *state) {
	succeeded, failed := process.list(file.Stmts, before, flow{certain: true})
	process.finish()
	return succeeded, failed
}

// finish reads the traps with every state they may fire in, and the functions that were defined
// and never called by a name the guard saw (they may be called some way it cannot see).
func (process *shell) finish() {
	for len(process.traps) > 0 {
		note := process.traps[0]
		process.traps = process.traps[1:]
		process.text(note.text, note.states, process.real)
	}
	for name, body := range process.functions {
		if process.called[name] {
			continue
		}
		process.called[name] = true
		blind := startState("/").forget()
		process.stmt(body, blind, flow{})
	}
}

// text parses and reads a script in this shell (a trap, an `eval` of known text).
func (process *shell) text(script string, before *state, real bool) (*state, *state) {
	file := process.reader.parse(script)
	if file == nil {
		return before, before
	}
	saved := process.real
	process.real = real
	defer func() { process.real = saved }()
	process.reader.enter(process.depth + 1)
	defer process.reader.leave()
	return process.list(file.Stmts, before, flow{certain: true})
}

// note records a state the line passes through, for the traps registered before it.
func (process *shell) note(here *state) {
	for _, trap := range process.traps {
		trap.states = join(trap.states, here, false)
	}
}

func (process *shell) list(stmts []*syntax.Stmt, before *state, how flow) (*state, *state) {
	current := before
	succeeded, failed := before, before
	for index, stmt := range stmts {
		succeeded, failed = process.stmt(stmt, current, how)
		if index == len(stmts)-1 {
			break
		}
		if current != nil && current.errexit && how.certain && !stmt.Negated && !stmt.Background {
			current = succeeded // `set -e` in a shell of its own: a failure ends the script
		} else {
			current = join(succeeded, failed, false)
		}
		process.note(current)
	}
	return succeeded, failed
}

// stmt reads one statement from `in` and gives the states it leaves when it succeeds and when it
// fails. A nil state is one nothing reaches; a statement nothing reaches is still read, with
// nothing known, so that what it would destroy is still seen.
func (process *shell) stmt(stmt *syntax.Stmt, before *state, how flow) (*state, *state) {
	reached := before != nil
	switch {
	case reached:
		process.lastSeen = before
	case process.lastSeen != nil:
		before = process.lastSeen
	default:
		before = startState("/").forget()
	}
	inner := how
	if stmt.Negated || stmt.Background || stmt.Coprocess || stmt.Disown {
		inner.certain = false
	}
	if stmt.Background || stmt.Coprocess || stmt.Disown {
		process.command(stmt, before, inner)
		if !reached {
			return nil, nil
		}
		return before, before
	}
	succeeded, failed := process.command(stmt, before, inner)
	if stmt.Negated {
		succeeded, failed = failed, succeeded
	}
	if !reached {
		return nil, nil
	}
	return succeeded, failed
}

func (process *shell) command(stmt *syntax.Stmt, before *state, how flow) (*state, *state) {
	process.reader.tick()
	for _, redirect := range stmt.Redirs {
		if redirect.Word != nil {
			process.substitutions(redirect.Word, before)
		}
		if redirect.Hdoc != nil && !quotedDelimiter(redirect) {
			process.substitutions(redirect.Hdoc, before)
		}
	}
	switch node := stmt.Cmd.(type) {
	case nil:
		return before, before
	case *syntax.CallExpr:
		return process.call(node, stmt, before, how, nil)
	case *syntax.BinaryCmd:
		return process.binary(stmt, node, before, how)
	case *syntax.Subshell:
		process.list(node.Stmts, before, how)
		return before, before
	case *syntax.Block:
		return process.list(node.Stmts, before, how)
	case *syntax.IfClause:
		return process.ifClause(node, before, how)
	case *syntax.WhileClause:
		return process.whileClause(node, before, how)
	case *syntax.ForClause:
		return process.forClause(node, before, how)
	case *syntax.CaseClause:
		return process.caseClause(node, before, how)
	case *syntax.FuncDecl:
		if rest, ok := node.Body.Cmd.(*syntax.BinaryCmd); ok && !node.Body.Negated {
			// The parser takes `f() { …; } && f a` as a body of `{ …; } && f a`; bash and zsh
			// define f and then run `f a`. Read it as they do.
			return process.command(definitionFirst(node, rest), before, how)
		}
		names := node.Names
		if node.Name != nil {
			names = append(names, node.Name)
		}
		for _, name := range names {
			process.functions[name.Value] = node.Body
			delete(process.called, name.Value)
		}
		return before, before
	case *syntax.ArithmCmd:
		after := process.arithmetic(node, before)
		return after, after
	case *syntax.TestClause:
		process.substitutions(node, before)
		return before, before
	case *syntax.DeclClause:
		after := process.declare(node, before)
		return after, after
	case *syntax.LetClause:
		after := process.arithmetic(node, before)
		return after, after
	case *syntax.TimeClause:
		if node.Stmt == nil {
			return before, before
		}
		return process.stmt(node.Stmt, before, how)
	case *syntax.CoprocClause:
		process.stmt(node.Stmt, before, flow{})
		return before, before
	default:
		process.substitutions(node, before)
		return before, before
	}
}

// definitionFirst rebuilds `f() BODY OP REST…` as `(f() BODY) OP REST…`: the leftmost operand of
// the list is the function's body.
func definitionFirst(node *syntax.FuncDecl, list *syntax.BinaryCmd) *syntax.Stmt {
	left := list.X
	if inner, ok := left.Cmd.(*syntax.BinaryCmd); ok && !left.Negated {
		left = definitionFirst(node, inner)
	} else {
		decl := *node
		decl.Body = left
		left = &syntax.Stmt{Cmd: &decl, Position: left.Position}
	}
	rebuilt := *list
	rebuilt.X = left
	return &syntax.Stmt{Cmd: &rebuilt, Position: left.Position}
}

func quotedDelimiter(redirect *syntax.Redirect) bool {
	if redirect.Word == nil {
		return false
	}
	for _, part := range redirect.Word.Parts {
		switch node := part.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			return true
		case *syntax.Lit:
			for index := range len(node.Value) {
				if node.Value[index] == '\\' {
					return true
				}
			}
		}
	}
	return false
}

func (process *shell) binary(stmt *syntax.Stmt, node *syntax.BinaryCmd, before *state, how flow) (*state, *state) {
	switch node.Op {
	case syntax.AndStmt:
		leftOK, leftFailed := process.stmt(node.X, before, flow{})
		rightOK, rightFailed := process.stmt(node.Y, leftOK, flow{})
		return rightOK, join(leftFailed, rightFailed, false)
	case syntax.OrStmt:
		leftOK, leftFailed := process.stmt(node.X, before, flow{})
		rightOK, rightFailed := process.stmt(node.Y, leftFailed, flow{})
		return join(leftOK, rightOK, false), rightFailed
	default:
		parts := append(pipeline(node.X), pipeline(node.Y)...)
		for index, part := range parts[:len(parts)-1] {
			if call, ok := part.Cmd.(*syntax.CallExpr); ok && index > 0 && !part.Negated {
				process.call(call, part, before, flow{}, parts[index-1])
				continue
			}
			process.stmt(part, before, flow{})
		}
		last := parts[len(parts)-1]
		var lastOK, lastFailed *state
		if call, ok := last.Cmd.(*syntax.CallExpr); ok && !last.Negated {
			lastOK, lastFailed = process.call(call, last, before, flow{}, parts[len(parts)-2])
		} else {
			lastOK, lastFailed = process.stmt(last, before, flow{})
		}
		// The last part runs in this shell in zsh and in a shell of its own in bash: either.
		both := join(before, join(lastOK, lastFailed, false), false)
		return both, both
	}
}

// pipeline is the parts of `a | b | c` in order.
func pipeline(stmt *syntax.Stmt) []*syntax.Stmt {
	node, ok := stmt.Cmd.(*syntax.BinaryCmd)
	if !ok || (node.Op != syntax.Pipe && node.Op != syntax.PipeAll) || stmt.Negated || len(stmt.Redirs) > 0 {
		return []*syntax.Stmt{stmt}
	}
	return append(pipeline(node.X), pipeline(node.Y)...)
}

func (process *shell) ifClause(node *syntax.IfClause, before *state, how flow) (*state, *state) {
	condOK, condFailed := process.list(node.Cond, before, flow{})
	thenOK, thenFailed := process.list(node.Then, condOK, how)
	switch {
	case node.Else == nil:
		return join(thenOK, condFailed, false), thenFailed
	case len(node.Else.Cond) > 0:
		elseOK, elseFailed := process.ifClause(node.Else, condFailed, how)
		return join(thenOK, elseOK, false), join(thenFailed, elseFailed, false)
	default:
		elseOK, elseFailed := process.list(node.Else.Then, condFailed, how)
		return join(thenOK, elseOK, false), join(thenFailed, elseFailed, false)
	}
}

// loop reads a loop's body again until what it leaves settles (section 3): from the state at its
// top, then from the join of that and the body's end. After loopPasses rounds, what the body
// writes is unknown, and the body is read once more from that.
func (process *shell) loop(before *state, round func(*state) (body, leave *state)) *state {
	entry := before
	var leaving *state
	for pass := 0; ; pass++ {
		frame := &loopFrame{}
		process.loops = append(process.loops, frame)
		body, leave := round(entry)
		process.loops = process.loops[:len(process.loops)-1]
		leaving = join(leaving, join(leave, frame.breaks, false), false)
		again := join(entry, join(body, frame.continues, false), true)
		if again != nil && entry != nil && pass >= 1 && !sameStrings(again.places, entry.places) {
			again = again.clone()
			again.places = nil // a folder that moves each time round
		}
		if sameState(again, entry) {
			return leaving
		}
		if pass >= loopPasses {
			widened := unknownWrites(before, again)
			frame = &loopFrame{}
			process.loops = append(process.loops, frame)
			body, leave = round(widened)
			process.loops = process.loops[:len(process.loops)-1]
			return unknownWrites(before, joinAll(leaving, leave, frame.breaks, widened, body))
		}
		entry = again
	}
}

func sameStrings(first, second []string) bool {
	if (first == nil) != (second == nil) || len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func (process *shell) whileClause(node *syntax.WhileClause, before *state, how flow) (*state, *state) {
	after := process.loop(before, func(entry *state) (*state, *state) {
		condOK, condFailed := process.list(node.Cond, entry, flow{})
		if node.Until {
			condOK, condFailed = condFailed, condOK
		}
		bodyOK, bodyFailed := process.list(node.Do, condOK, how)
		return join(bodyOK, bodyFailed, false), condFailed
	})
	return after, after
}

func (process *shell) forClause(node *syntax.ForClause, before *state, how flow) (*state, *state) {
	start := before
	switch loop := node.Loop.(type) {
	case *syntax.WordIter:
		held := unknownValue()
		if !node.Select && loop.InPos.IsValid() {
			held = process.listed(loop.Items, before)
		}
		for _, item := range loop.Items {
			process.substitutions(item, before)
		}
		start = before.setVar(loop.Name.Value, held)
	case *syntax.CStyleLoop:
		start = process.arithmetic(loop, before)
	}
	after := process.loop(start, func(entry *state) (*state, *state) {
		bodyOK, bodyFailed := process.list(node.Do, entry, how)
		return join(bodyOK, bodyFailed, false), entry
	})
	return after, after
}

// listed is what the variable of `for NAME in WORDS` holds: each word in turn, when every word can
// be named, none is a brace list, and there are few.
func (process *shell) listed(items []*syntax.Word, here *state) value {
	exp := expansion{reader: process.reader, here: here}
	var found []string
	for _, item := range items {
		if hasBraces(item) {
			return unknownValue()
		}
		for _, one := range exp.argsOf(item) {
			if !one.known() {
				return unknownValue()
			}
			found = append(found, one.texts...)
		}
		if len(found) > maxForWords {
			return unknownValue()
		}
	}
	if len(found) == 0 {
		return unknownValue()
	}
	sorted := slices.Clone(found)
	slices.Sort(sorted)
	return value{texts: slices.Compact(sorted)}
}

func (process *shell) caseClause(node *syntax.CaseClause, before *state, how flow) (*state, *state) {
	process.substitutions(node.Word, before)
	var ends, falling *state
	takesAll := false
	for _, item := range node.Items {
		for _, pattern := range item.Patterns {
			process.substitutions(pattern, before)
			if literal, ok := pattern.Parts[0].(*syntax.Lit); ok && len(pattern.Parts) == 1 && literal.Value == "*" {
				takesAll = true
			}
		}
		start := join(before, falling, false)
		bodyOK, bodyFailed := process.list(item.Stmts, start, how)
		end := join(bodyOK, bodyFailed, false)
		falling = nil
		if item.Op == syntax.Fallthrough || item.Op == syntax.Resume || item.Op == syntax.ResumeKorn {
			falling = end
		}
		ends = join(ends, end, false)
	}
	if !takesAll {
		ends = join(ends, before, false)
	}
	return ends, ends
}

// substitutions reads the scripts inside a node that a shell runs before the command: `$(…)`,
// backticks, `<(…)`. Each is a shell of its own: nothing it does comes back. Arithmetic in them
// is read by the caller.
func (process *shell) substitutions(node syntax.Node, before *state) {
	if node == nil {
		return
	}
	syntax.Walk(node, func(inner syntax.Node) bool {
		switch found := inner.(type) {
		case *syntax.CmdSubst:
			process.subshell(found.Stmts, before)
			return false
		case *syntax.ProcSubst:
			process.subshell(found.Stmts, before)
			return false
		}
		return true
	})
}

func (process *shell) subshell(stmts []*syntax.Stmt, before *state) {
	process.reader.enter(process.depth + 1)
	defer process.reader.leave()
	inner := &shell{reader: process.reader, zsh: process.zsh, real: false, depth: process.depth + 1,
		functions: process.functions, called: process.called}
	plain := before
	if plain != nil && plain.errexit {
		plain = plain.clone()
		plain.errexit = false
	}
	inner.list(stmts, plain, flow{})
	inner.traps = nil
}

// arithmetic makes unknown what arithmetic assigns (`(( D=1 ))`, `$(( D++ ))`, `let D=1`).
func (process *shell) arithmetic(node syntax.Node, before *state) *state {
	after := before
	if let, ok := node.(*syntax.LetClause); ok {
		for _, expr := range let.Exprs {
			word, isWord := expr.(*syntax.Word)
			if !isWord || word.Lit() != "" {
				continue
			}
			text, known := literalWord(word)
			if !known {
				continue
			}
			for _, match := range assignedInText.FindAllStringSubmatch(text, -1) {
				after = after.setVar(match[1]+match[2]+match[3], unknownValue())
			}
		}
	}
	syntax.Walk(node, func(inner syntax.Node) bool {
		switch found := inner.(type) {
		case *syntax.BinaryArithm:
			if assigning[found.Op] {
				if name := arithmName(found.X); name != "" {
					after = after.setVar(name, unknownValue())
				}
			}
		case *syntax.UnaryArithm:
			if found.Op == syntax.Inc || found.Op == syntax.Dec {
				if name := arithmName(found.X); name != "" {
					after = after.setVar(name, unknownValue())
				}
			}
		case *syntax.CmdSubst, *syntax.ProcSubst:
			process.substitutions(found, before)
			return false
		}
		return true
	})
	return after
}

var assigning = map[syntax.BinAritOperator]bool{syntax.Assgn: true, syntax.AddAssgn: true, syntax.SubAssgn: true,
	syntax.MulAssgn: true, syntax.QuoAssgn: true, syntax.RemAssgn: true, syntax.AndAssgn: true, syntax.OrAssgn: true,
	syntax.XorAssgn: true, syntax.ShlAssgn: true, syntax.ShrAssgn: true, syntax.AndBoolAssgn: true,
	syntax.OrBoolAssgn: true, syntax.XorBoolAssgn: true, syntax.PowAssgn: true}

// assignedInText finds what a quoted `let` expression assigns (`let 'D = 1'`).
var assignedInText = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*(?:<<|>>|\*\*|&&|\|\||\^\^|[-+*/%&|^])?=(?:[^=]|$)|(?:\+\+|--)\s*([A-Za-z_][A-Za-z0-9_]*)|([A-Za-z_][A-Za-z0-9_]*)\s*(?:\+\+|--)`)

func arithmName(node syntax.ArithmExpr) string {
	word, ok := node.(*syntax.Word)
	if !ok {
		return ""
	}
	if literal := word.Lit(); literal != "" {
		return literal
	}
	if len(word.Parts) == 1 {
		if param, ok := word.Parts[0].(*syntax.ParamExp); ok && param.Param != nil {
			return param.Param.Value
		}
	}
	return ""
}

// arithmeticInWords applies what `$(( … ))` inside the words of a command assigns.
func (process *shell) arithmeticInWords(words []*syntax.Word, before *state) *state {
	after := before
	for _, word := range words {
		syntax.Walk(word, func(inner syntax.Node) bool {
			if found, ok := inner.(*syntax.ArithmExp); ok {
				after = process.arithmetic(found, after)
				return false
			}
			_, nested := inner.(*syntax.CmdSubst)
			return !nested
		})
	}
	return after
}

// enter and leave count scripts inside scripts; past maxDepth the line is refused (too-deep).
func (reader *reader) enter(depth int) {
	if depth > maxDepth {
		panic(stop{rule: ruleTooDeep})
	}
}

func (reader *reader) leave() {}

// tick counts the commands read for one call and watches the reading's deadline.
func (reader *reader) tick() {
	reader.steps++
	if reader.steps > maxSteps {
		panic(stop{rule: ruleTooLong})
	}
	if reader.steps%256 == 0 && time.Now().After(reader.deadline) {
		panic(stop{rule: ruleTooLong})
	}
}

// stop ends the reading of a call that is refused as a whole (too deep, too long, unreadable).
type stop struct {
	rule string
}
