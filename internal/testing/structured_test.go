package scopes

import (
	"os"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/receipts"
)

func TestStructuredScopesRecordVerdictsAndUsePrivateReportPaths(t *testing.T) {
	const passing = `<testsuite tests="2" skipped="1" failures="0" errors="0"><testcase name="pass"/><testcase name="skip"><skipped/></testcase></testsuite>`
	const empty = `<testsuite tests="0" skipped="0" failures="0" errors="0"/>`
	scopes := map[string]any{
		"x-go-json": map[string]any{"judge": "go-json", "run": argv("sh", "-c", `printf '%s\n' "$@"`, "runner",
			`{"Action":"start","Package":"demo"}`, `{"Action":"run","Package":"demo","Test":"TestIt"}`,
			`{"Action":"pass","Package":"demo","Test":"TestIt"}`, `{"Action":"pass","Package":"demo"}`)},
	}
	for _, each := range []struct{ name, script, report string }{
		{"x-junit", `test "$1" = "$JACKSTAY_TEST_REPORT" || exit 9; printf '%s\n' "$1" >> "$SIDE/attempts"; printf '%s' "$2" > "$1"`, passing},
		{"x-empty", `printf '%s' "$2" > "$1"; exit 5`, empty},
		{"x-missing", `:`, "unused"},
		{"x-broken", `printf '%s' "$2" > "$1"`, "<testsuite>"},
		{"x-failed", `printf '%s' "$2" > "$1"`, `<testsuite tests="1"><testcase name="fails"><failure/></testcase></testsuite>`},
	} {
		scopes[each.name] = map[string]any{"judge": "junit", "report": "report.xml", "empty_exit": 5,
			"run": argv("sh", "-c", each.script, "runner", "{report}", each.report)}
	}
	world := newWorld(t, map[string]any{"scopes": scopes}, nil)
	for _, each := range []struct {
		name    string
		status  int
		outcome string
		count   float64
	}{
		{"x-go-json", 0, receipts.OutcomePassed, 1},
		{"x-junit", 0, receipts.OutcomePassed, 1},
		{"x-empty", receipts.StatusNothingRun, receipts.OutcomeNothingRun, 0},
		{"x-missing", receipts.StatusUnjudged, receipts.OutcomeUnjudged, 0},
		{"x-broken", receipts.StatusUnjudged, receipts.OutcomeUnjudged, 0},
		{"x-failed", 1, receipts.OutcomeFailed, 1},
	} {
		world.expectStatus(world.jst(each.name), each.status)
		record := world.lastRecord(receipts.EventRun)
		world.expectLast("outcome=" + each.outcome)
		if counted, _ := record.Number("counted"); record.Text("outcome") != each.outcome || counted != each.count {
			t.Fatalf("%s: %v", each.name, record)
		}
	}
	world.expectStatus(world.jst("x-junit", "--no-reuse"), 0)
	paths := strings.Fields(world.read("attempts"))
	if len(paths) != 2 || paths[0] == paths[1] {
		t.Fatalf("report paths %q", paths)
	}
	for _, name := range paths {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("report path survived the attempt: %s (%v)", name, err)
		}
	}
}
