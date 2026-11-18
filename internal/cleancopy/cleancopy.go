// Package cleancopy makes and removes clean copies (W-052, docs/design/discard-and-clean-gate.md
// section 3): a commit checked out where nothing of the checkout's own state reaches it, a
// repository of its own under `<jackstay home>/<project>/clean-copies/<name>/`, reading the
// checkout's objects through its alternates, with the checkout's refs and remotes. It is not a
// worktree: Claude's sandbox keeps `.git/worktrees` read-only, and nothing is written in the
// checkout's repository. `jst test --commit` runs a scope in one.
package cleancopy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/pinned"
)

// OwnerPrefix begins the owner file of every copy, whoever made it: one prefix, so every maker's
// sweep knows every copy's owner.
const OwnerPrefix = "jst test "

// budget bounds the git calls that make a copy.
const budget = 2 * time.Minute

// orphanAge is how old a copy folder with no owner file must be before a sweep removes it (one
// whose maker died between making the folder and writing the file).
const orphanAge = time.Hour

// Git runs git in a folder and returns its output.
type Git func(ctx context.Context, dir string, args ...string) ([]byte, error)

// Request is one copy to make.
type Request struct {
	// Here is the folder the maker was called in, inside the checkout.
	Here string
	// Commit names the commit to copy (a commit, a tag, a branch).
	Commit string
	// Project is the project's name: the copies live in its folder of jackstay's home, and the
	// copy is marked with it (config.CopyMarker).
	Project string
	Now     time.Time
	Git     Git
}

// Copy is a clean copy made.
type Copy struct {
	// Folder is what is removed: the owner file and the copy.
	Folder string
	// Tree is the copy's top.
	Tree string
}

// Remove deletes the copy folder, making read-only folders writable when a first try fails.
func (made *Copy) Remove() {
	if made == nil || made.Folder == "" {
		return
	}
	if os.RemoveAll(made.Folder) == nil {
		return
	}
	_ = filepath.WalkDir(made.Folder, func(place string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(place, 0o700) // #nosec G122 G302 -- a folder of jackstay's own copy (WalkDir follows no link), made removable
		}
		return nil
	})
	_ = os.RemoveAll(made.Folder)
}

// gitQuiet are the options of every git call that makes a copy: no hook runs.
var gitQuiet = []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}

// Make makes the clean copy of the commit request.Commit names, and returns it with the working
// folder inside it that matches request.Here. Its errors are refusals: nothing is left behind.
func Make(request Request) (*Copy, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	git := func(dir string, args ...string) (string, error) {
		out, err := request.Git(ctx, dir, append(append([]string{}, gitQuiet...), args...)...)
		return strings.TrimSpace(string(out)), err
	}
	if request.Commit == "" || strings.HasPrefix(request.Commit, "-") {
		return nil, "", fmt.Errorf("a clean copy takes a commit: '%s'", request.Commit)
	}
	if request.Project == "" {
		return nil, "", errors.New("a clean copy needs the project's name")
	}
	top, err := git(request.Here, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return nil, "", fmt.Errorf("a clean copy needs a git checkout: %s is not in one", request.Here)
	}
	commit, err := git(top, "rev-parse", "--verify", "--quiet", "--end-of-options", request.Commit+"^{commit}")
	if err != nil || commit == "" {
		return nil, "", fmt.Errorf("%s is not a commit here", request.Commit)
	}
	objects, err := git(top, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil || !filepath.IsAbs(objects) {
		return nil, "", fmt.Errorf("git's object folder cannot be found: %s", errorText(err))
	}
	realHere, err := filepath.EvalSymlinks(request.Here)
	if err != nil {
		realHere = request.Here
	}
	relative, err := filepath.Rel(top, realHere)
	if err != nil || strings.HasPrefix(relative, "..") {
		relative = "."
	}
	copies, err := homedir.ProjectPath(request.Project, "clean-copies")
	if err != nil {
		return nil, "", fmt.Errorf("jackstay's home: %w", err)
	}
	Sweep(copies, request.Now, func(words string) bool { return pinned.Alive(OwnerPrefix, words) })
	short := commit
	if len(short) > 12 {
		short = short[:12]
	}
	made := &Copy{Folder: filepath.Join(copies, short+"-"+pinned.RandomName())}
	made.Tree = filepath.Join(made.Folder, filepath.Base(top))
	if err := os.MkdirAll(made.Folder, 0o700); err != nil {
		return nil, "", fmt.Errorf("the copy's folder: %w", err)
	}
	fail := func(what string, err error) (*Copy, string, error) {
		made.Remove()
		return nil, "", fmt.Errorf("the clean copy could not be made (%s): %s", what, errorText(err))
	}
	if err := os.WriteFile(filepath.Join(made.Folder, "owner"), []byte(pinned.OwnWords(OwnerPrefix)+"\n"), 0o600); err != nil {
		return fail("its owner file", err)
	}
	if _, err := git(made.Folder, "init", "--quiet", made.Tree); err != nil {
		return fail("git init", err)
	}
	gitFolder := filepath.Join(made.Tree, ".git")
	if err := os.WriteFile(filepath.Join(gitFolder, "objects", "info", "alternates"), []byte(objects+"\n"), 0o600); err != nil {
		return fail("the alternates file", err)
	}
	if _, err := git(made.Tree, "fetch", "--quiet", "--no-tags", "--update-head-ok", top,
		"+refs/heads/*:refs/heads/*", "+refs/remotes/*:refs/remotes/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return fail("git fetch of the checkout's refs", err)
	}
	if _, err := git(made.Tree, "-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", commit); err != nil {
		return fail("git checkout", err)
	}
	if remotes, err := git(top, "config", "--get-regexp", `^remote\..*\.(url|fetch)$`); err == nil {
		for _, line := range strings.Split(remotes, "\n") {
			key, value, found := strings.Cut(line, " ")
			if found {
				if _, err := git(made.Tree, "config", "--add", key, value); err != nil {
					return fail("the remotes", err)
				}
			}
		}
	}
	if err := os.WriteFile(filepath.Join(gitFolder, config.CopyMarker), []byte(request.Project+"\n"), 0o600); err != nil {
		return fail("the project marker", err)
	}
	return made, filepath.Join(made.Tree, relative), nil
}

func errorText(err error) string {
	if err == nil {
		return "no answer"
	}
	return err.Error()
}

// Sweep removes the copy folders whose owner is gone (a kill, a crash, a reboot), and those with
// no owner file older than orphanAge; it returns what it removed.
func Sweep(copies string, now time.Time, alive func(words string) bool) []string {
	entries, err := os.ReadDir(copies)
	if err != nil {
		return nil
	}
	var removed []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		folder := filepath.Join(copies, entry.Name())
		words, err := os.ReadFile(filepath.Join(folder, "owner")) // #nosec G304 -- jackstay's own file
		switch {
		case errors.Is(err, fs.ErrNotExist):
			info, statErr := entry.Info()
			if statErr != nil || now.Sub(info.ModTime()) < orphanAge {
				continue
			}
		case err != nil || alive(string(words)):
			continue
		}
		made := &Copy{Folder: folder}
		made.Remove()
		if _, err := os.Lstat(folder); errors.Is(err, fs.ErrNotExist) {
			removed = append(removed, folder)
		}
	}
	return removed
}
