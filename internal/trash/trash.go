// Package trash is `jst trash`: the recorded way to delete inside the agent sandbox (W-042, W-107,
// docs/design/guard.md section 10). An item is moved into `<jackstay home>/.trash/`, one entry
// each. By default (`trash.keep` off) the entry is then deleted and the deletion logged (path,
// size, digest, session, command); with `trash.keep` on (experimental) it is kept and can be put
// back, and entries older than the project's `trash.keep_days` (7 by default, W-043) are removed
// by its next `jst trash`.
package trash

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/homedir"
)

// DefaultKeepDays is how long an entry is kept when the project does not say (W-043).
const DefaultKeepDays = 7

const usage = `usage: jst trash PATH...                      delete each and log it (kept, to restore, with trash.keep on)
       jst trash list [--json]                 the kept entries and the logged deletions, newest first
       jst trash restore ENTRY [--to PATH]     put a kept item back (refused when something is there)
       jst trash prune                         remove kept entries older than trash.keep_days`

func init() {
	cli.Register(cli.Command{Name: "trash", Summary: "delete files and folders with a log, or keep them to restore (trash.keep); list, restore, prune",
		Usage: help, Run: Command})
}

// An Entry is one item in the trash.
type Entry struct {
	ID      string `json:"-"`
	Path    string `json:"path"`
	MovedAt string `json:"moved_at"`
	Project string `json:"project"`
	Session string `json:"session"`
	Kind    string `json:"kind"`
	// Via is the command that kept it when not `jst trash` ("discard", W-052).
	Via string `json:"via,omitempty"`
	// Replaced is what `jst discard` wrote in the item's place: a restore replaces only that.
	Replaced *Replaced `json:"replaced,omitempty"`
	// KeepDays is the `trash.keep_days` of the project the item came from, when it came: pruning
	// reads it here, never from whichever project is working when the trash is pruned.
	KeepDays *int `json:"keep_days,omitempty"`
	// Held marks an entry whose move is not whole: one under way, or one whose original was removed
	// only in part (its checked copy may be the only whole one). No pruning removes it; a restore does.
	Held bool `json:"held,omitempty"`
}

// Replaced is an item on disk as `jst discard` left it: its kind (file, link, none), its executable
// bit, and the SHA-256 of its bytes or of a link's target.
type Replaced struct {
	Kind       string `json:"kind"`
	Executable bool   `json:"executable"`
	SHA256     string `json:"sha256"`
}

// Describe is what is at place now, read without following a link.
func Describe(place string) (Replaced, error) {
	info, err := os.Lstat(place)
	if errors.Is(err, fs.ErrNotExist) {
		return Replaced{Kind: "none"}, nil
	}
	if err != nil {
		return Replaced{}, err
	}
	var data []byte
	kind := "file"
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		kind = "link"
		target, err := os.Readlink(place)
		if err != nil {
			return Replaced{}, err
		}
		data = []byte(target)
	case info.Mode().IsRegular():
		if data, err = os.ReadFile(place); err != nil { // #nosec G304 -- the path jst discard wrote
			return Replaced{}, err
		}
	default:
		return Replaced{Kind: "other"}, nil
	}
	digest := sha256.Sum256(data)
	return Replaced{Kind: kind, Executable: kind == "file" && info.Mode().Perm()&0o111 != 0,
		SHA256: hex.EncodeToString(digest[:])}, nil
}

// Command is `jst trash`.
func Command(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	folder, err := Folder()
	if err != nil {
		fmt.Fprintln(stderr, "jst trash:", err)
		return 2
	}
	switch args[0] {
	case "list":
		return list(folder, args[1:], stdout, stderr)
	case "restore":
		return restore(folder, args[1:], stdout, stderr)
	case "prune":
		removed := Prune(folder, time.Now())
		fmt.Fprintf(stdout, "jst trash: %d entries removed\n", removed)
		return 0
	}
	paths := args
	if paths[0] == "--" {
		paths = paths[1:]
	}
	// Pruned first, so nothing this call moves is pruned by it, whatever keep_days says.
	Prune(folder, time.Now())
	// One setting for every path, from the project the command runs in, as the guard reads it.
	working, _ := os.Getwd()
	keep := Keeps(working)
	status := 0
	for _, given := range paths {
		origin := Origin(given)
		if !keep {
			deletion, err := Delete(folder, given, ProjectOf(origin), Session(), "jst trash", time.Now())
			if err != nil {
				fmt.Fprintln(stderr, "jst trash:", err)
				status = 1
				continue
			}
			fmt.Fprintf(stdout, "jst trash: %s deleted, %s (logged: `jst trash list`)\n", deletion.Path, Measure(deletion))
			continue
		}
		entry, err := Move(folder, given, ProjectOf(origin), Session(), KeepDays(origin), time.Now())
		if err != nil {
			fmt.Fprintln(stderr, "jst trash:", err)
			status = 1
			continue
		}
		fmt.Fprintf(stdout, "jst trash: %s -> %s (jst trash restore %s)\n", entry.Path, entry.ID, entry.ID)
	}
	return status
}

