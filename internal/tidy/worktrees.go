package tidy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/guard"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/proctree"
)

// regenerableNames are the guard's built-in regenerable folders (docs/design/guard.md section 0).
var regenerableNames = guard.RegenerableNames()

// sessionStateNames are what agents' tools write anywhere and nobody keeps; sessionStatePaths the
// same by path from the worktree's root (`.omc/plans` and `.omc/handoffs` are work: review A9).
var (
	sessionStateNames = []string{".cc-writes", ".DS_Store"}
	sessionStatePaths = []string{".omc/state", ".omc/logs", ".omc/sessions", ".omx/state", ".omx/logs", ".omx/sessions"}
)

// tree is one record of `git worktree list --porcelain -z`.
type tree struct {
	path, head, branch string
	locked, prunable   bool
	bare, main         bool
}

// parseWorktrees reads `git worktree list --porcelain -z`; the first record is the main checkout.
func parseWorktrees(listed []byte) []tree {
	var found []tree
	for _, field := range strings.Split(string(listed), "\x00") {
		keyword, value, _ := strings.Cut(field, " ")
		if keyword == "worktree" {
			found = append(found, tree{path: value, main: len(found) == 0})
			continue
		}
		if len(found) == 0 {
			continue
		}
		current := &found[len(found)-1]
		switch keyword {
		case "HEAD":
			current.head = value
		case "branch":
			current.branch = strings.TrimPrefix(value, "refs/heads/")
		case "locked":
			current.locked = true
		case "prunable":
			current.prunable = true
		case "bare":
			current.bare = true
		}
	}
	return found
}

// regenerablePatterns are the built-in names, the agents' runtime state and the project's
// `guard.regenerable` read from the jackstay.json at or above folder; an entry that would match nearly
// any name (`?*`) or an `.env`, or holds a slash, is not applied, as in the guard.
func regenerablePatterns(folder string) []string {
	patterns := slices.Concat(regenerableNames, sessionStateNames)
	_, loaded, err := config.Load(folder, []string{})
	if err != nil {
		return patterns
	}
	entries, _ := loaded.Section("guard")["regenerable"].([]any)
	for _, entry := range entries {
		text, _ := entry.(string)
		name := strings.TrimRight(strings.TrimSpace(text), "/")
		if guard.RegenerableProblem(name) != "" || slices.Contains(patterns, name) {
			continue
		}
		patterns = append(patterns, name)
	}
	return patterns
}

// regenerable says whether an ignored path (from the worktree's root) is made again or runtime
// state: one of its names matches a pattern, or it lies under a session-state path.
func regenerable(relative string, patterns []string) bool {
	relative = strings.TrimSuffix(relative, "/")
	for _, statePath := range sessionStatePaths {
		if relative == statePath || strings.HasPrefix(relative, statePath+"/") {
			return true
		}
	}
	for _, part := range strings.Split(relative, "/") {
		for _, pattern := range patterns {
			if matchName(pattern, part) {
				return true
			}
		}
	}
	return false
}

// sessionStateOnly says whether an ignored folder git reports whole (`.omc/`) holds only runtime
// state and regenerable folders: each file in it is looked at, so `.omc/plans` keeps the worktree.
func sessionStateOnly(root, name string, patterns []string) bool {
	folder := strings.TrimSuffix(name, "/")
	holdsState := false
	for _, statePath := range sessionStatePaths {
		if strings.HasPrefix(statePath, folder+"/") {
			holdsState = true
		}
	}
	if !holdsState {
		return false
	}
	clean := true
	err := filepath.WalkDir(filepath.Join(root, folder), func(place string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, place)
		if err != nil {
			return err
		}
		if !regenerable(filepath.ToSlash(relative), patterns) {
			clean = false
			return fs.SkipAll
		}
		return nil
	})
	return err == nil && clean
}

