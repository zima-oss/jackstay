package baseline

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestRunsTimesMergesAndDestructiveCommands(t *testing.T) {
	found := newFixture(t, map[string]string{"Add a thing (#1)": "2026-11-08T12:00:00Z", "Add another (#2)": "2026-11-08T13:00:00Z",
		"a note": "2026-11-08T14:00:00Z", "Old (#0)": "2026-10-01T12:00:00Z"})
	found.repo.Git("tag", "v1", found.repo.Git("rev-list", "--max-parents=0", "HEAD")) // tagged before the span
	t.Setenv("GIT_COMMITTER_DATE", "2026-11-09T00:00:00Z")
	found.repo.Git("tag", "-a", "v2", "-m", "in the span")
	start := at(t, "2026-11-08T10:00:00Z")
	records := ran("t1", start, 300, "make test")
	records = append(records, map[string]any{"type": "api-request-blob", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "make test"}}}}})
	records = append(records, use("t2", start+3600, "make test", true),
		answer("t2", start+3601, false, "", map[string]any{"toolUseResult": map[string]any{"backgroundTaskId": "b1"}}),
		notified("t2", start+4200, "completed", false),
		use("t3", start+7200, "make test", false), // never answered
		use("t4", start+9000, "git reset --hard && rm -rf build", false), answer("t4", start+9001, false, "", nil),
		use("t5", start+9100, "grep 'git reset --hard' AGENTS.md", false),
		use("t6", start-30*day, "make test", false)) // before the span
	writeLines(t, filepath.Join(found.projects, "proj-main", "a.jsonl"), records, "not json\n[1, 2]\n")
	writeLines(t, filepath.Join(found.projects, "proj-wt-1", "s", "subagents", "b.jsonl"), ran("w1", start+day, 900, "make test"), "")
	writeLines(t, filepath.Join(found.projects, "proj-mainly", "c.jsonl"), ran("o1", start, 600, "make test"), "") // another project

	report := found.measure(t, found.spec("proj"))
	whole := report.Kinds["whole"]
	if whole.Attempts != 4 || whole.Started != 4 || whole.Timed != 3 || value(t, whole.WallMedian) != 600 ||
		value(t, whole.StartedPerMerge) != 2.0 || whole.Gate == nil || !*whole.Gate {
		t.Errorf("whole: %+v", whole)
	}
	if got := report.Merges; got.Unit != "pr" || value(t, got.Count) != 2 || value(t, got.PullRequests) != 2 ||
		value(t, got.Commits) != 3 || value(t, got.Tags) != 1 {
		t.Errorf("merges: %+v", got)
	}
	if !reflect.DeepEqual(report.Destructive.Ran, map[string]int{"git-reset-hard": 1, "rm-recursive": 1}) ||
		report.Destructive.Calls != (Fates{Ran: 1}) || report.Headline.DestructiveRan != 2 {
		t.Errorf("destructive: %+v", report.Destructive)
	}
	if !reflect.DeepEqual(report.BashCallsByFate, map[string]int{"ran": 4, "refused": 0, "denied": 0, "unanswered": 2}) || report.BashCalls != 6 {
		t.Errorf("calls: %d %v", report.BashCalls, report.BashCallsByFate)
	}
	days := slices.Sorted(func(yield func(string) bool) {
		for name := range report.Days {
			if !yield(name) {
				return
			}
		}
	})
	if len(days) != 7 || days[0] != since || days[6] != until {
		t.Errorf("days: %v", days)
	}
	first, second := report.Days["2026-11-08"], report.Days["2026-11-09"]
	if first.Kinds["whole"].Started != 3 || first.Merges != 2 || first.Destructive != (Fates{Ran: 2}) ||
		value(t, second.Kinds["whole"].WallMedian) != 900 || second.Merges != 0 || second.Kinds["whole"].StartedPerMerge != nil {
		t.Errorf("per day: %+v %+v", first, second)
	}
	if report.JackstayLog.Records != 0 || report.Headline.ExecutedByJackstayLog != nil || report.Headline.Per != "pull request" {
		t.Errorf("headline: %+v", report.Headline)
	}
	perCommit := found.spec("proj")
	perCommit.MergeUnit = "commit"
	if got := found.measure(t, perCommit).Headline; got.Per != "commit on main" || value(t, got.GateRunsStartedPerMerge) != 1.33 {
		t.Errorf("per commit: %+v", got)
	}
	// Without git's answer the counts stand, and merges are not known.
	broken := found.spec("proj")
	broken.Branch = "no-such-branch"
	if got := found.measure(t, broken); got.Merges.Count != nil || got.Merges.Unit != "commit" || got.Kinds["whole"].StartedPerMerge != nil {
		t.Errorf("no git answer: %+v", got.Merges)
	}
}

