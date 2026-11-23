package guard

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/homedir"
)

// configUnusable is the hook's short line for a jackstay.json it cannot use; the reason itself is
// `jst check`'s (and the span's), so a session is not handed the same long line on every call.
const configUnusable = "jackstay.json cannot be used, so only the built-in rules apply; `jst check` says why " +
	"(said once in a session, and again when the file changes)"

// toldKeep is how long a session's mark is kept: longer than a working session.
const toldKeep = 7 * 24 * time.Hour

// stampOf is the path, size and time of the jackstay.json around a folder, or "".
func stampOf(folder string) string {
	_, place := config.Find(folder)
	if place == "" {
		return ""
	}
	info, err := os.Stat(place)
	if err != nil {
		return place
	}
	return place + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
}

// configNews says whether the session is still to be told that the project's jackstay.json cannot
// be used: once for each state of the file (its reason, path, size and time) in a session. It gives
// the mark `<jackstay home>/<project>/guard-told/<digest>` to take once the answer is written, so a
// hook stopped before it answers tells again; two calls at once may both tell. A call without a
// session is told every time.
func configNews(session string, found project) (string, bool) {
	if session == "" {
		return "", true
	}
	sum := sha256.Sum256([]byte(session + "\x00" + found.problem + "\x00" + found.stamp))
	folder, err := homedir.ProjectPath(found.name, "guard-told")
	if err != nil {
		return "", true
	}
	mark := filepath.Join(folder, hex.EncodeToString(sum[:16]))
	if _, err := os.Lstat(mark); err == nil {
		return "", false
	}
	return mark, true
}

// markTold takes the mark of a session that was told, and removes marks older than toldKeep, so
// the folder holds about a week of sessions.
func markTold(mark string) {
	folder := filepath.Dir(mark)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return
	}
	if file, err := os.OpenFile(mark, os.O_WRONLY|os.O_CREATE, 0o600); err == nil { // #nosec G304 -- jackstay's own mark
		_ = file.Close()
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > toldKeep {
			_ = os.Remove(filepath.Join(folder, entry.Name()))
		}
	}
}
