package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/review/codex"
	"github.com/zima-oss/jackstay/internal/review/sourceview"
)

// fakeRepo answers a round's questions from maps; trees are plain folders.
type fakeRepo struct {
	commits  map[string]string // name -> id
	parents  map[string]string
	files    map[string]bool   // "commit:path"
	contents map[string]string // optional "commit:path" content
	branch   string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{commits: map[string]string{"HEAD": "c1", "c1": "c1", "c2": "c2", "c3": "c3", "main": "c0", "c0": "c0", "other": "cX"},
		parents: map[string]string{"c1": "c0", "c2": "c1", "c3": "c2"}, files: map[string]bool{"c1:AGENTS.md": true, "c2:AGENTS.md": true, "c3:AGENTS.md": true},
		branch: "feature/x"}
}

func (repo *fakeRepo) Resolve(_ context.Context, name string) (string, error) {
	if id, ok := repo.commits[name]; ok {
		return id, nil
	}
	return "", errors.New(name + " is not a commit here")
}
func (repo *fakeRepo) FirstParent(_ context.Context, commit string) (string, error) {
	return repo.parents[commit], nil
}
func (repo *fakeRepo) Branch(context.Context) (string, error) { return repo.branch, nil }
func (repo *fakeRepo) ValidBranchName(_ context.Context, name string) bool {
	return !strings.Contains(name, " ")
}
func (repo *fakeRepo) Dirty(context.Context) (bool, error)             { return false, nil }
func (repo *fakeRepo) Subject(context.Context, string) (string, error) { return "subject", nil }
func (repo *fakeRepo) Show(_ context.Context, commit, path string) ([]byte, error) {
	if repo.files[commit+":"+path] {
		if content, ok := repo.contents[commit+":"+path]; ok {
			return []byte(content), nil
		}
		return []byte("text of " + path), nil
	}
	return nil, errors.New("missing")
}

func TestStandingReviewFilesRequireFreshAfterContentEdit(t *testing.T) {
	for _, changedFile := range []string{"brief", "template"} {
		t.Run(changedFile, func(t *testing.T) {
			h := newHarness(t, Answer{Verdict: "approve"}, Answer{Verdict: "approve"})
			h.round.Settings.Brief = "docs/review-brief.md"
			h.round.Settings.Template = "docs/review-template.md"
			h.repo.contents = map[string]string{}
			for _, commit := range []string{"c1", "c2"} {
				h.repo.files[commit+":"+h.round.Settings.Brief] = true
				h.repo.files[commit+":"+h.round.Settings.Template] = true
				h.repo.contents[commit+":"+h.round.Settings.Brief] = "standing brief"
				h.repo.contents[commit+":"+h.round.Settings.Template] = "{{.Subject}}"
			}
			path := h.round.Settings.Brief
			if changedFile == "template" {
				path = h.round.Settings.Template
			}
			h.repo.contents["c2:"+path] += " edited"
			first := h.round.Run(context.Background(), Options{})
			if first.Status != ExitClear || first.Record == nil || first.Record.ContentPolicy == "" {
				t.Fatalf("first round did not pin standing files: %+v", first)
			}
			changed := h.round.Run(context.Background(), Options{Revision: "c2"})
			if changed.Status != ExitUsage || !strings.Contains(changed.Next, "--fresh") || len(h.reviewer.prompts) != 1 {
				t.Fatalf("changed standing content continued delta chain: %+v", changed)
			}
			fresh := h.round.Run(context.Background(), Options{Revision: "c0..c2", Fresh: true})
			if fresh.Status != ExitClear || fresh.Record == nil || fresh.Record.Follows != 0 || fresh.Record.ContentPolicy == first.Record.ContentPolicy {
				t.Fatalf("fresh round did not pin new standing content: %+v", fresh)
			}
		})
	}
}

