package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
	chat := properties["chat"].(map[string]any)["properties"].(map[string]any)
	for key, wanted := range chatSection.keys {
		if wanted.maximum == 0 {
			continue
		}
		field := chat[key].(map[string]any)
		if field["minimum"] != float64(wanted.minimum) || field["maximum"] != float64(wanted.maximum) || field["default"] != float64(wanted.defaultInt) {
			t.Errorf("chat.%s schema bounds/default differ from runtime", key)
		}
	}
}

func TestChatAndQueueProjectPolicy(t *testing.T) {
	parsed, err := Parse([]byte(`{
		"$schema": "./schemas/jackstay.schema.json",
		"jackstay_schema": 2,
		"chat": {"enabled": true, "max_pending": 24},
		"queue": {"enabled": true}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	policy := ChatSettings(parsed.Section("chat"))
	if !policy.Enabled || policy.MaxPending != 24 || policy.MaxMessageBytes != 4096 || policy.RetentionHours != 72 {
		t.Fatalf("unexpected chat limits: %+v", policy)
	}
	if problems := SectionProblems(parsed).Of("queue"); len(problems) != 1 {
		t.Fatalf("expected a missing base on enabled queue, got %v", problems)
	}
	parsed.Section("chat")["max_pending"] = json.Number("513")
	if problems := SectionProblems(parsed).Of("chat"); len(problems) != 1 {
		t.Fatalf("expected an inbox size limit, got %v", problems)
	}
}

func TestChatQuotaAndCodexDefaults(t *testing.T) {
	parsed, err := Parse([]byte(`{"jackstay_schema":2,"chat":{"enabled":true,"max_pending":8,"codex_bridge":{"enabled":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	section := parsed.Section("chat")
	policy := ChatSettings(section)
	if policy.MaxPendingPerSession != 8 || !policy.CodexBridgeEnabled || policy.CodexPollIntervalMS != 1000 || policy.CodexAckTimeoutSeconds != 30 {
		t.Fatalf("unexpected effective policy: %+v", policy)
	}
	section["max_pending_per_session"] = json.Number("9")
	if problems := SectionProblems(parsed).Of("chat"); len(problems) != 1 {
		t.Fatalf("expected a per-session quota error: %v", problems)
	}
}
