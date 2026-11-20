package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/hosting"
)

// Connect is GitHub for repository (owner/name) as account (github.account; "" when unset), as
// the hosting adapter `jst land` uses; the token is returned for its note.
func Connect(ctx context.Context, account, repository string) (hosting.Host, githubapi.Token, error) {
	client, token, err := connectReal(ctx, account, repository)
	if err != nil {
		return nil, githubapi.Token{}, err
	}
	return newHost(client, repository), token, nil
}

// host is GitHub as a hosting.Host and a hosting.Explainer, for one repository.
type host struct {
	github     GitHub
	repository string
	// runs is the workflow run behind each check Checks returned, for Explain.
	mutex sync.Mutex
	runs  map[checkKey]int64
}

// checkKey names a check Checks returned: its name, the commit it ran on, and its link.
type checkKey struct {
	name, commit, link string
}

var (
	_ hosting.Host      = (*host)(nil)
	_ hosting.Explainer = (*host)(nil)
)

func newHost(client GitHub, repository string) *host {
	return &host{github: client, repository: repository, runs: map[checkKey]int64{}}
}

func (*host) Name() string { return "github" }

func (adapter *host) DefaultBranch(ctx context.Context) (string, error) {
	return adapter.github.DefaultBranch(ctx)
}

// changeOf is jackstay's hosting view of a pull request.
func changeOf(pull githubapi.PullRequest) hosting.Change {
	change := hosting.Change{Number: pull.Number, URL: pull.URL, Title: pull.Title, Body: pull.Body, State: pull.State,
		Draft: pull.Draft, Head: pull.Head, HeadSHA: pull.HeadSHA, HeadRepository: pull.HeadRepository,
		Base: pull.Base, BaseSHA: pull.BaseSHA, BaseRepository: pull.BaseRepository,
		Mergeable: "unknown", Behind: strings.EqualFold(pull.MergeableState, "behind")}
	if pull.Merged {
		change.State = "merged"
	}
	if change.State == "open" {
		change.TestMerge = pull.MergeCommitSHA
	}
	switch pull.Mergeable {
	case "MERGEABLE":
		change.Mergeable = "yes"
	case "CONFLICTING":
		change.Mergeable = "no"
	}
	return change
}

func (adapter *host) Change(ctx context.Context, ref hosting.ChangeRef) (hosting.Change, bool, error) {
	if ref.Number > 0 {
		pull, err := adapter.github.PullRequest(ctx, ref.Number)
		if err != nil {
			return hosting.Change{}, false, fmt.Errorf("pull request #%d: %w", ref.Number, err)
		}
		return changeOf(pull), true, nil
	}
	if ref.Head == "" {
		return hosting.Change{}, false, errors.New("a change is selected by its number or its head branch")
	}
	pull, found, err := openPullOf(ctx, adapter.github, adapter.repository, ref.Head, "")
	if err != nil || !found {
		return hosting.Change{}, false, err
	}
	// The list's answer leaves out mergeability; the pull request itself has it.
	pull, err = adapter.github.PullRequest(ctx, pull.Number)
	if err != nil {
		return hosting.Change{}, false, fmt.Errorf("pull request #%d: %w", pull.Number, err)
	}
	return changeOf(pull), true, nil
}

func (adapter *host) OpenChanges(ctx context.Context, head string) ([]hosting.Change, error) {
	pulls, err := openPulls(ctx, adapter.github, adapter.repository, head)
	if err != nil {
		return nil, err
	}
	changes := make([]hosting.Change, 0, len(pulls))
	for _, pull := range pulls {
		changes = append(changes, changeOf(pull))
	}
	return changes, nil
}

