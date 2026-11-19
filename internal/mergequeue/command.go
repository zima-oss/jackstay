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
	"strings"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/receipts"
	scopes "github.com/zima-oss/jackstay/internal/testing"
)

const queueHelp = `usage: jst queue add --head REF
       jst queue prepare | test | status | handoff | remove

The queue is enabled by queue.enabled and queue.base_ref in jackstay.json. This first local lane
holds one head. prepare writes an unreachable Git candidate commit; test runs the project gate on
a clean copy and requires counted portable receipts; handoff checks freshness and prints the
candidate and evidence for a manual merge. It never changes a Git ref or calls GitHub.

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
	return Service{Here: here, Project: config.ProjectName(root, loaded, here), BaseRef: base}, true
}

func addCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "--head" {
		fmt.Fprint(stderr, queueHelp)
		return 2
	}
	service, ok := commandService(stderr)
	if !ok {
		return 2
	}
	state, err := service.Enqueue(context.Background(), args[1])
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
	state, err := service.Prepare(context.Background())
	if err != nil {
		return queueError(stderr, err)
	}
	paths := receipts.RunLogPaths()
	before := len(receipts.ReadRecords(paths))
	if status := scopes.Command([]string{"gate", "--commit", state.Candidate}, nil, stdout, stderr); status != 0 {
		return queueError(stderr, fmt.Errorf("gate did not pass (exit %d)", status))
	}
	records := receipts.ReadRecords(paths)
	if len(records) <= before {
		return queueError(stderr, fmt.Errorf("the gate wrote no durable run record"))
	}
	var gateID string
	for _, record := range records[before:] {
		if record.Text("event") == receipts.EventGate && record.Text("project") == service.Project &&
			record.Text("outcome") == receipts.OutcomePassed && record.Text("head") == state.Candidate {
			gateID = record.Text("gate_run_id")
		}
	}
	if gateID == "" {
		return queueError(stderr, fmt.Errorf("no passed gate record for candidate %s", state.Candidate))
	}
	var evidence []Evidence
	for _, record := range records[before:] {
		if record.Text("event") != receipts.EventRun || record.Text("project") != service.Project ||
			record.Text("gate_run_id") != gateID || record.Text("head") != state.Candidate ||
			record.Text("outcome") != receipts.OutcomePassed {
			continue
		}
		count, ok := record.Number("evidence_count")
		key := record.Text("key")
		if !ok || count <= 0 || !objectID(key) {
			continue
		}
		data, err := exportReceipt(key)
		if err != nil || !validEvidence(data, state.Candidate, key) {
			continue
		}
		evidence = append(evidence, Evidence{Key: key, SHA256: digest(data)})
	}
	if len(evidence) == 0 {
		return queueError(stderr, fmt.Errorf("gate passed but no counted receipt for candidate %s was exportable", state.Candidate))
	}
	if _, err := service.recordGate(context.Background(), state.Candidate, gateID, evidence); err != nil {
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
	status := "prepared"
	if state.Candidate == "" {
		status = "queued"
	}
	if state.GateRunID != "" {
		status = "tested"
	}
	if state.Stale {
		status = "stale"
	}
	fmt.Fprintf(stdout, "%s: %s %s base=%s head=%s candidate=%s attempt=%d receipts=%d\n",
		status, state.HeadRef, state.BaseRef, state.Base, state.Head, state.Candidate, state.Attempt, len(state.Receipts))
	return 0
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
	state, err := service.Handoff(context.Background())
	if err != nil {
		return queueError(stderr, err)
	}
	for _, item := range state.Receipts {
		data, err := exportReceipt(item.Key)
		if err != nil || digest(data) != item.SHA256 || !validEvidence(data, state.Candidate, item.Key) {
			return queueError(stderr, fmt.Errorf("receipt %s is no longer verifiable", item.Key))
		}
	}
	if _, err := service.Handoff(context.Background()); err != nil {
		return queueError(stderr, err)
	}
	fmt.Fprintf(stdout, "manual handoff only: base %s=%s, head %s=%s\n", state.BaseRef, state.Base, state.HeadRef, state.Head)
	fmt.Fprintf(stdout, "tested candidate %s tree %s gate %s\n", state.Candidate, state.Tree, state.GateRunID)
	for _, item := range state.Receipts {
		fmt.Fprintf(stdout, "counted receipt %s sha256 %s\n", item.Key, item.SHA256)
	}
	fmt.Fprintln(stdout, "Recheck the live base/head and compare the landed tree after a human merge; this handoff does not prove the provider's atomic merge decision.")
	return 0
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
