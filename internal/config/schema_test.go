package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/version"
)

func TestCheckedInJSONSchemaMatchesRuntimeTable(t *testing.T) {
	generated, err := JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	checkedIn, err := os.ReadFile(filepath.Join("..", "..", "schemas", "jackstay.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(checkedIn, generated) {
		t.Fatal("schemas/jackstay.schema.json is stale; run go run ./cmd/jst-schema > schemas/jackstay.schema.json")
	}
}

func TestJSONSchemaDescribesRuntimeStructure(t *testing.T) {
	content, err := JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(content, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	for key := range Table.keys {
		if _, exists := properties[key]; !exists {
			t.Errorf("missing runtime key %s", key)
		}
	}
	if schema["additionalProperties"] != false {
		t.Fatal("unknown top-level keys must be rejected")
	}
	if properties["test"].(map[string]any)["properties"].(map[string]any)["scopes"].(map[string]any)["additionalProperties"] == nil {
		t.Fatal("dynamic test scope names must be accepted")
	}
	if _, found := properties["review"].(map[string]any)["properties"].(map[string]any)["pool"]; found {
		t.Fatal("review.pool is gone with the reviewer pool (folded into jst review)")
	}
	brief := properties["brief"].(map[string]any)["properties"].(map[string]any)["max_age_hours"].(map[string]any)
	if brief["minimum"] != float64(BriefMaxAgeHoursMinimum) || brief["maximum"] != float64(BriefMaxAgeHoursMaximum) ||
		brief["default"] != float64(BriefMaxAgeHours) {
		t.Fatalf("brief expiry schema: %+v", brief)
	}
	if !strings.Contains(schema["$id"].(string), "/v"+version.Version+"/schemas/") {
		t.Fatalf("$id is not on the version's tag: %v", schema["$id"])
	}
}

func TestChatProjectPolicy(t *testing.T) {
	parsed, err := Parse([]byte(`{
		"$schema": "./schemas/jackstay.schema.json",
		"jackstay_schema": 3,
		"chat": {"enabled": true, "codex_bridge": {"enabled": true}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	policy := ChatSettings(parsed.Section("chat"))
	if !policy.Enabled || !policy.CodexBridgeEnabled || policy.MaxPending != ChatMaxPending ||
		policy.MaxMessageBytes != ChatMaxMessageBytes || policy.CodexPollIntervalMS != ChatCodexPollIntervalMS {
		t.Fatalf("unexpected chat policy: %+v", policy)
	}
	parsed.Section("chat")["max_pending"] = json.Number("24")
	if problems := SectionProblems(parsed).Of("chat"); len(problems) != 1 || !strings.Contains(problems[0], "unknown key") {
		t.Fatalf("a chat limit is a constant, not a key: %v", problems)
	}
}

func TestClaudeCodeChatRequiresEnabledChat(t *testing.T) {
	parsed, err := Parse([]byte(`{"jackstay_schema":3,"chat":{"enabled":true,"claude_code":{"enabled":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !ChatSettings(parsed.Section("chat")).ClaudeCodeEnabled || len(SectionProblems(parsed).Of("chat")) != 0 {
		t.Fatal("enabled Claude Code chat hook was rejected")
	}
	parsed.Section("chat")["enabled"] = false
	if problems := SectionProblems(parsed).Of("chat"); len(problems) != 1 {
		t.Fatalf("Claude Code hook was allowed without chat: %v", problems)
	}
	content, err := JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte(`"claude_code"`)) {
		t.Fatal("editor schema omitted Claude Code chat settings")
	}
}

func TestPinnedAdoptionProfileNeedsDigestAndPath(t *testing.T) {
	parsed, err := Parse([]byte(`{"jackstay_schema":3,"adopt":{"profile_sha256":"bad"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if problems := SectionProblems(parsed).Of("adopt"); len(problems) != 2 {
		t.Fatalf("missing digest and path checks: %v", problems)
	}
	adopt := parsed.Section("adopt")
	adopt["profile"] = "policy.bundle.json"
	adopt["profile_sha256"] = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if problems := SectionProblems(parsed).Of("adopt"); len(problems) != 0 {
		t.Fatalf("valid profile digest rejected: %v", problems)
	}
}

func TestNewerKeysNeedSchema3AndAnOlderSchemaSaysSo(t *testing.T) {
	for _, document := range []string{
		`{"jackstay_schema":2,"$schema":"x"}`,
		`{"jackstay_schema":2,"chat":{"enabled":false}}`,
		`{"jackstay_schema":2,"brief":{"max_age_hours":24}}`,
		`{"jackstay_schema":2,"status":{"repos":["."]}}`,
		`{"jackstay_schema":2,"test":{"selection":{"order":[]}}}`,
		`{"jackstay_schema":2,"test":{"scopes":{"unit":{"run":["x"],"report":"r.xml"}}}}`,
		`{"jackstay_schema":2,"test":{"scopes":{"x-go":{"run":["x"],"ci":["linux"]}}}}`,
		`{"jackstay_schema":2,"test":{"scopes":{"x-ios":{"run":["x"],"systems":["darwin"]}}}}`,
		`{"jackstay_schema":2,"test":{"scopes":{"gate":{"deps":["x-go"]},"x-go":{"run":["x"]}}}}`,
		`{"jackstay_schema":2,"receipts":{"commands":[{"argv":["x"],"judge":"go-json"}]}}`,
		`{"jackstay_schema":1,"land":{"autonomy":"prepare"}}`,
		`{"jackstay_schema":2,"land":{"autonomy":"merge-when-green"}}`,
	} {
		parsed, err := Parse([]byte(document))
		if err != nil {
			t.Fatal(err)
		}
		problems := SectionProblems(parsed).Of("")
		if len(problems) == 0 || !strings.Contains(strings.Join(problems, "; "), "need `\"jackstay_schema\": 3`") {
			t.Errorf("%s: %v", document, problems)
		}
		parsed["jackstay_schema"] = json.Number("3")
		if problems := SectionProblems(parsed).Of(""); len(problems) != 0 {
			t.Errorf("%s with schema 3: %v", document, problems)
		}
	}
	parsed, err := Parse([]byte(`{"jackstay_schema":4}`))
	if err != nil {
		t.Fatal(err)
	}
	if problems := SectionProblems(parsed).Of(""); len(problems) != 1 || !strings.Contains(problems[0], "upgrade jackstay") {
		t.Fatalf("a newer schema: %v", problems)
	}
}

func TestOnlyAScopeSaysWhereItRunsAndASchema3CompositeChecksNothing(t *testing.T) {
	problemsOf := func(text string) []string {
		parsed, err := Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		return SectionProblems(parsed).Of("test")
	}
	if found := problemsOf(`{"jackstay_schema":3,"receipts":{"toolchain":[]},"test":{"scopes":{"x-go":{"run":["x"],"ci":["linux"],"systems":["linux","darwin"]},` +
		`"gate":{"deps":["x-go"]}},"steps":{"build":{"run":["y"],"deps":["x-go"]}}}}`); len(found) != 0 {
		t.Fatalf("a scope with ci, systems and deps, a step with deps: %v", found)
	}
	if found := problemsOf(`{"jackstay_schema":3,"test":{"steps":{"build":{"run":["x"],"ci":["linux"]}}}}`); len(found) == 0 {
		t.Fatal("a step with ci was accepted")
	}
	if found := strings.Join(problemsOf(`{"jackstay_schema":3,"receipts":{"toolchain":[]},"test":{"scopes":{"x-go":{"run":["x"]},`+
		`"gate":{"deps":["x-go"],"requires":[{"run":["y"],"why":"z"}]}}}}`), "; "); !strings.Contains(found, "test.scopes.gate.requires") {
		t.Fatalf("requires on a schema-3 composite: %s", found)
	}
	if found := problemsOf(`{"jackstay_schema":2,"receipts":{"toolchain":[]},"test":{"scopes":{"x-go":{"run":["x"]},` +
		`"gate":{"steps":["x-go"],"requires":[{"run":["y"],"why":"z"}]}}}}`); len(found) != 0 {
		t.Fatalf("a schema-2 list keeps its requires: %v", found)
	}
	if found := problemsOf(`{"jackstay_schema":3,"test":{"scopes":{"x-go":{"run":["x"]},"gate":{"deps":["x-go"]}}}}`); len(found) != 0 {
		t.Fatalf("deps without receipts.toolchain: %v", found)
	}
}

func TestBriefExpiryAndOverviewRepositories(t *testing.T) {
	if BriefMaxAge(nil) != BriefMaxAgeHours*time.Hour || BriefMaxAge(map[string]any{"max_age_hours": json.Number("24")}) != 24*time.Hour {
		t.Fatal("brief expiry")
	}
	repositories := StatusRepositories(map[string]any{"repos": []any{"../service", "/abs/app", ""}}, "/work/project")
	if strings.Join(repositories, ",") != "/work/service,/abs/app" {
		t.Fatalf("repositories %v", repositories)
	}
}

// The `resume` key went with `jst test --resume` in 0.6.0: it is refused, in both schemas, by a
// line that says so, not as an unknown key.
func TestTheRemovedResumeKeyIsRefusedByName(t *testing.T) {
	for _, schema := range []string{"2", "3"} {
		parsed, err := Parse([]byte(`{"jackstay_schema": ` + schema + `, "test": {"scopes": {
			"full": {"run": ["true"], "resume": false},
			"gate": {"steps": ["cheap"]}},
			"steps": {"cheap": {"run": ["true"], "resume": true}}}}`))
		if err != nil {
			t.Fatal(err)
		}
		problems := strings.Join(SectionProblems(parsed).Of("test"), "\n")
		for _, where := range []string{"test.scopes.full.resume", "test.steps.cheap.resume"} {
			if !strings.Contains(problems, "`"+where+"` was removed: it went with `jst test --resume` in jackstay 0.6.0") {
				t.Errorf("schema %s, %s: %s", schema, where, problems)
			}
		}
		if strings.Contains(problems, "unknown key") {
			t.Errorf("schema %s: %s", schema, problems)
		}
	}
}
