// Package workspaceboard reads bounded status summaries from explicitly named repositories.
package workspaceboard

import (
	"bytes"
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
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/project"
	"github.com/zima-oss/jackstay/internal/receipts"
	"github.com/zima-oss/jackstay/internal/series"
	"github.com/zima-oss/jackstay/internal/sessionchat"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

// Board is an ordered snapshot of the requested repository sources.
type Board struct {
	Schema       int          `json:"schema"`
	ObservedAt   time.Time    `json:"observed_at"`
	Partial      bool         `json:"partial"`
	Repositories []Repository `json:"repositories"`
}

// Repository contains the observed sources for one requested checkout.
type Repository struct {
	Path    string     `json:"path"`
	Project string     `json:"project,omitempty"`
	Status  string     `json:"status"`
	Error   string     `json:"error,omitempty"`
	Cards   CardStatus `json:"cards"`
	Gate    RunStatus  `json:"gate"`
	Receipt RunStatus  `json:"receipt"`
	Chat    ChatStatus `json:"chat"`
}

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

// RunStatus summarizes a recorded gate or receipt and its revision freshness.
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

// Read observes each requested repository independently. One unreadable repository does not hide
// the others. No state file, lock, Git ref, or checkout is written.
func Read(ctx context.Context, paths []string) Board {
	board := Board{Schema: 1, ObservedAt: time.Now().UTC(), Repositories: make([]Repository, 0, len(paths))}
	for _, supplied := range paths {
		repository := readRepository(ctx, supplied)
		board.Repositories = append(board.Repositories, repository)
		if repository.Status != "ok" || repository.Cards.Status == "unknown" ||
			repository.Gate.Status == "unknown" || repository.Receipt.Status == "unknown" || repository.Chat.Status == "unknown" {
			board.Partial = true
		}
	}
	return board
}

func readRepository(parent context.Context, supplied string) Repository {
	result := Repository{Path: supplied, Status: "unknown", Cards: CardStatus{Status: "unknown"},
		Gate:    RunStatus{Status: "unknown", Freshness: "unknown"},
		Receipt: RunStatus{Status: "unknown", Freshness: "unknown"}, Chat: ChatStatus{Status: "unknown"}}
	path, err := filepath.Abs(supplied)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if path, err = filepath.EvalSymlinks(path); err != nil {
		result.Error = err.Error()
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	git := gitcli.Git{Dir: path}
	root, err := git.Line(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Path = root
	common, err := git.CommonDir(ctx)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	configRoot, loaded, err := config.Load(root, []string{})
	if err != nil || configRoot != root {
		if err == nil {
			err = errors.New("jackstay.json is absent at the repository root")
		}
		result.Error = err.Error()
		return result
	}
	result.Project = config.ProjectName(root, loaded, root)
	result.Status = "ok"
	result.Cards = readCards(root)
	result.Gate, result.Receipt = readRuns(root, result.Project, git, ctx)
	result.Chat = readChat(root, common)
	return result
}

func readCards(root string) CardStatus {
	_, loaded, err := config.Load(root, []string{"cards"})
	if err != nil {
		return CardStatus{Status: "unknown", Error: err.Error()}
	}
	section := loaded.Section("cards")
	if section == nil {
		return CardStatus{Status: "disabled"}
	}
	rules, err := cards.FromSection(section)
	if err != nil {
		return CardStatus{Status: "unknown", Error: err.Error()}
	}
	directory, err := project.Inside(root, rules.Dir)
	if err != nil {
		return CardStatus{Status: "unknown", Error: err.Error()}
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return CardStatus{Status: "none"}
	}
	if err != nil {
		return CardStatus{Status: "unknown", Error: err.Error()}
	}
	result := CardStatus{Status: "ok"}
	for _, entry := range entries {
		id := rules.IDOfFile(entry.Name())
		if id == "" {
			continue
		}
		name, err := project.Inside(root, filepath.Join(rules.Dir, entry.Name()))
		if err != nil {
			result.Status, result.Error = "unknown", err.Error()
			return result
		}
		data, err := readRegular(name, 1<<20)
		if err != nil {
			result.Status, result.Error = "unknown", fmt.Sprintf("%s: %v", entry.Name(), err)
			return result
		}
		matter, err := series.ReadFrontMatter(string(data))
		if err != nil {
			result.Status, result.Error = "unknown", fmt.Sprintf("%s: %v", entry.Name(), err)
			return result
		}
		value, _ := matter.Get("state")
		state, ok := value.(string)
		if !ok || state == "" {
			result.Status, result.Error = "unknown", fmt.Sprintf("%s: state is missing", entry.Name())
			return result
		}
		if state != "done" && state != "verified" {
			result.Active = append(result.Active, Card{ID: id, State: state})
		}
	}
	sort.Slice(result.Active, func(a, b int) bool { return result.Active[a].ID < result.Active[b].ID })
	return result
}

func readRuns(root, name string, git gitcli.Git, ctx context.Context) (RunStatus, RunStatus) {
	unknown := RunStatus{Status: "unknown", Freshness: "unknown"}
	path, err := receipts.RunLogPath()
	if err != nil {
		unknown.Error = err.Error()
		return unknown, unknown
	}
	records, err := readRunLog(path)
	if errors.Is(err, os.ErrNotExist) {
		none := RunStatus{Status: "none", Freshness: "unknown"}
		return none, none
	} else if err != nil {
		unknown.Error = err.Error()
		return unknown, unknown
	}
	head, headErr := git.Line(ctx, "rev-parse", "HEAD")
	gate, receipt := RunStatus{Status: "none", Freshness: "unknown"}, RunStatus{Status: "none", Freshness: "unknown"}
	var gateEpoch, receiptEpoch float64
	for _, record := range records {
		if record.Text("project") != name || record.Text("worktree") != root || record.Inside() {
			continue
		}
		recordEpoch, _ := record.Number("ended_epoch")
		if recordEpoch == 0 {
			recordEpoch, _ = record.Number("epoch")
		}
		if record.Text("event") == receipts.EventGate && recordEpoch >= gateEpoch {
			gate = runOf(record, head, headErr)
			gateEpoch = recordEpoch
		}
		if record.Text("event") == receipts.EventRun && record.Text("outcome") == receipts.OutcomePassed && record["stored"] == true && recordEpoch >= receiptEpoch {
			receipt = runOf(record, head, headErr)
			receiptEpoch = recordEpoch
		}
	}
	return gate, receipt
}

func readRunLog(path string) ([]receipts.Record, error) {
	data, err := readRegular(path, 32<<20)
	if err != nil {
		return nil, err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return nil, errors.New("run log ends with an incomplete line")
	}
	var records []receipts.Record
	for index, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var record receipts.Record
		if err := strictjson.UnmarshalOpen(line, &record); err != nil || record == nil {
			return nil, fmt.Errorf("run log line %d is invalid", index+1)
		}
		records = append(records, record)
	}
	return records, nil
}

func readRegular(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
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

func runOf(record receipts.Record, head string, headErr error) RunStatus {
	result := RunStatus{Status: "recorded", Outcome: record.Text("outcome"), At: record.Text("at"), Freshness: "unknown"}
	if headErr == nil && record.Text("head") != "" {
		result.Freshness = "current"
		if !strings.EqualFold(record.Text("head"), head) {
			result.Freshness = "stale"
		}
	}
	return result
}

func readChat(root, common string) ChatStatus {
	_, loaded, err := config.Load(root, []string{"chat"})
	if err != nil {
		return ChatStatus{Status: "unknown", Error: err.Error()}
	}
	policy := config.ChatSettings(loaded.Section("chat"))
	if !policy.Enabled {
		return ChatStatus{Status: "disabled"}
	}
	store, err := sessionchat.StoreFor(common, policy)
	if err != nil {
		return ChatStatus{Status: "unknown", Error: err.Error()}
	}
	overview, err := store.ReadOverview()
	if err != nil {
		return ChatStatus{Status: "unknown", Error: err.Error()}
	}
	return ChatStatus{Status: "available", Sessions: overview.Sessions, Pending: overview.Pending}
}