// Folder is `<jackstay home>/.trash`.
func Folder() (string, error) {
	home, err := homedir.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".trash"), nil
}

// Origin is where an item's project and keep_days are read, not the folder the command runs in: a
// real folder from inside itself (a whole checkout carries its own jackstay.json), anything else
// from the folder that holds it.
func Origin(given string) string {
	absolute, err := filepath.Abs(given)
	if err != nil {
		return filepath.Dir(given)
	}
	if info, err := os.Lstat(absolute); err == nil && info.IsDir() {
		return absolute
	}
	return filepath.Dir(absolute)
}

// ProjectOf is the project a folder is in, for an entry's record.
func ProjectOf(folder string) string {
	root, loaded, err := config.Load(folder, []string{"trash"})
	if err != nil {
		root, _ = config.Find(folder)
		loaded = config.Config{}
	}
	return config.ProjectName(root, loaded, folder)
}

// KeepDays is the project's `trash.keep_days`, or the default.
func KeepDays(folder string) int {
	_, loaded, err := config.Load(folder, []string{"trash"})
	if err != nil {
		return DefaultKeepDays
	}
	// jackstay.json's numbers are json.Numbers (config.Parse).
	if number, ok := loaded.Section("trash")["keep_days"].(json.Number); ok {
		if days, err := number.Int64(); err == nil && days >= 0 {
			return int(days)
		}
	}
	return DefaultKeepDays
}

// Move puts one file or folder into the trash and records where it came from.
// keepDays is the project's `trash.keep_days`, recorded on the entry.
func Move(folder, given, project, session string, keepDays int, now time.Time) (Entry, error) {
	return move(folder, given, project, session, keepDays, now, nil)
}

// MoveForDiscard verifies what was moved and holds it until Mark records the replacement.
func MoveForDiscard(folder, given, project, session string, keepDays int, now time.Time, expected Replaced) (Entry, error) {
	return move(folder, given, project, session, keepDays, now, &expected)
}

func move(folder, given, project, session string, keepDays int, now time.Time, expected *Replaced) (Entry, error) {
	source, err := filepath.Abs(given)
	if err != nil {
		return Entry{}, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return Entry{}, fmt.Errorf("%s: %w", given, err)
	}
	if err := holdsHome(folder, given, source); err != nil {
		return Entry{}, err
	}
	kind := "file"
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		kind = "link"
	case info.IsDir():
		kind = "folder"
	}
	// Held until the move is whole: no pruning, by this command or another, can take a copy whose
	// original may be going; a move that stops part way leaves the entry held.
	entry := Entry{Path: source, MovedAt: now.Format(time.RFC3339), Project: project, Session: session, Kind: kind,
		KeepDays: &keepDays, Held: true}
	place, locked, err := newEntry(folder, &entry, now)
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = locked.Close() }()
	item := filepath.Join(place, "item", filepath.Base(source))
	if err := moveItem(source, item); err != nil {
		var left *originalLeftError
		if errors.As(err, &left) {
			// The checked copy may be the only whole one now: the entry stays held.
			return entry, fmt.Errorf("%s: copied into the trash as %s, but the original was not removed in full: %w",
				given, entry.ID, left.cause)
		}
		_ = os.RemoveAll(place)
		return Entry{}, err
	}
	if expected != nil {
		actual, err := Describe(item)
		if err != nil {
			return entry, fmt.Errorf("the file was kept as %s, but cannot be verified: %w", entry.ID, err)
		}
		if actual != *expected {
			return entry, fmt.Errorf("the working file changed during the move; its changed contents are kept as %s (`jst trash restore %s`)", entry.ID, entry.ID)
		}
		return entry, nil
	}
	entry.Held = false
	if err := writeRecord(place, entry); errors.Is(err, durable.ErrNotSynced) {
		// The record is released, and prunable, but a crash may bring the held one back.
		return entry, fmt.Errorf("the file was kept as %s, but its record may not survive a crash (`jst trash restore %s`): %w", entry.ID, entry.ID, err)
	} else if err != nil {
		entry.Held = true // the record on disk still holds it: kept until restored, never lost
	}
	return entry, nil
}

