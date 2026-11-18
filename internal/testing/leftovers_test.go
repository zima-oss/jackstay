package scopes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/receipts"
	"github.com/zima-oss/jackstay/internal/supervise"
)

func leftoverSection(w *world, failToken, failPlain bool) map[string]any {
	return map[string]any{"judge": "swift-test", "timeout": 600, "scopes": map[string]any{
		"x-token":  map[string]any{"run": argv("sh", "-c", `touch "$TMPDIR/left-$JACKSTAY_RUN_ID.plist"; cat "$SIDE/pkg-filtered-xctest.log"`)},
		"x-runtmp": map[string]any{"run": argv("sh", "-c", `touch "$JACKSTAY_RUN_TMP/snap.png"; cat "$SIDE/pkg-filtered-xctest.log"`)},
		"x-plain":  map[string]any{"run": argv("plain-maker")},
		"x-clean":  map[string]any{"run": cat("pkg-filtered-xctest")},
	}, "leftovers": []any{
		map[string]any{"glob": "$TMPDIR/left-{run_id}.plist", "fail": failToken},
		map[string]any{"glob": "{run_tmp}/*.png", "fail": true},
		map[string]any{"glob": w.at("plain") + "/Prefs.*.plist", "fail": failPlain},
	}}
}

// plainMaker makes a file no token names, born now: the run's clock is set to its birth, so the
// file is born inside the run whatever the fake clock said before.
func plainMaker(t *testing.T, w *world) override {
	return func(order launch) *launched {
		place := filepath.Join(w.side, "plain", "Prefs.made.plist")
		if err := os.WriteFile(place, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(place)
		if err != nil {
			t.Fatal(err)
		}
		w.clock.Set(bornAt(info))
		feed(order.spec.Output, strings.Join(passingLines(t), "\n"), "")
		return &launched{outcome: supervise.Outcome{PID: 60}}
	}
}

func newLeftoverWorld(t *testing.T, failToken, failPlain bool) *world {
	w := newWorld(t, nil, nil)
	if err := os.MkdirAll(w.at("plain"), 0o700); err != nil {
		t.Fatal(err)
	}
	w.configure(leftoverSection(w, failToken, failPlain), nil)
	w.overrides["plain-maker"] = plainMaker(t, w)
	return w
}

func TestAnAttributedLeftoverFailsTheRun(t *testing.T) {
	w := newLeftoverWorld(t, true, true)
	w.expectStatus(w.jst("x-token"), 1)
	w.expectLast("outcome=failed", "left behind")
	w = newLeftoverWorld(t, false, true)
	w.expectStatus(w.jst("x-token"), 0)
	w.expectSaid("listed, not judged")
	leftovers, _ := w.lastRecord("run")["leftovers"].(map[string]any)
	if listed, _ := leftovers["listed"].([]any); len(listed) != 1 {
		t.Fatalf("the record: %v", w.lastRecord("run"))
	}
}

func TestALeftoverInTheRunsFolderKeepsIt(t *testing.T) {
	w := newLeftoverWorld(t, true, true)
	w.expectStatus(w.jst("x-runtmp"), 1)
	kept := w.lastRecord("run").Text("run_tmp_kept")
	if _, err := os.Stat(filepath.Join(kept, "snap.png")); err != nil {
		t.Fatalf("the run's folder was not kept: %v (%s)", err, kept)
	}
}

func TestAnUnattributedLeftoverWithNoOtherRunIsTheRuns(t *testing.T) {
	w := newLeftoverWorld(t, true, true)
	w.expectStatus(w.jst("x-plain"), 1)
	w.expectLast("outcome=failed", "Prefs.made.plist")
}

func TestAnOverlappingRunMakesItUnjudged(t *testing.T) {
	w := newLeftoverWorld(t, true, true)
	logPath, err := receipts.RunLogPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// Another project's run started before this one and has no end; its process lives.
	started := epochSeconds(w.clock.Now()) - 5
	line, _ := json.Marshal(map[string]any{"event": "start", "run_id": "other-run", "project": "elsewhere", "pid": 12345,
		"started_epoch": started})
	if err := os.WriteFile(logPath, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	w.alive = func(pid int) bool { return pid == 12345 }
	w.expectStatus(w.jst("x-plain"), 77)
	w.expectLast("another run may have made", "elsewhere:other-run")
	// Under `"fail": false` the entries are listed, not judged.
	w = newLeftoverWorld(t, true, false)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	w.alive = func(int) bool { return true }
	w.expectStatus(w.jst("x-plain"), 0)
	w.expectSaid("listed, not judged")
}

func TestOverlappingRuns(t *testing.T) {
	at := func(seconds float64) time.Time { return time.Unix(0, int64(seconds*1e9)) }
	records := []receipts.Record{
		{"event": "start", "run_id": "ended-before", "project": "p", "started_epoch": 10.0},
		{"event": "run", "run_id": "ended-before", "project": "p", "ended_epoch": 20.0},
		{"event": "start", "run_id": "inside", "project": "p", "started_epoch": 105.0},
		{"event": "run", "run_id": "inside", "project": "p", "ended_epoch": 110.0},
		{"event": "start", "run_id": "dead", "project": "q", "started_epoch": 90.0, "pid": 7.0},
		{"event": "start", "run_id": "living", "project": "q", "started_epoch": 90.0, "pid": 8.0},
		{"event": "start", "run_id": "me", "project": "p", "started_epoch": 100.0},
		{"event": "start", "run_id": "outer", "project": "p", "started_epoch": 99.0},
		{"event": "start", "run_id": "nested", "project": "p", "started_epoch": 101.0, "inside": "me"},
	}
	found := overlappingRuns(records, "me", "outer", at(100), at(120), at(200), func(pid int) bool { return pid == 8 })
	if strings.Join(found, " ") != "p:inside q:living" {
		t.Fatalf("found %v", found)
	}
}

func TestAnOldFileIsNotALeftover(t *testing.T) {
	w := newLeftoverWorld(t, true, true)
	if err := os.WriteFile(filepath.Join(w.at("plain"), "Prefs.old.plist"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	w.expectStatus(w.jst("x-clean"), 0)
	// One born long before the run, though it was not there when the run looked first, is not
	// this run's either.
	seen := newLeftovers([]leftoverRule{{glob: w.at("plain") + "/*.plist", fail: true}}, "id", w.tmp, w.tmp, nil)
	seen.before[0] = map[string]time.Time{}
	if found := seen.judge(time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)); found.record() != nil {
		t.Fatalf("found %v", found)
	}
}

func TestASubfolderThatCannotBeListedIsNoAnswer(t *testing.T) {
	w := newWorld(t, nil, nil)
	hidden := filepath.Join(w.at("plain"), "inner")
	if err := os.MkdirAll(hidden, 0o700); err != nil {
		t.Fatal(err)
	}
	w.configure(map[string]any{"judge": "swift-test", "timeout": 600, "scopes": map[string]any{"x-clean": map[string]any{"run": cat("pkg-filtered-xctest")}},
		"leftovers": []any{map[string]any{"glob": w.at("plain") + "/*/Prefs.*.plist", "fail": true}}}, nil)
	if err := os.Chmod(hidden, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(hidden, 0o700) })
	w.expectStatus(w.jst("x-clean"), 77)
	w.expectLast("inner cannot be listed")
}

func TestAFolderOnTheWayThatCannotBeEnteredIsNoAnswer(t *testing.T) {
	w := newWorld(t, nil, nil)
	blocked := filepath.Join(w.at("plain"), "blocked")
	if err := os.MkdirAll(filepath.Join(blocked, "fixed"), 0o700); err != nil {
		t.Fatal(err)
	}
	w.configure(map[string]any{"judge": "swift-test", "timeout": 600, "scopes": map[string]any{"x-clean": map[string]any{"run": cat("pkg-filtered-xctest")}},
		"leftovers": []any{map[string]any{"glob": blocked + "/fixed/*.plist", "fail": true}}}, nil)
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	w.expectStatus(w.jst("x-clean"), 77)
	w.expectLast("cannot be looked at")
	paths, problem := globPaths(blocked + "/fixed/x.plist")
	if len(paths) != 0 || !strings.Contains(problem, "cannot be looked at") {
		t.Fatalf("%v %q", paths, problem)
	}
	// Under `"fail": false` the problem is listed, and the run passes.
	w.configure(map[string]any{"judge": "swift-test", "timeout": 600, "scopes": map[string]any{"x-clean": map[string]any{"run": cat("pkg-filtered-xctest")}},
		"leftovers": []any{map[string]any{"glob": blocked + "/fixed/*.plist", "fail": false}}}, nil)
	w.expectStatus(w.jst("x-clean"), 0)
	w.expectSaid("listed, not judged")
}

func TestAFolderThatCannotBeListedIsNoAnswer(t *testing.T) {
	w := newLeftoverWorld(t, true, true)
	if err := os.Chmod(w.at("plain"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(w.at("plain"), 0o700) })
	w.expectStatus(w.jst("x-clean"), 77)
	w.expectLast("cannot be looked for")
}

func TestTheGlobWalkMatchesAsGlobDoes(t *testing.T) {
	top := t.TempDir()
	for _, name := range []string{"a/x.plist", "a/.h.plist", "b/y.plist", "b/z.txt", "c.plist", "d/[x].plist"} {
		place := filepath.Join(top, name)
		if err := os.MkdirAll(filepath.Dir(place), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(place, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string][]string{
		"/*/*.plist":   {"/a/x.plist", "/b/y.plist", "/d/[x].plist"},
		"/*.plist":     {"/c.plist"},
		"/a/*":         {"/a/x.plist"},
		"/a/.*":        {"/a/.h.plist"},
		"/?/*.p*":      {"/a/x.plist", "/b/y.plist", "/d/[x].plist"},
		"/a/x.plist":   {"/a/x.plist"},
		"/nope/*":      nil,
		"/[!a]/*.txt":  {"/b/z.txt"},
		"/[ab]/*.txt":  {"/b/z.txt"},
		"/d/[[]x].*":   {"/d/[x].plist"},
		"/c.plist/*":   nil,
		"/a/x.plist/y": nil,
	}
	for pattern, wanted := range cases {
		found, problem := globPaths(top + pattern)
		if problem != "" {
			t.Fatalf("%s: %s", pattern, problem)
		}
		var relative []string
		for _, place := range found {
			relative = append(relative, strings.TrimPrefix(place, top))
		}
		slices.Sort(relative)
		if !slices.Equal(relative, wanted) {
			t.Errorf("%s: %v, want %v", pattern, relative, wanted)
		}
	}
}

func TestExpandGlob(t *testing.T) {
	got := expandGlob("$TMPDIR/a-{run_id}/${TMPDIR}/{run_tmp}/x", "R", "/T/run", "/T/")
	if got != "/T/a-R//T//T/run/x" {
		t.Fatalf("expanded as %s", got)
	}
	if home := expandGlob("~/Library/x", "", "", ""); !strings.HasSuffix(home, "/Library/x") || strings.HasPrefix(home, "~") {
		t.Fatalf("~ expanded as %s", home)
	}
}
