// Package judge reads a test command's output line by line and says which outcome it supports
// (testing.md section 2, docs/design/implementation.md section 9.3). Three judges: `swift-test` (SwiftPM's,
// XCTest's and Swift Testing's printed forms, one table in swifttest.go), `steps` (a project's
// `evidence` pattern counts the tests) and `exit` (the status only: `exited-0`, never `passed`). A
// project's `evidence` and `veto` patterns are added to any judge. It is a pure library: a Reader
// is a supervise.Sink, and its Verdict combines the reading with how the command ended.
package judge

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/testreport"
)

// A Kind is a judge, as jackstay.json's `judge` names it.
type Kind string

// The judges.
const (
	SwiftTest    Kind = "swift-test"
	Steps        Kind = "steps"
	Exit         Kind = "exit"
	GoJSON       Kind = "go-json"
	JUnit        Kind = "junit"
	JestJSON     Kind = "jest-json"
	TRX          Kind = "trx"
	XCResultJSON Kind = "xcresult-json"
)

// AdapterVersion identifies structured evidence in receipt and gate keys. Zero denotes a legacy judge.
func AdapterVersion(kind Kind) int {
	switch kind {
	case JUnit, JestJSON:
		return 2
	case GoJSON, TRX, XCResultJSON:
		return 1
	default:
		return 0
	}
}

// An Engine is a test engine a `swift-test` scope expects to see run.
type Engine string

// The engines.
const (
	XCTest       Engine = "xctest"
	SwiftTesting Engine = "swift-testing"
)

// An Outcome is a run's verdict.
type Outcome string

// The outcomes, by the order of verdicts: failed > timed-out > stopped > unjudged > nothing-run >
// passed (exited-0 is the `exit` judge's passed).
const (
	Failed     Outcome = "failed"
	TimedOut   Outcome = "timed-out"
	Stopped    Outcome = "stopped"
	Unjudged   Outcome = "unjudged"
	NothingRun Outcome = "nothing-run"
	Passed     Outcome = "passed"
	ExitedZero Outcome = "exited-0"
)

// Options are a scope's judge and patterns, as jackstay.json declares them. The patterns are RE2.
type Options struct {
	Kind Kind
	// GoSelection requires the Go JSON stream to execute this case in this package.
	GoSelection *GoSelection
	// JestSelection checks the selected file and assertion against the report.
	JestSelection *JestSelection
	// GoDurations collects bounded elapsed-time samples from completed Go test leaves.
	GoDurations bool
	// XCResultBundles names the test bundles the configured scheme must execute.
	XCResultBundles []string
	// XCResultDurations collects completed Xcode test case timings.
	XCResultDurations bool
	// JUnitDurations collects the reported duration of completed JUnit cases.
	JUnitDurations bool
	// Expect are the engines a `swift-test` scope runs; none means XCTest.
	Expect []Engine
	// Evidence holds one group for a number; the numbers are added over every matching line of
	// both streams. Needed by `steps`.
	Evidence string
	// Vetoes name lines that mean failure whatever the status says.
	Vetoes []string
	// EmptyExit is an explicitly configured JUnit producer status for zero collected tests.
	EmptyExit int
}

// A Reader hears the output (supervise.Sink) and gives the verdict. Its methods may be called from
// several goroutines.
type Reader struct {
	mutex             sync.Mutex
	kind              Kind
	evidence          *regexp.Regexp
	vetoes            []*regexp.Regexp
	swift             *swiftTest
	goJSON            *goJSONReader
	report            *Facts
	jestSelection     *JestSelection
	junitTimings      bool
	junitSamples      []JUnitCaseDuration
	junitTruncated    bool
	xcresultBundles   []string
	xcresultTimings   bool
	xcresultSamples   []XCResultCaseDuration
	xcresultTruncated bool
	emptyExit         int

	vetoed        bool
	vetoLine      string
	evidenceCount int
	uncounted     string
	notNumber     bool

	stopping      bool
	failureAtStop string
	vetoedAtStop  bool
	vetoAtStop    string
}

