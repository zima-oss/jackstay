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
	"(said again in 10 minutes, or when the file changes)"

// toldEvery is how long a session goes without the line after it was told; the hook cannot know
// that the line reached the session, so a told mark expires instead of lasting the session.
const toldEvery = 10 * time.Minute

// toldKeep is how long a mark is kept before any `jst guard` call removes it.
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

// configNews says whether the session is to be told that the project's jackstay.json cannot be
// used: unless it was told of this state of the file (its reason, path, size and time) in the last
// toldEvery. It gives the mark `<jackstay home>/<project>/guard-told/<digest>` to touch once the
// answer is written in full. Written limits: two calls at once may both tell, and an answer the
// session never received (the hook stopped after writing it) is told again toldEvery later. A
// call without a session, or a file that changed while it was read, is told every time.
func configNews(session string, found project, now time.Time) (string, bool) {
	if session == "" || found.stamp == "" {
		return "", true
	}
	sum := sha256.Sum256([]byte(session + "\x00" + found.problem + "\x00" + found.stamp))
	folder, err := homedir.ProjectPath(found.name, "guard-told")
	if err != nil {
		return "", true
	}
	mark := filepath.Join(folder, hex.EncodeToString(sum[:16]))
	if info, err := os.Lstat(mark); err == nil && now.Sub(info.ModTime()) < toldEvery {
		return "", false
	}
	return mark, true
}

// markTold touches the mark of a session just told, and removes marks older than toldKeep.
func markTold(mark string, now time.Time) {
	folder := filepath.Dir(mark)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(mark, os.O_WRONLY|os.O_CREATE, 0o600) // #nosec G304 -- jackstay's own mark
	if err != nil {
		return
	}
	_ = file.Close()
	_ = os.Chtimes(mark, now, now)
	entries, err := os.ReadDir(folder)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > toldKeep {
			_ = os.Remove(filepath.Join(folder, entry.Name()))
		}
	}
}
