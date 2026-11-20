package receipts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/supervise"
)

func TestAPassWithEvidenceStoresAReceipt(t *testing.T) {
	w := newWorld(t, configFile)
	w.fake = passing("12")
	if status := w.test("--", "make", "test"); status != 0 {
		t.Fatalf("status %d\n%s", status, w.stderr.String())
	}
	if got := w.lastLine(); got != "jst test: outcome=passed (0 s, tests counted: 12)" {
		t.Fatalf("last line %q", got)
	}
	if !strings.HasSuffix(w.storeFolder(), filepath.Join("demo", "receipts")) {
		t.Fatalf("store %s", w.storeFolder())
	}
	if len(w.receipts()) != 1 {
		t.Fatalf("receipts %v", w.receipts())
	}
	start, run := w.lastRecord(EventStart), w.lastRecord(EventRun)
	if start.Text("run_id") == "" || start.Text("run_id") != run.Text("run_id") {
		t.Fatalf("run ids %v %v", start, run)
	}
	for _, field := range []string{"at", "epoch", "event", "project", "command", "key", "version", "worktree", "head", "branch",
		"run_id", "pid", "locked", "outcome", "started_epoch", "ended_epoch", "seconds", "evidence", "receipt_removed",
		"evidence_count", "stored", "not_stored", "output"} {
		if _, ok := run[field]; !ok {
			t.Errorf("run line has no %s: %v", field, run)
		}
	}
	if run["stored"] != true || run.Text("branch") != "feature" || run["evidence_count"] != 12.0 || run["locked"] != false {
		t.Fatalf("run line %v", run)
	}
	if w.fake.spec.Mode != supervise.Group || !w.fake.spec.Terminal || w.fake.spec.SessionsFile == "" {
		t.Fatalf("spec %+v", w.fake.spec)
	}
	env := strings.Join(w.fake.spec.Env, "\n")
	for _, want := range []string{supervise.RunIDVariable + "=" + run.Text("run_id"), supervise.RunProjectVariable + "=demo",
		supervise.SessionsFileVariable + "=" + w.fake.spec.SessionsFile} {
		if !strings.Contains(env, want) {
			t.Errorf("the command's environment lacks %s", want)
		}
	}
	if !strings.Contains(w.stdout.String(), "Executed 12 tests") {
		t.Fatalf("output not passed on: %q", w.stdout.String())
	}
}

func TestAReceiptStandsInForTheRun(t *testing.T) {
	w := newWorld(t, reuseConfig)
	w.fake = passing("3")
	w.test("--", "make", "test")
	w.fake = passing("3")
	if status := w.test("--", "make", "test"); status != 0 || w.fake.calls != 0 {
		t.Fatalf("status %d calls %d\n%s", status, w.fake.calls, w.stderr.String())
	}
	if w.lastLine() != "jst test: outcome=reused" || !strings.Contains(w.stderr.String(), "REUSED: nothing was run") {
		t.Fatalf("said %s", w.stderr.String())
	}
	if w.stdout.Len() != 0 {
		t.Fatalf("a reuse wrote on standard output: %q", w.stdout.String())
	}
	if reused := w.lastRecord(EventReused); reused.Text("lookup") != "under-lock" || reused["saved_seconds"] == nil {
		t.Fatalf("reused %v", reused)
	}
	// --no-reuse forces a run, counted apart from a would-hit.
	w.fake = passing("3")
	w.test("--no-reuse", "--", "make", "test")
	if w.fake.calls != 1 || w.lastRecord(EventWouldHit)["forced"] != true {
		t.Fatalf("calls %d, %v", w.fake.calls, w.lastRecord(EventWouldHit))
	}
	// JACKSTAY_NO_REUSE does the same.
	w.env[NoReuseVariable] = "1"
	w.fake = passing("3")
	w.test("--", "make", "test")
	if w.fake.calls != 1 || w.lastRecord(EventWouldHit)["forced"] != true {
		t.Fatalf("JACKSTAY_NO_REUSE: calls %d", w.fake.calls)
	}
}

