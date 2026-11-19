// Package reviewpool keeps explicit review requests from several local sessions in one durable queue.
package reviewpool

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/project"
	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

const maximumStateBytes = 4 << 20
const maximumQueued = 256

// Request names one immutable review range in an explicit local repository.
type Request struct {
	Repository string
	Base       string
	Head       string
	Task       string
	Reviewers  []string
}

// Job is a persisted request. Status is pending, running, complete, or failed.
// A completed review may still block: Exit is the jst review verdict.
type Job struct {
	ID                  string    `json:"id"`
	Repository          string    `json:"repository"`
	Base                string    `json:"base"`
	Head                string    `json:"head"`
	Task                string    `json:"task"`
	Reviewers           []string  `json:"reviewers"`
	CommonDir           string    `json:"common_dir"`
	StoreRoot           string    `json:"store_root,omitempty"`
	RootDevice          string    `json:"root_device"`
	RootInode           uint64    `json:"root_inode"`
	GitDevice           string    `json:"git_device"`
	GitInode            uint64    `json:"git_inode"`
	Status              string    `json:"status"`
	Attempts            int       `json:"attempts"`
	Exit                int       `json:"exit"`
	AttemptID           string    `json:"attempt_id,omitempty"`
	FailureClass        string    `json:"failure_class,omitempty"`
	NotBefore           time.Time `json:"not_before,omitempty"`
	MaxAttempts         int       `json:"max_attempts,omitempty"`
	MaxPending          int       `json:"max_pending,omitempty"`
	RetryInitialSeconds int       `json:"retry_initial_seconds,omitempty"`
	RetryCapSeconds     int       `json:"retry_cap_seconds,omitempty"`
	HistoryDays         int       `json:"history_days,omitempty"`
	Generation          string    `json:"generation,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type state struct {
	Version       int            `json:"version"`
	Jobs          []Job          `json:"jobs"`
	Subscriptions []subscription `json:"subscriptions,omitempty"`
	Events        []event        `json:"events,omitempty"`
}

type subscription struct {
	JobID     string `json:"job_id"`
	SessionID string `json:"session_id"`
}

// Notification is one durable terminal transition for a subscribed session.
type Notification struct {
	JobID      string `json:"job_id"`
	Attempt    int    `json:"attempt"`
	Status     string `json:"status"`
	Exit       int    `json:"exit"`
	Generation string `json:"generation,omitempty"`
}

type event struct {
	Notification
	Delivered []string `json:"delivered,omitempty"`
}

// Pool stores state below Root. The default is ~/.jackstay/.all/review-pool.
type Pool struct{ Root string }

// Default locates the per-account review queue.
func Default() (Pool, error) {
	root, err := homedir.SharedPath("review-pool")
	return Pool{Root: root}, err
}

func (pool Pool) statePath() string  { return filepath.Join(pool.Root, "state.json") }
func (pool Pool) lockPath() string   { return filepath.Join(pool.Root, "state.lock") }
func (pool Pool) workerPath() string { return filepath.Join(pool.Root, "worker.lock") }

// Enqueue pins both revisions now. Repeated requests for the same repository, range,
// task, and reviewer set return the existing job without adding another attempt.
func (pool Pool) Enqueue(ctx context.Context, request Request) (Job, bool, error) {
	return pool.enqueue(ctx, request, "", "")
}

// EnqueueForSession subscribes a repository-bound session to terminal outcomes.
// The subscription is committed with the request, including duplicate submissions.
func (pool Pool) EnqueueForSession(ctx context.Context, request Request, sessionID, expectedCommonDirectory string) (Job, bool, error) {
	// Chat uses a 24-byte random ID or a 32-byte token-derived ID.
	if len(sessionID) != 48 && len(sessionID) != 64 {
		return Job{}, false, errors.New("invalid session identity")
	}
	if _, err := hex.DecodeString(sessionID); err != nil {
		return Job{}, false, errors.New("invalid session identity")
	}
	if expectedCommonDirectory == "" {
		return Job{}, false, errors.New("repository identity is required")
	}
	return pool.enqueue(ctx, request, sessionID, expectedCommonDirectory)
}

func (pool Pool) enqueue(ctx context.Context, request Request, sessionID, expectedCommonDirectory string) (Job, bool, error) {
	if request.Repository == "" || !filepath.IsAbs(request.Repository) {
		return Job{}, false, errors.New("name an absolute local repository path")
	}
	if request.Base == "" || request.Head == "" || request.Task == "" || len(request.Reviewers) == 0 {
		return Job{}, false, errors.New("base, head, task, and at least one reviewer are required")
	}
	for _, name := range request.Reviewers {
		if !slices.Contains(review.KnownReviewers, name) {
			return Job{}, false, fmt.Errorf("unknown reviewer %q", name)
		}
	}
	if len(request.Reviewers) > len(review.KnownReviewers) {
		return Job{}, false, errors.New("reviewer names must be unique")
	}
	chosen := slices.Clone(request.Reviewers)
	slices.Sort(chosen)
	for index := 1; index < len(chosen); index++ {
		if chosen[index] == chosen[index-1] {
			return Job{}, false, errors.New("reviewer names must be unique")
		}
	}
	opened, err := project.Open(request.Repository, []string{"review"})
	if err != nil {
		return Job{}, false, err
	}
	settings, problems := review.SettingsOf(opened.Config)
	if len(problems) > 0 {
		return Job{}, false, errors.New(strings.Join(problems, "; "))
	}
	store, err := review.StoreFor(settings, opened.Name)
	if err != nil {
		return Job{}, false, err
	}
	root, err := filepath.EvalSymlinks(opened.Root)
	if err != nil {
		return Job{}, false, err
	}
	commonDir, rootDevice, rootInode, gitDevice, gitInode, err := repositoryIdentity(ctx, root)
	if err != nil {
		return Job{}, false, err
	}
	if expectedCommonDirectory != "" && commonDir != expectedCommonDirectory {
		return Job{}, false, errors.New("review project is outside this MCP session's repository")
	}
	git := gitcli.Git{Dir: root}
	if !validTask(ctx, git, request.Task) || len(request.Task) > 200 {
		return Job{}, false, errors.New("task must be a Git branch name of at most 200 bytes")
	}
	base, err := resolve(ctx, git, request.Base)
	if err != nil {
		return Job{}, false, err
	}
	head, err := resolve(ctx, git, request.Head)
	if err != nil {
		return Job{}, false, err
	}
	id := jobID(Job{Repository: root, CommonDir: commonDir, RootDevice: rootDevice, RootInode: rootInode,
		GitDevice: gitDevice, GitInode: gitInode, Base: base, Head: head, Task: request.Task, Reviewers: chosen})
	var job Job
	created := false
	err = pool.edit(ctx, func(saved *state) error {
		for _, existing := range saved.Jobs {
			if existing.ID == id {
				job = existing
				if sessionID != "" {
					saved.subscribe(id, sessionID)
					if existing.Status == "complete" || existing.Status == "failed" {
						saved.recordTerminal(existing)
					}
				}
				return nil
			}
		}
		if !settings.Pool.Accepting {
			return errors.New("repository review pool is paused (review.pool.accepting is false)")
		}
		if err := admission(saved, root, settings.Pool.MaxPending); err != nil {
			return err
		}
		generation, err := newAttemptID()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		job = Job{ID: id, Repository: root, Base: base, Head: head, Task: request.Task, StoreRoot: store.Root,
			CommonDir: commonDir, RootDevice: rootDevice, RootInode: rootInode, GitDevice: gitDevice, GitInode: gitInode,
			Reviewers: chosen, Status: "pending", Exit: -1, CreatedAt: now, UpdatedAt: now,
			MaxPending: settings.Pool.MaxPending, MaxAttempts: settings.Pool.MaxAttempts, RetryInitialSeconds: settings.Pool.RetryInitialSeconds,
			RetryCapSeconds: settings.Pool.RetryCapSeconds, HistoryDays: settings.Pool.HistoryDays, Generation: generation}
		saved.Jobs = append(saved.Jobs, job)
		if sessionID != "" {
			saved.subscribe(id, sessionID)
		}
		created = true
		return nil
	})
	if err == nil && created {
		recordPoolTransition(job, "enqueue")
	}
	return job, created, err
}

func (saved *state) subscribe(jobID, sessionID string) {
	for _, item := range saved.Subscriptions {
		if item.JobID == jobID && item.SessionID == sessionID {
			return
		}
	}
	saved.Subscriptions = append(saved.Subscriptions, subscription{JobID: jobID, SessionID: sessionID})
}

// StatusForSession returns only a request this session submitted in this Git repository.
func (pool Pool) StatusForSession(ctx context.Context, jobID, sessionID, commonDirectory string) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	saved, err := pool.load()
	if err != nil {
		return Job{}, err
	}
	for _, item := range saved.Subscriptions {
		if item.JobID == jobID && item.SessionID == sessionID {
			for _, job := range saved.Jobs {
				if job.ID == jobID && job.CommonDir == commonDirectory {
					return job, nil
				}
			}
		}
	}
	return Job{}, errors.New("review request not found for this session")
}

// Undelivered returns terminal transitions pending materialization in chat.
func (pool Pool) Undelivered(ctx context.Context, sessionID, commonDirectory string) ([]Notification, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	saved, err := pool.load()
	if err != nil {
		return nil, err
	}
	result := []Notification{}
	for _, item := range saved.Events {
		if slices.Contains(item.Delivered, sessionID) {
			continue
		}
		for _, subscription := range saved.Subscriptions {
			if subscription.JobID != item.JobID || subscription.SessionID != sessionID {
				continue
			}
			for _, job := range saved.Jobs {
				if job.ID == item.JobID && job.CommonDir == commonDirectory {
					result = append(result, item.Notification)
				}
			}
		}
	}
	return result, nil
}

// MarkDelivered records that chat accepted an event, so retention cannot redeliver it.
func (pool Pool) MarkDelivered(ctx context.Context, notification Notification, sessionID string) error {
	return pool.edit(ctx, func(saved *state) error {
		for index := range saved.Events {
			item := &saved.Events[index]
			if item.Notification != notification {
				continue
			}
			for _, subscription := range saved.Subscriptions {
				if subscription.JobID == item.JobID && subscription.SessionID == sessionID {
					if !slices.Contains(item.Delivered, sessionID) {
						item.Delivered = append(item.Delivered, sessionID)
					}
					return nil
				}
			}
		}
		return errors.New("review notification is not available to this session")
	})
}

func (saved *state) recordTerminal(job Job) {
	subscribed := false
	for _, item := range saved.Subscriptions {
		if item.JobID == job.ID {
			subscribed = true
			break
		}
	}
	if !subscribed {
		return
	}
	for _, item := range saved.Events {
		if item.JobID == job.ID && item.Attempt == job.Attempts && item.Status == job.Status {
			return
		}
	}
	saved.Events = append(saved.Events, event{Notification: Notification{
		JobID: job.ID, Attempt: job.Attempts, Status: job.Status, Exit: job.Exit, Generation: job.Generation,
	}})
}

// List returns a snapshot in arrival order.
func (pool Pool) List(ctx context.Context) ([]Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	saved, err := pool.load()
	return slices.Clone(saved.Jobs), err
}

// Retry returns a failed job to pending. The request's pinned commits never change.
func (pool Pool) Retry(ctx context.Context, id string) error {
	var retried Job
	err := pool.edit(ctx, func(saved *state) error {
		for index := range saved.Jobs {
			if saved.Jobs[index].ID == id && saved.Jobs[index].Status == "failed" {
				if saved.Jobs[index].Attempts >= attemptLimit(saved.Jobs[index]) {
					return errors.New("review pool attempt limit reached")
				}
				limit := saved.Jobs[index].MaxPending
				if limit == 0 {
					limit = 32
				}
				if err := admission(saved, saved.Jobs[index].Repository, limit); err != nil {
					return err
				}
				saved.Jobs[index].Status = "pending"
				now := time.Now().UTC()
				saved.Jobs[index].UpdatedAt = now
				if saved.Jobs[index].FailureClass == "retryable" {
					saved.Jobs[index].NotBefore = now.Add(retryDelay(saved.Jobs[index]))
				} else {
					saved.Jobs[index].NotBefore = time.Time{}
				}
				retried = saved.Jobs[index]
				return nil
			}
		}
		return errors.New("no failed job with that ID")
	})
	if err == nil {
		recordPoolTransition(retried, "retry")
	}
	return err
}

func admission(saved *state, repository string, repositoryLimit int) error {
	queued, repositoryQueued := 0, 0
	for _, existing := range saved.Jobs {
		if existing.Status == "pending" || existing.Status == "running" {
			queued++
			if existing.Repository == repository {
				repositoryQueued++
			}
		}
	}
	if queued >= maximumQueued {
		return fmt.Errorf("review pool queue is full (%d jobs)", maximumQueued)
	}
	if repositoryQueued >= repositoryLimit {
		return fmt.Errorf("repository review pool queue is full (%d jobs)", repositoryLimit)
	}
	return nil
}

// RunNext takes the machine-wide worker lock and runs at most one request. A previous
// running job is retried first after an interrupted worker. Review findings (exit 1,
// 3, or 4) are final verdicts; reviewer/environment failures (5 or 6) need Retry.
func (pool Pool) RunNext(ctx context.Context, run func(context.Context, Job) (int, error)) (Job, bool, error) {
	worker, err := flock.TryFile(pool.workerPath())
	if err != nil {
		return Job{}, false, err
	}
	defer worker.Release()
	// A killed worker can have committed its round before updating queue state.
	// Reconcile the exact attempt token while still holding the worker lock.
	if err := pool.reconcile(ctx); err != nil {
		return Job{}, false, err
	}
	var job Job
	found := false
	err = pool.edit(ctx, func(saved *state) error {
		for _, status := range []string{"running", "pending"} {
			for index := range saved.Jobs {
				entry := &saved.Jobs[index]
				if entry.Status != status {
					continue
				}
				if status == "running" && entry.FailureClass == "unresolved" {
					continue
				}
				if status == "pending" && time.Now().Before(entry.NotBefore) {
					continue
				}
				token, err := newAttemptID()
				if err != nil {
					return err
				}
				entry.Status = "running"
				entry.Attempts++
				entry.AttemptID = token
				entry.NotBefore = time.Time{}
				entry.UpdatedAt = time.Now().UTC()
				job, found = *entry, true
				return nil
			}
		}
		return nil
	})
	if err != nil || !found {
		return job, found, err
	}
	recordPoolTransition(job, "start")
	runErr := validateJob(ctx, job)
	exit := review.ExitUsage
	if runErr == nil {
		exit, runErr = run(ctx, job)
	}
	if ctx.Err() != nil && runErr == nil {
		runErr = ctx.Err()
	}
	finished := job
	err = pool.edit(context.Background(), func(saved *state) error {
		for index := range saved.Jobs {
			entry := &saved.Jobs[index]
			if entry.ID != job.ID || entry.Status != "running" || entry.Attempts != job.Attempts {
				continue
			}
			entry.Exit = exit
			entry.AttemptID = ""
			entry.UpdatedAt = time.Now().UTC()
			if runErr == nil && (exit == review.ExitClear || exit == review.ExitBlocks ||
				exit == review.ExitRedesign || exit == review.ExitOwner) {
				entry.Status = "complete"
			} else {
				entry.Status = "failed"
				entry.FailureClass = failureClass(exit, runErr)
			}
			finished = *entry
			saved.recordTerminal(*entry)
			return nil
		}
		return errors.New("running job changed during review")
	})
	if err == nil {
		recordPoolTransition(finished, "finish")
	}
	return finished, true, errors.Join(runErr, err)
}

func newAttemptID() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func failureClass(exit int, runErr error) string {
	if runErr == nil && exit == review.ExitReviewer {
		return "retryable"
	}
	return "operator"
}

func retryDelay(job Job) time.Duration {
	initial := job.RetryInitialSeconds
	if initial == 0 {
		initial = 30
	}
	capSeconds := job.RetryCapSeconds
	if capSeconds == 0 {
		capSeconds = 900
	}
	delay := initial
	for step := 1; step < job.Attempts && delay < capSeconds; step++ {
		if delay >= capSeconds/2 {
			return time.Duration(capSeconds) * time.Second
		}
		delay *= 2
	}
	return time.Duration(min(delay, capSeconds)) * time.Second
}

func attemptLimit(job Job) int {
	if job.MaxAttempts > 0 {
		return job.MaxAttempts
	}
	return 5 // legacy queue state
}

func (pool Pool) reconcile(ctx context.Context) error {
	saved, err := pool.load()
	if err != nil {
		return err
	}
	for _, job := range saved.Jobs {
		if job.Status != "running" {
			continue
		}
		var exit int
		var found bool
		var evidenceErr error
		if job.AttemptID != "" {
			exit, found, evidenceErr = recordedRound(ctx, job)
		}
		if evidenceErr != nil {
			// The store may only be temporarily unreadable. Preserve the token so a
			// later next can reconcile the completed round before any replay.
			if err := pool.edit(ctx, func(saved *state) error {
				for index := range saved.Jobs {
					entry := &saved.Jobs[index]
					if entry.ID == job.ID && entry.Status == "running" && entry.AttemptID == job.AttemptID {
						entry.FailureClass = "unresolved"
						entry.UpdatedAt = time.Now().UTC()
						return nil
					}
				}
				return errors.New("running job changed during reconciliation")
			}); err != nil {
				return err
			}
			continue
		}
		if !found && job.FailureClass == "unresolved" {
			if err := pool.edit(ctx, func(saved *state) error {
				for index := range saved.Jobs {
					entry := &saved.Jobs[index]
					if entry.ID == job.ID && entry.Status == "running" && entry.AttemptID == job.AttemptID {
						entry.FailureClass = ""
						return nil
					}
				}
				return errors.New("running job changed during reconciliation")
			}); err != nil {
				return err
			}
		}
		if !found && job.Attempts < attemptLimit(job) {
			continue // old rows and incomplete attempts retain at-least-once execution
		}
		if !found {
			exit = review.ExitUsage
		}
		var settled Job
		if err := pool.edit(ctx, func(saved *state) error {
			for index := range saved.Jobs {
				entry := &saved.Jobs[index]
				if entry.ID == job.ID && entry.Status == "running" && entry.AttemptID == job.AttemptID {
					if found {
						entry.Status = "complete"
					} else {
						entry.Status = "failed"
						entry.FailureClass = "operator"
					}
					entry.Exit, entry.AttemptID = exit, ""
					entry.UpdatedAt = time.Now().UTC()
					saved.recordTerminal(*entry)
					settled = *entry
					return nil
				}
			}
			return errors.New("running job changed during reconciliation")
		}); err != nil {
			return err
		}
		recordPoolTransition(settled, "reconcile")
	}
	return nil
}

func recordedRound(ctx context.Context, job Job) (int, bool, error) {
	if err := validateJob(ctx, job); err != nil {
		return 0, false, err
	}
	opened, err := project.Open(job.Repository, []string{"review"})
	if err != nil {
		return 0, false, err
	}
	settings, problems := review.SettingsOf(opened.Config)
	if len(problems) > 0 {
		return 0, false, errors.New(strings.Join(problems, "; "))
	}
	store, err := review.StoreFor(settings, opened.Name)
	if err != nil {
		return 0, false, err
	}
	if job.StoreRoot != "" && job.StoreRoot != store.Root {
		return 0, false, errors.New("review store changed since enqueue")
	}
	entries, err := store.Entries(job.Task)
	if err != nil {
		return 0, false, err
	}
	for _, entry := range review.Rounds(entries) {
		record, err := store.ReadRound(job.Task, entry.Round)
		if err != nil {
			return 0, false, err
		}
		if record.PoolAttempt != job.AttemptID {
			continue
		}
		if record.Task != job.Task || record.Base != job.Base || record.Commit != job.Head || len(record.Runs) != len(job.Reviewers) {
			return 0, false, errors.New("recorded pool round does not match the pinned request")
		}
		names := make([]string, len(record.Runs))
		for index, run := range record.Runs {
			names[index] = run.Reviewer
		}
		slices.Sort(names)
		if !slices.Equal(names, job.Reviewers) || record.PoolExit == nil ||
			!slices.Contains([]int{0, 1, 3, 4}, *record.PoolExit) {
			return 0, false, errors.New("recorded pool round has an invalid verdict")
		}
		return *record.PoolExit, true, nil
	}
	return 0, false, nil
}

func (pool Pool) edit(ctx context.Context, change func(*state) error) error {
	return pool.editWhen(ctx, func(saved *state) (bool, error) {
		return true, change(saved)
	})
}

func (pool Pool) editWhen(ctx context.Context, change func(*state) (bool, error)) error {
	lock, err := flock.WaitFile(ctx, pool.lockPath())
	if err != nil {
		return err
	}
	defer lock.Release()
	saved, err := pool.load()
	if err != nil {
		return err
	}
	changed, err := change(&saved)
	if err != nil || !changed {
		return err
	}
	saved.Version = currentStateVersion
	return pool.save(saved)
}

func (pool Pool) load() (state, error) {
	file, err := os.OpenFile(pool.statePath(), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0) // #nosec G304 -- jackstay-owned queue state
	if errors.Is(err, os.ErrNotExist) {
		return state{Version: currentStateVersion}, nil
	}
	if err != nil {
		return state{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return state{}, errors.New("review pool state must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumStateBytes+1))
	if err != nil {
		return state{}, err
	}
	if len(data) > maximumStateBytes {
		return state{}, errors.New("review pool state exceeds size limit")
	}
	var saved state
	if err := strictjson.Unmarshal(data, &saved); err != nil {
		if versionErr := unsupportedVersion(data); versionErr != nil {
			return state{}, versionErr
		}
		return state{}, fmt.Errorf("invalid review pool state: %w", err)
	}
	if !supportedStateVersion(saved.Version) {
		return state{}, fmt.Errorf("unsupported review pool version %d", saved.Version)
	}
	if err := saved.validateHistory(); err != nil {
		return state{}, err
	}
	return saved, nil
}

func (pool Pool) save(saved state) error {
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maximumStateBytes {
		return errors.New("review pool state exceeds size limit")
	}
	if err := os.MkdirAll(pool.Root, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(pool.Root, ".review-pool-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), pool.statePath())
}

// ReviewersText is the stable reviewer choice passed to jst review.
func (job Job) ReviewersText() string { return strings.Join(job.Reviewers, ",") }

func repositoryIdentity(ctx context.Context, root string) (string, string, uint64, string, uint64, error) {
	common, err := (gitcli.Git{Dir: root}).CommonDir(ctx)
	if err != nil {
		return "", "", 0, "", 0, err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return "", "", 0, "", 0, err
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", "", 0, "", 0, err
	}
	gitInfo, err := os.Stat(common)
	if err != nil {
		return "", "", 0, "", 0, err
	}
	rootStat, rootOK := rootInfo.Sys().(*syscall.Stat_t)
	gitStat, gitOK := gitInfo.Sys().(*syscall.Stat_t)
	if !rootOK || !gitOK {
		return "", "", 0, "", 0, errors.New("cannot identify repository filesystem")
	}
	return common, fmt.Sprint(rootStat.Dev), rootStat.Ino, fmt.Sprint(gitStat.Dev), gitStat.Ino, nil
}

func validateJob(ctx context.Context, job Job) error {
	if job.Repository == "" || !filepath.IsAbs(job.Repository) || job.CommonDir == "" ||
		job.RootInode == 0 || job.GitInode == 0 || len(job.Base) < 40 || len(job.Head) < 40 ||
		len(job.Reviewers) == 0 || len(job.Task) == 0 || len(job.Task) > 200 {
		return errors.New("stored review job has invalid identity")
	}
	for _, reviewer := range job.Reviewers {
		if !slices.Contains(review.KnownReviewers, reviewer) {
			return errors.New("stored review job has an unknown reviewer")
		}
	}
	if !slices.IsSorted(job.Reviewers) || job.ID != jobID(job) {
		return errors.New("stored review job identity does not match its request")
	}
	root, err := filepath.EvalSymlinks(job.Repository)
	if err != nil {
		return err
	}
	if root != job.Repository {
		return errors.New("repository path changed since enqueue")
	}
	common, rootDevice, rootInode, gitDevice, gitInode, err := repositoryIdentity(ctx, root)
	if err != nil {
		return err
	}
	if common != job.CommonDir || rootDevice != job.RootDevice || rootInode != job.RootInode ||
		gitDevice != job.GitDevice || gitInode != job.GitInode {
		return errors.New("repository identity changed since enqueue")
	}
	git := gitcli.Git{Dir: root}
	if !validTask(ctx, git, job.Task) {
		return errors.New("stored review task is invalid")
	}
	for _, commit := range []string{job.Base, job.Head} {
		if _, err := hex.DecodeString(commit); err != nil {
			return errors.New("stored review commit is invalid")
		}
		resolved, err := resolve(ctx, git, commit)
		if err != nil || resolved != commit {
			return errors.New("stored review commit is unavailable")
		}
	}
	return nil
}

func jobID(job Job) string {
	identity, _ := json.Marshal([]any{job.Repository, job.CommonDir, job.RootDevice, job.RootInode,
		job.GitDevice, job.GitInode, job.Base, job.Head, job.Task, job.Reviewers})
	digest := sha256.Sum256(identity)
	return hex.EncodeToString(digest[:])[:24]
}

func validTask(ctx context.Context, git gitcli.Git, task string) bool {
	if task == "" || strings.HasPrefix(task, "-") {
		return false
	}
	_, err := git.Run(ctx, "check-ref-format", "--branch", task)
	return err == nil
}

func resolve(ctx context.Context, git gitcli.Git, name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "-") {
		return "", fmt.Errorf("%q is not a revision", name)
	}
	commit, err := git.Line(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", name+"^{commit}")
	if err != nil || commit == "" {
		return "", fmt.Errorf("%q is not a commit here", name)
	}
	return commit, nil
}
