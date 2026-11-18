package proctree

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/procs"
)

const ownUID = 501

// world is a fake process table: processes obey or ignore signals as each test says.
type world struct {
	now      time.Time
	table    map[int]procs.Process
	sent     []string
	ignores  map[int]bool          // TERM does not end it
	refuse   map[int]error         // the kernel's answer to any signal to this target
	onSleep  func(w *world)        // what happens while the stop pauses
	unread   bool                  // the table cannot be read
	spawned  map[int]procs.Process // added on the first pause
	killedAt time.Time
}

func (w *world) Now() time.Time { return w.now }

func (w *world) Sleep(pause time.Duration) {
	w.now = w.now.Add(pause)
	for pid, row := range w.spawned {
		w.table[pid] = row
	}
	w.spawned = nil
	if w.onSleep != nil {
		w.onSleep(w)
	}
}

func (w *world) read() (map[int]procs.Process, error) {
	if w.unread {
		return nil, errors.New("no table")
	}
	copied := map[int]procs.Process{}
	for pid, row := range w.table {
		copied[pid] = row
	}
	return copied, nil
}

func (w *world) signal(target int, sig syscall.Signal) error {
	name := map[syscall.Signal]string{unix.SIGTERM: "TERM", unix.SIGKILL: "KILL"}[sig]
	w.sent = append(w.sent, name+" "+itoa(target))
	if err, ok := w.refuse[target]; ok {
		return err
	}
	var hit []int
	if target < 0 {
		for pid, row := range w.table {
			if row.Group == -target {
				hit = append(hit, pid)
			}
		}
	} else if _, ok := w.table[target]; ok {
		hit = append(hit, target)
	}
	if len(hit) == 0 {
		return unix.ESRCH
	}
	for _, pid := range hit {
		if sig == unix.SIGKILL {
			w.killedAt = w.now
		}
		if sig == unix.SIGKILL || !w.ignores[pid] {
			delete(w.table, pid)
		}
	}
	return nil
}

func itoa(number int) string { return strconv.Itoa(number) }

func (w *world) system() System {
	return System{Read: w.read, Signal: w.signal, Clock: w, Poll: 100 * time.Millisecond, Settle: time.Second, UID: ownUID}
}

func row(pid, parent, group, session int, start int64) procs.Process {
	return procs.Process{PID: pid, Parent: parent, Group: group, Session: session, UID: ownUID, Start: start / 1_000_000,
		StartMicros: start, Name: "p" + itoa(pid)}
}

func newWorld(rows ...procs.Process) *world {
	table := map[int]procs.Process{}
	for _, entry := range rows {
		table[entry.PID] = entry
	}
	return &world{now: time.Unix(1_000, 0), table: table, ignores: map[int]bool{}, refuse: map[int]error{}}
}

func pids(members []Member) []int {
	var found []int
	for _, member := range members {
		found = append(found, member.PID)
	}
	return found
}

func TestSample(t *testing.T) {
	cases := []struct {
		name    string
		rows    []procs.Process
		follow  int
		members []int
	}{
		{"children and grandchildren by parent links",
			[]procs.Process{row(100, 1, 100, 100, 5), row(101, 100, 100, 100, 6), row(102, 101, 100, 100, 7), row(200, 1, 200, 200, 8)},
			0, []int{100, 101, 102}},
		{"a reparented process of a followed session a member is in",
			[]procs.Process{row(100, 1, 100, 100, 5), row(150, 1, 150, 100, 9), row(300, 1, 300, 300, 9)},
			100, []int{100, 150}},
		{"a pid given again is no member, nor are its children",
			[]procs.Process{row(100, 1, 100, 100, 99), row(101, 100, 100, 100, 100)},
			0, []int{100}},
		{"a session no running member is in is not followed",
			[]procs.Process{row(100, 1, 100, 100, 99), row(150, 1, 150, 100, 9)},
			100, []int{100}},
		{"a zombie member has no followers",
			[]procs.Process{{PID: 100, Parent: 1, Session: 100, StartMicros: 5, Zombie: true}, row(101, 100, 100, 100, 6)},
			0, []int{100}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tree := New()
			tree.Remember(100, 5)
			tree.FollowSession(test.follow)
			table := map[int]procs.Process{}
			for _, entry := range test.rows {
				table[entry.PID] = entry
			}
			tree.Sample(table)
			if got := pids(tree.Members()); !reflect.DeepEqual(got, test.members) {
				t.Fatalf("members %v, want %v", got, test.members)
			}
		})
	}
}

