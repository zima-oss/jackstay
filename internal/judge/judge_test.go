package judge

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/zima-oss/jackstay/internal/supervise"
)

// fixture is an output of `swift test` in tests/fixtures/swift-test (the shape of real runs, with
// neutral names), its `@passing` lines expanded.
func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "swift-test", name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	return expandPassing(t, string(data))
}

// expandPassing writes out a fixture's `@passing <Bundle>.<Suite> <N>` lines: each becomes N
// XCTest cases that start and pass, their methods numbered on through the file (test0001, …). The
// large runs keep their volume (thousands of cases in many bundles) without thousands of lines.
func expandPassing(t *testing.T, text string) string {
	t.Helper()
	var out []string
	method := 0
	for _, line := range lines(text) {
		rest, found := strings.CutPrefix(line, "@passing ")
		if !found {
			out = append(out, line)
			continue
		}
		class, count, ok := strings.Cut(rest, " ")
		cases, err := strconv.Atoi(count)
		if !ok || err != nil || cases <= 0 {
			t.Fatalf("a fixture line %q is no `@passing <Bundle>.<Suite> <N>`", line)
		}
		for range cases {
			method++
			name := fmt.Sprintf("-[%s test%04d]", class, method)
			out = append(out, "Test Case '"+name+"' started.", "Test Case '"+name+"' passed (0.001 seconds).")
		}
	}
	return strings.Join(out, "\n")
}

func lines(text string) []string { return strings.Split(text, "\n") }

