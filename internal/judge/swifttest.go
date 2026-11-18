package judge

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/supervise"
)

// The line forms of SwiftPM, XCTest and Swift Testing, in one table (testing.md section 2). A
// Swift release that changes a form is a change here, made once for every project. RE2 has no
// lookahead: the words a Swift Testing per-test line must not start with, and its `started.`
// end, are checked after the match (excludedName).
var (
	bundleStarted = regexp.MustCompile(`^Test Suite '(?P<name>.+\.xctest)' started at `)
	bundleEnded   = regexp.MustCompile(`^Test Suite '(?P<name>.+\.xctest)' (?P<how>passed|failed) at `)
	suiteFailed   = regexp.MustCompile(`^Test Suite '(?P<name>.+)' failed at `)
	executedLine  = regexp.MustCompile(`^\s*Executed (?P<executed>\d+) tests?, with (?:(?P<skipped>\d+) tests? skipped and )?(?P<failures>\d+) failures?\b`)
	caseStarted   = regexp.MustCompile(`^Test Case '(?P<name>.+)' started\.?\s*$`)
	caseEnded     = regexp.MustCompile(`^Test Case '(?P<name>.+)' (?P<how>passed|failed|skipped) \((?P<seconds>\d+(?:\.\d+)?) seconds\)`)
	objcName      = regexp.MustCompile(`^-\[(?P<class>[^\s\]]+) (?P<test>[^\]]+)\]$`)
	// Swift Testing: a symbol (one of ◇ ✔ ✘ ➜ ━ …, or an SF Symbol's private-use character), a space.
	runStarted    = regexp.MustCompile(`^\S{1,2} Test run started\.?\s*$`)
	runSummary    = regexp.MustCompile(`^\S{1,2} Test run with (?P<total>\d+) tests? (?:in \d+ suites? )?(?P<how>passed|failed) after `)
	testEnded     = regexp.MustCompile(`^\S{1,2} Test (?P<name>.+?) (?P<how>passed|failed) after \d+(?:\.\d+)? seconds`)
	testSkipped   = regexp.MustCompile(`^\S{1,2} Test (?P<name>.+?) skipped(?:[.:]|\s*$)`)
	startedAtEnd  = regexp.MustCompile(` started\.\s*$`)
	parallelLines = regexp.MustCompile(`^\[\d+/\d+\] Testing \S`)
)

// excludedName says whether a Swift Testing line's text after "Test " is no test's end or skip:
// the run's own lines, a parameterised case's line, or a `started.` line (its display name may
// hold any words).
func excludedName(rest string, skip bool) bool {
	if strings.HasPrefix(rest, "run with ") || strings.HasPrefix(rest, "case passing ") {
		return true
	}
	if !skip && strings.HasPrefix(rest, "run started") {
		return true
	}
	return startedAtEnd.MatchString(rest)
}

// matchFrom is the pattern's match of the line and the text from the start of its `name` group,
// or nil.
func matchFrom(pattern *regexp.Regexp, line string) ([]string, string) {
	indexes := pattern.FindStringSubmatchIndex(line)
	if indexes == nil {
		return nil, ""
	}
	groups := make([]string, len(indexes)/2)
	for index := range groups {
		if indexes[2*index] >= 0 {
			groups[index] = line[indexes[2*index]:indexes[2*index+1]]
		}
	}
	return groups, line[indexes[2*pattern.SubexpIndex("name")]:]
}

func group(pattern *regexp.Regexp, match []string, name string) string {
	return match[pattern.SubexpIndex(name)]
}

func number(text string) int {
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return value
}

type bundleTotal struct {
	executed, skipped int
}

type testRun struct {
	total                   int
	passed, failed, skipped int
	ended, unstarted        bool
}

// swiftTest is the reading of the `swift-test` judge.
type swiftTest struct {
	expect    []Engine
	open      []string
	started   int
	ended     int
	waiting   map[supervise.Stream]string // the bundle whose total is the next line of that stream
	totals    []bundleTotal
	noTotal   []string
	unstarted []string
	failure   string
	outside   string
	parallel  string
	runs      []*testRun
	current   *testRun
	cases     map[string]map[string]float64
}

