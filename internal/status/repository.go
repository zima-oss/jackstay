// Package status is `jst status`: one read-only overview of local work. For one repository it
// shows its worktrees and sessions, recent delivery events from the journal, chat counts, the
// brief, background jobs, the last gate and receipt and active cards; for several, one row each.
// It writes nothing: no state file, lock, Git ref or checkout.
package status

import (
	"context"
	"path/filepath"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
)

// Schema is the version of the JSON envelope; a change that removes or renames a field raises it.
const Schema = 1

// Kinds of report.
const (
	KindRepository = "repository" // one repository, in detail
	KindBoard      = "board"      // one row per repository
)

// Report is the one envelope `jst status --json` prints.
type Report struct {
	Schema       int          `json:"schema"`
	Kind         string       `json:"kind"`
	ObservedAt   time.Time    `json:"observed_at"`
	Since        time.Time    `json:"since"`
	Until        time.Time    `json:"until"`
	Partial      bool         `json:"partial"`
	Repositories []Repository `json:"repositories"`
}

// Repository is what status read of one repository. The lists are filled only in a detailed
// report; the counts always are.
type Repository struct {
	Path         string           `json:"path"`
	Project      string           `json:"project,omitempty"`
	RepositoryID string           `json:"repository_id,omitempty"`
	Branch       string           `json:"branch,omitempty"`
	Head         string           `json:"head,omitempty"`
	Status       string           `json:"status"`
	Error        string           `json:"error,omitempty"`
	Counts       Counts           `json:"counts"`
	Worktrees    []Worktree       `json:"worktrees,omitempty"`
	Sessions     []Session        `json:"sessions,omitempty"`
	Events       []Event          `json:"events,omitempty"`
	Journal      JournalStatus    `json:"journal"`
	Brief        BriefStatus      `json:"brief"`
	Chat         ChatStatus       `json:"chat"`
	Background   BackgroundStatus `json:"background"`
	Gate         RunStatus        `json:"gate"`
	Receipt      RunStatus        `json:"receipt"`
	Cards        CardStatus       `json:"cards"`
}

// Counts are the sizes of the lists, also given when the lists are not.
type Counts struct {
	Worktrees     int `json:"worktrees"`
	Sessions      int `json:"sessions"`
	Events        int `json:"events"`
	OmittedEvents int `json:"omitted_events"`
}

// JournalStatus says whether the journal was read whole for the window.
type JournalStatus struct {
	Status      string `json:"status"`
	Unreadable  int    `json:"unreadable_lines"`
	Problems    int    `json:"read_problems"`
	ScanPartial bool   `json:"scan_partial"`
}

// complete says every source of the repository was read.
func (repository Repository) complete() bool {
	return repository.Status == "ok" && repository.Journal.Status == "complete" &&
		repository.Brief.Status != stateUnknown && repository.Chat.Status != stateUnknown &&
		repository.Background.Status != stateUnknown && repository.Background.Unattributed == 0 &&
		repository.Gate.Status != stateUnknown &&
		repository.Receipt.Status != stateUnknown && repository.Cards.Status != stateUnknown
}

// Read observes each repository independently: one that cannot be read does not hide the others.
// With detail, a repository's lists are kept.
func Read(ctx context.Context, paths []string, asked window, detail bool, now time.Time) Report {
	kind := KindBoard
	if detail {
		kind = KindRepository
	}
	report := Report{Schema: Schema, Kind: kind, ObservedAt: now.UTC(), Since: asked.from, Until: asked.until,
		Repositories: make([]Repository, 0, len(paths))}
	for _, path := range paths {
		repository := readRepository(ctx, path, asked, now)
		if !detail {
			repository.Worktrees, repository.Sessions, repository.Events = nil, nil, nil
		}
		report.Partial = report.Partial || !repository.complete()
		report.Repositories = append(report.Repositories, repository)
	}
	return report
}

func readRepository(parent context.Context, supplied string, asked window, now time.Time) Repository {
	result := Repository{Path: supplied, Status: stateUnknown, Journal: JournalStatus{Status: stateUnknown},
		Brief: BriefStatus{Status: stateUnknown}, Chat: ChatStatus{Status: stateUnknown},
		Background: BackgroundStatus{Status: stateUnknown}, Gate: RunStatus{Status: stateUnknown, Freshness: stateUnknown},
		Receipt: RunStatus{Status: stateUnknown, Freshness: stateUnknown}, Cards: CardStatus{Status: stateUnknown}}
	path, err := filepath.Abs(supplied)
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	found, err := openCheckout(ctx, path)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Path, result.RepositoryID, result.Project = found.Root, found.RepositoryID, found.Project
	git := gitcli.Git{Dir: found.Root}
	result.Head, _ = git.Line(ctx, "rev-parse", "HEAD")
	result.Branch, _ = git.Line(ctx, "symbolic-ref", "--short", "-q", "HEAD")
	result.Worktrees = found.Worktrees
	result.Counts.Worktrees = len(found.Worktrees)

	seen, collected := newSightings(found), &eventCollector{asked: asked}
	read := journal.ReadBounded(found.Folders, asked.from, asked.until, now, maximumLine, func(span journal.Span) {
		seen.add(span)
		collected.add(span)
	})
	result.Sessions = seen.sessions()
	result.Events, result.Counts.OmittedEvents = collected.events(found)
	result.Counts.Sessions, result.Counts.Events = len(result.Sessions), len(result.Events)
	result.Journal = JournalStatus{Status: "complete", Unreadable: read.Unreadable, Problems: len(read.Problems),
		ScanPartial: collected.partial}
	if read.Unreadable > 0 || len(read.Problems) > 0 || collected.partial {
		result.Journal.Status = "partial"
	}

	result.Background = readBackground(found)
	result.Brief = readBrief(found.Root)
	result.Status = "ok"
	result.Gate, result.Receipt = readRuns(found.Root, found.Project, result.Head)
	// Each section is loaded on its own, so one that is invalid hides only its own source. A
	// repository without its own jackstay.json uses neither cards nor chat.
	configRoot, _, err := config.Load(found.Root, []string{})
	if err != nil {
		result.Cards = CardStatus{Status: stateUnknown, Error: err.Error()}
		result.Chat = ChatStatus{Status: stateUnknown, Error: err.Error()}
		return result
	}
	if configRoot != found.Root {
		result.Cards, result.Chat = CardStatus{Status: stateDisabled}, ChatStatus{Status: stateDisabled}
		return result
	}
	if _, loaded, err := config.Load(found.Root, []string{"cards"}); err != nil {
		result.Cards = CardStatus{Status: stateUnknown, Error: err.Error()}
	} else {
		result.Cards = readCards(found.Root, loaded)
	}
	if _, loaded, err := config.Load(found.Root, []string{"chat"}); err != nil {
		result.Chat = ChatStatus{Status: stateUnknown, Error: err.Error()}
	} else {
		result.Chat = readChat(found.Common, loaded)
	}
	return result
}
