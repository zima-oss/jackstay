package background

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/proctree"
	"github.com/zima-oss/jackstay/internal/supervise"
)

const procsUsage = "usage: jst procs [--all] [--json] | jst procs stop [--all] ID|NAME..."

// States an entry is shown in.
const (
	stateRunning           = "running"
	stateOrphaned          = "orphaned"
	stateGone              = "gone"
	stateUnknown           = "unknown"
	stateCleanupIncomplete = "cleanup-incomplete"
)

// orphanedDeadline is the reason a look gives when it enforces a dead supervisor's deadline.
const orphanedDeadline = "deadline (orphaned)"

// answerBound is how long `jst procs stop` waits for a living supervisor to stop its group and
// remove its entry: the grace, and room for the kill.
const answerBound = stopGrace + 10*time.Second

// answerPoll is the pause between two looks for the entry's removal.
const answerPoll = 50 * time.Millisecond

// shown is an entry as `jst procs` shows it: with its state.
type shown struct {
	Entry
	State string `json:"state"`
	// sandboxed says this command cannot signal the orphan's group (not shown in --json).
	sandboxed bool
}

// looker is one `jst procs` call.
type looker struct {
	host   machine
	place  store
	stdout io.Writer
	stderr io.Writer
}

func newLooker(stdout, stderr io.Writer) (*looker, error) {
	place, err := openStore()
	if err != nil {
		return nil, err
	}
	return &looker{host: realMachine(), place: place, stdout: stdout, stderr: stderr}, nil
}

func listCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	every, asJSON := false, false
	for _, word := range args {
		switch word {
		case "--all":
			every = true
		case "--json":
			asJSON = true
		default:
			fmt.Fprintln(stderr, procsUsage)
			return statusUsage
		}
	}
	look, err := newLooker(stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "jst procs: %v\n", err)
		return statusUsage
	}
	return look.list(every, asJSON)
}

// state is `running` or `held` (its supervisor lives), `orphaned` (the supervisor is gone and its
// group is not), `gone`, `unknown` or `cleanup-incomplete`.
func (look *looker) state(table map[int]procs.Process, entry Entry) string {
	if entry.Status == stateCleanupIncomplete {
		return stateCleanupIncomplete
	}
	if look.host.supervisorAlive(table, entry) {
		if entry.Status != "" {
			return entry.Status
		}
		return stateRunning
	}
	if table == nil {
		return stateUnknown
	}
	if look.host.groupAlive(table, entry.Group) {
		return stateOrphaned
	}
	return stateGone
}

// list shows the entries of this session (when it can be told) or every one, and on the way does
// what no supervisor is left to do: an orphan past its deadline is stopped (or held), a gone entry
// removed.
func (look *looker) list(every, asJSON bool) int {
	table := look.host.table()
	look.place.pruneNotes(table)
	mine := procs.OwnSession(table, look.host.pid).Session
	now := epoch(look.host.clock.Now())
	listed := []shown{}
	others := 0
	for _, entry := range look.place.entries() {
		state := look.state(table, entry)
		sandboxed := false
		if state == stateOrphaned && !entry.Keep && entry.Deadline != nil && *entry.Deadline < now && entry.LeaderStart != nil {
			if same, _ := sameProcess(table, entry.Group, *entry.LeaderStart); same {
				var stopped bool
				entry, stopped, sandboxed = look.enforceDeadline(table, entry)
				if stopped {
					continue
				}
			}
		}
		if state == stateGone {
			look.place.remove(entry.ID)
			note("cleaned", map[string]any{"id": entry.ID})
			continue
		}
		if !every && mine != "" && entry.sessionOf() != mine {
			others++
			continue
		}
		listed = append(listed, shown{Entry: entry, State: state, sandboxed: sandboxed})
	}
	if asJSON {
		data, err := marshalIndented(listed)
		if err != nil {
			fmt.Fprintf(look.stderr, "jst procs: %v\n", err)
			return statusUsage
		}
		fmt.Fprintln(look.stdout, string(data))
		return 0
	}
	look.writeList(listed, now, mine != "" && !every, others)
	return 0
}

// enforceDeadline does for an orphan past its deadline what its supervisor would have: a group
// writing a file is held (and asked again next time), any other stopped. stopped is true when it
// is gone and its entry removed; sandboxed when this command may not signal it.
func (look *looker) enforceDeadline(table map[int]procs.Process, entry Entry) (Entry, bool, bool) {
	if look.host.refusal(table, entry.Group) != 0 {
		return entry, false, true
	}
	// The look covers exactly what the stop signals: the group and its session's other members.
	files, looked := look.host.held(entry.Group, stopSet(table, entry.Group))
	if looked && len(files) > 0 {
		if !equalTexts(entry.Held, files) {
			entry.Status, entry.Held = "held", files
			look.place.write(entry)
			note("held", map[string]any{"id": entry.ID, "reason": orphanedDeadline, "files": files[:min(len(files), 20)]})
		}
		return entry, false, false
	}
	gone, err := look.host.stopGroup(table, entry.Group)
	if err != nil {
		fmt.Fprintf(look.stderr, "jst procs: %s: %v\n", entry.ID, err)
	}
	note("stop", map[string]any{"id": entry.ID, "reason": orphanedDeadline, "gone": gone, "looked": looked})
	if gone {
		look.place.remove(entry.ID)
	}
	return entry, gone, false
}

