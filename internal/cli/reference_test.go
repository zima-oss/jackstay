package cli

import (
	"strings"
	"testing"
)

func TestReferenceEscapesWhatMarkdownWouldMisread(t *testing.T) {
	if got := tableCell("the files under <dir> | `jst x <y>`"); got != "the files under \\<dir\\> \\| `jst x <y>`" {
		t.Errorf("tableCell = %q", got)
	}
	if got := sentence("is it usable? (for the gate)"); got != "Is it usable? (for the gate)" {
		t.Errorf("sentence = %q", got)
	}
}

func TestReferenceHasEveryCommandWithItsUsage(t *testing.T) {
	registerForTest(t, "fake sub")
	page := Reference()
	if !strings.Contains(page, "| [`jst fake sub`](#jst-fake-sub) | the summary of fake sub |") ||
		!strings.Contains(page, "\n## Other\n\n| Command | What it does |\n|---|---|\n| [`jst fake sub`]") ||
		!strings.Contains(page, "\n### jst fake sub\n\nThe summary of fake sub.\n\n```text\nusage: jst fake sub\n") {
		t.Fatalf("the reference:\n%s", page)
	}
}
