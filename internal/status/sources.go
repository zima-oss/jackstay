// Sources other than the journal: cards, the last gate and receipt, chat, the brief and
// background jobs. Each is read through its package's public read function, without writing.

package status

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/cards"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/contextbrief"
	"github.com/zima-oss/jackstay/internal/procs/background"
	"github.com/zima-oss/jackstay/internal/project"
	"github.com/zima-oss/jackstay/internal/receipts"
	"github.com/zima-oss/jackstay/internal/series"
	"github.com/zima-oss/jackstay/internal/sessionchat"
)

// Source states every source may be in besides its own.
const (
	stateUnknown  = "unknown"  // it could not be read: Error says why
	stateDisabled = "disabled" // the project does not use it
	stateNone     = "none"     // nothing is recorded yet
)

// Card contains only the identity and state of an active card.
type Card struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// CardStatus reports the local cards source and its active entries.
type CardStatus struct {
	Status string `json:"status"`
	Active []Card `json:"active,omitempty"`
	Error  string `json:"error,omitempty"`
}

// RunStatus summarizes a recorded gate or receipt and whether it is for the checkout's HEAD.
type RunStatus struct {
	Status    string `json:"status"`
	Outcome   string `json:"outcome,omitempty"`
	At        string `json:"at,omitempty"`
	Freshness string `json:"freshness"`
	Error     string `json:"error,omitempty"`
}

// ChatStatus contains aggregate counts from the repository's local chat namespace.
type ChatStatus struct {
	Status   string `json:"status"`
	Sessions int    `json:"sessions"`
	Pending  int    `json:"pending"`
	Error    string `json:"error,omitempty"`
}

// BriefStatus is the checkout's saved brief: its task id and freshness, never its text.
type BriefStatus struct {
	Status string `json:"status"`
	contextbrief.Summary
	Error string `json:"error,omitempty"`
}

// BackgroundStatus are the `jst bg` jobs proven to be this repository's, and how many other jobs
// this machine has. Attribution never guesses: a job it cannot place here is counted in Elsewhere,
// so a person always sees that it exists (`jst procs --all` lists them all).
type BackgroundStatus struct {
	Status    string           `json:"status"`
	Jobs      []background.Job `json:"jobs,omitempty"`
	Elsewhere int              `json:"elsewhere"`
	Error     string           `json:"error,omitempty"`
}

func readCards(root string, loaded config.Config) CardStatus {
	section := loaded.Section("cards")
	if section == nil {
		return CardStatus{Status: stateDisabled}
	}
	rules, err := cards.FromSection(section)
	if err != nil {
		return CardStatus{Status: stateUnknown, Error: err.Error()}
	}
	directory, err := project.Inside(root, rules.Dir)
	if err != nil {
		return CardStatus{Status: stateUnknown, Error: err.Error()}
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return CardStatus{Status: stateNone}
	}
	if err != nil {
		return CardStatus{Status: stateUnknown, Error: err.Error()}
	}
	result := CardStatus{Status: "ok"}
	for _, entry := range entries {
		id := rules.IDOfFile(entry.Name())
		if id == "" {
			continue
		}
		name, err := project.Inside(root, filepath.Join(rules.Dir, entry.Name()))
		if err != nil {
			return CardStatus{Status: stateUnknown, Error: err.Error()}
		}
		data, err := readRegular(name, 1<<20)
		if err != nil {
			return CardStatus{Status: stateUnknown, Error: fmt.Sprintf("%s: %v", entry.Name(), err)}
		}
		matter, err := series.ReadFrontMatter(string(data))
		if err != nil {
			return CardStatus{Status: stateUnknown, Error: fmt.Sprintf("%s: %v", entry.Name(), err)}
		}
		value, _ := matter.Get("state")
		state, ok := value.(string)
		if !ok || state == "" {
			return CardStatus{Status: stateUnknown, Error: fmt.Sprintf("%s: state is missing", entry.Name())}
		}
		if state != "done" && state != "verified" {
			result.Active = append(result.Active, Card{ID: id, State: state})
		}
	}
	sort.Slice(result.Active, func(a, b int) bool { return result.Active[a].ID < result.Active[b].ID })
	return result
}

