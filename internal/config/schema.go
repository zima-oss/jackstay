package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// JSONSchema returns the editor-facing JSON Schema for jackstay.json. The keys
// and structural types come from Table, the same table used by jst check.
// Cross-field and filesystem rules remain the responsibility of jst check.
func JSONSchema() ([]byte, error) {
	root := schemaNode(Table)
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["$id"] = EditorSchemaURL()
	root["title"] = "jackstay.json"
	root["description"] = "Project configuration for jackstay. Run jst check for semantic and cross-field validation."
	root["required"] = []string{"jackstay_schema"}
	setSchemaHints(root)
	content, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal jackstay JSON Schema: %w", err)
	}
	return append(content, '\n'), nil
}

func schemaNode(wanted *schemaType) map[string]any {
	switch wanted.class {
	case tStr:
		return map[string]any{"type": "string", "pattern": "\\S"}
	case tText:
		return map[string]any{"type": "string"}
	case tBool:
		return map[string]any{"type": "boolean"}
	case tInt:
		result := map[string]any{"type": "integer"}
		if wanted.maximum > 0 {
			result["minimum"] = wanted.minimum
			result["maximum"] = wanted.maximum
			result["default"] = wanted.defaultInt
		}
		return result
	case tNumber:
		return map[string]any{"type": "number", "exclusiveMinimum": 0}
	case tRegex:
		// JSON Schema's regex dialect differs from Go's RE2. jst check judges syntax.
		return map[string]any{"type": "string", "minLength": 1, "description": "Go RE2 regular expression; validate with jst check."}
	case tList:
		return map[string]any{"type": "array", "items": schemaNode(wanted.of)}
	case tMap:
		return map[string]any{"type": "object", "additionalProperties": schemaNode(wanted.of)}
	case tObject:
		properties := make(map[string]any, len(wanted.keys))
		for key, sub := range wanted.keys {
			properties[key] = schemaNode(sub)
		}
		return map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	default:
		panic("unknown jackstay schema type")
	}
}

func setSchemaHints(root map[string]any) {
	properties := root["properties"].(map[string]any)
	properties["jackstay_schema"].(map[string]any)["enum"] = []int{1, Schema}
	queue := properties["queue"].(map[string]any)
	queue["if"] = map[string]any{"required": []string{"enabled"}, "properties": map[string]any{"enabled": map[string]any{"const": true}}}
	queue["then"] = map[string]any{"required": []string{"base_ref"}}
	chat := properties["chat"].(map[string]any)
	enabled := map[string]any{"required": []string{"enabled"}, "properties": map[string]any{"enabled": map[string]any{"const": true}}}
	chat["if"] = map[string]any{
		"required": []string{"codex_bridge"}, "properties": map[string]any{"codex_bridge": enabled},
	}
	chat["then"] = enabled
	chat["properties"].(map[string]any)["max_pending_per_session"].(map[string]any)["description"] =
		"Pending messages per recipient. The default is min(32, max_pending); an explicit value must not exceed max_pending."
	testJudges := []string{"swift-test", "steps", "exit", "go-json", "junit", "jest-json", "trx", "xcresult-json"}
	for path, values := range map[string][]string{
		"baseline.merge_unit":        {"pr", "commit"},
		"cards.unit":                 {"pr", "commit"},
		"changes.newest":             {"first", "last"},
		"guard.rules.*.level":        {"refuse", "warn", "off"},
		"telemetry.commands":         {"masked", "skeleton"},
		"test.judge":                 testJudges,
		"test.scopes.*.judge":        testJudges,
		"test.steps.*.judge":         testJudges,
		"test.scopes.*.selector":     {"go-test", "jest-test"},
		"receipts.commands.[].judge": {"go-json", "junit", "jest-json", "trx", "xcresult-json"},
	} {
		node := root
		for _, part := range strings.Split(path, ".") {
			switch part {
			case "*":
				node = node["additionalProperties"].(map[string]any)
			case "[]":
				node = node["items"].(map[string]any)
			default:
				node = node["properties"].(map[string]any)[part].(map[string]any)
			}
		}
		node["enum"] = values
	}
}