func TestStandingReviewFilesAllowUnchangedDeltaAndRejectUnpinnedLegacy(t *testing.T) {
	h := newHarness(t, Answer{Verdict: "approve"}, Answer{Verdict: "approve"})
	h.round.Settings.Brief = "docs/review-brief.md"
	for _, commit := range []string{"c1", "c2"} {
		h.repo.files[commit+":"+h.round.Settings.Brief] = true
	}
	first := h.round.Run(context.Background(), Options{})
	if first.Status != ExitClear || first.Record == nil {
		t.Fatalf("first round: %+v", first)
	}
	second := h.round.Run(context.Background(), Options{Revision: "c2"})
	if second.Status != ExitClear || second.Record == nil || second.Record.Follows != first.Record.Round || second.Record.ContentPolicy != first.Record.ContentPolicy {
		t.Fatalf("unchanged brief did not continue chain: %+v", second)
	}

	legacy := newHarness(t)
	legacy.round.Settings.Brief = h.round.Settings.Brief
	legacy.repo.files["c2:"+legacy.round.Settings.Brief] = true
	legacyRecord := RoundRecord{Task: "feature/x", Round: 1, Attempt: 1, Chain: 1, Base: "c0", Commit: "c1",
		Policy: legacy.round.Settings.policyID(), Runs: []ReviewerRun{{Reviewer: "codex", Outcome: codex.OutcomeDone}}}
	if err := writeJSON(filepath.Join(legacy.round.Store.TaskPath("feature/x"), "round-1", RecordName), legacyRecord); err != nil {
		t.Fatal(err)
	}
	if result := legacy.round.Run(context.Background(), Options{Revision: "c2"}); result.Status != ExitUsage || !strings.Contains(result.Next, "--fresh") {
		t.Fatalf("legacy round without content digest continued chain: %+v", result)
	}
}
func (repo *fakeRepo) Exists(_ context.Context, commit, path string) bool {
	return repo.files[commit+":"+path]
}
func (repo *fakeRepo) BuildSource(_ context.Context, path string, _ sourceview.Revisions) error {
	return os.MkdirAll(path, 0o700)
}
func (repo *fakeRepo) RemoveTree(_ context.Context, path string) error        { return os.RemoveAll(path) }
func (repo *fakeRepo) LockedTrees(context.Context) (map[string]string, error) { return nil, nil }

// scripted is a fake reviewer: each call takes the next answer (or outcome) of the script.
type scripted struct {
	mutex   sync.Mutex
	answers []any // Answer, or a string outcome
	prompts []string
}

func (fake *scripted) run(_ context.Context, request codex.Request) codex.Result {
	fake.mutex.Lock()
	next := fake.answers[0]
	fake.answers = fake.answers[1:]
	fake.prompts = append(fake.prompts, string(request.Prompt))
	fake.mutex.Unlock()
	request.Answered()
	result := codex.Result{Argv: []string{"codex"}, Started: time.Now(), Ended: time.Now(), AnswerPath: filepath.Join(request.Folder, "answer.json")}
	if outcome, ok := next.(string); ok {
		result.Outcome, result.Reason, result.Exit = outcome, "scripted "+outcome, 1
		return result
	}
	answer := next.(Answer)
	if answer.Findings == nil {
		answer.Findings = []AnswerFinding{}
	}
	if answer.Prior == nil {
		answer.Prior = []PriorAnswer{}
	}
	for index := range answer.Findings {
		if answer.Findings[index].Repeats == nil {
			answer.Findings[index].Repeats = []string{}
		}
	}
	data, _ := json.Marshal(answer)
	_ = os.WriteFile(result.AnswerPath, data, 0o600)
	result.Outcome, result.Exit = codex.OutcomeDone, 0
	return result
}

type harness struct {
	round    Round
	repo     *fakeRepo
	reviewer *scripted
	logins   []string
}

func newHarness(t *testing.T, answers ...any) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{repo: newFakeRepo(), reviewer: &scripted{answers: answers}}
	settings := testSettings()
	h.round = Round{Settings: settings, Repo: h.repo, Store: Store{Root: filepath.Join(dir, "store")},
		Machine: Machine{
			RunReviewer:      h.reviewer.run,
			Probe:            func(context.Context, string, string) (string, error) { return "codex-cli 0.0.0", nil },
			CheckEnvironment: func([]ReviewerSettings) string { return "" },
			RecordLogin:      func(reason string) { h.logins = append(h.logins, reason) },
			SlotsDir:         filepath.Join(dir, "slots"), StartLockPath: filepath.Join(dir, "start.lock"),
			Now: time.Now, TreeAlive: func(string) bool { return true }, SlotPoll: 10 * time.Millisecond,
		},
		Out: &bytes.Buffer{}, Errors: &bytes.Buffer{}}
	return h
}

func blocker(id, place string, repeats ...string) AnswerFinding {
	return finding(id, place, "blocker", "ordinary", "severe", repeats...)
}

