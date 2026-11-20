// Package discard is `jst discard PATH…` (W-052, docs/design/discard-and-clean-gate.md section 1):
// each path goes back to its committed state (HEAD), and everything that throws away is kept in
// jackstay's trash first, restorable with `jst trash restore`. It is the guard's advice for dropping
// uncommitted changes, which `git restore` and `git checkout --` would lose for good.
//
// It decides from the disk and HEAD, not from the index alone: what is on disk at a changed path
// is kept unless it is byte for byte HEAD's version, and the index's version is kept when it
// differs from both.
package discard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/trash"
)

const usage = `usage: jst discard PATH...    put each path back to HEAD; what that drops is kept in jackstay's trash`

// emptyTree is git's tree with nothing in it: HEAD of a repository with no commit yet.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// budget is the whole call's limit; git is ended at it.
const budget = 5 * time.Minute

// Exit statuses.
const (
	statusDone    = 0
	statusFailed  = 1
	statusRefused = 2
)

func init() {
	cli.Register(cli.Command{Name: "discard", Summary: "put paths back to HEAD, keeping what that drops in jackstay's trash (undoable)",
		Usage: help, Run: command})
}

func command(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	working, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "jst discard:", err)
		return statusRefused
	}
	folder, err := trash.Folder()
	if err != nil {
		fmt.Fprintln(stderr, "jst discard:", err)
		return statusRefused
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	run := Run{Working: working, Trash: folder, Project: trash.ProjectOf(working), Session: os.Getenv("CLAUDE_SESSION_ID"),
		KeepDays: trash.KeepDays(working), Now: time.Now, Out: stdout, Errors: stderr}
	return run.Discard(ctx, args)
}

// Run is one call: where it runs, the trash it keeps into, and its streams.
type Run struct {
	Working, Trash   string
	Project, Session string
	KeepDays         int
	Now              func() time.Time
	Out, Errors      io.Writer
	repo             repository
}

// A change is one path to put back, as git and the disk show it before anything is done.
type change struct {
	path      string // relative to the top, as git writes it
	source    string // resolved HEAD or the empty tree
	head      *object
	index     *object
	disk      trash.Replaced
	keepDisk  bool
	keepIndex bool
}

// An object is a file as git keeps it: its mode and its blob.
type object struct {
	mode, blob string
}

func (found *object) link() bool       { return found != nil && found.mode == "120000" }
func (found *object) executable() bool { return found != nil && found.mode == "100755" }

func (run Run) say(format string, values ...any) {
	fmt.Fprintf(run.Out, "jst discard: "+format+"\n", values...)
}

func (run Run) refuse(format string, values ...any) int {
	fmt.Fprintf(run.Errors, "jst discard: "+format+"\n", values...)
	fmt.Fprintln(run.Errors, "jst discard: nothing was changed")
	return statusRefused
}

// Discard is the command: every path checked first, then each put back.
func (run Run) Discard(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	} else if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(run.Errors, usage)
		return statusRefused
	}
	if len(args) == 0 {
		fmt.Fprintln(run.Errors, usage)
		return statusRefused
	}
	top, err := topOf(ctx, run.Working)
	if err != nil {
		return run.refuse("%s is not inside a git work tree (%v)", run.Working, err)
	}
	run.repo = repository{top: top}
	var specs []string
	for _, given := range args {
		relative, problem := run.inside(top, given)
		if problem != "" {
			return run.refuse("%s", problem)
		}
		specs = append(specs, relative)
	}
	changes, untracked, problem := run.plan(ctx, specs)
	if problem != "" {
		return run.refuse("%s", problem)
	}
	for index, given := range args {
		if !slices.ContainsFunc(changes, func(each change) bool { return under(each.path, specs[index]) }) &&
			!slices.ContainsFunc(untracked, func(name string) bool { return under(name, specs[index]) }) {
			run.say("nothing uncommitted in %s", given)
		}
	}
	// Pruned first, so nothing this run keeps is pruned by it, whatever keep_days says.
	trash.Prune(run.Trash, run.Now())
	kept := 0
	for _, each := range changes {
		done, err := run.putBack(ctx, each)
		kept += done
		if err != nil {
			fmt.Fprintf(run.Errors, "jst discard: %s: %v\n", each.path, err)
			fmt.Fprintln(run.Errors, "jst discard: stopped here; what was kept is listed above (`jst trash list`), "+
				"and the paths after this one are unchanged")
			return statusFailed
		}
	}
	for _, name := range untracked {
		run.say("%s is untracked, not a change git can put back: left as it is (`jst trash %s` moves it)", name, name)
	}
	if len(changes) > 0 {
		run.say("%d path(s) back to HEAD; %d kept in jackstay's trash for %d days (`jst trash list`)", len(changes), kept, run.KeepDays)
	}
	return statusDone
}