// statusReasons are what `git status --porcelain=v1 -z --ignored=matching --untracked-files=all`
// and `git ls-files -v -z` of a worktree say would be lost; ignoredKept judges an ignored path.
func statusReasons(status, listed []byte, ignoredKept func(name string) bool) []reason {
	var changed, ignored, hidden []string
	entries := strings.Split(string(status), "\x00")
	for index := 0; index < len(entries); index++ {
		entry := entries[index]
		if len(entry) < 4 {
			continue
		}
		code, name := entry[:2], entry[3:]
		if code == "!!" {
			if ignoredKept(name) {
				ignored = append(ignored, name)
			}
			continue
		}
		changed = append(changed, name)
		if code[0] == 'R' || code[0] == 'C' {
			index++ // the rename's or copy's source follows as a field of its own
		}
	}
	for _, entry := range strings.Split(string(listed), "\x00") {
		if len(entry) > 2 && (entry[0] == 'S' || unicode.IsLower(rune(entry[0]))) {
			hidden = append(hidden, entry[2:])
		}
	}
	var reasons []reason
	if len(hidden) > 0 {
		reasons = append(reasons, reason{ID: whyHiddenEdits,
			Detail: fmt.Sprintf("%d marked skip-worktree or assume-unchanged (git does not show their edits): %s", len(hidden), some(hidden))})
	}
	if len(changed) > 0 {
		reasons = append(reasons, reason{ID: whyUncommitted, Detail: fmt.Sprintf("%d uncommitted or untracked: %s", len(changed), some(changed))})
	}
	if len(ignored) > 0 {
		reasons = append(reasons, reason{ID: whyIgnoredKept, Detail: fmt.Sprintf("%d ignored and not regenerable: %s", len(ignored), some(ignored))})
	}
	return reasons
}

func some(names []string) string {
	if len(names) > 5 {
		return strings.Join(names[:5], ", ") + " …"
	}
	return strings.Join(names, ", ")
}

// treeReasons are what in the worktree at folder would be lost (none: nothing).
func treeReasons(ctx context.Context, folder string, patterns []string) []reason {
	here := repo{dir: folder}
	status, err := here.run(ctx, "status", "--porcelain=v1", "-z", "--ignored=matching", "--untracked-files=all")
	if err != nil {
		return []reason{{ID: whyStatusFailed, Detail: "git status failed: " + gitSaid(err, 200)}}
	}
	listed, err := here.run(ctx, "ls-files", "-v", "-z")
	if err != nil {
		return []reason{{ID: whyStatusFailed, Detail: "git ls-files failed: " + gitSaid(err, 200)}}
	}
	return statusReasons(status, listed, func(name string) bool {
		return !regenerable(name, patterns) && !sessionStateOnly(folder, name, patterns)
	})
}

// A holder is a process whose working folder is in a worktree.
type holder struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
}

// seen is what a process view found for a folder: the processes working in it, whether the view
// is complete, and the processes it could not see (still running, their folder never named), each
// named from the process table.
type seen struct {
	holders  []holder
	complete bool
	unseen   []holder
}

// processView is the processes whose working folder is folder or inside it, and whether that view
// is complete.
type processView func(ctx context.Context, folder string) (seen, error)

// realProcesses is proctree's view (lsof checked against the process table, a process it missed
// asked about again by its id), named from the table.
func realProcesses(ctx context.Context, folder string) (seen, error) {
	view, err := proctree.FoldersInside(ctx, folder)
	if err != nil {
		return seen{}, err
	}
	table, _ := procs.ReadTable() // names only: a process unnamed is still named by its id
	found := seen{complete: view.Complete, holders: make([]holder, 0, len(view.Inside))}
	for pid := range view.Inside {
		found.holders = append(found.holders, holder{PID: pid, Name: table[pid].Name})
	}
	sort.Slice(found.holders, func(left, right int) bool { return found.holders[left].PID < found.holders[right].PID })
	for _, pid := range view.Unseen {
		found.unseen = append(found.unseen, holder{PID: pid, Name: table[pid].Name})
	}
	return found, nil
}

// named is up to four processes as "name (pid)", and how many more.
func named(processes []holder) string {
	var names []string
	for _, one := range processes[:min(len(processes), 4)] {
		name := one.Name
		if name == "" {
			name = "?"
		}
		names = append(names, fmt.Sprintf("%s (%d)", name, one.PID))
	}
	if more := len(processes) - len(names); more > 0 {
		names = append(names, fmt.Sprintf("%d more", more))
	}
	return strings.Join(names, ", ")
}