func TestReviewRequiresASelectionAndAcceptsEitherBuiltIn(t *testing.T) {
	for _, name := range []string{"codex", "claude-code"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, Answer{Verdict: "approve", Summary: "clear"})
			h.round.Settings.Use = nil
			ctx := context.Background()
			if result := h.round.Run(ctx, Options{}); result.Status != ExitUsage || !strings.Contains(result.Next, "choose a reviewer") {
				t.Fatalf("empty review: %+v", result)
			}
			options := Options{Reviewers: []string{name}, Effort: "minimal"}
			if name == "claude-code" {
				options.Effort = "max"
			}
			result := h.round.Run(ctx, options)
			if result.Status != ExitClear || result.Record == nil || !sameReviewers(result.Record.Runs, []string{name}) {
				t.Fatalf("selected %s: %+v", name, result)
			}
			if reasons, _ := CheckReasons(h.round.Settings, h.round.Store, "feature/x", "c0", "c1"); len(reasons) != 0 {
				t.Fatalf("explicit review should count without required reviewers: %v", reasons)
			}
		})
	}
}

func TestRoundsOfATask(t *testing.T) {
	h := newHarness(t,
		Answer{Verdict: "changes", Summary: "one", Findings: []AnswerFinding{blocker("F1", "a.go: run")}},
		Answer{Verdict: "approve", Summary: "two", Prior: []PriorAnswer{{ID: "r1-codex-F1", Status: "fixed"}}},
	)
	ctx := context.Background()
	first := h.round.Run(ctx, Options{})
	if first.Status != ExitBlocks || first.Record == nil || first.Record.Round != 1 || first.Record.Base != "c0" || first.Record.Commit != "c1" {
		t.Fatalf("round 1: %+v", first)
	}
	if !strings.Contains(h.round.Out.(*bytes.Buffer).String(), "jst bg --for 37") {
		t.Fatalf("budget line: %s", h.round.Out)
	}
	if _, err := os.Stat(filepath.Join(first.Folder, "source")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary source view was left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(first.Folder, "report.md")); err != nil {
		t.Fatal(err)
	}
	review := h.round.Machine.RunReviewer
	h.round.Machine.RunReviewer = func(ctx context.Context, request codex.Request) codex.Result {
		data, err := os.ReadFile(filepath.Join(request.Tree, "open-findings.json"))
		var findings []LedgerFinding
		if err != nil || json.Unmarshal(data, &findings) != nil || len(findings) != 1 || findings[0].ID != "r1-codex-F1" || findings[0].Detail == "" {
			t.Errorf("the incremental reviewer could not read the full prior finding: %s, %v", data, err)
		}
		return review(ctx, request)
	}
	second := h.round.Run(ctx, Options{Revision: "c2"})
	if second.Status != ExitClear || second.Record.Follows != 1 || second.Record.Base != "c0" {
		t.Fatalf("round 2: %+v", second)
	}
	if prompt := h.reviewer.prompts[1]; !strings.Contains(prompt, "changes/previous.patch") || !strings.Contains(prompt, "r1-codex-F1") {
		t.Fatalf("delta prompt: %s", prompt)
	}
	reasons, _ := CheckReasons(h.round.Settings, h.round.Store, "feature/x", "c0", "c2")
	if len(reasons) != 0 {
		t.Fatal(reasons)
	}
	if reasons, _ := CheckReasons(h.round.Settings, h.round.Store, "feature/x", "c1", "c2"); len(reasons) != 1 {
		t.Fatalf("another base must not pass: %v", reasons)
	}
	if reasons, _ := CheckReasons(h.round.Settings, h.round.Store, "feature/x", "c0", "c3"); len(reasons) != 1 {
		t.Fatalf("another commit must not pass: %v", reasons)
	}
	lines, _, _ := h.round.Store.ReadIndex()
	if len(lines) != 2 {
		t.Fatalf("index: %+v", lines)
	}
}

func TestAChainKeepsItsBase(t *testing.T) {
	h := newHarness(t, Answer{Verdict: "approve"}, Answer{Verdict: "approve"})
	ctx := context.Background()
	if result := h.round.Run(ctx, Options{Revision: "c0..c1"}); result.Status != ExitClear {
		t.Fatalf("%+v", result)
	}
	if result := h.round.Run(ctx, Options{Revision: "other..c2"}); result.Status != ExitUsage || !strings.Contains(result.Next, "--fresh") {
		t.Fatalf("%+v", result)
	}
	result := h.round.Run(ctx, Options{Revision: "c1..c2", Fresh: true})
	if result.Status != ExitClear || result.Record.Chain != 2 || result.Record.Base != "c1" || result.Record.Follows != 0 {
		t.Fatalf("%+v", result)
	}
}

