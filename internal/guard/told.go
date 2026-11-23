package guard

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/zima-oss/jackstay/internal/homedir"
)

// configUnusable is the hook's short line for a jackstay.json it cannot use; the reason itself is
// `jst check`'s (and the span's), so a session is not handed the same long line on every call.
const configUnusable = "jackstay.json cannot be used, so only the built-in rules apply; `jst check` says why " +
	"(said once in a session, and again when the file changes)"

// toldKeep is how long a session's mark is kept: longer than a working session.
const toldKeep = 7 * 24 * time.Hour

// configNews says whether the session is still to be told that the project's jackstay.json cannot
// be used: once for each state of the file (its reason, size and time) in a session. It takes a mark
// `<jackstay home>/<project>/guard-told/<digest>` that only one call can create, so two calls at once
// tell once. A call without a session, or a mark that cannot be made, is told: saying it twice is
// better than not at all.
func configNews(session string, found project) bool {
	if session == "" {
		return true
	}
	state := session + "\x00" + found.problem + "\x00" + found.place
	if info, err := os.Stat(found.place); err == nil {
		state += "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
	}
	sum := sha256.Sum256([]byte(state))
	folder, err := homedir.ProjectPath(found.name, "guard-told")
	if err != nil {
		return true
	}
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return true
	}
	mark, err := os.OpenFile(filepath.Join(folder, hex.EncodeToString(sum[:16])), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- jackstay's own mark
	if errors.Is(err, fs.ErrExist) {
		return false
	}
	if err == nil {
		_ = mark.Close()
	}
	return true
}

// pruneTold removes marks older than toldKeep, so the folder holds about a week of sessions.
func pruneTold(projectName string) {
	folder, err := homedir.ProjectPath(projectName, "guard-told")
	if err != nil {
		return
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
