package guard

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
)

// freshFolder stands for a folder `mktemp` makes in the same command: temporary inside, and never
// deleted itself. Every `$(mktemp …)` of a line reads as a folder under it.
const freshFolder = "/private/tmp/jst-guard-mktemp"

// A segment is one stretch of a word's value: known text, or a part only known when the command
// runs; whether it stood in quotes; whether the shell splits it (an expansion outside quotes);
// whether it holds a pattern the shell globs.
type segment struct {
	text   string
	known  bool
	quoted bool
	split  bool
	glob   bool
}

// A reading is one way a word may be read: when a variable holds one of a few texts, a word has
// a reading for each.
type reading []segment

func (one reading) known() bool {
	for _, part := range one {
		if !part.known {
			return false
		}
	}
	return true
}

func (one reading) text() string {
	var built strings.Builder
	for _, part := range one {
		built.WriteString(part.text)
	}
	return built.String()
}

// An arg is one word of a command after expansion, as the rules see it.
type arg struct {
	written string    // as it was written in the line
	texts   []string  // every known text it may be (one, or one of a few); nil: not known
	parts   []segment // its segments, when it is one reading (for the shape of an unknown word)
	braced  bool      // it held a brace list
	opaque  bool      // it stands for what another program supplies (xargs, find)
	spreads bool      // one of its readings splits into several words
	dynamic bool      // a part of it was an expansion (a variable, a substitution)
	globbed bool      // it holds an unquoted pattern character
	word    *syntax.Word
}

func literalArg(text string) arg {
	return arg{written: text, texts: []string{text}, parts: []segment{{text: text, known: true}}}
}

func opaqueArg(written string) arg {
	return arg{written: written, opaque: true, dynamic: true, parts: []segment{{text: written}}}
}

func (one arg) known() bool { return one.texts != nil && !one.opaque }

// single is the one text the arg is known to be; "" and false when it is not one known text.
func (one arg) single() (string, bool) {
	if one.known() && len(one.texts) == 1 {
		return one.texts[0], true
	}
	return "", false
}

// value is the text for matching: the known text, else the word as it was written.
func (one arg) value() string {
	if text, ok := one.single(); ok {
		return text
	}
	return one.written
}

// unknownOption says whether the arg is an option nobody can name: it starts with a dash and holds
// an unknown part (`-$F`, `--$mode`), or one of the texts it may be is an option. A word that is
// wholly unknown (`"$branch"`) is taken as a name (guard.md, "What it cannot see").
func (one arg) unknownOption() bool {
	if one.opaque {
		return false
	}
	if one.known() {
		if len(one.texts) < 2 {
			return false
		}
		for _, text := range one.texts {
			if strings.HasPrefix(text, "-") {
				return true
			}
		}
		return false
	}
	return len(one.parts) > 0 && one.parts[0].known && strings.HasPrefix(one.parts[0].text, "-")
}

func (one arg) startsWithDash() bool {
	text, ok := one.single()
	return ok && strings.HasPrefix(text, "-")
}

// expansion holds what words are read with: the state and the reader's knowledge of the session.
type expansion struct {
	reader *reader
	here   *state
}

// readings of a word: each a list of segments. Too many readings make the word one unknown reading.
func (exp expansion) readings(word *syntax.Word) []reading {
	word = braceSplit(word)
	found := []reading{{}}
	for index, part := range word.Parts {
		options := exp.part(part, false, index == 0)
		found = product(found, options)
		if found == nil {
			return []reading{{{text: wordSource(word), quoted: true}}}
		}
	}
	for _, one := range found {
		if len(one.text()) > maxValueBytes {
			return []reading{{{text: wordSource(word), quoted: true}}}
		}
	}
	return found
}

func product(left, right []reading) []reading {
	if len(left) == 1 && len(right) == 1 {
		left[0] = appendSegments(left[0], right[0])
		return left
	}
	if len(left)*len(right) > maxReadings {
		return nil
	}
	combined := make([]reading, 0, len(left)*len(right))
	for _, first := range left {
		for _, second := range right {
			joined := appendSegments(slices.Clone(first), second)
			combined = append(combined, joined)
		}
	}
	return combined
}

// appendSegments adds segments to a reading, joining a known one to the known one before it when
// they read alike, so that a long word stays a few segments.
func appendSegments(segments, more reading) reading {
	for _, part := range more {
		if last := len(segments) - 1; last >= 0 && part.known && segments[last].known && segments[last].quoted == part.quoted &&
			segments[last].split == part.split && segments[last].glob == part.glob {
			segments[last].text += part.text
			continue
		}
		segments = append(segments, part)
	}
	return segments
}

func unknownPart(quoted bool) []reading {
	return []reading{{{quoted: quoted, split: !quoted}}}
}