func TestStopEndsByTermWithinTheGrace(t *testing.T) {
	w := newWorld(row(100, 1, 100, 100, 5), row(101, 100, 100, 100, 6))
	tree := New()
	tree.Remember(100, 5)
	result, err := w.system().Stop(tree, Targets{Groups: []int{100}, Members: true}, 5*time.Second)
	if err != nil || len(result.Survivors) != 0 || len(result.Refused) != 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
	for _, sent := range w.sent {
		if strings.HasPrefix(sent, "KILL") {
			t.Fatalf("KILL sent: %v", w.sent)
		}
	}
	if w.sent[0] != "TERM -100" {
		t.Fatalf("the group first: %v", w.sent)
	}
}

func TestStopKillsAfterTheGraceWhatIgnoresTerm(t *testing.T) {
	w := newWorld(row(100, 1, 100, 100, 5))
	w.ignores[100] = true
	started := w.now
	tree := New()
	tree.Remember(100, 5)
	result, err := w.system().Stop(tree, Targets{Members: true}, 5*time.Second)
	if err != nil || len(result.Survivors) != 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
	if want := []string{"TERM 100", "KILL 100"}; !reflect.DeepEqual(w.sent, want) {
		t.Fatalf("sent %v, want %v", w.sent, want)
	}
	if got := w.killedAt.Sub(started); got != 5*time.Second {
		t.Fatalf("KILL after %v, want the grace", got)
	}
}

func TestStopSendsTermToAChildFoundWhileWaiting(t *testing.T) {
	w := newWorld(row(100, 1, 100, 100, 5))
	w.ignores[100] = true
	w.spawned = map[int]procs.Process{101: row(101, 100, 100, 100, 6)}
	w.ignores[101] = true
	tree := New()
	tree.Remember(100, 5)
	result, err := w.system().Stop(tree, Targets{Members: true}, time.Second)
	if err != nil || len(result.Survivors) != 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
	want := []string{"TERM 100", "TERM 101", "KILL 100", "KILL 101"}
	if !reflect.DeepEqual(w.sent, want) {
		t.Fatalf("sent %v, want %v", w.sent, want)
	}
}

func TestStopNeverSignalsAPidGivenAgain(t *testing.T) {
	w := newWorld(row(100, 1, 100, 100, 77))
	tree := New()
	tree.Remember(100, 5)
	result, err := w.system().Stop(tree, Targets{Groups: []int{100}, Members: true}, time.Second)
	if err != nil || len(w.sent) != 0 || len(result.Survivors) != 0 {
		t.Fatalf("sent %v result %+v err %v", w.sent, result, err)
	}
}

func TestStopSignalsAGroupOnlyWhileAMemberIsInIt(t *testing.T) {
	w := newWorld(row(100, 1, 100, 100, 5), row(300, 1, 300, 300, 9))
	tree := New()
	tree.Remember(100, 5)
	_, err := w.system().Stop(tree, Targets{Groups: []int{300, 100}}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"TERM -100"}; !reflect.DeepEqual(w.sent, want) {
		t.Fatalf("sent %v, want %v", w.sent, want)
	}
	if _, ok := w.table[300]; !ok {
		t.Fatal("a group without a member was signalled")
	}
}