func TestAReuseThatCannotBeLoggedDoesNotHappen(t *testing.T) {
	w := newWorld(t, reuseConfig)
	w.fake = passing("3")
	w.test("--", "make", "test")
	logPath, _ := RunLogPath()
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(logPath, 0o700); err != nil {
		t.Fatal(err)
	}
	w.fake = passing("3")
	if status := w.test("--", "make", "test"); status != 0 || w.fake.calls != 1 {
		t.Fatalf("status %d calls %d", status, w.fake.calls)
	}
	said := w.stderr.String()
	if !strings.Contains(said, "the reuse could not be logged") || !strings.Contains(said, "WARNING: the log could not be written") {
		t.Fatalf("said %s", said)
	}
	lines := strings.Split(strings.TrimSpace(said), "\n")
	if !strings.HasPrefix(lines[len(lines)-2], "jst test: WARNING") || !strings.HasPrefix(w.lastLine(), "jst test: outcome=passed") {
		t.Fatalf("the warning is not just before the outcome line: %s", said)
	}
}

func TestOnMainOrAtItsTipTheCommandAlwaysRuns(t *testing.T) {
	w := newWorld(t, reuseConfig)
	w.repo.Git("checkout", "-q", "main")
	w.fake = passing("3")
	w.test("--", "make", "test")
	w.fake = passing("3")
	w.test("--", "make", "test")
	if w.fake.calls != 1 || !strings.Contains(w.lastRecord(EventWouldHit).Text("reason"), "the branch is main") {
		t.Fatalf("on main: calls %d", w.fake.calls)
	}
	w.repo.Git("checkout", "-q", "--detach", "main")
	w.fake = passing("3")
	w.test("--", "make", "test")
	if w.fake.calls != 1 || !strings.Contains(w.lastRecord(EventWouldHit).Text("reason"), "HEAD is the commit main points at") {
		t.Fatalf("detached at main: %v", w.lastRecord(EventWouldHit))
	}
	// A detached HEAD at origin/main, where the local main is behind, is main too.
	w.repo.Git("checkout", "-q", "main")
	w.repo.Write(map[string]string{"other.txt": "x"})
	w.repo.Git("add", "other.txt")
	w.repo.Git("commit", "-q", "-m", "ahead")
	w.repo.Git("update-ref", "refs/remotes/origin/main", "HEAD")
	w.repo.Git("reset", "-q", "--hard", "HEAD~1")
	w.repo.Git("checkout", "-q", "--detach", "origin/main")
	w.fake = passing("3")
	w.test("--", "make", "test")
	w.fake = passing("3")
	w.test("--", "make", "test")
	if w.fake.calls != 1 || !strings.Contains(w.lastRecord(EventWouldHit).Text("reason"), "origin/main") {
		t.Fatalf("detached at origin/main: %v", w.lastRecord(EventWouldHit))
	}
}

func TestACommandThatIsNotOptedInOrNotListed(t *testing.T) {
	w := newWorld(t, reuseConfig)
	w.fake = passing("3")
	w.test("--", "make", "check", "X=1")
	w.fake = passing("3")
	w.test("--", "make", "check", "X=1")
	if w.fake.calls != 1 || w.lastRecord(EventWouldHit).Text("reason") != "the command is not opted in (reuse is not true)" {
		t.Fatalf("argv_prefix entry: %v", w.records())
	}
	// Not listed: no evidence pattern, so ended 0 is exited-0, never a receipt, and no would-hit.
	w.fake = &fakeRun{stdout: []string{"ok"}}
	if status := w.test("--", "true"); status != 0 {
		t.Fatalf("status %d", status)
	}
	if w.lastLine() != "jst test: outcome=exited-0 (no test count: not proof that tests ran), 0 s" {
		t.Fatalf("last %q", w.lastLine())
	}
	run := w.lastRecord(EventRun)
	if run.Text("outcome") != OutcomeExited0 || run["stored"] != false || run.Text("not_stored_kind") != NotStoredNoEvidence ||
		run["evidence_count"] != nil {
		t.Fatalf("run %v", run)
	}
}

