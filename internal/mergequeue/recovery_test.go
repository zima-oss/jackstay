package mergequeue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/gh"
	"github.com/zima-oss/jackstay/internal/githubapi"
)

func passingProvider(state *State) *fakeProvider {
	mergeSHA := strings.Repeat("c", 40)
	return &fakeProvider{
		pull: githubapi.PullRequest{Number: state.PullRequest, State: "open", Author: "author",
			HeadSHA: state.Head, BaseSHA: state.Base, Base: "main", MergeCommitSHA: mergeSHA},
		baseHead: state.Base,
		required: []gh.RequiredCheck{{Name: "gate", AppID: 4}},
		checks: []githubapi.Check{{Name: "gate", Kind: githubapi.CheckRun, AppID: 4,
			Status: "COMPLETED", Conclusion: "SUCCESS"}},
		reviews: []Review{{Author: "reviewer", State: "APPROVED", Commit: state.Head}},
		tree:    state.Tree, parents: []string{state.Base, state.Head}, decision: "APPROVED",
	}
}

func TestPRRecoveryKeepsExactAttemptAcrossProviderFailures(t *testing.T) {
	service, repository := fixture(t)
	state := testedHandoff(t, service, 23)
	ctx := context.Background()
	provider := passingProvider(state)
	provider.pullFailureAt, provider.pullFailure = 1, errors.New("HTTP 503")
	service.Provider = provider
	if _, err := service.Handoff(ctx); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("first provider outage was not reported: %v", err)
	}
	if saved, err := service.Status(ctx); err != nil || !saved.HandoffAt.IsZero() {
		t.Fatalf("failed handoff wrote an intent: %+v, %v", saved, err)
	}

	provider = passingProvider(state)
	provider.pullFailureAt, provider.pullFailure = 2, errors.New("HTTP 503")
	service.Provider = provider
	if _, err := service.Handoff(ctx); err == nil {
		t.Fatal("late provider outage handed off unconfirmed evidence")
	}
	if saved, err := service.Status(ctx); err != nil || !saved.HandoffAt.IsZero() {
		t.Fatalf("late failure wrote an intent: %+v, %v", saved, err)
	}

	provider = passingProvider(state)
	provider.targetFailureAt, provider.targetFailure = 2, errors.New("HTTP 503")
	service.Provider = provider
	if _, err := service.Handoff(ctx); err == nil {
		t.Fatal("target ref outage after evidence reads was handed off")
	}
	if saved, err := service.Status(ctx); err != nil || !saved.HandoffAt.IsZero() {
		t.Fatalf("target outage wrote an intent: %+v, %v", saved, err)
	}

	service.Provider = passingProvider(state)
	checked, err := service.Handoff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := service.recordHandoff(ctx, checked)
	if err != nil || recorded.HandoffAt.IsZero() {
		t.Fatalf("recovered handoff not recorded: %+v, %v", recorded, err)
	}
	// Simulate a new process using only the saved queue state and the fetched target ref.
	restarted := Service{Here: service.Here, Project: service.Project, BaseRef: service.BaseRef}
	if resumed, err := restarted.Status(ctx); err != nil || resumed.HandoffAt != recorded.HandoffAt ||
		resumed.Attempt != state.Attempt || resumed.Candidate != state.Candidate {
		t.Fatalf("restart lost the exact attempt: %+v, %v", resumed, err)
	}
	gitCommand(t, repository, "switch", "main")
	gitCommand(t, repository, "merge", "--no-ff", "feature", "-m", "manual merge")
	landed := gitCommand(t, repository, "rev-parse", "main")
	failed := &fakeProvider{pullFailureAt: 1, pullFailure: errors.New("HTTP 503")}
	restarted.Provider = failed
	if _, err := restarted.Reconcile(ctx, landed); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("confirmation outage was hidden: %v", err)
	}
	if pending, err := restarted.Status(ctx); err != nil || pending.Outcome != nil ||
		pending.HandoffAt != recorded.HandoffAt || pending.Candidate != state.Candidate {
		t.Fatalf("failed confirmation changed the attempt: %+v, %v", pending, err)
	}
	restarted.Provider = &fakeProvider{pull: githubapi.PullRequest{Number: 23, State: "closed", Merged: true,
		HeadSHA: state.Head, Base: "main", MergeCommitSHA: landed}}
	result, err := restarted.Reconcile(ctx, landed)
	if err != nil || result.Outcome == nil || !result.Outcome.Matched {
		t.Fatalf("retry did not record the observed landing: %+v, %v", result, err)
	}
	restarted.Provider = failed
	retry, err := restarted.Reconcile(ctx, landed)
	if err != nil || retry.Outcome.Observed != result.Outcome.Observed {
		t.Fatalf("retry overwrote a recorded outcome: %+v, %v", retry, err)
	}
	if _, err := restarted.Reconcile(ctx, state.Candidate); err == nil {
		t.Fatal("different commit replaced a recorded outcome")
	}
}

func TestRecordedHandoffCannotSwitchPullRequestOnLaneReplacement(t *testing.T) {
	service, _ := fixture(t)
	ctx := context.Background()
	old := testedHandoff(t, service, 23)
	if err := service.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	newState := testedHandoff(t, service, 24)
	// A rebuilt lane can have the same candidate object when commit timestamps coincide.
	// Keep the colliding identity explicit so the test does not depend on clock timing.
	place, _, err := service.place(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newState.Candidate = old.Candidate
	if err := save(place, newState); err != nil {
		t.Fatal(err)
	}
	if old.Candidate != newState.Candidate || old.GateRunID != newState.GateRunID || old.Attempt != newState.Attempt {
		t.Fatal("fixture did not reproduce the same candidate and gate identity")
	}
	if _, err := service.recordHandoff(ctx, old); err == nil {
		t.Fatal("checked evidence for the previous PR was recorded for a different PR")
	}
	if saved, err := service.Status(ctx); err != nil || !saved.HandoffAt.IsZero() || saved.PullRequest != 24 {
		t.Fatalf("rejected handoff changed replacement lane: %+v, %v", saved, err)
	}
}
