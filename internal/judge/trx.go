package judge

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

const (
	trxNamespace   = "http://microsoft.com/schemas/VisualStudio/TeamTest/2010"
	trxReportLimit = 16 << 20
	trxDepthLimit  = 32
	trxCaseLimit   = 100000
)

type trxSummary struct {
	total, executed, passed, failed, notExecuted int
	sawTotal, sawExecuted, sawPassed, sawFailed  bool
	unsupported                                  bool
}

// readTRX accepts a complete, bounded TeamTest/2010 report whose summary agrees with its
// UnitTestResult elements. Report text never appears in diagnostics.
func readTRX(report io.Reader) Facts {
	invalid := func() Facts {
		return Facts{Kind: TRX, Counts: Counts{Counting: true}, Unjudged: []string{"the TRX report is invalid or incomplete"}}
	}
	contents, err := io.ReadAll(io.LimitReader(report, trxReportLimit+1))
	if err != nil || len(contents) > trxReportLimit {
		return invalid()
	}
	contents = bytes.TrimPrefix(contents, []byte{0xef, 0xbb, 0xbf})
	decoder := xml.NewDecoder(bytes.NewReader(contents))
	var stack []xml.Name
	seenRoot, endedRoot := false, false
	seenResults, seenSummary, seenCounters := false, false, false
	var passed, failed, skipped int
	results := make(map[string]struct{})
	var summary trxSummary
	var summaryOutcome string
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return invalid()
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Space != trxNamespace || len(stack) >= trxDepthLimit || endedRoot {
				return invalid()
			}
			if len(stack) == 0 {
				if seenRoot || value.Name.Local != "TestRun" {
					return invalid()
				}
				seenRoot = true
			} else {
				parent := stack[len(stack)-1].Local
				switch parent {
				case "TestRun":
					switch value.Name.Local {
					case "Times", "TestSettings", "TestDefinitions", "TestEntries", "TestLists":
					case "Results":
						if seenResults {
							return invalid()
						}
						seenResults = true
					case "ResultSummary":
						if seenSummary {
							return invalid()
						}
						seenSummary = true
						attributes, ok := trxAttributes(value.Attr)
						if !ok {
							return invalid()
						}
						summaryOutcome = attributes["outcome"]
						if summaryOutcome != "Completed" && summaryOutcome != "Failed" {
							return invalid()
						}
					default:
						return invalid()
					}
				case "Results":
					if value.Name.Local != "UnitTestResult" || len(results) >= trxCaseLimit {
						return invalid()
					}
					attributes, ok := trxAttributes(value.Attr)
					if !ok || attributes["testId"] == "" || attributes["executionId"] == "" || attributes["testName"] == "" {
						return invalid()
					}
					identity := attributes["testId"] + "\x00" + attributes["executionId"]
					if _, exists := results[identity]; exists {
						return invalid()
					}
					results[identity] = struct{}{}
					switch attributes["outcome"] {
					case "Passed":
						passed++
					case "Failed":
						failed++
					case "NotExecuted":
						skipped++
					default:
						return invalid()
					}
				case "UnitTestResult":
					if value.Name.Local != "Output" {
						return invalid()
					}
				case "ResultSummary":
					switch value.Name.Local {
					case "Counters":
						if seenCounters {
							return invalid()
						}
						seenCounters = true
						parsed, ok := parseTRXCounters(value.Attr)
						if !ok {
							return invalid()
						}
						summary = parsed
					case "Output", "RunInfos":
					default:
						return invalid()
					}
				default:
					// Metadata and diagnostic payloads have no bearing on the result. Their
					// size, nesting, namespace and document boundaries remain checked.
					if value.Name.Local == "UnitTestResult" || value.Name.Local == "Results" || value.Name.Local == "ResultSummary" || value.Name.Local == "Counters" {
						return invalid()
					}
				}
			}
			stack = append(stack, value.Name)
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1] != value.Name {
				return invalid()
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				endedRoot = true
			}
		case xml.CharData:
			if len(strings.TrimSpace(string(value))) != 0 {
				if len(stack) == 0 {
					return invalid()
				}
				switch stack[len(stack)-1].Local {
				case "TestRun", "Results", "ResultSummary", "UnitTestResult", "Counters":
					return invalid()
				}
			}
		case xml.Directive:
			return invalid()
		case xml.ProcInst:
			if seenRoot || value.Target != "xml" {
				return invalid()
			}
		}
	}
	if !seenRoot || !endedRoot || len(stack) != 0 || !seenSummary || !seenCounters ||
		summary.unsupported || !summary.sawTotal || !summary.sawExecuted || !summary.sawPassed || !summary.sawFailed ||
		summary.total != passed+failed+skipped || summary.executed != passed+failed ||
		summary.passed != passed || summary.failed != failed || summary.notExecuted > skipped ||
		(summaryOutcome == "Failed") != (failed > 0) {
		return invalid()
	}
	facts := Facts{Kind: TRX, Counts: Counts{Counting: true, Counted: passed + failed, Skipped: skipped}}
	if failed > 0 {
		facts.Failure = "the TRX report contains failed test cases"
	}
	return facts
}

func trxAttributes(attributes []xml.Attr) (map[string]string, bool) {
	values := make(map[string]string)
	for _, attribute := range attributes {
		if attribute.Name.Space == "xmlns" || attribute.Name.Local == "xmlns" {
			continue
		}
		if attribute.Name.Space != "" {
			return nil, false
		}
		if _, duplicate := values[attribute.Name.Local]; duplicate {
			return nil, false
		}
		values[attribute.Name.Local] = attribute.Value
	}
	return values, true
}

func parseTRXCounters(attributes []xml.Attr) (trxSummary, bool) {
	values, ok := trxAttributes(attributes)
	if !ok {
		return trxSummary{}, false
	}
	var result trxSummary
	for name, value := range values {
		if value == "" {
			return trxSummary{}, false
		}
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return trxSummary{}, false
			}
		}
		number, err := strconv.Atoi(value)
		if err != nil || number > trxCaseLimit {
			return trxSummary{}, false
		}
		switch name {
		case "total":
			result.total, result.sawTotal = number, true
		case "executed":
			result.executed, result.sawExecuted = number, true
		case "passed":
			result.passed, result.sawPassed = number, true
		case "failed":
			result.failed, result.sawFailed = number, true
		case "notExecuted":
			result.notExecuted = number
		default:
			if number != 0 {
				result.unsupported = true
			}
		}
	}
	return result, true
}
