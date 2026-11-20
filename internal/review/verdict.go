package review

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/zima-oss/jackstay/internal/review/codex"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

// AnswerFinding is one finding as a reviewer writes it (section 3.5).
type AnswerFinding struct {
	ID          string `json:"id"`
	Place       string `json:"place"`
	Severity    string `json:"severity"`
	Likelihood  string `json:"likelihood"`
	Consequence string `json:"consequence"`
	Title       string `json:"title"`
	Detail      string `json:"detail"`
	// OrdinaryPath names how ordinary work reaches an unusual setup by accident, "" when it does not.
	OrdinaryPath string   `json:"ordinary_path"`
	Repeats      []string `json:"repeats"`
}

// PriorAnswer is a reviewer's word on one open finding of the ledger.
type PriorAnswer struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Answer is a reviewer's last message: one JSON object of the schema.
type Answer struct {
	Verdict  string          `json:"verdict"`
	Summary  string          `json:"summary"`
	Findings []AnswerFinding `json:"findings"`
	Prior    []PriorAnswer   `json:"prior"`
}

// Schema is the JSON Schema given to the reviewer, with the project's severity words.
func Schema(severities []string) []byte {
	stringType := map[string]any{"type": "string"}
	enum := func(words ...string) map[string]any { return map[string]any{"type": "string", "enum": words} }
	object := func(properties map[string]any) map[string]any {
		required := make([]string, 0, len(properties))
		for name := range properties {
			required = append(required, name)
		}
		slices.Sort(required)
		return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
	}
	finding := object(map[string]any{
		"id": stringType, "place": stringType, "severity": enum(severities...),
		"likelihood": enum("ordinary", "unusual"), "consequence": enum("severe", "mild"),
		"title": stringType, "detail": stringType, "ordinary_path": stringType,
		"repeats": map[string]any{"type": "array", "items": stringType},
	})
	prior := object(map[string]any{"id": stringType, "status": enum("fixed", "open")})
	schema := object(map[string]any{
		"verdict": enum("approve", "changes", "incomplete"), "summary": stringType,
		"findings": map[string]any{"type": "array", "items": finding},
		"prior":    map[string]any{"type": "array", "items": prior},
	})
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		panic(err) // a literal value always encodes
	}
	return append(data, '\n')
}

// ReadAnswer reads a reviewer's answer file: the outcome (done, no-answer, unparsable) and why.
func ReadAnswer(path string) (Answer, string, string) {
	data, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return Answer{}, codex.OutcomeNoAnswer, "the reviewer wrote no last message"
	}
	if len(data) > strictjson.DefaultLimit {
		return Answer{}, OutcomeUnparsable, fmt.Sprintf("the answer is larger than %d bytes", strictjson.DefaultLimit)
	}
	var answer Answer
	if err := strictjson.Unmarshal(data, &answer); err != nil {
		return Answer{}, OutcomeUnparsable, "the answer is not one JSON object of the schema: " + err.Error()
	}
	var whole any
	if err := json.Unmarshal(data, &whole); err != nil {
		return Answer{}, OutcomeUnparsable, "the answer is not JSON: " + err.Error()
	}
	if problem := schemaShape(whole); problem != "" {
		return Answer{}, OutcomeUnparsable, "the answer does not have the schema's shape: " + problem
	}
	return answer, codex.OutcomeDone, ""
}

// Every member the schema requires, and its JSON type ("string" or "array").
var (
	answerMembers  = map[string]string{"verdict": "string", "summary": "string", "findings": "array", "prior": "array"}
	findingMembers = map[string]string{"id": "string", "place": "string", "severity": "string", "likelihood": "string",
		"consequence": "string", "title": "string", "detail": "string", "repeats": "array"}
	priorMembers = map[string]string{"id": "string", "status": "string"}
)