func (look *looker) writeList(listed []shown, now float64, thisSession bool, others int) {
	for _, entry := range listed {
		left := "kept"
		switch {
		case entry.Keep || entry.Deadline == nil:
		case *entry.Deadline < now:
			left = "overdue"
		default:
			left = "in " + age(*entry.Deadline-now)
		}
		name, session := entry.nameOf(), entry.sessionOf()
		if name == "" {
			name = "-"
		}
		if session == "" {
			session = "unknown"
		}
		fmt.Fprintf(look.stdout, "%s  %s  %s  age %s  deadline %s  session %s  %s  %s\n", entry.ID, name, entry.State,
			age(now-entry.Started), left, session, entry.Cwd, cutRunes(strings.Join(entry.Command, " "), 120))
		if entry.State == stateOrphaned {
			fmt.Fprintf(look.stdout, "    its supervisor is gone; `jst procs stop %s` stops group %d\n", entry.ID, entry.Group)
			if entry.sandboxed {
				fmt.Fprintf(look.stdout, "    this command cannot signal group %d: run outside the sandbox\n", entry.Group)
			}
		}
		if entry.State == stateCleanupIncomplete {
			fmt.Fprintln(look.stdout, "    its supervisor could not confirm that all processes it started ended")
		}
		if len(entry.Held) > 0 {
			fmt.Fprintf(look.stdout, "    held open for writing: %s\n", strings.Join(entry.Held[:min(len(entry.Held), 5)], ", "))
		}
	}
	if len(listed) == 0 {
		where := ""
		if thisSession {
			where = " in this session"
		}
		fmt.Fprintln(look.stdout, "nothing supervised"+where)
	}
	if others > 0 {
		fmt.Fprintf(look.stdout, "(%d more in other sessions: jst procs --all)\n", others)
	}
}

// age is a span as `1h05m` or `4m09s`.
func age(seconds float64) string {
	whole := int64(max(0, seconds))
	if whole >= 3600 {
		return fmt.Sprintf("%dh%02dm", whole/3600, whole%3600/60)
	}
	return fmt.Sprintf("%dm%02ds", whole/60, whole%60)
}

func cutRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func stopCommand(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	every := false
	var wanted []string
	for _, word := range args {
		if word == "--all" {
			every = true
			continue
		}
		wanted = append(wanted, word)
	}
	if len(wanted) == 0 {
		fmt.Fprintln(stderr, procsUsage)
		return statusUsage
	}
	look, err := newLooker(stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "jst procs: %v\n", err)
		return statusUsage
	}
	return look.stop(wanted, every)
}

// stop stops each entry named: by ID whoever started it; by NAME this session's only, unless
// --all. A living supervisor is asked through the stop file; a group is signalled here only when
// its supervisor is gone and its leader is proven by its start.
func (look *looker) stop(wanted []string, every bool) int {
	listed := look.place.entries()
	table := look.host.table()
	mine := procs.OwnSession(table, look.host.pid).Session
	var missing []string
	for _, each := range wanted {
		chosen, refused := look.choose(listed, each, every, mine)
		if refused || len(chosen) == 0 {
			missing = append(missing, each)
			continue
		}
		for _, entry := range chosen {
			if !look.stopOne(entry) {
				missing = append(missing, each)
			}
		}
	}
	for _, each := range missing {
		known := false
		for _, entry := range listed {
			known = known || entry.ID == each || entry.nameOf() == each
		}
		if !known {
			fmt.Fprintf(look.stderr, "jst procs: nothing supervised is called %s\n", each)
		}
	}
	if len(missing) > 0 {
		return 1
	}
	return 0
}

// choose are the entries a word names; refused is true when a NAME names none of this session's.
func (look *looker) choose(listed []Entry, wanted string, every bool, mine string) (chosen []Entry, refused bool) {
	var named []Entry
	for _, entry := range listed {
		if entry.ID == wanted {
			return []Entry{entry}, false
		}
		if entry.nameOf() == wanted {
			named = append(named, entry)
		}
	}
	if len(named) == 0 || every {
		return named, false
	}
	ids := make([]string, 0, len(named))
	for _, entry := range named {
		ids = append(ids, entry.ID)
	}
	if mine == "" {
		// Two terminals jackstay cannot tell apart are both "no session": a NAME would stop the other
		// one's entries too.
		fmt.Fprintf(look.stderr, "jst procs: %s: this session is not known to jackstay, so which of %s are its own cannot be "+
			"told: stop one by its ID, or all with --all\n", wanted, strings.Join(ids, ", "))
		return nil, true
	}
	for _, entry := range named {
		if entry.sessionOf() == mine {
			chosen = append(chosen, entry)
		}
	}
	if len(chosen) == 0 {
		fmt.Fprintf(look.stderr, "jst procs: %s names entries of other sessions only (%s): stop one by its ID, or all with "+
			"--all\n", wanted, strings.Join(ids, ", "))
		return nil, true
	}
	return chosen, false
}

