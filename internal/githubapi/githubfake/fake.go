// Package githubfake is a hand-written stand-in for githubapi.Client in other packages' tests: each
// request answers from a sequence programmed for it (so a poller sees pending, then pass, with no
// time passing), the last answer repeating; every call is recorded, the writes also with what
// they sent. A request nothing was programmed for answers NoAnswerError.
package githubfake

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/zima-oss/jackstay/internal/githubapi"
)

// Result is one programmed answer.
type Result[Value any] struct {
	Value Value
	Err   error
}

// Ok is an answer with a value.
func Ok[Value any](value Value) Result[Value] { return Result[Value]{Value: value} }

// Fail is an answer with an error.
func Fail[Value any](err error) Result[Value] { return Result[Value]{Err: err} }

// NoAnswerError is a request the test did not program.
type NoAnswerError struct{ Op, Key string }

func (err *NoAnswerError) Error() string {
	return fmt.Sprintf("githubfake: no answer programmed for %s %s", err.Op, err.Key)
}

// Call is one call made of the fake.
type Call struct {
	Op  string // the method's name
	Key string // its arguments that choose the answer
}

// BodyEdit is a recorded EditPullRequestBody.
type BodyEdit struct {
	Number int
	Body   string
}

// MergeCall is a recorded Merge.
type MergeCall struct {
	Number         int
	Method         string
	HeadSHA        string
	Title, Message string
}

// Fake answers as githubapi.Client does, from programmed sequences.
type Fake struct {
	mutex   sync.Mutex
	answers map[string][]any
	served  map[string]int
	calls   []Call
	created []githubapi.NewPullRequest
	edits   []BodyEdit
	merges  []MergeCall
	queued  []EnqueueCall
	// dequeued and dequeueErr are Dequeue's record and answer.
	dequeued   []string
	dequeueErr error
}

// EnqueueCall is a recorded Enqueue.
type EnqueueCall struct {
	PullRequestID, ExpectedHead string
}

// New is a fake with nothing programmed.
func New() *Fake { return &Fake{answers: map[string][]any{}, served: map[string]int{}} }

func program[Value any](fake *Fake, method, key string, answers []Result[Value]) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	for _, answer := range answers {
		fake.answers[method+" "+key] = append(fake.answers[method+" "+key], answer)
	}
}

func next[Value any](fake *Fake, method, key string) (Value, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.calls = append(fake.calls, Call{Op: method, Key: key})
	request := method + " " + key
	sequence := fake.answers[request]
	if len(sequence) == 0 {
		var zero Value
		return zero, &NoAnswerError{Op: method, Key: key}
	}
	index := min(fake.served[request], len(sequence)-1)
	fake.served[request]++
	answer, _ := sequence[index].(Result[Value])
	return answer.Value, answer.Err
}

func headKey(head, state string) string { return head + " " + state }

func filterKey(filter githubapi.RunFilter) string {
	return fmt.Sprintf("workflow=%s branch=%s sha=%s event=%s", filter.Workflow, filter.Branch, filter.HeadSHA, filter.Event)
}

func number(value int64) string { return strconv.FormatInt(value, 10) }

// OnPullRequestsByHead programs PullRequestsByHead(head, state).
func (fake *Fake) OnPullRequestsByHead(head, state string, answers ...Result[[]githubapi.PullRequest]) {
	program(fake, "PullRequestsByHead", headKey(head, state), answers)
}

// OnPullRequest programs PullRequest(numberOfPull).
func (fake *Fake) OnPullRequest(numberOfPull int, answers ...Result[githubapi.PullRequest]) {
	program(fake, "PullRequest", strconv.Itoa(numberOfPull), answers)
}

// OnSearchPullRequestsBySHA programs SearchPullRequestsBySHA(sha).
func (fake *Fake) OnSearchPullRequestsBySHA(sha string, answers ...Result[[]githubapi.PullRequest]) {
	program(fake, "SearchPullRequestsBySHA", sha, answers)
}

