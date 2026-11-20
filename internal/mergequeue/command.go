package mergequeue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/receipts"
	scopes "github.com/zima-oss/jackstay/internal/testing"
)

const queueHelp = `usage: jst queue add --head REF [--pr NUMBER]
       jst queue prepare | test | status | handoff | reconcile --commit SHA | remove

The queue is enabled by queue.enabled and queue.base_ref in jackstay.json. It holds one head.
prepare writes an unreachable Git candidate commit; test runs the project gate on a clean copy
and requires counted portable receipts. With --pr, handoff reads current GitHub PR reviews,
required checks and test-merge tree. Without --pr it is explicitly local-only. In both modes,
handoff prints the candidate and evidence for a manual merge; it never changes a Git ref or
requests a GitHub merge. After landing, reconcile checks the observed target tree.

exit: 0 done; 1 no lane, conflict, failed gate or stale candidate; 2 invalid use or configuration.

example: jst queue add --head refs/heads/my-change
`

func init() {
	cli.Register(
		cli.Command{Name: "queue", Summary: "local single-head merge candidate and manual handoff", Usage: queueHelp, Run: queueUsage},
		cli.Command{Name: "queue add", Summary: "pin one head in the local merge queue", Usage: queueHelp, Run: addCommand},
		cli.Command{Name: "queue prepare", Summary: "build a candidate commit without changing refs", Usage: queueHelp, Run: prepareCommand},
		cli.Command{Name: "queue test", Summary: "run the gate on the candidate and save counted evidence", Usage: queueHelp, Run: testCommand},
		cli.Command{Name: "queue status", Summary: "show the candidate and input freshness", Usage: queueHelp, Run: statusCommand},
		cli.Command{Name: "queue handoff", Summary: "verify freshness and show manual landing evidence", Usage: queueHelp, Run: handoffCommand},
		cli.Command{Name: "queue reconcile", Summary: "record the observed landing and compare its tree", Usage: queueHelp, Run: reconcileCommand},
		cli.Command{Name: "queue remove", Summary: "empty the local queue without changing refs", Usage: queueHelp, Run: removeCommand},
	)
}

func queueUsage(_ []string, _ io.Reader, _, stderr io.Writer) int {
	fmt.Fprint(stderr, queueHelp)
	return 2
}

func commandService(stderr io.Writer) (Service, bool) {
	here, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "jst queue: working folder: %v\n", err)
		return Service{}, false
	}
	root, loaded, err := config.Load(here, []string{"queue"})
	if err != nil || root == "" {
		fmt.Fprintf(stderr, "jst queue: project configuration: %v\n", err)
		return Service{}, false
	}
	section := loaded.Section("queue")
	if section["enabled"] != true {
		fmt.Fprintln(stderr, "jst queue: set queue.enabled to true in jackstay.json")
		return Service{}, false
	}
	base, _ := section["base_ref"].(string)
	if base == "" {
		fmt.Fprintln(stderr, "jst queue: set queue.base_ref to a moving branch ref")
		return Service{}, false
	}
	service := Service{Here: here, Project: config.ProjectName(root, loaded, here), BaseRef: base}
	service.RequirePullRequest, _ = section["require_pull_request"].(bool)
	service.ReviewTask, _ = section["review_task"].(string)
	return service, true
}

func addCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 2 && len(args) != 4 || args[0] != "--head" || len(args) == 4 && args[2] != "--pr" {
		fmt.Fprint(stderr, queueHelp)
		return 2
	}
	var pullRequest []int
	if len(args) == 4 {
		number, err := strconv.Atoi(args[3])
		if err != nil || number <= 0 {
			fmt.Fprintln(stderr, "jst queue: --pr requires a positive number")
			return 2
		}
		pullRequest = append(pullRequest, number)
	}
	service, ok := commandService(stderr)
	if !ok {
		return 2
	}
	state, err := service.Enqueue(context.Background(), args[1], pullRequest...)
	if err != nil {
		return queueError(stderr, err)
	}
	fmt.Fprintf(stdout, "queued %s at %s against %s at %s\n", state.HeadRef, state.Head, state.BaseRef, state.Base)
	return 0
}

func prepareCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprint(stderr, queueHelp)
		return 2
	}
	service, ok := commandService(stderr)
	if !ok {
		return 2
	}
	state, err := service.Prepare(context.Background())
	if err != nil {
		return queueError(stderr, err)
	}
	fmt.Fprintf(stdout, "candidate %s tree %s attempt %d\n", state.Candidate, state.Tree, state.Attempt)
	return 0
}

func testCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprint(stderr, queueHelp)
		return 2
	}
	service, ok := commandService(stderr)
	if !ok {
		return 2
	}
	return service.testGate(context.Background(), scopes.PinnedGate, stdout, stderr)
}

func (service Service) testGate(ctx context.Context, runGate func(string, io.Writer, io.Writer) (string, int), stdout, stderr io.Writer) int {
	prepared, err := service.Prepare(ctx)
	if err != nil {
		return queueError(stderr, err)
	}
	state, err := service.beginGate(ctx, prepared)
	if err != nil {
		return queueError(stderr, err)
	}
	gateID, status := runGate(state.Candidate, stdout, stderr)
	if status != 0 {
		return queueError(stderr, fmt.Errorf("gate did not pass (exit %d)", status))
	}
	records := receipts.ReadRecords(receipts.RunLogPaths())
	pinnedPass := false
	for _, record := range records {
		if gateID != "" && record.Text("gate_run_id") == gateID && record.Text("event") == receipts.EventGate &&
			record.Text("project") == service.Project && record.Text("outcome") == receipts.OutcomePassed &&
			record.Text("head") == state.Candidate && record.Text("pinned_candidate") == state.Candidate {
			pinnedPass = true
			break
		}
	}
	if !pinnedPass {
		return queueError(stderr, fmt.Errorf("no passed pinned gate record for candidate %s", state.Candidate))
	}
	var evidence []Evidence
	for _, record := range records {
		if record.Text("event") != receipts.EventRun || record.Text("project") != service.Project ||
			record.Text("gate_run_id") != gateID || record.Text("head") != state.Candidate ||
			record.Text("outcome") != receipts.OutcomePassed {
			continue
		}
		count, ok := record.Number("evidence_count")
		key, runID := record.Text("key"), record.Text("run_id")
		if !ok || count <= 0 || record["stored"] != true || !objectID(key) || runID == "" {
			continue
		}
		data, err := receipts.ExportForRun(key, runID)
		if err != nil || !validEvidence(data, state.Candidate, key) {
			continue
		}
		evidence = append(evidence, Evidence{Key: key, SHA256: digest(data)})
	}
	if len(evidence) == 0 {
		return queueError(stderr, fmt.Errorf("gate passed but no counted receipt for candidate %s was exportable", state.Candidate))
	}
	if _, err := service.recordGate(ctx, state, gateID, evidence); err != nil {
		return queueError(stderr, err)
	}
	fmt.Fprintf(stdout, "queue candidate %s: gate %s passed, %d counted receipt(s) saved\n", state.Candidate, gateID, len(evidence))
	return 0
}

func statusCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprint(stderr, queueHelp)
		return 2
	}
	service, ok := commandService(stderr)
	if !ok {
		return 2
	}
	state, err := service.Status(context.Background())
	if err != nil {
		return queueError(stderr, err)
	}
	status := queueStatus(state)
	fmt.Fprintf(stdout, "%s: %s %s base=%s head=%s candidate=%s attempt=%d receipts=%d pr=%d\n",
		status, state.HeadRef, state.BaseRef, state.Base, state.Head, state.Candidate, state.Attempt, len(state.Receipts), state.PullRequest)
	if state.Outcome != nil {
		fmt.Fprintf(stdout, "observed commit %s tree %s, tested tree %s\n", state.Outcome.Commit, state.Outcome.Tree, state.Tree)
	}
	return 0
}