func (exp expansion) part(part syntax.WordPart, quoted, first bool) []reading {
	switch node := part.(type) {
	case *syntax.Lit:
		if quoted {
			return []reading{{{text: unescapeDouble(node.Value), known: true, quoted: true}}}
		}
		return []reading{exp.unquotedLiteral(node.Value, first)}
	case *syntax.SglQuoted:
		text := node.Value
		if node.Dollar {
			text = ansiC(text)
		}
		return []reading{{{text: text, known: true, quoted: true}}}
	case *syntax.DblQuoted:
		found := []reading{{}}
		for _, inner := range node.Parts {
			found = product(found, exp.part(inner, true, false))
			if found == nil {
				return unknownPart(true)
			}
		}
		if len(node.Parts) == 0 {
			return []reading{{{known: true, quoted: true}}}
		}
		return found
	case *syntax.ParamExp:
		return exp.parameter(node, quoted)
	case *syntax.CmdSubst:
		texts := exp.substituted(node)
		if texts == nil {
			return unknownPart(quoted)
		}
		options := make([]reading, 0, len(texts))
		for _, text := range texts {
			options = append(options, reading{{text: strings.TrimRight(text, "\n"), known: true, quoted: quoted, split: !quoted}})
		}
		return options
	case *syntax.BraceExp:
		if node.Sequence || quoted {
			return unknownPart(quoted)
		}
		var options []reading
		for _, element := range node.Elems {
			options = append(options, exp.readings(element)...)
			if len(options) > maxReadings {
				return unknownPart(false)
			}
		}
		return options
	case *syntax.ExtGlob:
		return []reading{{{text: node.Op.String() + node.Pattern.Value + ")", known: true, glob: true}}}
	default: // arithmetic, process substitution: only known when it runs
		return unknownPart(quoted)
	}
}

// unquotedLiteral reads literal text outside quotes: a backslash quotes the next character, `*`,
// `?` and `[` are a pattern, and a leading `~` is the account's home.
func (exp expansion) unquotedLiteral(text string, first bool) reading {
	var found reading
	var plain strings.Builder
	globbed := false
	flush := func() {
		if plain.Len() > 0 {
			found = append(found, segment{text: plain.String(), known: true, glob: globbed})
			plain.Reset()
			globbed = false
		}
	}
	if first && strings.HasPrefix(text, "~") {
		rest := text[1:]
		end := strings.IndexByte(rest, '/')
		if end < 0 {
			end = len(rest)
		}
		if end == 0 {
			plain.WriteString(exp.reader.home)
			text = rest
		} else {
			found = append(found, segment{split: false})
			text = rest[end:]
		}
	}
	for index := 0; index < len(text); index++ {
		char := text[index]
		switch {
		case char == '\\' && index+1 < len(text):
			if text[index+1] == '\n' {
				index++
				continue
			}
			flush()
			_, size := utf8.DecodeRuneInString(text[index+1:])
			found = append(found, segment{text: text[index+1 : index+1+size], known: true, quoted: true})
			index += size
		case char == '*' || char == '?' || char == '[':
			globbed = true
			plain.WriteByte(char)
		default:
			plain.WriteByte(char)
		}
	}
	flush()
	if len(found) == 0 {
		found = reading{{known: true}}
	}
	return found
}

// parameter reads `$NAME` and `${NAME…}` with what the line knows.
func (exp expansion) parameter(node *syntax.ParamExp, quoted bool) []reading {
	if node.Param == nil || node.Length || node.Excl || node.Width || node.IsSet || node.Index != nil ||
		node.Slice != nil || node.Repl != nil || node.Names != 0 || node.Flags != nil || len(node.Modifiers) > 0 ||
		node.NestedParam != nil {
		return unknownPart(quoted)
	}
	name := node.Param.Value
	held, set := exp.variable(name)
	if node.Exp != nil {
		switch node.Exp.Op {
		case syntax.DefaultUnsetOrNull, syntax.DefaultUnset:
			if !set {
				return unknownPart(quoted)
			}
			if held.known() && len(held.texts) == 1 && (held.texts[0] == "" && node.Exp.Op == syntax.DefaultUnsetOrNull) {
				if node.Exp.Word == nil {
					return []reading{{{known: true, quoted: quoted}}}
				}
				return exp.nested(node.Exp.Word, quoted)
			}
		case syntax.RemSmallSuffix, syntax.RemLargeSuffix, syntax.RemSmallPrefix, syntax.RemLargePrefix:
			pattern := ""
			if node.Exp.Word != nil {
				literal, ok := literalWord(node.Exp.Word)
				if !ok || strings.ContainsAny(literal, "*?[") {
					return unknownPart(quoted)
				}
				pattern = literal
			}
			if !held.known() {
				return unknownPart(quoted)
			}
			trimmed := make([]string, 0, len(held.texts))
			for _, text := range held.texts {
				switch node.Exp.Op {
				case syntax.RemSmallSuffix, syntax.RemLargeSuffix:
					trimmed = append(trimmed, strings.TrimSuffix(text, pattern))
				default:
					trimmed = append(trimmed, strings.TrimPrefix(text, pattern))
				}
			}
			held = value{texts: trimmed}
		default:
			return unknownPart(quoted)
		}
	}
	if !held.known() {
		return unknownPart(quoted)
	}
	options := make([]reading, 0, len(held.texts))
	for _, text := range held.texts {
		options = append(options, reading{{text: text, known: true, quoted: quoted, split: !quoted}})
	}
	return options
}

