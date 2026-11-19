package judge

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These fixtures retain the result and counter structure of reports produced by
// .NET SDK 11.0.100 and MSTest 4.2.3. Host, path, time and run IDs are removed.
func TestTRXReadsMSTestReports(t *testing.T) {
	for _, sample := range []struct {
		name    string
		counted int
		skipped int
		outcome Outcome
	}{
		{"pass", 1, 1, Passed},
		{"fail", 1, 0, Failed},
		{"zero", 0, 0, NothingRun},
	} {
		t.Run(sample.name, func(t *testing.T) {
			report, err := os.Open(filepath.Join("testdata", "trx", sample.name+".trx"))
			if err != nil {
				t.Fatal(err)
			}
			defer report.Close()
			facts := readTRX(report)
			if facts.Kind != TRX || len(facts.Unjudged) != 0 || !facts.Counts.Counting ||
				facts.Counts.Counted != sample.counted || facts.Counts.Skipped != sample.skipped {
				t.Fatalf("facts: %+v", facts)
			}
			if verdict := Evaluate(facts, End{}, ScopePolicy); verdict.Outcome != sample.outcome {
				t.Fatalf("verdict: %+v", verdict)
			}
		})
	}
}

func TestTRXRejectsAmbiguousReports(t *testing.T) {
	const start = `<TestRun xmlns="http://microsoft.com/schemas/VisualStudio/TeamTest/2010">`
	const result = `<Results><UnitTestResult testId="one" executionId="run-one" testName="One" outcome="Passed"/></Results>`
	const summary = `<ResultSummary outcome="Completed"><Counters total="1" executed="1" passed="1" failed="0" notExecuted="0"/></ResultSummary>`
	valid := start + result + summary + `</TestRun>`
	for name, report := range map[string]string{
		"empty":              "",
		"truncated":          strings.TrimSuffix(valid, `</TestRun>`),
		"wrong namespace":    strings.Replace(valid, trxNamespace, "urn:other", 1),
		"missing namespace":  strings.Replace(valid, ` xmlns="`+trxNamespace+`"`, "", 1),
		"extra root":         valid + start + `</TestRun>`,
		"doctype":            `<!DOCTYPE TestRun [<!ENTITY x "secret">]>` + valid,
		"duplicate summary":  start + result + summary + summary + `</TestRun>`,
		"duplicate counters": strings.Replace(valid, `</ResultSummary>`, `<Counters total="1" executed="1" passed="1" failed="0"/></ResultSummary>`, 1),
		"duplicate result":   strings.Replace(valid, `</Results>`, `<UnitTestResult testId="one" executionId="run-one" testName="One" outcome="Passed"/></Results>`, 1),
		"unknown outcome":    strings.Replace(valid, `outcome="Passed"`, `outcome="Inconclusive"`, 1),
		"nested result":      strings.Replace(valid, `/>`, `><InnerResults><UnitTestResult testId="two" executionId="two" testName="Two" outcome="Passed"/></InnerResults></UnitTestResult>`, 1),
		"unsupported result": strings.Replace(valid, `<UnitTestResult`, `<TestResult`, 1),
		"counter mismatch":   strings.Replace(valid, `total="1"`, `total="2"`, 1),
		"unsupported count":  strings.Replace(valid, `failed="0"`, `failed="0" inconclusive="1"`, 1),
		"duplicate count":    strings.Replace(valid, `total="1"`, `total="1" total="1"`, 1),
		"invalid count":      strings.Replace(valid, `total="1"`, `total="-1"`, 1),
		"summary mismatch":   strings.Replace(valid, `outcome="Completed"`, `outcome="Failed"`, 1),
		"missing summary":    start + result + `</TestRun>`,
		"unexpected text":    strings.Replace(valid, `<Results>`, `unexpected<Results>`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			facts := readTRX(strings.NewReader(report))
			if len(facts.Unjudged) == 0 || facts.Counts.Counted != 0 || facts.Failure != "" ||
				Evaluate(facts, End{}, ScopePolicy).Outcome != Unjudged {
				t.Fatalf("accepted invalid report: %+v", facts)
			}
		})
	}
}

func TestTRXBoundsReport(t *testing.T) {
	const start = `<TestRun xmlns="http://microsoft.com/schemas/VisualStudio/TeamTest/2010"><Results>`
	var many strings.Builder
	many.WriteString(start)
	for index := 0; index <= trxCaseLimit; index++ {
		name := strconv.Itoa(index)
		many.WriteString(`<UnitTestResult testId="` + name + `" executionId="` + name + `" testName="Case" outcome="Passed"/>`)
	}
	many.WriteString(`</Results></TestRun>`)
	deep := start + `<UnitTestResult testId="x" executionId="x" testName="X" outcome="Passed"><Output>` +
		strings.Repeat("<Text>", trxDepthLimit) + strings.Repeat("</Text>", trxDepthLimit) +
		`</Output></UnitTestResult></Results></TestRun>`
	for name, report := range map[string]string{
		"bytes": strings.Repeat(" ", trxReportLimit+1),
		"depth": deep,
		"cases": many.String(),
	} {
		t.Run(name, func(t *testing.T) {
			facts := readTRX(strings.NewReader(report))
			if len(facts.Unjudged) == 0 {
				t.Fatalf("accepted oversized report: %+v", facts)
			}
		})
	}
}

type failingTRXReader struct{}

func (failingTRXReader) Read([]byte) (int, error) {
	return 0, errors.New("sensitive report path")
}

func TestTRXDoesNotExposeReaderErrors(t *testing.T) {
	facts := readTRX(failingTRXReader{})
	if len(facts.Unjudged) == 0 || strings.Contains(facts.Unjudged[0], "sensitive") {
		t.Fatalf("read error leaked: %+v", facts)
	}
}

var _ io.Reader = failingTRXReader{}