// schemaShape says what of the schema's shape an answer lacks ("" when nothing): every required
// member present, of its type, never null — what a Go struct alone would let pass as empty.
func schemaShape(whole any) string {
	top, problem := shapeOf(whole, answerMembers, "the answer")
	if problem != "" {
		return problem
	}
	for name, members := range map[string]map[string]string{"findings": findingMembers, "prior": priorMembers} {
		for index, item := range top[name].([]any) {
			fields, problem := shapeOf(item, members, fmt.Sprintf("%s[%d]", name, index))
			if problem != "" {
				return problem
			}
			if name == "findings" {
				for _, repeated := range fields["repeats"].([]any) {
					if _, ok := repeated.(string); !ok {
						return fmt.Sprintf("findings[%d].repeats holds a value that is not a string", index)
					}
				}
			}
		}
	}
	return ""
}

func shapeOf(value any, members map[string]string, where string) (map[string]any, string) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, where + " is not an object"
	}
	for _, name := range sortedNames(members) {
		member, present := object[name]
		switch {
		case !present:
			return nil, fmt.Sprintf("%s has no %q", where, name)
		case members[name] == "string":
			if _, ok := member.(string); !ok {
				return nil, fmt.Sprintf("%s.%s is not a string", where, name)
			}
		case members[name] == "array":
			if _, ok := member.([]any); !ok {
				return nil, fmt.Sprintf("%s.%s is not an array", where, name)
			}
		}
	}
	return object, ""
}

// CheckAnswer says what is inconsistent in an answer against the settings and the open ledger
// ("" when nothing): section 3.5's checks.
func CheckAnswer(answer Answer, settings Settings, ledger []LedgerFinding) string {
	var problems []string
	problem := func(format string, values ...any) { problems = append(problems, fmt.Sprintf(format, values...)) }
	switch answer.Verdict {
	case "incomplete":
		if strings.TrimSpace(answer.Summary) == "" {
			return "an incomplete verdict must summarize why the review could not finish"
		}
		return ""
	case "changes":
		if len(answer.Findings) == 0 && !slices.ContainsFunc(answer.Prior, func(prior PriorAnswer) bool { return prior.Status == StatusOpen }) {
			problem("a changes verdict must identify a new or still-open finding")
		}
	case "approve":
	default:
		problem("verdict %q is not approve, changes or incomplete", answer.Verdict)
	}
	known := map[string]string{}
	for _, finding := range ledger {
		known[finding.ID] = finding.Status
	}
	seen := map[string]bool{}
	for _, finding := range answer.Findings {
		if strings.TrimSpace(finding.ID) == "" {
			problem("a finding has no id")
		} else if seen[finding.ID] {
			problem("finding id %s is used twice", finding.ID)
		}
		seen[finding.ID] = true
		if strings.TrimSpace(finding.Place) == "" {
			problem("finding %s has no place", finding.ID)
		}
		if !slices.Contains(settings.Severities, finding.Severity) {
			problem("finding %s has severity %q", finding.ID, finding.Severity)
		}
		if finding.Likelihood != "ordinary" && finding.Likelihood != "unusual" {
			problem("finding %s has likelihood %q", finding.ID, finding.Likelihood)
		}
		if finding.Consequence != "severe" && finding.Consequence != "mild" {
			problem("finding %s has consequence %q", finding.ID, finding.Consequence)
		}
		for _, repeated := range finding.Repeats {
			if _, ok := known[repeated]; !ok {
				problem("finding %s repeats %s, which is not in the ledger", finding.ID, repeated)
			}
		}
	}
	answered := map[string]bool{}
	for _, prior := range answer.Prior {
		status, ok := known[prior.ID]
		switch {
		case !ok:
			problem("prior answers %s, which is not in the ledger", prior.ID)
		case status != StatusOpen:
			problem("prior answers %s, which is %s, not open", prior.ID, status)
		case answered[prior.ID]:
			problem("prior answers %s twice", prior.ID)
		}
		answered[prior.ID] = true
		if prior.Status != "fixed" && prior.Status != StatusOpen {
			problem("prior %s has status %q: fixed or open", prior.ID, prior.Status)
		}
	}
	for _, finding := range ledger {
		if finding.Status == StatusOpen && !answered[finding.ID] {
			problem("prior does not answer open finding %s", finding.ID)
		}
	}
	return strings.Join(problems, "; ")
}
