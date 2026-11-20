package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// sameKeysAs are the keys whose entries take the keys of another: a test step is a scope.
var sameKeysAs = map[string]string{"test.steps.*": "test.scopes.*"}

// Reference is the configuration reference, docs/reference/configuration.md: every key of
// jackstay.json with its type, its bounds and values, and what it does, made from the editor schema
// (JSONSchema) and keyDescriptions. It is generated (`just reference`) and checked against the file
// by cmd/jst's tests.
func Reference() (string, error) {
	content, err := JSONSchema()
	if err != nil {
		return "", err
	}
	var root map[string]any
	if err := json.Unmarshal(content, &root); err != nil {
		return "", fmt.Errorf("read the schema back: %w", err)
	}
	var page strings.Builder
	page.WriteString("# Configuration reference\n\n")
	page.WriteString("Every key of `jackstay.json`, the file at the root of an adopted project. `jst check` says\n" +
		"whether a file is usable; editors read the same rules from `schemas/jackstay.schema.json`. A key\n" +
		"left out takes its default. This page is generated from the code by `just reference`; change a\n" +
		"key's description in `internal/config/describe.go`, not here.\n\n")
	properties := root["properties"].(map[string]any)
	var plain, sections []string
	for _, key := range sortedKeys(properties) {
		if node := properties[key].(map[string]any); node["type"] == "object" || node["type"] == "array" && isObject(node["items"]) {
			sections = append(sections, key)
		} else {
			plain = append(plain, key)
		}
	}
	page.WriteString("## Top-level keys\n\n| Key | Type | What it does |\n|---|---|---|\n")
	for _, key := range plain {
		row(&page, key, key, properties[key].(map[string]any))
	}
	for _, key := range sections {
		node := properties[key].(map[string]any)
		fmt.Fprintf(&page, "\n## `%s`\n\n%s\n\n| Key | Type | What it does |\n|---|---|---|\n", key, describe(key, node))
		walkRows(&page, key, "", node)
	}
	return page.String(), nil
}

// walkRows writes a row for each key under node: path is its full path, shown the part after the
// section's name.
func walkRows(page *strings.Builder, path, shown string, node map[string]any) {
	if path != "" && shown != "" {
		row(page, path, shown, node)
	}
	if _, same := sameKeysAs[path]; same {
		return
	}
	join := func(name string) (string, string) {
		if shown == "" {
			return path + "." + name, name
		}
		return path + "." + name, shown + "." + name
	}
	switch node["type"] {
	case "object":
		if properties, ok := node["properties"].(map[string]any); ok {
			for _, key := range sortedKeys(properties) {
				full, part := join(key)
				walkRows(page, full, part, properties[key].(map[string]any))
			}
		} else if entry, ok := node["additionalProperties"].(map[string]any); ok && isContainer(entry) {
			full, part := join("*")
			walkRows(page, full, part, entry)
		}
	case "array":
		if item, ok := node["items"].(map[string]any); ok && isObject(item) {
			properties := item["properties"].(map[string]any)
			for _, key := range sortedKeys(properties) {
				full, part := path+"[]."+key, shown+"[]."+key
				if shown == "" {
					part = "[]." + key
				}
				walkRows(page, full, part, properties[key].(map[string]any))
			}
		}
	}
}

func row(page *strings.Builder, path, shown string, node map[string]any) {
	fmt.Fprintf(page, "| `%s` | %s | %s |\n", shown, typeOf(node), strings.ReplaceAll(describe(path, node), "|", `\|`))
}

// describe is a key's description, with its values, bounds and default when the schema has them.
func describe(path string, node map[string]any) string {
	text := keyDescriptions[path]
	if text == "" {
		if other, same := sameKeysAs[path]; same {
			text = keyDescriptions[other]
		}
	}
	var facts []string
	if values, ok := node["enum"].([]any); ok {
		var names []string
		named := true // the description names every value already
		for _, value := range values {
			names = append(names, fmt.Sprintf("`%v`", value))
			named = named && strings.Contains(text, names[len(names)-1])
		}
		if !named {
			facts = append(facts, "One of "+strings.Join(names, ", ")+".")
		}
	}
	if minimum, ok := node["minimum"]; ok {
		facts = append(facts, fmt.Sprintf("From %v to %v; default %v.", minimum, node["maximum"], node["default"]))
	} else if value, ok := node["default"]; ok {
		facts = append(facts, fmt.Sprintf("Default `%v`.", value))
	}
	if other, same := sameKeysAs[path]; same {
		facts = append(facts, fmt.Sprintf("Takes the keys of `%s`.", other))
	}
	return strings.TrimSpace(text + " " + strings.Join(facts, " "))
}

// typeOf names a schema node's type for a reader.
func typeOf(node map[string]any) string {
	switch node["type"] {
	case "string":
		if _, regex := node["minLength"]; regex {
			return "regex"
		}
		return "string"
	case "boolean", "integer":
		return node["type"].(string)
	case "number":
		return "number"
	case "array":
		item, _ := node["items"].(map[string]any)
		return "list of " + plural(typeOf(item))
	case "object":
		if entry, ok := node["additionalProperties"].(map[string]any); ok {
			return "map of " + plural(typeOf(entry))
		}
		return "object"
	}
	return "value"
}

func plural(name string) string {
	switch {
	case strings.HasPrefix(name, "list of "), strings.HasPrefix(name, "map of "):
		return name
	case name == "regex":
		return "regexes"
	}
	return name + "s"
}

func isObject(value any) bool {
	node, ok := value.(map[string]any)
	return ok && node["type"] == "object" && node["properties"] != nil
}

func isContainer(node map[string]any) bool {
	return node["type"] == "object" || node["type"] == "array"
}
