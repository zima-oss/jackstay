package reviewpool

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
)

// SubscriptionOverview lists one job's opaque chat recipients without exposing
// its repository, task, or reviewer output.
type SubscriptionOverview struct {
	JobID       string             `json:"job_id"`
	Generation  string             `json:"generation"`
	Status      string             `json:"status"`
	Subscribers []SubscriptionView `json:"subscribers"`
}

// SubscriptionView reports the delivery state for one opaque recipient.
type SubscriptionView struct {
	SessionID    string `json:"session_id"`
	Closed       bool   `json:"closed"`
	Undelivered  int    `json:"undelivered"`
	Materialized int    `json:"materialized"`
}

// SubscriptionClosure is the read-only plan or committed outcome of closing
// one recipient's future queue notifications. Already accepted chat messages
// remain in that recipient's inbox until acknowledged there.
type SubscriptionClosure struct {
	JobID         string `json:"job_id"`
	SessionID     string `json:"session_id"`
	Generation    string `json:"generation"`
	AlreadyClosed bool   `json:"already_closed"`
	Undelivered   int    `json:"undelivered"`
	Materialized  int    `json:"materialized"`
	Applied       bool   `json:"applied"`
}

func subscriptionGeneration(job Job) string {
	if job.Generation == "" {
		return "legacy"
	}
	return job.Generation
}

// ListSubscriptions shows the exact recipient identities and delivery state
// required to preview a closure. The read never creates queue state.
func (pool Pool) ListSubscriptions(ctx context.Context, jobID string) (SubscriptionOverview, error) {
	if err := ctx.Err(); err != nil {
		return SubscriptionOverview{}, err
	}
	if jobID == "" {
		return SubscriptionOverview{}, errors.New("job ID is required")
	}
	saved, err := pool.load()
	if err != nil {
		return SubscriptionOverview{}, err
	}
	job, found := saved.findSubscriptionJob(jobID)
	if !found {
		return SubscriptionOverview{}, errors.New("review job not found")
	}
	view := SubscriptionOverview{JobID: job.ID, Generation: subscriptionGeneration(job), Status: job.Status,
		Subscribers: []SubscriptionView{}}
	for _, subscriber := range saved.Subscriptions {
		if subscriber.JobID != jobID {
			continue
		}
		view.Subscribers = append(view.Subscribers, saved.subscriptionView(subscriber))
	}
	slices.SortFunc(view.Subscribers, func(left, right SubscriptionView) int {
		if left.SessionID < right.SessionID {
			return -1
		}
		if left.SessionID > right.SessionID {
			return 1
		}
		return 0
	})
	return view, nil
}

// CloseSubscription previews or atomically closes one job recipient. Applying
// requires the generation shown by ListSubscriptions, so a preview cannot
// accidentally close a newly queued generation with the same deterministic ID.
func (pool Pool) CloseSubscription(ctx context.Context, jobID, sessionID, expectedGeneration string, apply bool) (SubscriptionClosure, error) {
	if err := ctx.Err(); err != nil {
		return SubscriptionClosure{}, err
	}
	if jobID == "" || expectedGeneration == "" {
		return SubscriptionClosure{}, errors.New("job ID and generation are required")
	}
	if (len(sessionID) != 48 && len(sessionID) != 64) || !validSubscriptionSessionID(sessionID) {
		return SubscriptionClosure{}, errors.New("invalid session identity")
	}
	if !apply {
		saved, err := pool.load()
		if err != nil {
			return SubscriptionClosure{}, err
		}
		_, plan, err := saved.closeSubscriptionPlan(jobID, sessionID, expectedGeneration)
		return plan, err
	}
	var plan SubscriptionClosure
	err := pool.editWhen(ctx, func(saved *state) (bool, error) {
		index, current, err := saved.closeSubscriptionPlan(jobID, sessionID, expectedGeneration)
		if err != nil {
			return false, err
		}
		plan = current
		if plan.AlreadyClosed {
			return false, nil
		}
		saved.Subscriptions[index].Closed = true
		return true, nil
	})
	if err == nil && !plan.AlreadyClosed {
		plan.Applied = true
	}
	return plan, err
}

func validSubscriptionSessionID(sessionID string) bool {
	_, err := hex.DecodeString(sessionID)
	return err == nil
}

func (saved state) closeSubscriptionPlan(jobID, sessionID, expectedGeneration string) (int, SubscriptionClosure, error) {
	job, found := saved.findSubscriptionJob(jobID)
	if !found {
		return -1, SubscriptionClosure{}, errors.New("review job not found")
	}
	generation := subscriptionGeneration(job)
	if expectedGeneration != generation {
		return -1, SubscriptionClosure{}, errors.New("review job generation changed; inspect subscribers again")
	}
	for index, subscriber := range saved.Subscriptions {
		if subscriber.JobID == jobID && subscriber.SessionID == sessionID {
			view := saved.subscriptionView(subscriber)
			return index, SubscriptionClosure{JobID: jobID, SessionID: sessionID, Generation: generation,
				AlreadyClosed: subscriber.Closed, Undelivered: view.Undelivered,
				Materialized: view.Materialized}, nil
		}
	}
	return -1, SubscriptionClosure{}, errors.New("review subscription not found")
}

func (saved state) findSubscriptionJob(jobID string) (Job, bool) {
	for _, job := range saved.Jobs {
		if job.ID == jobID {
			return job, true
		}
	}
	return Job{}, false
}

func (saved state) subscriptionView(subscriber subscription) SubscriptionView {
	view := SubscriptionView{SessionID: subscriber.SessionID, Closed: subscriber.Closed}
	for _, outcome := range saved.Events {
		if outcome.JobID != subscriber.JobID {
			continue
		}
		if slices.Contains(outcome.Delivered, subscriber.SessionID) {
			view.Materialized++
		} else if !subscriber.Closed {
			view.Undelivered++
		}
	}
	return view
}
