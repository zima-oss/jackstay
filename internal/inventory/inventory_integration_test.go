//go:build integration

package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

func TestInventorySeparatesReposAndFollowsWorktreeAndRunnerEvidence(t *testing.T) {
	const settings = `{"jackstay_schema":2,"project":"demo"}`
	repo := testrepo.New(t, map[string]string{"jackstay.json": settings})
	linked := filepath.Join(filepath.Dir(repo.Root), "linked")
	repo.Git("worktree", "add", "-q", "-b", "linked", linked)

	// An independent Git repository under the primary checkout uses the same display name,
	// shares its journal folder, and has paths whose leading components are the same.
	other := filepath.Join(repo.Root, "independent")
	repo.Git("init", "-q", "-b", "main", other)
	if err := os.WriteFile(filepath.Join(other, "jackstay.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	repo.GitIn(other, "add", ".")
	repo.GitIn(other, "commit", "-qm", "independent")

	folder := filepath.Join(repo.Home, homedir.MarkerName("demo"), telemetry.FolderName)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var log bytes.Buffer
	appendSpan(&log, now, 1, 1, 0, map[string]string{
		telemetry.AttrCommand: "guard", telemetry.AttrCwd: repo.Root, telemetry.AttrClaudeSession: "claude-id",
		telemetry.AttrClaudeTranscript: "/example/.claude/projects/demo/a.jsonl", telemetry.AttrGuardOutcome: "allowed",
	})
	appendSpan(&log, now, 2, 1, 0, map[string]string{telemetry.AttrCommand: "review", telemetry.AttrWorktree: linked})
	appendSpan(&log, now, 2, 2, 1, map[string]string{telemetry.AttrCodexThread: "codex-id"})
	appendSpan(&log, now, 3, 1, 0, map[string]string{telemetry.AttrCommand: "test", telemetry.AttrCwd: linked})
	appendSpan(&log, now, 4, 1, 0, map[string]string{telemetry.AttrCommand: "test", telemetry.AttrCwd: other,
		telemetry.AttrClaudeSession: "other-id"})
	appendSpan(&log, now, 1, 3, 1, map[string]string{telemetry.AttrCodexThread: "other-child",
		telemetry.AttrWorktree: other})
	appendSpan(&log, now, 5, 1, 0, map[string]string{telemetry.AttrCommand: "guard", telemetry.AttrCwd: linked,
		telemetry.AttrClaudeSession: "unknown-hook", telemetry.AttrGuardOutcome: "failed_open"})
	if err := os.WriteFile(filepath.Join(folder, journal.FileName(now.Format(time.DateOnly))), log.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	main := invokeInventory(t)
	t.Chdir(linked)
	copied := invokeInventory(t)
	t.Chdir(other)
	independent := invokeInventory(t)
	if main.Schema != 1 || main.Project != "demo" || main.RepositoryID != copied.RepositoryID ||
		independent.RepositoryID == main.RepositoryID || copied.Current != linked || len(main.Worktrees) != 2 ||
		main.Worktrees[0].ID == main.Worktrees[1].ID {
		t.Fatalf("main %+v\nlinked %+v\nindependent %+v", main, copied, independent)
	}
	if len(independent.Sessions) != 2 || !hasSession(independent, "claude-code", "other-id", "unknown") ||
		!hasSession(main, "claude-code", "claude-id", "observed") || !hasSession(main, "codex", "codex-id", "unknown") ||
		!hasSession(main, "unknown", "", "unknown") || !hasSession(main, "unknown", "unknown-hook", "unknown") ||
		slices.ContainsFunc(main.Sessions, func(item session) bool { return strings.HasPrefix(item.ID, "other") }) {
		t.Fatalf("main sessions: %+v\nindependent sessions: %+v", main.Sessions, independent.Sessions)
	}
}

func TestWorktreeIDRejectsAnIndependentRepositoryAtAReusedPath(t *testing.T) {
	repo := testrepo.New(t, nil)
	linked := filepath.Join(filepath.Dir(repo.Root), "linked")
	repo.Git("worktree", "add", "-q", "-b", "linked", linked)
	common := filepath.Join(repo.Root, ".git")
	if id := worktreeID(context.Background(), linked, common); id == "" {
		t.Fatal("the original linked checkout has no identity")
	}
	if err := os.Rename(linked, linked+"-original"); err != nil {
		t.Fatal(err)
	}
	repo.Git("init", "-q", "-b", "main", linked)
	if id := worktreeID(context.Background(), linked, common); id != "" {
		t.Fatalf("reused path was treated as a linked worktree: %s", id)
	}
}

func appendSpan(log *bytes.Buffer, now time.Time, traceID, spanID, parentID int, attributes map[string]string) {
	fields := []any{}
	for key, value := range attributes {
		fields = append(fields, map[string]any{"key": key, "value": map[string]string{"stringValue": value}})
	}
	parent := ""
	if parentID > 0 {
		parent = fmt.Sprintf(",\"parentSpanId\":\"%016x\"", parentID)
	}
	attributesJSON, _ := json.Marshal(fields)
	fmt.Fprintf(log, `{"resourceSpans":[{"scopeSpans":[{"scope":{"name":"jackstay"},"spans":[`+
		`{"traceId":"%032x","spanId":"%016x"%s,"name":"jst test","startTimeUnixNano":"%d","endTimeUnixNano":"%d","attributes":%s}]}]}]}`+"\n",
		traceID, spanID, parent, now.Add(-time.Second).UnixNano(), now.UnixNano(), attributesJSON)
}

func invokeInventory(t *testing.T) report {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(nil, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
	var result report
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func hasSession(found report, runner, id, guard string) bool {
	return slices.ContainsFunc(found.Sessions, func(item session) bool {
		return item.Runner == runner && item.ID == id && item.Guard == guard
	})
}

func TestInventoryRefusesMalformedFlags(t *testing.T) {
	if code := run([]string{"--since"}, nil, io.Discard, io.Discard); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
