package config

import (
	"sort"
	"testing"
)

// describedPaths are the key paths of Table that keyDescriptions describes: every key, object, map
// and list, with `*` for a map's entry and `[]` for a list's item when those are objects (a scalar
// entry or item is described by its map or list); the keys of a test step are those of a test
// scope, so they are described once, under test.scopes.*.
func describedPaths() []string {
	var paths []string
	var walk func(schema *schemaType, path string)
	walk = func(schema *schemaType, path string) {
		if path != "" {
			paths = append(paths, path)
		}
		if path == "test.steps" {
			paths = append(paths, path+".*")
			return
		}
		switch schema.class {
		case tObject:
			for key, sub := range schema.keys {
				child := key
				if path != "" {
					child = path + "." + key
				}
				walk(sub, child)
			}
		case tMap:
			if schema.of.class == tObject {
				walk(schema.of, path+".*")
			}
		case tList:
			if schema.of.class == tObject {
				walk(schema.of, path+"[]")
			}
		}
	}
	walk(Table, "")
	sort.Strings(paths)
	return paths
}

func TestEveryKeyIsDescribed(t *testing.T) {
	inTable := map[string]bool{}
	for _, path := range describedPaths() {
		inTable[path] = true
		if keyDescriptions[path] == "" {
			t.Errorf("no description for %s", path)
		}
	}
	for path := range keyDescriptions {
		if !inTable[path] {
			t.Errorf("a description for %s, which is not a key of jackstay.json", path)
		}
	}
}
