package receipts

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

// runLogReadLimit bounds what LatestRuns reads of the run log.
const runLogReadLimit = 32 << 20

// ErrNoRunLog is LatestRuns' answer when no run has been logged on this machine.
var ErrNoRunLog = errors.New("no run log")

// LatestRuns are the newest gate line and the newest stored pass of a checkout (project and
// worktree as the run log records them), each nil when there is none. Lines of an outer run are
// skipped. Unlike ReadRecords it reads strictly: a line that is not a JSON object, an unfinished
// last line, a file that is not regular or one larger than 32 MiB is an error, so a reader never
// reports a partial log as complete.
func LatestRuns(project, worktree string) (gate, pass Record, err error) {
	path, err := RunLogPath()
	if err != nil {
		return nil, nil, err
	}
	data, err := durable.ReadLimited(path, runLogReadLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrNoRunLog
	}
	if err != nil {
		return nil, nil, err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return nil, nil, errors.New("run log ends with an incomplete line")
	}
	var gateEpoch, passEpoch float64
	for index, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var record Record
		if err := strictjson.UnmarshalOpen(line, &record); err != nil || record == nil {
			return nil, nil, fmt.Errorf("run log line %d is invalid", index+1)
		}
		if record.Text("project") != project || record.Text("worktree") != worktree || record.Inside() {
			continue
		}
		epoch, _ := record.Number("ended_epoch")
		if epoch == 0 {
			epoch, _ = record.Number("epoch")
		}
		if record.Text("event") == EventGate && epoch >= gateEpoch {
			gate, gateEpoch = record, epoch
		}
		if record.Text("event") == EventRun && record.Text("outcome") == OutcomePassed && record["stored"] == true && epoch >= passEpoch {
			pass, passEpoch = record, epoch
		}
	}
	return gate, pass, nil
}
