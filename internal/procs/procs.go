// Package procs keeps the note of which Claude Code session a process serves (docs/design/implementation.md
// section 5.1): the guard hook, which runs before every Bash call, notes for its nearest ancestor that
// is not a shell which session that process serves; `jst bg` walks up its own ancestors to the
// nearest noted one. And the log of background calls not started through `jst bg`.
package procs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"go.opentelemetry.io/otel/attribute"

	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/telemetry"
)

// A Process is one row of the process table. A process is known by its id and its start: an id
// given again to a later process has another start.
type Process struct {
	PID    int
	Parent int
	// Group is its process group.
	Group int
	// UID is the account it runs as (its effective user id).
	UID int
	// Start is its start in whole seconds, as the holder notes and procs entries write it.
	Start int64
	// StartMicros is its start in microseconds: since the epoch on macOS; on Linux, since boot
	// plus a base fixed for the boot (table_linux.go), so the clock being set never changes it.
	// It tells two processes of one id apart, and is compared, never read as a time of day.
	StartMicros int64
	Name        string
	// Zombie says it has ended and waits for its parent to reap it: it no longer runs.
	Zombie bool
	// Session is its session's id, 0 when the kernel would not say.
	Session int
}

// A TableError says the process table could not be read.
type TableError struct {
	Err error
}

func (problem *TableError) Error() string {
	return "the process table cannot be read: " + problem.Err.Error()
}

func (problem *TableError) Unwrap() error { return problem.Err }

// notASession names the processes between a Bash call and its session's own process.
var notASession = regexp.MustCompile(`^(?:-?(?:sh|bash|zsh|dash|ksh|fish|tcsh|csh)|env|timeout|nice|nohup|caffeinate|[Pp]ython[0-9.]*|jst|jackstay|jst-guard)$`)

// IsSessionProcess says whether a process may be a session's own (not a shell, a wrapper or jackstay).
func IsSessionProcess(name string) bool {
	return !notASession.MatchString(filepath.Base(name))
}

// An Ancestor is a process above another: its id, start and name.
type Ancestor struct {
	PID   int
	Start int64
	Name  string
}

// Ancestors are the ancestors of pid, nearest first.
func Ancestors(table map[int]Process, pid int) []Ancestor {
	var found []Ancestor
	seen := map[int]bool{}
	current := table[pid].Parent
	for current > 1 && !seen[current] {
		row, ok := table[current]
		if !ok {
			break
		}
		seen[current] = true
		found = append(found, Ancestor{PID: current, Start: row.Start, Name: row.Name})
		current = row.Parent
	}
	return found
}

// NotePath is where the note of a process is kept.
func NotePath(pid int, start int64) (string, error) {
	return homedir.SharedPath("procs", "sessions", fmt.Sprintf("%d-%d", pid, start))
}

type note struct {
	Session string `json:"session"`
	PID     int    `json:"pid"`
	Start   int64  `json:"start"`
	Name    string `json:"name"`
}

// NoteSession notes which session the nearest ancestor that is not a shell serves. It returns the
// note's path, or "" when there is none to write; it never fails the hook.
func NoteSession(session string, table map[int]Process) string {
	if session == "" || table == nil {
		return ""
	}
	for _, above := range Ancestors(table, os.Getpid()) {
		if !IsSessionProcess(above.Name) {
			continue
		}
		place, err := NotePath(above.PID, above.Start)
		if err != nil {
			return ""
		}
		text, err := json.Marshal(note{Session: session, PID: above.PID, Start: above.Start, Name: filepath.Base(above.Name)})
		if err != nil {
			return ""
		}
		if old, err := os.ReadFile(place); err == nil && string(old) == string(text) {
			return place
		}
		if err := os.MkdirAll(filepath.Dir(place), 0o700); err != nil {
			return ""
		}
		partial := fmt.Sprintf("%s.%d.partial", place, os.Getpid())
		if err := os.WriteFile(partial, text, 0o600); err != nil {
			return ""
		}
		if err := os.Rename(partial, place); err != nil {
			_ = os.Remove(partial)
			return ""
		}
		return place
	}
	return ""
}

