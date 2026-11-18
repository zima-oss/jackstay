// Package telemetrytest runs a command's telemetry inside a test: a jackstay home of its own, a clean
// environment, and a reader of what was written.
package telemetrytest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
)

// Begin starts a command's telemetry in a jackstay home of the test's own; the environment that would
// steer it (a parent's TRACEPARENT, a Claude session, the switch) is cleared unless env sets it.
func Begin(t *testing.T, command string, args []string, env map[string]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	for _, name := range []string{telemetry.TraceparentVariable, telemetry.TracestateVariable,
		telemetry.ClaudeSessionVariable, telemetry.SwitchVariable} {
		t.Setenv(name, env[name])
		if env[name] == "" {
			_ = os.Unsetenv(name)
		}
	}
	telemetry.Reset()
	t.Cleanup(telemetry.Reset)
	telemetry.Begin(command, args)
	return home
}

// Spans reads every span written under home, in order of start.
func Spans(t *testing.T, home string) []journal.Span {
	t.Helper()
	folders, _ := filepath.Glob(filepath.Join(home, "*", telemetry.FolderName))
	var spans []journal.Span
	far := time.Now().Add(-24 * time.Hour * 400)
	result := journal.Read(folders, far, time.Now().Add(time.Hour), time.Now().Add(24*time.Hour), func(span journal.Span) {
		spans = append(spans, span)
	})
	if result.Unreadable > 0 {
		t.Fatalf("%d unreadable lines", result.Unreadable)
	}
	journal.SortByStart(spans)
	return spans
}

// Named is the spans of a name.
func Named(spans []journal.Span, name string) []journal.Span {
	var found []journal.Span
	for _, span := range spans {
		if span.Name == name {
			found = append(found, span)
		}
	}
	return found
}