func TestARefusedOrDeniedCallDidNotRun(t *testing.T) {
	found := newFixture(t, map[string]string{"One (#1)": "2026-11-08T12:00:00Z"})
	reject := map[string]any{"decision": "reject", "source": "hook", "reasonType": "hook"}
	refusal := "jst guard refused this command; nothing was run."
	start := at(t, "2026-11-08T10:00:00Z")
	records := []map[string]any{
		use("r1", start, "git reset --hard", false), answer("r1", start+0.2, true, refusal, map[string]any{"permissionDecision": reject}),
		use("r2", start+60, "git stash drop", false), answer("r2", start+60.2, true, refusal, nil),
		use("r3", start+120, "git clean -fd", false), answer("r3", start+120.2, true, "PreToolUse:Bash hook error: refused", nil),
		use("d1", start+180, "git reset --hard HEAD~1", false), answer("d1", start+180.2, true, "Permission for this action was denied.",
			map[string]any{"toolDenialKind": "automode-blocked", "permissionDecision": map[string]any{"decision": "reject", "source": "config"}}),
		use("d2", start+240, "git reset --hard HEAD~2", false), answer("d2", start+240.2, true, "The user doesn't want to proceed with this tool use.", nil),
		use("k1", start+300, "git reset --hard", false), answer("k1", start+301, false, "HEAD is now at 1234567", nil),
		use("k2", start+360, "git stash drop", false), answer("k2", start+361, true, "Exit code 1\nNo stash entries found.", nil),
		use("k3", start+420, "git reset --hard; cat guard.log", false), answer("k3", start+421, true, "Exit code 2\n"+refusal,
			map[string]any{"permissionDecision": map[string]any{"decision": "accept"}}),
		use("k4", start+480, "git reset --hard", false),
		use("s1", start+3600, "make test", false), answer("s1", start+3630, true, refusal, map[string]any{"permissionDecision": reject}),
		use("s2", start+4200, "make test", false), answer("s2", start+4230, true, "x", map[string]any{"toolDenialKind": "automode-unavailable"}),
		use("s3", start+4800, "make test", false), answer("s3", start+4920, false, "", nil),
	}
	writeLines(t, filepath.Join(found.projects, "proj-main", "a.jsonl"), records, "")
	report := found.measure(t, found.spec("proj"))
	harm := report.Destructive
	if !reflect.DeepEqual(harm.Refused, map[string]int{"git-clean-force": 1, "git-reset-hard": 1, "git-stash-drop": 1}) ||
		!reflect.DeepEqual(harm.Denied, map[string]int{"git-reset-hard": 2}) ||
		!reflect.DeepEqual(harm.Ran, map[string]int{"git-reset-hard": 3, "git-stash-drop": 1}) ||
		harm.Calls != (Fates{Ran: 4, Refused: 3, Denied: 2}) || harm.RanTotal != 4 || harm.RefusedTotal != 3 || harm.DeniedTotal != 2 {
		t.Errorf("destructive: %+v", harm)
	}
	whole := report.Kinds["whole"]
	if whole.Attempts != 3 || whole.Refused != 1 || whole.Denied != 1 || whole.Started != 1 || whole.Timed != 1 || value(t, whole.WallMedian) != 120 {
		t.Errorf("whole: %+v", whole)
	}
}

func TestABackgroundRunEndsAtItsNotification(t *testing.T) {
	found := newFixture(t, map[string]string{"one": "2026-11-08T12:00:00Z", "two": "2026-11-08T13:00:00Z"})
	start := at(t, "2026-11-08T10:00:00Z")
	moved := func(id string) map[string]any {
		return map[string]any{"toolUseResult": map[string]any{"backgroundTaskId": id}}
	}
	writeLines(t, filepath.Join(found.projects, "proj-main", "a.jsonl"), []map[string]any{
		use("b1", start, "just gate", true), answer("b1", start+2, false, "", moved("t1")),
		use("x1", start+300, "git status", false), answer("x1", start+301, false, "", nil),
		notified("b1", start+1200, "completed", false),
		// The notice arrives as a user message, and says the command failed.
		use("b2", start+3600, "just gate", true), answer("b2", start+3602, false, "", moved("t2")),
		notified("b2", start+4200, "failed", true),
		// A foreground call moved to the background when its time was up; a notice told twice.
		use("b3", start+7200, "just gate", false), answer("b3", start+7800, false, "", moved("t3")),
		notified("b3", start+9000, "completed", false), notified("b3", start+10740, "completed", false),
		// Started in the background and never heard of again; a notice for a call of no transcript here.
		use("b4", start+10800, "just gate", true), answer("b4", start+10802, false, "", moved("t4")),
		notified("zz", start+12600, "completed", false),
	}, "")
	gate := found.measure(t, found.spec("proj")).Kinds["gate"]
	if gate.Started != 4 || gate.Timed != 3 || gate.Failed != 1 || value(t, gate.WallMedian) != 1200 || value(t, gate.WallP90) != 1800 {
		t.Errorf("gate: %+v", gate)
	}
}