// OnChecks programs Checks(sha).
func (fake *Fake) OnChecks(sha string, answers ...Result[[]githubapi.Check]) {
	program(fake, "Checks", sha, answers)
}

// OnCreatePullRequest programs CreatePullRequest for a head branch.
func (fake *Fake) OnCreatePullRequest(head string, answers ...Result[githubapi.PullRequest]) {
	program(fake, "CreatePullRequest", head, answers)
}

// OnEditPullRequestBody programs EditPullRequestBody(numberOfPull): nil for success.
func (fake *Fake) OnEditPullRequestBody(numberOfPull int, errs ...error) {
	answers := make([]Result[struct{}], 0, len(errs))
	for _, err := range errs {
		answers = append(answers, Result[struct{}]{Err: err})
	}
	program(fake, "EditPullRequestBody", strconv.Itoa(numberOfPull), answers)
}

// OnMerge programs Merge(numberOfPull).
func (fake *Fake) OnMerge(numberOfPull int, answers ...Result[githubapi.MergeResult]) {
	program(fake, "Merge", strconv.Itoa(numberOfPull), answers)
}

// OnEnqueue programs Enqueue(pullRequestID): nil for success.
func (fake *Fake) OnEnqueue(pullRequestID string, errs ...error) {
	answers := make([]Result[struct{}], 0, len(errs))
	for _, err := range errs {
		answers = append(answers, Result[struct{}]{Err: err})
	}
	program(fake, "Enqueue", pullRequestID, answers)
}

// OnWorkflowRuns programs WorkflowRuns(filter).
func (fake *Fake) OnWorkflowRuns(filter githubapi.RunFilter, answers ...Result[[]githubapi.WorkflowRun]) {
	program(fake, "WorkflowRuns", filterKey(filter), answers)
}

// OnJobs programs Jobs(runID).
func (fake *Fake) OnJobs(runID int64, answers ...Result[[]githubapi.Job]) {
	program(fake, "Jobs", number(runID), answers)
}

// OnJobLog programs JobLog(jobID).
func (fake *Fake) OnJobLog(jobID int64, answers ...Result[string]) {
	program(fake, "JobLog", number(jobID), answers)
}

// OnDefaultBranch programs DefaultBranch.
func (fake *Fake) OnDefaultBranch(answers ...Result[string]) {
	program(fake, "DefaultBranch", "", answers)
}

// PullRequestsByHead answers as githubapi.Client.PullRequestsByHead.
func (fake *Fake) PullRequestsByHead(_ context.Context, head, state string) ([]githubapi.PullRequest, error) {
	return next[[]githubapi.PullRequest](fake, "PullRequestsByHead", headKey(head, state))
}

// PullRequest answers as githubapi.Client.PullRequest.
func (fake *Fake) PullRequest(_ context.Context, numberOfPull int) (githubapi.PullRequest, error) {
	return next[githubapi.PullRequest](fake, "PullRequest", strconv.Itoa(numberOfPull))
}

// SearchPullRequestsBySHA answers as githubapi.Client.SearchPullRequestsBySHA.
func (fake *Fake) SearchPullRequestsBySHA(_ context.Context, sha string) ([]githubapi.PullRequest, error) {
	return next[[]githubapi.PullRequest](fake, "SearchPullRequestsBySHA", sha)
}

// Checks answers as githubapi.Client.Checks.
func (fake *Fake) Checks(_ context.Context, sha string) ([]githubapi.Check, error) {
	return next[[]githubapi.Check](fake, "Checks", sha)
}

// CreatePullRequest records the request and answers as githubapi.Client.CreatePullRequest.
func (fake *Fake) CreatePullRequest(_ context.Context, request githubapi.NewPullRequest) (githubapi.PullRequest, error) {
	fake.mutex.Lock()
	fake.created = append(fake.created, request)
	fake.mutex.Unlock()
	return next[githubapi.PullRequest](fake, "CreatePullRequest", request.Head)
}

