package receipts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/supervise"
)

func TestTheEvidenceJudge(t *testing.T) {
	judge, err := NewEvidence(map[string]any{"evidence": "Executed ([0-9]+) tests?", "veto": "FAILED"})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"Executed 3 tests", supervise.Clean("\x1b[32mExecuted 4 tests\x1b[0m"), "x FAILED y", "FAILED again",
		"Executed 1 test Executed 2 tests"} {
		_ = judge.Line(supervise.Stdout, line)
	}
	reading := judge.Reading()
	if !reading.Counts || reading.Counted != 10 || reading.Veto != "x FAILED y" || reading.Unjudged != "" {
		t.Fatalf("%+v", reading)
	}
	if _, err := NewEvidence(map[string]any{"evidence": "Executed [0-9]+ tests"}); err == nil {
		t.Fatal("an evidence pattern without a group")
	}
	if _, err := NewEvidence(map[string]any{"veto": "("}); err == nil {
		t.Fatal("a veto that cannot be read")
	}
	none, _ := NewEvidence(nil)
	_ = none.Line(supervise.Stdout, "Executed 3 tests")
	if reading := none.Reading(); reading.Counts || reading.NoCount != noEvidence {
		t.Fatalf("%+v", reading)
	}
	vetoOnly, _ := NewEvidence(map[string]any{"veto": "panic:"})
	_ = vetoOnly.Line(supervise.Stderr, "panic: boom")
	if vetoOnly.Reading().Veto != "panic: boom" || vetoOnly.Reading().Counts {
		t.Fatal("a veto stands without evidence")
	}
}

func TestOnlyTheNewestOutputsAreKept(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	folder, _ := OutputsFolder("demo")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	logPath, _ := RunLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	var ended []string
	for index := range 35 {
		id := fmt.Sprintf("%02d-1", index)
		place := filepath.Join(folder, id+".log")
		if err := os.WriteFile(place, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(place, now, now)
		if index != 1 && index != 2 {
			line, _ := json.Marshal(map[string]any{"event": EventRun, "run_id": id})
			ended = append(ended, string(line))
		}
	}
	// 02's run has no end but is a week old: it goes; 01's has no end and is recent: it stays.
	old := now.Add(-8 * 24 * time.Hour)
	_ = os.Chtimes(filepath.Join(folder, "02-1.log"), old, old)
	if err := os.WriteFile(logPath, []byte(strings.Join(ended, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept, err := KeepOutput("demo", "99-1", []string{logPath}, now)
	if err != nil {
		t.Fatal(err)
	}
	kept.Line("hello")
	if !kept.Close() {
		t.Fatal("not whole")
	}
	names, _ := filepath.Glob(filepath.Join(folder, "*.log"))
	have := map[string]bool{}
	for _, name := range names {
		have[filepath.Base(name)] = true
	}
	for _, gone := range []string{"00-1.log", "02-1.log", "03-1.log", "05-1.log"} {
		if have[gone] {
			t.Errorf("%s kept", gone)
		}
	}
	for _, stays := range []string{"01-1.log", "06-1.log", "34-1.log", "99-1.log"} {
		if !have[stays] {
			t.Errorf("%s removed", stays)
		}
	}
	data, _ := os.ReadFile(kept.Path)
	if string(data) != "hello\n" {
		t.Fatalf("kept %q", data)
	}
}

func TestNotableLines(t *testing.T) {
	place := filepath.Join(t.TempDir(), "out.log")
	var lines []string
	for index := range 45 {
		lines = append(lines, fmt.Sprintf("x.swift:%d: error: broken", index))
	}
	lines = append(lines, "fine", "x.swift:0: error: broken", "custom stop word", "\x1b[31mFAIL\x1b[0m here")
	if err := os.WriteFile(place, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	found, more := NotableLines(place, regexp.MustCompile("stop word"))
	if len(found) != NotableMax || more != 7 {
		t.Fatalf("%d lines, %d more", len(found), more)
	}
	found, _ = NotableLines(place, nil)
	if found[0] != "x.swift:0: error: broken" {
		t.Fatal(found[0])
	}
	var out strings.Builder
	WriteNotable(&out, "p: ", []string{"a"}, 2)
	if out.String() != "p: what the output says went wrong (3 lines, the first 1):\np: | a\n" {
		t.Fatalf("%q", out.String())
	}
	if found, _ := NotableLines(filepath.Join(t.TempDir(), "none"), nil); found != nil {
		t.Fatal("a missing file")
	}
}

func TestRunIDAndTimes(t *testing.T) {
	moment := time.Unix(0, 0x18e82d0f66706158)
	if RunID(moment, 45742) != "18e82d0f66706158-45742" {
		t.Fatal(RunID(moment, 45742))
	}
	if roundTo(1.25, 1) != 1.3 || roundTo(-1.25, 1) != -1.3 || secondsBetween(moment, moment.Add(1549*time.Millisecond)) != 1.5 {
		t.Fatal("rounding")
	}
}