func without(text string, drop func(string) bool) string {
	var kept []string
	for _, line := range lines(text) {
		if !drop(line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func reader(t *testing.T, options Options) *Reader {
	t.Helper()
	r, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// judgeText feeds every line of the text on standard output and judges it with the end.
func judgeText(t *testing.T, options Options, text string, end End) Verdict {
	t.Helper()
	r := reader(t, options)
	for _, line := range lines(text) {
		if err := r.Line(supervise.Stdout, line); err != nil {
			t.Fatal(err)
		}
	}
	return r.Verdict(end)
}

func swift(expect ...Engine) Options { return Options{Kind: SwiftTest, Expect: expect} }

func TestRecordedOutputs(t *testing.T) {
	both := []Engine{XCTest, SwiftTesting}
	cases := []struct {
		name    string
		expect  []Engine
		status  int
		outcome Outcome
		detail  string
		failure string // a part of the first failing line
		why     string // a part of the reason
	}{
		// The app's filtered run the old `Executed` veto failed: each bundle's own total less its
		// skipped, 767+35+23+(329-1)+51+58+0+0+1028 (testing.md section 2).
		{"app-filtered-passed", nil, 0, Passed, "counted=2290 bundles=9/9 xctest=2290 swift-testing=0 skipped=1", "", ""},
		{"app-full-passed", both, 0, Passed, "counted=2411 bundles=9/9 xctest=2411 swift-testing=0 skipped=1", "", ""},
		{"app-failed", nil, 0, Failed, "counted=1040 bundles=9/9 xctest=1040 swift-testing=0 skipped=0", "Suite149Tests", ""},
		{"lib-passed", nil, 0, Passed, "counted=2334 bundles=1/1 xctest=2334 swift-testing=0 skipped=4", "", ""},
		{"lib-failed", nil, 0, Failed, "counted=2346 bundles=1/1 xctest=2346 swift-testing=0 skipped=4", "Suite363Tests", ""},
		// An expected failure is no failure; the enclosing 'Selected tests' totals are not added.
		{"pkg-filtered-xctest", nil, 0, Passed, "counted=2 bundles=2/2 xctest=2 swift-testing=0 skipped=1", "", ""},
		{"pkg-mixed-failed", both, 0, Failed, "counted=9 bundles=1/1 xctest=3 swift-testing=6 skipped=2", "BetaTests testFails", ""},
		// A proven failure wins over the parallel lines.
		{"pkg-parallel", nil, 1, Failed, "counted=7 bundles=1/1 xctest=1 swift-testing=6 skipped=1", "BetaTests testFails", ""},
		{"pkg-swift-testing-only", []Engine{SwiftTesting}, 0, Passed, "counted=3 bundles=0/0 xctest=0 swift-testing=3 skipped=1", "", ""},
		{"pkg-swift-testing-only", nil, 0, Unjudged, "counted=3 bundles=0/0 xctest=0 swift-testing=3 skipped=1", "", "no XCTest bundle"},
		{"pkg-filtered-xctest", both, 0, Unjudged, "counted=2 bundles=2/2 xctest=2 swift-testing=0 skipped=1", "", "no Swift Testing run"},
		{"lib-passed", both, 0, Unjudged, "counted=2334 bundles=1/1 xctest=2334 swift-testing=0 skipped=4", "", "no Swift Testing run"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+string(tc.outcome), func(t *testing.T) {
			text := fixture(t, tc.name)
			verdict := judgeText(t, swift(tc.expect...), text, End{Status: tc.status})
			if verdict.Outcome != tc.outcome || verdict.Detail() != tc.detail {
				t.Fatalf("got %s %s (%s), want %s %s", verdict.Outcome, verdict.Detail(), verdict.Why, tc.outcome, tc.detail)
			}
			if !strings.Contains(verdict.Failure, tc.failure) || (tc.failure == "") != (verdict.Failure == "") {
				t.Errorf("failure %q, want one with %q", verdict.Failure, tc.failure)
			}
			if tc.failure != "" && !strings.Contains(verdict.Failure, "failed") {
				t.Errorf("failure %q says no failed", verdict.Failure)
			}
			if !strings.Contains(verdict.Why, tc.why) {
				t.Errorf("why %q, want %q", verdict.Why, tc.why)
			}
			if tc.outcome == Passed && verdict.XCTest > 0 {
				// The XCTest count is the number of its `passed` case lines.
				passed := 0
				for _, line := range lines(text) {
					if strings.HasPrefix(line, "Test Case '") && strings.Contains(line, "' passed (") {
						passed++
					}
				}
				if passed != verdict.XCTest {
					t.Errorf("%d passed case lines, counted %d", passed, verdict.XCTest)
				}
			}
		})
	}
}

// edit changes the recorded output at the line that starts with the prefix.
func edit(t *testing.T, name, prefix string, change func(lines []string, at int) []string) string {
	t.Helper()
	all := lines(fixture(t, name))
	at := slices.IndexFunc(all, func(line string) bool { return strings.HasPrefix(line, prefix) })
	if at < 0 {
		t.Fatalf("no line %q in %s", prefix, name)
	}
	return strings.Join(change(all, at), "\n")
}

func TestReadingRules(t *testing.T) {
	stOnly := fixture(t, "pkg-swift-testing-only")
	filtered := fixture(t, "pkg-filtered-xctest")
	lastAppStart := func() string {
		all := lines(fixture(t, "app-filtered-passed"))
		last := 0
		for index, line := range all {
			if strings.HasPrefix(line, "Test Suite 'IotaTests.xctest' started") {
				last = index
			}
		}
		return strings.Join(all[:last+200], "\n")
	}
	cases := []struct {
		name    string
		options Options
		text    string
		outcome Outcome
		why     string
	}{
		{"a run cut short inside a bundle", swift(), lastAppStart(), Unjudged, "the bundle IotaTests.xctest started and did not end"},
		{"a bundle's total is its own, not the enclosing suite's", swift(),
			edit(t, "pkg-filtered-xctest", "Test Suite 'OtherTests.xctest' passed", func(all []string, at int) []string {
				return slices.Delete(all, at+1, at+2)
			}), Unjudged, "OtherTests.xctest ended without its `Executed N tests` line"},
		{"a foreign count after a bundle is not its total", swift(),
			edit(t, "pkg-filtered-xctest", "Test Suite 'OtherTests.xctest' passed", func(all []string, at int) []string {
				return slices.Replace(all, at+1, at+2, "check: OK", "\t Executed 3 tests, with 1 test skipped and 0 failures (0 unexpected) in 0.020 (0.021) seconds")
			}), Unjudged, "OtherTests.xctest ended without"},
		{"a blank line before the total breaks the very next line", swift(),
			edit(t, "pkg-filtered-xctest", "Test Suite 'OtherTests.xctest' passed", func(all []string, at int) []string {
				return slices.Insert(all, at+1, "")
			}), Unjudged, "OtherTests.xctest ended without its `Executed N tests` line"},
		{"a bundle that ends without starting", swift(),
			without(filtered, func(line string) bool { return strings.HasPrefix(line, "Test Suite 'LibTests.xctest' started") }),
			Unjudged, "LibTests.xctest ended and never started"},
		{"a bundle without its total", swift(),
			without(filtered, func(line string) bool { return strings.Contains(line, "Executed 0 tests") }),
			Unjudged, "without its `Executed N tests` line"},
		{"a bundle ending at the end of the output", swift(),
			"Test Suite 'A.xctest' started at 1.\nTest Suite 'A.xctest' passed at 2.", Unjudged, "A.xctest ended without"},
		{"parallel progress lines with no failure", swift(),
			without(fixture(t, "pkg-parallel"), func(line string) bool {
				return strings.Contains(strings.ToLower(line), "fails") || strings.Contains(line, "failed")
			}), Unjudged, "SwiftPM's parallel progress lines ('[1/4] Testing OtherTests.AlphaTests/testSkip')"},
		{"a Swift Testing summary its lines do not account for", swift(SwiftTesting),
			without(stOnly, func(line string) bool { return strings.Contains(line, "inner()") }),
			Unjudged, "Swift Testing's summary says 4 tests, and its per-test lines account for 3 (2 ran, 1 skipped)"},
		{"a Swift Testing run without its summary", swift(SwiftTesting),
			without(stOnly, func(line string) bool { return strings.Contains(line, "Test run with") }),
			Unjudged, "did not reach its summary"},
		{"a Swift Testing line with no run started", swift(SwiftTesting),
			without(stOnly, func(line string) bool { return strings.Contains(line, "Test run started") }),
			Unjudged, "a Swift Testing line with no `Test run started` before it"},
		{"proven zeros in both engines are nothing run", swift(XCTest, SwiftTesting),
			"Test Suite 'A.xctest' started at 2026-01-01 00:00:00.000.\n" +
				"Test Suite 'A.xctest' passed at 2026-01-01 00:00:00.000.\n" +
				"\t Executed 0 tests, with 0 failures (0 unexpected) in 0.000 (0.000) seconds\n" +
				"◇ Test run started.\n✔ Test run with 0 tests in 0 suites passed after 0.001 seconds.\n",
			NothingRun, ""},
		{"a Swift Testing run of only skipped tests is a proven zero", swift(SwiftTesting),
			"◇ Test run started.\n➜ Test a() skipped: \"no\"\n➜ Test b() skipped.\n✔ Test run with 2 tests in 0 suites passed after 0.001 seconds.",
			NothingRun, ""},
		{"Swift Testing alone passes on its own count", swift(SwiftTesting),
			"◇ Test run started.\n✔ Test a() passed after 0.001 seconds.\n✔ Test run with 1 test passed after 0.001 seconds.", Passed, ""},
		{"a case outside every bundle", swift(),
			filtered + "\nTest Case '-[X.Y testZ]' started.\nTest Case '-[X.Y testZ]' passed (0.001 seconds).\n",
			Unjudged, "the XCTest case -[X.Y testZ] ran outside any bundle"},
		{"wrapper lines after the end", swift(), filtered + "\nmake[1]: Leaving directory\ncheck-docs: OK\n", Passed, ""},
		{"a display name with skipped or passed in it is no end", swift(SwiftTesting),
			strings.Replace(stOnly, "◇ Test passes() started.",
				"◇ Test passes() started.\n◇ Test \"handles skipped: x and passed after 1 seconds\" started.", 1), Passed, ""},
		{"a parameterised case's line is no test", swift(SwiftTesting),
			"◇ Test run started.\n✔ Test case passing 1 argument x → 1 to p(x:) passed after 0.001 seconds.\n" +
				"✔ Test p(x:) with 1 test case passed after 0.001 seconds.\n✔ Test run with 1 test passed after 0.001 seconds.",
			Passed, ""},
		{"a failing bundle total", swift(),
			"Test Suite 'A.xctest' started at 1.\nTest Suite 'A.xctest' passed at 2.\n\t Executed 2 tests, with 1 failure (0 unexpected) in 0.1 (0.1) seconds",
			Failed, "A.xctest: 1 failure"},
		{"a suite that failed", swift(),
			"Test Suite 'A.xctest' started at 1.\nTest Suite 'Inner' failed at 2.\nTest Suite 'A.xctest' passed at 2.\n\t Executed 1 test, with 0 failures (0 unexpected) in 0.1 (0.1) seconds",
			Failed, "Test Suite 'Inner' failed"},
		{"a bundle that failed", swift(),
			"Test Suite 'A.xctest' started at 1.\nTest Suite 'A.xctest' failed at 2.\n\t Executed 1 test, with 0 failures (0 unexpected) in 0.1 (0.1) seconds",
			Failed, "Test Suite 'A.xctest' failed"},
		{"a failed Swift Testing run", swift(SwiftTesting),
			"◇ Test run started.\n✔ Test a() passed after 0.001 seconds.\n✘ Test run with 1 test failed after 0.001 seconds with 1 issue.",
			Failed, "✘ Test run with 1 test failed"},
		{"a failed Swift Testing test", swift(SwiftTesting),
			"◇ Test run started.\n✘ Test a() failed after 0.001 seconds with 1 issue.\n✘ Test run with 1 test failed after 0.001 seconds with 1 issue.",
			Failed, "✘ Test a() failed after 0.001 seconds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict := judgeText(t, tc.options, tc.text, End{})
			if verdict.Outcome != tc.outcome || !strings.Contains(verdict.Why, tc.why) {
				t.Fatalf("got %s (%s), want %s (%s)", verdict.Outcome, verdict.Why, tc.outcome, tc.why)
			}
		})
	}
}

func TestATotalIsTheNextLineOfItsOwnStream(t *testing.T) {
	cases := []struct {
		name    string
		between []supervise.Stream // streams of the lines between the end and the total
		total   supervise.Stream
		outcome Outcome
	}{
		{"on the same stream", nil, supervise.Stdout, Passed},
		{"another stream's line in between", []supervise.Stream{supervise.Stderr}, supervise.Stdout, Passed},
		{"its own stream's line in between", []supervise.Stream{supervise.Stdout}, supervise.Stdout, Unjudged},
		{"on the other stream", nil, supervise.Stderr, Unjudged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := reader(t, swift())
			_ = r.Line(supervise.Stdout, "Test Suite 'A.xctest' started at 1.")
			_ = r.Line(supervise.Stdout, "Test Case '-[A.B testC]' started.")
			_ = r.Line(supervise.Stdout, "Test Case '-[A.B testC]' passed (0.5 seconds).")
			_ = r.Line(supervise.Stdout, "Test Suite 'A.xctest' passed at 2.")
			for _, stream := range tc.between {
				_ = r.Line(stream, "a line")
			}
			_ = r.Line(tc.total, "\t Executed 1 test, with 0 failures (0 unexpected) in 0.5 (0.5) seconds")
			if verdict := r.Verdict(End{}); verdict.Outcome != tc.outcome {
				t.Fatalf("got %s (%s), want %s", verdict.Outcome, verdict.Why, tc.outcome)
			}
		})
	}
}

func TestEvidenceAndVeto(t *testing.T) {
	steps := Options{Kind: Steps, Evidence: `ran ([0-9]+) tests`}
	cases := []struct {
		name    string
		options Options
		stdout  []string
		stderr  []string
		end     End
		outcome Outcome
		detail  string
		why     string
	}{
		{"summed over every line of both streams", steps, []string{"ran 2 tests", "x ran 3 tests ran 4 tests"}, []string{"ran 5 tests"}, End{}, Passed, "counted=14", ""},
		{"zero counted is nothing run", steps, []string{"ran 0 tests", "done"}, nil, End{}, NothingRun, "counted=0", ""},
		{"no line is nothing run", steps, nil, nil, End{}, NothingRun, "counted=0", ""},
		{"a group that is no number", Options{Kind: Steps, Evidence: `ran (\S+) tests`}, []string{"ran 3 tests", "ran many tests"}, nil, End{}, Unjudged, "counted=3",
			"the evidence pattern matched a line whose group is not a number: 'ran many tests'"},
		{"a group that did not take part", Options{Kind: Steps, Evidence: `ran (?:([0-9]+)|all) tests`}, []string{"ran all tests"}, nil, End{}, Unjudged, "counted=0", "not a number"},
		{"the read problem is said before the evidence", Options{Kind: Steps, Evidence: `ran (\S+) tests`}, []string{"ran x tests"}, nil,
			End{ReadProblem: "its output could not be read to its end"}, Unjudged, "counted=0", "its output could not be read to its end"},
		{"a veto fails a run that ended 0", Options{Kind: Steps, Evidence: `ran ([0-9]+) tests`, Vetoes: []string{`RECOGNIZER-INVARIANT`}},
			[]string{"ran 3 tests"}, []string{"  RECOGNIZER-INVARIANT broken  "}, End{}, Failed, "counted=3", "a veto line: RECOGNIZER-INVARIANT broken"},
		{"a second veto pattern", Options{Kind: Exit, Vetoes: []string{`never`, `^FAIL`}}, []string{"FAIL one", "FAIL two"}, nil, End{}, Failed, "counted=-", "a veto line: FAIL one"},
		{"exit is exited-0, never passed", Options{Kind: Exit}, []string{"Executed 5 tests"}, nil, End{}, ExitedZero, "counted=-", ""},
		{"exit with evidence counts and is still exited-0", Options{Kind: Exit, Evidence: `ran ([0-9]+)`}, []string{"ran 4"}, nil, End{}, ExitedZero, "counted=4", ""},
		{"exit fails on its status", Options{Kind: Exit}, nil, nil, End{Status: 2}, Failed, "counted=-", "exit 2"},
		{"evidence beside swift-test does not count its tests", Options{Kind: SwiftTest, Expect: []Engine{SwiftTesting}, Evidence: `ran ([0-9]+)`},
			[]string{"◇ Test run started.", "ran 9", "✔ Test a() passed after 0.001 seconds.", "✔ Test run with 1 test passed after 0.001 seconds."}, nil, End{}, Passed,
			"counted=1 bundles=0/0 xctest=0 swift-testing=1 skipped=0", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := reader(t, tc.options)
			for _, line := range tc.stdout {
				_ = r.Line(supervise.Stdout, line)
			}
			for _, line := range tc.stderr {
				_ = r.Line(supervise.Stderr, line)
			}
			verdict := r.Verdict(tc.end)
			if verdict.Outcome != tc.outcome || verdict.Detail() != tc.detail || !strings.Contains(verdict.Why, tc.why) {
				t.Fatalf("got %s %s (%s), want %s %s (%s)", verdict.Outcome, verdict.Detail(), verdict.Why, tc.outcome, tc.detail, tc.why)
			}
		})
	}
}

func TestEvaluateUsesOneVerdictForDecodedEvidence(t *testing.T) {
	cases := []struct {
		name    string
		facts   Facts
		end     End
		policy  Policy
		outcome Outcome
		why     string
	}{
		{"counted pass", Facts{Kind: Steps, Counts: Counts{Counting: true, Counted: 3}}, End{}, ScopePolicy, Passed, ""},
		{"no matching case", Facts{Kind: Steps, Counts: Counts{Counting: true}}, End{}, DirectPolicy, NothingRun, ""},
		{"no evidence", Facts{Kind: Exit}, End{}, DirectPolicy, ExitedZero, ""},
		{"veto", Facts{Kind: Steps, Counts: Counts{Counting: true, Counted: 3}, Vetoed: true, VetoLine: "FAIL"}, End{}, DirectPolicy, Failed, "a veto line: FAIL"},
		{"unreadable output", Facts{Kind: Steps, Counts: Counts{Counting: true, Counted: 3}}, End{ReadProblem: "stream incomplete"}, DirectPolicy, Unjudged, "stream incomplete"},
		{"cleanup failure", Facts{Kind: Steps, Counts: Counts{Counting: true, Counted: 3}}, End{LeftRunning: "child[7]"}, ScopePolicy, Unjudged, "child[7]"},
		{"scope failure before stop", Facts{Kind: Steps, Failure: "assertion failed"}, End{Signal: "SIGTERM"}, ScopePolicy, Failed, "assertion failed"},
		{"direct stop before failure", Facts{Kind: Steps, Failure: "assertion failed"}, End{Signal: "SIGTERM"}, DirectPolicy, Stopped, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			verdict := Evaluate(testCase.facts, testCase.end, testCase.policy)
			if verdict.Outcome != testCase.outcome || !strings.Contains(verdict.Why, testCase.why) {
				t.Fatalf("got %s (%s), want %s (%s)", verdict.Outcome, verdict.Why, testCase.outcome, testCase.why)
			}
		})
	}
}