// readRegular reads a card file the repository holds: a FIFO or a device is refused at once
// rather than waited on.
func readRegular(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 -- a card file inside the repository
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("source is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("source exceeds %d bytes", limit)
	}
	return data, nil
}

// readRuns are the checkout's last gate and last stored pass, each compared with its HEAD.
func readRuns(root, name, head string) (gate, receipt RunStatus) {
	gateLine, passLine, err := receipts.LatestRuns(name, root)
	if errors.Is(err, receipts.ErrNoRunLog) {
		return RunStatus{Status: stateNone, Freshness: stateUnknown}, RunStatus{Status: stateNone, Freshness: stateUnknown}
	}
	if err != nil {
		unknown := RunStatus{Status: stateUnknown, Freshness: stateUnknown, Error: err.Error()}
		return unknown, unknown
	}
	return runOf(gateLine, head), runOf(passLine, head)
}

func runOf(record receipts.Record, head string) RunStatus {
	if record == nil {
		return RunStatus{Status: stateNone, Freshness: stateUnknown}
	}
	result := RunStatus{Status: "recorded", Outcome: record.Text("outcome"), At: record.Text("at"), Freshness: stateUnknown}
	if head != "" && record.Text("head") != "" {
		result.Freshness = "current"
		if !strings.EqualFold(record.Text("head"), head) {
			result.Freshness = "stale"
		}
	}
	return result
}

func readChat(common string, loaded config.Config) ChatStatus {
	policy := config.ChatSettings(loaded.Section("chat"))
	if !policy.Enabled {
		return ChatStatus{Status: stateDisabled}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	overview, err := sessionchat.OverviewOf(ctx, common)
	if err != nil {
		return ChatStatus{Status: stateUnknown, Error: err.Error()}
	}
	return ChatStatus{Status: "available", Sessions: overview.Sessions, Pending: overview.Pending}
}

func readBrief(root string) BriefStatus {
	summary, err := contextbrief.SummaryOf(root)
	if err != nil {
		return BriefStatus{Status: stateUnknown, Summary: contextbrief.Summary{Freshness: stateUnknown}, Error: err.Error()}
	}
	if summary.Freshness == "absent" {
		return BriefStatus{Status: stateNone, Summary: summary}
	}
	return BriefStatus{Status: "saved", Summary: summary}
}

// readBackground places each job by the repository identity `jst bg` recorded at launch (its
// common folder's device and inode, which a renamed or moved checkout keeps and a new clone at an
// old path does not get), or, for a job of an older jst that recorded none, by its folder while
// that folder is inside one of the worktrees. Every other job is counted as elsewhere.
func readBackground(found checkout) BackgroundStatus {
	jobs, err := background.Jobs()
	if err != nil {
		return BackgroundStatus{Status: stateUnknown, Error: err.Error()}
	}
	return placeJobs(found, jobs)
}

// placeJobs sorts the machine's jobs into this repository's and the count of the others.
func placeJobs(found checkout, jobs []background.Job) BackgroundStatus {
	result := BackgroundStatus{Status: stateNone}
	for _, job := range jobs {
		switch {
		case job.Repository != "" && job.Repository == found.Inode:
			result.Jobs = append(result.Jobs, job)
		case job.Repository == "" && found.holds(job.Cwd):
			result.Jobs = append(result.Jobs, job)
		default:
			result.Elsewhere++
		}
	}
	if len(result.Jobs) > 0 {
		result.Status = "listed"
	}
	// A job of this repository whose state cannot be told (the process table cannot be read) may
	// still run: the jobs stay listed and the source is unknown, so the report is partial.
	unknown := 0
	for _, job := range result.Jobs {
		if job.State == "unknown" {
			unknown++
		}
	}
	if unknown > 0 {
		result.Status = stateUnknown
		result.Error = fmt.Sprintf("the state of %d job(s) here cannot be told (the process table cannot be read): "+
			"they may still run", unknown)
	}
	return result
}