// nested reads a word inside an expansion (a default's text) in the quoting it stands in.
func (exp expansion) nested(word *syntax.Word, quoted bool) []reading {
	found := []reading{{}}
	for _, part := range word.Parts {
		found = product(found, exp.part(part, quoted, false))
		if found == nil {
			return unknownPart(quoted)
		}
	}
	return found
}

// variable is what NAME holds at this point, and whether it is set at all: what the line set; the
// session's TMPDIR and the account's HOME; PWD where the folder is one; else unknown.
func (exp expansion) variable(name string) (value, bool) {
	if held, ok := exp.here.vars[name]; ok {
		return held, true
	}
	if exp.here.blind {
		return unknownValue(), true
	}
	switch name {
	case "TMPDIR":
		return certain(exp.reader.tmpdir), true
	case "HOME":
		return certain(exp.reader.home), true
	case "PWD":
		if len(exp.here.places) == 1 {
			return certain(exp.here.places[0]), true
		}
	}
	return unknownValue(), false
}

// substituted is what a command substitution writes when that can be said: a folder `mktemp`
// makes, or the text of a constant `echo`, `printf` or `cat` of a here-document (W-017).
func (exp expansion) substituted(node *syntax.CmdSubst) []string {
	if len(node.Stmts) != 1 {
		return nil
	}
	if madeByMktemp(node.Stmts[0]) {
		return []string{freshFolder + "/made"}
	}
	return exp.reader.produced(node.Stmts[0], exp.here)
}

// madeByMktemp says whether a statement only runs `mktemp` (`mktemp -d 2>/dev/null || mktemp -d -t x`).
func madeByMktemp(stmt *syntax.Stmt) bool {
	if stmt.Negated || stmt.Background {
		return false
	}
	switch command := stmt.Cmd.(type) {
	case *syntax.BinaryCmd:
		return command.Op == syntax.OrStmt && madeByMktemp(command.X) && madeByMktemp(command.Y)
	case *syntax.CallExpr:
		words := command.Args
		if len(command.Assigns) > 0 || len(words) == 0 {
			return false
		}
		head, ok := literalWord(words[0])
		if ok && head == "command" && len(words) > 1 {
			head, ok = literalWord(words[1])
		}
		return ok && (head == "mktemp" || head == "/usr/bin/mktemp")
	}
	return false
}

// literalWord is a word's text when it has no expansion in it.
func literalWord(word *syntax.Word) (string, bool) {
	var built strings.Builder
	for _, part := range word.Parts {
		switch node := part.(type) {
		case *syntax.Lit:
			built.WriteString(unescapeUnquoted(node.Value))
		case *syntax.SglQuoted:
			if node.Dollar {
				built.WriteString(ansiC(node.Value))
			} else {
				built.WriteString(node.Value)
			}
		case *syntax.DblQuoted:
			for _, inner := range node.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				built.WriteString(unescapeDouble(lit.Value))
			}
		default:
			return "", false
		}
	}
	return built.String(), true
}

func unescapeUnquoted(text string) string {
	if !strings.Contains(text, "\\") {
		return text
	}
	var built strings.Builder
	for index := 0; index < len(text); index++ {
		if text[index] == '\\' && index+1 < len(text) {
			index++
			if text[index] == '\n' {
				continue
			}
		}
		built.WriteByte(text[index])
	}
	return built.String()
}

func unescapeDouble(text string) string {
	if !strings.Contains(text, "\\") {
		return text
	}
	var built strings.Builder
	for index := 0; index < len(text); index++ {
		if text[index] == '\\' && index+1 < len(text) && strings.IndexByte("$`\"\\\n", text[index+1]) >= 0 {
			index++
			if text[index] == '\n' {
				continue
			}
		}
		built.WriteByte(text[index])
	}
	return built.String()
}