// Open opens the pull request. A write is not retried: an answer lost after it is settled by
// looking for the pull request again.
func (adapter *host) Open(ctx context.Context, request hosting.NewChange) (hosting.Change, error) {
	created, err := adapter.github.CreatePullRequest(ctx, githubapi.NewPullRequest{Head: request.Head, Base: request.Base,
		Title: request.Title, Body: request.Body})
	if err != nil {
		found, open, lookErr := openPullOf(ctx, adapter.github, adapter.repository, request.Head, request.Base)
		if lookErr != nil || !open {
			return hosting.Change{}, fmt.Errorf("the pull request could not be opened: %w", err)
		}
		created = found
	}
	return changeOf(created), nil
}

// stateOf is a bucket as a hosting state.
var stateOf = map[githubapi.Bucket]hosting.State{
	githubapi.Pass: hosting.Pass, githubapi.Fail: hosting.Fail, githubapi.Pending: hosting.Pending,
	githubapi.Skipping: hosting.Skipped, githubapi.Cancel: hosting.Cancelled,
}

// Checks are the rows on the head and, once the test merge commit has any, its rows too; nothing
// is dropped, since the caller decides which commit is judged.
func (adapter *host) Checks(ctx context.Context, change hosting.Change) ([]hosting.Check, error) {
	headRows, err := adapter.github.Checks(ctx, change.HeadSHA)
	if err != nil {
		return nil, fmt.Errorf("the checks of %s: %w", short(change.HeadSHA, 8), err)
	}
	checks, err := adapter.checksOf(ctx, headRows, change.HeadSHA, false, change.Number)
	if err != nil {
		return nil, err
	}
	if change.TestMerge == "" {
		return checks, nil
	}
	mergeRows, err := adapter.github.Checks(ctx, change.TestMerge)
	var notFound *githubapi.CommitNotFoundError
	switch {
	case errors.As(err, &notFound):
		return checks, nil
	case err != nil:
		return nil, fmt.Errorf("the checks of the test merge commit %s: %w", short(change.TestMerge, 8), err)
	case len(mergeRows) == 0:
		return checks, nil
	}
	onMerge, err := adapter.checksOf(ctx, mergeRows, change.TestMerge, true, change.Number)
	if err != nil {
		return nil, err
	}
	return append(checks, onMerge...), nil
}

// checksOf is one commit's rows, superseded runs replaced, as hosting checks; each check's run is
// kept for Explain.
func (adapter *host) checksOf(ctx context.Context, rows []githubapi.Check, commit string, onTestMerge bool, pullNumber int) ([]hosting.Check, error) {
	result, err := supersede(ctx, adapter.github, rows, commit, pullNumber)
	if err != nil {
		// Whether a cancelled run was superseded cannot be told: the rows stand as read, each
		// cancelled one saying why, so an optional cancellation never stops a landing by itself.
		result = superseded{rows: rows, rowNotes: make([]string, len(rows))}
		for index, row := range rows {
			if row.Bucket() == githubapi.Cancel {
				result.rowNotes[index] = fmt.Sprintf("whether a newer run supersedes it cannot be told: %v", err)
			}
		}
	}
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	checks := make([]hosting.Check, 0, len(result.rows))
	for index, row := range result.rows {
		check := hosting.Check{Name: row.Name, State: stateOf[row.Bucket()], Commit: commit, OnTestMerge: onTestMerge,
			Link: row.Link, Workflow: row.Workflow, Note: result.rowNotes[index]}
		if row.Kind == githubapi.CheckRun {
			check.App = row.AppID
		}
		adapter.runs[checkKey{name: check.Name, commit: commit, link: check.Link}] = row.RunID
		checks = append(checks, check)
	}
	return checks, nil
}

// Explain is, for each failed check, its failed step's log.
func (adapter *host) Explain(ctx context.Context, failed []hosting.Check, lines int) string {
	adapter.mutex.Lock()
	rows := make([]githubapi.Check, 0, len(failed))
	for _, check := range failed {
		if check.State != hosting.Fail {
			continue
		}
		rows = append(rows, githubapi.Check{Name: check.Name, Kind: githubapi.CheckRun, Status: "COMPLETED", Conclusion: "FAILURE",
			Link: check.Link, Workflow: check.Workflow, RunID: adapter.runs[checkKey{name: check.Name, commit: check.Commit, link: check.Link}]})
	}
	adapter.mutex.Unlock()
	return strings.TrimPrefix(failureDetail(ctx, adapter.github, rows, lines), "\n")
}

