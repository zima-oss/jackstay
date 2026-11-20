// Sessions and worktrees: which agents worked in which linked worktree of a repository, from the
// local journal (formerly `jst inventory`).

package status

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
)

// Worktree is one checkout Git lists for the repository.
type Worktree struct {
	ID       string `json:"id,omitempty"`
	Path     string `json:"path"`
	Head     string `json:"head,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Locked   bool   `json:"locked"`
	Prunable bool   `json:"prunable"`
}

// Session is one runner session seen in the journal, attributed to a worktree.
type Session struct {
	Runner   string    `json:"runner"`
	ID       string    `json:"session_id,omitempty"`
	Worktree string    `json:"worktree_id"`
	SeenAt   time.Time `json:"last_seen"`
	Guard    string    `json:"guard"`
}

// checkout is what Git and jackstay.json say of the repository at a path: its identity, its
// worktrees and the journal folders its spans may be in. Folder names alone do not establish
// ownership, because independent repositories can share a project name; spans are attributed
// by their recorded paths.
type checkout struct {
	RepositoryID string
	Project      string
	Root         string
	Common       string
	Worktrees    []Worktree
	Folders      []string
	byPath       map[string]string
}

// openCheckout reads the repository containing current.
func openCheckout(ctx context.Context, current string) (checkout, error) {
	git := gitcli.Git{Dir: current}
	top, err := git.Line(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return checkout{}, err
	}
	if resolved, err := filepath.EvalSymlinks(top); err == nil {
		top = resolved
	}
	common, err := git.CommonDir(ctx)
	if err != nil {
		return checkout{}, err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return checkout{}, err
	}
	listed, err := git.Run(ctx, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return checkout{}, err
	}
	found := checkout{RepositoryID: localID(common), Root: top, Common: common, Worktrees: parseWorktrees(string(listed))}
	home, err := homedir.Home()
	if err != nil {
		return checkout{}, err
	}
	folders := []string{filepath.Join(home, homedir.Shared, telemetry.FolderName)}
	if root, settings, err := config.Load(top, []string{}); err == nil && root != "" {
		found.Project = config.ProjectName(root, settings, top)
		folders = append(folders, filepath.Join(home, homedir.MarkerName(found.Project), telemetry.FolderName))
	}
	// A project configured below the Git root journals under its own name: the folder the reader
	// asked from selects it too. Spans are still attributed only through the worktrees' paths.
	if root, settings, err := config.Load(current, []string{}); err == nil && root != "" {
		folders = append(folders, filepath.Join(home, homedir.MarkerName(config.ProjectName(root, settings, current)), telemetry.FolderName))
	}
	for index := range found.Worktrees {
		item := &found.Worktrees[index]
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
	found.Folders = slices.Compact(folders)
	found.byPath = worktreePaths(found.Worktrees)
	return found, nil
}

// worktreeOwning is the worktree containing an observed absolute location, or "" when the
// location is missing, belongs to another repository, or cannot be read.
func (found checkout) worktreeOwning(location string) string {
	return ownerOf(found.byPath, location)
}

// holds says path is inside one of the repository's worktrees. A path that no longer exists (a
// job's folder renamed or removed while it runs) is judged by its words against the worktrees'
// paths, so a running job is not hidden.
func (found checkout) holds(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	if found.worktreeOwning(path) != "" {
		return true
	}
	if _, err := os.Lstat(path); err == nil {
		return false
	}
	cleaned := filepath.Clean(path)
	for _, tree := range found.Worktrees {
		for _, root := range []string{tree.Path, realPathOf(tree.Path)} {
			if root != "" && (cleaned == root || strings.HasPrefix(cleaned, root+string(filepath.Separator))) {
				return true
			}
		}
	}
	return false
}

// realPathOf is the path with its links resolved, "" when it cannot be.
func realPathOf(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	return resolved
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

func parseWorktrees(raw string) []Worktree {
	result := []Worktree{}
	for _, record := range strings.Split(raw, "\x00\x00") {
		var tree Worktree
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
	slices.SortFunc(result, func(left, right Worktree) int { return strings.Compare(left.Path, right.Path) })
	return result
}

type sighting struct {
	parent spanKey
	// pathKnown also distinguishes a path outside this repository from a child with no
	// path. The former must never inherit another worktree from its trace.
	pathKnown bool
	byRunner  bool
	worktree  string
	runner    string
	id        string
	guard     string
	at        time.Time
}

type sightings struct {
	worktrees map[string]string
	paths     map[string]string
	spans     map[spanKey]sighting
}

func newSightings(found checkout) sightings {
	return sightings{worktrees: found.byPath, paths: map[string]string{}, spans: map[spanKey]sighting{}}
}

func worktreePaths(trees []Worktree) map[string]string {
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

func (seen sightings) add(span journal.Span) {
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
	seen.spans[spanKey{span.TraceID, span.SpanID}] = sighting{
		parent: spanKey{span.TraceID, span.ParentID}, pathKnown: location != "", worktree: seen.owner(location),
		byRunner: command != "" || codex != "", runner: runner, id: id, guard: span.Text(telemetry.AttrGuardOutcome), at: span.End.UTC()}
}

func (seen sightings) owner(location string) string {
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

func (seen sightings) worktreeOf(item sighting) string {
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

func (seen sightings) sessions() []Session {
	bySession := map[[3]string]Session{}
	for _, item := range seen.spans {
		if !item.byRunner {
			continue
		}
		owner := seen.worktreeOf(item)
		if owner == "" || item.id == "" && !item.pathKnown {
			continue
		}
		key := [3]string{owner, item.runner, item.id}
		held, exists := bySession[key]
		if !exists {
			held = Session{Runner: item.runner, ID: item.id, Worktree: owner, Guard: "unknown"}
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
	result := make([]Session, 0, len(bySession))
	for _, item := range bySession {
		result = append(result, item)
	}
	slices.SortFunc(result, func(left, right Session) int {
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
