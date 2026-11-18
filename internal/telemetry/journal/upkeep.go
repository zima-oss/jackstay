package journal

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Retention is a folder's `telemetry` settings (section 10).
type Retention struct {
	KeepDays          int
	CompressAfterDays int
}

// DefaultRetention is the retention of a folder whose project says nothing (W-056).
var DefaultRetention = Retention{KeepDays: 180, CompressAfterDays: 14}

// UpkeepBound is the most time one upkeep works; the rest waits for the next day.
const UpkeepBound = 2 * time.Second

const upkeepLock = ".upkeep.lock"

// Upkeep gzips the day files older than CompressAfterDays and deletes those older than KeepDays,
// under a lock tried once (held: another process is at it). now is the clock.
func Upkeep(folder string, retention Retention, now time.Time) {
	started := time.Now()
	lockFile, err := os.OpenFile(filepath.Join(folder, upkeepLock), os.O_WRONLY|os.O_CREATE, 0o600) // #nosec G304 -- jackstay's own journal
	if err != nil {
		return
	}
	defer func() { _ = lockFile.Close() }()
	if unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil { // #nosec G115 -- a descriptor
		return
	}
	defer func() { _ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN) }() // #nosec G115 -- a descriptor
	today := dayStart(now)
	for _, day := range Days(folder) {
		if time.Since(started) > UpkeepBound {
			return
		}
		age := int(today.Sub(day).Hours() / 24)
		plain := filepath.Join(folder, FileName(day.Format(time.DateOnly)))
		packed := plain + ".gz"
		switch {
		case age > retention.KeepDays:
			_ = os.Remove(plain)
			_ = os.Remove(packed)
			_ = os.Remove(packed + ".partial")
		case age > retention.CompressAfterDays:
			compress(plain, packed)
		}
	}
}

func dayStart(moment time.Time) time.Time {
	year, month, day := moment.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, moment.Location())
}

// Days lists the dates of a folder's day files, plain or compressed, oldest first.
func Days(folder string) []time.Time {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var days []time.Time
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".gz")
		if !strings.HasSuffix(name, Suffix) {
			continue
		}
		date := strings.TrimSuffix(name, Suffix)
		day, err := time.ParseInLocation(time.DateOnly, date, time.Local)
		if err != nil || seen[date] {
			continue
		}
		seen[date] = true
		days = append(days, day)
	}
	return days
}

// compress makes plain's .gz and removes plain only once the .gz decodes whole. A .gz left by a
// stopped upkeep is checked the same way; a .gz that does not decode is made again.
func compress(plain, packed string) {
	if _, err := os.Stat(plain); err != nil {
		return
	}
	if !decodesWhole(packed) {
		if err := writeGzip(plain, packed); err != nil {
			return
		}
		if !decodesWhole(packed) {
			return
		}
	}
	_ = os.Remove(plain)
}

func writeGzip(plain, packed string) (err error) {
	source, err := os.Open(plain) // #nosec G304 -- jackstay's own journal
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	partial := packed + ".partial"
	target, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- jackstay's own journal
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = target.Close()
			_ = os.Remove(partial)
		}
	}()
	writer := gzip.NewWriter(target)
	if _, err = io.Copy(writer, source); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	if err = target.Sync(); err != nil {
		return err
	}
	if err = target.Close(); err != nil {
		return err
	}
	return os.Rename(partial, packed)
}

func decodesWhole(packed string) bool {
	file, err := os.Open(packed) // #nosec G304 -- jackstay's own journal
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return false
	}
	// A day file is a few MB; the bound only stops a file that is not jackstay's from reading forever.
	_, err = io.CopyN(io.Discard, reader, 4<<30)
	return err == nil || errors.Is(err, io.EOF)
}
