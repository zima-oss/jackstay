// Package mergequeue keeps a single local candidate for manual landing. It does not write refs
// or ask a hosting provider to merge: an operator must make and verify the final decision.
package mergequeue

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/delivery"
	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

const stateSchema = 1

// Evidence identifies one exported, counted receipt of the exact candidate commit.
type Evidence struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}

// Outcome records one observed manual landing against the tested candidate.
type Outcome struct {
	Attempt  int       `json:"attempt"`
	Commit   string    `json:"commit"`
	Tree     string    `json:"tree"`
	Matched  bool      `json:"matched"`
	Observed time.Time `json:"observed"`
}

// State is a local single-head queue. Attempts increment whenever inputs change.
type State struct {
	Schema      int        `json:"schema"`
	Repository  string     `json:"repository"`
	BaseRef     string     `json:"base_ref"`
	HeadRef     string     `json:"head_ref"`
	PullRequest int        `json:"pull_request,omitempty"`
	Base        string     `json:"base"`
	Head        string     `json:"head"`
	Candidate   string     `json:"candidate,omitempty"`
	Tree        string     `json:"tree,omitempty"`
	Attempt     int        `json:"attempt"`
	GateClaim   string     `json:"gate_claim,omitempty"`
	GateRunID   string     `json:"gate_run_id,omitempty"`
	Receipts    []Evidence `json:"receipts,omitempty"`
	HandoffAt   time.Time  `json:"handoff_at,omitempty"`
	Outcome     *Outcome   `json:"outcome,omitempty"`
	Updated     time.Time  `json:"updated"`
	Stale       bool       `json:"-"`
}

// Service locates local queue state by project and Git common directory. BaseRef must name a
// moving ref; supplying an immutable SHA would make freshness checks meaningless.
type Service struct {
	Here               string
	Project            string
	BaseRef            string
	RequirePullRequest bool
	ReviewTask         string
	Provider           Provider
}

func (service Service) git() gitcli.Git { return gitcli.Git{Dir: service.Here} }

func (service Service) place(ctx context.Context) (string, string, error) {
	if service.Project == "" {
		return "", "", errors.New("the queue needs a project name")
	}
	common, err := service.git().CommonDir(ctx)
	if err != nil {
		return "", "", err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(common))
	folder, err := homedir.ProjectPath(service.Project, "merge-queue", hex.EncodeToString(sum[:12]))
	return filepath.Join(folder, "state.json"), common, err
}

func (service Service) withLock(ctx context.Context, action func(string, string) (*State, error)) (*State, error) {
	place, common, err := service.place(ctx)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(place), 0o700); err != nil {
		return nil, err
	}
	lock, err := durable.Lock(ctx, place)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	return action(place, common)
}

func read(place string) (*State, error) {
	data, err := durable.ReadLimited(place, delivery.Limit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("queue state cannot be read: %w", err)
	}
	var state State
	if err := strictjson.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Schema != stateSchema || state.Repository == "" || state.BaseRef == "" || state.HeadRef == "" ||
		state.Base == "" || state.Head == "" || state.PullRequest < 0 || state.Attempt < 1 || state.Updated.IsZero() {
		return nil, errors.New("queue state is incomplete or has an unsupported schema")
	}
	if state.GateClaim != "" {
		if decoded, err := hex.DecodeString(state.GateClaim); err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != state.GateClaim {
			return nil, errors.New("queue test claim is invalid")
		}
	}
	if state.Outcome != nil && (state.HandoffAt.IsZero() || state.Outcome.Attempt != state.Attempt ||
		!objectID(state.Outcome.Commit) || !objectID(state.Outcome.Tree) || state.Outcome.Observed.IsZero() ||
		state.Outcome.Matched != (state.Outcome.Tree == state.Tree)) {
		return nil, errors.New("queue outcome is incomplete or inconsistent")
	}
	return &state, nil
}

func save(place string, state *State) error {
	state.Updated = time.Now().UTC()
	data, err := json.Marshal(state)
	if err != nil || len(data) >= delivery.Limit {
		return errors.New("queue state exceeds its size limit")
	}
	if err := os.MkdirAll(filepath.Dir(place), 0o700); err != nil {
		return err
	}
	return durable.WriteAtomic(place, append(data, '\n'), 0o600, 0)
}

