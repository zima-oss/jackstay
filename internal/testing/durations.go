package scopes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/homedir"
)

// keepDurationRuns is how many runs' durations are kept and read (testing.md section 4).
const keepDurationRuns = 20

// durationsPath is `<jackstay home>/<project>/durations.jsonl`.
func durationsPath(project string) (string, error) {
	return homedir.ProjectPath(project, "durations.jsonl")
}

// classDuration is one class's numbers in one run.
type classDuration struct {
	Slowest float64  `json:"slowest"`
	Tests   []string `json:"tests"`
	Total   float64  `json:"total"`
}

// durationRun is one line of durations.jsonl.
type durationRun struct {
	At      string                   `json:"at"`
	Classes map[string]classDuration `json:"classes"`
	Head    string                   `json:"head"`
	RunID   string                   `json:"run_id"`
	Scope   string                   `json:"scope"`
}

// readDurations are the recorded runs, oldest first, the last keepDurationRuns; an error when the
// file is there and cannot be read.
func readDurations(project string) ([]durationRun, error) {
	place, err := durationsPath(project)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(place)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found []durationRun
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for scanner.Scan() {
		var raw map[string]any
		if json.Unmarshal(scanner.Bytes(), &raw) != nil || objectOf(raw["classes"]) == nil {
			continue
		}
		var run durationRun
		if json.Unmarshal(scanner.Bytes(), &run) == nil {
			found = append(found, run)
		}
	}
	if len(found) > keepDurationRuns {
		found = found[len(found)-keepDurationRuns:]
	}
	return found, nil
}

func round3(value float64) float64 { return math.Round(value*1000) / 1000 }

// recordDurations adds one run's per-class seconds (class → test → seconds), rewriting the file
// under its `.lock` flock and keeping the last keepDurationRuns; an error when it cannot.
func recordDurations(project, runID, scope, head string, cases map[string]map[string]float64, now time.Time) error {
	classes := map[string]classDuration{}
	for class, tests := range cases {
		if len(tests) == 0 {
			continue
		}
		var total, slowest float64
		for _, seconds := range tests {
			total += seconds
			slowest = max(slowest, seconds)
		}
		classes[class] = classDuration{Total: round3(total), Slowest: round3(slowest), Tests: slices.Sorted(func(yield func(string) bool) {
			for name := range tests {
				if !yield(name) {
					return
				}
			}
		})}
	}
	if len(classes) == 0 {
		return nil
	}
	place, err := durationsPath(project)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(place), 0o700); err != nil {
		return err
	}
	held, err := durable.Lock(context.Background(), place)
	if err != nil {
		return err
	}
	defer held.Release()
	kept, err := readDurations(project)
	if err != nil {
		kept = nil
	}
	kept = append(kept, durationRun{RunID: runID, Scope: scope, Head: head, At: now.Format("2006-01-02T15:04:05-0700"), Classes: classes})
	if len(kept) > keepDurationRuns {
		kept = kept[len(kept)-keepDurationRuns:]
	}
	var text bytes.Buffer
	for _, each := range kept {
		line, err := json.Marshal(each)
		if err != nil {
			return err
		}
		text.Write(line)
		text.WriteByte('\n')
	}
	return durable.WriteAtomic(place, text.Bytes(), 0o600, 0)
}

// classMedian is a class's median total and slowest case over its whole runs.
type classMedian struct {
	total, slowest float64
	runs           int
}

// classMedians are each class's medians over the runs whose set of tests for it is the largest
// set recorded for it (a run that ran part of a class does not count).
func classMedians(runs []durationRun) map[string]classMedian {
	byClass := map[string][]classDuration{}
	for _, run := range runs {
		for _, class := range sortedKeys(run.Classes) {
			byClass[class] = append(byClass[class], run.Classes[class])
		}
	}
	found := map[string]classMedian{}
	for class, seen := range byClass {
		var largest []string
		for index, facts := range seen { // oldest first: among sets of one size the newest wins
			if index == 0 || len(facts.Tests) >= len(largest) {
				largest = facts.Tests
			}
		}
		var totals, slowest []float64
		for _, facts := range seen {
			if slices.Equal(facts.Tests, largest) {
				totals = append(totals, facts.Total)
				slowest = append(slowest, facts.Slowest)
			}
		}
		found[class] = classMedian{total: median(totals), slowest: median(slowest), runs: len(totals)}
	}
	return found
}

// slowClass is a class the rule makes slow, with its medians.
type slowClass struct {
	name           string
	total, slowest float64
}

// slowClasses are the classes the rule makes slow, by name, and what to say when there are no
// data ("" when there are).
func slowClasses(project string, rule slowRule) ([]slowClass, string) {
	runs, err := readDurations(project)
	if err != nil {
		return nil, "quick=everything (the durations file cannot be read)"
	}
	if len(runs) == 0 {
		return nil, "quick=everything (no durations yet)"
	}
	medians := classMedians(runs)
	var found []slowClass
	for _, class := range sortedKeys(medians) {
		numbers := medians[class]
		if rule.hasClassSeconds && numbers.total >= rule.classSeconds || rule.hasCaseSeconds && numbers.slowest >= rule.caseSeconds {
			found = append(found, slowClass{name: class, total: numbers.total, slowest: numbers.slowest})
		}
	}
	return found, ""
}

// slowPattern is `{slow}`: a `--skip` pattern of the classes, or one that skips nothing.
func slowPattern(classes []slowClass) string {
	if len(classes) == 0 {
		return nothing
	}
	names := make([]string, 0, len(classes))
	for _, class := range classes {
		names = append(names, escapePattern(class.name))
	}
	return "^(" + strings.Join(names, "|") + ")(/|$)"
}
