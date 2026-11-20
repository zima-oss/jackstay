package review

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/chatevent"
	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/project"
	"github.com/zima-oss/jackstay/internal/strictjson"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// A review run in the background (section 6) is a `jst bg` job running `jst review` with
// `--request ID`. Its request, `<store>/.requests/<task key>.json`, pins what it reviews and keeps
// the chat sessions subscribed to its result. One task has at most one live request: a second
// submission of the same review joins it, a different one is refused while it runs. When the run
// ends it turns each subscription into an owed `review.finished` event, `<store>/.owed/<key>.json`,
// kept until the chat has stored it.

// maximumRequestBytes bounds a request file; it holds a handful of short strings.
const maximumRequestBytes = 64 << 10

// MaximumSubscribers bounds the sessions one request tells.
const MaximumSubscribers = 32

// Request is one review run in the background.
type Request struct {
	ID          string   `json:"id"`
	Task        string   `json:"task"`
	Worktree    string   `json:"worktree"`
	Repository  string   `json:"repository,omitempty"` // its Git common folder
	Base        string   `json:"base,omitempty"`       // "" when the round takes the chain's base or the first parent
	Head        string   `json:"head"`
	Reviewers   []string `json:"reviewers"`
	Fresh       bool     `json:"fresh,omitempty"`
	PastLimit   bool     `json:"past_limit,omitempty"`
	Focus       string   `json:"focus,omitempty"`
	Subscribers []string `json:"subscribers,omitempty"`
	Created     string   `json:"created"`
	Deadline    string   `json:"deadline"`
	PID         int      `json:"pid,omitempty"`
	Start       int64    `json:"start,omitempty"` // the process's start in microseconds (procs.Process)
	Running     bool     `json:"running,omitempty"`
	Log         string   `json:"log"`
}

func (store Store) requestsPath() string { return filepath.Join(store.Root, ".requests") }

// RequestPath is a task's request file.
func (store Store) RequestPath(task string) string {
	return filepath.Join(store.requestsPath(), taskKey(task)+".json")
}

// Submission is what a session asks to have reviewed in the background.
type Submission struct {
	Revision   string // REV or BASE..REV; "" is HEAD
	Task       string
	Reviewers  []string
	Fresh      bool
	PastLimit  bool
	Focus      string
	Subscriber string // the chat session to tell, "" for none
}

// Launch starts argv detached in folder, its output going to log, and returns its process id and
// its start (procs.Process.StartMicros, 0 when unknown). done is closed when the process ends.
type Launch func(argv []string, folder, log string) (pid int, start int64, done <-chan struct{}, err error)

// DetachedLaunch is Launch on this machine: a session of its own (supervise.Detach), with
// jackstay's environment less the chat session's token, which the run does not need.
func DetachedLaunch(argv []string, folder, log string) (int, int64, <-chan struct{}, error) {
	environment := slices.DeleteFunc(os.Environ(), func(pair string) bool {
		return strings.HasPrefix(pair, "JACKSTAY_CHAT_SESSION_TOKEN=")
	})
	pid, done, err := supervise.Detach(argv, folder, environment, log)
	if err != nil {
		return 0, 0, nil, err
	}
	var start int64
	if row, found, err := procs.Row(pid); err == nil && found {
		start = row.StartMicros
	}
	return pid, start, done, nil
}

// Background submits reviews of one project.
type Background struct {
	RepositoryRoot string
	// Repository is the Git common folder of RepositoryRoot: the repository a request belongs to, so
	// two clones that share a project's review store see only their own requests. "" is any.
	Repository string
	Settings   Settings
	Repo       Repository
	Store      Store
	Executable string
	Launch     Launch
	Now        func() time.Time
	// Alive says whether the process of a request still runs; nil uses the process table.
	Alive func(pid int, start int64) (alive, known bool)
	// Started waits for a new request to be taken by its run; nil does not wait.
	Started time.Duration
}

// Submitted is a submission's answer.
type Submitted struct {
	Request Request
	Joined  bool // an equal request was already running and this one subscribed to it
}

// ErrBusy is a task with another review running.
var ErrBusy = errors.New("task busy")

