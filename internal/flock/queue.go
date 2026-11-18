package flock

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// ticketName is a place in line: twenty digits of the clock's nanoseconds when it was made, and
// the waiting run's process id.
var ticketName = regexp.MustCompile(`^\d{20}-\d+$`)

// makingPrefix begins the name a ticket is made under, which nobody reads as a ticket.
const makingPrefix = "making-"

// MakingStale is the age after which a `making-…` file nobody holds is a dead run's.
const MakingStale = 60 * time.Second

// AheadBound is how far ahead of the clock a ticket may be numbered and still be a place in line:
// one further ahead was made while the clock was far wrong (or put there by hand).
const AheadBound = 24 * time.Hour

// QueueFolder is the folder of the lock's line.
func QueueFolder(lock string) string { return lock + ".queue" }

func numberOf(name string) int64 {
	digits, _, _ := strings.Cut(name, "-")
	number, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		// Twenty digits beyond int64: as far ahead as a number can be.
		return 1<<63 - 1
	}
	return number
}

// placeAfter is the number a new ticket takes: the clock's nanoseconds, and never one that sorts
// before a ticket already in line, unless that ticket is numbered beyond AheadBound (nobody's place).
func placeAfter(folder string, now time.Time) int64 {
	nowNanos := now.UnixNano()
	latest := int64(0)
	names, _ := os.ReadDir(folder)
	for _, entry := range names {
		name := entry.Name()
		if !ticketName.MatchString(name) {
			continue
		}
		if number := numberOf(name); number <= nowNanos+int64(AheadBound) {
			latest = max(latest, number)
		}
	}
	return max(nowNanos, latest+1)
}

// heldByNobody says whether the file can be locked now (only for the asking): no living run holds it.
func heldByNobody(path string) bool {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil // #nosec G115 -- a descriptor number
}

// sweep removes what dead runs left that no run in line would remove: a `making-…` file older
// than MakingStale that nobody holds, and a ticket numbered beyond AheadBound that nobody holds.
func sweep(folder string, now time.Time) {
	names, err := os.ReadDir(folder)
	if err != nil {
		return
	}
	for _, entry := range names {
		name := entry.Name()
		path := filepath.Join(folder, name)
		stale := false
		switch {
		case strings.HasPrefix(name, makingPrefix):
			info, err := os.Stat(path)
			stale = err == nil && now.Sub(info.ModTime()) > MakingStale
		case ticketName.MatchString(name):
			stale = numberOf(name) > now.UnixNano()+int64(AheadBound)
		}
		if stale && heldByNobody(path) {
			_ = os.Remove(path)
		}
	}
}

// ticket is a waiting run's place in line: a file in the queue's folder, locked by its run for as
// long as it waits. The lock goes to the earliest ticket whose run still lives; a ticket nobody
// holds is a dead run's, removed by whoever sees it.
type ticket struct {
	folder string
	name   string
	file   *os.File
	pid    int
	clock  Clock
}

// newTicket makes a ticket: under a `making-` name, locked, and only then renamed into its place
// after every ticket in line, so a ticket seen in line is always held. words are written into it
// for `status`.
func newTicket(folder, words string, pid int, clock Clock) (*ticket, error) {
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return nil, err
	}
	making := filepath.Join(folder, fmt.Sprintf("%s%d-%d", makingPrefix, pid, clock.Now().UnixNano()))
	file, err := os.OpenFile(making, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil { // #nosec G115 -- a descriptor number
		_ = file.Close()
		_ = os.Remove(making)
		return nil, err
	}
	place := &ticket{folder: folder, file: file, pid: pid, clock: clock}
	for {
		// The name holds this process's id, so no other run can take it: the look before the
		// rename only keeps the rename from ever replacing a file.
		name := fmt.Sprintf("%020d-%d", placeAfter(folder, clock.Now()), pid)
		if _, err := os.Lstat(filepath.Join(folder, name)); err == nil {
			continue
		}
		if err := os.Rename(making, filepath.Join(folder, name)); err != nil {
			_ = file.Close()
			_ = os.Remove(making)
			return nil, err
		}
		place.name = name
		break
	}
	if words != "" {
		_, _ = file.WriteString(words + "\n")
	}
	sweep(folder, clock.Now())
	return place, nil
}

