package judge

import (
	"encoding/json/jsontext"
	"io"

	"github.com/zima-oss/jackstay/internal/strictjson"
)

const (
	jestReportLimit = 16 << 20
	jestCaseLimit   = 100000
)

// JestSelection identifies one assertion by its absolute file and reported full name.
type JestSelection struct {
	File string
	Name string
}

type jestAssertion struct {
	FullName    string         `json:"fullName"`
	Status      string         `json:"status"`
	WouldRun    jsontext.Value `json:"wouldRun"`
	Invocations *int           `json:"invocations"`
}

type jestSuite struct {
	File       string          `json:"name"`
	Status     string          `json:"status"`
	Assertions []jestAssertion `json:"assertionResults"`
}

type jestReport struct {
	OK            *bool       `json:"success"`
	Total         *int        `json:"numTotalTests"`
	Passed        *int        `json:"numPassedTests"`
	Failed        *int        `json:"numFailedTests"`
	Pending       *int        `json:"numPendingTests"`
	Todo          *int        `json:"numTodoTests"`
	TotalSuites   *int        `json:"numTotalTestSuites"`
	FailedSuites  *int        `json:"numFailedTestSuites"`
	PassedSuites  *int        `json:"numPassedTestSuites"`
	PendingSuites *int        `json:"numPendingTestSuites"`
	RuntimeErrors *int        `json:"numRuntimeErrorTestSuites"`
	Suites        []jestSuite `json:"testResults"`
}

// readJest validates the aggregate against execution results. Discovery explicitly flags
// selected assertions as wouldRun and must never be mistaken for an executed pass.
func readJest(report io.Reader) Facts {
	return readJestSelected(report, nil)
}

func readJestSelected(report io.Reader, selection *JestSelection) Facts {
	facts := Facts{Kind: JestJSON, Counts: Counts{Counting: true}}
	invalid := func(reason string) Facts {
		return Facts{Kind: JestJSON, Unjudged: []string{reason}}
	}
	content, err := io.ReadAll(io.LimitReader(report, jestReportLimit+1))
	if err != nil || len(content) > jestReportLimit {
		return invalid("the Jest JSON report is unreadable or exceeds the size limit")
	}
	var saved jestReport
	if strictjson.UnmarshalOpen(content, &saved) != nil || saved.OK == nil || saved.Suites == nil {
		return invalid("the Jest JSON report is incomplete or malformed")
	}
	for _, total := range []*int{saved.Total, saved.Passed, saved.Failed, saved.Pending, saved.Todo,
		saved.TotalSuites, saved.FailedSuites, saved.PassedSuites, saved.PendingSuites, saved.RuntimeErrors} {
		if total == nil || *total < 0 || *total > jestCaseLimit {
			return invalid("the Jest JSON report has missing or invalid totals")
		}
	}
	var passed, failed, pending, todo int
	var passedSuites, failedSuites, pendingSuites int
	var retried int
	var wrongSelection bool
	var selectedSuites, selectedCases int
	for _, suite := range saved.Suites {
		if suite.Assertions == nil {
			return invalid("a Jest suite has no assertion results")
		}
		selectedFile := selection != nil && suite.File == selection.File
		if selection != nil {
			if selectedFile {
				selectedSuites++
			} else {
				wrongSelection = true
			}
		}
		passedBefore, failedBefore := passed, failed
		for _, assertion := range suite.Assertions {
			if assertion.WouldRun != nil {
				return invalid("the Jest report is a discovery result")
			}
			if assertion.Invocations != nil {
				if *assertion.Invocations < 1 {
					return invalid("the Jest report contains an invalid invocation count")
				}
				if *assertion.Invocations > 1 && assertion.Status == "passed" {
					retried++
				}
			}
			if selection != nil {
				if selectedFile && assertion.FullName == selection.Name {
					selectedCases++
				} else if assertion.Status == "passed" || assertion.Status == "failed" {
					wrongSelection = true
				}
			}
			switch assertion.Status {
			case "passed":
				passed++
			case "failed":
				failed++
			case "pending":
				pending++
			case "todo":
				todo++
			default:
				return invalid("the Jest report contains an unsupported assertion status")
			}
		}
		switch suite.Status {
		case "passed", "focused":
			if failed != failedBefore {
				return invalid("a successful Jest suite contains failed assertions")
			}
			passedSuites++
		case "failed":
			failedSuites++
		case "skipped":
			if passed != passedBefore || failed != failedBefore {
				return invalid("a skipped Jest suite contains executed assertions")
			}
			pendingSuites++
		default:
			return invalid("the Jest report contains an unsupported suite status")
		}
	}
	if passed+failed+pending+todo != *saved.Total || passed != *saved.Passed || failed != *saved.Failed ||
		pending != *saved.Pending || todo != *saved.Todo || len(saved.Suites) != *saved.TotalSuites ||
		passedSuites != *saved.PassedSuites || failedSuites != *saved.FailedSuites || pendingSuites != *saved.PendingSuites ||
		*saved.RuntimeErrors > failedSuites || (*saved.OK && failedSuites != 0) {
		return invalid("the Jest JSON report totals disagree with its assertion results")
	}
	facts.Counts.Counted, facts.Counts.Skipped = passed+failed, pending+todo
	facts.Retried = retried
	if wrongSelection || selectedSuites > 1 || selectedCases > 1 {
		facts.Unjudged = append(facts.Unjudged, "the Jest report does not uniquely match the selected file and assertion")
	}
	if !*saved.OK {
		facts.Failure = "the Jest report records a failed run"
	}
	return facts
}
