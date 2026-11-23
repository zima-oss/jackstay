package trash

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
)

// The deletion log: one JSON line for each item deleted, in the trash folder. At logLimit bytes it
// is renamed to the previous log, which replaces the one before; `jst trash list` reads both.
const (
	logName     = "deleted.jsonl"
	oldLogName  = "deleted.1.jsonl"
	logLimit    = 1 << 20
	maxLogEntry = 64 << 10
)

// A Deletion is one item deleted for good (trash.keep off), as the log records it.
type Deletion struct {
	DeletedAt string `json:"deleted_at"`
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	// Size is the bytes of a file, of a link's target, or of every file in a folder.
	Size int64 `json:"size"`
	// SHA256 is of a file's bytes or a link's target; of a folder, of a listing of each entry's
	// relative path, kind, mode and digest.
	SHA256  string `json:"sha256"`
	Project string `json:"project"`
	Session string `json:"session"`
	Command string `json:"command"`
	// Entry is the trash entry it went through; while that entry is still there, it is kept.
	Entry string `json:"entry"`
}

// Keeps says whether the project around a folder keeps what jst trash and jst discard delete
// (`trash.keep` "on", experimental); by default, and when jackstay.json cannot be used, they do not.
func Keeps(folder string) bool {
	_, loaded, err := config.Load(folder, []string{"trash"})
	return err == nil && loaded.Section("trash")["keep"] == "on"
}

// Purge deletes a whole, released entry for good: the deletion is logged first, so a delete stopped
// part way leaves the entry kept and listed as such, never an unlogged deletion.
func Purge(folder, id, command string, now time.Time) (Deletion, error) {
	place := filepath.Join(folder, id)
	locked, err := lockEntry(place)
	if err != nil {
		return Deletion{}, err
	}
	defer func() { _ = locked.Close() }()
	entry, err := readEntry(place)
	if err != nil {
		return Deletion{}, err
	}
	if entry.Held {
		return Deletion{}, fmt.Errorf("entry %s is held (its move is not whole): kept", id)
	}
	item := filepath.Join(place, "item", filepath.Base(entry.Path))
	size, digest, err := digestTree(item)
	if err != nil {
		return Deletion{}, err
	}
	deletion := Deletion{DeletedAt: now.Format(time.RFC3339), Path: entry.Path, Kind: entry.Kind, Size: size,
		SHA256: digest, Project: entry.Project, Session: entry.Session, Command: command, Entry: id}
	if err := appendLog(folder, deletion); err != nil {
		return Deletion{}, fmt.Errorf("the deletion cannot be logged, so %s is kept: %w", id, err)
	}
	if err := os.RemoveAll(place); err != nil {
		return deletion, fmt.Errorf("logged as deleted, but entry %s could not be removed in full: %w", id, err)
	}
	return deletion, nil
}

// digestTree is an item's size and digest, read without following links.
func digestTree(root string) (int64, string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return 0, "", err
	}
	if !info.IsDir() {
		return digestOne(root, info)
	}
	listing := sha256.New()
	var total int64
	err = filepath.WalkDir(root, func(place string, _ fs.DirEntry, err error) error {
		if err != nil || place == root {
			return err
		}
		info, err := os.Lstat(place)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, place)
		kind, digest := "folder", ""
		if !info.IsDir() {
			var size int64
			if size, digest, err = digestOne(place, info); err != nil {
				return err
			}
			total += size
			kind = "file"
			if info.Mode()&os.ModeSymlink != 0 {
				kind = "link"
			}
		}
		_, err = fmt.Fprintf(listing, "%s\x00%s\x00%o\x00%s\n", relative, kind, info.Mode().Perm(), digest)
		return err
	})
	if err != nil {
		return 0, "", err
	}
	return total, hex.EncodeToString(listing.Sum(nil)), nil
}

func digestOne(place string, info fs.FileInfo) (int64, string, error) {
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(place)
		if err != nil {
			return 0, "", err
		}
		sum := sha256.Sum256([]byte(target))
		return int64(len(target)), hex.EncodeToString(sum[:]), nil
	}
	file, err := os.Open(place) // #nosec G304 -- inside the trash
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

// appendLog adds one line to the deletion log and syncs it; a log at its limit becomes the previous one.
func appendLog(folder string, deletion Deletion) error {
	line, err := json.Marshal(deletion)
	if err != nil {
		return err
	}
	place := filepath.Join(folder, logName)
	if info, err := os.Stat(place); err == nil && info.Size()+int64(len(line)) >= logLimit {
		if err := os.Rename(place, filepath.Join(folder, oldLogName)); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(place, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) // #nosec G304 -- jackstay's own log
	if err != nil {
		return err
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// Deletions are the logged deletions, newest first, without those whose entry is still kept (a
// delete stopped part way): those are listed as entries.
func Deletions(folder string) []Deletion {
	var found []Deletion
	for _, name := range []string{oldLogName, logName} {
		file, err := os.Open(filepath.Join(folder, name)) // #nosec G304 -- jackstay's own log
		if err != nil {
			continue
		}
		lines := bufio.NewScanner(file)
		lines.Buffer(make([]byte, 0, 4096), maxLogEntry)
		for lines.Scan() {
			var deletion Deletion
			if json.Unmarshal(lines.Bytes(), &deletion) != nil {
				continue // a line cut short by a crash: the others still read
			}
			if deletion.Entry != "" && !strings.Contains(deletion.Entry, "/") {
				if _, err := os.Lstat(filepath.Join(folder, deletion.Entry, "entry.json")); err == nil {
					continue
				}
			}
			found = append(found, deletion)
		}
		_ = file.Close()
	}
	sort.SliceStable(found, func(first, second int) bool { return found[first].DeletedAt > found[second].DeletedAt })
	return found
}

// Measure is a deletion's size and the start of its digest, as `jst trash` prints them.
func Measure(deletion Deletion) string {
	digest := deletion.SHA256
	if len(digest) > 12 {
		digest = digest[:12]
	}
	return fmt.Sprintf("%d bytes, sha256 %s", deletion.Size, digest)
}

// wasDeleted says whether the log records the deletion of entry id.
func wasDeleted(folder, id string) bool {
	return slices.ContainsFunc(Deletions(folder), func(deletion Deletion) bool { return deletion.Entry == id })
}