// New is a Reader for the options, or why they cannot judge.
func New(options Options) (*Reader, error) {
	reader := &Reader{kind: options.Kind, emptyExit: options.EmptyExit,
		jestSelection: options.JestSelection, junitTimings: options.JUnitDurations,
		xcresultTimings: options.XCResultDurations,
		xcresultBundles: append([]string(nil), options.XCResultBundles...)}
	if options.JestSelection != nil && options.Kind != JestJSON {
		return nil, errors.New("judge: Jest selection requires jest-json")
	}
	if len(options.XCResultBundles) != 0 && options.Kind != XCResultJSON {
		return nil, errors.New("judge: Xcode bundles require xcresult-json")
	}
	if options.Kind == XCResultJSON {
		if len(options.XCResultBundles) == 0 || len(options.XCResultBundles) > 32 {
			return nil, errors.New("judge: xcresult-json needs 1 to 32 expected bundle names")
		}
		seen := make(map[string]bool, len(options.XCResultBundles))
		for _, bundle := range options.XCResultBundles {
			if bundle == "" || len(bundle) > 512 || seen[bundle] {
				return nil, errors.New("judge: Xcode bundle names must be distinct and nonempty")
			}
			seen[bundle] = true
		}
	}
	if options.XCResultDurations && options.Kind != XCResultJSON {
		return nil, errors.New("judge: Xcode durations require xcresult-json")
	}
	if options.JUnitDurations && options.Kind != JUnit {
		return nil, errors.New("judge: JUnit durations require junit")
	}
	if options.Kind != GoJSON && (options.GoSelection != nil || options.GoDurations) {
		return nil, errors.New("judge: Go selection and durations require go-json")
	}
	if selection := options.GoSelection; selection != nil {
		if selection.Package == "" || selection.Name == "" ||
			len(selection.Package) > maxGoJSONName || len(selection.Name) > maxGoJSONName ||
			strings.ContainsAny(selection.Package, " \t\r\n") ||
			strings.HasPrefix(selection.Name, "/") || strings.HasSuffix(selection.Name, "/") ||
			strings.Contains(selection.Name, "//") {
			return nil, errors.New("judge: Go selection needs a package and a slash-delimited test name")
		}
	}
	switch options.Kind {
	case SwiftTest:
		for _, engine := range options.Expect {
			if engine != XCTest && engine != SwiftTesting {
				return nil, fmt.Errorf("judge: the engine %q is none of %s, %s", engine, XCTest, SwiftTesting)
			}
		}
		reader.swift = newSwiftTest(options.Expect)
	case Steps, Exit, GoJSON, JUnit, JestJSON, TRX, XCResultJSON:
		if len(options.Expect) > 0 {
			return nil, errors.New("judge: `expect` is for the swift-test judge")
		}
		if options.Kind == GoJSON {
			reader.goJSON = &goJSONReader{selection: options.GoSelection, withDurations: options.GoDurations}
		}
	default:
		return nil, fmt.Errorf("judge: %q is none of %s, %s, %s, %s, %s, %s, %s, %s", options.Kind, SwiftTest, Steps, Exit, GoJSON, JUnit, JestJSON, TRX, XCResultJSON)
	}
	if AdapterVersion(options.Kind) != 0 && options.Evidence != "" {
		return nil, errors.New("judge: a structured judge cannot use an evidence pattern")
	}
	if options.EmptyExit != 0 && (options.Kind != JUnit || options.EmptyExit < 1 || options.EmptyExit > 125) {
		return nil, errors.New("judge: an empty-selection exit must be between 1 and 125 and requires JUnit")
	}
	if options.Evidence != "" {
		pattern, err := regexp.Compile(options.Evidence)
		if err != nil {
			return nil, fmt.Errorf("judge: the evidence pattern cannot be read: %w", err)
		}
		if pattern.NumSubexp() < 1 {
			return nil, errors.New("judge: the evidence pattern has no group: it must capture the number of tests")
		}
		reader.evidence = pattern
	} else if options.Kind == Steps {
		return nil, errors.New("judge: the steps judge needs an evidence pattern that counts its tests")
	}
	for _, veto := range options.Vetoes {
		pattern, err := regexp.Compile(veto)
		if err != nil {
			return nil, fmt.Errorf("judge: the veto pattern cannot be read: %w", err)
		}
		reader.vetoes = append(reader.vetoes, pattern)
	}
	return reader, nil
}

// Line reads one line of a stream, escape sequences already removed (supervise.Clean). It never
// fails.
func (reader *Reader) Line(stream supervise.Stream, text string) error {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if reader.swift != nil {
		reader.swift.feed(stream, text)
	}
	if reader.goJSON != nil && stream == supervise.Stdout {
		reader.goJSON.line(text)
	}
	if !reader.vetoed {
		for _, veto := range reader.vetoes {
			if veto.MatchString(text) {
				reader.vetoed, reader.vetoLine = true, clip(text)
				break
			}
		}
	}
	if reader.evidence != nil {
		for _, match := range reader.evidence.FindAllStringSubmatch(text, -1) {
			count, err := strconv.Atoi(strings.TrimSpace(match[1]))
			if err != nil || count < 0 {
				if !reader.notNumber {
					reader.notNumber, reader.uncounted = true, clip(text)
				}
				continue
			}
			reader.evidenceCount += count
		}
	}
	return nil
}

// Stopping keeps what the output had shown when jackstay began its own stop (the timeout or a signal):
// only a failure line or a veto seen by then fails a stopped run. The first call counts.
func (reader *Reader) Stopping() {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if reader.stopping {
		return
	}
	reader.stopping = true
	if reader.swift != nil {
		reader.failureAtStop = reader.swift.failure
	}
	if reader.goJSON != nil {
		reader.failureAtStop = reader.goJSON.facts().Failure
	}
	reader.vetoedAtStop, reader.vetoAtStop = reader.vetoed, reader.vetoLine
}