// ansiC reads the escapes of `$'…'` and of `echo -e`, `printf`'s format and `%b`.
func ansiC(text string) string {
	if !strings.Contains(text, "\\") {
		return text
	}
	var built strings.Builder
	for index := 0; index < len(text); index++ {
		if text[index] != '\\' || index+1 >= len(text) {
			built.WriteByte(text[index])
			continue
		}
		index++
		switch char := text[index]; char {
		case 'n', 'f', 'v':
			built.WriteByte('\n')
		case 't':
			built.WriteByte('\t')
		case 'r':
			built.WriteByte('\r')
		case 'a', 'b', 'e', 'E', 'c':
		case 'x', 'u', 'U':
			limit := map[byte]int{'x': 2, 'u': 4, 'U': 8}[char]
			end := index + 1
			for end < len(text) && end-index-1 < limit && strings.IndexByte("0123456789abcdefABCDEF", text[end]) >= 0 {
				end++
			}
			if end == index+1 {
				built.WriteByte('\\')
				built.WriteByte(char)
				continue
			}
			if char == 'x' {
				number, _ := strconv.ParseUint(text[index+1:end], 16, 8)
				built.WriteByte(byte(number & 0xff))
			} else {
				number, _ := strconv.ParseInt(text[index+1:end], 16, 32)
				built.WriteRune(rune(int32(number)))
			}
			index = end - 1
		case '0', '1', '2', '3', '4', '5', '6', '7':
			end := index
			for end < len(text) && end-index < 3 && text[end] >= '0' && text[end] <= '7' {
				end++
			}
			number, _ := strconv.ParseUint(text[index:end], 8, 16)
			built.WriteByte(byte(number & 0xff))
			index = end - 1
		default:
			built.WriteByte(char)
		}
	}
	return built.String()
}

// fields splits a reading as the shell does: an expansion outside quotes splits at blanks.
func fields(one reading) []reading {
	var found []reading
	var current reading
	started := false
	for _, part := range one {
		if !part.split || !part.known {
			current = append(current, part)
			started = true
			continue
		}
		pieces := strings.FieldsFunc(part.text, isBlank)
		leading := part.text != "" && isBlank(rune(part.text[0]))
		trailing := part.text != "" && isBlank(rune(part.text[len(part.text)-1]))
		if len(pieces) == 0 {
			if part.text != "" && started {
				found = append(found, current)
				current, started = nil, false
			}
			continue
		}
		for index, piece := range pieces {
			if (index > 0 || leading) && started {
				found = append(found, current)
				current = nil
			}
			current = append(current, segment{text: piece, known: true, glob: strings.ContainsAny(piece, "*?[")})
			started = true
		}
		if trailing {
			found = append(found, current)
			current, started = nil, false
		}
	}
	if started {
		found = append(found, current)
	}
	return found
}

func isBlank(char rune) bool { return char == ' ' || char == '\t' || char == '\n' }

// args makes the words of a command from the line's words.
func (exp expansion) args(words []*syntax.Word) []arg {
	var found []arg
	for _, word := range words {
		found = append(found, exp.argsOf(word)...)
	}
	return found
}

func (exp expansion) argsOf(word *syntax.Word) []arg {
	written := wordSource(word)
	braced := hasBraces(word)
	dynamic := hasExpansion(word)
	options := exp.readings(word)
	if len(options) == 1 {
		var found []arg
		for _, field := range fields(options[0]) {
			one := arg{written: written, parts: field, braced: braced, dynamic: dynamic, word: word}
			if field.known() {
				one.texts = []string{field.text()}
			}
			one.globbed = globbedParts(field)
			found = append(found, one)
		}
		return found
	}
	one := arg{written: written, braced: braced, dynamic: dynamic, word: word}
	texts := make([]string, 0, len(options))
	known := true
	for _, option := range options {
		split := fields(option)
		if len(split) != 1 {
			one.spreads = true
		}
		if !option.known() {
			known = false
		}
		texts = append(texts, option.text())
		one.globbed = one.globbed || globbedParts(option)
	}
	if known && !one.spreads {
		one.texts = texts
	}
	return []arg{one}
}

func globbedParts(parts reading) bool {
	for _, part := range parts {
		if part.glob && !part.quoted {
			return true
		}
	}
	return false
}

// braceSplit is the word with its brace lists made into brace expansions, on a copy: the tree
// itself stays as the parser made it.
func braceSplit(word *syntax.Word) *syntax.Word {
	copied := &syntax.Word{Parts: slices.Clone(word.Parts)}
	syntax.SplitBraces(copied)
	return copied
}

func hasBraces(word *syntax.Word) bool {
	for _, part := range braceSplit(word).Parts {
		if _, ok := part.(*syntax.BraceExp); ok {
			return true
		}
	}
	return false
}

func hasExpansion(word *syntax.Word) bool {
	found := false
	syntax.Walk(word, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ProcSubst:
			found = true
		}
		return !found
	})
	return found
}

// wordSource is the word as it was written in the line.
func wordSource(word *syntax.Word) string {
	var built strings.Builder
	printer := syntax.NewPrinter(syntax.Minify(true))
	if err := printer.Print(&built, word); err != nil {
		return word.Lit()
	}
	return built.String()
}