func TestOutcomesAndExitStatuses(t *testing.T) {
	cases := []struct {
		name    string
		fake    *fakeRun
		status  int
		outcome string
		last    string
		fields  map[string]any
	}{
		{"a status of its own", &fakeRun{stdout: []string{"error: no"}, outcome: supervise.Outcome{Code: 3}}, 3, OutcomeFailed,
			"jst test: outcome=failed (exit 3, 0 s)", map[string]any{"status": 3.0}},
		{"75 is the command's", &fakeRun{outcome: supervise.Outcome{Code: 75}}, 75, OutcomeFailed,
			"jst test: outcome=failed (exit 75, 0 s)", nil},
		{"76 is the command's", &fakeRun{outcome: supervise.Outcome{Code: 76}}, 76, OutcomeFailed,
			"jst test: outcome=failed (exit 76, 0 s)", nil},
		{"died by a signal", &fakeRun{outcome: supervise.Outcome{Signal: syscall.SIGSEGV}}, 139, OutcomeFailed,
			"jst test: outcome=failed (exit 139, 0 s)", nil},
		{"a veto line", &fakeRun{stdout: []string{"\x1b[31mFAILED\x1b[0m: x", "Executed 4 tests"}}, 1, OutcomeFailed,
			"jst test: outcome=failed (exit 1: a veto line in the output of a command that ended 0, 0 s)",
			map[string]any{"status": 1.0, "command_status": 0.0}},
		{"nothing ran", &fakeRun{stdout: []string{"Executed 0 tests"}}, 76, OutcomeNothingRun,
			"jst test: outcome=nothing-run (0 s)", nil},
		{"no count at all", &fakeRun{stdout: []string{"done"}}, 76, OutcomeNothingRun, "jst test: outcome=nothing-run (0 s)", nil},
		{"output not read to its end", &fakeRun{stdout: []string{"Executed 2 tests"},
			outcome: supervise.Outcome{Unread: "the output was not read to its end within 10 s after the stop"}}, 77, OutcomeUnjudged,
			"jst test: outcome=unjudged (exit 77: the output of a command that ended 0 could not be judged, 0 s)",
			map[string]any{"why": "the output was not read to its end within 10 s after the stop"}},
		{"the end cannot be confirmed", &fakeRun{stdout: []string{"Executed 2 tests"},
			outcome: supervise.Outcome{Problems: []error{errors.New("the process table cannot be read")}}}, 77, OutcomeUnjudged, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, configFile)
			w.fake = c.fake
			if status := w.test("--", "make", "test"); status != c.status {
				t.Fatalf("status %d, want %d\n%s", status, c.status, w.stderr.String())
			}
			if c.last != "" && w.lastLine() != c.last {
				t.Fatalf("last %q, want %q", w.lastLine(), c.last)
			}
			run := w.lastRecord(EventRun)
			if run.Text("outcome") != c.outcome {
				t.Fatalf("outcome %v", run)
			}
			for name, want := range c.fields {
				if run[name] != want {
					t.Errorf("%s = %v, want %v", name, run[name], want)
				}
			}
			if len(w.receipts()) != 0 || len(w.notes()) != 1 {
				t.Fatalf("receipts %v notes %v", w.receipts(), w.notes())
			}
		})
	}
}

func TestAnUncountableEvidenceGroupIsUnjudged(t *testing.T) {
	w := newWorld(t, strings.Replace(configFile, `Executed ([0-9]+) tests?`, `Executed (\\S+) tests?`, 1))
	w.fake = &fakeRun{stdout: []string{"Executed 3 tests", "Executed many tests"}}
	if status := w.test("--", "make", "test"); status != StatusUnjudged {
		t.Fatalf("status %d", status)
	}
	if why := w.lastRecord(EventRun).Text("why"); !strings.Contains(why, "Executed many tests") {
		t.Fatalf("why %q", why)
	}
}

