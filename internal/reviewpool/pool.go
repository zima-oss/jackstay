// Package reviewpool keeps explicit review requests from several local sessions in one durable queue.
package reviewpool

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
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/project"
	"github.com/zima-oss/jackstay/internal/review"
)

const maximumStateBytes = 4 << 20

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
	ID         string    `json:"id"`
	Repository string    `json:"repository"`
	Base       string    `json:"base"`
	Head       string    `json:"head"`
	Task       string    `json:"task"`
	Reviewers  []string  `json:"reviewers"`
	CommonDir  string    `json:"common_dir"`
	RootDevice string    `json:"root_device"`
	RootInode  uint64    `json:"root_inode"`
	GitDevice  string    `json:"git_device"`
	GitInode   uint64    `json:"git_inode"`
	Status     string    `json:"status"`
	Attempts   int       `json:"attempts"`
	Exit       int       `json:"exit"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type state struct {
	Version int   `json:"version"`
	Jobs    []Job `json:"jobs"`
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
	root, err := filepath.EvalSymlinks(opened.Root)
	if err != nil {
		return Job{}, false, err
	}
	commonDir, rootDevice, rootInode, gitDevice, gitInode, err := repositoryIdentity(ctx, root)
	if err != nil {
		return Job{}, false, err
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
				return nil
			}
		}
		now := time.Now().UTC()
		job = Job{ID: id, Repository: root, Base: base, Head: head, Task: request.Task,
			CommonDir: commonDir, RootDevice: rootDevice, RootInode: rootInode, GitDevice: gitDevice, GitInode: gitInode,
			Reviewers: chosen, Status: "pending", Exit: -1, CreatedAt: now, UpdatedAt: now}
		saved.Jobs = append(saved.Jobs, job)
		created = true
		return nil
	})
	return job, created, err
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
	return pool.edit(ctx, func(saved *state) error {
		for index := range saved.Jobs {
			if saved.Jobs[index].ID == id && saved.Jobs[index].Status == "failed" {
				saved.Jobs[index].Status = "pending"
				saved.Jobs[index].UpdatedAt = time.Now().UTC()
				return nil
			}
		}
		return errors.New("no failed job with that ID")
	})
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
	var job Job
	found := false
	err = pool.edit(ctx, func(saved *state) error {
		for _, status := range []string{"running", "pending"} {
			for index := range saved.Jobs {
				entry := &saved.Jobs[index]
				if entry.Status != status {
					continue
				}
				entry.Status = "running"
				entry.Attempts++
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
			entry.UpdatedAt = time.Now().UTC()
			if runErr == nil && (exit == review.ExitClear || exit == review.ExitBlocks ||
				exit == review.ExitRedesign || exit == review.ExitOwner) {
				entry.Status = "complete"
			} else {
				entry.Status = "failed"
			}
			finished = *entry
			return nil
		}
		return errors.New("running job changed during review")
	})
	return finished, true, errors.Join(runErr, err)
}

func (pool Pool) edit(ctx context.Context, change func(*state) error) error {
	lock, err := flock.WaitFile(ctx, pool.lockPath())
	if err != nil {
		return err
	}
	defer lock.Release()
	saved, err := pool.load()
	if err != nil {
		return err
	}
	if err := change(&saved); err != nil {
		return err
	}
	return pool.save(saved)
}

func (pool Pool) load() (state, error) {
	file, err := os.OpenFile(pool.statePath(), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0) // #nosec G304 -- jackstay-owned queue state
	if errors.Is(err, os.ErrNotExist) {
		return state{Version: 1}, nil
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
	if err := json.Unmarshal(data, &saved); err != nil {
		return state{}, err
	}
	if saved.Version != 1 {
		return state{}, fmt.Errorf("unsupported review pool version %d", saved.Version)
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
