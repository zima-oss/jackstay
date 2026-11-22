//go:build integration

package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/review/runner"
)

func executable(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProbeRequiresRestrictedCLI(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   string
	}{
		{"minimum supported", "printf '%s\\n' '2.1.248 (Claude Code)'", "2.1.248"},
		{"later supported", "printf '%s\\n' '2.2.1'", "2.2.1"},
		{"old version", "printf '%s\\n' '2.1.247 (Claude Code)'", ""},
		{"unrecognized", "printf '%s\\n' 'unknown version'", ""},
		{"unexpected version output", "printf '%s\\n' '2.1.248 extra output'", ""},
		{"failed invocation", "printf '%s\\n' '2.1.248 (Claude Code)'; exit 5", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := executable(t, "test \"$#\" -eq 1 && test \"$1\" = --version || exit 7\n"+test.script)
			got, err := Probe(context.Background(), path)
			if got != test.want || (err != nil) != (test.want == "") {
				t.Fatalf("Probe = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

// fixtureBound is the deadline of a fixture run that is not about the deadline: a printf script ends at
// once, so the bound only has to outlast a loaded machine, never to be reached (#81).
const fixtureBound = 2 * time.Minute

func TestUnusableClaudeCodeResults(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   string
	}{
		{"ordinary text", "printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"approve\"}'", runner.OutcomeNoAnswer},
		{"error status", "printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":true,\"structured_output\":{}}'", runner.OutcomeFailed},
		{"duplicate envelope field", "printf '%s\\n' '{\"type\":\"result\",\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"structured_output\":{}}'", runner.OutcomeNoAnswer},
		{"authentication failed", "printf '%s\\n' 'Invalid API key: made-up-key' >&2; exit 1", runner.OutcomeLogin},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			folder := t.TempDir()
			schema := filepath.Join(folder, "schema.json")
			if err := os.WriteFile(schema, []byte(`{"type":"object"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			path := executable(t, "cat >/dev/null\n"+test.script)
			released := false
			result := Run(context.Background(), runner.Request{Command: path, Tree: folder, Folder: folder,
				SchemaPath: schema, Timeout: fixtureBound, Prompt: []byte("review this patch"),
				Environ: []string{"PATH=/bin:/usr/bin"}, Answered: func() { released = true }})
			if result.Outcome != test.want || strings.Contains(result.Reason, "made-up-key") {
				t.Fatalf("unexpected run result: %+v", result)
			}
			if !released {
				t.Fatal("run did not release the start lock")
			}
			if _, err := os.Stat(result.AnswerPath); !os.IsNotExist(err) {
				t.Fatalf("unexpected saved verdict: %v", err)
			}
		})
	}
}
