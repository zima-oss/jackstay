package judge

import (
	"encoding/json/jsontext"
	"math"
	"strings"

	"github.com/zima-oss/jackstay/internal/strictjson"
)

// A goJSONReader consumes the stdout of one go test -json invocation. The limits bound
// memory even when the command produces an untrusted or unexpectedly large stream.
type goJSONReader struct {
	packages           map[string]*goJSONPackage
	selection          *GoSelection
	withDurations      bool
	durations          []GoCaseDuration
	durationsTruncated bool
	outsideSelection   bool
	events             int
	bytes              int
	problem            string
	failure            string
	cached             bool
}

type goJSONPackage struct {
	started bool
	ended   bool
	active  map[string]*goJSONCase
	passed  int
	failed  int
	skipped int
}

type goJSONCase struct {
	child    bool
	selected bool
}

type goJSONEvent struct {
	Action      string         `json:"Action"`
	Package     string         `json:"Package"`
	Test        string         `json:"Test"`
	Output      string         `json:"Output"`
	FailedBuild string         `json:"FailedBuild"`
	Elapsed     jsontext.Value `json:"Elapsed"`
}

// GoSelection identifies one Go test or subtest in one import-path package.
type GoSelection struct {
	Package string
	Name    string
}

// GoCaseDuration is one completed leaf execution. Repeated executions remain separate samples.
type GoCaseDuration struct {
	Package string  `json:"package"`
	Test    string  `json:"test"`
	Seconds float64 `json:"seconds"`
}

const (
	maxGoJSONEvents      = 1_000_000
	maxGoJSONBytes       = 128 << 20
	maxGoJSONPackages    = 4_096
	maxGoJSONCases       = 100_000
	maxGoJSONName        = 512
	maxGoDurationSamples = 512
)

func (reader *goJSONReader) line(text string) {
	if reader.problem != "" {
		return
	}
	reader.events++
	reader.bytes += len(text)
	if reader.events > maxGoJSONEvents || reader.bytes > maxGoJSONBytes {
		reader.problem = "the Go JSON event stream exceeds its limit"
		return
	}
	if len(text) > 1<<20 {
		reader.problem = "a Go JSON event is too large"
		return
	}
	var event goJSONEvent
	if err := strictjson.UnmarshalOpen([]byte(text), &event); err != nil {
		reader.problem = "a Go JSON event cannot be decoded"
		return
	}
	switch event.Action {
	case "build-output":
		return
	case "build-fail":
		reader.failure = "Go tests could not be built"
		return
	}
	if event.Package == "" || len(event.Package) > maxGoJSONName || len(event.Test) > maxGoJSONName {
		reader.problem = "a Go JSON event has no usable package or test identity"
		return
	}
	if reader.packages == nil {
		reader.packages = make(map[string]*goJSONPackage)
	}
	pkg := reader.packages[event.Package]
	if pkg == nil {
		if len(reader.packages) == maxGoJSONPackages {
			reader.problem = "the Go JSON stream names too many packages"
			return
		}
		pkg = &goJSONPackage{}
		reader.packages[event.Package] = pkg
	}
	if event.Action == "start" && event.Test == "" {
		if pkg.started {
			reader.problem = "a Go package starts more than once"
			return
		}
		pkg.started = true
		pkg.active = make(map[string]*goJSONCase)
		return
	}
	if !pkg.started || pkg.ended {
		reader.problem = "a Go package has events outside its run"
		return
	}
	if event.Test == "" {
		reader.packageEvent(pkg, event)
		return
	}
	reader.caseEvent(pkg, event)
}

func (reader *goJSONReader) packageEvent(pkg *goJSONPackage, event goJSONEvent) {
	switch event.Action {
	case "output":
		// go test replays test events from its cache. The package summary records
		// that fact even though the replayed events look like a fresh execution.
		fields := strings.Fields(event.Output)
		if len(fields) == 3 && fields[0] == "ok" && fields[1] == event.Package && fields[2] == "(cached)" {
			reader.cached = true
		}
	case "pass", "fail", "skip":
		pkg.ended = true
		if len(pkg.active) != 0 {
			reader.problem = "a Go package ended with unfinished tests"
		}
		if event.Action == "skip" && pkg.passed+pkg.failed+pkg.skipped > 0 {
			reader.problem = "a skipped Go package reported test results"
		}
		if event.Action == "fail" {
			reader.failure = "a Go test package failed"
		}
	case "bench":
		// Benchmarks are measurements and do not establish an executed test.
	default:
		reader.problem = "a Go package reported an unknown action"
	}
}

