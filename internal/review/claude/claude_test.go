package claude

import (
	"slices"
	"testing"

	"github.com/zima-oss/jackstay/internal/review/runner"
)

func TestPinnedSourceKeepsRestrictedFileNavigation(t *testing.T) {
	words := argv(runner.Request{PinnedSource: true, Tree: "/projected/review"}, "claude", []byte(`{"type":"object"}`))
	for _, want := range []string{"--restricted", "--safe-mode", "Read,Glob,Grep", "dontAsk", "mcp__*"} {
		if !slices.Contains(words, want) {
			t.Errorf("missing %q: %v", want, words)
		}
	}
	for _, forbidden := range []string{"Bash", "Edit", "Write", "WebFetch", "--add-dir", "--dangerously-skip-permissions"} {
		if slices.Contains(words, forbidden) {
			t.Errorf("unexpected %q: %v", forbidden, words)
		}
	}
}
