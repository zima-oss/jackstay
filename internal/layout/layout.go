// Package layout is the installed jackstay under a prefix P (docs/design/implementation.md section 7.1):
// the launchers P/bin/jst, P/bin/jackstay and P/bin/jst-guard, each a fixed link into the
// activation point P/lib/jackstay, the version folders beside it, and the install lock. Read is
// the one reader of a prefix; install, uninstall and doctor all decide from the State it gives,
// and every change is a name made beside its place (`<name>.partial`) and put there by one
// rename(2).
package layout

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Names inside a prefix.
const (
	// LinkName is P/lib/jackstay, the one activation point.
	LinkName = "jackstay"
	// FolderPrefix begins every version folder's name in P/lib.
	FolderPrefix = "jackstay-"
	// Program is the binary in a version folder; the other launchers are links to it.
	Program = "jst"
	// Guard is the hook's name: the binary called by it is `jst guard`.
	Guard = "jst-guard"
	// LockName is the install lock in P/lib, shared by install and uninstall.
	LockName = ".jackstay-install.lock"
	// PartialSuffix marks a name being made beside its place.
	PartialSuffix = ".partial"
	// VersionFile holds a folder's version text, "0.5.0 (1a2b3c4)".
	VersionFile = "VERSION"
	// SourceFile names the checkout a folder was built from, "binary FILE" or "release".
	SourceFile = "SOURCE"
)

// Launchers are the entry points in P/bin: the command, its long name, and the hook.
var Launchers = []string{Program, LinkName, Guard}

// LauncherKind is what a launcher is.
type LauncherKind int

// The kinds of launchers.
const (
	Absent LauncherKind = iota
	Fixed
	Foreign
)

func (kind LauncherKind) String() string {
	return [...]string{"absent", "fixed", "foreign"}[kind]
}

// Folder is a version folder in P/lib.
type Folder struct {
	Name, Path string
	// Version is VERSION's text.
	Version string
	// Made is the folder's own time: it is renamed into place whole and never written again.
	Made time.Time
}

// Launcher is one of P/bin's launchers.
type Launcher struct {
	Name, Path string
	Kind       LauncherKind
}

// LinkState is what P/lib/jackstay is.
type LinkState int

// The states of P/lib/jackstay.
const (
	LinkAbsent LinkState = iota
	LinkToFolder
	// LinkElsewhere is a link to something that is not a version folder in P/lib.
	LinkElsewhere
	// LinkNotALink is a plain file or a folder at P/lib/jackstay.
	LinkNotALink
)

// State is a prefix as it is now.
type State struct {
	Prefix    string
	Launchers []Launcher
	Link      LinkState
	// LinkTarget is what lib/jackstay names, as written.
	LinkTarget string
	// Live is the folder lib/jackstay names (nil unless Link is LinkToFolder).
	Live *Folder
	// Folders are the version folders in P/lib, the live one included, by name.
	Folders []Folder
	// Partials are leftover `.partial` names in P/bin and P/lib.
	Partials []string
}

// Lib is P/lib.
func Lib(prefix string) string { return filepath.Join(prefix, "lib") }

// Bin is P/bin.
func Bin(prefix string) string { return filepath.Join(prefix, "bin") }

// Link is P/lib/jackstay.
func Link(prefix string) string { return filepath.Join(Lib(prefix), LinkName) }

// LockPath is the install lock.
func LockPath(prefix string) string { return filepath.Join(Lib(prefix), LockName) }

// FixedTarget is what a fixed launcher link says: ../lib/jackstay/<name>.
func FixedTarget(name string) string { return "../lib/" + LinkName + "/" + name }