func TestIncompleteReviewCannotCertifyThePinnedRange(t *testing.T) {
	h := newHarness(t,
		Answer{Verdict: "incomplete", Summary: "required source file unavailable"},
		Answer{Verdict: "changes", Summary: "could not read the source"},
		Answer{Verdict: "approve", Summary: "clear"},
	)
	ctx := context.Background()
	for _, want := range []struct{ outcome, diagnostic string }{
		{OutcomeIncomplete, "required source file unavailable"},
		{OutcomeInconsistent, "a changes verdict must identify a new or still-open finding"},
	} {
		result := h.round.Run(ctx, Options{})
		if result.Status != ExitReviewer || result.Record != nil {
			t.Fatalf("unusable review became a round: %+v", result)
		}
		report, err := os.ReadFile(filepath.Join(result.Folder, "report.md"))
		if err != nil || !strings.Contains(string(report), want.outcome) || !strings.Contains(string(report), want.diagnostic) {
			t.Fatalf("missing failure evidence: %s, %v", report, err)
		}
		if reasons, _ := CheckReasons(h.round.Settings, h.round.Store, "feature/x", "c0", "c1"); len(reasons) == 0 {
			t.Fatal("an unusable review certified the pinned range")
		}
	}
	if result := h.round.Run(ctx, Options{}); result.Status != ExitClear || result.Record == nil || result.Record.Round != 1 || result.Record.Attempt != 3 {
		t.Fatalf("usable retry did not publish round 1: %+v", result)
	}
}

func TestFailedAttemptTakesNoNumber(t *testing.T) {
	h := newHarness(t, codex.OutcomeTimeout, Answer{Verdict: "approve"})
	ctx := context.Background()
	failed := h.round.Run(ctx, Options{})
	if failed.Status != ExitReviewer || failed.Record != nil || filepath.Base(failed.Folder) != "attempt-1" {
		t.Fatalf("%+v", failed)
	}
	if reasons, notes := CheckReasons(h.round.Settings, h.round.Store, "feature/x", "c0", "c1"); len(reasons) == 0 || len(notes) != 0 {
		t.Fatal(reasons, notes)
	}
	next := h.round.Run(ctx, Options{})
	if next.Status != ExitClear || next.Record.Round != 1 || next.Record.Attempt != 2 {
		t.Fatalf("%+v", next)
	}
	if err := h.round.Store.Rebuild(); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := h.round.Store.ReadIndex()
	if len(lines) != 2 || lines[0].Outcome != codex.OutcomeTimeout || lines[0].Commit != "c1" || lines[0].Base != "c0" {
		t.Fatalf("a rebuilt index keeps a failed attempt's commit and base: %+v", lines)
	}
}

func TestUnparsableAndInconsistent(t *testing.T) {
	h := newHarness(t, Answer{Verdict: "maybe"})
	if result := h.round.Run(context.Background(), Options{}); result.Status != ExitReviewer ||
		!strings.Contains(result.Next, "inconsistent") {
		t.Fatalf("%+v", result)
	}
}

func TestLoginFailureIsRecorded(t *testing.T) {
	h := newHarness(t, codex.OutcomeLogin)
	if result := h.round.Run(context.Background(), Options{}); result.Status != ExitEnvironment || len(h.logins) != 1 {
		t.Fatalf("%+v %v", result, h.logins)
	}
}

func TestEnvironmentRefusesFirst(t *testing.T) {
	h := newHarness(t)
	h.round.Machine.CheckEnvironment = func([]ReviewerSettings) string { return "inside a sandbox" }
	if result := h.round.Run(context.Background(), Options{}); result.Status != ExitEnvironment || result.Next != "inside a sandbox" {
		t.Fatalf("%+v", result)
	}
}

