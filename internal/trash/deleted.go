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
	"sort"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/telemetry"
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
}

// Keeps says whether the project around a folder keeps what jst trash and jst discard delete
// (`trash.keep` "on", experimental); by default, and when jackstay.json cannot be used, they do not.
func Keeps(folder string) bool {
	_, loaded, err := config.Load(folder, []string{"trash"})
	return err == nil && loaded.Section("trash")["keep"] == "on"
}

// Session is the Claude Code session a command runs in, for its records.
func Session() string {
	if session := os.Getenv(telemetry.ClaudeSessionVariable); session != "" {
		return session
	}
	return os.Getenv("CLAUDE_SESSION_ID")
}

// Delete deletes an item at its path for good (`trash.keep` off). Its deletion is logged and synced
// first, so no deletion goes unlogged. Written limits: a removal that then fails, or a command
// stopped part way, leaves a line for an item still there, in whole or in part, and the command says
// so; what is written into the item after it was measured is deleted with it, unmeasured.
func Delete(folder, given, project, session, command string, now time.Time) (Deletion, error) {
	source, err := Physical(given)
	if err != nil {
		return Deletion{}, fmt.Errorf("%s: %w", given, err)
	}
	if err := holdsHome(folder, given, source); err != nil {
		return Deletion{}, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return Deletion{}, fmt.Errorf("%s: %w", given, err)
	}
	size, digest, err := digestTree(source)
	if err != nil {
		return Deletion{}, fmt.Errorf("%s cannot be read, so nothing was deleted: %w", given, err)
	}
	deletion := Deletion{DeletedAt: now.Format(time.RFC3339), Path: source, Kind: kindOf(info), Size: size, SHA256: digest,
		Project: project, Session: session, Command: command}
	if err := appendLog(folder, deletion); err != nil {
		return Deletion{}, fmt.Errorf("the deletion of %s cannot be logged, so nothing was deleted: %w", given, err)
	}
	if err := os.RemoveAll(source); err != nil {
		return deletion, fmt.Errorf("logged as deleted, but %s could not be removed in full: %w", given, err)
	}
	return deletion, nil
}

// Log logs a deletion another command makes (jst discard), before it makes it.
func Log(folder string, deletion Deletion) error {
	return appendLog(folder, deletion)
}

// Digest is the size and digest of bytes, as a deletion records them.
func Digest(data []byte) (int64, string) {
	sum := sha256.Sum256(data)
	return int64(len(data)), hex.EncodeToString(sum[:])
}

func kindOf(info fs.FileInfo) string {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "link"
	case info.IsDir():
		return "folder"
	}
	return "file"
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
	if !info.Mode().IsRegular() {
		return 0, "", nil // a pipe, socket or device: never read, it could block
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
	if err := durable.MakeFolder(folder, 0o700); err != nil {
		return err
	}
	// One writer at a time, machine-wide: the size check, a rollover and the append are one step.
	unlock, err := lockLog(folder, unix.LOCK_EX)
	if err != nil {
		return err
	}
	defer unlock()
	place := filepath.Join(folder, logName)
	if info, err := os.Stat(place); err == nil && info.Size()+int64(len(line)) >= logLimit {
		if err := os.Rename(place, filepath.Join(folder, oldLogName)); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(place, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600) // #nosec G304 -- jackstay's own log
	if err != nil {
		return err
	}
	// A line a stopped writer left without its end is closed first, so this one reads on its own.
	if info, err := file.Stat(); err == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := file.ReadAt(last, info.Size()-1); err == nil && last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return durable.SyncFolder(folder) // the log's name, after a creation or a rollover, survives a crash
}

// lockLog takes the deletion log's lock (shared to read, exclusive to write) and gives its release.
func lockLog(folder string, how int) (func(), error) {
	lock, err := os.OpenFile(filepath.Join(folder, "deleted.lock"), os.O_WRONLY|os.O_CREATE, 0o600) // #nosec G304 -- jackstay's own lock
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), how); err != nil { // #nosec G115 -- a file descriptor
		_ = lock.Close()
		return nil, err
	}
	return func() { _ = lock.Close() }, nil
}

// Deletions are the logged deletions, newest first.
func Deletions(folder string) []Deletion {
	found := readLog(folder)
	sort.SliceStable(found, func(first, second int) bool { return found[first].DeletedAt > found[second].DeletedAt })
	return found
}

// readLog is every line of both logs, oldest first; a line cut short by a crash is passed over.
func readLog(folder string) []Deletion {
	var found []Deletion
	// Both generations under one shared lock: no rollover between the two reads.
	if unlock, err := lockLog(folder, unix.LOCK_SH); err == nil {
		defer unlock()
	}
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
				continue
			}
			found = append(found, deletion)
		}
		_ = file.Close()
	}
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
