// Package choices are two of the questions `jst setup` and `jst adopt` put to a person, so nothing
// about how jackstay behaves in a project is a surprise (W-090): which reviewer runs and whether
// chat is on. Who merges (land.autonomy) is asked by adopt's land planning the same way (#104).
// Every question shows its options with the recommended one first.
//
// Setup keeps this machine's reviewer in `<jackstay home>/.all/setup.json` beside land_autonomy;
// adopt offers it first, and the project's jackstay.json decides: a value it already sets is
// never asked again. Limits: two questions, each a fixed list of answers; no per-scope CI question
// until a scope's `ci` key is read.
package choices

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/adopt/jsondoc"
)

// Question is one choice.
type Question struct {
	// ID is how its flag and the machine file name it (`--reviewer codex`).
	ID string
	// Text is the question.
	Text string
	// Options are the answers, the recommended one first.
	Options []Option
	// Machine says setup keeps a machine answer for it, which adopt offers first.
	Machine bool
	// Key writes and reads the answer in jackstay.json.
	Key Key
	// Schema3 says the key needs "jackstay_schema": 3, so a project pinned to an older jackstay is
	// not asked.
	Schema3 bool
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
	Chat = Question{ID: "chat", Text: "Turn on the agent chat for this repository?",
		Options: []Option{{"off", "off (recommended; it can be turned on per repository later)"}, {"on", "on"}},
		Key:     chatEnabled{}, Schema3: true} // per repository, so setup keeps no machine answer
)

// All are the questions, in order.
func All() []Question { return []Question{Reviewer, Chat} }

// Find is the question of that ID.
func Find(id string) (Question, bool) {
	for _, question := range All() {
		if question.ID == id {
			return question, true
		}
	}
	return Question{}, false
}

// Check is nil when value is one of the question's answers, else an error naming them.
func (question Question) Check(value string) error {
	if question.Has(value) {
		return nil
	}
	return fmt.Errorf("--%s takes one of: %s", question.ID, question.Values())
}
