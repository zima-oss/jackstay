package review

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	Base        string   `json:"base,omitempty"` // "" when the round takes the chain's base or the first parent
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

func (store Store) owedPath() string { return filepath.Join(store.Root, ".owed") }

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
	Settings       Settings
	Repo           Repository
	Store          Store
	Executable     string
	Launch         Launch
	Now            func() time.Time
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
	asked := Request{Task: task, Worktree: background.RepositoryRoot, Base: base, Head: head, Reviewers: names,
		Fresh: submission.Fresh, PastLimit: submission.PastLimit, Focus: submission.Focus,
		Created: now.UTC().Format(time.RFC3339),
		// The backstop of `jst bg` is the review's own budget and a minute more.
		Deadline: now.Add(time.Duration(settings.Budget(names)+1) * time.Minute).UTC().Format(time.RFC3339)}
	if submission.Subscriber != "" {
		asked.Subscribers = []string{submission.Subscriber}
	}

	var answer Submitted
	var replaced *Request
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
			replaced = &running
			// An equal review that ended without telling its sessions is run again for them.
			if running.sameReview(asked) {
				for _, subscriber := range running.Subscribers {
					if !slices.Contains(asked.Subscribers, subscriber) && len(asked.Subscribers) < MaximumSubscribers {
						asked.Subscribers = append(asked.Subscribers, subscriber)
					}
				}
				replaced.Subscribers = nil
			} else if err := background.Store.owe(ctx, running.lostEvents()); err != nil {
				// Saved before the request that names them is overwritten.
				return nil, fmt.Errorf("telling the sessions of the dead review it was lost: %w", err)
			}
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
	if replaced != nil && len(replaced.Subscribers) > 0 {
		// Owed already; told now when the chat takes it, else pulled later.
		_ = background.Store.deliver(ctx, replaced.lostEvents(), chatevent.Default())
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
		_ = background.Store.dropRequest(ctx, request.Task, request.ID)
		return Submitted{}, fmt.Errorf("starting the review in the background: %w", err)
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

func processAlive(pid int, start int64) (bool, bool) {
	row, found, err := procs.Row(pid)
	if err != nil {
		return false, false
	}
	return found && !row.Zombie && (start == 0 || row.StartMicros == start), true
}

func (request Request) sameReview(other Request) bool {
	return request.Task == other.Task && request.Worktree == other.Worktree && request.Base == other.Base &&
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

// retireRequest removes the task's request when it is id (or, with id "", whatever request is
// there and dead says is dead). Under the request's lock and before the removal, before(request)
// saves what must outlive it: a crash between the two leaves the request, never a lost result.
func (store Store) retireRequest(ctx context.Context, task, id string, dead func(Request) bool, before func(Request) error) (Request, error) {
	var taken Request
	path := store.RequestPath(task)
	held, err := durable.Lock(ctx, path)
	if err != nil {
		return Request{}, err
	}
	defer held.Release()
	data, err := durable.ReadLimited(path, maximumRequestBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return Request{}, nil
	}
	if err != nil {
		return Request{}, err
	}
	if err := strictjson.UnmarshalOpen(data, &taken); err != nil {
		return Request{}, err
	}
	if id != "" && taken.ID != id || id == "" && (dead == nil || !dead(taken)) {
		return Request{}, nil
	}
	if before != nil {
		if err := before(taken); err != nil {
			return Request{}, err
		}
	}
	if err := os.Remove(path); err != nil {
		return Request{}, err
	}
	return taken, durable.SyncFolder(filepath.Dir(path))
}

func (store Store) dropRequest(ctx context.Context, task, id string) error {
	_, err := store.retireRequest(ctx, task, id, nil, nil)
	return err
}

// Finish ends the run of request id: its subscriptions become owed `review.finished` events,
// published now when the chat takes them. It never fails the review: what it could not do is said.
func (store Store) Finish(ctx context.Context, task, id string, result Result, publisher chatevent.Publisher) error {
	var events []chatevent.Event
	_, err := store.retireRequest(ctx, task, id, nil, func(request Request) error {
		events = request.finishedEvents(result)
		if err := store.owe(ctx, events); err != nil {
			return err
		}
		return store.writeLast(request, result)
	})
	if err != nil {
		return err
	}
	return store.deliver(ctx, events, publisher)
}

// Last is how a task's latest background request ended: kept beside its request file, so the
// status of a run that never made an attempt is not an older round's.
type Last struct {
	Request Request `json:"request"`
	Status  int     `json:"status"`
	Outcome string  `json:"outcome"`
	Next    string  `json:"next"`
	Folder  string  `json:"folder,omitempty"`
	Round   int     `json:"round,omitempty"`
	Ended   string  `json:"ended"`
}

func (store Store) lastPath(task string) string {
	return filepath.Join(store.requestsPath(), taskKey(task)+".last.json")
}

func (store Store) writeLast(request Request, result Result) error {
	last := Last{Request: request, Status: result.Status, Outcome: OutcomeWord(result.Status), Next: result.Next,
		Folder: result.Folder, Ended: time.Now().UTC().Format(time.RFC3339)}
	last.Request.Subscribers = nil
	if result.Record != nil {
		last.Round = result.Record.Round
	}
	data, err := json.MarshalIndent(last, "", "  ")
	if err != nil {
		return err
	}
	return durable.WriteAtomic(store.lastPath(request.Task), data, 0o600, maximumRequestBytes)
}

// ReadLast is how the task's latest background request ended, if one has.
func (store Store) ReadLast(task string) (Last, bool, error) {
	data, err := durable.ReadLimited(store.lastPath(task), maximumRequestBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return Last{}, false, nil
	}
	if err != nil {
		return Last{}, false, err
	}
	var last Last
	if err := strictjson.UnmarshalOpen(data, &last); err != nil {
		return Last{}, false, err
	}
	return last, true, nil
}

// ReapLost retires every request whose process is gone without having finished, telling its
// sessions so: a run killed before Finish (a restart, a KILL) is not waited for forever.
func (background Background) ReapLost(ctx context.Context) error {
	listed, err := os.ReadDir(background.Store.requestsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range listed {
		name := item.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".last.json") || strings.HasPrefix(name, ".") {
			continue
		}
		data, err := durable.ReadLimited(filepath.Join(background.Store.requestsPath(), name), maximumRequestBytes)
		var request Request
		if err != nil || strictjson.UnmarshalOpen(data, &request) != nil || request.Task == "" || background.live(request) {
			continue
		}
		_, err = background.Store.retireRequest(ctx, request.Task, "", func(current Request) bool {
			return current.ID == request.ID && !background.live(current)
		}, func(current Request) error {
			return background.Store.owe(ctx, current.lostEvents())
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// DeliverOwed tells recipient what is owed to it, after retiring the requests that died.
func (background Background) DeliverOwed(ctx context.Context, recipient string, publisher chatevent.Publisher) error {
	reaped := background.ReapLost(ctx)
	return errors.Join(reaped, background.Store.DeliverOwed(ctx, recipient, publisher))
}

func (request Request) event(summary, body string, meta map[string]string, attachments []chatevent.Attachment) chatevent.Event {
	meta["task"] = request.Task
	meta["request"] = request.ID
	return chatevent.Event{Kind: chatevent.KindReviewFinished, Worktree: request.Worktree, Summary: chatevent.Summary(summary),
		Body: body, Meta: meta, Attachments: attachments}
}

// perSubscriber is one event for each subscriber, each with its own key, so each is owed apart.
func (request Request) perSubscriber(event chatevent.Event, ending string) []chatevent.Event {
	events := make([]chatevent.Event, 0, len(request.Subscribers))
	for _, subscriber := range request.Subscribers {
		one := event
		one.Recipients = []string{subscriber}
		digest := sha256.Sum256([]byte(request.ID + "\x00" + ending + "\x00" + subscriber))
		one.Key = "review/" + hex.EncodeToString(digest[:16])
		events = append(events, one)
	}
	return events
}

func (request Request) finishedEvents(result Result) []chatevent.Event {
	outcome := OutcomeWord(result.Status)
	summary := fmt.Sprintf("review of %s (%s): %s", request.Task, request.rangeText(), outcome)
	meta := map[string]string{"exit": strconv.Itoa(result.Status), "verdict": outcome}
	var attachments []chatevent.Attachment
	if result.Record != nil {
		summary = fmt.Sprintf("review of %s, round %d (%s): %s", request.Task, result.Record.Round, request.rangeText(), outcome)
		meta["round"] = strconv.Itoa(result.Record.Round)
		attachments = append(attachments, chatevent.Attachment{
			URI: fmt.Sprintf("jst:review/%s/%d", request.Task, result.Record.Round), Title: "the round's report"})
	}
	body := "next: " + result.Next
	if result.Folder != "" {
		body += "\nreport: " + filepath.Join(result.Folder, "report.md")
	}
	body += fmt.Sprintf("\nread it with `jst review last --task %s`", request.Task)
	if len(body) > chatevent.MaxBodyBytes {
		body = body[:chatevent.MaxBodyBytes]
	}
	return request.perSubscriber(request.event(summary, body, meta, attachments), "finished")
}

func (request Request) lostEvents() []chatevent.Event {
	summary := fmt.Sprintf("review of %s (%s): stopped before it finished", request.Task, request.rangeText())
	body := fmt.Sprintf("its process is gone; the log is %s. Submit it again; reviewers that answered are not run again.", request.Log)
	return request.perSubscriber(request.event(summary, body, map[string]string{"verdict": "lost"}, nil), "lost")
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

func owedFile(store Store, key string) string {
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(store.owedPath(), hex.EncodeToString(digest[:])+".json")
}

// owe writes each event as owed, before it is published.
func (store Store) owe(_ context.Context, events []chatevent.Event) error {
	if len(events) == 0 {
		return nil
	}
	if err := durable.MakeFolder(store.owedPath(), 0o700); err != nil {
		return err
	}
	var failures []error
	for _, event := range events {
		data, err := json.MarshalIndent(event, "", "  ")
		if err == nil {
			err = durable.WriteAtomic(owedFile(store, event.Key), data, 0o600, maximumRequestBytes)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (store Store) deliver(ctx context.Context, events []chatevent.Event, publisher chatevent.Publisher) error {
	var failures []error
	for _, event := range events {
		if err := publisher.Publish(ctx, event); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := os.Remove(owedFile(store, event.Key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Owed are the events still owed to recipient.
func (store Store) Owed(recipient string) ([]chatevent.Event, error) {
	listed, err := os.ReadDir(store.owedPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var owed []chatevent.Event
	for _, item := range listed {
		if !strings.HasSuffix(item.Name(), ".json") || strings.HasPrefix(item.Name(), ".") {
			continue
		}
		data, err := durable.ReadLimited(filepath.Join(store.owedPath(), item.Name()), maximumRequestBytes)
		if err != nil {
			continue
		}
		var event chatevent.Event
		if strictjson.UnmarshalOpen(data, &event) != nil || !slices.Contains(event.Recipients, recipient) {
			continue
		}
		owed = append(owed, event)
	}
	return owed, nil
}

// DeliverOwed publishes what is still owed to recipient: the chat pulls it when that session
// next reads its inbox, so no event waits for another review to run.
func (store Store) DeliverOwed(ctx context.Context, recipient string, publisher chatevent.Publisher) error {
	owed, err := store.Owed(recipient)
	if err != nil || len(owed) == 0 {
		return err
	}
	return store.deliver(ctx, owed, publisher)
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
	return Background{RepositoryRoot: found.Root, Settings: settings, Repo: RepositoryAt(found.Root), Store: store,
		Executable: executable, Launch: DetachedLaunch, Now: time.Now, Started: 10 * time.Second}, nil
}

// Status is where a task's reviews stand: the request running in the background, if any, and the
// newest round or attempt.
type Status struct {
	Running *Request `json:"running,omitempty"`
	// Last is how the latest background request ended, even one that made no attempt.
	Last    *Last  `json:"last,omitempty"`
	Newest  *Entry `json:"newest,omitempty"`
	Commit  string `json:"commit,omitempty"` // what the newest round or attempt reviewed
	Base    string `json:"base,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	Blocks  *bool  `json:"blocks,omitempty"`
	Report  string `json:"report,omitempty"`
}

// StatusOf reads where task stands.
func (background Background) StatusOf(task string) (Status, error) {
	var status Status
	request, found, err := background.Store.ReadRequest(task)
	if err != nil {
		return Status{}, err
	}
	if found && background.live(request) {
		status.Running = &request
	}
	last, found, err := background.Store.ReadLast(task)
	if err != nil {
		return Status{}, err
	}
	if found {
		status.Last = &last
	}
	entries, err := background.Store.Entries(task)
	if err != nil || len(entries) == 0 {
		return status, err
	}
	newest := entries[len(entries)-1]
	status.Newest = &newest
	status.Report = filepath.Join(newest.Path, "report.md")
	if newest.Round > 0 {
		record, err := readRecord(newest.Path)
		if err != nil {
			return status, err
		}
		code, _ := Disposition(background.Settings, record)
		status.Outcome = OutcomeWord(code)
		status.Blocks = &record.Outcome.Blocks
		status.Commit, status.Base = record.Commit, record.Base
	} else {
		status.Outcome = "no round: " + failureWords(ReadRuns(newest.Path))
		var attempt AttemptRecord
		if data, err := os.ReadFile(filepath.Join(newest.Path, AttemptName)); err == nil && strictjson.UnmarshalOpen(data, &attempt) == nil {
			status.Commit, status.Base = attempt.Commit, attempt.Base
		}
	}
	return status, nil
}
