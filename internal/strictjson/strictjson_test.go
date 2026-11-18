package strictjson

import (
	"strings"
	"testing"
)

type record struct {
	A int    `json:"a"`
	B string `json:"b,omitempty"`
}

func TestRefuses(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"a member named twice", `{"a": 1, "a": 2}`},
		{"a member the type does not declare", `{"a": 1, "c": 2}`},
		{"a second value", `{"a": 1} {"a": 2}`},
		{"not an object", `[1]`},
		{"a member twice deeper down", `{"a": 1, "b": "x", "b": "y"}`},
		{"empty", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r record
			if err := Unmarshal([]byte(tc.in), &r); err == nil {
				t.Fatalf("%s: read as %+v", tc.in, r)
			}
		})
	}
}

func TestReads(t *testing.T) {
	var r record
	if err := Unmarshal([]byte(" {\"b\": \"x\", \"a\": 2}\n"), &r); err != nil || r.A != 2 || r.B != "x" {
		t.Fatalf("got %+v, %v", r, err)
	}
}

func TestLimit(t *testing.T) {
	var r record
	if err := Read(strings.NewReader(`{"a": 1}`), 8, &r); err != nil {
		t.Fatalf("8 bytes at a limit of 8: %v", err)
	}
	if err := Read(strings.NewReader(`{"a": 10}`), 8, &r); err == nil {
		t.Fatal("9 bytes read at a limit of 8")
	}
}