func newSwiftTest(expect []Engine) *swiftTest {
	if len(expect) == 0 {
		expect = []Engine{XCTest}
	}
	return &swiftTest{expect: expect, waiting: map[supervise.Stream]string{}, cases: map[string]map[string]float64{}}
}

func (reading *swiftTest) fail(text string) {
	if reading.failure == "" {
		reading.failure = text
	}
}

func (reading *swiftTest) feed(stream supervise.Stream, text string) {
	line := strings.TrimRight(text, "\r\n")
	if bundle, waiting := reading.waiting[stream]; waiting {
		delete(reading.waiting, stream)
		// A bundle's total is the very next line of its stream: anything else first, a blank line
		// too, means it was not printed, and a later `Executed` line is someone else's.
		match := executedLine.FindStringSubmatch(line)
		if match == nil {
			reading.noTotal = append(reading.noTotal, bundle)
		} else {
			executed, skipped := number(group(executedLine, match, "executed")), number(group(executedLine, match, "skipped"))
			reading.totals = append(reading.totals, bundleTotal{executed: executed, skipped: skipped})
			if failures := number(group(executedLine, match, "failures")); failures > 0 {
				reading.fail(fmt.Sprintf("%s: %d failure%s", bundle, failures, plural(failures)))
			}
			return
		}
	}
	if line == "" {
		return
	}
	if reading.feedXCTest(stream, line) {
		return
	}
	if parallelLines.MatchString(line) {
		if reading.parallel == "" {
			reading.parallel = clip(line)
		}
		return
	}
	reading.feedSwiftTesting(line)
}

func (reading *swiftTest) feedXCTest(stream supervise.Stream, line string) bool {
	if match := bundleStarted.FindStringSubmatch(line); match != nil {
		reading.open = append(reading.open, group(bundleStarted, match, "name"))
		reading.started++
		return true
	}
	if match := bundleEnded.FindStringSubmatch(line); match != nil {
		name := group(bundleEnded, match, "name")
		if index := indexOf(reading.open, name); index >= 0 {
			reading.open = append(reading.open[:index], reading.open[index+1:]...)
		} else {
			reading.unstarted = append(reading.unstarted, name)
		}
		reading.ended++
		reading.waiting[stream] = name
		if group(bundleEnded, match, "how") == "failed" {
			reading.fail(fmt.Sprintf("Test Suite '%s' failed", name))
		}
		return true
	}
	if match := suiteFailed.FindStringSubmatch(line); match != nil {
		reading.fail(fmt.Sprintf("Test Suite '%s' failed", group(suiteFailed, match, "name")))
		return true
	}
	if match := caseEnded.FindStringSubmatch(line); match != nil {
		name, how := group(caseEnded, match, "name"), group(caseEnded, match, "how")
		if how == "failed" {
			reading.fail(fmt.Sprintf("Test Case '%s' failed", name))
		}
		if objc := objcName.FindStringSubmatch(name); objc != nil && how != "skipped" {
			class := group(objcName, objc, "class")
			if reading.cases[class] == nil {
				reading.cases[class] = map[string]float64{}
			}
			seconds, _ := strconv.ParseFloat(group(caseEnded, match, "seconds"), 64)
			reading.cases[class][group(objcName, objc, "test")] = seconds
		}
		return true
	}
	if match := caseStarted.FindStringSubmatch(line); match != nil {
		if len(reading.open) == 0 && reading.outside == "" {
			reading.outside = group(caseStarted, match, "name")
		}
		return true
	}
	return false
}

