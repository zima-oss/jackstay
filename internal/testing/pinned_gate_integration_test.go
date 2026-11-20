//go:build integration

package scopes

import (
	"testing"

	"github.com/zima-oss/jackstay/internal/receipts"
)

func TestPinnedGateChecksTrackedInputsAtEachBoundary(t *testing.T) {
	fixtures := []struct {
		name, preparation, reason string
		prerequisite              bool
		status                    int
	}{
		{"tracked file", `printf "%s" "let a = 2" > Sources/a.swift`, "tracked files or the index changed", false, receipts.StatusUnjudged},
		{"index mode", `git update-index --chmod=+x Sources/a.swift`, "tracked files or the index changed", false, receipts.StatusUnjudged},
		{"HEAD", `git checkout -q --detach HEAD^`, "HEAD differs from the selected commit", false, receipts.StatusUnjudged},
		{"hidden index entry", `git update-index --assume-unchanged Sources/a.swift`, "Git did not verify every tracked entry", false, receipts.StatusUnjudged},
		{"replaced with identical bytes", `cp -p Sources/a.swift restored && mv restored Sources/a.swift`, "a tracked file was rewritten during the gate", false, receipts.StatusUnjudged},
		{"prerequisite", `printf "%s" "let a = 2" > Sources/a.swift`, "tracked files or the index changed", true, statusRefused},
		{"untracked artifact", `touch generated.txt`, "", false, 0},
	}
	for _, each := range fixtures {
		t.Run(each.name, func(t *testing.T) {
			preparation := map[string]any{"run": argv("sh", "-c", each.preparation), "judge": "exit"}
			gate := map[string]any{"steps": argv("prepare", "verify")}
			if each.prerequisite {
				gate["requires"] = []any{map[string]any{"run": preparation["run"], "why": "the fixture input must be ready"}}
				preparation["run"] = argv("sh", "-c", "true")
			}
			section := map[string]any{"scopes": map[string]any{"gate": gate}, "steps": map[string]any{
				"prepare": preparation,
				"verify":  map[string]any{"run": counted("verified", "true"), "judge": "exit"},
			}}
			w := newWorld(t, section, nil)
			w.commit()
			commit := w.repo.Git("rev-parse", "HEAD")
			gateRun, status := w.tester().pinnedGate(commit)
			w.expectStatus(status, each.status)
			gateRecord := w.lastRecord(receipts.EventGate)
			if each.status == 0 {
				w.expectCounters("verified", 1)
				if gateRun == "" || gateRecord.Text("gate_run_id") != gateRun || gateRecord.Text("pinned_candidate") != commit {
					t.Fatalf("gate %q did not record exact pinned evidence: %v", gateRun, gateRecord)
				}
			} else {
				w.expectCounters("verified", 0)
				w.expectSaid(each.reason)
				if gateRun != "" || gateRecord.Text("pinned_candidate") != "" || gateRecord.Text("outcome") == receipts.OutcomePassed {
					t.Fatalf("a changed checkout acquired pinned evidence: %v", gateRecord)
				}
			}
		})
	}
}

func TestPinnedGateDoesNotStoreCountedEvidenceAfterATrackedRewrite(t *testing.T) {
	const passing = `<testsuite tests="1"><testcase name="case"/></testsuite>`
	command := argv("sh", "-c", `printf changed > Sources/a.swift; printf '%s' "$1" > "$JACKSTAY_TEST_REPORT"`, "runner", passing)
	section := map[string]any{
		"scopes": map[string]any{"gate": map[string]any{"steps": argv("counted")}},
		"steps": map[string]any{"counted": map[string]any{
			"run": command, "judge": "junit", "report": "tests.xml",
		}},
	}
	w := newWorld(t, section, nil)
	w.commit()
	commit := w.repo.Git("rev-parse", "HEAD")
	gateRun, status := w.tester().pinnedGate(commit)
	w.expectStatus(status, receipts.StatusUnjudged)
	if gateRun != "" {
		t.Fatalf("a changed checkout acquired pinned gate %s", gateRun)
	}
	leaf := w.lastRecord(receipts.EventRun)
	if leaf.Text("outcome") != receipts.OutcomeUnjudged || leaf["stored"] == true || leaf["evidence_count"] != nil {
		t.Fatalf("a changed checkout produced counted evidence: %v", leaf)
	}

	w.expectStatus(w.jst("gate", "--commit", commit), 0)
	if ordinary := w.lastRecord(receipts.EventGate); ordinary.Text("outcome") != receipts.OutcomePassed || ordinary.Text("pinned_candidate") != "" {
		t.Fatalf("ordinary clean-copy behavior changed: %v", ordinary)
	}
}
