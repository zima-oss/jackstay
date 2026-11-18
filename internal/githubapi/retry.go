package githubapi

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"github.com/google/go-github/v93/github"
)

// Retry is how a read is made again after a failure that may pass (docs/design/implementation.md,
// sections 8.1 and 10, "Practices": exponential backoff with
// ±50% jitter, bounded by the context's deadline; no retry library). The clock, the sleep and the
// jitter are fields so that tests run without time (W-013).
type Retry struct {
	Attempts int           // calls in all, the first included
	Base     time.Duration // the first wait; each next one doubles
	Now      func() time.Time
	Sleep    func(ctx context.Context, wait time.Duration) error
	Jitter   func(limit int64) int64 // a number in [0, limit)
}

// DefaultRetry is four calls, waiting about 1, 2 and 4 seconds, on the real clock.
func DefaultRetry() Retry {
	return Retry{Attempts: 4, Base: time.Second, Now: time.Now, Sleep: sleep,
		Jitter: func(limit int64) int64 { return rand.Int64N(limit) }} // #nosec G404 -- jitter, not a secret
}

func sleep(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RateLimitedError is a rate limit that lasts past the call's deadline: exit 1, naming the time.
type RateLimitedError struct {
	Until     time.Time
	Secondary bool // GitHub's secondary limit (Retry-After) rather than the hourly one
}

func (err *RateLimitedError) Error() string {
	which := "rate limit"
	if err.Secondary {
		which = "secondary rate limit"
	}
	return fmt.Sprintf("GitHub's %s holds until %s, after this call's deadline", which, err.Until.Local().Format("15:04:05"))
}

// Code is the exit status the error means.
func (*RateLimitedError) Code() int { return 1 }

// downloadError is a download that answered with an error status.
type downloadError struct{ status int }

func (err *downloadError) Error() string { return fmt.Sprintf("the download answered %d", err.status) }

// transient says whether a read's error may pass if it is made again: a server error or the
// network. Rate limits are waited out separately (Read).
func transient(err error) bool {
	var responseErr *github.ErrorResponse
	if errors.As(err, &responseErr) && responseErr.Response != nil {
		return responseErr.Response.StatusCode >= http.StatusInternalServerError
	}
	var failedDownload *downloadError
	if errors.As(err, &failedDownload) {
		return failedDownload.status >= http.StatusInternalServerError
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// Read makes a read (a REST GET or a read-only GraphQL query; never a write) again after a server
// error or a network failure, waiting Base·2ⁿ with ±50% jitter; a primary rate limit is waited out
// until its reset and a secondary one for its Retry-After, when that is inside the context's
// deadline, else the answer is a RateLimitedError.
func Read[Result any](ctx context.Context, policy Retry, call func() (Result, error)) (Result, error) {
	var result Result
	var err error
	for attempt := range policy.Attempts {
		result, err = call()
		if err == nil || attempt == policy.Attempts-1 {
			return result, err
		}
		wait, ok, limited := policy.waitAfter(err, attempt)
		if !ok {
			return result, err
		}
		if deadline, has := ctx.Deadline(); has && policy.Now().Add(wait).After(deadline) {
			if limited != nil {
				return result, errors.Join(limited, err)
			}
			return result, err
		}
		if sleepErr := policy.Sleep(ctx, wait); sleepErr != nil {
			return result, errors.Join(err, sleepErr)
		}
	}
	return result, err
}

// waitAfter is how long to wait before the next call, whether to make one, and the error to give
// when a rate limit outlasts the deadline.
func (policy Retry) waitAfter(err error, attempt int) (time.Duration, bool, *RateLimitedError) {
	var primary *github.RateLimitError
	if errors.As(err, &primary) {
		until := primary.Rate.Reset.Time
		return max(until.Sub(policy.Now()), 0), true, &RateLimitedError{Until: until}
	}
	var secondary *github.AbuseRateLimitError
	if errors.As(err, &secondary) {
		wait := secondary.GetRetryAfter()
		if wait <= 0 {
			wait = policy.backoff(attempt)
		}
		return wait, true, &RateLimitedError{Until: policy.Now().Add(wait), Secondary: true}
	}
	if !transient(err) {
		return 0, false, nil
	}
	return policy.backoff(attempt), true, nil
}

func (policy Retry) backoff(attempt int) time.Duration {
	wait := policy.Base << attempt
	if wait <= 0 {
		return 0
	}
	return wait/2 + time.Duration(policy.Jitter(int64(wait)))
}

// ReadPage is Read for a listing call that also answers with GitHub's response (for its pages).
func ReadPage[Item any](ctx context.Context, policy Retry, call func() ([]Item, *github.Response, error)) ([]Item, *github.Response, error) {
	type answer struct {
		items    []Item
		response *github.Response
	}
	got, err := Read(ctx, policy, func() (answer, error) {
		items, response, err := call()
		return answer{items: items, response: response}, err
	})
	return got.items, got.response, err
}