// renumber gives a ticket numbered beyond AheadBound a number of now, after every ticket in line:
// the file and its lock stay, only the name changes.
func (place *ticket) renumber() {
	for {
		name := fmt.Sprintf("%020d-%d", placeAfter(place.folder, place.clock.Now()), place.pid)
		if _, err := os.Lstat(filepath.Join(place.folder, name)); err == nil {
			continue
		}
		if os.Rename(filepath.Join(place.folder, place.name), filepath.Join(place.folder, name)) == nil {
			place.name = name
		}
		return
	}
}

// ahead is how many living runs came before this one; the dead met on the way are removed.
func (place *ticket) ahead() int {
	if numberOf(place.name) > place.clock.Now().UnixNano()+int64(AheadBound) {
		place.renumber()
	}
	entries, err := os.ReadDir(place.folder)
	if err != nil {
		return 0
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	living := 0
	for _, name := range names {
		if !ticketName.MatchString(name) {
			continue
		}
		if name >= place.name {
			break
		}
		if isLocked(filepath.Join(place.folder, name), true) {
			living++
		}
	}
	return living
}

// isLocked says whether a living run holds the file; with remove, a file nobody holds is removed
// while it is locked for the asking (so no living run's is).
func isLocked(path string, remove bool) bool {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) // #nosec G115 -- a descriptor number
	if errors.Is(err, unix.EWOULDBLOCK) {
		return true
	}
	if err == nil && remove {
		_ = os.Remove(path)
	}
	return false
}

// drop gives the place in line up.
func (place *ticket) drop() {
	if place.file == nil {
		return
	}
	_ = os.Remove(filepath.Join(place.folder, place.name))
	_ = place.file.Close()
	place.file = nil
}

// Waiting are the living runs in line for the lock, first to go first: each ticket's words; a line
// that cannot be read has none (WaitingAt says why).
func Waiting(lock string) []string {
	found, _ := WaitingAt(lock)
	return found
}

// WaitingAt are the living runs in line for the lock, first to go first: each ticket's words. No
// folder is nobody waiting; a folder or a ticket that cannot be read or locked is an error, never
// an empty line.
func WaitingAt(lock string) ([]string, error) {
	folder := QueueFolder(lock)
	entries, err := os.ReadDir(folder)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the line of %s cannot be read: %w", lock, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if ticketName.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	var found []string
	for _, name := range names {
		words, waiting, err := ticketWords(filepath.Join(folder, name), name)
		if err != nil {
			return nil, fmt.Errorf("the line of %s cannot be read: %w", lock, err)
		}
		if waiting {
			found = append(found, words)
		}
	}
	return found, nil
}

// ticketWords are a ticket's words when a living run holds it; a ticket gone meanwhile is nobody's.
func ticketWords(path, name string) (string, bool, error) {
	file, err := os.Open(path) // #nosec G304 -- a ticket in the lock's own folder
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer func() { _ = file.Close() }()
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) // #nosec G115 -- a descriptor number
	switch {
	case err == nil:
		return "", false, nil
	case !errors.Is(err, unix.EWOULDBLOCK):
		return "", false, fmt.Errorf("%s: %w", path, err)
	}
	buffer := make([]byte, 4096)
	count, _ := file.Read(buffer)
	words := strings.TrimSpace(strings.ToValidUTF8(string(buffer[:max(count, 0)]), "\ufffd"))
	if words == "" {
		_, pid, _ := strings.Cut(name, "-")
		words = fmt.Sprintf("a run that left no note (process %s)", pid)
	}
	return words, true, nil
}