// Read reads the prefix. A prefix with no lib or bin folder is read as empty.
func Read(prefix string) (State, error) {
	state := State{Prefix: prefix}
	for _, name := range Launchers {
		launcher, err := readLauncher(filepath.Join(Bin(prefix), name))
		if err != nil {
			return state, err
		}
		state.Launchers = append(state.Launchers, launcher)
	}
	entries, err := os.ReadDir(Lib(prefix))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return state, err
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(Lib(prefix), name)
		switch {
		case name == LinkName:
		case isOurPartial(name):
			state.Partials = append(state.Partials, path)
		case strings.HasPrefix(name, FolderPrefix) && entry.IsDir():
			if folder, ok := ReadFolder(path); ok {
				state.Folders = append(state.Folders, folder)
			}
		}
	}
	binEntries, err := os.ReadDir(Bin(prefix))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return state, err
	}
	for _, entry := range binEntries {
		for _, name := range Launchers {
			if entry.Name() == name+PartialSuffix {
				state.Partials = append(state.Partials, filepath.Join(Bin(prefix), entry.Name()))
			}
		}
	}
	if err := state.readLink(); err != nil {
		return state, err
	}
	return state, nil
}

// isOurPartial is a `.partial` name an install makes in P/lib: lib/jackstay's or a version folder's.
func isOurPartial(name string) bool {
	return name == LinkName+PartialSuffix || strings.HasPrefix(name, FolderPrefix) && strings.HasSuffix(name, PartialSuffix)
}

func (state *State) readLink() error {
	path := Link(state.Prefix)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		state.Link = LinkAbsent
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		state.Link = LinkNotALink
		return nil
	}
	target, err := os.Readlink(path)
	if err != nil {
		return err
	}
	state.LinkTarget = target
	state.Link = LinkElsewhere
	if strings.Contains(target, "/") {
		return nil
	}
	for index := range state.Folders {
		if state.Folders[index].Name == target {
			state.Link = LinkToFolder
			state.Live = &state.Folders[index]
		}
	}
	return nil
}

func readLauncher(path string) (Launcher, error) {
	launcher := Launcher{Name: filepath.Base(path), Path: path}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return launcher, nil
	}
	if err != nil {
		return launcher, err
	}
	launcher.Kind = Foreign
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return launcher, err
		}
		if target == FixedTarget(launcher.Name) {
			launcher.Kind = Fixed
		}
	}
	return launcher, nil
}

// ReadFolder reads a folder of P/lib; false when it is not a whole version folder: a VERSION,
// the program, and each other launcher a link to it.
func ReadFolder(path string) (Folder, bool) {
	folder := Folder{Name: filepath.Base(path), Path: path}
	data, err := os.ReadFile(filepath.Join(path, VersionFile)) // #nosec G304 -- a version folder's own file
	if err != nil {
		return folder, false
	}
	folder.Version = strings.TrimSpace(string(data))
	if folder.Version == "" || !isExecutable(filepath.Join(path, Program)) {
		return folder, false
	}
	for _, name := range Launchers {
		if name != Program && !linksTo(filepath.Join(path, name), Program) {
			return folder, false
		}
	}
	if info, err := os.Lstat(path); err == nil {
		folder.Made = info.ModTime()
	}
	return folder, true
}