// Submit starts the review in the background, or joins an equal one running.
func (background Background) Submit(ctx context.Context, submission Submission) (Submitted, error) {
	settings := background.Settings
	names := settings.Use
	if len(submission.Reviewers) > 0 {
		names = submission.Reviewers
	}
	if len(names) == 0 {
		return Submitted{}, fmt.Errorf("`review.use` is empty: choose a reviewer with --reviewer %s", strings.Join(KnownReviewers, " or "))
	}
	for _, name := range names {
		if !slices.Contains(KnownReviewers, name) {
			return Submitted{}, fmt.Errorf("unknown reviewer %q: %s", name, strings.Join(KnownReviewers, ", "))
		}
	}
	if len(slices.Compact(slices.Sorted(slices.Values(names)))) != len(names) {
		return Submitted{}, errors.New("a reviewer is named twice")
	}
	baseName, revisionName, ranged := strings.Cut(submission.Revision, "..")
	if !ranged {
		revisionName, baseName = submission.Revision, ""
	}
	if revisionName == "" {
		revisionName = "HEAD"
	}
	head, err := background.Repo.Resolve(ctx, revisionName)
	if err != nil {
		return Submitted{}, err
	}
	base := ""
	if ranged {
		if base, err = background.Repo.Resolve(ctx, baseName); err != nil {
			return Submitted{}, err
		}
	}
	task := submission.Task
	if task == "" {
		if task, err = background.Repo.Branch(ctx); err != nil || task == "" {
			return Submitted{}, errors.New("no branch is checked out: name the task with --task")
		}
	}
	if len(task) > 200 || !background.Repo.ValidBranchName(ctx, task) {
		return Submitted{}, fmt.Errorf("task %q is not a name git takes for a branch (at most 200 bytes)", task)
	}
	if strings.ContainsAny(submission.Focus, "\r\n") || len(submission.Focus) > 2000 {
		return Submitted{}, errors.New("--focus is one line of at most 2000 bytes")
	}
	now := background.Now()
	asked := Request{Task: task, Worktree: background.RepositoryRoot, Repository: background.Repository, Base: base, Head: head, Reviewers: names,
		Fresh: submission.Fresh, PastLimit: submission.PastLimit, Focus: submission.Focus,
		Created: now.UTC().Format(time.RFC3339),
		// The backstop of `jst bg` is the review's own budget and a minute more.
		Deadline: now.Add(time.Duration(settings.Budget(names)+1) * time.Minute).UTC().Format(time.RFC3339)}
	if submission.Subscriber != "" {
		asked.Subscribers = []string{submission.Subscriber}
	}

	// A request whose run died is ended first, its sessions told it was lost.
	if err := background.endIfDead(ctx, task); err != nil {
		return Submitted{}, err
	}
	var answer Submitted
	path := background.Store.RequestPath(task)
	err = durable.Update(ctx, path, 0o600, maximumRequestBytes, func(current []byte) ([]byte, error) {
		if current != nil {
			var running Request
			if err := strictjson.UnmarshalOpen(current, &running); err != nil {
				return nil, fmt.Errorf("the task's request %s: %w", path, err)
			}
			if background.live(running) {
				if !running.sameReview(asked) {
					return nil, fmt.Errorf("%w: a review of %s is running in the background (`jst procs`); submit again when it ends",
						ErrBusy, running.rangeText())
				}
				if submission.Subscriber != "" && !slices.Contains(running.Subscribers, submission.Subscriber) {
					if len(running.Subscribers) >= MaximumSubscribers {
						return nil, fmt.Errorf("the running review already tells %d sessions", MaximumSubscribers)
					}
					running.Subscribers = append(running.Subscribers, submission.Subscriber)
				}
				answer = Submitted{Request: running, Joined: true}
				return json.MarshalIndent(running, "", "  ")
			}
			return nil, fmt.Errorf("%w: the review of %s just stopped; submit again", ErrBusy, running.rangeText())
		}
		identity, err := newRequestID()
		if err != nil {
			return nil, err
		}
		asked.ID = identity
		asked.Log = filepath.Join(background.Store.requestsPath(), taskKey(task)+"-"+identity+".log")
		answer = Submitted{Request: asked}
		return json.MarshalIndent(asked, "", "  ")
	})
	if err != nil {
		return Submitted{}, err
	}
	if answer.Joined {
		return answer, nil
	}
	return background.launch(ctx, answer)
}

