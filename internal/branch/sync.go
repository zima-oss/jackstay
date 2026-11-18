package branch

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/zima-oss/jackstay/internal/remote"
)

const syncUsage = "usage: jst sync [--no-fetch] [--push] [DIR]"

// treeID is an object name of SHA-1 or SHA-256.
var treeID = regexp.MustCompile(`^[0-9a-f]{40}(?:[0-9a-f]{24})?$`)

// syncRequest is what `jst sync` was asked.
type syncRequest struct {
	dir     string
	noFetch bool
	push    bool
}

func runSync(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	var request syncRequest
	var rebase bool
	positional, err := parseArgs(args, map[string]*bool{"--no-fetch": &request.noFetch, "--push": &request.push,
		"--rebase": &rebase}, nil)
	switch {
	case errors.Is(err, errHelp):
		fmt.Fprintln(stdout, syncUsage)
		return 0
	case err == nil && len(positional) > 1:
		err = fmt.Errorf("one folder at most, not %d", len(positional))
	}
	if err != nil {
		fmt.Fprintf(stderr, "jst sync: %v\n%s\n", err, syncUsage)
		return 2
	}
	if rebase {
		// A rebase replays every commit, and one of them may write or delete
		// a file git ignores here although neither end has it. The class is removed, not patched.
		return finish("sync", refused("jst sync merges only; rebase with git yourself (a rebase replays every commit, "+
			"and one of them may write or delete a file git ignores here although neither end has it)"), stderr)
	}
	request.dir = "."
	if len(positional) == 1 {
		request.dir = positional[0]
	}
	return finish("sync", sync(request, stdout), stderr)
}

func sync(request syncRequest, stdout io.Writer) error {
	dir, err := absolute(request.dir)
	if err != nil {
		return refused("%v", err)
	}
	where, err := locate(dir)
	if err != nil {
		return err
	}
	if !request.noFetch {
		if err := where.fetch(stdout); err != nil {
			return err
		}
	}
	base, err := where.defaultBranch(false)
	if err != nil {
		return refused("%v", err)
	}
	here := at(where.root)
	branch, err := here.line("symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || branch == "" {
		return refused("HEAD is detached: check out a branch first")
	}
	if err := here.readyToMerge(); err != nil {
		return err
	}
	var push []string
	if request.push {
		if push, err = here.pushArgs(branch, where.remote); err != nil {
			return err
		}
	}
	// One commit stands for each end from here on: another worktree's fetch moves the shared
	// remote-tracking ref whenever it likes, and the merge must bring in what was checked.
	pinned, err := here.pin(base.Ref)
	if err != nil {
		return err
	}
	// A file git ignores here that the merge would write is work (an ignored `.env`). git guards
	// only part of this itself: `--no-overwrite-ignore` stops a fast-forward, but a three-way merge
	// writes over the file without a word (measured with git 2.54). So one check, before the
	// fast-forward and the merge alike, compares HEAD with the merge's own result; both keep
	// git's flag as well.
	found, err := here.inTheWay(pinned)
	if err != nil {
		return err
	}
	if len(found) > 0 {
		return failed("%d files here that git does not track would be written over by %s: %s — move them aside first",
			len(found), base.Ref, listed(found))
	}
	afterCheck()
	if now, err := here.line("rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil || now != pinned.head {
		return failed("HEAD moved while jst sync looked at it (from %s): nothing merged; run it again", pinned.head)
	}
	if err := here.merge(branch, base, pinned.base, stdout); err != nil {
		return err
	}
	if push == nil {
		return nil
	}
	if _, err := here.run(push...); err != nil {
		return failed("git %s failed: %s", strings.Join(push, " "), gitSaid(err, 300))
	}
	fmt.Fprintf(stdout, "pushed %s (git %s)\n", branch, strings.Join(push, " "))
	return nil
}

// afterCheck runs between the check of ignored files and the merge: nothing, except in a test
// that moves a ref there.
var afterCheck = func() {}

// pinnedEnds are the commits HEAD and the default branch name when a sync begins.
type pinnedEnds struct {
	head, base string
}