// Rules are the branch's rules (rulesets and classic protection) and the repository's merge
// methods. A request GitHub refuses (403 or 404: a plan or a permission) leaves that part unread,
// saying why; another failure is an error.
func (adapter *host) Rules(ctx context.Context, branch string) (hosting.Rules, error) {
	var rules hosting.Rules
	var unread []string
	branchRules, err := adapter.github.BranchRules(ctx, branch)
	switch {
	case refused(err):
		unread = append(unread, fmt.Sprintf("GitHub refused to show the rules of %s (%v): the repository's plan or "+
			"this account's permission keeps them hidden", branch, err))
	case err != nil:
		return hosting.Rules{}, err
	default:
		rules.ChecksRead, rules.UpToDateRead, rules.QueueRead = true, true, true
		rules.UpToDate, rules.Queue = branchRules.UpToDate, branchRules.Queue
		for _, check := range branchRules.Required {
			rules.Required = append(rules.Required, hosting.RequiredCheck{Name: check.Name, App: check.AppID})
		}
	}
	methods, read, err := adapter.github.MergeMethods(ctx)
	switch {
	case refused(err):
		unread = append(unread, fmt.Sprintf("GitHub refused to show the merge methods %s allows (%v)", adapter.repository, err))
	case err != nil:
		return hosting.Rules{}, fmt.Errorf("the merge methods of %s: %w", adapter.repository, err)
	case !read:
		unread = append(unread, fmt.Sprintf("GitHub does not show the merge methods %s allows to this account "+
			"(it shows them to an account that may administer the repository)", adapter.repository))
	default:
		rules.MethodsRead = true
		for _, method := range methods {
			rules.Methods = append(rules.Methods, hosting.Method(method))
		}
	}
	rules.Unread = strings.Join(unread, "; ")
	return rules, nil
}

// refused is a request GitHub answered 403 or 404: a plan or a permission, not a failure.
func refused(err error) bool {
	status := githubapi.StatusOf(err)
	return err != nil && (status == http.StatusForbidden || status == http.StatusNotFound)
}

func (adapter *host) UpdateBranch(ctx context.Context, change hosting.Change, expectedHead string) error {
	return adapter.github.UpdateBranch(ctx, change.Number, expectedHead)
}

// Merge asks GitHub once to merge at the expected head; a lost answer is the caller's to settle by
// reading the change.
func (adapter *host) Merge(ctx context.Context, change hosting.Change, request hosting.MergeRequest) (hosting.Merged, error) {
	switch request.Method {
	case hosting.Squash, hosting.Merge, hosting.Rebase:
	default:
		return hosting.Merged{}, fmt.Errorf("the merge method %q is not one GitHub has (squash, merge, rebase)", request.Method)
	}
	if request.ExpectedHead == "" {
		return hosting.Merged{}, errors.New("a merge needs the head it expects")
	}
	result, err := adapter.github.Merge(ctx, change.Number, string(request.Method), request.ExpectedHead, request.Title, request.Body)
	if err != nil {
		return hosting.Merged{}, err
	}
	return hosting.Merged{Merged: result.Merged, SHA: result.SHA}, nil
}

// Enqueue puts the pull request in its base's merge queue at expectedHead (GraphQL
// enqueuePullRequest, by the pull request's node id).
func (adapter *host) Enqueue(ctx context.Context, change hosting.Change, expectedHead string) error {
	pull, err := adapter.github.PullRequest(ctx, change.Number)
	if err != nil {
		return fmt.Errorf("pull request #%d: %w", change.Number, err)
	}
	return adapter.github.Enqueue(ctx, pull.NodeID, expectedHead)
}