func (service Service) resolve(ctx context.Context, ref string) (string, error) {
	if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/remotes/") ||
		strings.ContainsAny(ref, " \t\r\n") {
		return "", fmt.Errorf("%q must be a full local or remote-tracking branch ref", ref)
	}
	if _, err := service.git().Run(ctx, "check-ref-format", ref); err != nil {
		return "", fmt.Errorf("invalid branch ref %q: %w", ref, err)
	}
	if _, err := service.git().Run(ctx, "show-ref", "--verify", ref); err != nil {
		return "", fmt.Errorf("branch ref %q does not exist: %w", ref, err)
	}
	name, err := service.git().Line(ctx, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s: %w", ref, err)
	}
	return strings.TrimSpace(name), nil
}

func (service Service) fresh(ctx context.Context, state *State) (bool, error) {
	if service.BaseRef != state.BaseRef {
		return false, nil
	}
	base, err := service.resolve(ctx, state.BaseRef)
	if err != nil {
		return false, err
	}
	head, err := service.resolve(ctx, state.HeadRef)
	if err != nil {
		return false, err
	}
	return base == state.Base && head == state.Head, nil
}

// Enqueue pins the current commits at a base and head ref. The first slice has one lane only.
func (service Service) Enqueue(ctx context.Context, headRef string, pullRequest ...int) (*State, error) {
	if service.BaseRef == "" {
		return nil, errors.New("queue.base_ref is required")
	}
	if len(pullRequest) > 1 || len(pullRequest) == 1 && pullRequest[0] <= 0 {
		return nil, errors.New("the pull request number must be positive")
	}
	if service.RequirePullRequest && len(pullRequest) == 0 {
		return nil, errors.New("queue.require_pull_request requires --pr NUMBER")
	}
	return service.withLock(ctx, func(place, common string) (*State, error) {
		if _, err := os.Lstat(place); err == nil {
			return nil, errors.New("the queue already has a head; remove it before adding another")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		base, err := service.resolve(ctx, service.BaseRef)
		if err != nil {
			return nil, err
		}
		head, err := service.resolve(ctx, headRef)
		if err != nil {
			return nil, err
		}
		if base == head {
			return nil, errors.New("head is the base commit; nothing to queue")
		}
		state := &State{Schema: stateSchema, Repository: common, BaseRef: service.BaseRef,
			HeadRef: headRef, Base: base, Head: head, Attempt: 1}
		if len(pullRequest) == 1 {
			state.PullRequest = pullRequest[0]
		}
		return state, save(place, state)
	})
}

// Status reads state and reports whether either ref moved, without changing local state.
func (service Service) Status(ctx context.Context) (*State, error) {
	return service.withLock(ctx, func(place, common string) (*State, error) {
		return service.snapshot(ctx, place, common)
	})
}

// Snapshot reports the queue state without creating a lock or changing local files.
// It is suitable for read-only inspection; writers may change the state immediately afterward.
func (service Service) Snapshot(ctx context.Context) (*State, error) {
	place, common, err := service.place(ctx)
	if err != nil {
		return nil, err
	}
	return service.snapshot(ctx, place, common)
}

func (service Service) snapshot(ctx context.Context, place, common string) (*State, error) {
	state, err := read(place)
	if err != nil {
		return nil, err
	}
	if state.Repository != common {
		return nil, errors.New("queue state belongs to another repository")
	}
	if state.Outcome != nil {
		return state, nil
	}
	fresh, err := service.fresh(ctx, state)
	state.Stale = !fresh
	return state, err
}

// Prepare recomputes a candidate when an input ref moved. Git merge-tree refuses conflicts;
// commit-tree writes an unreachable object and never updates a branch or the checkout.
func (service Service) Prepare(ctx context.Context) (*State, error) {
	return service.withLock(ctx, func(place, common string) (*State, error) {
		state, err := read(place)
		if err != nil {
			return nil, err
		}
		if state.Repository != common {
			return nil, errors.New("queue state belongs to another repository")
		}
		if state.Outcome != nil {
			return nil, errors.New("the observed landing is recorded; remove the lane before another attempt")
		}
		if !state.HandoffAt.IsZero() {
			return nil, errors.New("the handoff is recorded; reconcile its result before preparing another attempt")
		}
		baseRef := service.BaseRef
		if baseRef == "" {
			return nil, errors.New("queue.base_ref is required")
		}
		base, err := service.resolve(ctx, baseRef)
		if err != nil {
			return nil, err
		}
		head, err := service.resolve(ctx, state.HeadRef)
		if err != nil {
			return nil, err
		}
		if baseRef != state.BaseRef || base != state.Base || head != state.Head {
			state.BaseRef, state.Base, state.Head = baseRef, base, head
			state.Candidate, state.Tree, state.GateClaim, state.GateRunID, state.Receipts = "", "", "", "", nil
			state.Attempt++
			if err := save(place, state); err != nil {
				return nil, err
			}
		}
		if state.Candidate != "" {
			_, err := service.git().Run(ctx, "rev-parse", "--verify", "--quiet", state.Candidate+"^{commit}")
			if err == nil {
				return state, nil
			}
			var gitExit *gitcli.ExitError
			if !errors.As(err, &gitExit) || gitExit.Code != 1 {
				return nil, fmt.Errorf("cannot inspect queue candidate: %w", err)
			}
			state.Candidate, state.Tree, state.GateClaim, state.GateRunID, state.Receipts = "", "", "", "", nil
			state.Attempt++
			if err := save(place, state); err != nil {
				return nil, err
			}
		}
		tree, err := service.git().Line(ctx, "merge-tree", "--write-tree", base, head)
		if err != nil {
			return nil, fmt.Errorf("candidate merge has conflicts or Git refused it: %w", err)
		}
		tree = strings.TrimSpace(tree)
		if !objectID(tree) {
			return nil, errors.New("git did not return one candidate tree")
		}
		git := service.git()
		git.Env = append(gitcli.WithoutRepository(os.Environ()),
			"GIT_AUTHOR_NAME=jackstay", "GIT_AUTHOR_EMAIL=jackstay@example.invalid",
			"GIT_COMMITTER_NAME=jackstay", "GIT_COMMITTER_EMAIL=jackstay@example.invalid")
		candidate, err := git.RunInput(ctx, []byte("jackstay queue candidate\n"), "commit-tree", tree, "-p", base, "-p", head)
		if err != nil {
			return nil, err
		}
		state.Tree, state.Candidate = tree, strings.TrimSpace(string(candidate))
		if !objectID(state.Candidate) {
			return nil, errors.New("git did not return one candidate commit")
		}
		fresh, err := service.fresh(ctx, state)
		if err != nil || !fresh {
			return nil, errors.New("a queue input moved while building the candidate; prepare again")
		}
		return state, save(place, state)
	})
}

func objectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

// beginGate invalidates the previous result before testing starts. A later test
// supersedes the claim, including when both tests use the same candidate.
func (service Service) beginGate(ctx context.Context, prepared *State) (*State, error) {
	var claim [16]byte
	if _, err := rand.Read(claim[:]); err != nil {
		return nil, err
	}
	return service.withLock(ctx, func(place, common string) (*State, error) {
		state, err := read(place)
		if err != nil {
			return nil, err
		}
		fresh, err := service.fresh(ctx, state)
		if err != nil || !fresh || !objectID(state.Candidate) || state.Repository != common ||
			state.Candidate != prepared.Candidate || state.Attempt != prepared.Attempt ||
			state.HeadRef != prepared.HeadRef || state.BaseRef != prepared.BaseRef || state.PullRequest != prepared.PullRequest {
			return nil, errors.New("the candidate or its inputs changed before the gate started")
		}
		if state.Outcome != nil || !state.HandoffAt.IsZero() {
			return nil, errors.New("the handoff is recorded; reconcile its result before testing again")
		}
		state.GateClaim = hex.EncodeToString(claim[:])
		state.GateRunID, state.Receipts = "", nil
		return state, save(place, state)
	})
}

// recordGate saves verified evidence from the current test claim only.
func (service Service) recordGate(ctx context.Context, tested *State, gateRunID string, evidence []Evidence) (*State, error) {
	if !objectID(tested.Candidate) || tested.GateClaim == "" || gateRunID == "" || len(evidence) == 0 {
		return nil, errors.New("a counted gate and its candidate are required")
	}
	for _, item := range evidence {
		if len(item.Key) != 64 || len(item.SHA256) != 64 || !objectID(item.Key) || !objectID(item.SHA256) {
			return nil, errors.New("invalid receipt identity")
		}
	}
	return service.withLock(ctx, func(place, common string) (*State, error) {
		state, err := read(place)
		if err != nil {
			return nil, err
		}
		fresh, err := service.fresh(ctx, state)
		if err != nil || !fresh || state.Repository != common || state.Candidate != tested.Candidate ||
			state.Attempt != tested.Attempt || state.GateClaim != tested.GateClaim {
			return nil, errors.New("the candidate, its inputs or the current test changed while the gate ran")
		}
		if state.Outcome != nil {
			return nil, errors.New("the observed landing is already recorded")
		}
		if !state.HandoffAt.IsZero() {
			return nil, errors.New("the handoff is recorded; reconcile its result before changing gate evidence")
		}
		state.GateRunID, state.Receipts = gateRunID, evidence
		return state, save(place, state)
	})
}

// Handoff returns a tested candidate only while its pinned inputs still match live refs.
func (service Service) Handoff(ctx context.Context) (*State, error) {
	return service.handoff(ctx, nil)
}

func (service Service) handoff(ctx context.Context, verifyReceipts func(*State) error) (*State, error) {
	state, err := service.Status(ctx)
	if err != nil {
		return nil, err
	}
	if state.Stale || state.Candidate == "" || state.GateClaim == "" || state.GateRunID == "" || len(state.Receipts) == 0 {
		return nil, errors.New("candidate is stale or has no counted gate evidence; prepare and test again")
	}
	if state.Outcome != nil {
		return nil, errors.New("the observed landing is already recorded")
	}
	if service.RequirePullRequest && state.PullRequest == 0 {
		return nil, errors.New("queue.require_pull_request forbids local-only handoff")
	}
	if verifyReceipts != nil {
		if err := verifyReceipts(state); err != nil {
			return nil, err
		}
	}
	if err := service.verifyPolicy(ctx, state); err != nil {
		return nil, err
	}
	confirmed, err := service.Status(ctx)
	if err != nil || confirmed.Stale || confirmed.Attempt != state.Attempt || confirmed.Candidate != state.Candidate ||
		confirmed.Repository != state.Repository || confirmed.BaseRef != state.BaseRef ||
		confirmed.HeadRef != state.HeadRef || confirmed.Base != state.Base || confirmed.Head != state.Head ||
		confirmed.PullRequest != state.PullRequest || confirmed.Tree != state.Tree ||
		confirmed.GateClaim != state.GateClaim || confirmed.GateRunID != state.GateRunID ||
		!slices.Equal(confirmed.Receipts, state.Receipts) {
		return nil, errors.New("queue inputs or policy changed while handoff evidence was read")
	}
	return state, nil
}

// recordHandoff pins the verified candidate before the operator can land it.
func (service Service) recordHandoff(ctx context.Context, checked *State) (*State, error) {
	return service.withLock(ctx, func(place, common string) (*State, error) {
		state, err := read(place)
		if err != nil {
			return nil, err
		}
		fresh, err := service.fresh(ctx, state)
		if err != nil || !fresh || state.Repository != common || state.Outcome != nil ||
			state.Repository != checked.Repository || state.BaseRef != checked.BaseRef ||
			state.HeadRef != checked.HeadRef || state.Base != checked.Base || state.Head != checked.Head ||
			state.PullRequest != checked.PullRequest ||
			state.Attempt != checked.Attempt || state.Candidate != checked.Candidate || state.Tree != checked.Tree ||
			state.GateClaim == "" || state.GateClaim != checked.GateClaim || state.GateRunID == "" ||
			state.GateRunID != checked.GateRunID || len(state.Receipts) == 0 || !slices.Equal(state.Receipts, checked.Receipts) {
			return nil, errors.New("queue inputs or evidence changed before handoff could be recorded")
		}
		if !state.HandoffAt.IsZero() {
			return state, nil
		}
		state.HandoffAt = time.Now().UTC()
		return state, save(place, state)
	})
}

// Reconcile stores the observed landing and whether its tree matches the tested candidate.
// Local-only lanes require the current target tip. PR lanes accept its confirmed merge commit
// while it remains reachable from the target, even if later work advanced that branch.
func (service Service) Reconcile(ctx context.Context, commit string) (*State, error) {
	if !objectID(commit) {
		return nil, errors.New("reconcile requires a full observed commit ID")
	}
	return service.withLock(ctx, func(place, common string) (*State, error) {
		state, err := read(place)
		if err != nil {
			return nil, err
		}
		if state.Repository != common || state.BaseRef != service.BaseRef {
			return nil, errors.New("queue repository or target branch changed")
		}
		if state.Outcome != nil {
			if state.Outcome.Commit == commit {
				return state, nil
			}
			return nil, errors.New("a different landing outcome is already recorded")
		}
		if state.HandoffAt.IsZero() || state.Candidate == "" || state.Tree == "" ||
			state.GateRunID == "" || len(state.Receipts) == 0 {
			return nil, errors.New("reconcile requires a recorded, tested handoff")
		}
		observed, err := service.resolve(ctx, state.BaseRef)
		if err != nil {
			return nil, errors.New("target ref cannot be read; fetch the target and retry")
		}
		if state.PullRequest == 0 && observed != commit {
			return nil, errors.New("observed commit is not the current local target ref")
		}
		if state.PullRequest != 0 {
			if _, err := service.git().Run(ctx, "merge-base", "--is-ancestor", commit, observed); err != nil {
				return nil, errors.New("PR landing commit is not reachable from the current target ref")
			}
		}
		if commit == state.Base {
			return nil, errors.New("the target ref has not advanced from the tested base")
		}
		if _, err := service.git().Run(ctx, "merge-base", "--is-ancestor", state.Base, commit); err != nil {
			return nil, errors.New("observed commit does not descend from the tested base")
		}
		tree, err := service.git().Line(ctx, "rev-parse", "--verify", commit+"^{tree}")
		if err != nil || !objectID(tree) {
			return nil, errors.New("the observed commit tree could not be read")
		}
		if state.PullRequest != 0 {
			provider, repository, err := service.provider(ctx)
			if err != nil {
				return nil, fmt.Errorf("pull request landing cannot be confirmed: %w", err)
			}
			pull, err := provider.PullRequest(ctx, state.PullRequest)
			if err != nil {
				return nil, fmt.Errorf("GitHub cannot confirm PR landing; retry reconciliation: %w", err)
			}
			if pull.Number != state.PullRequest || !pull.Merged || pull.MergeCommitSHA != commit ||
				pull.HeadSHA != state.Head || pull.Base != strings.TrimPrefix(state.BaseRef, "refs/heads/") ||
				(repository != "" && reposDiffer(repository, pull.BaseRepository)) {
				return nil, errors.New("GitHub has not confirmed this exact PR landing commit")
			}
		}
		// Do not persist an observation whose target moved during the provider read.
		confirmed, err := service.resolve(ctx, state.BaseRef)
		if err != nil || confirmed != observed {
			return nil, errors.New("target ref moved while the landing was inspected")
		}
		state.Outcome = &Outcome{Attempt: state.Attempt, Commit: commit, Tree: tree,
			Matched: tree == state.Tree, Observed: time.Now().UTC()}
		return state, save(place, state)
	})
}

// Remove ends the local lane. It never changes Git refs or remote state.
func (service Service) Remove(ctx context.Context) error {
	_, err := service.withLock(ctx, func(place, _ string) (*State, error) {
		if _, err := read(place); err != nil {
			return nil, err
		}
		return nil, os.Remove(place)
	})
	return err
}