func queueStatus(state *State) string {
	if state.Outcome != nil {
		if state.Outcome.Matched {
			return "landed-matched"
		}
		return "landed-mismatch"
	}
	if state.Stale {
		return "stale"
	}
	if !state.HandoffAt.IsZero() {
		return "handed-off"
	}
	if state.GateClaim != "" && state.GateRunID != "" {
		return "tested"
	}
	if state.Candidate != "" {
		return "prepared"
	}
	return "queued"
}

func handoffCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprint(stderr, queueHelp)
		return 2
	}
	service, ok := commandService(stderr)
	if !ok {
		return 2
	}
	state, err := service.handoff(context.Background(), func(candidate *State) error {
		for _, item := range candidate.Receipts {
			data, err := exportReceipt(item.Key)
			if err != nil || digest(data) != item.SHA256 || !validEvidence(data, candidate.Candidate, item.Key) {
				return fmt.Errorf("receipt %s is no longer verifiable", item.Key)
			}
		}
		return nil
	})
	if err != nil {
		return queueError(stderr, err)
	}
	state, err = service.recordHandoff(context.Background(), state)
	if err != nil {
		return queueError(stderr, err)
	}
	if state.PullRequest == 0 {
		fmt.Fprintln(stdout, "manual handoff only: local evidence verified, GitHub PR review and required-check freshness unchecked")
	} else {
		fmt.Fprintf(stdout, "manual handoff only: PR #%d review and required checks were current at the final read\n", state.PullRequest)
	}
	fmt.Fprintf(stdout, "base %s=%s, head %s=%s\n", state.BaseRef, state.Base, state.HeadRef, state.Head)
	fmt.Fprintf(stdout, "tested candidate %s tree %s gate %s\n", state.Candidate, state.Tree, state.GateRunID)
	for _, item := range state.Receipts {
		fmt.Fprintf(stdout, "counted receipt %s sha256 %s\n", item.Key, item.SHA256)
	}
	fmt.Fprintln(stdout, "Recheck the live base/head and compare the landed tree after a human merge; this handoff does not prove the provider's atomic merge decision.")
	return 0
}

func reconcileCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "--commit" || !objectID(args[1]) {
		fmt.Fprint(stderr, queueHelp)
		return 2
	}
	service, ok := commandService(stderr)
	if !ok {
		return 2
	}
	state, err := service.Reconcile(context.Background(), args[1])
	if err != nil {
		return queueError(stderr, err)
	}
	if state.Outcome.Matched {
		fmt.Fprintf(stdout, "observed landing %s tree %s matches tested candidate %s\n", state.Outcome.Commit, state.Outcome.Tree, state.Candidate)
		return 0
	}
	fmt.Fprintf(stderr, "jst queue: observed landing %s tree %s differs from tested candidate tree %s; outcome recorded for inspection\n",
		state.Outcome.Commit, state.Outcome.Tree, state.Tree)
	return 1
}

func removeCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprint(stderr, queueHelp)
		return 2
	}
	service, ok := commandService(stderr)
	if !ok {
		return 2
	}
	if err := service.Remove(context.Background()); err != nil {
		return queueError(stderr, err)
	}
	fmt.Fprintln(stdout, "local queue emptied; Git refs were not changed")
	return 0
}

func queueError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "jst queue: %v\n", err)
	return 1
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type portableEvidence struct {
	Schema   int    `json:"schema"`
	Head     string `json:"head"`
	Key      string `json:"key"`
	Manifest string `json:"manifest"`
	Outcome  string `json:"outcome"`
	Count    int    `json:"evidence_count"`
}

func validEvidence(data []byte, candidate, key string) bool {
	var found portableEvidence
	return json.Unmarshal(data, &found) == nil && found.Schema == 1 && found.Head == candidate &&
		found.Key == key && found.Outcome == receipts.OutcomePassed && found.Count > 0 && objectID(found.Manifest)
}

func exportReceipt(key string) ([]byte, error) {
	var output, diagnostics bytes.Buffer
	if cli.Dispatch([]string{"receipts", "export", key}, nil, &output, &diagnostics) != 0 {
		return nil, fmt.Errorf("receipt export failed: %s", strings.TrimSpace(diagnostics.String()))
	}
	return output.Bytes(), nil
}
