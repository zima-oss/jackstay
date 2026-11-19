package judge

import (
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

const (
	junitReportLimit = 16 << 20
	junitCaseLimit   = 100000
	junitDepthLimit  = 32
)

type junitCounts struct {
	tests    int
	failures int
	errors   int
	skipped  int
}

type junitElement struct {
	name     string
	suite    string
	counts   junitCounts
	declared map[string]int
	status   string
}

type junitParser struct {
	stack    []*junitElement
	cases    map[string]struct{}
	total    junitCounts
	sawRoot  bool
	complete bool
}

// readJUnit decodes one bounded JUnit XML report. It only trusts a complete document whose
// suite summaries agree with its test cases. Diagnostics never contain report text.
func readJUnit(report io.Reader) Facts {
	invalid := func(reason string) Facts {
		return Facts{Kind: JUnit, Counts: Counts{Counting: true}, Unjudged: []string{reason}}
	}
	limited := &io.LimitedReader{R: report, N: junitReportLimit + 1}
	decoder := xml.NewDecoder(limited)
	parser := junitParser{cases: make(map[string]struct{})}
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return invalid("the JUnit XML report is unreadable or incomplete")
		}
		switch value := token.(type) {
		case xml.StartElement:
			if err := parser.start(value); err != nil {
				return invalid(err.Error())
			}
		case xml.EndElement:
			if err := parser.end(value); err != nil {
				return invalid(err.Error())
			}
		case xml.CharData:
			if len(parser.stack) == 0 && len(strings.TrimSpace(string(value))) != 0 {
				return invalid("the JUnit XML report has content outside its root")
			}
			if len(parser.stack) > 0 {
				switch parser.stack[len(parser.stack)-1].name {
				case "testsuites", "testsuite", "testcase", "properties":
					if len(strings.TrimSpace(string(value))) != 0 {
						return invalid("the JUnit XML report has unexpected text")
					}
				}
			}
		case xml.Directive:
			return invalid("the JUnit XML report contains a forbidden directive")
		case xml.ProcInst:
			if parser.sawRoot || value.Target != "xml" {
				return invalid("the JUnit XML report contains a forbidden instruction")
			}
		}
	}
	if limited.N == 0 {
		return invalid("the JUnit XML report exceeds the size limit")
	}
	if !parser.sawRoot || !parser.complete || len(parser.stack) != 0 {
		return invalid("the JUnit XML report is incomplete")
	}
	facts := Facts{Kind: JUnit, Counts: Counts{
		Counting: true, Counted: parser.total.tests - parser.total.skipped,
		Skipped: parser.total.skipped,
	}}
	if parser.total.failures+parser.total.errors > 0 {
		facts.Failure = "the JUnit XML report contains failed or errored test cases"
	}
	return facts
}

func (parser *junitParser) start(element xml.StartElement) error {
	if len(parser.stack) >= junitDepthLimit {
		return errors.New("the JUnit XML report exceeds the nesting limit")
	}
	name := element.Name.Local
	if element.Name.Space != "" {
		return errors.New("the JUnit XML report uses an unsupported namespace")
	}
	if len(parser.stack) == 0 {
		if parser.sawRoot || (name != "testsuites" && name != "testsuite") {
			return errors.New("the JUnit XML report has an unsupported root")
		}
		parser.sawRoot = true
	} else if !junitChildAllowed(parser.stack[len(parser.stack)-1].name, name) {
		return errors.New("the JUnit XML report has an unsupported element")
	}
	node := &junitElement{name: name}
	if name == "testsuites" || name == "testsuite" {
		declared, err := junitDeclaredCounts(element.Attr)
		if err != nil {
			return err
		}
		node.declared = declared
		if name == "testsuite" {
			for _, attribute := range element.Attr {
				if attribute.Name.Local == "name" {
					node.suite = attribute.Value
				}
			}
		}
	}
	if name == "testcase" {
		if parser.total.tests >= junitCaseLimit {
			return errors.New("the JUnit XML report exceeds the test case limit")
		}
		identity, err := parser.caseIdentity(element.Attr)
		if err != nil {
			return err
		}
		if _, duplicate := parser.cases[identity]; duplicate {
			return errors.New("the JUnit XML report repeats a test case")
		}
		parser.cases[identity] = struct{}{}
	}
	if name == "failure" || name == "error" || name == "skipped" {
		parent := parser.stack[len(parser.stack)-1]
		if parent.status != "" {
			return errors.New("the JUnit XML report gives one test case conflicting results")
		}
		parent.status = name
	}
	parser.stack = append(parser.stack, node)
	return nil
}

