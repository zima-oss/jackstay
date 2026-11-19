package reviewpool

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/zima-oss/jackstay/internal/strictjson"
)

const (
	legacyStateVersion  = 1
	currentStateVersion = 2
	legacyHistoryDays   = 30
	maximumHistoryDays  = 3650
)

func supportedStateVersion(version int) bool {
	return version == legacyStateVersion || version == currentStateVersion
}

// Prefer an actionable version error when a future writer also adds fields.
func unsupportedVersion(document []byte) error {
	var header struct {
		Version int `json:"version"`
	}
	if err := strictjson.UnmarshalOpen(document, &header); err == nil && !supportedStateVersion(header.Version) {
		return fmt.Errorf("unsupported review pool version %d", header.Version)
	}
	return nil
}

func (saved state) validateHistory() error {
	identities := make(map[string]bool, len(saved.Jobs))
	for _, job := range saved.Jobs {
		if job.ID == "" || identities[job.ID] {
			return errors.New("invalid or repeated review pool job identity")
		}
		if job.HistoryDays < 0 || job.HistoryDays > maximumHistoryDays {
			return errors.New("invalid review pool history retention")
		}
		identities[job.ID] = true
	}
	return nil
}

// Migration reports the stored and current versions without modifying a legacy file.
type Migration struct {
	From    int  `json:"from"`
	To      int  `json:"to"`
	Needed  bool `json:"needed"`
	Applied bool `json:"applied"`
}

// Migrate upgrades version 1 under the state lock when apply is requested.
// Legacy rows keep their original identity and notification keys. A missing
// history policy has the same 30-day interpretation before and after upgrade.
func (pool Pool) Migrate(ctx context.Context, apply bool) (Migration, error) {
	var result Migration
	plan := func(saved *state) (bool, error) {
		needed := saved.Version != currentStateVersion
		result = Migration{From: saved.Version, To: currentStateVersion, Needed: needed}
		return needed, nil
	}
	if !apply {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		saved, err := pool.load()
		if err != nil {
			return result, err
		}
		_, _ = plan(&saved)
		return result, nil
	}
	err := pool.editWhen(ctx, plan)
	result.Applied = err == nil && result.Needed
	return result, err
}

// PruneReport counts eligible and protected history without disclosing job details.
type PruneReport struct {
	Eligible  int `json:"eligible"`
	Protected int `json:"protected"`
	Removed   int `json:"removed"`
}

// Prune removes expired terminal jobs only after their notifications are durably
// materialized in chat. The session chat and review report stores are separate.
func (pool Pool) Prune(ctx context.Context, now time.Time, apply bool) (PruneReport, error) {
	if err := ctx.Err(); err != nil {
		return PruneReport{}, err
	}
	if !apply {
		saved, err := pool.load()
		if err != nil {
			return PruneReport{}, err
		}
		_, report := saved.prunePlan(now)
		return report, nil
	}
	var report PruneReport
	err := pool.editWhen(ctx, func(saved *state) (bool, error) {
		removed, plan := saved.prunePlan(now)
		report = plan
		if len(removed) == 0 {
			return false, nil
		}
		saved.Jobs = slices.DeleteFunc(saved.Jobs, func(job Job) bool { return removed[job.ID] })
		saved.Subscriptions = slices.DeleteFunc(saved.Subscriptions, func(item subscription) bool { return removed[item.JobID] })
		saved.Events = slices.DeleteFunc(saved.Events, func(item event) bool { return removed[item.JobID] })
		return true, nil
	})
	if err == nil {
		report.Removed = report.Eligible
	}
	return report, err
}

func (saved state) prunePlan(now time.Time) (map[string]bool, PruneReport) {
	recipients := make(map[string][]string)
	for _, subscriber := range saved.Subscriptions {
		recipients[subscriber.JobID] = append(recipients[subscriber.JobID], subscriber.SessionID)
	}
	protected := make(map[string]bool)
	for _, item := range saved.Events {
		for _, recipient := range recipients[item.JobID] {
			if !slices.Contains(item.Delivered, recipient) {
				protected[item.JobID] = true
				break
			}
		}
	}
	removed := make(map[string]bool)
	var report PruneReport
	for _, job := range saved.Jobs {
		if (job.Status != "complete" && job.Status != "failed") || job.UpdatedAt.IsZero() {
			continue
		}
		days := job.HistoryDays
		if days == 0 {
			days = legacyHistoryDays
		}
		if now.Sub(job.UpdatedAt) < time.Duration(days)*24*time.Hour {
			continue
		}
		if protected[job.ID] {
			report.Protected++
			continue
		}
		removed[job.ID] = true
		report.Eligible++
	}
	return removed, report
}