// pin reads HEAD and baseRef once, as commits.
func (here repo) pin(baseRef string) (pinnedEnds, error) {
	var ends pinnedEnds
	for _, end := range []struct {
		name   string
		commit *string
	}{{"HEAD", &ends.head}, {baseRef, &ends.base}} {
		commit, err := here.line("rev-parse", "--verify", "--quiet", end.name+"^{commit}")
		if err != nil || !treeID.MatchString(commit) {
			return pinnedEnds{}, failed("%s cannot be read as a commit", end.name)
		}
		*end.commit = commit
	}
	return ends, nil
}

// readyToMerge refuses a merge or rebase under way, uncommitted or untracked work, and files git is
// told not to look at.
func (here repo) readyToMerge() error {
	for _, marker := range []string{"MERGE_HEAD", "rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		path, err := here.line("rev-parse", "--path-format=absolute", "--git-path", marker)
		if err != nil {
			return refused("git rev-parse --git-path failed: %s", gitSaid(err, 200))
		}
		if _, err := os.Lstat(path); err == nil {
			return refused("a merge, rebase, cherry-pick or revert is already under way here (%s): finish it or abort it first", marker)
		}
	}
	status, err := here.run("status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return refused("git status failed: %s", gitSaid(err, 200))
	}
	if dirty := statusPaths(status); len(dirty) > 0 {
		return refused("%d uncommitted or untracked: %s — commit or stash first", len(dirty), listed(dirty))
	}
	// Files git is told not to look at (assume-unchanged, skip-worktree) may hold edits `status`
	// does not show; a merge that touches them writes over them. A sparse checkout marks what it
	// leaves out skip-worktree: those are not edits.
	sparse, _ := here.line("config", "--get", "--bool", "core.sparseCheckout")
	entries, err := here.run("ls-files", "-v", "-z")
	if err != nil {
		return refused("git ls-files failed: %s", gitSaid(err, 200))
	}
	if hidden := hiddenPaths(entries, sparse == "true"); len(hidden) > 0 {
		return refused("%d files are marked assume-unchanged or skip-worktree (%s): their edits do not show and a merge may "+
			"write over them. Clear the marks (`git update-index --no-assume-unchanged --no-skip-worktree <path>`) "+
			"and commit or stash first", len(hidden), listed(hidden))
	}
	return nil
}

// statusPaths are the paths of `git status --porcelain=v1 -z` (a rename's or copy's source, which
// follows it as a word of its own, left out).
func statusPaths(status []byte) []string {
	words := splitNames(status)
	var paths []string
	for index := 0; index < len(words); index++ {
		word := words[index]
		if len(word) < 4 {
			continue
		}
		paths = append(paths, word[3:])
		if word[0] == 'R' || word[0] == 'C' {
			index++
		}
	}
	return paths
}

// hiddenPaths are the entries of `git ls-files -v -z` marked assume-unchanged (a lower-case tag) or
// skip-worktree (`S`, unless the checkout is sparse).
func hiddenPaths(entries []byte, sparse bool) []string {
	var hidden []string
	for _, entry := range splitNames(entries) {
		if len(entry) < 3 {
			continue
		}
		tag := entry[0]
		if (tag == 'S' && !sparse) || (tag >= 'a' && tag <= 'z') {
			hidden = append(hidden, entry[2:])
		}
	}
	return hidden
}

