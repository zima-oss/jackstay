package guard

import (
	"errors"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// ErrUnreadable is a line neither bash nor zsh reads.
var ErrUnreadable = errors.New("the line cannot be read as shell")

// Words are the words of every simple command of a line, in the order written, as `jst baseline`
// reads them: quotes and escapes taken off, an expansion kept as it was written (`$X`, `$(…)`),
// assignments and redirections left out. The commands inside a substitution are not read: they
// are a word's text, not a command of the line.
func Words(command string) ([][]string, error) {
	file, text, ok := parsedLine(command)
	if !ok {
		return nil, ErrUnreadable
	}
	var found [][]string
	syntax.Walk(file, func(node syntax.Node) bool {
		switch typed := node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			return false
		case *syntax.CallExpr:
			if len(typed.Args) > 0 {
				words := make([]string, 0, len(typed.Args))
				for _, word := range typed.Args {
					words = append(words, plainWord(word, text))
				}
				found = append(found, words)
			}
		}
		return true
	})
	return found, nil
}

// parsedLine is the line read as bash, then as zsh, with the reader's fallbacks (an open
// here-document closed, `((` read as two subshells); and the text that was read.
func parsedLine(text string) (*syntax.File, string, bool) {
	for _, attempt := range []string{text, spacedSubshells(text)} {
		for _, language := range []syntax.LangVariant{syntax.LangBash, syntax.LangZsh} {
			candidate := attempt
			for range 8 {
				file, err := syntax.NewParser(syntax.Variant(language)).Parse(strings.NewReader(candidate), "")
				if err == nil {
					return file, candidate, true
				}
				delimiter := openHeredoc.FindStringSubmatch(err.Error())
				if delimiter == nil {
					break
				}
				candidate += "\n" + delimiter[1]
			}
		}
	}
	return nil, "", false
}

// plainWord is a word's text with its quotes taken off and each expansion as written.
func plainWord(word *syntax.Word, text string) string {
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
				if lit, isLit := inner.(*syntax.Lit); isLit {
					built.WriteString(unescapeDouble(lit.Value))
				} else {
					built.WriteString(sourceOf(inner, text))
				}
			}
		default:
			built.WriteString(sourceOf(part, text))
		}
	}
	return built.String()
}

func sourceOf(node syntax.Node, text string) string {
	start, end := node.Pos().Offset(), node.End().Offset()
	if start > end || int(end) > len(text) {
		return ""
	}
	return text[start:end]
}

// NoGit answers the questions to git for a command read long after it ran (`jst baseline`), when
// what git would have said that day cannot be asked. A verdict that needs that day's state is not
// counted (it counts only where the verdict needs no git), so every question about
// the tree is answered the way that finds nothing: no path had edits (a path restore or checkout
// never counts), a name is a branch (a checkout of it is a switch), a merge is under way with no
// edit outside its conflicts (a side-taking checkout never counts); while a branch is held by
// nothing else, so a forced delete or move — destructive whatever the tree — always counts.
// The live hook never uses NoGit: it asks git, and an unknown answer there refuses.
type NoGit struct{}

// Held says a branch is held by nothing else.
func (NoGit) Held(string, string) answer { return answerNo }

// BranchExists says the name is a branch.
func (NoGit) BranchExists(string, string) answer { return answerYes }

// Conflicts says a merge is under way.
func (NoGit) Conflicts(string) bool { return true }

// Unrelated says no edit is outside the conflicts.
func (NoGit) Unrelated(string, []string) ([]string, bool) { return nil, true }

// Changed says no path had edits.
func (NoGit) Changed(string, []string, string) ([]string, bool) { return nil, true }

// AddsNothing is not proven.
func (NoGit) AddsNothing(string, string) bool { return false }

// Checkout cannot be said.
func (NoGit) Checkout(string) (string, string, bool) { return "", "", false }

// RefExists says the ref exists.
func (NoGit) RefExists(string, string) answer { return answerYes }

// CardsPush cannot be said.
func (NoGit) CardsPush(string, CardPush) ([]string, bool) { return nil, false }