func (reading *swiftTest) feedSwiftTesting(line string) {
	if runStarted.MatchString(line) {
		reading.current = &testRun{}
		reading.runs = append(reading.runs, reading.current)
		return
	}
	if match := runSummary.FindStringSubmatch(line); match != nil {
		run := reading.current
		if run == nil || run.ended {
			run = &testRun{unstarted: true}
			reading.runs = append(reading.runs, run)
		}
		run.total, run.ended = number(group(runSummary, match, "total")), true
		if group(runSummary, match, "how") == "failed" {
			reading.fail(clip(line))
		}
		reading.current = nil
		return
	}
	if match, rest := matchFrom(testEnded, line); match != nil && !excludedName(rest, false) {
		run := reading.openRun()
		if group(testEnded, match, "how") == "failed" {
			run.failed++
			reading.fail(clip(line))
		} else {
			run.passed++
		}
		return
	}
	if match, rest := matchFrom(testSkipped, line); match != nil && !excludedName(rest, true) {
		reading.openRun().skipped++
	}
}

// openRun is the Swift Testing run a per-test line belongs to. With no run open it is counted in a
// run of its own that never started, so the reading says the output is not one it can judge.
func (reading *swiftTest) openRun() *testRun {
	if reading.current == nil {
		reading.current = &testRun{unstarted: true}
		reading.runs = append(reading.runs, reading.current)
	}
	return reading.current
}

// counts is what the output shows: the counts and why it cannot be judged (none when it can).
func (reading *swiftTest) counts() (counts Counts, why []string) {
	counts.BundlesStarted, counts.BundlesEnded = reading.started, reading.ended
	if len(reading.open) > 0 {
		why = append(why, fmt.Sprintf("the bundle %s started and did not end", reading.open[0]))
	}
	if len(reading.unstarted) > 0 {
		why = append(why, fmt.Sprintf("the bundle %s ended and never started", reading.unstarted[0]))
	}
	pending := append([]string{}, reading.noTotal...)
	for _, stream := range []supervise.Stream{supervise.Stdout, supervise.Stderr} {
		if bundle, waiting := reading.waiting[stream]; waiting {
			pending = append(pending, bundle)
		}
	}
	if len(pending) > 0 {
		why = append(why, fmt.Sprintf("the bundle %s ended without its `Executed N tests` line", pending[0]))
	}
	if reading.outside != "" {
		why = append(why, fmt.Sprintf("the XCTest case %s ran outside any bundle (outside every judged total)", reading.outside))
	}
	if reading.parallel != "" {
		why = append(why, fmt.Sprintf("SwiftPM's parallel progress lines ('%s'): `--parallel` hides the lines a pass is proven by", reading.parallel))
	}
	for _, total := range reading.totals {
		counts.XCTest += total.executed - total.skipped
		counts.Skipped += total.skipped
	}
	summarised := false
	for _, run := range reading.runs {
		switch {
		case run.unstarted:
			why = append(why, "a Swift Testing line with no `Test run started` before it")
			continue
		case !run.ended:
			why = append(why, "a Swift Testing run started and did not reach its summary (`Test run with N tests …`)")
			continue
		}
		summarised = true
		done := run.passed + run.failed
		if run.total == 0 {
			continue // a proven zero
		}
		if done+run.skipped != run.total {
			why = append(why, fmt.Sprintf("Swift Testing's summary says %d tests, and its per-test lines account for %d (%d ran, %d skipped)",
				run.total, done+run.skipped, done, run.skipped))
			continue
		}
		counts.SwiftTesting += done
		counts.Skipped += run.skipped
	}
	if expects(reading.expect, XCTest) && reading.started == 0 {
		why = append(why, "no XCTest bundle started (`Test Suite 'X.xctest' started`), and the scope expects XCTest")
	}
	if expects(reading.expect, SwiftTesting) && !summarised {
		why = append(why, "no Swift Testing run (`Test run started` … `Test run with N tests`), and the scope expects Swift Testing")
	}
	counts.Counted = counts.XCTest + counts.SwiftTesting
	return counts, why
}

func expects(engines []Engine, engine Engine) bool {
	return indexOf(engines, engine) >= 0
}

func indexOf[Item comparable](items []Item, wanted Item) int {
	for index, item := range items {
		if item == wanted {
			return index
		}
	}
	return -1
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

// clip is a line as a reason holds it: trimmed, at most 200 characters.
func clip(line string) string {
	runes := []rune(strings.TrimSpace(line))
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return string(runes)
}
