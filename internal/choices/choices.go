// Package choices are the questions `jst setup` and `jst adopt` put to a person, so nothing about
// how jackstay behaves in a project is a surprise (W-090): which reviewer runs, how far the agent
// may land on its own, whether chat is on, and where each test scope runs. Every question shows
// its options with the recommended one first and marked.
//
// Setup keeps a machine default for the questions that have one; adopt offers it first, and the
// project's jackstay.json decides: a value it already sets is never asked again. An answer is
// written to its configuration key through a Key; a question whose key this jackstay does not
// read yet is not asked by adopt (its answer would have nowhere to go), but setup keeps it.
package choices

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/adopt/jsondoc"
)

// Question is one choice.
type Question struct {
	// ID is how flags and the machine defaults name it (`--answer reviewer=codex`).
	ID string
	// Text is the question.
	Text string
	// Options are the answers, the recommended one first.
	Options []Option
	// Machine says setup keeps a machine default for it, which adopt offers first.
	Machine bool
	// Key writes and reads the answer in jackstay.json; nil while no key holds it.
	Key Key
}

// Option is one answer.
type Option struct {
	Value string
	Label string
}

// Recommended is the first option's value.
func (question Question) Recommended() string { return question.Options[0].Value }

// Has says value is one of the options.
func (question Question) Has(value string) bool {
	return slices.ContainsFunc(question.Options, func(option Option) bool { return option.Value == value })
}

// Values are the options' values, as a usage line names them.
func (question Question) Values() string {
	values := make([]string, len(question.Options))
	for index, option := range question.Options {
		values[index] = option.Value
	}
	return strings.Join(values, ", ")
}

// Key is where an answer lives in jackstay.json.
type Key interface {
	// Name is the key as a person reads it (`review.use`).
	Name() string
	// Read is the project's value, false when the file does not set it.
	Read(document *jsondoc.Object) (string, bool)
	// Write sets the answer. An answer that equals what the absent key means may leave the file
	// unchanged.
	Write(document *jsondoc.Object, value string)
}

// The questions, in the order they are asked.
var (
	Reviewer = Question{ID: "reviewer", Text: "Which reviewer runs `jst review` here?", Machine: true,
		Options: []Option{{"codex", "Codex (recommended)"}, {"claude-code", "Claude Code"}, {"codex,claude-code", "both, Codex and Claude Code"}},
		Key:     reviewUse{}}
	Autonomy = Question{ID: "autonomy", Text: "How far may the agent land a change on its own?", Machine: true,
		Options: []Option{
			{"merge-when-green", "the agent merges when the required checks pass; jackstay.json and the listed paths need a person (recommended)"},
			{"prepare", "the agent prepares the change; a person merges"}}}
	Chat = Question{ID: "chat", Text: "Turn on the agent chat for this repository?", Machine: true,
		Options: []Option{{"off", "off (recommended; it can be turned on per repository later)"}, {"on", "on"}},
		Key:     chatEnabled{}}
)

// All are the questions, in order. A project's test scopes get theirs from ScopeQuestions.
func All() []Question { return []Question{Reviewer, Autonomy, Chat} }

// Find is the question of that ID.
func Find(id string) (Question, bool) {
	for _, question := range All() {
		if question.ID == id {
			return question, true
		}
	}
	return Question{}, false
}

// ScopeQuestions ask, for each of a project's test scopes, whether CI runs it (W-092): the
// project's split between local runs and CI. Their key, a scope's `ci`, is not read by this
// jackstay yet, so they carry none and adopt does not ask them until it is.
func ScopeQuestions(scopes []string) []Question {
	questions := make([]Question, 0, len(scopes))
	for _, scope := range scopes {
		questions = append(questions, Question{ID: "ci." + scope, Text: fmt.Sprintf("Does CI run the test scope %s?", scope),
			Options: []Option{{"local", "no, it runs locally before a pull request (recommended until CI runs it)"},
				{"linux", "yes, on Linux"}, {"darwin", "yes, on macOS"}, {"linux,darwin", "yes, on Linux and macOS"}}})
	}
	return questions
}

// ParseAnswer reads `ID=VALUE` (from `--answer`) against the questions given.
func ParseAnswer(text string, questions []Question) (string, string, error) {
	id, value, found := strings.Cut(text, "=")
	if !found || id == "" || value == "" {
		return "", "", errors.New("--answer takes ID=VALUE")
	}
	for _, question := range questions {
		if question.ID != id {
			continue
		}
		if !question.Has(value) {
			return "", "", fmt.Errorf("--answer %s takes one of: %s", id, question.Values())
		}
		return id, value, nil
	}
	known := make([]string, len(questions))
	for index, question := range questions {
		known[index] = question.ID
	}
	return "", "", fmt.Errorf("--answer: no question %q (the questions: %s)", id, strings.Join(known, ", "))
}
