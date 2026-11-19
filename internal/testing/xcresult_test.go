package scopes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zima-oss/jackstay/internal/receipts"
)

func TestXCResultScopeRequiresMatchingBundlesAndBothReports(t *testing.T) {
	scope := map[string]any{
		"judge": "xcresult-json", "reports": argv("summary.json", "tests.json"),
		"xcresult_bundles": argv("LegacyTests", "ModernTests"), "durations": true,
		"run": argv("sh", "-c", `cp "$SIDE/xcode-summary.json" "$1/summary.json" &&
			{ test -f "$SIDE/missing" || cp "$SIDE/xcode-tests.json" "$1/tests.json"; }`, "runner", "{report_dir}"),
	}
	section := map[string]any{"scopes": map[string]any{"x-xcode": scope}}
	world := newWorld(t, section, nil)
	for _, name := range []string{"summary", "tests"} {
		report, err := os.ReadFile(filepath.Join(fixtures, "..", "..", "..", "internal", "judge", "testdata", "xcresult", "pass-"+name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(world.at("xcode-"+name+".json"), report, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	world.expectStatus(world.jst("x-xcode"), 0)
	world.expectLast("outcome=passed", "counted=5", "skipped=2")
	if samples := items(world.lastRecord(receipts.EventRun)["case_durations"]); len(samples) == 0 {
		t.Fatal("the passed result has no measured case timings")
	}
	world.flag("missing", true)
	world.expectStatus(world.jst("x-xcode"), receipts.StatusUnjudged)
	world.flag("missing", false)
	scope["xcresult_bundles"] = argv("LegacyTests", "OtherTests")
	world.configure(section, nil)
	world.expectStatus(world.jst("x-xcode"), receipts.StatusUnjudged)
	scope["xcresult_bundles"] = argv("LegacyTests", "LegacyTests")
	world.configure(section, nil)
	world.expectStatus(world.jst("x-xcode"), statusRefused)
	world.expectSaid("distinct expected unit test bundles")
}
