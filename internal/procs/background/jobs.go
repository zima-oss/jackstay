package background

// Job is one `jst bg` group as another command may read it: no command line, no session.
type Job struct {
	ID      string  `json:"id"`
	Name    string  `json:"name,omitempty"`
	State   string  `json:"state"`
	Cwd     string  `json:"cwd"`
	Started float64 `json:"started"`
}

// Jobs are the background groups of this machine with their states, read without changing
// anything: unlike `jst procs`, it neither removes gone entries nor enforces a dead supervisor's
// deadline. Without an entries folder there are none.
func Jobs() ([]Job, error) {
	place, err := openStore()
	if err != nil {
		return nil, err
	}
	return jobsOf(realMachine(), place), nil
}

func jobsOf(host machine, place store) []Job {
	look := &looker{host: host, place: place}
	table := host.table()
	found := []Job{}
	for _, entry := range place.entries() {
		found = append(found, Job{ID: entry.ID, Name: entry.nameOf(), State: look.state(table, entry),
			Cwd: entry.Cwd, Started: entry.Started})
	}
	return found
}
