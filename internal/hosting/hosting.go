// Package hosting is what `jst land` asks of a code host (docs/design/land.md section 2): one
// change request, its checks, the base's rules, and a merge at an expected head. A host that
// lacks an operation answers ErrUnsupported, and the caller says which one and stops; it never
// falls back silently. GitHub's adapter is internal/gh; GitLab and Bitbucket fit the same
// interface and are not built.
package hosting

import (
	"context"
	"errors"
	"fmt"
)

// ErrUnsupported is an operation the host does not offer.
var ErrUnsupported = errors.New("not offered by this host")

// Unsupported is ErrUnsupported naming the operation and the host.
func Unsupported(host, operation string) error {
	return fmt.Errorf("%s: %s is %w", host, operation, ErrUnsupported)
}

// ChangeRef selects a change request: by number, or by its head branch (one open change from it).
type ChangeRef struct {
	Number int
	Head   string
}

// Change is one change request (a pull request, a merge request) as the host sees it now.
type Change struct {
	Number int
	URL    string
	Title  string
	Body   string
	// State is "open", "closed" or "merged".
	State          string
	Draft          bool
	Head           string // the head branch
	HeadSHA        string
	HeadRepository string // owner/name; differs from BaseRepository for a fork
	Base           string
	BaseSHA        string
	BaseRepository string
	// TestMerge is the host's trial merge of head into base while the change is open ("" none).
	TestMerge string
	// Mergeable is "yes", "no" (a conflict) or "unknown" (the host has not computed it yet).
	Mergeable string
	// Behind is true when the host says the base has commits the head lacks.
	Behind bool
}

// Merged reports whether the change is merged.
func (change Change) Merged() bool { return change.State == "merged" }

// NewChange is a change request to open.
type NewChange struct {
	Head, Base, Title, Body string
}

// State is where a check stands.
type State string

// The states of a check. Cancelled is settled and not a pass.
const (
	Pass      State = "pass"
	Fail      State = "fail"
	Pending   State = "pending"
	Skipped   State = "skipping"
	Cancelled State = "cancel"
)

// Settled reports whether the check has finished.
func (state State) Settled() bool { return state != Pending }

// Check is one check on a commit of the change: on its head, or on its test merge.
type Check struct {
	Name string
	// App is the app that reported it, where the host names one (0: none or unknown).
	App   int64
	State State
	// Commit is the commit it ran on; OnTestMerge says that commit is the change's test merge.
	Commit      string
	OnTestMerge bool
	Link        string
	Workflow    string
	// Note says how the host reached this state, when that is not plain (a superseded run).
	Note string
}

// RequiredCheck is a check a base's rules require: its name, and the app that must report it (0: any).
type RequiredCheck struct {
	Name string
	App  int64
}

// SatisfiedBy reports whether a check is this required one.
func (required RequiredCheck) SatisfiedBy(check Check) bool {
	return check.Name == required.Name && (required.App == 0 || check.App == required.App)
}

func (required RequiredCheck) String() string {
	if required.App == 0 {
		return required.Name
	}
	return fmt.Sprintf("%s (app %d)", required.Name, required.App)
}

// Method is a merge method.
type Method string

// The merge methods.
const (
	Squash Method = "squash"
	Merge  Method = "merge"
	Rebase Method = "rebase"
)

// Rules are a base branch's rules, each with whether it could be read. Unread says why one could
// not be (a plan or a permission refused it).
type Rules struct {
	ChecksRead bool
	Required   []RequiredCheck
	// UpToDate: the branch must contain the base's tip before a merge.
	UpToDateRead bool
	UpToDate     bool
	// Queue: merges go through the host's merge queue.
	QueueRead bool
	Queue     bool
	// Methods are the merge methods the repository allows.
	MethodsRead bool
	Methods     []Method
	Unread      string
}

// MergeRequest is a merge of a change at an expected head, with the commit's message where the
// method writes one.
type MergeRequest struct {
	Method       Method
	ExpectedHead string
	Title, Body  string
}

// Merged is the host's answer to a merge.
type Merged struct {
	Merged bool
	SHA    string
}

// Host is a code host as `jst land` uses it.
type Host interface {
	// Name is the host's name in messages ("github").
	Name() string
	// DefaultBranch is the repository's default branch: a new change's base.
	DefaultBranch(ctx context.Context) (string, error)
	// Change is one change: by number, or the one open change from a head branch (found false
	// when there is none; more than one is an error naming them).
	Change(ctx context.Context, ref ChangeRef) (change Change, found bool, err error)
	// OpenChanges are the open changes from a head branch of this repository.
	OpenChanges(ctx context.Context, head string) ([]Change, error)
	Open(ctx context.Context, request NewChange) (Change, error)
	// Checks are the change's checks on its head and, once the host has made one, on its test
	// merge; a run superseded by a newer run of the same workflow, commit, event and change is
	// replaced by the newer run's own job of the same name.
	Checks(ctx context.Context, change Change) ([]Check, error)
	Rules(ctx context.Context, branch string) (Rules, error)
	// UpdateBranch merges the base into the change's branch on the host while its head is
	// expectedHead.
	UpdateBranch(ctx context.Context, change Change, expectedHead string) error
	Merge(ctx context.Context, change Change, request MergeRequest) (Merged, error)
	// Enqueue puts the change in the host's merge queue at expectedHead.
	Enqueue(ctx context.Context, change Change, expectedHead string) error
}

// Explainer is a host that can say why failed checks failed (their failed step's log).
type Explainer interface {
	Explain(ctx context.Context, failed []Check, lines int) string
}
