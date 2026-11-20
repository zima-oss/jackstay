package review

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zima-oss/jackstay/internal/review/runner"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

// A crashed or partly failed attempt keeps the answers its reviewers gave. The next run of the
// task re-runs only the reviewers whose answer is missing (docs/decisions/W-090-review-of-0-6-0.md): a saved answer is reused when
// the attempt that holds it is bound to exactly what this attempt asks — the same round, commits,
// prompt, verdict schema, review policy and reviewers' settings.

// bindingOf names what an attempt asks of its reviewers and who answers: two attempts with one
// binding put the same question to the same reviewers, each with the same command, model, effort
// and isolation, so an answer saved without its run.json still cannot be credited to another model.
func bindingOf(planned plan, settings Settings, names []string, prompt, schema, brief []byte) string {
	digest := func(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
	data, _ := json.Marshal(struct {
		Round                        int
		Task, Commit, Base, Previous string
		Policy, ContentPolicy        string
		Prompt, Schema, Brief        string
		Reviewers                    []ReviewerSettings
	}{planned.number, planned.task, planned.commit, planned.base, planned.previous, settings.policyID(),
		planned.contentPolicy, digest(prompt), digest(schema), digest(brief), reviewersOf(settings, names)})
	return "v1:" + digest(data)
}

// reused is a reviewer's answer kept by an earlier attempt.
type reused struct {
	from   string // the attempt's folder name
	run    ReviewerRun
	answer Answer
}

// reusableAnswers finds, for each reviewer, the newest earlier attempt bound to binding whose
// answer of that reviewer still reads and checks. An attempt that became a round is never a
// source: its round already counts.
func (round Round) reusableAnswers(planned plan, settings Settings, binding string, names []string) map[string]reused {
	found := map[string]reused{}
	entries, err := round.Store.Entries(planned.task)
	if err != nil {
		return found
	}
	for index := len(entries) - 1; index >= 0 && len(found) < len(names); index-- {
		entry := entries[index]
		if entry.Round != 0 || entry.Attempt >= planned.attempt {
			continue
		}
		var record AttemptRecord
		data, err := os.ReadFile(filepath.Join(entry.Path, AttemptName))
		if err != nil || strictjson.UnmarshalOpen(data, &record) != nil || record.Binding != binding ||
			record.Task != planned.task || record.Attempt != entry.Attempt {
			continue
		}
		for _, name := range names {
			if _, done := found[name]; done {
				continue
			}
			if saved, ok := savedAnswer(filepath.Join(entry.Path, name), name, settings, planned.ledger); ok {
				saved.from = entry.Name
				found[name] = saved
			}
		}
	}
	return found
}

// savedAnswer is a reviewer folder's answer when it can stand for a run now: it reads and checks
// as a fresh one would, is not an incomplete verdict, and was given with the same settings. A
// missing run.json is a run whose jackstay ended after the answer was written.
func savedAnswer(folder, name string, settings Settings, ledger []LedgerFinding) (reused, bool) {
	answer, outcome, _ := ReadAnswer(filepath.Join(folder, "answer.json"))
	if outcome != runner.OutcomeDone || answer.Verdict == "incomplete" || CheckAnswer(answer, settings, ledger) != "" {
		return reused{}, false
	}
	reviewer := settings.Reviewers[name]
	run := ReviewerRun{Reviewer: name, Model: reviewer.Model, Effort: reviewer.Effort, Exit: -1}
	if data, err := os.ReadFile(filepath.Join(folder, "run.json")); err == nil {
		var saved ReviewerRun
		if strictjson.UnmarshalOpen(data, &saved) != nil || saved.Reviewer != name ||
			(saved.Outcome != runner.OutcomeDone && saved.Outcome != "") {
			return reused{}, false
		}
		run = saved
	}
	if run.Model != reviewer.Model || run.Effort != reviewer.Effort {
		return reused{}, false
	}
	run.Outcome, run.Reason = runner.OutcomeDone, ""
	return reused{run: run, answer: answer}, true
}

// adopt copies a reused answer into this attempt's reviewer folder, so the round's folder holds
// every answer it counts.
func (saved reused) adopt(folder string) (ReviewerRun, error) {
	run := saved.run
	run.Reused = saved.from
	data, err := json.Marshal(saved.answer)
	if err != nil {
		return run, err
	}
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return run, err
	}
	if err := writeFileWhole(filepath.Join(folder, "answer.json"), data); err != nil {
		return run, err
	}
	run.Argv = redactedArgv(run.Argv) // a run.json kept before redaction is not copied as it was
	return run, writeRun(filepath.Join(folder, "run.json"), run)
}

// reviewersOf are who answers: each reviewer's settings less its deadline, which changes how long
// it may take, not what it answers (a retry with a longer --timeout reuses what was answered).
func reviewersOf(settings Settings, names []string) []ReviewerSettings {
	reviewers := make([]ReviewerSettings, 0, len(names))
	for _, name := range names {
		reviewer := settings.Reviewers[name]
		reviewer.TimeoutMinutes = 0
		reviewers = append(reviewers, reviewer)
	}
	return reviewers
}