// under says whether a path git names is the spec or inside it.
func under(name, spec string) bool {
	return spec == "." || name == spec || strings.HasPrefix(name, spec+"/")
}

// inside is a given path relative to the top, or why it cannot be used. Its folder's links are
// followed (the top git gives is a real path); the path's last part is not.
func (run Run) inside(top, given string) (string, string) {
	absolute := given
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(run.Working, given)
	}
	absolute = filepath.Clean(absolute)
	folder, base := filepath.Dir(absolute), filepath.Base(absolute)
	if resolved, err := filepath.EvalSymlinks(folder); err == nil {
		folder = resolved
	}
	real := filepath.Join(folder, base)
	if real == top {
		return ".", ""
	}
	relative, err := filepath.Rel(top, real)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", fmt.Sprintf("%s is outside the work tree %s", given, top)
	}
	if relative == ".git" || strings.HasPrefix(relative, ".git/") {
		return "", fmt.Sprintf("%s is inside git's own folder", given)
	}
	return relative, ""
}

// plan is every change under the specs, decided before anything is written, and the untracked
// files under them; or why nothing may be done.
func (run Run) plan(ctx context.Context, specs []string) ([]change, []string, string) {
	repo := run.repo
	if conflicted, err := repo.names(ctx, append([]string{"ls-files", "-u", "-z", "--"}, specs...)...); err != nil {
		return nil, nil, "git ls-files: " + err.Error()
	} else if len(conflicted) > 0 {
		return nil, nil, fmt.Sprintf("%s is in a merge conflict: finish the merge, or take one side with "+
			"`git checkout --ours|--theirs -- <path>`", conflicted[0])
	}
	head := repo.source(ctx)
	unstaged, err := repo.names(ctx, append([]string{"diff", "--name-only", "-z", "--no-renames", "--"}, specs...)...)
	if err != nil {
		return nil, nil, "git diff: " + err.Error()
	}
	staged, err := repo.names(ctx, append([]string{"diff", "--cached", "--name-only", "-z", "--no-renames", head, "--"}, specs...)...)
	if err != nil {
		return nil, nil, "git diff --cached: " + err.Error()
	}
	untrackedAll, err := repo.names(ctx, append([]string{"ls-files", "--others", "--exclude-standard", "-z", "--"}, specs...)...)
	if err != nil {
		return nil, nil, "git ls-files --others: " + err.Error()
	}
	paths := append(append([]string{}, unstaged...), staged...)
	slices.Sort(paths)
	paths = slices.Compact(paths)
	var untracked []string
	for _, name := range untrackedAll {
		if !slices.Contains(paths, name) {
			untracked = append(untracked, name)
		}
	}
	var changes []change
	for _, name := range paths {
		each := change{path: name, source: head}
		if head != emptyTree {
			if each.head, err = repo.object(ctx, "ls-tree", "-z", head, "--", name); err != nil {
				return nil, nil, "git ls-tree: " + err.Error()
			}
		}
		if each.index, err = repo.object(ctx, "ls-files", "-s", "-z", "--", name); err != nil {
			return nil, nil, "git ls-files -s: " + err.Error()
		}
		if each.head != nil && each.head.mode == "160000" || each.index != nil && each.index.mode == "160000" {
			return nil, nil, fmt.Sprintf("%s is a submodule, another repository: discard inside it", name)
		}
		if link := linkInside(repo.top, name); link != "" {
			return nil, nil, fmt.Sprintf("%s is reached through the link %s: discard what it leads to where it is", name, link)
		}
		place := filepath.Join(repo.top, name)
		info, statErr := os.Lstat(place)
		if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			return nil, nil, statErr.Error()
		}
		if statErr == nil && info.IsDir() {
			return nil, nil, fmt.Sprintf("%s is a folder on disk where git has a file: move it yourself first (`jst trash %s`)", name, name)
		}
		each.disk, err = trash.Describe(place)
		if err != nil {
			return nil, nil, err.Error()
		}
		if each.disk.Kind != "none" {
			same, err := repo.sameAs(ctx, each.disk, each.head)
			if err != nil {
				return nil, nil, err.Error()
			}
			each.keepDisk = !same
		}
		if each.index != nil && (each.head == nil || *each.index != *each.head) {
			same := false
			if each.disk.Kind != "none" {
				if same, err = repo.sameAs(ctx, each.disk, each.index); err != nil {
					return nil, nil, err.Error()
				}
			}
			each.keepIndex = !same
		}
		changes = append(changes, each)
	}
	return changes, untracked, ""
}