// holdsHome refuses the jackstay home, which holds the trash, and a folder that holds it.
func holdsHome(folder, given, source string) error {
	home := filepath.Dir(folder)
	realSource := source
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(source)); err == nil {
		realSource = filepath.Join(resolved, filepath.Base(source))
	}
	realHome := home
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		realHome = resolved
	}
	if realSource == realHome || strings.HasPrefix(realHome+"/", realSource+"/") {
		return fmt.Errorf("%s holds jackstay's own folder %s: not moved", given, home)
	}
	return nil
}

// newEntry makes an entry's folder and writes its record under a lock; entry.ID is set.
func newEntry(folder string, entry *Entry, now time.Time) (string, *os.File, error) {
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return "", nil, err
	}
	stamp := now.UTC().Format("20060102-150405")
	var place string
	for number := 1; ; number++ {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", nil, fmt.Errorf("make trash entry ID: %w", err)
		}
		place = filepath.Join(folder, fmt.Sprintf("%s-%d-%x", stamp, number, nonce))
		err := os.Mkdir(place, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || number > 10000 {
			return "", nil, err
		}
	}
	locked, err := lockEntry(place)
	if err != nil {
		_ = os.RemoveAll(place)
		return "", nil, err
	}
	entry.ID = filepath.Base(place)
	if err := writeRecord(place, *entry); err != nil {
		_ = os.RemoveAll(place)
		_ = locked.Close()
		return "", nil, err
	}
	return place, locked, nil
}

// writeAtomic is durable.WriteAtomic; a test makes it fail after the rename.
var writeAtomic = durable.WriteAtomic

func writeRecord(place string, entry Entry) error {
	record, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(place, "entry.json"), append(record, '\n'), 0o600, 0)
}

// Keep makes an entry from bytes: an item that is not on disk (the staged version of a file, kept
// by `jst discard`). link makes the item a symbolic link whose target is data.
func Keep(folder, path string, data []byte, executable, link bool, project, session string, keepDays int,
	now time.Time) (Entry, error) {
	entry := Entry{Path: path, MovedAt: now.Format(time.RFC3339), Project: project, Session: session, Kind: "staged",
		Via: "discard", KeepDays: &keepDays, Held: true}
	place, locked, err := newEntry(folder, &entry, now)
	if err != nil {
		return Entry{}, err
	}
	defer func() { _ = locked.Close() }()
	item := filepath.Join(place, "item", filepath.Base(path))
	if err := os.MkdirAll(filepath.Dir(item), 0o700); err != nil {
		_ = os.RemoveAll(place)
		return Entry{}, err
	}
	mode := fs.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	if link {
		err = os.Symlink(string(data), item)
	} else {
		err = os.WriteFile(item, data, mode) // #nosec G306 -- the file's own mode, as git keeps it
	}
	if err != nil {
		_ = os.RemoveAll(place)
		return Entry{}, err
	}
	return entry, nil
}

// Mark records on an entry what replaced its item and the command that kept it.
func Mark(folder, id, via string, replaced Replaced) error {
	place := filepath.Join(folder, id)
	locked, err := lockEntry(place)
	if err != nil {
		return err
	}
	defer func() { _ = locked.Close() }()
	entry, err := readEntry(place)
	if err != nil {
		return err
	}
	entry.Via, entry.Replaced, entry.Held = via, &replaced, false
	return writeRecord(place, entry)
}

// renameItem is os.Rename and removeOriginal os.RemoveAll; a test makes the one answer as across
// volumes and runs a prune inside the other.
var renameItem, removeOriginal = os.Rename, os.RemoveAll
var exchangeRestore = exchangeFiles

// moveItem renames; across volumes it copies, checks the copy, and only then removes the original.
func moveItem(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	err := renameItem(source, target)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyTree(source, target); err != nil {
		_ = os.RemoveAll(target)
		return fmt.Errorf("copying %s: %w (the original is kept)", source, err)
	}
	if err := sameTree(source, target); err != nil {
		_ = os.RemoveAll(target)
		return fmt.Errorf("the copy of %s differs: %w (the original is kept)", source, err)
	}
	if err := removeOriginal(source); err != nil {
		return &originalLeftError{cause: err}
	}
	return nil
}

// originalLeftError is a cross-volume move whose checked copy is in place but whose original was
// removed only in part: the copy must be kept.
type originalLeftError struct{ cause error }

func (left *originalLeftError) Error() string { return "removing the original: " + left.cause.Error() }

func (left *originalLeftError) Unwrap() error { return left.cause }