// stopOne stops one entry's group; false when it was not stopped. A living supervisor is asked,
// and the group is signalled from here only once the supervisor is proven gone.
func (look *looker) stopOne(entry Entry) bool {
	table := look.host.table()
	if entry.Status == stateCleanupIncomplete {
		return look.incompleteStop(entry)
	}
	if look.host.supervisorAlive(table, entry) {
		switch look.askSupervisor(entry) {
		case stopConfirmed:
			fmt.Fprintf(look.stdout, "jst procs: %s%s stopped\n", entry.ID, inBrackets(entry.nameOf()))
			return true
		case stopIncomplete:
			return look.incompleteStop(entry)
		case stopPending:
			fmt.Fprintf(look.stderr, "jst procs: %s: its supervisor (process %d) was asked to stop it and has not finished "+
				"within %s; the request stays\n", entry.ID, entry.Supervisor, supervise.Seconds(answerBound))
			return false
		case stopSupervisorGone:
			table = look.host.table()
		default:
			return false
		}
	}
	if !look.host.supervisorGone(table, entry) {
		fmt.Fprintf(look.stderr, "jst procs: %s: whether its supervisor (process %d) still runs cannot be told here; "+
			"not signalled\n", entry.ID, entry.Supervisor)
		return false
	}
	if entry.LeaderStart != nil {
		same, known := sameProcess(table, entry.Group, *entry.LeaderStart)
		if known && !same && !look.host.groupAlive(table, entry.Group) {
			look.place.remove(entry.ID)
			fmt.Fprintf(look.stdout, "jst procs: %s had ended already\n", entry.ID)
			return true
		}
		if same {
			return look.signalOrphan(table, entry)
		}
	}
	fmt.Fprintf(look.stderr, "jst procs: %s: its supervisor is gone and the group %d cannot be shown to be the one it "+
		"started (its leader ended, or the process list cannot be read here); not signalled. Look: ps -axo "+
		"pid,pgid,lstart,command | grep -w %d; then `kill -- -%d`\n", entry.ID, entry.Group, entry.Group, entry.Group)
	return false
}

func (look *looker) incompleteStop(entry Entry) bool {
	fmt.Fprintf(look.stderr, "jst procs: %s: cleanup incomplete; whether all processes stopped cannot be confirmed; it stays listed\n", entry.ID)
	return false
}

type stopAnswer int

const (
	stopRequestFailed stopAnswer = iota
	stopConfirmed
	stopIncomplete
	stopPending
	stopSupervisorGone
)

// askSupervisor writes the stop file and waits for a confirmed removal, an incomplete cleanup
// record, or the supervisor's disappearance. A timed-out request remains for the supervisor.
func (look *looker) askSupervisor(entry Entry) stopAnswer {
	if err := look.place.askStop(entry.ID); err != nil {
		fmt.Fprintf(look.stderr, "jst procs: cannot ask %s to stop (%v)\n", entry.ID, err)
		return stopRequestFailed
	}
	until := look.host.clock.Now().Add(answerBound)
	for look.place.listed(entry.ID) {
		if latest, ok := look.place.read(entry.ID); ok && latest.Status == stateCleanupIncomplete {
			_ = look.place.removeStop(entry.ID)
			return stopIncomplete
		}
		if look.host.supervisorGone(look.host.table(), entry) {
			_ = look.place.removeStop(entry.ID)
			return stopSupervisorGone
		}
		if !look.host.clock.Now().Before(until) {
			return stopPending
		}
		look.host.clock.Sleep(answerPoll)
	}
	_ = look.place.removeStop(entry.ID)
	return stopConfirmed
}

// signalOrphan stops a group whose supervisor is gone and whose leader is proven, unless this
// command may not signal it.
func (look *looker) signalOrphan(table map[int]procs.Process, entry Entry) bool {
	switch look.host.refusal(table, entry.Group) {
	case proctree.SandboxForbids:
		fmt.Fprintf(look.stderr, "jst procs: %s: its supervisor is gone and the sandbox cannot signal group %d from here; "+
			"not signalled: run outside the sandbox\n", entry.ID, entry.Group)
		return false
	case proctree.NotThisAccount:
		fmt.Fprintf(look.stderr, "jst procs: %s: group %d is not this account's; not signalled\n", entry.ID, entry.Group)
		return false
	}
	gone, err := look.host.stopGroup(table, entry.Group)
	if err != nil {
		fmt.Fprintf(look.stderr, "jst procs: %s: %v\n", entry.ID, err)
	}
	note("stop", map[string]any{"id": entry.ID, "reason": "asked", "gone": gone})
	if !gone {
		fmt.Fprintf(look.stderr, "jst procs: %s: group %d did not stop; it stays listed\n", entry.ID, entry.Group)
		return false
	}
	fmt.Fprintf(look.stdout, "jst procs: %s%s stopped\n", entry.ID, inBrackets(entry.nameOf()))
	if !look.host.supervisorAlive(look.host.table(), entry) {
		look.place.remove(entry.ID)
	}
	return true
}
