package mergequeue

import (
	"context"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/gh"
	"github.com/zima-oss/jackstay/internal/githubapi"
)

type fakeProvider struct {
	pull            githubapi.PullRequest
	pullSecond      *githubapi.PullRequest
	baseHead        string
	baseSecond      string
	baseReads       int
	required        []gh.RequiredCheck
	requiredSecond  []gh.RequiredCheck
	requiredReads   int
	checks          []githubapi.Check
	reviews         []Review
	tree            string
	parents         []string
	decision        string
	read            int
	pullFailureAt   int
	pullFailure     error
	targetFailureAt int
	targetFailure   error
}

func (fake *fakeProvider) PullRequest(_ context.Context, _ int) (githubapi.PullRequest, error) {
	fake.read++
	if fake.pullFailure != nil && fake.read == fake.pullFailureAt {
		return githubapi.PullRequest{}, fake.pullFailure
	}
	if fake.pullSecond != nil && fake.read == 2 {
		return *fake.pullSecond, nil
	}
	return fake.pull, nil
}
func (fake *fakeProvider) TargetHead(context.Context, string) (string, error) {
	fake.baseReads++
	if fake.targetFailure != nil && fake.baseReads == fake.targetFailureAt {
		return "", fake.targetFailure
	}
	if fake.baseSecond != "" && fake.baseReads == 2 {
		return fake.baseSecond, nil
	}
	return fake.baseHead, nil
}
func (fake *fakeProvider) RequiredChecks(context.Context, string) ([]gh.RequiredCheck, error) {
	fake.requiredReads++
	if fake.requiredSecond != nil && fake.requiredReads == 2 {
		return fake.requiredSecond, nil
	}
	return fake.required, nil
}
func (fake *fakeProvider) Checks(context.Context, string) ([]githubapi.Check, error) {
	return fake.checks, nil
}
func (fake *fakeProvider) MergeCommit(context.Context, string) (MergeCommit, error) {
	return MergeCommit{Tree: fake.tree, Parents: fake.parents}, nil
}
func (fake *fakeProvider) Reviews(context.Context, int) ([]Review, error) { return fake.reviews, nil }
func (fake *fakeProvider) ReviewDecision(context.Context, int) (string, error) {
	return fake.decision, nil
}

func TestPRHandoffRequiresCurrentProviderEvidence(t *testing.T) {
	service, _ := fixture(t)
	ctx := context.Background()
	if _, err := service.Enqueue(ctx, "refs/heads/feature", 17); err != nil {
		t.Fatal(err)
	}
	state, err := service.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.recordGate(ctx, state.Candidate, "gate-17", []Evidence{{Key: strings.Repeat("a", 64), SHA256: strings.Repeat("b", 64)}}); err != nil {
		t.Fatal(err)
	}
	mergeSHA := strings.Repeat("c", 40)
	fake := &fakeProvider{
		pull: githubapi.PullRequest{Number: 17, State: "open", Author: "author", HeadSHA: state.Head,
			BaseSHA: mergeSHA, Base: "main", MergeCommitSHA: mergeSHA},
		baseHead: state.Base,
		required: []gh.RequiredCheck{{Name: "gate", AppID: 4}},
		checks: []githubapi.Check{{Name: "gate", Kind: githubapi.CheckRun, AppID: 4,
			Status: "COMPLETED", Conclusion: "SUCCESS"}},
		reviews: []Review{{Author: "reviewer", State: "APPROVED", Commit: state.Head},
			{Author: "reviewer", State: "COMMENTED", Commit: state.Head}},
		tree: state.Tree, parents: []string{state.Base, state.Head}, decision: "APPROVED",
	}
	service.Provider = fake
	if _, err := service.Handoff(ctx); err != nil || fake.read != 2 {
		t.Fatalf("current PR evidence rejected: %v (reads %d)", err, fake.read)
	}
	cases := []struct {
		name   string
		change func()
		undo   func()
	}{
		{"head moved", func() { fake.pull.HeadSHA = mergeSHA }, func() { fake.pull.HeadSHA = state.Head }},
		{"base moved", func() { fake.baseHead = mergeSHA }, func() { fake.baseHead = state.Base }},
		{"test merge changed", func() { fake.tree = mergeSHA }, func() { fake.tree = state.Tree }},
		{"same tree wrong base parent", func() { fake.parents[0] = mergeSHA }, func() { fake.parents[0] = state.Base }},
		{"same tree wrong head parent", func() { fake.parents[1] = mergeSHA }, func() { fake.parents[1] = state.Head }},
		{"check pending", func() { fake.checks[0].Status = "IN_PROGRESS" }, func() { fake.checks[0].Status = "COMPLETED" }},
		{"check source changed", func() { fake.checks[0].AppID = 5 }, func() { fake.checks[0].AppID = 4 }},
		{"same-name pending run cannot borrow a success", func() {
			fake.checks = append(fake.checks, githubapi.Check{Name: "gate", Kind: githubapi.CheckRun, AppID: 4, Status: "IN_PROGRESS"})
		}, func() { fake.checks = fake.checks[:1] }},
		{"review stale", func() { fake.reviews[0].Commit = mergeSHA }, func() { fake.reviews[0].Commit = state.Head }},
		{"more approvals required", func() { fake.decision = "REVIEW_REQUIRED" }, func() { fake.decision = "APPROVED" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			test.change()
			defer test.undo()
			if _, err := service.Handoff(ctx); err == nil {
				t.Fatal("stale provider evidence was handed off")
			}
		})
	}
	fake.baseReads = 0
	fake.baseSecond = mergeSHA
	if _, err := service.Handoff(ctx); err == nil {
		t.Fatal("target branch movement during verification was handed off")
	}
	fake.baseSecond = ""
	fake.read = 0
	draft := fake.pull
	draft.Draft = true
	fake.pullSecond = &draft
	if _, err := service.Handoff(ctx); err == nil {
		t.Fatal("PR that became draft during verification was handed off")
	}
	fake.pullSecond = nil
	fake.requiredReads = 0
	fake.requiredSecond = []gh.RequiredCheck{{Name: "new-gate", AppID: 4}}
	if _, err := service.Handoff(ctx); err == nil {
		t.Fatal("changed required-check policy was handed off")
	}
}

func TestLocalOnlyAndRequiredPRModes(t *testing.T) {
	service, _ := fixture(t)
	service.RequirePullRequest = true
	if _, err := service.Enqueue(context.Background(), "refs/heads/feature"); err == nil {
		t.Fatal("required PR missing")
	}
	service.RequirePullRequest = false
	if _, err := service.Enqueue(context.Background(), "refs/heads/feature"); err != nil {
		t.Fatal(err)
	}
	state, err := service.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.recordGate(context.Background(), state.Candidate, "local-gate", []Evidence{{Key: strings.Repeat("a", 64), SHA256: strings.Repeat("b", 64)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Handoff(context.Background()); err != nil {
		t.Fatal(err)
	}
	service.RequirePullRequest = true
	if _, err := service.Handoff(context.Background()); err == nil {
		t.Fatalf("required PR did not block local handoff: %v", err)
	}
}