func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(place string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, place)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		info, err := os.Lstat(place)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			leads, err := os.Readlink(place)
			if err != nil {
				return err
			}
			return os.Symlink(leads, destination) // #nosec G122 -- the link is copied as a link, never followed
		case info.IsDir():
			return os.MkdirAll(destination, info.Mode().Perm()|0o700)
		default:
			return copyFile(place, destination, info.Mode().Perm())
		}
	})
}

func copyFile(source, target string, mode fs.FileMode) error {
	input, err := os.Open(source) // #nosec G304 -- the item the person names
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) // #nosec G304 -- inside the trash
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

// sameTree checks a copy: every entry there, of the same kind, size and mode.
func sameTree(source, target string) error {
	return filepath.WalkDir(source, func(place string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(source, place)
		original, err := os.Lstat(place)
		if err != nil {
			return err
		}
		copied, err := os.Lstat(filepath.Join(target, relative))
		if err != nil {
			return err
		}
		if original.Mode() != copied.Mode() && original.Mode().Type() != copied.Mode().Type() {
			return fmt.Errorf("%s is of another kind", relative)
		}
		if original.Mode().IsRegular() && (original.Size() != copied.Size() || original.Mode().Perm() != copied.Mode().Perm()) {
			return fmt.Errorf("%s differs in size or mode", relative)
		}
		return nil
	})
}

// Entries are the trash's entries, newest first.
func Entries(folder string) []Entry {
	names, err := os.ReadDir(folder)
	if err != nil {
		return nil
	}
	var found []Entry
	for _, name := range names {
		entry, err := readEntry(filepath.Join(folder, name.Name()))
		if err == nil {
			found = append(found, entry)
		}
	}
	sort.Slice(found, func(first, second int) bool { return found[first].ID > found[second].ID })
	return found
}

func readEntry(place string) (Entry, error) {
	var entry Entry
	data, err := os.ReadFile(filepath.Join(place, "entry.json")) // #nosec G304 -- inside the trash
	if err != nil {
		return entry, err
	}
	err = json.Unmarshal(data, &entry)
	entry.ID = filepath.Base(place)
	return entry, err
}

// Lock the directory itself so a pruned entry is never recreated just to hold a lock.
func lockEntry(place string) (*os.File, error) {
	file, err := os.Open(place) // #nosec G304 -- inside the trash
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil { // #nosec G115 -- a file descriptor
		_ = file.Close()
		return nil, err
	}
	opened, openErr := file.Stat()
	current, statErr := os.Stat(place)
	if openErr != nil || statErr != nil || !os.SameFile(opened, current) {
		_ = file.Close()
		return nil, fs.ErrNotExist
	}
	return file, nil
}

func list(folder string, args []string, stdout, stderr io.Writer) int {
	asJSON := slices.Equal(args, []string{"--json"})
	if len(args) > 0 && !asJSON {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	entries, deletions := Entries(folder), Deletions(folder)
	if asJSON {
		type shown struct {
			ID string `json:"id"`
			Entry
		}
		kept := make([]shown, 0, len(entries))
		for _, entry := range entries {
			kept = append(kept, shown{ID: entry.ID, Entry: entry})
		}
		if deletions == nil {
			deletions = []Deletion{}
		}
		data, _ := json.MarshalIndent(map[string]any{"kept": kept, "deleted": deletions}, "", "  ")
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	for _, entry := range entries {
		fmt.Fprintf(stdout, "%s  %-6s  %s  (%s)\n", entry.ID, entry.Kind, entry.Path, entry.Project)
	}
	for _, deletion := range deletions {
		fmt.Fprintf(stdout, "deleted %s  %-6s  %s  %s  (%s, %s)\n", deletion.DeletedAt, deletion.Kind, deletion.Path,
			Measure(deletion), deletion.Command, deletion.Project)
	}
	return 0
}

func restore(folder string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 && (len(args) != 3 || args[1] != "--to") {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	id := args[0]
	if id == "" || strings.Contains(id, "/") || id == "." || id == ".." {
		fmt.Fprintln(stderr, "jst trash: no entry", id)
		return 1
	}
	place := filepath.Join(folder, id)
	locked, err := lockEntry(place)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stderr, "jst trash: no entry", id, "(with trash.keep off, nothing is kept to restore)")
		} else {
			fmt.Fprintf(stderr, "jst trash: cannot lock entry %s: %v\n", id, err)
		}
		return 1
	}
	defer func() { _ = locked.Close() }()
	entry, err := readEntry(place)
	if err != nil {
		fmt.Fprintln(stderr, "jst trash: no entry", id)
		return 1
	}
	target, expected := entry.Path, entry.Replaced
	if len(args) == 3 {
		expected = nil
		if target, err = filepath.Abs(args[2]); err != nil {
			fmt.Fprintln(stderr, "jst trash:", err)
			return 1
		}
	}
	if err := restoreItem(filepath.Join(place, "item", filepath.Base(entry.Path)), target, expected); err != nil {
		fmt.Fprintln(stderr, "jst trash:", err)
		return 1
	}
	_ = os.RemoveAll(place)
	fmt.Fprintf(stdout, "jst trash: %s restored to %s\n", id, target)
	if entry.Kind == "staged" {
		fmt.Fprintln(stdout, "jst trash: it was a staged version: it is back as a working file, not staged (`git add` stages it)")
	}
	return 0
}