func (parser *junitParser) end(element xml.EndElement) error {
	if len(parser.stack) == 0 || parser.stack[len(parser.stack)-1].name != element.Name.Local {
		return errors.New("the JUnit XML report has mismatched elements")
	}
	node := parser.stack[len(parser.stack)-1]
	parser.stack = parser.stack[:len(parser.stack)-1]
	if node.name == "testcase" {
		parser.total.tests++
		switch node.status {
		case "failure":
			parser.total.failures++
		case "error":
			parser.total.errors++
		case "skipped":
			parser.total.skipped++
		}
		for _, parent := range parser.stack {
			if parent.name == "testsuites" || parent.name == "testsuite" {
				parent.counts.tests++
				switch node.status {
				case "failure":
					parent.counts.failures++
				case "error":
					parent.counts.errors++
				case "skipped":
					parent.counts.skipped++
				}
			}
		}
	}
	if node.declared != nil {
		actual := map[string]int{
			"tests": node.counts.tests, "failures": node.counts.failures,
			"errors": node.counts.errors, "skipped": node.counts.skipped,
		}
		for name, expected := range node.declared {
			if actual[name] != expected {
				return errors.New("the JUnit XML report's suite totals disagree with its test cases")
			}
		}
	}
	if len(parser.stack) == 0 {
		parser.complete = true
	}
	return nil
}

func (parser *junitParser) caseIdentity(attributes []xml.Attr) (string, error) {
	var class, name string
	for _, attribute := range attributes {
		switch attribute.Name.Local {
		case "classname":
			class = attribute.Value
		case "name":
			name = attribute.Value
		}
	}
	if name == "" {
		return "", errors.New("the JUnit XML report has a test case without a name")
	}
	var path strings.Builder
	for _, node := range parser.stack {
		if node.name == "testsuite" {
			path.WriteString(node.suite)
			path.WriteByte(0)
		}
	}
	path.WriteString(class)
	path.WriteByte(0)
	path.WriteString(name)
	return path.String(), nil
}

func junitChildAllowed(parent, child string) bool {
	switch parent {
	case "testsuites":
		return child == "testsuite"
	case "testsuite":
		return child == "testsuite" || child == "testcase" || child == "properties" ||
			child == "system-out" || child == "system-err"
	case "testcase":
		return child == "failure" || child == "error" || child == "skipped" ||
			child == "properties" || child == "system-out" || child == "system-err"
	case "properties":
		return child == "property"
	default:
		return false
	}
}

func junitDeclaredCounts(attributes []xml.Attr) (map[string]int, error) {
	declared := make(map[string]int)
	for _, attribute := range attributes {
		name := attribute.Name.Local
		if name != "tests" && name != "failures" && name != "errors" && name != "skipped" {
			continue
		}
		if _, duplicate := declared[name]; duplicate {
			return nil, errors.New("the JUnit XML report repeats a suite count")
		}
		value := attribute.Value
		if value == "" {
			return nil, errors.New("the JUnit XML report has an invalid suite count")
		}
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return nil, errors.New("the JUnit XML report has an invalid suite count")
			}
		}
		number, err := strconv.Atoi(value)
		if err != nil || number > junitCaseLimit {
			return nil, errors.New("the JUnit XML report has an invalid suite count")
		}
		declared[name] = number
	}
	return declared, nil
}