func TestNegativeEvidenceCannotCreateADirectPass(t *testing.T) {
	config := strings.Replace(configFile, `Executed ([0-9]+) tests?`, `Executed (-?[0-9]+) tests?`, 1)
	w := newWorld(t, config)
	w.fake = &fakeRun{stdout: []string{"Executed 4 tests", "Executed -1 tests"}}
	if status := w.test("--", "make", "test"); status != StatusUnjudged {
		t.Fatalf("status %d; stderr:\n%s", status, w.stderr.String())
	}
	if run := w.lastRecord(EventRun); run.Text("outcome") != OutcomeUnjudged || len(w.receipts()) != 0 {
		t.Fatalf("negative evidence produced a pass or receipt: %v", run)
	}
}

// A veto or an uncountable evidence group that matches a blank line counts as much as one that
// matches text (docs/design/implementation.md section 9.3): the match is the fact, not the text it holds.
func TestAMatchOnABlankLineStillCounts(t *testing.T) {
	cases := []struct {
		name, from, to string
		status         int
		outcome        string
	}{
		{"a veto that names a blank line fails the run", `"veto": "FAILED"`, `"veto": "^\\s*$"`, StatusVeto, OutcomeFailed},
		{"an evidence group that holds no number on a blank line is unjudged", `"Executed ([0-9]+) tests?"`,
			`"^ *([0-9]*) *(?:tests? passed)?$"`, StatusUnjudged, OutcomeUnjudged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			config := strings.Replace(configFile, c.from, c.to, 1)
			if config == configFile {
				t.Fatal("the configuration did not change")
			}
			w := newWorld(t, config)
			w.fake = &fakeRun{stdout: []string{"Executed 3 tests", "3 tests passed"}}
			w.test("--", "make", "test")
			if len(w.receipts()) != 1 {
				t.Fatalf("no receipt to begin with:\n%s", w.stderr.String())
			}
			w.fake = &fakeRun{stdout: []string{"Executed 3 tests", "3 tests passed", "   ", "done"}}
			if status := w.test("--", "make", "test"); status != c.status {
				t.Fatalf("status %d, want %d\n%s", status, c.status, w.stderr.String())
			}
			if run := w.lastRecord(EventRun); run.Text("outcome") != c.outcome {
				t.Fatalf("run %v", run)
			}
			if len(w.receipts()) != 0 || len(w.notes()) != 1 {
				t.Fatalf("receipts %v notes %v", w.receipts(), w.notes())
			}
		})
	}
}

