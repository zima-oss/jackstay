package background

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/telemetry/mask"
)

// An Entry is `<jackstay home>/.all/procs/<id>.json`: one group a `jst bg` supervises
// (docs/design/implementation.md section 5.1). Starts are whole seconds; a value written as null is a nil
// pointer here.
type Entry struct {
	ID              string   `json:"id"`
	Name            *string  `json:"name"`
	Supervisor      int      `json:"supervisor"`
	SupervisorStart *int64   `json:"supervisor_start"`
	Group           int      `json:"group"`
	Started         float64  `json:"started"`
	LeaderStart     *int64   `json:"leader_start"`
	Deadline        *float64 `json:"deadline"`
	Keep            bool     `json:"keep"`
	Session         *string  `json:"session"`
	Watched         *Watched `json:"watched"`
	Cwd             string   `json:"cwd"`
	// Repository is the Git repository the command was started in (gitcli.RepositoryID), "" outside
	// one or from a jst before 0.6.0: what `jst status` attributes the job by, whatever happens to
	// its folder since.
	Repository string   `json:"repository,omitempty"`
	Command    []string `json:"command"`
	Status     string   `json:"status"`
	Held       []string `json:"held,omitempty"`

	commandMasked bool
}

// savedEntry carries the storage marker without adding it to the procs JSON output.
type savedEntry struct {
	Entry
	CommandMasked bool `json:"command_masked,omitempty"`
}

// Watched is the process whose end stops the group: the session's own (pid and start).
type Watched struct {
	PID   int    `json:"pid"`
	Start *int64 `json:"start"`
	Name  string `json:"name"`
}

// nameOf is the entry's name, "" without one.
func (entry Entry) nameOf() string {
	if entry.Name == nil {
		return ""
	}
	return *entry.Name
}

// sessionOf is the entry's session, "" when it is not known.
func (entry Entry) sessionOf() string {
	if entry.Session == nil {
		return ""
	}
	return *entry.Session
}

// identifiers are the ids `jst bg` gives: `<epoch s>-<pid>`. An entry whose id is not one, or not
// its file's name, is not read: the id names the files jackstay writes and removes.
var identifiers = regexp.MustCompile(`^[0-9]+-[0-9]+$`)

// store is the folder of the entries, the stop requests and the log.
type store struct {
	folder string
}

func openStore() (store, error) {
	folder, err := homedir.SharedPath("procs")
	if err != nil {
		return store{}, err
	}
	return store{folder: folder}, nil
}

func (place store) entryPath(id string) string { return filepath.Join(place.folder, id+".json") }

func (place store) stopPath(id string) string { return filepath.Join(place.folder, id+".stop") }

// write puts the entry in place whole (a temporary file in the same folder, synced, renamed);
// false when it cannot be written.
func (place store) write(entry Entry) bool {
	data, err := marshal(savedEntry{Entry: entry, CommandMasked: entry.commandMasked})
	if err != nil {
		return false
	}
	if err := durable.MakeFolder(place.folder, 0o700); err != nil {
		return false
	}
	return durable.WriteAtomic(place.entryPath(entry.ID), data, 0o600, 0) == nil
}

// remove removes the entry's file, and its stop request with it.
func (place store) remove(id string) {
	_ = os.Remove(place.entryPath(id))
	_ = os.Remove(place.stopPath(id))
}

// removeStop takes a stop request back.
func (place store) removeStop(id string) error {
	return os.Remove(place.stopPath(id))
}

// listed says whether the entry's file is still there.
func (place store) listed(id string) bool {
	_, err := os.Stat(place.entryPath(id))
	return !errors.Is(err, fs.ErrNotExist)
}

// stopAsked says whether `jst procs stop` has asked the supervisor to stop its group.
func (place store) stopAsked(id string) bool {
	_, err := os.Stat(place.stopPath(id))
	return err == nil
}

// askStop writes the request a living supervisor answers.
func (place store) askStop(id string) error {
	return os.WriteFile(place.stopPath(id), nil, 0o600)
}

// read loads one entry written whole by write.
func (place store) read(id string) (Entry, bool) {
	data, err := os.ReadFile(place.entryPath(id))
	if err != nil {
		return Entry{}, false
	}
	var saved savedEntry
	if json.Unmarshal(data, &saved) != nil || saved.ID != id || saved.Group <= 1 {
		return Entry{}, false
	}
	if !saved.CommandMasked {
		saved.Command = mask.Argv(saved.Command, mask.Masked)
	}
	saved.commandMasked = true
	return saved.Entry, true
}

// entries are the readable entries, by file name.
func (place store) entries() []Entry {
	names, err := os.ReadDir(place.folder)
	if err != nil {
		return nil
	}
	var found []Entry
	for _, each := range names {
		stem, isEntry := strings.CutSuffix(each.Name(), ".json")
		if !isEntry || !identifiers.MatchString(stem) {
			continue
		}
		if entry, ok := place.read(stem); ok {
			found = append(found, entry)
		}
	}
	sort.Slice(found, func(left, right int) bool { return found[left].ID < found[right].ID })
	return found
}

// pruneNotes removes the session notes of processes the table no longer shows as noted (another
// process holds the id, or none does), on every listing.
func (place store) pruneNotes(table map[int]procs.Process) {
	if table == nil {
		return
	}
	folder := filepath.Join(place.folder, "sessions")
	names, err := os.ReadDir(folder)
	if err != nil {
		return
	}
	for _, each := range names {
		found := notes.FindStringSubmatch(each.Name())
		if found == nil {
			continue
		}
		pid, err := strconv.Atoi(found[1])
		if err != nil {
			continue
		}
		row, present := table[pid]
		if !present || strconv.FormatInt(row.Start, 10) != found[2] {
			_ = os.Remove(filepath.Join(folder, each.Name()))
		}
	}
}

var notes = regexp.MustCompile(`^([0-9]+)-([0-9]+)$`)

// marshal is JSON without HTML escapes.
func marshal(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// marshalIndented is marshal with two spaces of indent (`--json`).
func marshalIndented(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}
