package config

import (
	"strings"
	"testing"
)

func TestTelemetrySection(t *testing.T) {
	cases := map[string]string{
		`{"jackstay_schema": 2, "telemetry": {"keep_days": 30, "compress_after_days": 7, "commands": "skeleton"}}`: "",
		`{"jackstay_schema": 2, "telemetry": {}}`:                     "",
		`{"jackstay_schema": 2, "telemetry": {"keep_days": 0}}`:       "must be from 1 to 3650",
		`{"jackstay_schema": 2, "telemetry": {"keep_days": 10}}`:      "must not be less than",
		`{"jackstay_schema": 2, "telemetry": {"commands": "raw"}}`:    "\"masked\" or \"skeleton\"",
		`{"jackstay_schema": 2, "telemetry": {"keep": 1}}`:            "keep",
		`{"jackstay_schema": 2, "telemetry": {"keep_days": "a lot"}}`: "keep_days",
	}
	for text, want := range cases {
		parsed, err := Parse([]byte(text))
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		got := strings.Join(SectionProblems(parsed).Of("telemetry"), "; ")
		if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("%s: got %q, want %q", text, got, want)
		}
	}
}