func TestOrderOfVerdicts(t *testing.T) {
	unjudgedText := "Test Suite 'A.xctest' started at 1."
	passing := "◇ Test run started.\n✔ Test a() passed after 0.001 seconds.\n✔ Test run with 1 test passed after 0.001 seconds."
	failing := "◇ Test run started.\n✘ Test a() failed after 0.001 seconds.\n✘ Test run with 1 test failed after 0.001 seconds."
	cases := []struct {
		name     string
		options  Options
		before   string // lines read before jackstay's own stop (all of them when it did not stop)
		after    string // lines read after it
		end      End
		outcome  Outcome
		why      string
		vetoLine string
	}{
		{"unjudged", swift(), unjudgedText, "", End{}, Unjudged, "did not end", ""},
		{"a status of its own fails what is unjudged", swift(), unjudgedText, "", End{Status: 3}, Failed, "exit 3", ""},
		{"a timeout's status is no failure", swift(), unjudgedText, "", End{Status: 143, TimedOut: true}, TimedOut, "", ""},
		{"a signal's status is no failure", swift(), unjudgedText, "", End{Status: 143, Signal: "SIGTERM"}, Stopped, "", ""},
		{"a timeout wins over a signal", swift(), unjudgedText, "", End{TimedOut: true, Signal: "SIGTERM"}, TimedOut, "", ""},
		{"a failure seen before the stop fails it", swift(SwiftTesting), failing, "", End{Status: 143, TimedOut: true}, Failed, "✘ Test a() failed", ""},
		{"a failure seen after the stop does not", swift(SwiftTesting), "◇ Test run started.", failing, End{Status: 143, Signal: "SIGINT"}, Stopped, "", ""},
		{"a veto seen before the stop fails it", Options{Kind: Exit, Vetoes: []string{`BAD`}}, "BAD", "", End{TimedOut: true}, Failed, "a veto line: BAD", "BAD"},
		{"a veto seen after the stop does not", Options{Kind: Exit, Vetoes: []string{`BAD`}}, "fine", "BAD", End{TimedOut: true}, TimedOut, "", ""},
		{"a veto fails what is unjudged", Options{Kind: SwiftTest, Vetoes: []string{`RECOGNIZER-INVARIANT`}}, unjudgedText + "\nRECOGNIZER-INVARIANT", "", End{}, Failed,
			"a veto line: RECOGNIZER-INVARIANT", "RECOGNIZER-INVARIANT"},
		{"an attributed leftover fails it", swift(), unjudgedText, "", End{Leftovers: Leftovers{Failed: []string{"/a", "/b", "/c", "/d"}}}, Failed, "left behind: /a, /b, /c", ""},
		{"an attributed leftover beats a timeout", swift(), unjudgedText, "", End{TimedOut: true, Leftovers: Leftovers{Failed: []string{"/a"}}}, Failed, "left behind: /a", ""},
		{"processes left running", swift(SwiftTesting), passing, "", End{LeftRunning: "sleep[1]"}, Unjudged,
			"the command ended and left processes running, which were stopped: sleep[1]", ""},
		{"an unattributed leftover", swift(SwiftTesting), passing, "", End{Leftovers: Leftovers{Unattributed: []string{"/x"}, Overlap: []string{"run-2"}}}, Unjudged,
			"leftovers that another run may have made (runs overlapped: run-2): /x", ""},
		{"leftovers that cannot be looked for", swift(SwiftTesting), passing, "", End{Leftovers: Leftovers{Unreadable: []string{"/t/*: denied"}}}, Unjudged,
			"leftovers cannot be looked for: /t/*: denied", ""},
		{"output not read to its end", swift(SwiftTesting), passing, "", End{ReadProblem: "its output could not be read to its end"}, Unjudged, "could not be read", ""},
		{"unjudged beats nothing run", swift(), "Test Suite 'A.xctest' started at 1.\nTest Suite 'A.xctest' passed at 2.", "", End{}, Unjudged, "ended without", ""},
		{"passed", swift(SwiftTesting), passing, "", End{}, Passed, "", ""},
		{"exited-0", Options{Kind: Exit}, "anything", "", End{}, ExitedZero, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := reader(t, tc.options)
			for _, line := range lines(tc.before) {
				_ = r.Line(supervise.Stdout, line)
			}
			if tc.end.TimedOut || tc.end.Signal != "" {
				r.Stopping()
				r.Stopping() // the first call counts
			}
			for _, line := range lines(tc.after) {
				_ = r.Line(supervise.Stdout, line)
			}
			verdict := r.Verdict(tc.end)
			if verdict.Outcome != tc.outcome || !strings.Contains(verdict.Why, tc.why) || verdict.VetoLine != tc.vetoLine {
				t.Fatalf("got %s (%s, veto %q), want %s (%s, veto %q)", verdict.Outcome, verdict.Why, verdict.VetoLine, tc.outcome, tc.why, tc.vetoLine)
			}
		})
	}
}

