package judge

import (
	"errors"
	"io"
	"math"
	"strings"

	"github.com/zima-oss/jackstay/internal/strictjson"
)

const (
	xcresultDocumentLimit = 16 << 20
	xcresultCaseLimit     = 100000
	xcresultDepthLimit    = 32
	xcresultDurationLimit = 512
)

// XCResultCaseDuration is a completed Xcode test case, at the same level as
// xcresulttool's summary count. Arguments of a parameterized case are not added.
type XCResultCaseDuration struct {
	Test    string  `json:"test"`
	Seconds float64 `json:"seconds"`
}

type xcresultSummary struct {
	Result                   string                `json:"result"`
	Total                    *int                  `json:"totalTestCount"`
	Passed                   *int                  `json:"passedTests"`
	Failed                   *int                  `json:"failedTests"`
	Skipped                  *int                  `json:"skippedTests"`
	ExpectedFailures         *int                  `json:"expectedFailures"`
	DevicesAndConfigurations []xcresultDeviceCount `json:"devicesAndConfigurations"`
}

type xcresultDeviceCount struct {
	Passed           *int `json:"passedTests"`
	Failed           *int `json:"failedTests"`
	Skipped          *int `json:"skippedTests"`
	ExpectedFailures *int `json:"expectedFailures"`
}

type xcresultTree struct {
	Devices                []struct{}     `json:"devices"`
	TestPlanConfigurations []struct{}     `json:"testPlanConfigurations"`
	TestNodes              []xcresultNode `json:"testNodes"`
}

type xcresultNode struct {
	Type     string         `json:"nodeType"`
	Result   string         `json:"result"`
	Name     string         `json:"name"`
	URL      string         `json:"nodeIdentifierURL"`
	Seconds  *float64       `json:"durationInSeconds"`
	Children []xcresultNode `json:"children"`
}

type xcresultCounts struct {
	passed, failed, skipped    int
	devicePassed, deviceFailed int
	seen                       map[string]struct{}
	bundles                    map[string]struct{}
	durations                  []XCResultCaseDuration
	truncated                  bool
	nodes                      int
}

// ReadXCResult reads the summary and test tree exported from one completed
// xcresult bundle by xcresulttool. The first supported shape is one device and
// one test plan configuration. Unknown producer fields are ignored, while the
// fields used to grant a pass must reconcile exactly.
func ReadXCResult(summary, tests io.Reader) (Facts, []XCResultCaseDuration, bool) {
	return readXCResultForBundles(summary, tests, nil)
}

func readXCResultForBundles(summary, tests io.Reader, required []string) (Facts, []XCResultCaseDuration, bool) {
	invalid := func(reason string) (Facts, []XCResultCaseDuration, bool) {
		return Facts{Kind: XCResultJSON, Counts: Counts{Counting: true}, Unjudged: []string{reason}}, nil, false
	}
	var header xcresultSummary
	if err := readXCResultJSON(summary, &header); err != nil {
		return invalid("the Xcode result summary is missing, malformed or oversized")
	}
	var tree xcresultTree
	if err := readXCResultJSON(tests, &tree); err != nil {
		return invalid("the Xcode test tree is missing, malformed or oversized")
	}
	if len(tree.Devices) != 1 || len(tree.TestPlanConfigurations) != 1 || len(tree.TestNodes) != 1 ||
		len(header.DevicesAndConfigurations) != 1 || tree.TestNodes[0].Type != "Test Plan" {
		return invalid("the Xcode result needs one device, configuration and test plan")
	}
	if !xcresultValidCount(header.Total) || !xcresultValidCount(header.Passed) ||
		!xcresultValidCount(header.Failed) || !xcresultValidCount(header.Skipped) ||
		!xcresultValidCount(header.ExpectedFailures) || *header.ExpectedFailures != 0 {
		return invalid("the Xcode result summary has missing or unsupported totals")
	}
	device := header.DevicesAndConfigurations[0]
	if !xcresultValidCount(device.Passed) || !xcresultValidCount(device.Failed) ||
		!xcresultValidCount(device.Skipped) || !xcresultValidCount(device.ExpectedFailures) ||
		*device.ExpectedFailures != 0 {
		return invalid("the Xcode device summary has missing or unsupported totals")
	}
	counts := xcresultCounts{seen: make(map[string]struct{}), bundles: make(map[string]struct{})}
	if err := counts.visit(tree.TestNodes[0], 0); err != nil {
		return invalid(err.Error())
	}
	for _, bundle := range required {
		if _, found := counts.bundles[bundle]; !found {
			return invalid("the Xcode result is missing a required test bundle")
		}
	}
	if counts.passed != *header.Passed || counts.failed != *header.Failed ||
		counts.skipped != *header.Skipped || counts.passed+counts.failed+counts.skipped != *header.Total ||
		counts.devicePassed != *device.Passed || counts.deviceFailed != *device.Failed ||
		counts.skipped != *device.Skipped {
		return invalid("the Xcode result totals disagree with the test tree")
	}
	if header.Result != "Passed" && header.Result != "Failed" ||
		header.Result == "Passed" && counts.failed != 0 ||
		header.Result == "Failed" && counts.failed == 0 {
		return invalid("the Xcode result status disagrees with its test cases")
	}
	facts := Facts{Kind: XCResultJSON, Counts: Counts{Counting: true,
		Counted: counts.passed + counts.failed, Skipped: counts.skipped}}
	if counts.failed != 0 {
		facts.Failure = "the Xcode result contains failed test cases"
	}
	return facts, counts.durations, counts.truncated
}

