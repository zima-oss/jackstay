package reviewpool

import (
	"context"
	"encoding/hex"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/telemetry"
)

// Overview summarizes the local pool without exposing repository paths, task names,
// session identities, or reviewer output. Ready describes queue eligibility only;
// an active worker may still hold the worker lock.
type Overview struct {
	Version                 int                `json:"version"`
	Total                   int                `json:"total"`
	Pending                 int                `json:"pending"`
	Running                 int                `json:"running"`
	Complete                int                `json:"complete"`
	Failed                  int                `json:"failed"`
	Eligible                int                `json:"eligible"`
	Delayed                 int                `json:"delayed"`
	Recoverable             int                `json:"recoverable"`
	Unresolved              int                `json:"unresolved"`
	Ready                   bool               `json:"ready"`
	OldestPendingAt         *time.Time         `json:"oldest_pending_at,omitempty"`
	OldestPendingAgeSeconds int64              `json:"oldest_pending_age_seconds,omitempty"`
	Outcomes                OutcomeCounts      `json:"outcomes"`
	Notifications           NotificationCounts `json:"notifications"`
}

// OutcomeCounts separates terminal review verdicts from execution failures.
type OutcomeCounts struct {
	Clear     int `json:"clear"`
	Blocks    int `json:"blocks"`
	Redesign  int `json:"redesign"`
	Owner     int `json:"owner"`
	Other     int `json:"other"`
	Retryable int `json:"retryable_failure"`
	Operator  int `json:"operator_failure"`
}

// NotificationCounts counts durable handoffs to subscribed sessions. A
// materialized message remains in session chat until its recipient calls chat.ack.
type NotificationCounts struct {
	Undelivered  int `json:"undelivered"`
	Materialized int `json:"materialized"`
}

// Inspect reads a bounded state snapshot without acquiring or creating a lock.
// The supplied clock makes eligibility and age deterministic for callers.
func (pool Pool) Inspect(ctx context.Context, now time.Time) (Overview, error) {
	if err := ctx.Err(); err != nil {
		return Overview{}, err
	}
	saved, err := pool.load()
	if err != nil {
		return Overview{}, err
	}
	view := Overview{Version: saved.Version, Total: len(saved.Jobs)}
	for _, job := range saved.Jobs {
		switch job.Status {
		case "pending":
			view.Pending++
			if now.Before(job.NotBefore) {
				view.Delayed++
			} else {
				view.Eligible++
			}
			if !job.CreatedAt.IsZero() && (view.OldestPendingAt == nil || job.CreatedAt.Before(*view.OldestPendingAt)) {
				created := job.CreatedAt.UTC()
				view.OldestPendingAt = &created
			}
		case "running":
			view.Running++
			if job.FailureClass == "unresolved" {
				view.Unresolved++
			} else {
				view.Recoverable++
			}
		case "complete":
			view.Complete++
			switch job.Exit {
			case review.ExitClear:
				view.Outcomes.Clear++
			case review.ExitBlocks:
				view.Outcomes.Blocks++
			case review.ExitRedesign:
				view.Outcomes.Redesign++
			case review.ExitOwner:
				view.Outcomes.Owner++
			default:
				view.Outcomes.Other++
			}
		case "failed":
			view.Failed++
			if job.FailureClass == "retryable" {
				view.Outcomes.Retryable++
			} else {
				view.Outcomes.Operator++
			}
		}
	}
	view.Ready = view.Eligible > 0 || view.Recoverable > 0
	if view.OldestPendingAt != nil && now.After(*view.OldestPendingAt) {
		view.OldestPendingAgeSeconds = int64(now.Sub(*view.OldestPendingAt).Seconds())
	}
	subscribers := make(map[string][]string, len(saved.Subscriptions))
	for _, subscription := range saved.Subscriptions {
		subscribers[subscription.JobID] = append(subscribers[subscription.JobID], subscription.SessionID)
	}
	for _, item := range saved.Events {
		for _, sessionID := range subscribers[item.JobID] {
			if slices.Contains(item.Delivered, sessionID) {
				view.Notifications.Materialized++
			} else {
				view.Notifications.Undelivered++
			}
		}
	}
	return view, nil
}

// recordPoolTransition adds a privacy-preserving event to the current command
// trace. With telemetry disabled it has no side effect.
func recordPoolTransition(job Job, action string) {
	switch action {
	case "enqueue", "retry", "start", "finish", "reconcile":
	default:
		return
	}
	if len(job.ID) != 24 {
		return
	}
	if _, err := hex.DecodeString(job.ID); err != nil {
		return
	}
	switch job.Status {
	case "pending", "running", "complete", "failed":
	default:
		return
	}
	telemetry.Note(telemetry.EventReviewPoolTransition,
		attribute.String(telemetry.AttrReviewPoolJob, job.ID),
		attribute.String(telemetry.AttrReviewPoolAction, action),
		attribute.String(telemetry.AttrReviewPoolState, job.Status),
		attribute.Int(telemetry.AttrReviewPoolAttempts, job.Attempts),
		attribute.Int(telemetry.AttrReviewPoolExit, job.Exit),
	)
}
