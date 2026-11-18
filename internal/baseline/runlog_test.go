package baseline

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestWhatExecutedAndWhatWasReusedComeFromTheRunLog(t *testing.T) {
	found := newFixture(t, map[string]string{"One (#1)": "2026-11-08T12:00:00Z"})
	start := at(t, "2026-11-08T10:00:00Z")
	row := func(event string, epoch float64, more map[string]any) map[string]any {
		record := map[string]any{"event": event, "project": "proj", "command": []any{"make", "test"}, "epoch": epoch}
		for key, value := range more {
			record[key] = value
		}
		return record
	}
	writeLines(t, found.log, []map[string]any{
		row("run", start, map[string]any{"outcome": "passed", "seconds": 300.0}),
		row("run", start+1000, map[string]any{"outcome": "failed", "seconds": 100.0}),
		row("run", start+2000, map[string]any{"outcome": "passed", "seconds": 320.0}),
		row("run", start+3000, map[string]any{"outcome": "passed", "seconds": 90.5, "command": []any{"make", "test-changed"}, "scope": "changed"}),
		row("run", start+4000, map[string]any{"outcome": "gave-up"}), // nothing ran
		row("reused", start+5000, nil),
		row("reused", start+6000, nil),
		row("would-hit", start+7000, nil),
		row("run", start+8000, map[string]any{"outcome": "passed", "seconds": 7, "command": []any{"npm", "run", "x y"}}),
		{"event": "run", "project": "other", "command": []any{"just", "gate"}, "outcome": "passed", "seconds": 1200.0, "epoch": start - 200},
		row("run", start-30*day, map[string]any{"outcome": "passed", "seconds": 999.0}), // before the span
		{"event": "run", "project": "proj", "outcome": "passed", "seconds": 999.0},      // no time
		row("run", start+9000, map[string]any{"outcome": "passed", "seconds": 50.0, "inside": "r0"}),
	}, "not json\n")
	writeLines(t, filepath.Join(found.projects, "proj-main", "a.jsonl"), nil, "")
	log := found.measure(t, found.spec("proj")).JackstayLog
	if log.Records != 9 || log.Executed != 5 || log.Reused != 2 || log.WouldHit != 1 || log.GaveUp != 1 ||
		!reflect.DeepEqual(log.ByOutcome, map[string]int{"passed": 4, "failed": 1}) || log.Path != found.log {
		t.Errorf("log: %+v", log)
	}
	// The first run (300 s, ending at start) executed while the other project's did; the next ones
	// ran one after another: a run is not beside itself.
	if log.ExecutedOverlapped != 1 {
		t.Errorf("overlapped: %d", log.ExecutedOverlapped)
	}
	want := LogKind{Executed: 3, Passed: 2, Reused: 2, WouldHit: 1, RunAloneMedian: intPointer(300), RunAloneHours: 0.2}
	if !reflect.DeepEqual(log.Kinds["whole"], want) || value(t, log.Kinds["changed"].RunAloneMedian) != 90 || log.Kinds["(no kind)"].Executed != 1 {
		t.Errorf("kinds: %+v", log.Kinds)
	}
	if got := log.Scopes["changed"]; got.Executed != 1 || got.Passed != 1 || value(t, got.Median) != 90 {
		t.Errorf("scopes: %+v", log.Scopes)
	}
}

