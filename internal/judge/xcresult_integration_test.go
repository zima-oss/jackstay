//go:build integration

package judge

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Set XCRESULT_PILOT_ROOT to an output of tests/producers/swift/capture.sh.
// The ordinary gate uses sanitized snapshots and needs no Xcode installation.
func TestXCResultToolExportsMatchThePilot(t *testing.T) {
	root := os.Getenv("XCRESULT_PILOT_ROOT")
	if root == "" {
		t.Skip("set XCRESULT_PILOT_ROOT to a Swift pilot capture directory")
	}
	version, err := exec.Command("xcodebuild", "-version").CombinedOutput()
	if err != nil || !strings.Contains(string(version), "Xcode 27.2") {
		t.Fatalf("expected Xcode 27.2, got %q: %v", version, err)
	}
	for _, sample := range []struct {
		name string
		want Outcome
	}{
		{"pass", Passed},
		{"failed", Failed},
	} {
		t.Run(sample.name, func(t *testing.T) {
			bundle := filepath.Join(root, "xcode-"+sample.name+".xcresult")
			export := func(part string) []byte {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				result, err := exec.CommandContext(ctx, "xcrun", "xcresulttool", "get", "test-results", part,
					"--path", bundle, "--compact").CombinedOutput()
				if err != nil {
					t.Fatalf("xcresulttool %s: %v: %s", part, err, result)
				}
				return result
			}
			facts, _, _ := readXCResultForBundles(bytes.NewReader(export("summary")), bytes.NewReader(export("tests")),
				[]string{"ModernTests", "LegacyTests"})
			if verdict := Evaluate(facts, End{}, ScopePolicy); verdict.Outcome != sample.want || verdict.Counted != 5 || verdict.Skipped != 2 {
				t.Fatalf("Xcode %s verdict %+v", sample.name, verdict)
			}
		})
	}
}