// EditPullRequestBody records the edit and answers as githubapi.Client.EditPullRequestBody.
func (fake *Fake) EditPullRequestBody(_ context.Context, numberOfPull int, body string) error {
	fake.mutex.Lock()
	fake.edits = append(fake.edits, BodyEdit{Number: numberOfPull, Body: body})
	fake.mutex.Unlock()
	_, err := next[struct{}](fake, "EditPullRequestBody", strconv.Itoa(numberOfPull))
	return err
}

// Merge records the merge and answers as githubapi.Client.Merge.
func (fake *Fake) Merge(_ context.Context, numberOfPull int, method, headSHA, title, body string) (githubapi.MergeResult, error) {
	fake.mutex.Lock()
	fake.merges = append(fake.merges, MergeCall{Number: numberOfPull, Method: method, HeadSHA: headSHA, Title: title, Message: body})
	fake.mutex.Unlock()
	return next[githubapi.MergeResult](fake, "Merge", strconv.Itoa(numberOfPull))
}

// Enqueue records the request and answers as githubapi.Client.Enqueue.
func (fake *Fake) Enqueue(_ context.Context, pullRequestID, expectedHead string) error {
	fake.mutex.Lock()
	fake.queued = append(fake.queued, EnqueueCall{PullRequestID: pullRequestID, ExpectedHead: expectedHead})
	fake.mutex.Unlock()
	_, err := next[struct{}](fake, "Enqueue", pullRequestID)
	return err
}

// Dequeue records the request and answers nil, or as OnDequeue programs it.
func (fake *Fake) Dequeue(_ context.Context, pullRequestID string) error {
	fake.mutex.Lock()
	fake.dequeued = append(fake.dequeued, pullRequestID)
	fake.mutex.Unlock()
	return fake.dequeueErr
}

// OnDequeue makes every Dequeue answer err.
func (fake *Fake) OnDequeue(err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.dequeueErr = err
}

// Dequeues is every Dequeue's pull request ID, in order.
func (fake *Fake) Dequeues() []string {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]string(nil), fake.dequeued...)
}

// WorkflowRuns answers as githubapi.Client.WorkflowRuns.
func (fake *Fake) WorkflowRuns(_ context.Context, filter githubapi.RunFilter) ([]githubapi.WorkflowRun, error) {
	return next[[]githubapi.WorkflowRun](fake, "WorkflowRuns", filterKey(filter))
}

// Jobs answers as githubapi.Client.Jobs.
func (fake *Fake) Jobs(_ context.Context, runID int64) ([]githubapi.Job, error) {
	return next[[]githubapi.Job](fake, "Jobs", number(runID))
}

// JobLog answers as githubapi.Client.JobLog.
func (fake *Fake) JobLog(_ context.Context, jobID int64) (string, error) {
	return next[string](fake, "JobLog", number(jobID))
}

// DefaultBranch answers as githubapi.Client.DefaultBranch.
func (fake *Fake) DefaultBranch(context.Context) (string, error) {
	return next[string](fake, "DefaultBranch", "")
}

// Calls is every call made, in order.
func (fake *Fake) Calls() []Call {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]Call(nil), fake.calls...)
}

// Created is every CreatePullRequest's request, in order.
func (fake *Fake) Created() []githubapi.NewPullRequest {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]githubapi.NewPullRequest(nil), fake.created...)
}

// BodyEdits is every EditPullRequestBody, in order.
func (fake *Fake) BodyEdits() []BodyEdit {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]BodyEdit(nil), fake.edits...)
}

// Enqueues is every Enqueue, in order.
func (fake *Fake) Enqueues() []EnqueueCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]EnqueueCall(nil), fake.queued...)
}

// Merges is every Merge, in order.
func (fake *Fake) Merges() []MergeCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]MergeCall(nil), fake.merges...)
}
