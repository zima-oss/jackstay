//go:build integration

package scopes

import (
	"os"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/receipts"
)

func TestGoTypedSelectionAndDurationsWithRealGo(t *testing.T) {
	const pkg = "example.invalid/parser"
	world := newWorld(t, map[string]any{"scopes": map[string]any{"focused": map[string]any{
		"judge": "go-json", "selector": "go-test", "durations": true,
		"run": argv("go", "test", "-json", "-count=1", "{test_args}"),
	}}}, nil)
	if cache := os.Getenv("GOCACHE"); cache != "" {
		world.env["GOCACHE"] = cache
	}
	world.env["GOTOOLCHAIN"] = "local"
	world.repo.Write(map[string]string{
		"go.mod": "module " + pkg + "\n\ngo 1.27.0\n",
		"fixture_test.go": `package parser
import "testing"

func TestParse(t *testing.T) {
	t.Run("[a]+x", func(t *testing.T) {})
	t.Run("wrong", func(t *testing.T) { t.Fatal("unexpected subtest") })
}
func TestParseOther(t *testing.T) { t.Fatal("unexpected top-level test") }
`,
	})
	id := pkg + "::TestParse/[a]+x"
	world.expectStatus(world.jst("plan", "focused", id), 0)
	if !strings.Contains(world.stdout.String(), `-run=^TestParse$/^\[a\]\+x$`) || len(world.launches) != 0 {
		t.Fatalf("unexpected plan: %s", world.stdout.String())
	}
	world.expectStatus(world.jst("focused", id), 0)
	world.expectLast("outcome=passed", "counted=1")
	run := world.lastRecord(receipts.EventRun)
	samples := items(run["case_durations"])
	if len(samples) != 1 {
		t.Fatalf("recorded samples = %v, want one", samples)
	}
	sample := objectOf(samples[0])
	seconds, measured := numberOf(sample["seconds"])
	if sample["package"] != pkg || sample["test"] != "TestParse/[a]+x" || !measured || seconds < 0 {
		t.Fatalf("unexpected timing sample: %v", sample)
	}

	world.expectStatus(world.jst("focused", pkg+"::TestParse/absent"), receipts.StatusNothingRun)
	world.expectLast("outcome=nothing-run")
	if _, exists := world.lastRecord(receipts.EventRun)["case_durations"]; exists {
		t.Fatal("ancestor-only no-match recorded timing")
	}
}