func (background Background) launch(ctx context.Context, answer Submitted) (Submitted, error) {
	request := answer.Request
	argv := []string{background.Executable, "bg", "--for", strconv.Itoa(background.Settings.Budget(request.Reviewers) + 1),
		"--name", "review-" + request.Task, "--", background.Executable, "review"}
	argv = append(argv, request.reviewArguments()...)
	pid, start, done, err := background.Launch(argv, background.RepositoryRoot, request.Log)
	if err != nil {
		// Sessions may have joined meanwhile: they are told, as of any other end.
		failure := fmt.Errorf("starting the review in the background: %w", err)
		end, _, endErr := background.Store.endRequest(ctx, request.Task, func(current Request) bool { return current.ID == request.ID },
			func(current Request) End {
				return End{Request: current, Status: ExitUsage, Next: failure.Error() + "; submit again"}
			})
		if endErr == nil {
			_ = background.Store.deliver(ctx, end, "", chatevent.Default())
		}
		return Submitted{}, errors.Join(failure, endErr)
	}
	request.PID, request.Start = pid, start
	err = durable.Update(ctx, background.Store.RequestPath(request.Task), 0o600, maximumRequestBytes, func(current []byte) ([]byte, error) {
		var saved Request
		if current == nil || strictjson.UnmarshalOpen(current, &saved) != nil || saved.ID != request.ID {
			return nil, nil // the run has ended already, or its request was replaced
		}
		saved.PID, saved.Start = pid, start
		request = saved
		return json.MarshalIndent(saved, "", "  ")
	})
	answer.Request = request
	if err != nil {
		return answer, err
	}
	if background.Started > 0 {
		// `jst bg` finds the session it belongs to from its parents: wait until the run has taken
		// its task, so its parent is still here when it looks.
		background.waitStarted(ctx, request, done)
	}
	return answer, nil
}