func TestNew(t *testing.T) {
	cases := []struct {
		name    string
		options Options
		problem string
	}{
		{"swift-test", swift(XCTest, SwiftTesting), ""},
		{"steps", Options{Kind: Steps, Evidence: `([0-9]+)`}, ""},
		{"exit", Options{Kind: Exit, Vetoes: []string{`x`}}, ""},
		{"an unknown judge", Options{Kind: "pytest"}, "is none of"},
		{"an unknown engine", swift("junit"), "is none of"},
		{"expect beside another judge", Options{Kind: Exit, Expect: []Engine{XCTest}}, "is for the swift-test judge"},
		{"steps without evidence", Options{Kind: Steps}, "needs an evidence pattern"},
		{"evidence without a group", Options{Kind: Steps, Evidence: `ran [0-9]+`}, "has no group"},
		{"evidence that is no RE2", Options{Kind: Steps, Evidence: `(?!x)([0-9]+)`}, "cannot be read"},
		{"a veto that is no RE2", Options{Kind: Exit, Vetoes: []string{`(?<=a)b`}}, "cannot be read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.options)
			if (tc.problem == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.problem)) {
				t.Fatalf("got %v, want %q", err, tc.problem)
			}
		})
	}
}

func TestDurations(t *testing.T) {
	r := reader(t, swift())
	for _, line := range lines(fixture(t, "pkg-filtered-xctest")) {
		_ = r.Line(supervise.Stdout, line)
	}
	got := r.Durations()
	want := map[string]float64{"testExpected": 0.019, "testOne": 0}
	if len(got) != 1 || len(got["OtherTests.AlphaTests"]) != 2 {
		t.Fatalf("got %v", got)
	}
	for test, seconds := range want {
		if got["OtherTests.AlphaTests"][test] != seconds {
			t.Errorf("%s: got %v, want %v", test, got["OtherTests.AlphaTests"][test], seconds)
		}
	}
	if steps := reader(t, Options{Kind: Exit}).Durations(); len(steps) != 0 {
		t.Errorf("exit judge durations: %v", steps)
	}
}

// The two streams' readers may call it at once, and a verdict may be taken while one still reads.
func TestConcurrentStreams(t *testing.T) {
	r := reader(t, Options{Kind: Steps, Evidence: `ran ([0-9]+)`})
	var group sync.WaitGroup
	for _, stream := range []supervise.Stream{supervise.Stdout, supervise.Stderr} {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 500 {
				_ = r.Line(stream, "ran 1")
			}
		}()
	}
	_ = r.Verdict(End{})
	group.Wait()
	if verdict := r.Verdict(End{}); verdict.Counted != 1000 {
		t.Fatalf("counted %d", verdict.Counted)
	}
}

func TestClip(t *testing.T) {
	long := strings.Repeat("é", 250)
	if got := clip("  " + long + "  "); got != strings.Repeat("é", 200) {
		t.Fatalf("clip kept %d runes", len([]rune(got)))
	}
}
