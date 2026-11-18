//go:build latency

// Package latency measures the guard hook's wall time with telemetry off and on (docs/design/
// observability.md, section 8): the built binary, a recorded allowed and a refused call, runs
// interleaved, a fresh jackstay home. Not in the gate (timing is no pass/fail on a shared machine):
// `just guard-latency`; its numbers belong with the change that moves them.
package latency

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

func TestGuardLatency(t *testing.T) {
	runs := 500
	if text := os.Getenv("JACKSTAY_LATENCY_RUNS"); text != "" {
		runs, _ = strconv.Atoi(text)
	}
	root, _ := filepath.Abs("../../..")
	binary := filepath.Join(t.TempDir(), "jst")
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/jst")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	// JACKSTAY_LATENCY_BASELINE names a binary built without telemetry (main before it): measured beside.
	binaries := map[string]string{"on": binary, "off": binary}
	modes := []string{"off", "on"}
	if baseline := os.Getenv("JACKSTAY_LATENCY_BASELINE"); baseline != "" {
		binaries["before"] = baseline
		modes = append(modes, "before")
	}
	home := t.TempDir()
	calls := map[string]string{"allowed": "git status --short", "refused": "git reset --hard HEAD~1"}
	results := map[string][]time.Duration{}
	for round := 0; round < runs; round++ {
		for _, kind := range []string{"allowed", "refused"} {
			for _, mode := range modes {
				input, _ := json.Marshal(map[string]any{"session_id": "latency-session", "tool_name": "Bash",
					"tool_use_id": fmt.Sprintf("toolu_%d", round), "cwd": root, "hook_event_name": "PreToolUse",
					"tool_input": map[string]any{"command": calls[kind]}})
				command := exec.Command(binaries[mode], "guard")
				command.Dir = root
				command.Stdin = bytes.NewReader(input)
				command.Env = append(os.Environ(), "JACKSTAY_HOME="+home, "JACKSTAY_TELEMETRY="+mode)
				started := time.Now()
				if err := command.Run(); err != nil {
					t.Fatal(err)
				}
				results[kind+" "+mode] = append(results[kind+" "+mode], time.Since(started))
			}
		}
	}
	keys := make([]string, 0, len(results))
	for key := range results {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	median := map[string]time.Duration{}
	for _, key := range keys {
		sorted := results[key]
		sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
		median[key] = sorted[len(sorted)/2]
		t.Logf("%-12s runs %d  median %6.2f ms  p95 %6.2f ms", key, len(sorted), ms(median[key]),
			ms(sorted[len(sorted)*95/100]))
	}
	for _, kind := range []string{"allowed", "refused"} {
		t.Logf("%s: telemetry adds %+.2f ms on the median (on − off)", kind, ms(median[kind+" on"]-median[kind+" off"]))
		if _, ok := median[kind+" before"]; ok {
			t.Logf("%s: against the binary before it %+.2f ms on the median (on − before)", kind, ms(median[kind+" on"]-median[kind+" before"]))
		}
	}
}

func ms(duration time.Duration) float64 { return float64(duration.Microseconds()) / 1000 }