func TestAToolUseIDSeenAgainIsOneCall(t *testing.T) {
	found := newFixture(t, map[string]string{"One (#1)": "2026-11-08T12:00:00Z"})
	start := at(t, "2026-11-08T10:00:00Z")
	first := append(ran("t1", start, 300, "make test"), ran("t2", start+3600, 1, "git reset --hard")...)
	writeLines(t, filepath.Join(found.projects, "proj-main", "a.jsonl"), append(slices.Clone(first), first[0], first[0]), "")
	writeLines(t, filepath.Join(found.projects, "proj-main", "b.jsonl"), append(slices.Clone(first), use("t3", start+7200, "make test", false),
		answer("t3", start+7800, false, "", nil), answer("t3", start+10200, false, "", nil)), "")
	// A copy whose result comes before its call in reading order is still paired.
	writeLines(t, filepath.Join(found.projects, "proj-wt-w", "c.jsonl"), append([]map[string]any{answer("t4", start+10920, false, "", nil),
		use("t4", start+10800, "make test", false)}, first...), "")
	report := found.measure(t, found.spec("proj"))
	whole := report.Kinds["whole"]
	if report.BashCalls != 4 || whole.Attempts != 3 || whole.Timed != 3 || whole.WallHours != rounded(1020.0/3600, 1) ||
		!reflect.DeepEqual(report.Destructive.Ran, map[string]int{"git-reset-hard": 1}) {
		t.Errorf("report: %d %+v %v", report.BashCalls, whole, report.Destructive.Ran)
	}
}

func TestGateRunsThatSharedWallTime(t *testing.T) {
	found := newFixture(t, map[string]string{"One (#1)": "2026-11-08T12:00:00Z"})
	clock := func(text string) float64 { return at(t, text) }
	var mine []map[string]any
	for _, run := range []struct {
		id, start string
		took      float64
		command   string
	}{
		{"a1", "2026-11-08T10:00:00Z", 600, "make test"},  // overlaps a2 and the other project's g1
		{"a2", "2026-11-08T10:05:00Z", 1200, "make test"}, // overlaps a1 and g1
		{"a3", "2026-11-08T12:00:00Z", 300, "make test"},  // alone
		{"a4", "2026-11-08T12:05:02Z", 178, "make test"},  // starts as a3 ends: alone
		{"a5", "2026-11-08T14:00:00Z", 1800, "swift test --filter A"},
		{"a6", "2026-11-08T14:10:00Z", 120, "make test"}, // beside a focused run only: alone
		{"a8", "2026-11-07T00:01:00Z", 120, "make test"}, // overlaps a9, of the day before the span
		{"a9", "2026-11-06T23:59:00Z", 180, "make test"},
	} {
		mine = append(mine, ran(run.id, clock(run.start), run.took, run.command)...)
	}
	mine = append(mine, use("a7", clock("2026-11-08T16:00:00Z"), "make test", false)) // no end: not looked at
	writeLines(t, filepath.Join(found.projects, "proj-main", "a.jsonl"), mine, "")
	writeLines(t, filepath.Join(found.projects, "other-main", "g.jsonl"), append(ran("g1", clock("2026-11-08T10:08:00Z"), 720, "just gate"),
		ran("g2", clock("2026-11-08T12:06:00Z"), 60, "just check-docs")...), "")
	other := found.spec("other")
	report := found.measure(t, found.spec("proj"), other)
	want := Overlap{Timed: 6, Overlapped: 3, OverlappedSame: 3, OverlappedOther: 2, NotOverlapped: 3,
		MedianOverlapped: intPointer(600), MedianNotOverlapped: intPointer(178)}
	if !reflect.DeepEqual(report.Overlap.With, []string{"proj", "other"}) || !reflect.DeepEqual(report.Overlap.Kinds["whole"], want) {
		t.Errorf("overlap: %v %+v", report.Overlap.With, report.Overlap.Kinds["whole"])
	}
	if got := report.Days["2026-11-07"].Overlap["whole"].Overlapped; got != 1 {
		t.Errorf("the first day: %d", got)
	}
	if got := found.measure(t, found.spec("proj")).Overlap.Kinds["whole"].OverlappedOther; got != 0 {
		t.Errorf("without --also: %d", got)
	}
}
