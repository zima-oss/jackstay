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
)

// A Kind is a judge, as jackstay.json's `judge` names it.
type Kind string

// The judges.
const (
	SwiftTest Kind = "swift-test"
	Steps     Kind = "steps"
	Exit      Kind = "exit"
)

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
	// Expect are the engines a `swift-test` scope runs; none means XCTest.
	Expect []Engine
	// Evidence holds one group for a number; the numbers are added over every matching line of
	// both streams. Needed by `steps`.
	Evidence string
	// Vetoes name lines that mean failure whatever the status says.
	Vetoes []string
}

// A Reader hears the output (supervise.Sink) and gives the verdict. Its methods may be called from
// several goroutines.
type Reader struct {
	mutex    sync.Mutex
	kind     Kind
	evidence *regexp.Regexp
	vetoes   []*regexp.Regexp
	swift    *swiftTest

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
	reader := &Reader{kind: options.Kind}
	switch options.Kind {
	case SwiftTest:
		for _, engine := range options.Expect {
			if engine != XCTest && engine != SwiftTesting {
				return nil, fmt.Errorf("judge: the engine %q is none of %s, %s", engine, XCTest, SwiftTesting)
			}
		}
		reader.swift = newSwiftTest(options.Expect)
	case Steps, Exit:
		if len(options.Expect) > 0 {
			return nil, errors.New("judge: `expect` is for the swift-test judge")
		}
	default:
		return nil, fmt.Errorf("judge: %q is none of %s, %s, %s", options.Kind, SwiftTest, Steps, Exit)
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
			if err != nil {
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

var _ supervise.Sink = (*Reader)(nil)