// putBack keeps what a change drops, then writes the path back from HEAD; it returns how many
// entries it kept.
func (run Run) putBack(ctx context.Context, each change) (int, error) {
	repo := run.repo
	place := filepath.Join(repo.top, each.path)
	if err := repo.recheck(ctx, each, each.disk); err != nil {
		return 0, err
	}
	var staged string
	replacement := trash.Replaced{Kind: "none"}
	// Every path HEAD has is written through a prepared replacement installed against what was
	// seen on disk, even when nothing is kept: a file an editor recreates meanwhile is left alone.
	if each.head != nil {
		parent, err := stagingParent(place)
		if err != nil {
			return 0, fmt.Errorf("preparing the replacement: %w", err)
		}
		folder, err := os.MkdirTemp(parent, ".jackstay-discard-")
		if err != nil {
			return 0, fmt.Errorf("preparing the replacement: %w", err)
		}
		defer func() { _ = os.RemoveAll(folder) }()
		staged, replacement, err = repo.stageReplacement(ctx, each, folder)
		if err != nil {
			return 0, fmt.Errorf("preparing the replacement: %w", err)
		}
		if err := repo.recheck(ctx, each, each.disk); err != nil {
			return 0, err
		}
	}
	var entries []string
	if each.keepIndex {
		data, err := repo.git(ctx, "cat-file", "blob", each.index.blob)
		if err != nil {
			return 0, err
		}
		entry, err := trash.Keep(run.Trash, place, data, each.index.executable(), each.index.link(), run.Project, run.Session, run.KeepDays, run.Now())
		if err != nil {
			return 0, err
		}
		entries = append(entries, entry.ID)
		run.say("%s: the staged version kept as %s (`jst trash restore %s --to <path>`)", each.path, entry.ID, entry.ID)
	}
	if each.keepDisk {
		entry, err := trash.MoveForDiscard(run.Trash, place, run.Project, run.Session, run.KeepDays, run.Now(), each.disk)
		if entry.ID != "" {
			entries = append(entries, entry.ID)
		}
		if err != nil {
			return len(entries), err
		}
		run.say("%s: what was on disk kept as %s (`jst trash restore %s`)", each.path, entry.ID, entry.ID)
	}
	var err error
	expected := each.disk
	if each.keepDisk {
		expected = trash.Replaced{Kind: "none"}
	}
	if len(entries) > 0 {
		err = repo.recheck(ctx, each, expected)
	}
	if err == nil {
		switch {
		case each.head == nil:
			_, err = repo.git(ctx, "rm", "--cached", "--quiet", "--ignore-unmatch", "--", each.path)
		default:
			err = repo.restoreStaged(ctx, each, staged, place, expected)
		}
	}
	if err != nil {
		if each.keepDisk {
			return len(entries), fmt.Errorf("%w; what was on disk is in the trash: `jst trash restore %s` puts it back",
				err, entries[len(entries)-1])
		}
		return len(entries), err
	}
	if !each.keepDisk && !each.keepIndex {
		run.say("%s: written back from HEAD (nothing to keep)", each.path)
	}
	for _, id := range entries {
		if err := trash.Mark(run.Trash, id, "discard", replacement); err != nil {
			return len(entries), err
		}
	}
	return len(entries), nil
}

