package background

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
)

// Job is one `jst bg` group as another command may read it: no command line, no session.
type Job struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	State string `json:"state"`
	Cwd   string `json:"cwd"`
	// Repository is the repository it was started in; "" for a job started by a jst before 0.6.0.
	Repository string  `json:"repository,omitempty"`
	Started    float64 `json:"started"`
}

// Jobs are the background groups of this machine with their states, read without changing
// anything: unlike `jst procs`, it neither removes gone entries nor enforces a dead supervisor's
// deadline. Without an entries folder there are none; an entry removed while it reads is left
// out. A folder or an entry that cannot be read is an error, so a reader never takes an unreadable
// store for an empty one.
func Jobs() ([]Job, error) {
	place, err := openStore()
	if err != nil {
		return nil, err
	}
	return jobsOf(realMachine(), place)
}

func jobsOf(host machine, place store) ([]Job, error) {
	names, err := os.ReadDir(place.folder)
	if errors.Is(err, fs.ErrNotExist) {
		return []Job{}, nil
	}
	if err != nil {
		return nil, err
	}
	look := &looker{host: host, place: place}
	table := host.table()
	found := []Job{}
	for _, each := range names {
		stem, isEntry := strings.CutSuffix(each.Name(), ".json")
		if !isEntry || !identifiers.MatchString(stem) {
			continue
		}
		data, err := os.ReadFile(place.entryPath(stem))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var saved savedEntry
		if json.Unmarshal(data, &saved) != nil || saved.ID != stem || saved.Group <= 1 {
			return nil, fmt.Errorf("background job %s: unreadable entry", stem)
		}
		entry := saved.Entry
		found = append(found, Job{ID: entry.ID, Name: entry.nameOf(), State: look.state(table, entry),
			Cwd: entry.Cwd, Repository: entry.Repository, Started: entry.Started})
	}
	sort.Slice(found, func(left, right int) bool { return found[left].ID < found[right].ID })
	return found, nil
}