// backgroundEvents are the events of a `jst bg` group, recorded as span events of the command that
// sees them (docs/design/observability.md, section 2): the journal is their only record.
var backgroundEvents = map[string]string{"start": telemetry.EventBgStarted, "end": telemetry.EventBgEnded,
	"stop": telemetry.EventBgStopped, "held": telemetry.EventBgHeld, "cleaned": telemetry.EventBgCleaned}

// Note records a `jst bg` group's event on the running command's span.
func Note(event string, fields map[string]any) {
	name, ok := backgroundEvents[event]
	if !ok {
		return
	}
	var attributes []attribute.KeyValue
	if id, ok := fields["id"].(string); ok {
		attributes = append(attributes, attribute.String(telemetry.AttrBgID, id))
	}
	if label, ok := fields["name"].(string); ok && label != "" {
		attributes = append(attributes, attribute.String(telemetry.AttrBgName, label))
	}
	switch deadline := fields["deadline"].(type) {
	case *float64:
		if deadline != nil {
			attributes = append(attributes, attribute.String(telemetry.AttrBgDeadline, fmt.Sprint(*deadline)))
		}
	case float64, int, int64:
		attributes = append(attributes, attribute.String(telemetry.AttrBgDeadline, fmt.Sprint(deadline)))
	}
	if status, ok := fields["status"].(int); ok {
		attributes = append(attributes, attribute.Int(telemetry.AttrExitCode, status))
	}
	if group, ok := fields["group"].(int); ok && group > 0 {
		attributes = append(attributes, attribute.Int(telemetry.AttrBgGroup, group))
	}
	// The files that hold a group (held), and whether a stop looked at them and saw the group gone:
	// what tells an orphan's confirmed stop from an attempt.
	if files, ok := fields["files"].([]string); ok && len(files) > 0 {
		attributes = append(attributes, attribute.StringSlice(telemetry.AttrBgFiles, files))
	}
	if gone, ok := fields["gone"].(bool); ok {
		attributes = append(attributes, attribute.Bool(telemetry.AttrBgGone, gone))
	}
	if looked, ok := fields["looked"].(bool); ok {
		attributes = append(attributes, attribute.Bool(telemetry.AttrBgLooked, looked))
	}
	if reason, ok := fields["reason"].(string); ok && reason != "" {
		attributes = append(attributes, attribute.String(telemetry.AttrReason, reason))
	}
	telemetry.Note(name, attributes...)
}

// Own is the session a process runs under, as its ancestors' notes say: the nearest ancestor with
// a note names it; without one, Session is "" and the process is the nearest that may be a
// session's own. Found is false when the table cannot be read or no ancestor may be a session's.
type Own struct {
	Session string
	PID     int
	Start   int64
	Name    string
	Found   bool
}

// OwnSession is the session process pid runs under (table nil: none can be told).
func OwnSession(table map[int]Process, pid int) Own {
	if table == nil {
		return Own{}
	}
	line := Ancestors(table, pid)
	for _, above := range line {
		place, err := NotePath(above.PID, above.Start)
		if err != nil {
			return Own{}
		}
		data, err := os.ReadFile(place) // #nosec G304 -- jackstay's own note under its home
		if err != nil {
			continue
		}
		var written note
		if json.Unmarshal(data, &written) != nil || written.Session == "" {
			continue
		}
		return Own{Session: written.Session, PID: above.PID, Start: above.Start, Name: filepath.Base(above.Name), Found: true}
	}
	for _, above := range line {
		if IsSessionProcess(above.Name) {
			return Own{PID: above.PID, Start: above.Start, Name: filepath.Base(above.Name), Found: true}
		}
	}
	return Own{}
}
