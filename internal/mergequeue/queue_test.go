package mergequeue

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitCommand(t *testing.T, folder string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = folder
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Queue Test", "GIT_AUTHOR_EMAIL=queue@example.invalid",
		"GIT_COMMITTER_NAME=Queue Test", "GIT_COMMITTER_EMAIL=queue@example.invalid")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func fixture(t *testing.T) (Service, string) {
	t.Helper()
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	repository := t.TempDir()
	gitCommand(t, repository, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "add", "base.txt")
	gitCommand(t, repository, "commit", "-m", "base")
	gitCommand(t, repository, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(repository, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "add", "feature.txt")
	gitCommand(t, repository, "commit", "-m", "feature")
	return Service{Here: repository, Project: "queue-fixture", BaseRef: "refs/heads/main"}, repository
}

func TestCandidateIsPersistentAndDoesNotMoveRefs(t *testing.T) {
	service, repository := fixture(t)
	ctx := context.Background()
	base := gitCommand(t, repository, "rev-parse", "refs/heads/main")
	head := gitCommand(t, repository, "rev-parse", "refs/heads/feature")
	if _, err := service.Enqueue(ctx, "refs/heads/feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enqueue(ctx, "refs/heads/feature"); err == nil {
		t.Fatal("second head entered the single lane")
	}
	candidate, err := service.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Candidate == "" || candidate.Tree != gitCommand(t, repository, "rev-parse", head+"^{tree}") {
		t.Fatalf("wrong candidate: %+v", candidate)
	}
	if got := gitCommand(t, repository, "rev-parse", "refs/heads/main"); got != base {
		t.Fatalf("base moved to %s", got)
	}
	if got := gitCommand(t, repository, "rev-parse", "refs/heads/feature"); got != head {
		t.Fatalf("head moved to %s", got)
	}
	if same, err := service.Prepare(ctx); err != nil || same.Candidate != candidate.Candidate || same.Attempt != 1 {
		t.Fatalf("prepare changed an unchanged attempt: %+v, %v", same, err)
	}
	if _, err := service.Handoff(ctx); err == nil {
		t.Fatal("untested candidate handed off")
	}
	other := service
	other.Here = filepath.Join(repository, "subdir")
	if err := os.Mkdir(other.Here, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := other.Status(ctx); err != nil || got.Candidate != candidate.Candidate {
		t.Fatalf("queue is not shared across worktree folders: %+v, %v", got, err)
	}
}

func TestMovedBaseInvalidatesAndRebuildsCandidate(t *testing.T) {
	service, repository := fixture(t)
	ctx := context.Background()
	if _, err := service.Enqueue(ctx, "refs/heads/feature"); err != nil {
		t.Fatal(err)
	}
	old, err := service.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "switch", "main")
	if err := os.WriteFile(filepath.Join(repository, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "add", "later.txt")
	gitCommand(t, repository, "commit", "-m", "advance base")
	status, err := service.Status(ctx)
	if err != nil || !status.Stale {
		t.Fatalf("advanced base was not stale: %+v, %v", status, err)
	}
	if _, err := service.Handoff(ctx); err == nil {
		t.Fatal("stale candidate handed off")
	}
	newCandidate, err := service.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if newCandidate.Attempt != 2 || newCandidate.Candidate == old.Candidate || newCandidate.Tree == old.Tree {
		t.Fatalf("old candidate survived base advance: old=%+v new=%+v", old, newCandidate)
	}
	if newCandidate.Tree != gitCommand(t, repository, "rev-parse", newCandidate.Candidate+"^{tree}") {
		t.Fatal("stored tree differs from candidate")
	}
	if err := service.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Status(ctx); err == nil {
		t.Fatal("removed lane remains")
	}
}

func TestMovedHeadInvalidatesAndRejectsOldGate(t *testing.T) {
	service, repository := fixture(t)
	ctx := context.Background()
	if _, err := service.Enqueue(ctx, "refs/heads/feature"); err != nil {
		t.Fatal(err)
	}
	old, err := service.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "next.txt"), []byte("next\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "add", "next.txt")
	gitCommand(t, repository, "commit", "-m", "advance head")
	if _, err := service.recordGate(ctx, old.Candidate, "old-gate", []Evidence{{Key: strings.Repeat("a", 64), SHA256: strings.Repeat("b", 64)}}); err == nil {
		t.Fatal("old gate attached to changed head")
	}
	newCandidate, err := service.Prepare(ctx)
	if err != nil || newCandidate.Attempt != 2 || newCandidate.GateRunID != "" || len(newCandidate.Receipts) != 0 {
		t.Fatalf("head move did not clear evidence: %+v, %v", newCandidate, err)
	}
}

func TestConfiguredBaseRefChangeInvalidatesAttempt(t *testing.T) {
	service, repository := fixture(t)
	ctx := context.Background()
	if _, err := service.Enqueue(ctx, "refs/heads/feature"); err != nil {
		t.Fatal(err)
	}
	first, err := service.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "branch", "alternate", "main")
	service.BaseRef = "refs/heads/alternate"
	status, err := service.Status(ctx)
	if err != nil || !status.Stale {
		t.Fatalf("changed base ref was not stale: %+v, %v", status, err)
	}
	if _, err := service.Handoff(ctx); err == nil {
		t.Fatal("old base policy was handed off")
	}
	rebuilt, err := service.Prepare(ctx)
	if err != nil || rebuilt.BaseRef != service.BaseRef || rebuilt.Attempt != first.Attempt+1 || rebuilt.GateRunID != "" {
		t.Fatalf("base policy did not rebuild attempt: %+v, %v", rebuilt, err)
	}
}

func TestOnlyFullBranchRefsAreAllowed(t *testing.T) {
	service, repository := fixture(t)
	for _, ref := range []string{"main", "HEAD", "refs/heads/main^{tree}", gitCommand(t, repository, "rev-parse", "main")} {
		if _, err := service.Enqueue(context.Background(), ref); err == nil {
			t.Fatalf("accepted non-branch head %q", ref)
		}
	}
}

func TestCommandRunsCountedGateAndRefusesStaleHandoff(t *testing.T) {
	service, repository := fixture(t)
	config := `{"jackstay_schema":2,"project":"queue-fixture","queue":{"enabled":true,"base_ref":"refs/heads/main"},"receipts":{"inputs":["."],"toolchain":[]},"test":{"scopes":{"gate":{"steps":["check"]}},"steps":{"check":{"run":["sh","-c","echo ran 1 tests"],"judge":"steps","evidence":"ran ([0-9]+) tests"}}}}`
	gitCommand(t, repository, "switch", "main")
	if err := os.WriteFile(filepath.Join(repository, "jackstay.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "add", "jackstay.json")
	gitCommand(t, repository, "commit", "-m", "configure project")
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repository); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	var out, diagnostics bytes.Buffer
	if status := addCommand([]string{"--head", "refs/heads/feature"}, nil, &out, &diagnostics); status != 0 {
		t.Fatalf("queue add exit %d: %s", status, diagnostics.String())
	}
	out.Reset()
	diagnostics.Reset()
	if status := testCommand(nil, nil, &out, &diagnostics); status != 0 {
		t.Fatalf("queue test exit %d: %s\n%s", status, diagnostics.String(), out.String())
	}
	state, err := service.Handoff(context.Background())
	if err != nil || len(state.Receipts) != 1 {
		t.Fatalf("no counted gate handoff: %+v, %v", state, err)
	}
	out.Reset()
	diagnostics.Reset()
	if status := handoffCommand(nil, nil, &out, &diagnostics); status != 0 || !strings.Contains(out.String(), "manual handoff only") {
		t.Fatalf("handoff exit %d: %s %s", status, out.String(), diagnostics.String())
	}
	if recorded, err := service.Status(context.Background()); err != nil || recorded.HandoffAt.IsZero() {
		t.Fatalf("command did not persist a handoff: %+v, %v", recorded, err)
	}
	gitCommand(t, repository, "switch", "feature")
	if err := os.WriteFile(filepath.Join(repository, "next.txt"), []byte("next\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "add", "next.txt")
	gitCommand(t, repository, "commit", "-m", "advance feature")
	if status := handoffCommand(nil, nil, &out, &diagnostics); status == 0 {
		t.Fatal("handoff ignored moved head")
	}
	gitCommand(t, repository, "switch", "main")
	gitCommand(t, repository, "merge", "--no-ff", "feature", "-m", "manual landing")
	landed := gitCommand(t, repository, "rev-parse", "main")
	diagnostics.Reset()
	if status := reconcileCommand([]string{"--commit", landed}, nil, &out, &diagnostics); status != 1 || !strings.Contains(diagnostics.String(), "outcome recorded") {
		t.Fatalf("changed landing was not recorded as a mismatch: exit=%d %s", status, diagnostics.String())
	}
	if recorded, err := service.Status(context.Background()); err != nil || recorded.Outcome == nil || recorded.Outcome.Matched {
		t.Fatalf("command lost landing mismatch: %+v, %v", recorded, err)
	}
}