func TestAFailureRemovesTheReceiptAndSaysWhatWentWrong(t *testing.T) {
	w := newWorld(t, configFile)
	w.fake = passing("3")
	w.test("--", "make", "test")
	if len(w.receipts()) != 1 {
		t.Fatal("no receipt")
	}
	w.fake = &fakeRun{stdout: []string{"compiling", "x.swift:1: error: nope", "x.swift:1: error: nope", "Executed 3 tests, with 1 failure"},
		outcome: supervise.Outcome{Code: 1}}
	w.test("--", "make", "test")
	if len(w.receipts()) != 0 || len(w.notes()) != 1 {
		t.Fatalf("receipts %v notes %v", w.receipts(), w.notes())
	}
	run := w.lastRecord(EventRun)
	if run["receipt_removed"] != true {
		t.Fatalf("run %v", run)
	}
	said := w.stderr.String()
	for _, want := range []string{"what the output says went wrong (2 lines):", "| x.swift:1: error: nope",
		"| Executed 3 tests, with 1 failure", "the whole output: " + run.Text("output")} {
		if !strings.Contains(said, want) {
			t.Errorf("not said: %q in\n%s", want, said)
		}
	}
	if strings.Count(said, "error: nope") != 1 {
		t.Errorf("a repeated line shown twice:\n%s", said)
	}
	// `jst test last` says it again, the outcome line last.
	var out, errs strings.Builder
	if status := LastCommand(nil, nil, &out, &errs); status != 0 {
		t.Fatalf("last: %d %s", status, errs.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if lines[len(lines)-1] != "outcome=failed (0 s, exit 1)" || !strings.Contains(out.String(), "| x.swift:1: error: nope") {
		t.Fatalf("last said:\n%s", out.String())
	}
}

func TestReceiptRulesAfterThePass(t *testing.T) {
	cases := []struct {
		name   string
		during func(w *world)
		kind   string
	}{
		{"the tree changed", func(w *world) { w.repo.Write(map[string]string{"src/a.swift": "let a = 2\n"}) }, NotStoredTreeChanged},
		{"an input rewritten to the same content", func(w *world) {
			place := filepath.Join(w.repo.Root, "src", "a.swift")
			if err := os.Remove(place); err != nil {
				w.t.Fatal(err)
			}
			w.repo.Write(map[string]string{"src/a.swift": "let a = 1\n"})
		}, NotStoredInputRewritten},
		{"another run failed after this one started", func(w *world) {
			store, _ := OpenStore("demo", nil, w.clock)
			key, _ := KeyOf(context.Background(), Keying{Root: w.repo.Root, Config: mustLoad(w.t, w.repo.Root), Project: "demo",
				Command: []string{"make", "test"}, Dir: w.repo.Root, Environ: []string{"PATH=" + os.Getenv("PATH")}})
			store.NoteFailure(key.Value, OutcomeFailed)
		}, NotStoredFailedSince},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, configFile)
			w.fake = passing("3")
			w.fake.during = func() { c.during(w) }
			if status := w.test("--", "make", "test"); status != 0 {
				t.Fatalf("status %d", status)
			}
			run := w.lastRecord(EventRun)
			if run.Text("outcome") != OutcomePassed || run["stored"] != false || run.Text("not_stored_kind") != c.kind {
				t.Fatalf("run %v", run)
			}
			if len(w.receipts()) != 0 || !strings.Contains(w.stderr.String(), "passed; no receipt stored:") {
				t.Fatalf("receipts %v", w.receipts())
			}
		})
	}
}

func TestWithoutAKeyThePassStoresNothingAndSaysWhy(t *testing.T) {
	w := newWorld(t, `{"jackstay_schema": 3, "project": "demo", "receipts": {"commands": [{"argv": ["make", "test"], "evidence": "Executed ([0-9]+) tests?"}]}}`)
	w.fake = passing("3")
	if status := w.test("--", "make", "test"); status != 0 {
		t.Fatalf("status %d", status)
	}
	run := w.lastRecord(EventRun)
	if run["key"] != nil || run.Text("not_stored_kind") != NotStoredNoKey || run.Text("not_stored") != "jackstay.json names no receipts.inputs" {
		t.Fatalf("run %v", run)
	}
}

func TestAStopIsRecordedAndEndsByTheSignal(t *testing.T) {
	w := newWorld(t, configFile)
	w.fake = passing("3")
	w.test("--", "make", "test")
	w.fake = &fakeRun{stdout: []string{"Executed 3 tests"}, outcome: supervise.Outcome{StoppedBy: syscall.SIGTERM, Signal: syscall.SIGTERM}}
	if status := w.test("--", "make", "test"); status != 143 || w.ended != syscall.SIGTERM {
		t.Fatalf("status %d ended %v", status, w.ended)
	}
	run := w.lastRecord(EventRun)
	if run.Text("outcome") != OutcomeStopped || run.Text("signal") != "SIGTERM" || run["status"] != 143.0 || run["command_ended"] != false {
		t.Fatalf("run %v", run)
	}
	if !strings.HasPrefix(w.lastLine(), "jst test: outcome=stopped (SIGTERM after ") || len(w.receipts()) != 0 || len(w.notes()) != 1 {
		t.Fatalf("last %q receipts %v", w.lastLine(), w.receipts())
	}
}

func TestACommandThatCannotStart(t *testing.T) {
	w := newWorld(t, configFile)
	w.fake = &fakeRun{err: &supervise.StartError{Command: "nope", Err: errors.New("not found")}}
	if status := w.test("--", "nope"); status != StatusNoStart {
		t.Fatalf("status %d", status)
	}
	if run := w.lastRecord(EventRun); run.Text("outcome") != OutcomeFailed || run["status"] != 127.0 {
		t.Fatalf("run %v", run)
	}
}