func TestTaskBusy(t *testing.T) {
	h := newHarness(t)
	held, err := flock.TryFile(h.round.Store.TaskLockPath("feature/x"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if result := h.round.Run(context.Background(), Options{}); result.Status != ExitUsage || !strings.Contains(result.Next, "busy") {
		t.Fatalf("%+v", result)
	}
}

func TestRedesignAndTheLimit(t *testing.T) {
	h := newHarness(t,
		Answer{Verdict: "changes", Findings: []AnswerFinding{blocker("F1", "a.go: run")}},
		Answer{Verdict: "changes", Prior: []PriorAnswer{{ID: "r1-codex-F1", Status: "fixed"}}, Findings: []AnswerFinding{blocker("F1", "a.go: run")}},
		Answer{Verdict: "changes", Prior: []PriorAnswer{{ID: "r2-codex-F1", Status: "fixed"}}, Findings: []AnswerFinding{blocker("F1", "b.go: other")}},
	)
	h.round.Settings.RoundLimit = 3
	ctx := context.Background()
	h.round.Run(ctx, Options{})
	second := h.round.Run(ctx, Options{Revision: "c2"})
	if second.Status != ExitRedesign || len(second.Record.Outcome.RedesignDue) != 1 {
		t.Fatalf("%+v", second)
	}
	third := h.round.Run(ctx, Options{Revision: "c3"})
	if third.Status != ExitOwner {
		t.Fatalf("%+v", third)
	}
	refused := h.round.Run(ctx, Options{Revision: "c3"})
	if refused.Status != ExitOwner || refused.Record != nil {
		t.Fatalf("%+v", refused)
	}
}

func TestReviewersRunTogether(t *testing.T) {
	h := newHarness(t, Answer{Verdict: "approve"}, Answer{Verdict: "approve"})
	s := h.round.Settings
	s.Reviewers = map[string]ReviewerSettings{"codex": s.Reviewers["codex"], "second": s.Reviewers["codex"]}
	h.round.Settings = s
	// "second" is not a known reviewer by name in jackstay.json, but a round runs whatever settings hold.
	result := h.round.Run(context.Background(), Options{Reviewers: []string{"codex", "second"}})
	if result.Status != ExitClear || len(result.Record.Runs) != 2 {
		t.Fatalf("%+v", result)
	}
}

func TestSlotsWaitAndGiveUp(t *testing.T) {
	h := newHarness(t, Answer{Verdict: "approve"})
	h.round.Settings.MaxConcurrent = 1
	h.round.Settings.QueueMinutes = 1
	held, err := flock.TryFile(filepath.Join(h.round.Machine.SlotsDir, "0.lock"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = h.round.takeSlot(ctx, 1)
	if err == nil {
		t.Fatal("took a slot that was held")
	}
	held.Release()
	slot, err := h.round.takeSlot(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	slot.Release()
	if !strings.Contains(h.round.Errors.(*bytes.Buffer).String(), "waiting") {
		t.Fatal("a waiting run says so")
	}
}

func TestCheckRequiresWholeChainToHaveTheCurrentPolicyAndReviewers(t *testing.T) {
	h := newHarness(t, Answer{Verdict: "approve"}, Answer{Verdict: "approve"}, Answer{Verdict: "approve"})
	settings := h.round.Settings
	settings.Reviewers = map[string]ReviewerSettings{"codex": settings.Reviewers["codex"], "second": settings.Reviewers["codex"]}
	settings.Use = []string{"codex", "second"}
	h.round.Settings = settings
	ctx := context.Background()
	task := "feature/x"
	first := h.round.Run(ctx, Options{Reviewers: []string{"codex"}})
	if first.Status != ExitClear || first.Record == nil {
		t.Fatalf("%+v", first)
	}
	reasons, _ := CheckReasons(settings, h.round.Store, task, "c0", "c1")
	if len(reasons) != 1 || !strings.Contains(reasons[0], `required reviewer "second"`) {
		t.Fatalf("unexpected verdict: %v", reasons)
	}
	if changed := h.round.Run(ctx, Options{Revision: "c2"}); changed.Status != ExitUsage || !strings.Contains(changed.Next, "--fresh") {
		t.Fatalf("changed reviewer set continued the chain: %+v", changed)
	}
	fresh := h.round.Run(ctx, Options{Revision: "c0..c2", Fresh: true})
	if fresh.Status != ExitClear || fresh.Record.Follows != 0 {
		t.Fatalf("%+v", fresh)
	}
	if reasons, _ := CheckReasons(settings, h.round.Store, task, "c0", "c2"); len(reasons) != 0 {
		t.Fatalf("fresh round did not pass: %v", reasons)
	}
	reconfigured := settings
	reconfigured.Triage = !settings.Triage
	if reasons, _ := CheckReasons(reconfigured, h.round.Store, task, "c0", "c2"); len(reasons) != 1 || !strings.Contains(reasons[0], "review policy changed") {
		t.Fatalf("changed policy passed: %v", reasons)
	}
	h.round.Settings = reconfigured
	if changed := h.round.Run(ctx, Options{Revision: "c3"}); changed.Status != ExitUsage || !strings.Contains(changed.Next, "--fresh") {
		t.Fatalf("changed policy continued the chain: %+v", changed)
	}
}

func TestLegacyRoundCanBeReadButDoesNotCertifyCurrentPolicy(t *testing.T) {
	h := newHarness(t)
	task := "feature/x"
	legacy := filepath.Join(h.round.Store.Root, "feature%2Fx", "round-1")
	record := RoundRecord{Task: task, Round: 1, Attempt: 1, Base: "c0", Commit: "c1",
		Runs: []ReviewerRun{{Reviewer: "codex", Outcome: codex.OutcomeDone}}}
	if err := writeJSON(filepath.Join(legacy, RecordName), record); err != nil {
		t.Fatal(err)
	}
	reasons, _ := CheckReasons(h.round.Settings, h.round.Store, task, "c0", "c1")
	if len(reasons) != 1 || !strings.Contains(reasons[0], "no recorded review policy") {
		t.Fatalf("legacy round was not read or was accepted: %v", reasons)
	}
}

func TestInaccessibleReviewLocksAreReportedAsIOFailures(t *testing.T) {
	for _, lock := range []string{"slot", "start"} {
		t.Run(lock, func(t *testing.T) {
			h := newHarness(t)
			var path, message string
			if lock == "slot" {
				path, message = h.round.Machine.SlotsDir, "cannot acquire a review slot"
			} else {
				path, message = h.round.Machine.StartLockPath, "cannot acquire the start lock"
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if lock == "slot" {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			// Already past the queue deadline, so an implementation that mistakenly
			// treats the I/O error as contention reports immediately, too.
			h.round.Machine.Now = func() time.Time { return time.Unix(0, 0) }
			result := h.round.Run(context.Background(), Options{})
			if result.Status != ExitReviewer || !strings.Contains(result.Next, message) {
				t.Fatalf("%+v", result)
			}
		})
	}
}

func TestPromptReferencesBriefWithoutEmbeddingItsContents(t *testing.T) {
	h := newHarness(t, Answer{Verdict: "approve", Summary: "clear"})
	brief := filepath.Join(t.TempDir(), "brief.txt")
	if err := os.WriteFile(brief, []byte("explicit brief fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.round.Settings.Brief = "AGENTS.md"
	review := h.round.Machine.RunReviewer
	h.round.Machine.RunReviewer = func(ctx context.Context, request codex.Request) codex.Result {
		body, err := os.ReadFile(filepath.Join(request.Tree, "review-brief.md"))
		if err != nil || string(body) != "explicit brief fixture" || !request.PinnedSource {
			t.Errorf("the explicit brief was not available in the pinned source view: %v", err)
		}
		prompt := string(request.Prompt)
		if !strings.Contains(prompt, "head/AGENTS.md") || !strings.Contains(prompt, "review-brief.md") ||
			strings.Contains(prompt, "explicit brief fixture") || strings.Contains(prompt, "text of AGENTS.md") {
			t.Errorf("the initial prompt did not use brief references: %s", prompt)
		}
		return review(ctx, request)
	}
	if result := h.round.Run(context.Background(), Options{BriefFile: brief}); result.Status != ExitClear {
		t.Fatalf("review failed: %+v", result)
	}
}

func TestTheKeptArgvNamesMCPServersWithoutTheirArguments(t *testing.T) {
	argv := redactedArgv([]string{"codex", "-c", `mcp_servers.docs={command="docs",args=["--token","made-up"]}`, "-c", "mcp_servers={}",
		"--mcp-config", `{"mcpServers":{"docs":{"args":["--token","made-up"]}}}`})
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "made-up") || !strings.Contains(joined, "mcp_servers.docs=<redacted>") || !strings.Contains(joined, "mcp_servers={}") {
		t.Fatalf("argv: %v", argv)
	}
	if empty := redactedArgv([]string{"--mcp-config", `{"mcpServers":{}}`}); empty[1] != `{"mcpServers":{}}` {
		t.Fatalf("the empty configuration was hidden: %v", empty)
	}
}
