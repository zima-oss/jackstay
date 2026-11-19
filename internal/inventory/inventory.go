// Package inventory reports local repository and runner observations.
package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
)

const usage = `usage: jst inventory [--since DURATION]

Print the local repository, its linked worktrees, and recent session observations as JSON.
  --since DURATION  look back at most this long (default 24h, maximum 720h)

Runner and guard coverage are reported from recorded evidence. Missing evidence is unknown.
exit: 0 report written; 1 journal incomplete; 2 bad request or repository unavailable.

example: jst inventory --since 48h`

func init() {
	cli.Register(cli.Command{Name: "inventory", Summary: "local Git worktrees and observed runner sessions as JSON",
		Usage: usage, Run: run})
}

type report struct {
	Schema       int        `json:"schema"`
	RepositoryID string     `json:"repository_id"`
	Project      string     `json:"project,omitempty"`
	Current      string     `json:"current_worktree_path"`
	Worktrees    []worktree `json:"worktrees"`
	Sessions     []session  `json:"sessions"`
	Since        time.Time  `json:"since"`
	Until        time.Time  `json:"until"`
	Unreadable   int        `json:"unreadable_lines"`
}

type worktree struct {
	ID       string `json:"id,omitempty"`
	Path     string `json:"path"`
	Head     string `json:"head,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Locked   bool   `json:"locked"`
	Prunable bool   `json:"prunable"`
}

type session struct {
	Runner   string    `json:"runner"`
	ID       string    `json:"session_id,omitempty"`
	Worktree string    `json:"worktree_id"`
	SeenAt   time.Time `json:"last_seen"`
	Guard    string    `json:"guard"`
}

func run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("inventory", flag.ContinueOnError)
	flags.SetOutput(stderr)
	since := flags.Duration("since", 24*time.Hour, "time window")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *since <= 0 || *since > 30*24*time.Hour {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	current, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "jst inventory:", err)
		return 2
	}
	now := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, folders, err := repository(ctx, current, now.Add(-*since), now)
	if err != nil {
		fmt.Fprintln(stderr, "jst inventory:", err)
		return 2
	}
	observed := newObservations(result.Worktrees)
	read := journal.Read(folders, result.Since, now, now, observed.add)
	result.Sessions = observed.sessions()
	result.Unreadable = read.Unreadable
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(stderr, "jst inventory:", err)
		return 2
	}
	if read.Unreadable > 0 {
		fmt.Fprintf(stderr, "jst inventory: %d unreadable journal line(s)\n", read.Unreadable)
	}
	for _, problem := range read.Problems {
		fmt.Fprintln(stderr, "jst inventory:", problem)
	}
	if read.Unreadable > 0 || len(read.Problems) > 0 {
		return 1
	}
	return 0
}

// JournalScope identifies the local repository and journal folders to scan. Folder names
// alone do not establish ownership because independent repositories can share a project name.
type JournalScope struct {
	RepositoryID string
	Folders      []string
	worktrees    map[string]string
}

// OpenJournalScope resolves the registered worktrees of the current checkout.
func OpenJournalScope(ctx context.Context, current string) (JournalScope, error) {
	found, folders, err := repository(ctx, current, time.Time{}, time.Time{})
	if err != nil {
		return JournalScope{}, err
	}
	return JournalScope{RepositoryID: found.RepositoryID, Folders: folders, worktrees: worktreePaths(found.Worktrees)}, nil
}

// WorktreeID returns the worktree containing an observed absolute location, or an empty
// result when the location is missing, belongs to another repository, or cannot be read.
func (scope JournalScope) WorktreeID(location string) string {
	return ownerOf(scope.worktrees, location)
}

func repository(ctx context.Context, current string, since, now time.Time) (report, []string, error) {
	git := gitcli.Git{Dir: current}
	top, err := git.Line(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return report{}, nil, err
	}
	common, err := git.CommonDir(ctx)
	if err != nil {
		return report{}, nil, err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return report{}, nil, err
	}
	listed, err := git.Run(ctx, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return report{}, nil, err
	}
	result := report{Schema: 1, RepositoryID: localID(common), Since: since.UTC(), Until: now.UTC(),
		Current: top, Worktrees: parseWorktrees(string(listed)), Sessions: []session{}}
	home, err := homedir.Home()
	if err != nil {
		return report{}, nil, err
	}
	folders := []string{filepath.Join(home, homedir.Shared, telemetry.FolderName)}
	if root, settings, err := config.Load(current, []string{}); err == nil && root != "" {
		result.Project = config.ProjectName(root, settings, current)
		folders = append(folders, filepath.Join(home, homedir.MarkerName(result.Project), telemetry.FolderName))
	}
	for index := range result.Worktrees {
		item := &result.Worktrees[index]
		// Git lists prunable registrations too. Keep the path and state even if its local
		// metadata cannot be read. Only real working copies supply session evidence.
		if item.Prunable {
			continue
		}
		item.ID = worktreeID(ctx, item.Path, common)
		if item.ID == "" {
			continue
		}
		root, settings, err := config.Load(item.Path, []string{})
		if err != nil || root == "" {
			continue
		}
		name := config.ProjectName(root, settings, item.Path)
		folders = append(folders, filepath.Join(home, homedir.MarkerName(name), telemetry.FolderName))
	}
	slices.Sort(folders)
	return result, slices.Compact(folders), nil
}

func worktreeID(ctx context.Context, directory, expectedCommon string) string {
	git := gitcli.Git{Dir: directory}
	paths, err := git.Run(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir", "--absolute-git-dir")
	if err != nil {
		return ""
	}
	common, dir, found := strings.Cut(strings.TrimSuffix(string(paths), "\n"), "\n")
	if !found {
		return ""
	}
	// A registered path may have been reused by another checkout. Its Git metadata must
	// still name this repository before any session from that path is attributed to it.
	if canonical, err := filepath.EvalSymlinks(common); err != nil || canonical != expectedCommon {
		return ""
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return ""
	}
	return localID(dir)
}

func localID(source string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(source)))
	return "git:" + hex.EncodeToString(digest[:])
}

func parseWorktrees(raw string) []worktree {
	result := []worktree{}
	for _, record := range strings.Split(raw, "\x00\x00") {
		var tree worktree
		bare := false
		for _, field := range strings.Split(record, "\x00") {
			keyword, value, _ := strings.Cut(field, " ")
			switch keyword {
			case "worktree":
				tree.Path = value
			case "HEAD":
				tree.Head = value
			case "branch":
				tree.Branch = strings.TrimPrefix(value, "refs/heads/")
			case "locked":
				tree.Locked = true
			case "prunable":
				tree.Prunable = true
			case "bare":
				bare = true
			}
		}
		if tree.Path != "" && !bare {
			result = append(result, tree)
		}
	}
	slices.SortFunc(result, func(left, right worktree) int { return strings.Compare(left.Path, right.Path) })
	return result
}

type spanKey struct{ trace, span string }

type observation struct {
	parent spanKey
	// pathKnown also distinguishes a path outside this repository from a child with no
	// path. The former must never inherit another worktree from its trace.
	pathKnown bool
	sighting  bool
	worktree  string
	runner    string
	id        string
	guard     string
	at        time.Time
}

type observations struct {
	worktrees map[string]string
	paths     map[string]string
	spans     map[spanKey]observation
}

func newObservations(trees []worktree) observations {
	return observations{worktrees: worktreePaths(trees), paths: map[string]string{}, spans: map[spanKey]observation{}}
}

func worktreePaths(trees []worktree) map[string]string {
	paths := map[string]string{}
	for _, tree := range trees {
		if tree.ID != "" {
			if canonical, err := filepath.EvalSymlinks(tree.Path); err == nil {
				paths[canonical] = tree.ID
			}
		}
	}
	return paths
}

func (seen observations) add(span journal.Span) {
	if span.TraceID == "" || span.SpanID == "" {
		return
	}
	location := span.Text(telemetry.AttrWorktree)
	if location == "" {
		location = span.Text(telemetry.AttrCwd)
	}
	command, codex := span.Text(telemetry.AttrCommand), span.Text(telemetry.AttrCodexThread)
	runner, id := "unknown", span.Text(telemetry.AttrClaudeSession)
	switch {
	case codex != "":
		runner, id = "codex", codex
	case command == "":
		id = ""
	case id != "" && command != "guard":
		runner = "claude-code"
	case id != "":
		// Legacy guard records store either runner's session in one field. Only known
		// transcript layouts identify the runner when the hook supplied a path.
		transcript := "/" + filepath.ToSlash(span.Text(telemetry.AttrClaudeTranscript))
		switch {
		case strings.Contains(transcript, "/.claude/projects/"):
			runner = "claude-code"
		case strings.Contains(transcript, "/.codex/sessions/"):
			runner = "codex"
		}
	}
	seen.spans[spanKey{span.TraceID, span.SpanID}] = observation{
		parent: spanKey{span.TraceID, span.ParentID}, pathKnown: location != "", worktree: seen.owner(location),
		sighting: command != "" || codex != "", runner: runner, id: id, guard: span.Text(telemetry.AttrGuardOutcome), at: span.End.UTC()}
}

func (seen observations) owner(location string) string {
	if cached, ok := seen.paths[location]; ok {
		return cached
	}
	owner := ownerOf(seen.worktrees, location)
	seen.paths[location] = owner
	return owner
}

func ownerOf(worktrees map[string]string, location string) string {
	if !filepath.IsAbs(location) {
		return ""
	}
	directory, err := filepath.EvalSymlinks(location)
	if err != nil {
		return ""
	}
	for {
		if id, known := worktrees[directory]; known {
			return id
		}
		// A nested checkout takes precedence over an enclosing registered worktree.
		if _, err := os.Lstat(filepath.Join(directory, ".git")); !errors.Is(err, fs.ErrNotExist) {
			return ""
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return ""
		}
		directory = parent
	}
}

func (seen observations) worktreeOf(item observation) string {
	// Malformed journal spans can form a cycle. An ordinary agent trace has few ancestors.
	for depth := 0; depth < 64; depth++ {
		if item.pathKnown {
			return item.worktree
		}
		parent, ok := seen.spans[item.parent]
		if !ok {
			break
		}
		item = parent
	}
	return ""
}

func (seen observations) sessions() []session {
	bySession := map[[3]string]session{}
	for _, item := range seen.spans {
		if !item.sighting {
			continue
		}
		owner := seen.worktreeOf(item)
		if owner == "" || item.id == "" && !item.pathKnown {
			continue
		}
		key := [3]string{owner, item.runner, item.id}
		held, exists := bySession[key]
		if !exists {
			held = session{Runner: item.runner, ID: item.id, Worktree: owner, Guard: "unknown"}
		}
		if item.at.After(held.SeenAt) {
			held.SeenAt = item.at
		}
		switch item.guard {
		case "allowed", "refused", "warned":
			held.Guard = "observed"
		}
		bySession[key] = held
	}
	result := make([]session, 0, len(bySession))
	for _, item := range bySession {
		result = append(result, item)
	}
	slices.SortFunc(result, func(left, right session) int {
		if difference := right.SeenAt.Compare(left.SeenAt); difference != 0 {
			return difference
		}
		if difference := strings.Compare(left.Worktree, right.Worktree); difference != 0 {
			return difference
		}
		if difference := strings.Compare(left.Runner, right.Runner); difference != 0 {
			return difference
		}
		return strings.Compare(left.ID, right.ID)
	})
	return result
}