func TestFailuresAreSplitByWhetherAnotherRunWasGoing(t *testing.T) {
	found := newFixture(t, map[string]string{"One (#1)": "2026-11-08T12:00:00Z"})
	base := at(t, "2026-11-08T10:00:00Z")
	row := func(project string, end, seconds float64, outcome string, key any) map[string]any {
		return map[string]any{"epoch": base + end, "event": "run", "project": project, "command": []any{"make", "test"}, "outcome": outcome,
			"seconds": seconds, "key": key}
	}
	timed := func(record map[string]any, begin, end float64) map[string]any {
		record["started_epoch"], record["ended_epoch"] = base+begin, base+end
		return record
	}
	writeLines(t, found.log, []map[string]any{
		row("widgets", 1000, 900, "passed", "g1"), // 100..1000
		row("proj", 600, 300, "failed", "k1"),     // beside the other project's run
		row("proj", 1500, 300, "passed", "k1"),    // the same tree passes: not the tree's failure
		row("proj", 2000, 300, "failed", "k2"),    // alone, and the tree changed after it
		row("proj", 2500, 300, "passed", "k3"),
		row("proj", 3000, 300, "failed", nil), // no key: cannot be called unchanged
		timed(row("proj", 3400, 300, "stopped", "k4"), 3100, 3400),
		timed(row("proj", 3900, 300, "passed", "k4"), 3200, 3500), // began before the stop: not a retry
		row("proj", 3*3600+5000, 300, "passed", "k2"),             // more than two hours later
		row("proj", 4*3600+5000, 60, "failed", "k5"),              // alone
		{"epoch": base + 4*3600 + 5100, "event": "run", "project": "proj", "command": []any{"make", "test"}, "outcome": "passed",
			"seconds": 30, "key": "k5", "not_stored_kind": "tree-changed", "not_stored": "the tree changed while the command ran"}, // its tree changed: not unchanged
		{"epoch": base + 4000, "event": "start", "project": "proj", "run_id": "r9"},
		{"epoch": base + 4100, "event": "start", "project": "proj", "run_id": "r8"},
		{"epoch": base + 4200, "event": "run", "project": "proj", "run_id": "r8", "outcome": "gave-up"},
	}, "")
	log := found.measure(t, found.spec("proj")).JackstayLog
	if got := log.Split[besideOther]; got.Runs != 1 || got.NotWell != 1 || got.ThenPassedUnchanged != 1 || value(t, got.RateThenPassed) != 1.0 {
		t.Errorf("other: %+v", got)
	}
	if got := log.Split[besideSame]; got.Runs != 2 || got.NotWell != 1 || got.ThenPassedUnchanged != 0 {
		t.Errorf("same: %+v", got)
	}
	if got := log.Split[besideNone]; got.Runs != 7 || got.NotWell != 3 || got.ThenPassedUnchanged != 0 || value(t, got.RateThenPassed) != 0 {
		t.Errorf("alone: %+v", got)
	}
	if log.Unfinished != 1 || log.LockEvidence.Shown {
		t.Errorf("unfinished %d, evidence %+v", log.Unfinished, log.LockEvidence)
	}
	if !reflect.DeepEqual(log.LockEvidence.WhyNot, []string{whyDays, whyRuns, whyAlone, whyFewBeside}) || len(log.LockEvidence.Explained) != 4 {
		t.Errorf("why not: %v", log.LockEvidence.WhyNot)
	}
}

func TestLockEvidenceNamesEachConditionThatFails(t *testing.T) {
	week, err := SpanOf(since, until)
	if err != nil {
		t.Fatal(err)
	}
	short, err := SpanOf(since, "2026-11-12")
	if err != nil {
		t.Fatal(err)
	}
	split := func(other, otherRepassed, alone, aloneRepassed int) map[string]Situation {
		return map[string]Situation{besideOther: {Runs: other, ThenPassedUnchanged: otherRepassed}, besideSame: {},
			besideNone: {Runs: alone, ThenPassedUnchanged: aloneRepassed}}
	}
	for name, check := range map[string]struct {
		split map[string]Situation
		span  Span
		days  int
		want  []string
	}{
		"shown":            {split(30, 6, 40, 1), week, 5, []string{}},
		"the rate":         {split(30, 6, 40, 4), week, 5, []string{whyRate}},
		"nothing to match": {split(50, 5, 0, 0), week, 5, []string{whyAlone}},
		"one day":          {split(30, 6, 40, 1), week, 1, []string{whyDays}},
		"a short span":     {split(30, 6, 40, 1), short, 5, []string{whySpan}},
		"few runs":         {split(9, 6, 40, 1), week, 5, []string{whyRuns}},
		"few repassed":     {split(30, 4, 40, 0), week, 5, []string{whyFewBeside}},
		"none repassed":    {split(30, 0, 40, 0), week, 5, []string{whyFewBeside, whyRate}},
	} {
		evidence := lockEvidence(check.split, check.span, check.days)
		if !reflect.DeepEqual(evidence.WhyNot, check.want) || evidence.Shown != (len(check.want) == 0) || len(evidence.Explained) != len(check.want) {
			t.Errorf("%s: %+v", name, evidence)
		}
	}
}
