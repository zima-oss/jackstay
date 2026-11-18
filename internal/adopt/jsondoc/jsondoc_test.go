package jsondoc

import (
	"strings"
	"testing"
)

func TestKeysKeepTheirOrderAndNewOnesGoAfter(t *testing.T) {
	value, err := Parse([]byte(`{"zeta": 1, "alpha": {"b": [1, 2], "a": "x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	object := value.(*Object)
	object.Set("middle", true)
	object.Object("alpha").Set("c", nil)
	want := `{
  "zeta": 1,
  "alpha": {
    "b": [
      1,
      2
    ],
    "a": "x",
    "c": null
  },
  "middle": true
}
`
	if got := string(Format(object)); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestFormatThenParseGivesTheSameBytes(t *testing.T) {
	text := "{\n  \"a\": [],\n  \"b\": {},\n  \"c\": \"é <&> \\\"q\\\"\",\n  \"d\": 1.50\n}\n"
	value, err := Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(Format(value)); got != text {
		t.Fatalf("got %q want %q", got, text)
	}
}

func TestAKeyWrittenTwiceIsRefused(t *testing.T) {
	if _, err := Parse([]byte(`{"a": 1, "a": 2}`)); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("got %v", err)
	}
	if _, err := Parse([]byte(`{} {}`)); err == nil {
		t.Fatal("two values were read as one")
	}
}

func TestEqualSeesOrderAndValues(t *testing.T) {
	first, _ := Parse([]byte(`{"a": 1, "b": [true]}`))
	second, _ := Parse([]byte(`{"a": 1, "b": [true]}`))
	reordered, _ := Parse([]byte(`{"b": [true], "a": 1}`))
	changed, _ := Parse([]byte(`{"a": 1, "b": [false]}`))
	if !Equal(first, second) || Equal(first, reordered) || Equal(first, changed) {
		t.Fatal("Equal judged wrongly")
	}
	copied := Clone(first)
	copied.(*Object).Set("c", "new")
	if Equal(first, copied) {
		t.Fatal("Clone shares its object with the original")
	}
}

func TestFromPlainTakesKeysInLexicalOrder(t *testing.T) {
	value := FromPlain(map[string]any{"z": 1, "a": map[string]any{"y": "1", "b": []any{2.0}}})
	want := "{\n  \"a\": {\n    \"b\": [\n      2\n    ],\n    \"y\": \"1\"\n  },\n  \"z\": 1\n}\n"
	if got := string(Format(value)); got != want {
		t.Fatalf("got\n%s", got)
	}
	plain := ToPlain(value).(map[string]any)
	if len(plain) != 2 {
		t.Fatalf("ToPlain: %v", plain)
	}
}