// Durations are the seconds of each XCTest case that passed or failed, by class and test
// (testing.md section 4); none for the other judges.
func (reader *Reader) Durations() map[string]map[string]float64 {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	durations := map[string]map[string]float64{}
	if reader.swift == nil {
		return durations
	}
	for class, tests := range reader.swift.cases {
		durations[class] = map[string]float64{}
		for test, seconds := range tests {
			durations[class][test] = seconds
		}
	}
	return durations
}

// GoDurations returns witnessed Go case timings. Invalid or incomplete streams have no usable samples.
func (reader *Reader) GoDurations() ([]GoCaseDuration, bool) {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if reader.goJSON == nil || !reader.goJSON.withDurations || len(reader.goJSON.facts().Unjudged) != 0 {
		return nil, false
	}
	return append([]GoCaseDuration(nil), reader.goJSON.durations...), reader.goJSON.durationsTruncated
}

// JUnitDurations returns bounded timings from the accepted set of JUnit reports.
func (reader *Reader) JUnitDurations() ([]JUnitCaseDuration, bool) {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if reader.report == nil || len(reader.report.Unjudged) != 0 {
		return nil, false
	}
	return append([]JUnitCaseDuration(nil), reader.junitSamples...), reader.junitTruncated
}

// XCResultDurations returns bounded measurements from a valid Xcode result.
func (reader *Reader) XCResultDurations() ([]XCResultCaseDuration, bool) {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if reader.report == nil || len(reader.report.Unjudged) != 0 {
		return nil, false
	}
	return append([]XCResultCaseDuration(nil), reader.xcresultSamples...), reader.xcresultTruncated
}

var _ supervise.Sink = (*Reader)(nil)

// ReadReport decodes every declared report after the producer exits and before directory cleanup.
func (reader *Reader) ReadReport(report *testreport.Report) {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	facts := Facts{Kind: reader.kind, Counts: Counts{Counting: true}}
	reader.junitSamples = nil
	reader.junitTruncated = false
	reader.xcresultSamples = nil
	reader.xcresultTruncated = false
	if reader.kind == XCResultJSON {
		if report.Count() != 2 {
			reader.report = &Facts{Kind: XCResultJSON, Counts: Counts{Counting: true},
				Unjudged: []string{"the Xcode result needs a summary and test tree"}}
			return
		}
		summary, summaryError := report.ReadAt(0)
		if summaryError != nil {
			reader.report = &Facts{Kind: XCResultJSON, Counts: Counts{Counting: true},
				Unjudged: []string{"the Xcode result summary is missing or unreadable"}}
			return
		}
		defer func() { _ = summary.Close() }()
		tests, testsError := report.ReadAt(1)
		if testsError != nil {
			reader.report = &Facts{Kind: XCResultJSON, Counts: Counts{Counting: true},
				Unjudged: []string{"the Xcode result test tree is missing or unreadable"}}
			return
		}
		defer func() { _ = tests.Close() }()
		decoded, samples, truncated := readXCResultForBundles(summary, tests, reader.xcresultBundles)
		reader.report = &decoded
		if reader.xcresultTimings && len(decoded.Unjudged) == 0 {
			reader.xcresultSamples, reader.xcresultTruncated = samples, truncated
		}
		return
	}
	seen := make(map[string]struct{})
	duplicate, tooMany := false, false
	for index := range report.Count() {
		file, err := report.ReadAt(index)
		if err != nil {
			facts.Unjudged = append(facts.Unjudged, "a declared test report is missing or unreadable")
			continue
		}
		var part Facts
		switch reader.kind {
		case JestJSON:
			part = readJestSelected(file, reader.jestSelection)
		case TRX:
			part = readTRX(file)
		default:
			cases := make(map[string]struct{})
			if reader.junitTimings {
				var samples []JUnitCaseDuration
				var truncated bool
				part, samples, truncated = readJUnitTimed(file, cases)
				room := junitDurationLimit - len(reader.junitSamples)
				reader.junitSamples = append(reader.junitSamples, samples[:min(room, len(samples))]...)
				reader.junitTruncated = reader.junitTruncated || truncated || len(samples) > room
			} else {
				part = readJUnitCases(file, cases)
			}
			if report.Count() > 1 && len(part.Unjudged) == 0 {
				for identity := range cases {
					if _, exists := seen[identity]; exists {
						duplicate = true
					} else if len(seen) == junitCaseLimit {
						tooMany = true
					} else {
						seen[identity] = struct{}{}
					}
				}
			}
		}
		_ = file.Close()
		facts.Counts.Counted += part.Counts.Counted
		facts.Counts.Skipped += part.Counts.Skipped
		if facts.Failure == "" {
			facts.Failure = part.Failure
		}
		facts.Unjudged = append(facts.Unjudged, part.Unjudged...)
	}
	if duplicate {
		facts.Unjudged = append(facts.Unjudged, "declared JUnit reports repeat a test case")
	}
	if tooMany {
		facts.Unjudged = append(facts.Unjudged, "declared JUnit reports exceed the combined test case limit")
	}
	reader.report = &facts
}