// pushArgs is the one push `--push` makes: this branch to its own upstream (`git push <remote>
// HEAD:<its branch>`), or with `-u` to the remote under its own name the first time — never a bare
// `git push`, which push.default and pushRemote may send elsewhere or with other branches. Decided
// before anything is merged, so a refused push changes nothing.
func (here repo) pushArgs(branch, remoteName string) ([]string, error) {
	theirRemote, _ := here.line("config", "--get", "branch."+branch+".remote")
	theirName, _ := here.line("config", "--get", "branch."+branch+".merge")
	own := "refs/heads/" + branch
	if theirRemote != "" && strings.HasPrefix(theirName, "refs/heads/") {
		if theirName != own {
			// A branch that tracks `origin/main` would push into main: only a branch's own name.
			return nil, refused("--push: %s tracks %s/%s, a branch of another name; jst sync pushes a branch only to its own "+
				"name. Push it yourself (`git push %s %s`) or set its upstream to its own name (`git branch --unset-upstream`)",
				branch, theirRemote, strings.TrimPrefix(theirName, "refs/heads/"), theirRemote, branch)
		}
		return []string{"push", "--quiet", theirRemote, "HEAD:" + own}, nil
	}
	if remoteName == "" {
		return nil, refused("--push: there is no remote")
	}
	return []string{"push", "--quiet", "-u", remoteName, "HEAD:" + own}, nil
}

// merge brings branch up to date with base, as the commit baseCommit read once: a fast-forward
// only on the default branch itself, a merge on any other. A conflict leaves the merge in place, to
// resolve or abort. The merge's message names base.Ref as `git merge <ref>` would.
func (here repo) merge(branch string, base remote.Default, baseCommit string, stdout io.Writer) error {
	behind, err := here.line("rev-list", "--count", "HEAD.."+baseCommit)
	switch {
	case err != nil:
		return failed("git rev-list HEAD..%s failed: %s", base.Ref, gitSaid(err, 200))
	case behind == "0":
		fmt.Fprintf(stdout, "%s already has every commit of %s\n", branch, base.Ref)
		return nil
	case branch == base.Short:
		if _, err := here.run("merge", "--ff-only", "--quiet", "--no-overwrite-ignore", baseCommit); err != nil {
			if overwritten := inTheWaySaid(err, base.Ref); overwritten != nil {
				return overwritten
			}
			return failed("%s and %s have moved apart, so a fast-forward is not possible: %s", branch, base.Ref, gitSaid(err, 200))
		}
		fmt.Fprintf(stdout, "%s fast-forwarded to %s (%s commits)\n", branch, base.Ref, behind)
		return nil
	}
	message, err := here.mergeMessage(base.Ref, baseCommit)
	if err != nil {
		return failed("git fmt-merge-msg failed: %s", gitSaid(err, 200))
	}
	if _, err := here.run("merge", "--no-edit", "--quiet", "--no-overwrite-ignore", "--no-log", "-m", message, baseCommit); err != nil {
		unmerged, listErr := here.run("diff", "-z", "--name-only", "--diff-filter=U")
		conflicted := splitNames(unmerged)
		if listErr != nil || len(conflicted) == 0 {
			if overwritten := inTheWaySaid(err, base.Ref); overwritten != nil {
				return overwritten
			}
			return failed("git merge %s failed: %s", base.Ref, gitSaid(err, 300))
		}
		return failed("the merge of %s stopped at a conflict in: %s. Resolve, `git add` them and `git commit --no-edit`; "+
			"or `git merge --abort`", base.Ref, strings.Join(quoteAll(conflicted), ", "))
	}
	fmt.Fprintf(stdout, "merged %s into %s (%s commits)\n", base.Ref, branch, behind)
	return nil
}

// mergeMessage is the message `git merge <ref>` writes (merge.log and the branch it goes into
// heeded), for ref's commit given as its object name: git's own fmt-merge-msg, fed the line merge
// feeds it.
func (here repo) mergeMessage(ref, commit string) (string, error) {
	kind := "branch"
	if strings.HasPrefix(ref, "refs/remotes/") {
		kind = "remote-tracking branch"
	}
	message, err := here.runInput([]byte(commit+"\t\t"+kind+" '"+ref+"' of .\n"), "fmt-merge-msg")
	return strings.TrimRight(string(message), "\n"), err
}

// inTheWaySaid is git's own refusal to write over files it does not track, or nil.
func inTheWaySaid(err error, ref string) error {
	said := gitSaid(err, 600)
	if !strings.Contains(said, "would be overwritten") {
		return nil
	}
	return failed("files here that git does not track would be written over by %s; git refused and changed nothing — "+
		"move them aside first:\n%s", ref, said)
}

