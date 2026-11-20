package receipts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/version"
)

var fixtures = []string{filepath.Join("testdata", "runs.jsonl")}

func TestStatsOverARunLog(t *testing.T) {
	summary := Summarise(ReadRecords(fixtures))
	alpha := summary["alpha"]
	want := Tally{Runs: 5, Passed: 2, Exited0: 1, Failed: 1, Stopped: 1, GaveUp: 1, StoppedWaiting: 1, Unfinished: 1,
		RunSeconds: 300, WouldHit: 1, WouldSaveSeconds: 60, Forced: 2}
	if *alpha != want {
		t.Fatalf("alpha\n got %+v\nwant %+v", *alpha, want)
	}
	beta := summary["beta"]
	if beta.Runs != 1 || beta.NothingRun != 1 || beta.Reused != 1 || beta.SavedSeconds != 7200 || beta.Unfinished != 0 {
		t.Fatalf("beta %+v", *beta)
	}
}

func TestTheStatsCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	logPath, _ := RunLogPath()
	data, err := os.ReadFile(fixtures[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errs strings.Builder
	if status := statsCommand([]string{"--project", "beta", "--json"}, nil, &out, &errs); status != 0 {
		t.Fatalf("%d %s", status, errs.String())
	}
	var decoded map[string]map[string]float64
	if err := json.Unmarshal([]byte(out.String()), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded["beta"]["saved_seconds"] != 7200 || decoded["beta"]["nothing_run"] != 1 {
		t.Fatalf("%v", decoded)
	}
	out.Reset()
	if status := statsCommand(nil, nil, &out, &errs); status != 0 {
		t.Fatal(status)
	}
	if !strings.Contains(out.String(), "alpha: 5 runs (2 passed, 0 passed only on retry, 1 failed, 0 ran nothing, 1 ended 0 uncounted, 0 unjudged, 1 stopped), 0.08 h") ||
		!strings.Contains(out.String(), "2 runs forced over a receipt") {
		t.Fatalf("said:\n%s", out.String())
	}
	if statsCommand([]string{"--frob"}, nil, &out, &errs) != StatusUnusable {
		t.Fatal("a word stats does not know")
	}
}

func TestTypedFields(t *testing.T) {
	records := ReadRecords(fixtures)
	var forced, kinds []string
	for _, record := range records {
		if record.Text("event") == EventWouldHit && Forced(record) {
			forced = append(forced, record.Text("reason"))
		}
		if kind := NotStoredKind(record); kind != "" {
			kinds = append(kinds, kind)
		}
	}
	if !slices.Equal(forced, []string{"--no-reuse", "JACKSTAY_NO_REUSE is set"}) {
		t.Fatalf("forced %v", forced)
	}
	if !slices.Equal(kinds, []string{NotStoredInputRewritten, NotStoredTreeChanged}) {
		t.Fatalf("kinds %v", kinds)
	}
	if Forced(Record{"reason": "--no-reuse"}) || NotStoredKind(Record{"not_stored": "the tree changed"}) != "" {
		t.Fatal("words taken for the typed fields")
	}
}

func TestTheRunLogsLines(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	path, _ := RunLogPath()
	clock := newClock()
	log := RunLog{Path: path, Project: "demo", Command: []string{"make", "test"}, Where: Where{Worktree: "/w", Head: "h", Branch: "b"},
		Clock: clock}
	if !log.Write(EventStart, "", map[string]any{"run_id": "1-2"}) {
		t.Fatal("not written")
	}
	log.Inside = "outer"
	log.Write(EventRun, "k", map[string]any{"outcome": OutcomePassed})
	records := ReadRecords([]string{path})
	if len(records) != 2 {
		t.Fatal(records)
	}
	first := records[0]
	for _, field := range []string{"at", "epoch", "event", "project", "command", "key", "version", "worktree", "head", "branch"} {
		if _, ok := first[field]; !ok {
			t.Errorf("no %s", field)
		}
	}
	if first["key"] != nil || first.Inside() || first.Text("version") != version.Version || first.Text("at") != "2026-11-17T10:00:00+0000" {
		t.Fatalf("first %v", first)
	}
	if !records[1].Inside() || records[1].Text("key") != "k" {
		t.Fatalf("second %v", records[1])
	}
	if ParentRun(func(name string) string {
		return map[string]string{"JACKSTAY_TEST_RUN_ID": "o", "JACKSTAY_TEST_RUN_PROJECT": "demo"}[name]
	}, "demo") != "o" {
		t.Fatal("ParentRun")
	}
	if (RunLog{Clock: clock}).Write(EventStart, "", nil) {
		t.Fatal("a log without a path was written")
	}
}

func TestAWouldHitIsCountedFromTheLog(t *testing.T) {
	now := time.Unix(1763190200, 0)
	records := ReadRecords(fixtures)
	earlier, found := EarlierPass(records[:7], "alpha", "k1", 72*time.Hour, now)
	if !found || earlier.Text("run_id") != "r2-102" {
		t.Fatalf("earlier %v", earlier)
	}
	if _, found := EarlierPass(records, "alpha", "k1", 72*time.Hour, now); found {
		t.Fatal("a failure after the pass did not end it (the inside pass must not count)")
	}
	if _, found := EarlierPass(records[:6], "alpha", "k1", time.Minute, now.Add(time.Hour)); found {
		t.Fatal("too old")
	}
	if _, found := EarlierPass(records[:6], "beta", "k1", 72*time.Hour, now); found {
		t.Fatal("another project")
	}
}
