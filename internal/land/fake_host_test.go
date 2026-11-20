package land

import (
	"context"
	"fmt"
	"time"

	"github.com/zima-oss/jackstay/internal/hosting"
)

// fakeClock is a clock that moves only when jst land sleeps, so no test depends on timing.
type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)} }

func (clock *fakeClock) Now() time.Time { return clock.now }

func (clock *fakeClock) Sleep(_ context.Context, wait time.Duration) error {
	clock.sleeps = append(clock.sleeps, wait)
	clock.now = clock.now.Add(max(wait, time.Millisecond))
	return nil
}

// fakeHost is a code host scripted per test. It holds one change; jst land is single-threaded, so
// the hooks change the host's state directly.
type fakeHost struct {
	defaultBranch string
	// template is the change Open opens (its number, head and base); change is the host's change
	// now, and opened says one is open from the head branch.
	template hosting.Change
	change   hosting.Change
	opened   bool
	openErr  error

	// reads counts the reads of the change by number; beforeRead runs before each one, with its
	// count, so a test can move the head or the base at a chosen read.
	reads      int
	beforeRead func(host *fakeHost, read int)

	// checks answers each Checks call (counted from 1) for the change as it is.
	checkCalls int
	checks     func(call int, change hosting.Change) []hosting.Check

	// rules answer each Rules call in turn; the last repeats.
	ruleCalls int
	rules     []hosting.Rules

	// mergeAnswer answers a merge; nil merges the change.
	merges      []hosting.MergeRequest
	mergeAnswer func(host *fakeHost, request hosting.MergeRequest) (hosting.Merged, error)

	// enqueued are the expected heads Enqueue was asked with; the change merges on the read
	// queueMergesAfter reads after it was enqueued (0: never).
	enqueued         []string
	enqueueErr       error
	enqueuedAtRead   int
	queueMergesAfter int

	// updates are the expected heads UpdateBranch was asked with; onUpdate does the update.
	updates  []string
	onUpdate func(host *fakeHost) error

	opens []hosting.NewChange
	calls []string
}

func newFakeHost(template hosting.Change, rules ...hosting.Rules) *fakeHost {
	return &fakeHost{defaultBranch: "main", template: template, rules: rules}
}

// passing is checks that are the same on every call.
func passing(list ...hosting.Check) func(int, hosting.Change) []hosting.Check {
	return func(_ int, change hosting.Change) []hosting.Check { return onHead(change, list) }
}

// onHead is the checks placed on the change's head commit.
func onHead(change hosting.Change, list []hosting.Check) []hosting.Check {
	out := make([]hosting.Check, 0, len(list))
	for _, each := range list {
		each.Commit = change.HeadSHA
		out = append(out, each)
	}
	return out
}

func (host *fakeHost) record(format string, values ...any) {
	host.calls = append(host.calls, fmt.Sprintf(format, values...))
}

func (host *fakeHost) Name() string { return "fake" }

func (host *fakeHost) DefaultBranch(context.Context) (string, error) {
	host.record("default-branch")
	return host.defaultBranch, nil
}

func (host *fakeHost) Change(_ context.Context, ref hosting.ChangeRef) (hosting.Change, bool, error) {
	if ref.Number == 0 {
		host.record("change head=%s", ref.Head)
		if host.opened && host.change.State == "open" && host.change.Head == ref.Head {
			return host.change, true, nil
		}
		return hosting.Change{}, false, nil
	}
	host.reads++
	host.record("change #%d", ref.Number)
	if host.beforeRead != nil {
		host.beforeRead(host, host.reads)
	}
	if host.queueMergesAfter > 0 && len(host.enqueued) > 0 && host.change.State == "open" &&
		host.reads-host.enqueuedAtRead >= host.queueMergesAfter {
		host.change.State = "merged"
	}
	if !host.opened || ref.Number != host.change.Number {
		return hosting.Change{}, false, nil
	}
	return host.change, true, nil
}

func (host *fakeHost) OpenChanges(_ context.Context, head string) ([]hosting.Change, error) {
	host.record("open-changes %s", head)
	if host.opened && host.change.State == "open" && host.change.Head == head {
		return []hosting.Change{host.change}, nil
	}
	return nil, nil
}

func (host *fakeHost) Open(_ context.Context, request hosting.NewChange) (hosting.Change, error) {
	host.record("open %s into %s", request.Head, request.Base)
	host.opens = append(host.opens, request)
	if host.openErr != nil {
		return hosting.Change{}, host.openErr
	}
	host.change = host.template
	host.change.Title, host.change.Body = request.Title, request.Body
	host.opened = true
	return host.change, nil
}

func (host *fakeHost) Checks(_ context.Context, change hosting.Change) ([]hosting.Check, error) {
	host.checkCalls++
	host.record("checks %s", short(change.HeadSHA, 8))
	if host.checks == nil {
		return nil, nil
	}
	return host.checks(host.checkCalls, change), nil
}

func (host *fakeHost) Rules(_ context.Context, branch string) (hosting.Rules, error) {
	host.ruleCalls++
	host.record("rules %s", branch)
	if len(host.rules) == 0 {
		return hosting.Rules{}, nil
	}
	return host.rules[min(host.ruleCalls, len(host.rules))-1], nil
}

func (host *fakeHost) UpdateBranch(_ context.Context, change hosting.Change, expectedHead string) error {
	host.record("update-branch #%d %s", change.Number, short(expectedHead, 8))
	host.updates = append(host.updates, expectedHead)
	if host.onUpdate == nil {
		return hosting.Unsupported("fake", "update-branch")
	}
	return host.onUpdate(host)
}

func (host *fakeHost) Merge(_ context.Context, change hosting.Change, request hosting.MergeRequest) (hosting.Merged, error) {
	host.record("merge #%d %s %s", change.Number, request.Method, short(request.ExpectedHead, 8))
	host.merges = append(host.merges, request)
	if host.mergeAnswer != nil {
		return host.mergeAnswer(host, request)
	}
	if request.ExpectedHead != host.change.HeadSHA {
		return hosting.Merged{}, fmt.Errorf("head moved: %s is not %s", short(request.ExpectedHead, 8), short(host.change.HeadSHA, 8))
	}
	host.change.State = "merged"
	return hosting.Merged{Merged: true, SHA: "5ca1ab1e00000000000000000000000000000000"}, nil
}

func (host *fakeHost) Enqueue(_ context.Context, change hosting.Change, expectedHead string) error {
	host.record("enqueue #%d %s", change.Number, short(expectedHead, 8))
	if host.enqueueErr != nil {
		return host.enqueueErr
	}
	host.enqueued = append(host.enqueued, expectedHead)
	host.enqueuedAtRead = host.reads
	return nil
}

var _ hosting.Host = (*fakeHost)(nil)