func readXCResultJSON(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, xcresultDocumentLimit+1))
	if err != nil || len(data) > xcresultDocumentLimit {
		return errors.New("the Xcode result exceeds its size limit")
	}
	return strictjson.UnmarshalOpen(data, target)
}

func xcresultValidCount(value *int) bool {
	return value != nil && *value >= 0 && *value <= xcresultCaseLimit
}

func (counts *xcresultCounts) visit(node xcresultNode, depth int) error {
	counts.nodes++
	if counts.nodes > xcresultCaseLimit*4 || depth >= xcresultDepthLimit {
		return errors.New("the Xcode test tree exceeds its node or depth limit")
	}
	switch node.Type {
	case "Test Plan", "Unit test bundle", "Test Suite":
		if depth == 0 && node.Type != "Test Plan" || len(node.Children) == 0 {
			return errors.New("the Xcode test tree has an incomplete container")
		}
		if node.Type == "Unit test bundle" {
			if node.Name == "" || len(node.Name) > 512 {
				return errors.New("the Xcode test tree has an unnamed bundle")
			}
			if _, exists := counts.bundles[node.Name]; exists {
				return errors.New("the Xcode test tree repeats a test bundle")
			}
			counts.bundles[node.Name] = struct{}{}
		}
		failedBefore := counts.failed
		for _, child := range node.Children {
			allowed := node.Type == "Test Plan" && child.Type == "Unit test bundle" ||
				node.Type == "Unit test bundle" && (child.Type == "Test Suite" || child.Type == "Test Case") ||
				node.Type == "Test Suite" && child.Type == "Test Case"
			if !allowed {
				return errors.New("the Xcode test tree has an unsupported container child")
			}
			if err := counts.visit(child, depth+1); err != nil {
				return err
			}
		}
		if node.Result != "Passed" && node.Result != "Failed" ||
			node.Result == "Passed" && counts.failed > failedBefore ||
			node.Result == "Failed" && counts.failed == failedBefore {
			return errors.New("the Xcode container status disagrees with its cases")
		}
	case "Test Case":
		if node.URL == "" || len(node.URL) > 4096 || !strings.HasPrefix(node.URL, "test://") ||
			len(counts.seen) >= xcresultCaseLimit {
			return errors.New("the Xcode test tree has an invalid or excessive test identity")
		}
		if _, exists := counts.seen[node.URL]; exists {
			return errors.New("the Xcode test tree repeats a test case")
		}
		counts.seen[node.URL] = struct{}{}
		switch node.Result {
		case "Passed":
			counts.passed++
		case "Failed":
			counts.failed++
		case "Skipped":
			counts.skipped++
		default:
			return errors.New("the Xcode test tree has an unsupported test status")
		}
		arguments, failedArguments := 0, 0
		for _, child := range node.Children {
			switch child.Type {
			case "Arguments":
				arguments++
				if len(child.Children) != 0 || child.URL == "" || child.Result != "Passed" && child.Result != "Failed" {
					return errors.New("the Xcode test tree has an incomplete parameter result")
				}
				if child.Result == "Failed" && node.Result != "Failed" {
					return errors.New("the Xcode parameter failure disagrees with its test")
				}
				if child.Result == "Failed" {
					failedArguments++
				}
			case "Skip Message", "Failure Message":
				if len(child.Children) != 0 {
					return errors.New("the Xcode test tree has nested diagnostics")
				}
			default:
				return errors.New("the Xcode test tree has an unsupported test child")
			}
		}
		if arguments > 0 {
			if node.Result == "Skipped" || node.Result == "Failed" && failedArguments == 0 {
				return errors.New("the Xcode parameter results disagree with their test")
			}
			counts.devicePassed += arguments - failedArguments
			counts.deviceFailed += failedArguments
		} else {
			switch node.Result {
			case "Passed":
				counts.devicePassed++
			case "Failed":
				counts.deviceFailed++
			}
		}
		if node.Result != "Skipped" && node.Seconds != nil &&
			!math.IsNaN(*node.Seconds) && !math.IsInf(*node.Seconds, 0) && *node.Seconds >= 0 {
			if len(counts.durations) == xcresultDurationLimit {
				counts.truncated = true
			} else {
				counts.durations = append(counts.durations, XCResultCaseDuration{Test: node.URL, Seconds: *node.Seconds})
			}
		}
	default:
		return errors.New("the Xcode test tree has an unsupported node")
	}
	return nil
}