func TestStopNamesRefusalsAndSurvivors(t *testing.T) {
	other := row(102, 100, 100, 100, 7)
	other.UID = 0
	w := newWorld(row(100, 1, 100, 100, 5), row(101, 100, 100, 100, 6), other, row(103, 100, 100, 100, 8))
	w.refuse[101] = unix.EPERM
	w.refuse[102] = unix.EPERM
	w.refuse[103] = unix.ESRCH
	w.onSleep = func(w *world) { delete(w.table, 103) }
	tree := New()
	tree.Remember(100, 5)
	result, err := w.system().Stop(tree, Targets{Members: true}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := pids(survivorMembers(result.Survivors)); !reflect.DeepEqual(got, []int{101, 102}) {
		t.Fatalf("survivors %v", got)
	}
	var texts []string
	for _, refused := range result.Refused {
		texts = append(texts, refused.Error())
	}
	want := []string{"process 101: the sandbox cannot signal it from here: run outside the sandbox",
		"process 102: not this account's"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("refused %q, want %q", texts, want)
	}
	if !errors.Is(result.Refused[0], unix.EPERM) || result.Refused[0].Kind != SandboxForbids {
		t.Fatalf("refusal %+v", result.Refused[0])
	}
	if result.Survivors[0].Name != "p101" {
		t.Fatalf("survivor %+v", result.Survivors[0])
	}
}

func TestStopRefusedGroupAndOtherFailure(t *testing.T) {
	w := newWorld(row(100, 1, 100, 100, 5))
	w.refuse[-100] = unix.EPERM
	w.refuse[100] = unix.EINVAL
	tree := New()
	tree.Remember(100, 5)
	result, err := w.system().Stop(tree, Targets{Groups: []int{100}, Members: true}, time.Second)
	if err != nil || len(result.Refused) != 2 {
		t.Fatalf("result %+v err %v", result, err)
	}
	texts := []string{result.Refused[0].Error(), result.Refused[1].Error()}
	want := []string{"process 100: cannot be signalled: invalid argument",
		"process group 100: the sandbox cannot signal it from here: run outside the sandbox"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("refused %q, want %q", texts, want)
	}
}

func TestStopFailsWithoutTheTable(t *testing.T) {
	w := newWorld()
	w.unread = true
	if _, err := w.system().Stop(New(), Targets{Members: true}, time.Second); err == nil {
		t.Fatal("a stop without a table")
	}
}

func TestStopKeepsTheLastViewWhenARereadFails(t *testing.T) {
	w := newWorld(row(100, 1, 100, 100, 5))
	w.ignores[100] = true
	w.onSleep = func(w *world) { w.unread = !w.unread }
	tree := New()
	tree.Remember(100, 5)
	result, err := w.system().Stop(tree, Targets{Members: true}, time.Second)
	if err != nil || len(result.Survivors) != 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
}

func TestStopSendsNoKillWhenTheTableCannotBeReadBeforeIt(t *testing.T) {
	// The member ends, its id goes to a later process, and the
	// table cannot be read from then on. The last table read still shows the member; KILL by it
	// would hit the later process.
	w := newWorld(row(100, 1, 100, 100, 5))
	w.ignores[100] = true
	w.onSleep = func(w *world) {
		w.table[100] = row(100, 1, 100, 100, 77)
		w.unread = true
	}
	tree := New()
	tree.Remember(100, 5)
	result, err := w.system().Stop(tree, Targets{Groups: []int{100}, Members: true}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"TERM -100", "TERM 100"}; !reflect.DeepEqual(w.sent, want) {
		t.Fatalf("sent %v, want %v", w.sent, want)
	}
	if !result.KillWithheld {
		t.Fatalf("result %+v: the withheld KILL not said", result)
	}
}

func TestStopKillsOnlyWhatAFreshReadConfirms(t *testing.T) {
	// The member's id goes to a later process at the grace's last poll, whose read fails; the read
	// just before KILL works and shows the later process, so nothing is killed and the stop is
	// complete.
	w := newWorld(row(100, 1, 100, 100, 5))
	w.ignores[100] = true
	sleeps := 0
	w.onSleep = func(w *world) {
		sleeps++
		if sleeps == 10 { // the grace is ten polls
			w.table[100] = row(100, 1, 100, 100, 77)
			w.unread = true
		}
	}
	system := w.system()
	system.Read = func() (map[int]procs.Process, error) {
		table, err := w.read()
		w.unread = false // one read fails, the next works
		return table, err
	}
	tree := New()
	tree.Remember(100, 5)
	result, err := system.Stop(tree, Targets{Groups: []int{100}, Members: true}, time.Second)
	if err != nil || result.KillWithheld || len(result.Survivors) != 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
	if want := []string{"TERM -100", "TERM 100"}; !reflect.DeepEqual(w.sent, want) {
		t.Fatalf("sent %v, want %v", w.sent, want)
	}
}

func survivorMembers(survivors []Survivor) []Member {
	var members []Member
	for _, survivor := range survivors {
		members = append(members, survivor.Member)
	}
	return members
}

func TestRealSystem(t *testing.T) {
	system := Real()
	if system.Read == nil || system.Signal == nil || system.Clock == nil || system.Poll <= 0 || system.Settle <= 0 {
		t.Fatalf("system %+v", system)
	}
	before := system.Clock.Now()
	system.Clock.Sleep(0)
	if system.Clock.Now().Before(before) {
		t.Fatal("the clock went back")
	}
}

func TestFoldersInside(t *testing.T) {
	root := t.TempDir()
	resolved := mustResolve(t, root)
	foreign := row(400, 1, 400, 400, 9)
	foreign.UID = 0
	base := []procs.Process{row(100, 1, 100, 100, 5), row(101, 1, 101, 101, 6), foreign}
	cases := []struct {
		name     string
		listing  string
		exited   bool
		after    func(map[int]procs.Process)
		inside   map[int]string
		complete bool
		unseen   []int
	}{
		{"every process of this account listed",
			"p100\nfcwd\nn" + resolved + "\np101\nfcwd\nn" + resolved + "x/sub\n", true, nil,
			map[int]string{100: resolved}, true, nil},
		{"a process that ran throughout, not listed: partial, named",
			"p100\nfcwd\nn" + resolved + "/deep\n", true, nil,
			map[int]string{100: resolved + "/deep"}, false, []int{101}},
		{"a process gone before the second read does not count",
			"p100\nfcwd\nn/elsewhere\n", true, func(table map[int]procs.Process) { delete(table, 101) },
			map[int]string{}, true, nil},
		{"listed without a folder: partial",
			"p100\nfcwd\nn/elsewhere\np101\n", true, nil, map[int]string{}, false, []int{101}},
		{"a process started while lsof walked, not listed, proves nothing about what lsof sees",
			"p100\nfcwd\nn/elsewhere\np101\nfcwd\nn/elsewhere\n", true,
			func(table map[int]procs.Process) { table[102] = row(102, 1, 102, 102, 70) },
			map[int]string{}, true, nil},
		{"a process started while lsof walked, listed in the folder, holds it",
			"p100\nfcwd\nn/elsewhere\np101\nfcwd\nn/elsewhere\np102\nfcwd\nn" + resolved + "\n", true,
			func(table map[int]procs.Process) { table[102] = row(102, 1, 102, 102, 70) },
			map[int]string{102: resolved}, true, nil},
		{"an id given again while lsof walked: what lsof named counts for neither",
			"p100\nfcwd\nn/elsewhere\np101\nfcwd\nn" + resolved + "\n", true,
			func(table map[int]procs.Process) { table[101] = row(101, 1, 101, 101, 66) },
			map[int]string{}, true, nil},
		{"a zombie or another account's process does not count",
			"p100\nfcwd\nn/elsewhere\np101\nfcwd\nn/elsewhere\n", true,
			func(table map[int]procs.Process) {
				dead := row(103, 1, 103, 103, 71)
				dead.Zombie = true
				table[103] = dead
				theirs := row(104, 1, 104, 104, 72)
				theirs.UID = 0
				table[104] = theirs
			},
			map[int]string{}, true, nil},
		{"a holder that ended before the second read holds nothing",
			"p100\nfcwd\nn" + resolved + "\np101\nfcwd\nn/elsewhere\n", true,
			func(table map[int]procs.Process) { delete(table, 100) },
			map[int]string{}, true, nil},
		{"lsof ended with a failure: partial, nobody named",
			"p100\nfcwd\nn" + resolved + "\np101\nn/x\n", false, nil, map[int]string{100: resolved}, false, nil},
		{"the sandbox: lsof lists only its caller's own, ends with 0: partial, every other named",
			"p100\nfcwd\nn/elsewhere\n", true, nil, map[int]string{}, false, []int{101}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			sources := cwdSources{uid: ownUID,
				read: func() (map[int]procs.Process, error) {
					reads++
					table := map[int]procs.Process{}
					for _, entry := range base {
						table[entry.PID] = entry
					}
					if reads == 2 && test.after != nil {
						test.after(table)
					}
					return table, nil
				},
				list: func(context.Context) ([]byte, bool, error) { return []byte(test.listing), test.exited, nil },
			}
			view, err := foldersInside(context.Background(), root, sources)
			if err != nil || view.Complete != test.complete || !reflect.DeepEqual(view.Inside, test.inside) ||
				!slices.Equal(view.Unseen, test.unseen) {
				t.Fatalf("view %+v err %v; want inside %v complete %v unseen %v", view, err, test.inside, test.complete, test.unseen)
			}
			if reads != 2 {
				t.Fatalf("the table read %d times", reads)
			}
		})
	}
}

func TestFoldersInsideFailures(t *testing.T) {
	good := func() (map[int]procs.Process, error) { return map[int]procs.Process{}, nil }
	bad := func() (map[int]procs.Process, error) { return nil, errors.New("no table") }
	listing := func(context.Context) ([]byte, bool, error) { return nil, true, nil }
	failing := func(context.Context) ([]byte, bool, error) { return nil, false, errors.New("no lsof") }
	cases := []struct {
		name    string
		sources cwdSources
	}{
		{"no table", cwdSources{read: bad, list: listing}},
		{"no lsof", cwdSources{read: good, list: failing}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := foldersInside(context.Background(), t.TempDir(), test.sources); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func mustResolve(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