func (repo repository) restoreStaged(ctx context.Context, each change, staged, place string, expected trash.Replaced) error {
	if err := repo.ensureParent(each.path); err != nil {
		return err
	}
	if _, err := repo.git(ctx, "restore", "--source="+each.source, "--staged", "--", each.path); err != nil {
		return err
	}
	if link := linkInside(repo.top, each.path); link != "" {
		return fmt.Errorf("the index was restored, but the worktree path is now reached through the link %s", link)
	}
	if err := installReplacement(staged, place, expected); err != nil {
		return fmt.Errorf("the index was restored, but the worktree replacement could not be installed: %w", err)
	}
	return nil
}

// installReplacement is replaced in a test to model an editor save during discard.
var installReplacement = trash.InstallDiscardReplacement

// stagingParent stays on the destination volume even when some directories are missing.
func stagingParent(place string) (string, error) {
	for parent := filepath.Dir(place); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("%s is not a directory", parent)
			}
			return parent, nil
		}
		if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(parent) == parent {
			return "", err
		}
	}
}

func (repo repository) ensureParent(path string) error {
	root, err := os.OpenRoot(repo.top)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.MkdirAll(filepath.Dir(path), 0o755)
}

// stageReplacement lets Git apply the worktree's conversions once, without writing
// the destination. The captured identity belongs to the file actually installed.
func (repo repository) stageReplacement(ctx context.Context, each change, folder string) (string, trash.Replaced, error) {
	indexPath, err := repo.git(ctx, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", trash.Replaced{}, err
	}
	index := filepath.Join(folder, "index")
	if err := copyIndex(strings.TrimSuffix(string(indexPath), "\n"), index); err != nil {
		return "", trash.Replaced{}, err
	}
	git := gitcli.Git{Dir: repo.top, Env: append(gitcli.WithoutRepository(os.Environ()), "GIT_INDEX_FILE="+index)}
	if _, err := runGit(ctx, git, "restore", "--source="+each.source, "--staged", "--", each.path); err != nil {
		return "", trash.Replaced{}, err
	}
	prefix := filepath.Join(folder, "checkout") + string(filepath.Separator)
	if _, err := runGit(ctx, git, "checkout-index", "--prefix="+prefix, "--", each.path); err != nil {
		return "", trash.Replaced{}, err
	}
	staged := filepath.Join(prefix, each.path)
	replacement, err := trash.Describe(staged)
	if err == nil && replacement.Kind != "file" && replacement.Kind != "link" {
		err = errors.New("the pinned replacement could not be materialized")
	}
	return staged, replacement, err
}

func copyIndex(source, target string) error {
	input, err := os.Open(source) // #nosec G304 -- Git supplies this checkout's index path
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	return errors.Join(copyErr, output.Close())
}

