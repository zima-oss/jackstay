// Sources other than the journal: cards, the last gate and receipt, chat, the brief and
// background jobs. Each is read through its package's public read function, without writing.

package status

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

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

// BackgroundStatus are the `jst bg` jobs started in the repository. Unattributed counts jobs of
// an older jst whose folder no longer exists, which may be this repository's: the report is then
// partial.
type BackgroundStatus struct {
	Status       string           `json:"status"`
	Jobs         []background.Job `json:"jobs,omitempty"`
	Unattributed int              `json:"unattributed"`
	Error        string           `json:"error,omitempty"`
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
	store, err := sessionchat.StoreFor(common, policy)
	if err != nil {
		return ChatStatus{Status: stateUnknown, Error: err.Error()}
	}
	overview, err := store.ReadOverview()
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

// readBackground are the jobs started in this repository, found by the repository identity
// `jst bg` recorded at launch: its common folder's device and inode, which a renamed or moved
// checkout keeps and a new clone at an old path does not get. A job of an older jst carries none
// and is placed by its folder while that exists. A live job that can be placed nowhere (its folder
// gone, and its identity unknown here, as after a move across file systems) is counted as
// unattributed, which makes the report partial, rather than guessed or dropped.
func readBackground(found checkout) BackgroundStatus {
	jobs, err := background.Jobs()
	if err != nil {
		return BackgroundStatus{Status: stateUnknown, Error: err.Error()}
	}
	result := BackgroundStatus{Status: stateNone}
	for _, job := range jobs {
		switch {
		case job.Repository != "" && job.Repository == found.Inode:
			result.Jobs = append(result.Jobs, job)
		case job.Repository == "" && found.holds(job.Cwd):
			result.Jobs = append(result.Jobs, job)
		case job.Live() && !exists(job.Cwd):
			result.Unattributed++
		}
	}
	if len(result.Jobs) > 0 {
		result.Status = "listed"
	}
	return result
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