// processReasons keep a worktree a process works in, or whose processes cannot all be seen.
func processReasons(found seen, err error) []reason {
	if err != nil {
		return []reason{{ID: whyProcessUnusable, Detail: "the process table could not be read (" + err.Error() +
			"), so whether something works there is unknown"}}
	}
	var reasons []reason
	switch {
	case found.complete:
	case len(found.unseen) > 0 && len(found.unseen) <= 8:
		// A few processes of this account that lsof never named, asked about again by id: a
		// process it cannot see (another account's view of it), not the sandbox.
		reasons = append(reasons, reason{ID: whyProcessPartial,
			Detail: "lsof never named the working folder of " + named(found.unseen) +
				", still running, so whether it works there is unknown"})
	default:
		reasons = append(reasons, reason{ID: whyProcessPartial,
			Detail: "lsof did not list every process of this account (inside the agent sandbox it lists only its own), so whether something works there is unknown"})
	}
	if len(found.holders) > 0 {
		reasons = append(reasons, reason{ID: whyInUse, Detail: "in use: " + named(found.holders)})
	}
	return reasons
}

// pruneRecords removes only records of the requested paths whose folders are still gone.
// Every requested path gets its own outcome, including when the admin record cannot be read.
func pruneRecords(ctx context.Context, main string, paths []string) []action {
	bounded, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	common, err := repo{dir: main}.git().CommonDir(bounded)
	if err != nil {
		return failedPrunes(paths, "git rev-parse failed: "+gitSaid(err, 160))
	}
	folder := filepath.Join(common, "worktrees")
	names, err := os.ReadDir(folder)
	if err != nil {
		return failedPrunes(paths, "could not read worktree records: "+err.Error())
	}
	result := failedPrunes(paths, "no matching worktree record could be verified")
	type record struct{ admin, place, gitdir string }
	matches := make([][]record, len(paths))
	wanted := make(map[string]int, len(paths))
	for index, place := range paths {
		wanted[filepath.Clean(place)] = index
		wanted[realPath(place)] = index
	}
	for _, entry := range names {
		if !entry.IsDir() {
			continue
		}
		admin := filepath.Join(folder, entry.Name())
		pointed, err := os.ReadFile(filepath.Join(admin, "gitdir"))
		if err != nil {
			continue
		}
		gitdir := strings.TrimSpace(string(pointed))
		if filepath.Base(gitdir) != ".git" {
			continue
		}
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(admin, gitdir) // worktree.useRelativePaths
		}
		place := filepath.Dir(filepath.Clean(gitdir))
		index, found := wanted[place]
		if !found {
			index, found = wanted[realPath(place)]
		}
		if found {
			matches[index] = append(matches[index], record{admin: admin, place: place, gitdir: string(pointed)})
		}
	}
	for index, matched := range matches {
		switch len(matched) {
		case 0:
		case 1:
			record := matched[0]
			if err := pruneRecord(bounded, record.admin, record.place, record.gitdir); err != nil {
				result[index].Detail = err.Error()
				continue
			}
			result[index].ID = didPrune
			result[index].Detail = "the folder is still gone"
		default:
			result[index].Detail = "more than one worktree record points to this folder"
		}
	}
	return result
}

func failedPrunes(paths []string, why string) []action {
	result := make([]action, 0, len(paths))
	for _, place := range paths {
		result = append(result, action{ID: didFailPrune, Target: place, Detail: why})
	}
	return result
}

func pruneRecord(ctx context.Context, admin, place, expectedGitdir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(place); err == nil { // #nosec G703 -- inspect the worktree path recorded by Git
		return errors.New("the worktree folder exists again")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking the worktree folder: %w", err)
	}
	if _, err := os.Lstat(filepath.Join(admin, "locked")); err == nil {
		return errors.New("the worktree is now locked")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking the worktree lock: %w", err)
	}
	current, err := os.ReadFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		return fmt.Errorf("rechecking the worktree record: %w", err)
	}
	if string(current) != expectedGitdir {
		return errors.New("the worktree record changed during pruning")
	}
	return os.RemoveAll(admin)
}