// recheck detects changes between planning and applying a path, including a file that reappears
// while its previous version is being kept. Git and editors do not share a worktree-wide lock.
func (repo repository) recheck(ctx context.Context, each change, expected trash.Replaced) error {
	if repo.source(ctx) != each.source {
		return errors.New("HEAD changed since discard was planned")
	}
	index, err := repo.object(ctx, "ls-files", "-s", "-z", "--", each.path)
	if err != nil {
		return err
	}
	if (index == nil) != (each.index == nil) || index != nil && *index != *each.index {
		return errors.New("the index changed since discard was planned")
	}
	if link := linkInside(repo.top, each.path); link != "" {
		return fmt.Errorf("the path is now reached through the link %s", link)
	}
	current, err := trash.Describe(filepath.Join(repo.top, each.path))
	if err != nil {
		return err
	}
	if current != expected {
		return errors.New("the working file changed while discard was in progress")
	}
	return nil
}

// topOf is the work tree's top folder (a real path), or an error outside one.
func topOf(ctx context.Context, folder string) (string, error) {
	out, err := gitcli.Git{Dir: folder}.Run(ctx, "rev-parse", "--is-inside-work-tree", "--show-toplevel")
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || lines[0] != "true" {
		return "", errors.New("not in a work tree")
	}
	return lines[1], nil
}

// repository runs git at the top: literal pathspecs, no hooks.
type repository struct{ top string }

func (repo repository) git(ctx context.Context, args ...string) ([]byte, error) {
	return runGit(ctx, gitcli.Git{Dir: repo.top}, args...)
}

func runGit(ctx context.Context, git gitcli.Git, args ...string) ([]byte, error) {
	full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "--literal-pathspecs"}, args...)
	return git.Run(ctx, full...)
}

func (repo repository) source(ctx context.Context) string {
	out, err := repo.git(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return emptyTree
	}
	return strings.TrimSpace(string(out))
}

// names are git's NUL-separated answer.
func (repo repository) names(ctx context.Context, args ...string) ([]string, error) {
	out, err := repo.git(ctx, args...)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, name := range bytes.Split(out, []byte{0}) {
		if len(name) > 0 {
			found = append(found, string(name))
		}
	}
	return found, nil
}

// object is the one entry `ls-tree` or `ls-files -s` gives for a path, or nil.
// ls-tree: "<mode> <type> <blob>\t<path>"; ls-files -s: "<mode> <blob> <stage>\t<path>".
func (repo repository) object(ctx context.Context, args ...string) (*object, error) {
	entries, err := repo.names(ctx, args...)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	fields := strings.Fields(strings.SplitN(entries[0], "\t", 2)[0])
	if len(fields) != 3 {
		return nil, fmt.Errorf("git %s: unexpected %q", args[0], entries[0])
	}
	if args[0] == "ls-tree" {
		return &object{mode: fields[0], blob: fields[2]}, nil
	}
	if fields[2] != "0" {
		return nil, errors.New("the path is in a merge conflict")
	}
	return &object{mode: fields[0], blob: fields[1]}, nil
}

// sameAs says whether what is on disk at place is byte for byte the object: same kind, same
// executable bit, the raw bytes of its blob (no filters: a clean filter could hide a difference).
func (repo repository) sameAs(ctx context.Context, disk trash.Replaced, want *object) (bool, error) {
	if want == nil {
		return false, nil
	}
	kind := "file"
	if want.link() {
		kind = "link"
	}
	if disk.Kind != kind || disk.Executable != want.executable() {
		return false, nil
	}
	blob, err := repo.git(ctx, "cat-file", "blob", want.blob)
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256(blob)
	return disk.SHA256 == hex.EncodeToString(digest[:]), nil
}

// linkInside is the first folder on a path's way below the top that is a symbolic link, or "":
// what is beyond it is not in the work tree, and a move there would take another folder's file.
func linkInside(top, name string) string {
	parts := strings.Split(name, "/")
	current := top
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return ""
		}
		if info.Mode()&os.ModeSymlink != 0 {
			relative, _ := filepath.Rel(top, current)
			return relative
		}
	}
	return ""
}