// restoreItem keeps both the current target and the trash entry until the replacement is ready.
// A cross-volume copy is staged next to the target so the last change is always a rename.
func restoreItem(source, target string, expected *Replaced) error {
	install := func(candidate string) error {
		if _, err := os.Lstat(target); errors.Is(err, fs.ErrNotExist) {
			if err := renameAbsent(candidate, target); !errors.Is(err, fs.ErrExist) {
				return err
			}
		} else if err != nil {
			return err
		} else if expected != nil && (expected.Kind == "file" || expected.Kind == "link") {
			current, err := Describe(target)
			if err != nil {
				return err
			}
			if current == *expected {
				return restoreOverReplacement(candidate, target, *expected)
			}
		}
		return fmt.Errorf("%s is there already: nothing restored (use --to PATH)", target)
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	err := install(source)
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".jackstay-restore-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	candidate := filepath.Join(staging, "item")
	if err := copyTree(source, candidate); err != nil {
		return err
	}
	if err := sameTree(source, candidate); err != nil {
		return err
	}
	return install(candidate)
}

// InstallDiscardReplacement installs a privately staged worktree file only where
// discard expects absence or an exact existing replacement.
func InstallDiscardReplacement(source, target string, expected Replaced) error {
	if expected.Kind == "none" {
		return renameAbsent(source, target)
	}
	return restoreOverReplacement(source, target, expected)
}

// restoreOverReplacement checks the displaced file after exchange. The trash copy
// stays recoverable throughout, including if another editor changes the destination.
func restoreOverReplacement(source, target string, expected Replaced) error {
	staging, err := os.MkdirTemp(filepath.Dir(target), ".jackstay-restore-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()
	staged := filepath.Join(staging, "item")
	if err := copyTree(source, staged); err != nil {
		return err
	}
	if err := sameTree(source, staged); err != nil {
		return err
	}
	if err := exchangeRestore(staged, target); err != nil {
		return fmt.Errorf("cannot safely replace %s (use --to PATH): %w", target, err)
	}
	displaced, inspectErr := Describe(staged)
	if inspectErr == nil && displaced == expected {
		return nil
	}
	if err := exchangeRestore(staged, target); err != nil {
		cleanup = false
		return fmt.Errorf("the destination changed and could not be put back: %w; displaced contents kept at %s", err, staged)
	}
	if err := sameTree(source, staged); err != nil {
		cleanup = false
		return fmt.Errorf("the destination changed again during restore; the other version is kept at %s", staged)
	}
	return fmt.Errorf("%s changed during restore; its current contents and the trash entry were kept", target)
}

// Prune removes the entries older than the days each records (the `trash.keep_days` of the project it
// came from), never a held one; an entry written before entries recorded their days reads them from
// the project it came from, or the default when that is gone. It returns how many went.
func Prune(folder string, now time.Time) int {
	removed := 0
	for _, entry := range Entries(folder) {
		if !entry.expired(now) {
			continue
		}
		place := filepath.Join(folder, entry.ID)
		locked, err := lockEntry(place)
		if err != nil {
			continue
		}
		current, err := readEntry(place)
		if err == nil && current.expired(now) && os.RemoveAll(place) == nil {
			removed++
		}
		_ = locked.Close()
	}
	return removed
}

func (entry Entry) expired(now time.Time) bool {
	moved, err := time.Parse(time.RFC3339, entry.MovedAt)
	if err != nil || entry.Held || !now.After(moved) {
		return false
	}
	var days int
	if entry.KeepDays != nil {
		days = *entry.KeepDays
	} else {
		days = KeepDays(filepath.Dir(entry.Path))
	}
	const day = 24 * time.Hour
	elapsed := now.Sub(moved)
	wholeDays := int(elapsed / day)
	return wholeDays > days || wholeDays == days && elapsed%day > 0
}
