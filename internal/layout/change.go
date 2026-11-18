package layout

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
)

// ErrBusy is the install lock held by another install or uninstall.
var ErrBusy = errors.New("another install or uninstall is running")

// Lock takes the install lock without waiting.
func Lock(prefix string) (*flock.File, error) {
	held, err := flock.TryFile(LockPath(prefix))
	if errors.Is(err, flock.ErrBusy) {
		return nil, ErrBusy
	}
	return held, err
}

// LockHeld says whether another process holds the install lock now (false when it cannot be told).
func LockHeld(prefix string) bool {
	if _, err := os.Lstat(LockPath(prefix)); err != nil {
		return false
	}
	held, err := flock.HeldAt(LockPath(prefix))
	return err == nil && held
}

// ReplaceLink puts a link saying target at path in one rename; the partial is removed on every
// failure.
func ReplaceLink(path, target string) error {
	partial := path + PartialSuffix
	if err := os.Remove(partial); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- bin and lib are read by every shell of the account
		return err
	}
	if err := os.Symlink(target, partial); err != nil {
		return err
	}
	if err := os.Rename(partial, path); err != nil {
		_ = os.Remove(partial)
		return err
	}
	return nil
}

// FolderName is a new version folder's name: jackstay-<version>-<commit>-<seconds>-<pid>. "+" in
// a commit becomes "-".
func FolderName(version, commit string, now time.Time) string {
	return fmt.Sprintf("%s%s-%s-%d-%d", FolderPrefix, version, strings.ReplaceAll(commit, "+", "-"), now.Unix(), os.Getpid())
}

// MakeFolder makes a version folder in P/lib: put writes the binary at the path it is given, then
// the other launchers (links to it), VERSION (versionText) and SOURCE are written, and the folder,
// made as `<name>.partial`, is renamed whole.
func MakeFolder(prefix, versionText, source string, now time.Time, put func(binary string) error) (string, error) {
	version, commit, _ := strings.Cut(versionText, " (")
	final := freshPath(prefix, FolderName(version, strings.TrimSuffix(commit, ")"), now))
	partial := final + PartialSuffix
	if err := os.MkdirAll(partial, 0o755); err != nil { // #nosec G301 -- read by every shell of the account
		return "", err
	}
	fail := func(err error) (string, error) {
		_ = os.RemoveAll(partial)
		return "", err
	}
	binary := filepath.Join(partial, Program)
	if err := put(binary); err != nil {
		return fail(err)
	}
	if err := os.Chmod(binary, 0o755); err != nil { // #nosec G302 -- an executable for every shell of the account
		return fail(err)
	}
	for _, name := range Launchers {
		if name == Program {
			continue
		}
		if err := os.Symlink(Program, filepath.Join(partial, name)); err != nil {
			return fail(err)
		}
	}
	for name, text := range map[string]string{VersionFile: versionText + "\n", SourceFile: source + "\n"} {
		if err := os.WriteFile(filepath.Join(partial, name), []byte(text), 0o644); err != nil { // #nosec G306 -- read by every shell of the account
			return fail(err)
		}
	}
	if err := os.Rename(partial, final); err != nil {
		return fail(err)
	}
	return final, nil
}

// CopyBinary copies the file at from to destination (put of MakeFolder for a given binary).
func CopyBinary(from string) func(string) error {
	return func(destination string) error {
		data, err := os.ReadFile(from) // #nosec G304 -- the binary being installed
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0o755) // #nosec G306 G703 -- an executable for every shell of the account
	}
}

// Prune removes every version folder that is neither live nor named by keep, and the leftover
// partial names; it returns the removed paths and what could not be removed.
func Prune(state State, keep string) (removed []string, problems []error) {
	for _, folder := range state.Folders {
		if state.Live != nil && folder.Name == state.Live.Name || folder.Name == keep {
			continue
		}
		if err := os.RemoveAll(folder.Path); err != nil {
			problems = append(problems, err)
			continue
		}
		removed = append(removed, folder.Path)
	}
	for _, path := range state.Partials {
		if err := os.RemoveAll(path); err != nil {
			problems = append(problems, err)
			continue
		}
		removed = append(removed, path)
	}
	return removed, problems
}

// RemoveAll is uninstall's change (docs/design/implementation.md section 7.6): the launchers jackstay wrote, then lib/jackstay,
// then every version folder and leftover partial name. A foreign launcher is kept, and so is the
// lock file.
func RemoveAll(state State) (removed []string, problems []error) {
	remove := func(path string, whole bool) {
		var err error
		if whole {
			err = os.RemoveAll(path)
		} else {
			err = os.Remove(path)
		}
		switch {
		case err == nil:
			removed = append(removed, path)
		case !errors.Is(err, fs.ErrNotExist):
			problems = append(problems, err)
		}
	}
	for _, launcher := range state.Launchers {
		if launcher.Kind == Fixed {
			remove(launcher.Path, false)
		}
	}
	if state.Link == LinkToFolder {
		remove(Link(state.Prefix), false)
	}
	names := make([]string, 0, len(state.Folders))
	for _, folder := range state.Folders {
		names = append(names, folder.Path)
	}
	names = append(names, state.Partials...)
	sort.Strings(names)
	for _, path := range names {
		remove(path, true)
	}
	return removed, problems
}

// ReadSource is a folder's SOURCE ("" when there is none).
func ReadSource(folder string) string {
	data, err := os.ReadFile(filepath.Join(folder, SourceFile)) // #nosec G304 -- a version folder's own file
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// RemoveFolder removes a folder that never became live (a failed proof).
func RemoveFolder(path string) { _ = os.RemoveAll(path) }

// freshPath is P/lib/name, or name with "-2", "-3"… when a folder of that name (or its partial) is
// there: two folders made by one process in one second never collide.
func freshPath(prefix, name string) string {
	path := filepath.Join(Lib(prefix), name)
	for count := 2; exists(path) || exists(path+PartialSuffix); count++ {
		path = filepath.Join(Lib(prefix), fmt.Sprintf("%s-%d", name, count))
	}
	return path
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
