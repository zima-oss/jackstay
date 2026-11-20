package workspaceboard

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/sessionchat"
)

func repository(t *testing.T, parent, name string) string {
	t.Helper()
	root := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(root, "cards"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `{"jackstay_schema":3,"project":"shared","cards":{"dir":"cards","id":"T-[0-9]+","unit":"pr","since":"2026-01-01"},"chat":{"enabled":true}}`
	write(t, filepath.Join(root, "jackstay.json"), settings)
	write(t, filepath.Join(root, "cards", "T-1-ready.md"), "---\n{\"state\":\"ready\"}\n---\nPrivate card body\n")
	write(t, filepath.Join(root, "cards", "T-2-done.md"), "---\n{\"state\":\"done\"}\n---\n")
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "add", ".")
	git(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial")
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func write(t *testing.T, name, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBoardKeepsRepositoriesSeparateAndDoesNotWrite(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state")
	t.Setenv("JACKSTAY_HOME", home)
	parent := t.TempDir()
	first := repository(t, parent, "first")
	second := repository(t, parent, "second")
	board := Read(context.Background(), []string{first, second})
	if board.Partial || board.Schema != 1 || len(board.Repositories) != 2 {
		t.Fatalf("board: %+v", board)
	}
	for _, row := range board.Repositories {
		if row.Cards.Status != "ok" || len(row.Cards.Active) != 1 || row.Cards.Active[0].ID != "T-1" ||
			row.Gate.Status != "none" || row.Receipt.Status != "none" ||
			row.Chat.Status != "available" || row.Chat.Sessions != 0 {
			t.Fatalf("row: %+v", row)
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("read-only board created state home: %v", err)
	}
	common, err := (gitcli.Git{Dir: first}).CommonDir(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store, err := sessionchat.StoreFor(common, config.ChatSettings(map[string]any{"enabled": true}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register("private agent", ""); err != nil {
		t.Fatal(err)
	}
	board = Read(context.Background(), []string{first, second})
	if board.Repositories[0].Chat.Sessions != 1 || board.Repositories[1].Chat.Sessions != 0 {
		t.Fatalf("chat leaked across independent repositories: %+v", board.Repositories)
	}
}

func TestConfiguredMissingCardsDirectoryIsEmpty(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	root := repository(t, t.TempDir(), "cards-missing")
	if err := os.RemoveAll(filepath.Join(root, "cards")); err != nil {
		t.Fatal(err)
	}
	board := Read(context.Background(), []string{root})
	if board.Partial || len(board.Repositories) != 1 || board.Repositories[0].Cards.Status != "none" {
		t.Fatalf("missing cards source: %+v", board)
	}
}

func TestBoardSharesChatAcrossLinkedWorktrees(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	parent := t.TempDir()
	root := repository(t, parent, "main")
	linked := filepath.Join(parent, "linked")
	git(t, root, "worktree", "add", "-qb", "feature", linked)
	write(t, filepath.Join(linked, "cards", "T-3-live.md"), "---\n{\"state\":\"in_progress\"}\n---\n")
	write(t, filepath.Join(linked, "feature.txt"), "feature")
	git(t, linked, "add", "feature.txt")
	git(t, linked, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "feature")

	common, err := (gitcli.Git{Dir: root}).CommonDir(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store, err := sessionchat.StoreFor(common, config.ChatSettings(map[string]any{"enabled": true}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register("private agent", ""); err != nil {
		t.Fatal(err)
	}
	head, err := (gitcli.Git{Dir: root}).Line(context.Background(), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, ".all", "runs.jsonl"),
		`{"event":"gate","project":"shared","worktree":"`+root+`","head":"`+head+`","epoch":100,"at":"2026-11-19T10:00:00Z","outcome":"passed"}`+"\n")

	board := Read(context.Background(), []string{root, linked})
	if board.Partial || len(board.Repositories) != 2 {
		t.Fatalf("board: %+v", board)
	}
	mainRow, linkedRow := board.Repositories[0], board.Repositories[1]
	if mainRow.Chat.Sessions != 1 || linkedRow.Chat.Sessions != 1 {
		t.Fatalf("shared sources: %+v %+v", mainRow, linkedRow)
	}
	if len(mainRow.Cards.Active) != 1 || len(linkedRow.Cards.Active) != 2 ||
		mainRow.Gate.Status != "recorded" || linkedRow.Gate.Status != "none" {
		t.Fatalf("checkout sources: %+v %+v", mainRow, linkedRow)
	}
}

func TestBoardReportsPartialRowsAndMalformedLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	root := repository(t, t.TempDir(), "valid")
	write(t, filepath.Join(home, ".all", "runs.jsonl"), "{not-json}\n")
	missing := filepath.Join(t.TempDir(), "missing")
	board := Read(context.Background(), []string{missing, root})
	if !board.Partial || board.Repositories[0].Status != "unknown" || board.Repositories[1].Status != "ok" ||
		board.Repositories[1].Gate.Status != "unknown" || board.Repositories[1].Receipt.Status != "unknown" {
		t.Fatalf("partial board: %+v", board)
	}
	var output, diagnostics bytes.Buffer
	if code := runBoard([]string{"--json", missing, root}, nil, &output, &diagnostics); code != 1 {
		t.Fatalf("exit %d: %s", code, diagnostics.String())
	}
	var decoded Board
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || len(decoded.Repositories) != 2 {
		t.Fatalf("JSON: %v: %s", err, output.String())
	}
	if strings.Contains(output.String(), "Private card body") || strings.Contains(output.String(), "private agent") {
		t.Fatal("private content appeared in JSON")
	}
}

func TestBoardCommandIsReadOnlyAndRequiresPaths(t *testing.T) {
	entry, _, found := cli.Find([]string{"workspace", "board"})
	if !found || !entry.SkipTelemetry || !entry.PrivateArgs {
		t.Fatalf("workspace board telemetry settings: %+v", entry)
	}
	var output, diagnostics bytes.Buffer
	if code := runBoard([]string{"--json"}, nil, &output, &diagnostics); code != 2 {
		t.Fatalf("missing path exit %d", code)
	}
	if code := runBoard([]string{"--bad", "x"}, nil, &output, &diagnostics); code != 2 {
		t.Fatalf("unknown flag exit %d", code)
	}
}

func TestBoardMarksEvidenceStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	root := repository(t, t.TempDir(), "repo")
	gitReader := gitcli.Git{Dir: root}
	head, err := gitReader.Line(context.Background(), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	records := []map[string]any{
		{"event": "gate", "project": "shared", "worktree": root, "head": head, "epoch": 100.0,
			"at": "2026-11-19T10:00:00Z", "outcome": "passed"},
		{"event": "run", "project": "shared", "worktree": root, "head": head, "epoch": 101.0,
			"at": "2026-11-19T10:01:00Z", "outcome": "passed", "stored": true},
	}
	var log bytes.Buffer
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		log.Write(encoded)
		log.WriteByte('\n')
	}
	write(t, filepath.Join(home, ".all", "runs.jsonl"), log.String())
	git(t, root, "checkout", "-qb", "feature")
	write(t, filepath.Join(root, "feature.txt"), "feature")
	git(t, root, "add", "feature.txt")
	git(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "feature")
	board := Read(context.Background(), []string{root})
	row := board.Repositories[0]
	if board.Partial || row.Gate.Freshness != "stale" || row.Receipt.Freshness != "stale" {
		t.Fatalf("old evidence: %+v", row)
	}
}

func TestBoardRejectsFIFOInputsWithoutWaitingForWriter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	root := repository(t, t.TempDir(), "repo")
	card := filepath.Join(root, "cards", "T-1-ready.md")
	if err := os.Remove(card); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(card, 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(home, ".all", "runs.jsonl")
	if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(log, 0o600); err != nil {
		t.Fatal(err)
	}
	board := Read(context.Background(), []string{root})
	if !board.Partial || board.Repositories[0].Cards.Status != "unknown" ||
		board.Repositories[0].Gate.Status != "unknown" || board.Repositories[0].Receipt.Status != "unknown" {
		t.Fatalf("FIFO sources: %+v", board)
	}
}
