package procs

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParseStatReadsANameWithSpacesAndParentheses(t *testing.T) {
	stat := []byte("4242 (my (odd) name) Z 7 4242 4000 0 -1 4194304 1 0 0 0 0 0 0 0 20 0 1 0 250 0 0\n")
	process, session, err := parseStat(stat, 1_700_000_000)
	if err != nil {
		t.Fatal(err)
	}
	want := Process{PID: 4242, Parent: 7, Group: 4242, Name: "my (odd) name", Zombie: true,
		Start: 1_700_000_002, StartMicros: 1_700_000_002_500_000}
	if process != want || session != 4000 {
		t.Fatalf("got %+v session %d, want %+v session 4000", process, session, want)
	}
}

func TestParseStatRefusesAShortLine(t *testing.T) {
	for _, stat := range []string{"", "12 no name 3", "12 (x) S 1 2"} {
		if _, _, err := parseStat([]byte(stat), 0); err == nil {
			t.Errorf("%q: no error", stat)
		}
	}
}

func TestStatusGivesTheEffectiveUserAndTheThreads(t *testing.T) {
	uid, threads, err := readStatus([]byte("Name:\tx\nUid:\t1000\t0\t0\t0\nGid:\t1\t1\t1\t1\nThreads:\t3\n"))
	if err != nil || uid != 0 || threads != 3 {
		t.Fatalf("uid %d threads %d err %v", uid, threads, err)
	}
	for _, status := range []string{"Name:\tx\n", "Uid:\t1\t1\t1\t1\n", "Threads:\t1\n", "Uid:\t1\nThreads:\t1\n"} {
		if _, _, err := readStatus([]byte(status)); err == nil {
			t.Errorf("%q: no error", status)
		}
	}
}

func TestTheBootBaseIsFixedByTheBootID(t *testing.T) {
	base, err := bootBase("6f1b2c3d-0000-4000-8000-000000000000\n")
	if err != nil || base != 0x6f1b2c3d {
		t.Fatalf("base %d err %v", base, err)
	}
	if _, err := bootBase("abc"); err == nil {
		t.Fatal("a short boot id, no error")
	}
}

func TestAProcessReadTwiceHasOneStart(t *testing.T) {
	first, found, err := Row(os.Getpid())
	if err != nil || !found {
		t.Fatal(found, err)
	}
	second, _, _ := Row(os.Getpid())
	if first.StartMicros != second.StartMicros || first.Start != second.Start {
		t.Fatalf("%+v then %+v", first, second)
	}
}

// fakeProc writes a /proc of the given rows (pid → state) under a test folder and points procRoot
// at it for the test.
func fakeProc(t *testing.T, rows map[int]string) {
	t.Helper()
	root := t.TempDir()
	for pid, state := range rows {
		folder := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}
		stat := fmt.Sprintf("%d (p%d) %s 1 %d %d 0 -1 0 0 0 0 0 0 0 0 20 0 1 0 100 0 0\n", pid, pid, state, 4242, 4000)
		status := "Name:\tp\nUid:\t1000\t1000\t1000\t1000\nThreads:\t1\n"
		if err := os.WriteFile(filepath.Join(folder, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "status"), []byte(status), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	previous := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = previous })
}

// A process listed, then starting a child of its group and ending before its row is read, is read
// as a zombie; its child was not in that listing. The table must hold the child: the zombie is no
// member, so without it the group would look empty while the child runs.
func TestAZombieReadAfterTheListingMakesTheListingReadAgain(t *testing.T) {
	fakeProc(t, map[int]string{10: "Z", 11: "S"})
	listings := [][]int{{10}, {10, 11}}
	list := func() ([]int, error) {
		next := listings[0]
		if len(listings) > 1 {
			listings = listings[1:]
		}
		return next, nil
	}
	table, err := readTableWith(list, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if child, ok := table[11]; !ok || child.Zombie || child.Group != 4242 {
		t.Fatalf("the child is not in the table: %+v", table)
	}
}

// Processes that only start under the reading do not make it read again: on a busy machine a
// listing that adds nothing may never come.
func TestNewProcessesAloneDoNotMakeTheListingReadAgain(t *testing.T) {
	fakeProc(t, map[int]string{10: "S", 11: "S"})
	readings := 0
	list := func() ([]int, error) {
		readings++
		return []int{10, 11}, nil
	}
	if _, err := readTableWith(list, 0, false); err != nil || readings != 1 {
		t.Fatalf("readings %d err %v", readings, err)
	}
}

// A parent, group or session beyond 32 bits is refused rather than cut down to another number.
func TestParseStatRefusesAProcessNumberBeyondThirtyTwoBits(t *testing.T) {
	const tail = " 0 -1 4194304 1 0 0 0 0 0 0 0 20 0 1 0 250 0 0\n"
	for _, numbers := range []string{"4294967303 4242 4000", "7 4294967303 4000", "7 4242 4294967303", "7 4242 -2147483649"} {
		if _, _, err := parseStat([]byte("4242 (x) S "+numbers+tail), 0); err == nil {
			t.Errorf("%q: no error", numbers)
		}
	}
}
