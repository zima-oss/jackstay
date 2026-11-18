package receipts

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zima-oss/jackstay/internal/supervise"
)

// A Judge is fed a command's output line by line (escape sequences removed, as supervise gives
// them) and says what it read. `jst test --` uses Evidence; internal/judge's judges plug in for
// scopes.
type Judge interface {
	supervise.Sink
	Reading() Reading
}

// Reading is what a judge read in the output.
type Reading struct {
	// Counts says the judge counts tests; without it a command that ended 0 is `exited-0`.
	Counts  bool
	Counted int
	// NoCount says why nothing counts tests (when Counts is false).
	NoCount string
	// Vetoed says a veto pattern named a line; Veto is that first line (it may be blank).
	Vetoed bool
	Veto   string
	// Unjudged says why the output cannot be judged ("" when it can).
	Unjudged string
}

// Evidence is the judge of `jst test --`: the command's `evidence` pattern counts its tests (the
// numbers of its first group added up over every matching line of both streams) and its `veto`
// pattern names a line that means failure whatever the status says.
type Evidence struct {
	pattern *regexp.Regexp
	veto    *regexp.Regexp
	noCount string

	counted     int
	vetoed      bool
	vetoLine    string
	uncountable bool
	uncounted   string
}

const noEvidence = "the command has no evidence pattern in receipts.commands"

// NewEvidence is the judge for a receipts.commands entry (nil: a command not listed). An evidence
// pattern without a group cannot count: an error (jackstay.json is refused before anything runs).
func NewEvidence(entry map[string]any) (*Evidence, error) {
	judge := &Evidence{noCount: noEvidence}
	if text, _ := entry["veto"].(string); text != "" {
		veto, err := regexp.Compile(text)
		if err != nil {
			return nil, fmt.Errorf("jackstay.json: the veto pattern of the command cannot be read (%w)", err)
		}
		judge.veto = veto
	}
	text, _ := entry["evidence"].(string)
	if text == "" {
		return judge, nil
	}
	pattern, err := regexp.Compile(text)
	if err != nil {
		return nil, fmt.Errorf("jackstay.json: a pattern of the command cannot be read (%w)", err)
	}
	if pattern.NumSubexp() < 1 {
		return nil, fmt.Errorf("jackstay.json: the evidence pattern needs a group that holds the number of tests, as in 'Executed ([0-9]+) tests?'")
	}
	judge.pattern, judge.noCount = pattern, ""
	return judge, nil
}

// Veto is the veto pattern (nil without one).
func (judge *Evidence) Veto() *regexp.Regexp { return judge.veto }

// Line reads one line of output.
func (judge *Evidence) Line(_ supervise.Stream, text string) error {
	if judge.veto != nil && !judge.vetoed && judge.veto.MatchString(text) {
		judge.vetoed, judge.vetoLine = true, cut(strings.TrimSpace(text), 200)
	}
	if judge.pattern == nil {
		return nil
	}
	for _, match := range judge.pattern.FindAllStringSubmatch(text, -1) {
		number, err := strconv.Atoi(strings.TrimSpace(match[1]))
		if err != nil || number < 0 {
			if !judge.uncountable {
				judge.uncountable, judge.uncounted = true, cut(strings.TrimSpace(text), 200)
			}
			continue
		}
		judge.counted += number
	}
	return nil
}

// Reading is what was read.
func (judge *Evidence) Reading() Reading {
	reading := Reading{Counts: judge.pattern != nil, Counted: judge.counted, NoCount: judge.noCount,
		Vetoed: judge.vetoed, Veto: judge.vetoLine}
	if judge.uncountable {
		reading.Unjudged = fmt.Sprintf("the evidence pattern matched a line whose group is not a number, so its tests cannot be "+
			"counted: %q (the group must hold the number of tests, as in 'Executed ([0-9]+) tests?')", judge.uncounted)
	}
	return reading
}

// cut is text at most limit characters long.
func cut(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}
