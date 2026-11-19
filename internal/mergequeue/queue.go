// Package mergequeue keeps a single local candidate for manual landing. It does not write refs
// or ask a hosting provider to merge: an operator must make and verify the final decision.
package mergequeue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/delivery"
	"github.com/zima-oss/jackstay/internal/flock"
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

// State is a local single-head queue. Attempts increment whenever inputs change.
type State struct {
	Schema     int        `json:"schema"`
	Repository string     `json:"repository"`
	BaseRef    string     `json:"base_ref"`
	HeadRef    string     `json:"head_ref"`
	Base       string     `json:"base"`
	Head       string     `json:"head"`
	Candidate  string     `json:"candidate,omitempty"`
	Tree       string     `json:"tree,omitempty"`
	Attempt    int        `json:"attempt"`
	GateRunID  string     `json:"gate_run_id,omitempty"`
	Receipts   []Evidence `json:"receipts,omitempty"`
	Updated    time.Time  `json:"updated"`
	Stale      bool       `json:"-"`
}

// Service locates local queue state by project and Git common directory. BaseRef must name a
// moving ref; supplying an immutable SHA would make freshness checks meaningless.
type Service struct {
	Here    string
	Project string
	BaseRef string
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
	lock, err := flock.WaitFile(ctx, place+".lock")
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	return action(place, common)
}

func read(place string) (*State, error) {
	file, err := os.OpenFile(place, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("queue state is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, delivery.Limit+1))
	if err != nil || len(data) > delivery.Limit {
		return nil, errors.New("queue state cannot be read or is too large")
	}
	var state State
	if err := strictjson.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Schema != stateSchema || state.Repository == "" || state.BaseRef == "" || state.HeadRef == "" ||
		state.Base == "" || state.Head == "" || state.Attempt < 1 || state.Updated.IsZero() {
		return nil, errors.New("queue state is incomplete or has an unsupported schema")
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
	temporary, err := os.CreateTemp(filepath.Dir(place), ".queue-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	defer func() { _ = temporary.Close() }()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), place)
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
func (service Service) Enqueue(ctx context.Context, headRef string) (*State, error) {
	if service.BaseRef == "" {
		return nil, errors.New("queue.base_ref is required")
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
		return state, save(place, state)
	})
}

// Status reads state and reports whether either ref moved, without changing local state.
func (service Service) Status(ctx context.Context) (*State, error) {
	return service.withLock(ctx, func(place, common string) (*State, error) {
		state, err := read(place)
		if err != nil {
			return nil, err
		}
		if state.Repository != common {
			return nil, errors.New("queue state belongs to another repository")
		}
		fresh, err := service.fresh(ctx, state)
		state.Stale = !fresh
		return state, err
	})
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
			state.Candidate, state.Tree, state.GateRunID, state.Receipts = "", "", "", nil
			state.Attempt++
			if err := save(place, state); err != nil {
				return nil, err
			}
		}
		if state.Candidate != "" {
			return state, nil
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

// recordGate saves evidence from this command's completed gate invocation, after the caller
// verified each exported receipt. It is deliberately private: no CLI can attach arbitrary data.
func (service Service) recordGate(ctx context.Context, candidate, gateRunID string, evidence []Evidence) (*State, error) {
	if !objectID(candidate) || gateRunID == "" || len(evidence) == 0 {
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
		if err != nil || !fresh || state.Repository != common || state.Candidate != candidate {
			return nil, errors.New("the candidate or its inputs changed while the gate ran")
		}
		state.GateRunID, state.Receipts = gateRunID, evidence
		return state, save(place, state)
	})
}

// Handoff returns a tested candidate only while its pinned inputs still match live refs.
func (service Service) Handoff(ctx context.Context) (*State, error) {
	state, err := service.Status(ctx)
	if err != nil {
		return nil, err
	}
	if state.Stale || state.Candidate == "" || state.GateRunID == "" || len(state.Receipts) == 0 {
		return nil, errors.New("candidate is stale or has no counted gate evidence; prepare and test again")
	}
	return state, nil
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