func TestAnUnusableProjectFileRunsNothing(t *testing.T) {
	w := newWorld(t, `{"jackstay_schema": 3, "receipts": {"inputs": ["src"], "commands": [{"argv": ["make", "test"], "evidence": "Executed [0-9]+ tests"}]}}`)
	if status := w.test("--", "make", "test"); status != StatusUnusable || w.fake.calls != 0 {
		t.Fatalf("status %d calls %d", status, w.fake.calls)
	}
	if w.lastLine() != "jst test: outcome=not-started (jackstay.json cannot be used; nothing was run)" {
		t.Fatalf("last %q", w.lastLine())
	}
	w2 := newWorld(t, `{"jackstay_schema": 3, "receipts": {"commands": [{"argv_prefix": ["make"], "reuse": true, "evidence": "E ([0-9]+)"}]}}`)
	if status := w2.test("--", "make", "test"); status != StatusUnusable || w2.fake.calls != 0 {
		t.Fatalf("reuse with argv_prefix: status %d", status)
	}
}

func TestANestedRunIsMarkedInside(t *testing.T) {
	w := newWorld(t, configFile)
	w.env[supervise.RunIDVariable] = "outer-1"
	w.env[supervise.RunProjectVariable] = "demo"
	w.fake = passing("3")
	w.test("--", "make", "test")
	for _, record := range w.records() {
		if record.Text("inside") != "outer-1" {
			t.Fatalf("not marked: %v", record)
		}
	}
	// A run of another project inside it is that project's own.
	w.env[supervise.RunProjectVariable] = "other"
	w.fake = passing("3")
	w.test("--", "make", "test")
	if w.lastRecord(EventRun).Inside() {
		t.Fatal("another project's outer run marked this one")
	}
	if strings.Contains(strings.Join(w.fake.spec.Env, "\n"), "outer-1") {
		t.Fatal("the outer run's id was handed to the command")
	}
}

func TestTheLockOffByDefaultAndItsOutcomes(t *testing.T) {
	w := newWorld(t, configFile)
	t.Setenv(flock.PathVariable, filepath.Join(t.TempDir(), "suite.lock"))
	asked := false
	w.held = func(context.Context, flock.Request) (*flock.Held, error) { asked = true; return nil, &flock.GaveUp{} }
	w.fake = passing("3")
	w.test("--", "make", "test")
	if asked {
		t.Fatal("the lock was taken while off")
	}
	if status := w.test("--lock", "--wait", "1", "--", "make", "test"); status != StatusGaveUp || w.fake.calls != 1 {
		t.Fatalf("gave up: status %d calls %d", status, w.fake.calls)
	}
	run := w.lastRecord(EventRun)
	if run.Text("outcome") != OutcomeGaveUp || run["run_id"] != nil || run["waited_seconds"] == nil || run["key"] != nil {
		t.Fatalf("gave-up line %v", run)
	}
	if w.lastLine() != "jst test: outcome=gave-up (nothing was run)" {
		t.Fatalf("last %q", w.lastLine())
	}
	w.held = func(context.Context, flock.Request) (*flock.Held, error) {
		return nil, &supervise.StopRequest{Signal: syscall.SIGINT}
	}
	if status := w.test("--lock", "--", "make", "test"); status != 130 || w.ended != syscall.SIGINT {
		t.Fatalf("stopped waiting: %d", status)
	}
	if run := w.lastRecord(EventRun); run.Text("outcome") != OutcomeStoppedWaiting || run.Text("signal") != "SIGINT" {
		t.Fatalf("line %v", run)
	}
	w.held = func(context.Context, flock.Request) (*flock.Held, error) {
		return nil, &flock.Unusable{Words: "cannot open"}
	}
	if status := w.test("--lock", "--", "make", "test"); status != StatusUnusable {
		t.Fatalf("unusable: %d", status)
	}
	if w.lastLine() != "jst test: outcome=not-started (the lock could not be used; nothing was run)" {
		t.Fatalf("last %q", w.lastLine())
	}
	// lock.enabled turns it on; --no-lock turns it off for one call.
	on := newWorld(t, strings.Replace(configFile, `"project": "demo",`, `"project": "demo", "lock": {"enabled": true},`, 1))
	t.Setenv(flock.PathVariable, filepath.Join(t.TempDir(), "suite.lock"))
	taken := 0
	on.held = func(context.Context, flock.Request) (*flock.Held, error) { taken++; return nil, &flock.GaveUp{} }
	on.test("--", "make", "test")
	on.fake = passing("1")
	on.test("--no-lock", "--", "make", "test")
	if taken != 1 || on.fake.calls != 1 {
		t.Fatalf("taken %d calls %d", taken, on.fake.calls)
	}
}

