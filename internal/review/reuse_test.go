package review

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zima-oss/jackstay/internal/review/codex"
)

// byReviewer scripts each reviewer apart, since reviewers of one round run at once.
type byReviewer struct {
	mutex   sync.Mutex
	scripts map[string][]any
	calls   map[string]int
}

func (fake *byReviewer) run(ctx context.Context, request codex.Request) codex.Result {
	fake.mutex.Lock()
	next := fake.scripts[request.Reviewer][0]
	fake.scripts[request.Reviewer] = fake.scripts[request.Reviewer][1:]
	fake.calls[request.Reviewer]++
	fake.mutex.Unlock()
	one := &scripted{answers: []any{next}}
	return one.run(ctx, request)
}

func twoReviewers(t *testing.T, scripts map[string][]any) (*harness, *byReviewer) {
	t.Helper()
	h := newHarness(t)
	fake := &byReviewer{scripts: scripts, calls: map[string]int{}}
	h.round.Machine.RunReviewer = fake.run
	h.round.Settings.Use = []string{"codex", "claude-code"}
	return h, fake
}

func TestACrashedReviewReRunsOnlyTheReviewerWhoseAnswerIsMissing(t *testing.T) {
	approve := Answer{Verdict: "approve", Summary: "clear"}
	h, fake := twoReviewers(t, map[string][]any{
		"codex":       {approve},
		"claude-code": {codex.OutcomeTimeout, approve},
	})
	ctx := context.Background()
	failed := h.round.Run(ctx, Options{})
	if failed.Status != ExitReviewer || failed.Record != nil {
		t.Fatalf("an attempt with one reviewer timed out became a round: %+v", failed)
	}
	// The jackstay that ran it was killed after Codex wrote its answer: no run.json.
	if err := os.Remove(filepath.Join(failed.Folder, "codex", "run.json")); err != nil {
		t.Fatal(err)
	}
	result := h.round.Run(ctx, Options{})
	if result.Status != ExitClear || result.Record == nil {
		t.Fatalf("second attempt: %+v", result)
	}
	if fake.calls["codex"] != 1 || fake.calls["claude-code"] != 2 {
		t.Fatalf("calls %v: Codex answered once and must not be paid for again", fake.calls)
	}
	for _, run := range result.Record.Runs {
		want := ""
		if run.Reviewer == "codex" {
			want = filepath.Base(failed.Folder)
		}
		if run.Reused != want {
			t.Fatalf("%s reused %q, want %q", run.Reviewer, run.Reused, want)
		}
	}
	if _, err := os.Stat(filepath.Join(result.Folder, "codex", "answer.json")); err != nil {
		t.Fatalf("the round's folder lacks the reused answer: %v", err)
	}
	report, _ := os.ReadFile(filepath.Join(result.Folder, "report.md"))
	if !strings.Contains(string(report), "answer reused from "+filepath.Base(failed.Folder)) {
		t.Fatalf("the report does not say the answer was reused:\n%s", report)
	}
}

func TestEveryAnswerSavedMeansNoReviewerRunsAgain(t *testing.T) {
	approve := Answer{Verdict: "approve", Summary: "clear"}
	h, fake := twoReviewers(t, map[string][]any{"codex": {approve}, "claude-code": {approve}})
	ctx := context.Background()
	first := h.round.Run(ctx, Options{})
	if first.Record == nil {
		t.Fatalf("first: %+v", first)
	}
	// A crash between the answers and the round's rename leaves an attempt with every answer.
	attempt := filepath.Join(filepath.Dir(first.Folder), "attempt-9")
	if err := os.Rename(first.Folder, attempt); err != nil {
		t.Fatal(err)
	}
	var record AttemptRecord
	if err := os.Remove(filepath.Join(attempt, RecordName)); err != nil {
		t.Fatal(err)
	}
	rewriteAttemptNumber(t, attempt, 9, &record)
	h.round.Machine.RunReviewer = func(context.Context, codex.Request) codex.Result {
		t.Fatal("a reviewer ran although every answer was saved")
		return codex.Result{}
	}
	result := h.round.Run(ctx, Options{})
	if result.Status != ExitClear || result.Record == nil || result.Record.Round != 1 {
		t.Fatalf("round from saved answers: %+v", result)
	}
	if fake.calls["codex"] != 1 || fake.calls["claude-code"] != 1 {
		t.Fatalf("calls %v", fake.calls)
	}
}

func rewriteAttemptNumber(t *testing.T, folder string, number int, record *AttemptRecord) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(folder, AttemptName))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, record); err != nil {
		t.Fatal(err)
	}
	record.Attempt = number
	if err := writeJSON(filepath.Join(folder, AttemptName), record); err != nil {
		t.Fatal(err)
	}
}

func TestASavedAnswerToAnotherQuestionIsNotReused(t *testing.T) {
	approve := Answer{Verdict: "approve", Summary: "clear"}
	for name, change := range map[string]func(*harness, *Options){
		"focus":  func(_ *harness, options *Options) { options.Focus = "the parser" },
		"model":  func(h *harness, _ *Options) { setModel(h, "codex", "another-model") },
		"policy": func(h *harness, _ *Options) { h.round.Settings.Blocking = []string{"blocker", "major"} },
		"commit": func(_ *harness, options *Options) { options.Revision = "c2" },
	} {
		t.Run(name, func(t *testing.T) {
			h, fake := twoReviewers(t, map[string][]any{
				"codex":       {approve, approve},
				"claude-code": {codex.OutcomeFailed, approve},
			})
			ctx := context.Background()
			if failed := h.round.Run(ctx, Options{}); failed.Status != ExitReviewer {
				t.Fatalf("first: %+v", failed)
			}
			options := Options{}
			change(h, &options)
			result := h.round.Run(ctx, options)
			if result.Status != ExitClear || fake.calls["codex"] != 2 {
				t.Fatalf("an answer to another question was reused: %+v, calls %v", result, fake.calls)
			}
		})
	}
}

func TestAnIncompleteOrInconsistentSavedAnswerIsNotReused(t *testing.T) {
	approve := Answer{Verdict: "approve", Summary: "clear"}
	h, fake := twoReviewers(t, map[string][]any{
		"codex":       {Answer{Verdict: "incomplete", Summary: "the view was empty"}, approve},
		"claude-code": {approve, approve},
	})
	ctx := context.Background()
	if failed := h.round.Run(ctx, Options{}); failed.Status != ExitReviewer {
		t.Fatalf("first: %+v", failed)
	}
	result := h.round.Run(ctx, Options{})
	if result.Status != ExitClear || fake.calls["codex"] != 2 || fake.calls["claude-code"] != 1 {
		t.Fatalf("calls %v, result %+v", fake.calls, result)
	}
}

func setModel(h *harness, name, model string) {
	reviewer := h.round.Settings.Reviewers[name]
	reviewer.Model = model
	h.round.Settings.Reviewers[name] = reviewer
}