func quoteAll(names []string) []string {
	quoted := make([]string, len(names))
	for index, name := range names {
		quoted[index] = fmt.Sprintf("%q", name)
	}
	return quoted
}

// inTheWay are the files here git does not track that merging ends.base into ends.head would write: a path the merge's
// result adds to HEAD that is there, or has a file on its way, or is a folder holding untracked
// files. The result is the merge itself, made in memory (`git merge-tree --write-tree`, whose tree
// is written also when the merge has conflicts), not ref's tree: a folder ref renamed takes this
// branch's new files with it, to paths neither end has. Names are read as bytes from `-z` lists,
// nothing trimmed; an answer git did not mean stops the sync.
func (here repo) inTheWay(ends pinnedEnds) ([]string, error) {
	cannotTell := func(what string, err error) error {
		return failed("git %s failed: %s; what the merge would write over cannot be told", what, gitSaid(err, 200))
	}
	// --stdin answers 0 for a clean and a conflicted merge alike, with "<clean 1|0>\0<tree>\0…".
	answer, err := here.runInput([]byte(ends.head+" "+ends.base+"\n"), "merge-tree", "--write-tree", "--stdin", "-z", "--name-only", "--no-messages")
	if err != nil {
		return nil, cannotTell("merge-tree "+ends.head+" "+ends.base, err)
	}
	words := strings.SplitN(string(answer), "\x00", 3)
	if len(words) < 2 || (words[0] != "0" && words[0] != "1") || !treeID.MatchString(words[1]) {
		return nil, failed("git merge-tree %s %s gave an answer of another shape; what the merge would write over cannot be told", ends.head, ends.base)
	}
	tree := words[1]
	added, err := here.run("diff", "-z", "--name-only", "--no-renames", "--diff-filter=A", ends.head, tree)
	if err != nil {
		return nil, cannotTell("diff "+ends.head+" "+tree, err)
	}
	headPaths, err := here.run("ls-tree", "-r", "-z", "--name-only", "--full-tree", ends.head)
	if err != nil {
		return nil, cannotTell("ls-tree "+ends.head, err)
	}
	tracked := map[string]bool{}
	for _, name := range splitNames(headPaths) {
		tracked[name] = true
	}
	return obstacles(here.git.Dir, splitNames(added), tracked), nil
}

// obstacles are the untracked things in root that writing the added paths would replace. What git
// tracks is no obstacle: the tree is clean, so the merge itself replaces it (a tracked file where a
// folder arrives, a tracked folder where a file arrives). What it does not track is: something at
// the path, anything untracked inside a folder at the path, and anything but a real folder on the
// path's way (a file, or a link, where a folder must be). A path that cannot be looked at counts.
func obstacles(root string, added []string, tracked map[string]bool) []string {
	var found []string
	for _, name := range added {
		place := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(place)
		switch {
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR): // nothing there; its way is looked at below
		case err != nil:
			found = append(found, name)
		case info.IsDir():
			found = append(found, untrackedInside(root, place, tracked)...)
		case !tracked[name]:
			found = append(found, name)
		}
		parts := strings.Split(name, "/")
		for depth := 1; depth < len(parts); depth++ {
			prefix := strings.Join(parts[:depth], "/")
			info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(prefix)))
			if errors.Is(err, fs.ErrNotExist) {
				break
			}
			if (err != nil || !info.IsDir()) && !tracked[prefix] {
				found = append(found, name)
				break
			}
		}
	}
	return slices.Compact(found)
}

// untrackedInside is every file or link under folder that git does not track (folder itself when
// it cannot be read whole).
func untrackedInside(root, folder string, tracked map[string]bool) []string {
	var found []string
	err := filepath.WalkDir(folder, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if name := filepath.ToSlash(relative); !tracked[name] {
			found = append(found, name)
		}
		return nil
	})
	if err != nil {
		relative, _ := filepath.Rel(root, folder)
		found = append(found, filepath.ToSlash(relative))
	}
	return found
}
