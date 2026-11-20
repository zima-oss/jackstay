package supervise

import (
	"slices"
	"testing"
)

func TestTelemetryKeepsTheTraceArgvWhenOneIsGiven(t *testing.T) {
	argv := []string{"codex", "-c", "mcp_servers.x={args=[\"made-up\"]}"}
	if kept := traceArgv(Spec{Argv: argv}); !slices.Equal(kept, argv) {
		t.Fatalf("without a trace argv: %v", kept)
	}
	redacted := []string{"codex", "-c", "mcp_servers.x=<redacted>"}
	if kept := traceArgv(Spec{Argv: argv, TraceArgv: redacted}); !slices.Equal(kept, redacted) {
		t.Fatalf("with a trace argv: %v", kept)
	}
}