func (background Background) waitStarted(ctx context.Context, request Request, done <-chan struct{}) {
	wait, cancel := context.WithTimeout(ctx, background.Started)
	defer cancel()
	for {
		current, found, err := background.Store.ReadRequest(request.Task)
		if err != nil || !found || current.ID != request.ID || current.Running {
			return
		}
		select {
		case <-wait.Done():
			return
		case <-done:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (background Background) live(request Request) bool {
	if request.ID != "" {
		// A request with an end is over, whatever its process: its launcher died after ending it.
		if _, err := os.Stat(background.Store.endPath(request.Task, request.ID)); err == nil {
			return false
		}
	}
	if request.PID <= 0 {
		// Not launched yet, or its launch failed before it was recorded: live while young.
		created, err := time.Parse(time.RFC3339, request.Created)
		return err == nil && background.Now().Sub(created) < time.Minute
	}
	alive := background.Alive
	if alive == nil {
		alive = processAlive
	}
	if running, known := alive(request.PID, request.Start); known {
		return running
	}
	deadline, err := time.Parse(time.RFC3339, request.Deadline)
	return err == nil && background.Now().Before(deadline)
}

// ours says whether request was made in this background's repository.
func (background Background) ours(request Request) bool {
	return background.Repository == "" || request.Repository == background.Repository
}

func processAlive(pid int, start int64) (bool, bool) {
	row, found, err := procs.Row(pid)
	if err != nil {
		return false, false
	}
	return found && !row.Zombie && (start == 0 || row.StartMicros == start), true
}

func (request Request) sameReview(other Request) bool {
	return request.Task == other.Task && request.Worktree == other.Worktree && request.Repository == other.Repository &&
		request.Base == other.Base &&
		request.Head == other.Head && slices.Equal(request.Reviewers, other.Reviewers) && request.Fresh == other.Fresh &&
		request.PastLimit == other.PastLimit && request.Focus == other.Focus
}

func (request Request) rangeText() string {
	if request.Base == "" {
		return short(request.Head)
	}
	return short(request.Base) + ".." + short(request.Head)
}

func (request Request) reviewArguments() []string {
	revision := request.Head
	if request.Base != "" {
		revision = request.Base + ".." + request.Head
	}
	words := []string{"--task", request.Task, "--reviewer", strings.Join(request.Reviewers, ","), "--request", request.ID}
	if request.Fresh {
		words = append(words, "--fresh")
	}
	if request.PastLimit {
		words = append(words, "--past-limit")
	}
	if request.Focus != "" {
		words = append(words, "--focus", request.Focus)
	}
	return append(words, "--", revision)
}

func newRequestID() (string, error) {
	data := make([]byte, 8)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

// ReadRequest is the task's request, if it has one.
func (store Store) ReadRequest(task string) (Request, bool, error) {
	data, err := durable.ReadLimited(store.RequestPath(task), maximumRequestBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return Request{}, false, nil
	}
	if err != nil {
		return Request{}, false, err
	}
	var request Request
	if err := strictjson.UnmarshalOpen(data, &request); err != nil {
		return Request{}, false, err
	}
	return request, true, nil
}

// markRunning records that the run of request id has taken its task.
func (store Store) markRunning(ctx context.Context, task, id string) error {
	return durable.Update(ctx, store.RequestPath(task), 0o600, maximumRequestBytes, func(current []byte) ([]byte, error) {
		var saved Request
		if current == nil || strictjson.UnmarshalOpen(current, &saved) != nil || saved.ID != id {
			return nil, fmt.Errorf("request %s is not the task's request", id)
		}
		saved.Running = true
		return json.MarshalIndent(saved, "", "  ")
	})
}

// OutcomeWord is the word for a review's exit status.
func OutcomeWord(status int) string {
	switch status {
	case ExitClear:
		return "clear"
	case ExitBlocks:
		return "blocks"
	case ExitUsage:
		return "refused"
	case ExitRedesign:
		return "redesign due"
	case ExitOwner:
		return "the owner decides"
	case ExitReviewer:
		return "no usable answer"
	case ExitEnvironment:
		return "the environment refuses"
	}
	return "exit " + strconv.Itoa(status)
}

// BackgroundAt is the project around folder, ready to submit reviews from another command (the
// chat's `review.submit`), with jackstay's own binary as executable.
func BackgroundAt(folder, executable string) (Background, error) {
	found, err := project.Open(folder, []string{"review"})
	if err != nil {
		return Background{}, err
	}
	settings, problems := SettingsOf(found.Config)
	if len(problems) > 0 {
		return Background{}, errors.New(strings.Join(problems, "; "))
	}
	store, err := StoreFor(settings, found.Name)
	if err != nil {
		return Background{}, err
	}
	repository, err := (gitRepository{root: found.Root}).CommonDir(context.Background())
	if err != nil {
		return Background{}, err
	}
	return Background{RepositoryRoot: found.Root, Repository: repository, Settings: settings, Repo: RepositoryAt(found.Root),
		Store: store, Executable: executable, Launch: DetachedLaunch, Now: time.Now, Started: 10 * time.Second}, nil
}

// Status is where a task's background reviews in this repository stand: the request running, if
// any, and how the latest one ended (its outcome, round and report). It reads nothing of the task's
// rounds kept by project, which clones of one project share: a round made in the foreground is
// read with `jst review last`.
type Status struct {
	Running *Request `json:"running,omitempty"`
	Last    *End     `json:"last,omitempty"`
	Report  string   `json:"report,omitempty"` // the latest end's report, when it made a round or attempt
}

// StatusOf reads where task stands in this repository.
func (background Background) StatusOf(task string) (Status, error) {
	var status Status
	if err := background.ReapLost(context.Background()); err != nil {
		return Status{}, err
	}
	request, found, err := background.Store.ReadRequest(task)
	if err != nil {
		return Status{}, err
	}
	if found && background.live(request) && background.ours(request) {
		status.Running = &request
	}
	ends, err := background.Store.ends(task)
	if err != nil {
		return Status{}, err
	}
	for index := len(ends) - 1; index >= 0; index-- {
		if background.ours(ends[index].Request) {
			status.Last = &ends[index]
			if status.Last.Folder != "" {
				status.Report = filepath.Join(status.Last.Folder, "report.md")
			}
			break
		}
	}
	return status, nil
}