func TestTheRealLockIsHeldWhileTheCommandRuns(t *testing.T) {
	w := newWorld(t, configFile)
	lockPath := filepath.Join(t.TempDir(), "suite.lock")
	t.Setenv(flock.PathVariable, lockPath)
	w.fake = passing("2")
	heldNow := false
	w.fake.during = func() { heldNow = flock.IsHeld(lockPath) }
	if status := w.test("--lock", "--", "make", "test"); status != 0 {
		t.Fatalf("status %d\n%s", status, w.stderr.String())
	}
	if !heldNow || w.lastRecord(EventRun)["locked"] != true {
		t.Fatalf("held %v, %v", heldNow, w.lastRecord(EventRun))
	}
	if !strings.Contains(strings.Join(w.fake.spec.Env, "\n"), flock.HeldVariable+"=") {
		t.Fatal("the command was not told the lock is held")
	}
}

func TestParsing(t *testing.T) {
	if !IsRaw([]string{"--no-reuse", "--project", "x", "--", "make"}) || IsRaw([]string{"unit"}) || IsRaw([]string{"--lock"}) {
		t.Fatal("IsRaw")
	}
	for _, bad := range [][]string{{"--"}, {"--wait", "soon", "--", "x"}, {"--frob", "--", "x"}, {"make"}, nil} {
		if _, ok := parseRaw(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	asked, ok := parseRaw([]string{"--no-lock", "--wait", "1.5", "--project", "p", "--no-reuse", "--", "a", "--", "b"})
	if !ok || asked.lock == nil || *asked.lock || !asked.bounded || asked.wait.Seconds() != 1.5 || asked.project != "p" ||
		!asked.noReuse || !slices.Equal(asked.argv, []string{"a", "--", "b"}) {
		t.Fatalf("%+v", asked)
	}
	var out, errs strings.Builder
	if TestCommand([]string{"--help"}, nil, &out, &errs) != 0 || !strings.Contains(out.String(), "usage: jst test") {
		t.Fatal("help")
	}
	if TestCommand([]string{"--", ""}[:1], nil, &out, &errs) != StatusUnusable {
		t.Fatal("usage")
	}
}

func mustLoad(t *testing.T, root string) config.Config {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "jackstay.json"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestWithoutAProjectFileTheCommandJustRuns(t *testing.T) {
	w := newWorld(t, configFile)
	w.repo.Git("rm", "-q", "jackstay.json")
	w.env[flock.HeldVariable] = "/x/suite.lock:1"
	w.fake = &fakeRun{stdout: []string{"Executed 3 tests"}}
	if status := w.test("--", "make", "test"); status != 0 || w.fake.calls != 1 {
		t.Fatalf("status %d calls %d\n%s", status, w.fake.calls, w.stderr.String())
	}
	if run := w.lastRecord(EventRun); run.Text("outcome") != OutcomeExited0 || run["key"] != nil || run["locked"] != true {
		t.Fatalf("run %v", run)
	}
	if !slices.Contains(w.fake.spec.Env, flock.HeldVariable+"=/x/suite.lock:1") {
		t.Fatal("an inherited holder was not passed on")
	}
}
