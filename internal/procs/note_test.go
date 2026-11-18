package procs

import (
	"testing"

	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/telemetrytest"
)

// A group's events land on the command's span with the fields the procs log held before W-056:
// the files that hold a group, and whether a stop looked and saw the group gone.
func TestNoteKeepsTheFieldsOfAStopAndAHold(t *testing.T) {
	home := telemetrytest.Begin(t, "bg", nil, nil)
	Note("held", map[string]any{"id": "1-2", "reason": "session-gone", "files": []string{"/w/out.bin"}})
	Note("stop", map[string]any{"id": "1-2", "reason": "orphaned", "gone": true, "looked": false})
	Note("unknown", map[string]any{"id": "1-2"})
	telemetry.Finish(0)
	spans := telemetrytest.Named(telemetrytest.Spans(t, home), "jst bg")
	if len(spans) != 1 || len(spans[0].Events) != 2 {
		t.Fatalf("spans %+v", spans)
	}
	held, stop := spans[0].Events[0], spans[0].Events[1]
	if files, _ := held.Attributes[telemetry.AttrBgFiles].([]any); held.Name != telemetry.EventBgHeld || len(files) != 1 || files[0] != "/w/out.bin" {
		t.Fatalf("held %+v", held)
	}
	if stop.Name != telemetry.EventBgStopped || stop.Attributes[telemetry.AttrBgGone] != true || stop.Attributes[telemetry.AttrBgLooked] != false {
		t.Fatalf("stop %+v", stop)
	}
}