func (reader *goJSONReader) caseEvent(pkg *goJSONPackage, event goJSONEvent) {
	root := strings.SplitN(event.Test, "/", 2)[0]
	if strings.HasPrefix(root, "Benchmark") {
		if event.Action == "fail" {
			reader.failure = "a Go benchmark failed"
		}
		return
	}
	if !strings.HasPrefix(root, "Test") && !strings.HasPrefix(root, "Example") && !strings.HasPrefix(root, "Fuzz") {
		reader.problem = "a Go JSON event names an unknown test kind"
		return
	}
	switch event.Action {
	case "run":
		if pkg.active[event.Test] != nil || len(pkg.active) == maxGoJSONCases {
			reader.problem = "the Go JSON stream has duplicate or too many active tests"
			return
		}
		if parent := strings.LastIndexByte(event.Test, '/'); parent >= 0 {
			enclosing := pkg.active[event.Test[:parent]]
			if enclosing == nil {
				reader.problem = "a Go subtest has no running parent"
				return
			}
			enclosing.child = true
		}
		selected := true
		if reader.selection != nil {
			name := reader.selection.Name
			selected = event.Package == reader.selection.Package &&
				(event.Test == name || strings.HasPrefix(event.Test, name+"/"))
			ancestor := event.Package == reader.selection.Package && strings.HasPrefix(name, event.Test+"/")
			if !selected && !ancestor {
				reader.outsideSelection = true
			}
		}
		pkg.active[event.Test] = &goJSONCase{selected: selected}
	case "pause", "cont":
		if pkg.active[event.Test] == nil {
			reader.problem = "a Go test changed state without starting"
		}
	case "pass", "fail", "skip":
		active := pkg.active[event.Test]
		if active == nil {
			reader.problem = "a Go test ended without starting"
			return
		}
		delete(pkg.active, event.Test)
		if event.Action == "fail" {
			reader.failure = "a Go test failed"
		}
		if active.child {
			return
		}
		if !active.selected {
			return
		}
		switch event.Action {
		case "skip":
			pkg.skipped++
		case "fail":
			pkg.failed++
		case "pass":
			pkg.passed++
		}
		if reader.withDurations && event.Action != "skip" && len(event.Elapsed) > 0 && string(event.Elapsed) != "null" {
			var seconds float64
			if err := strictjson.Unmarshal(event.Elapsed, &seconds); err != nil ||
				seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
				reader.problem = "a Go JSON event has an invalid elapsed time"
				return
			}
			if len(reader.durations) < maxGoDurationSamples {
				reader.durations = append(reader.durations, GoCaseDuration{
					Package: event.Package, Test: event.Test, Seconds: seconds,
				})
			} else {
				reader.durationsTruncated = true
			}
		}
	case "output":
		// Output is retained by the normal run log, not as structured evidence.
	case "bench":
		reader.problem = "a Go test reported a benchmark action"
	default:
		reader.problem = "a Go test reported an unknown action"
	}
}

func (reader *goJSONReader) facts() Facts {
	facts := Facts{Kind: GoJSON, Counts: Counts{Counting: true}, Failure: reader.failure}
	if reader.problem != "" {
		facts.Unjudged = append(facts.Unjudged, reader.problem)
	}
	if len(reader.packages) == 0 {
		facts.Unjudged = append(facts.Unjudged, "the Go JSON stream has no package")
	}
	incomplete := false
	for _, pkg := range reader.packages {
		facts.Counts.Counted += pkg.passed + pkg.failed
		facts.Counts.Skipped += pkg.skipped
		incomplete = incomplete || !pkg.started || !pkg.ended || len(pkg.active) != 0
	}
	if incomplete {
		facts.Unjudged = append(facts.Unjudged, "the Go JSON stream has an incomplete package")
	}
	if reader.cached {
		facts.Unjudged = append(facts.Unjudged, "the Go test result came from the cache")
	}
	if reader.selection != nil {
		if _, found := reader.packages[reader.selection.Package]; !found {
			facts.Unjudged = append(facts.Unjudged, "the selected Go package did not report")
		}
		if reader.outsideSelection {
			facts.Unjudged = append(facts.Unjudged, "a Go test outside the selected case ran")
		}
	}
	return facts
}