func isExecutable(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func linksTo(path, target string) bool {
	written, err := os.Readlink(path)
	return err == nil && written == target
}

// Refusal is why a prefix is not jackstay's to change: a launcher or lib/jackstay that is
// someone else's.
func (state State) Refusal() error {
	var found []string
	for _, launcher := range state.Launchers {
		if launcher.Kind == Foreign {
			found = append(found, launcher.Path+" is not a launcher jackstay wrote")
		}
	}
	switch state.Link {
	case LinkNotALink:
		found = append(found, Link(state.Prefix)+" is not a link")
	case LinkElsewhere:
		found = append(found, fmt.Sprintf("%s names %q, which is not a version folder of jackstay's", Link(state.Prefix), state.LinkTarget))
	}
	if len(found) == 0 {
		return nil
	}
	return errors.New(strings.Join(found, "; "))
}

// AllFixed says every launcher is its fixed link.
func (state State) AllFixed() bool {
	for _, launcher := range state.Launchers {
		if launcher.Kind != Fixed {
			return false
		}
	}
	return true
}

// Before is the version before: the newest folder that is not live and holds another version
// (nil when there is none). It is kept, so a run that started with it finishes with it.
func (state State) Before() *Folder {
	var before *Folder
	for index := range state.Folders {
		folder := &state.Folders[index]
		if state.Live != nil && (folder.Name == state.Live.Name || folder.Version == state.Live.Version) {
			continue
		}
		if before == nil || folder.Made.After(before.Made) {
			before = folder
		}
	}
	return before
}

// LiveVersion is the live folder's VERSION text ("" when nothing is live).
func (state State) LiveVersion() string {
	if state.Live == nil {
		return ""
	}
	return state.Live.Version
}

// RunningPrefix is the prefix the running binary is installed under: the P of a launcher
// P/bin/<launcher> that is an install's own (a link to ../lib/jackstay/<launcher>) and leads to the
// running binary, found along the chain of links the binary was started through, one link at a
// time; else the P its own path names (P/lib/jackstay-*/jst). An alias such as
// /usr/local/bin/jst -> /opt/jackstay/bin/jst is no install's launcher, so /opt/jackstay is found;
// a prefix whose lib links elsewhere is found by its launcher; an install that stopped before
// writing jst-guard is still found (the caller asks whether its guard launcher runs). False when
// the binary runs from no install (a checkout's build, a download not yet installed).
func RunningPrefix() (string, bool) {
	program, err := os.Executable()
	if err != nil {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(program)
	if err != nil {
		return "", false
	}
	return installPrefix(launcherChain(startedAs()), program, resolved)
}

// installPrefix is the prefix of the first install launcher on chain that leads to resolved, else
// the prefix program's path names, before or after its links are followed.
func installPrefix(chain []string, program, resolved string) (string, bool) {
	for _, path := range chain {
		prefix, named := prefixOfLauncherPath(path)
		if !named {
			continue
		}
		launcher, err := readLauncher(path)
		if err != nil || launcher.Kind != Fixed {
			continue
		}
		if leads, err := filepath.EvalSymlinks(path); err == nil && leads == resolved {
			return prefix, true
		}
	}
	if prefix, named := prefixOfProgram(program); named {
		return prefix, true
	}
	return prefixOfProgram(resolved)
}

// launcherChain is path and every path its links lead to, one link at a time (at most 16).
func launcherChain(path string) []string {
	var chain []string
	for range 16 {
		if path == "" {
			break
		}
		chain = append(chain, path)
		target, err := os.Readlink(path)
		if err != nil {
			break
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = filepath.Clean(target)
	}
	return chain
}

// startedAs is the path the binary was started through, when argv[0] names one: a path, or a name
// found on PATH.
func startedAs() string {
	if len(os.Args) == 0 || os.Args[0] == "" {
		return ""
	}
	name := os.Args[0]
	if !strings.ContainsRune(name, filepath.Separator) {
		found, err := exec.LookPath(name)
		if err != nil {
			return ""
		}
		name = found
	}
	absolute, err := filepath.Abs(name)
	if err != nil {
		return ""
	}
	return absolute
}

// prefixOfLauncherPath is P for a path P/bin/<name> whose name is one of Launchers.
func prefixOfLauncherPath(path string) (string, bool) {
	if filepath.Base(filepath.Dir(path)) != "bin" || !slices.Contains(Launchers, filepath.Base(path)) {
		return "", false
	}
	return filepath.Dir(filepath.Dir(path)), true
}

// prefixOfProgram is P for a program at P/lib/jackstay-*/jst.
func prefixOfProgram(program string) (string, bool) {
	folder := filepath.Dir(program)
	if !strings.HasPrefix(filepath.Base(folder), FolderPrefix) || filepath.Base(filepath.Dir(folder)) != "lib" {
		return "", false
	}
	return filepath.Dir(filepath.Dir(folder)), true
}

// GuardLauncher is the hook's launcher under a prefix, P/bin/jst-guard.
func GuardLauncher(prefix string) string { return filepath.Join(Bin(prefix), Guard) }
