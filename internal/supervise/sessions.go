package supervise

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/proctree"
)

// SessionsFileVariable names the file in which a run started under a group (`jst lock`,
// `jst test --`) records each session it starts (`jst test <scope>`), one `<session id> <its
// leader's start>` per line; the group's supervisor stops those sessions after any end, also when
// the run that wrote them was killed outright.
const SessionsFileVariable = "JACKSTAY_LOCK_SESSIONS_FILE"

// RunIDVariable and RunProjectVariable name the `jst test` run a command runs inside, handed down
// to a nested `jst test` (passed on unchanged).
const (
	RunIDVariable      = "JACKSTAY_TEST_RUN_ID"
	RunProjectVariable = "JACKSTAY_TEST_RUN_PROJECT"
)

// SessionGrace is what a recorded session is given between SIGTERM and SIGKILL.
const SessionGrace = 10 * time.Second

// RecordSession appends a session to the sessions file. start is the leader's start in
// microseconds.
func RecordSession(path string, session int, start int64) error {
	if path == "" || session <= 0 {
		return nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(file, "%d %d\n", session, start)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// A RecordedSession is one line of the sessions file.
type RecordedSession struct {
	Session int
	// Start is the leader's start in microseconds.
	Start int64
}

// ReadSessions are the well-formed lines of the sessions file; an unreadable file has none.
func ReadSessions(path string) []RecordedSession {
	data, err := os.ReadFile(path) // #nosec G304 -- the file the run's own environment names
	if err != nil {
		return nil
	}
	var found []RecordedSession
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		session, sessionErr := strconv.Atoi(fields[0])
		start, startErr := strconv.ParseInt(fields[1], 10, 64)
		if sessionErr != nil || startErr != nil || session <= 1 || start < 0 {
			continue
		}
		found = append(found, RecordedSession{Session: session, Start: start})
	}
	return found
}

// sameStart says whether a row is the process a recorded start names.
func sameStart(row procs.Process, start int64) bool {
	return row.StartMicros == start
}

// sessionTree is the tree of a recorded session as the table shows it: every running process of
// the session while it is still that run's (its leader is the one recorded, or gone and its id
// not yet given to another); nil when the session's id now belongs to another process.
func sessionTree(table map[int]procs.Process, recorded RecordedSession) *proctree.Tree {
	if leader, present := table[recorded.Session]; present && !sameStart(leader, recorded.Start) {
		return nil
	}
	tree := proctree.New()
	tree.FollowSession(recorded.Session)
	for pid, row := range table {
		if row.Session == recorded.Session && !row.Zombie {
			tree.Remember(pid, row.StartMicros)
		}
	}
	tree.Sample(table)
	return tree
}

// stopSessions stops what is left of every session the file names.
func stopSessions(path string, system proctree.System, say func(string, ...any)) ([]proctree.Survivor, error) {
	recorded := ReadSessions(path)
	if len(recorded) == 0 {
		return nil, nil
	}
	table, err := system.Read()
	if err != nil {
		return nil, err
	}
	var survivors []proctree.Survivor
	for _, each := range recorded {
		tree := sessionTree(table, each)
		if tree == nil {
			continue
		}
		living := tree.Living(table)
		if len(living) == 0 {
			continue
		}
		say("the run's session %d still has %s; stopping them.", each.Session, Names(living))
		result, err := system.Stop(tree, proctree.Targets{Groups: []int{each.Session}, Members: true}, SessionGrace)
		if err != nil {
			return survivors, err
		}
		survivors = append(survivors, result.Survivors...)
	}
	return survivors, nil
}
