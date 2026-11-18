// Package homedir is jackstay's user-level folder and its logs (W-008):
// `~/.jackstay` found from the account (never from HOME), or JACKSTAY_HOME; what every project shares under
// `.all`; what is one project's under its marker name; one JSON line appended in one write.
package homedir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// Shared is the folder every project shares; no project can be called this (MarkerName writes a
// leading dot as %2E).
const Shared = ".all"

// AccountHome is the account's home from the password database, as pwd.getpwuid(os.getuid()).
func AccountHome() string {
	if current, err := user.Current(); err == nil && current.HomeDir != "" {
		return current.HomeDir
	}
	return os.Getenv("HOME")
}

// Home is `~/.jackstay`, or JACKSTAY_HOME (which must be absolute).
func Home() (string, error) {
	if chosen := os.Getenv("JACKSTAY_HOME"); chosen != "" {
		if !filepath.IsAbs(chosen) {
			return "", fmt.Errorf("JACKSTAY_HOME must be an absolute path, so every checkout names the same folder: %s", chosen)
		}
		return chosen, nil
	}
	return filepath.Join(AccountHome(), ".jackstay"), nil
}

// SharedPath is a path under `<jackstay home>/.all`.
func SharedPath(parts ...string) (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home, Shared}, parts...)...), nil
}

// ProjectPath is a path under `<jackstay home>/<marker name of project>`.
func ProjectPath(project string, parts ...string) (string, error) {
	home, err := Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home, MarkerName(project)}, parts...)...), nil
}

// Expand is `~/x` from the account's home, not HOME's.
func Expand(path string) string {
	if path == "~" {
		return AccountHome()
	}
	if strings.HasPrefix(path, "~/") {
		return pyJoin(AccountHome(), path[2:])
	}
	return path
}

// pyJoin is os.path.join of two parts: an absolute second part replaces the first.
func pyJoin(first, second string) string {
	if strings.HasPrefix(second, "/") {
		return second
	}
	if first == "" || strings.HasSuffix(first, "/") {
		return first + second
	}
	return first + "/" + second
}

// MarkerName is the file name a project is kept under: [a-z0-9_-] and a dot not first stay,
// a capital letter is `^` and the letter, every other character is %XX of its UTF-8 bytes.
func MarkerName(project string) string {
	if project == "" {
		return "unknown"
	}
	var builder strings.Builder
	for index, char := range []rune(project) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '_', char == '-', char == '.' && index > 0:
			builder.WriteRune(char)
		case char >= 'A' && char <= 'Z':
			builder.WriteByte('^')
			builder.WriteRune(char + ('a' - 'A'))
		default:
			for _, octet := range []byte(string(char)) {
				fmt.Fprintf(&builder, "%%%02X", octet)
			}
		}
	}
	return builder.String()
}

// AppendLine adds one JSON object as a line of a log in a single write, so lines of several
// processes never mix (the fields are the log's schema; their order and spacing are not). False
// when it could not be written; a log is never worth failing a tool for.
func AppendLine(path string, record any) bool {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return false
	}
	data := buffer.Bytes()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	written, err := file.Write(data)
	if err == nil && written == len(data) {
		return true
	}
	if written > 0 && written < len(data) {
		// A line cut short: the rest after it, else at least an end of line.
		rest := data[written:]
		for len(rest) > 0 {
			more, err := file.Write(rest)
			if err != nil || more <= 0 {
				break
			}
			rest = rest[more:]
		}
		if len(rest) > 0 {
			_, _ = file.Write([]byte("\n"))
		}
		return len(rest) == 0
	}
	return false
}

// NowText is the local time as logs write it (2026-11-16T14:00:00+0200).
func NowText() string { return time.Now().Format("2006-01-02T15:04:05-0700") }

// Epoch is the time in seconds since 1970.
func Epoch() float64 { return float64(time.Now().UnixNano()) / 1e9 }
