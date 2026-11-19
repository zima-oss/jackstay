package mergequeue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/githubapi"
)

func testedHandoff(t *testing.T, service Service, pullRequest ...int) *State {
	t.Helper()
	ctx := context.Background()
	if _, err := service.Enqueue(ctx, "refs/heads/feature", pullRequest...); err != nil {
		t.Fatal(err)
	}
	state, err := service.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state, err = service.recordGate(ctx, state.Candidate, "counted-gate", []Evidence{{Key: strings.Repeat("a", 64), SHA256: strings.Repeat("b", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestLocalLandingReconciliationPersistsAndRejectsReplacement(t *testing.T) {
	service, repository := fixture(t)
	state := testedHandoff(t, service)
	ctx := context.Background()
	if _, err := service.Reconcile(ctx, state.Candidate); err == nil {
		t.Fatal("reconciliation accepted a candidate without a recorded handoff")
	}
	checked, err := service.Handoff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.recordHandoff(ctx, checked); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Prepare(ctx); err == nil {
		t.Fatal("prepare replaced an unresolved manual handoff")
	}
	gitCommand(t, repository, "switch", "main")
	gitCommand(t, repository, "merge", "--no-ff", "feature", "-m", "manual merge")
	landed := gitCommand(t, repository, "rev-parse", "main")
	first, err := service.Reconcile(ctx, landed)
	if err != nil || first.Outcome == nil || !first.Outcome.Matched || first.Outcome.Tree != state.Tree {
		t.Fatalf("matching landing was not persisted: %+v, %v", first, err)
	}
	repeated, err := service.Reconcile(ctx, landed)
	if err != nil || repeated.Outcome.Observed != first.Outcome.Observed {
		t.Fatalf("retry altered the observed outcome: %+v, %v", repeated, err)
	}
	if _, err := service.Prepare(ctx); err == nil {
		t.Fatal("prepare replaced the observed outcome")
	}
	if err := os.WriteFile(filepath.Join(repository, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "add", "later.txt")
	gitCommand(t, repository, "commit", "-m", "later")
	if _, err := service.Reconcile(ctx, gitCommand(t, repository, "rev-parse", "main")); err == nil {
		t.Fatal("a later commit replaced the recorded landing")
	}
	loaded, err := service.Status(ctx)
	if err != nil || loaded.Outcome.Commit != landed {
		t.Fatalf("restart lost outcome: %+v, %v", loaded, err)
	}
}

func TestLocalLandingMismatchIsDurable(t *testing.T) {
	service, repository := fixture(t)
	_ = testedHandoff(t, service)
	ctx := context.Background()
	checked, err := service.Handoff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.recordHandoff(ctx, checked); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "switch", "main")
	if err := os.WriteFile(filepath.Join(repository, "unrelated.txt"), []byte("different\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "add", "unrelated.txt")
	gitCommand(t, repository, "commit", "-m", "different landing")
	landed := gitCommand(t, repository, "rev-parse", "main")
	result, err := service.Reconcile(ctx, landed)
	if err != nil || result.Outcome == nil || result.Outcome.Matched {
		t.Fatalf("mismatch was not persisted: %+v, %v", result, err)
	}
	if _, err := service.Reconcile(ctx, checked.Candidate); err == nil {
		t.Fatal("uncertain outcome was replaced")
	}
}

func TestPRLandingMayBeReconciledAfterBaseAdvances(t *testing.T) {
	service, repository := fixture(t)
	service.Provider = &fakeProvider{}
	state := testedHandoff(t, service, 19)
	ctx := context.Background()
	// Exercise reconciliation with a pinned attempt after the handoff marker.
	if _, err := service.recordHandoff(ctx, state); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "switch", "main")
	gitCommand(t, repository, "merge", "--no-ff", "feature", "-m", "manual merge")
	landed := gitCommand(t, repository, "rev-parse", "main")
	if err := os.WriteFile(filepath.Join(repository, "next.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repository, "add", "next.txt")
	gitCommand(t, repository, "commit", "-m", "later")
	if _, err := service.Reconcile(ctx, landed); err == nil {
		t.Fatal("unconfirmed PR merge was recorded")
	}
	service.Provider = &fakeProvider{pull: githubapi.PullRequest{Number: 19, State: "closed", Merged: true,
		HeadSHA: state.Head, Base: "main", MergeCommitSHA: landed}}
	result, err := service.Reconcile(ctx, landed)
	if err != nil || result.Outcome == nil || !result.Outcome.Matched {
		t.Fatalf("reachable PR merge was not reconciled: %+v, %v", result, err)
	}
	gitCommand(t, repository, "branch", "-D", "feature")
	if recorded, err := service.Status(ctx); err != nil || recorded.Outcome.Commit != landed {
		t.Fatalf("deleted PR head hid the durable outcome: %+v, %v", recorded, err)
	}
}
