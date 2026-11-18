package flock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/procs"
)

// fakeClock moves only when a wait asks it to (W-013); each pause asked for is kept, and a hook
// may act while the run waits.
type fakeClock struct {
	mutex  sync.Mutex
	now    time.Time
	pauses []time.Duration
	hook   func(count int)
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Now()} }

func (c *fakeClock) Now() time.Time {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.now
}

func (c *fakeClock) After(pause time.Duration) <-chan time.Time {
	c.mutex.Lock()
	c.now = c.now.Add(pause)
	c.pauses = append(c.pauses, pause)
	count, hook := len(c.pauses), c.hook
	c.mutex.Unlock()
	if hook != nil {
		hook(count)
	}
	channel := make(chan time.Time, 1)
	channel <- c.Now()
	return channel
}

// holdFile holds a lock on path from another open file, as another run would.
func holdFile(t *testing.T, path string) *os.File {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func names(t *testing.T, folder string) []string {
	t.Helper()
	entries, _ := os.ReadDir(folder)
	var found []string
	for _, entry := range entries {
		found = append(found, entry.Name())
	}
	sort.Strings(found)
	return found
}

func TestPlaceAfterFollowsTheLineButNotATicketFarAhead(t *testing.T) {
	folder := t.TempDir()
	now := time.Now()
	hourAhead := now.Add(time.Hour).UnixNano()
	os.WriteFile(filepath.Join(folder, fmt.Sprintf("%020d-1", hourAhead)), nil, 0o600)
	if got := placeAfter(folder, now); got != hourAhead+1 {
		t.Fatalf("after an hour-ahead ticket: %d, want %d", got, hourAhead+1)
	}
	os.Remove(filepath.Join(folder, fmt.Sprintf("%020d-1", hourAhead)))
	os.WriteFile(filepath.Join(folder, "99999999999999999999-1"), nil, 0o600)
	if got := placeAfter(folder, now); got != now.UnixNano() || len(fmt.Sprintf("%020d", got)) != 20 {
		t.Fatalf("a ticket days ahead was followed: %d", got)
	}
}

func TestSweepRemovesWhatDeadRunsLeft(t *testing.T) {
	folder := t.TempDir()
	clock := newFakeClock()
	longAgo := clock.Now().Add(-time.Hour)
	for number := range 40 {
		stale := filepath.Join(folder, fmt.Sprintf("making-%d-%d", 90000+number, number))
		os.WriteFile(stale, nil, 0o600)
		os.Chtimes(stale, longAgo, longAgo)
	}
	os.WriteFile(filepath.Join(folder, "making-90100-1"), nil, 0o600)
	inHand := filepath.Join(folder, "making-90101-1")
	holdFile(t, inHand)
	os.Chtimes(inHand, longAgo, longAgo)
	os.WriteFile(filepath.Join(folder, "99999999999999999999-1"), nil, 0o600)
	sweep(folder, clock.Now())
	if got := names(t, folder); strings.Join(got, ",") != "making-90100-1,making-90101-1" {
		t.Fatalf("left %v", got)
	}
}

func TestATicketFarAheadIsRenumberedAndDroppedUnderItsNewName(t *testing.T) {
	folder := t.TempDir()
	clock := newFakeClock()
	place, err := newTicket(folder, "the test", 4242, clock)
	if err != nil {
		t.Fatal(err)
	}
	far := "99999999999999999998-4242"
	os.Rename(filepath.Join(folder, place.name), filepath.Join(folder, far))
	place.name = far
	if placeAfter(folder, clock.Now()) > clock.Now().UnixNano() {
		t.Fatal("a new ticket was numbered after one from a clock far ahead")
	}
	if ahead := place.ahead(); ahead != 0 || numberOf(place.name) != clock.Now().UnixNano() {
		t.Fatalf("ahead %d, name %s", ahead, place.name)
	}
	if got := names(t, folder); len(got) != 1 || got[0] != place.name || heldByNobody(filepath.Join(folder, place.name)) {
		t.Fatalf("folder %v", got)
	}
	place.drop()
	if got := names(t, folder); len(got) != 0 {
		t.Fatalf("left %v", got)
	}
}

func TestAheadCountsTheLivingAndRemovesTheDead(t *testing.T) {
	folder := t.TempDir()
	clock := newFakeClock()
	early := clock.Now().Add(-time.Second).UnixNano()
	living := filepath.Join(folder, fmt.Sprintf("%020d-11", early))
	holdFile(t, living)
	dead := filepath.Join(folder, fmt.Sprintf("%020d-12", early+1))
	os.WriteFile(dead, nil, 0o600)
	place, err := newTicket(folder, "", 4242, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer place.drop()
	if ahead := place.ahead(); ahead != 1 {
		t.Fatalf("ahead %d", ahead)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatal("a dead run's ticket was not removed")
	}
}

func request(lock string, clock *fakeClock, out *bytes.Buffer) Request {
	return Request{Lock: lock, Words: "project demo: true in /here (process 1), since now", Out: out, Clock: clock,
		Schedule: DefaultSchedule()}
}

func TestAFreeLockIsTakenAtOnceAndLeavesNoTicket(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "nested", "suite.lock")
	var out bytes.Buffer
	held, err := Acquire(context.Background(), request(lock, newFakeClock(), &out))
	if err != nil {
		t.Fatal(err)
	}
	if !IsHeld(lock) || out.Len() != 0 || len(names(t, QueueFolder(lock))) != 0 {
		t.Fatalf("held %v, said %q, line %v", IsHeld(lock), out.String(), names(t, QueueFolder(lock)))
	}
	info, _ := os.Stat(filepath.Dir(lock))
	file, _ := os.Stat(lock)
	if info.Mode().Perm() != 0o700 || file.Mode().Perm() != 0o600 {
		t.Fatalf("modes %v %v", info.Mode().Perm(), file.Mode().Perm())
	}
	held.Release()
	if IsHeld(lock) {
		t.Fatal("released but held")
	}
}

func TestAWaitThatIsOverGivesUpRunningNothing(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "suite.lock")
	holdFile(t, lock)
	os.WriteFile(HolderPath(lock), []byte("project other: just gate in /x, the command's process group 7 (the lock's 6), since then\n"), 0o600)
	clock := newFakeClock()
	var out bytes.Buffer
	asked := request(lock, clock, &out)
	asked.Wait, asked.Bounded = 150*time.Second, true
	began := clock.Now()
	_, err := Acquire(context.Background(), asked)
	var gaveUp *GaveUp
	if !errors.As(err, &gaveUp) {
		t.Fatalf("err %v", err)
	}
	said := out.String()
	if strings.Count(said, "jst lock: waiting for another heavy run on this machine") != 3 || !strings.Contains(said, "Nothing was run") ||
		!strings.Contains(said, "the last to take the lock: project other") || !strings.Contains(said, "gave up after waiting 150 s") {
		t.Fatalf("said %q", said)
	}
	if clock.Now().Sub(began) != 150*time.Second || len(names(t, QueueFolder(lock))) != 0 {
		t.Fatalf("waited %v, line %v", clock.Now().Sub(began), names(t, QueueFolder(lock)))
	}
}

func TestThePausesGrowToTheirBound(t *testing.T) {
	for _, each := range []struct {
		name  string
		ahead bool
		most  time.Duration
	}{{"next in line", false, 200 * time.Millisecond}, {"behind another", true, time.Second}} {
		t.Run(each.name, func(t *testing.T) {
			lock := filepath.Join(t.TempDir(), "suite.lock")
			holdFile(t, lock)
			if each.ahead {
				holdFile(t, filepath.Join(QueueFolder(lock), fmt.Sprintf("%020d-11", time.Now().Add(-time.Second).UnixNano())))
			}
			clock := newFakeClock()
			asked := request(lock, clock, &bytes.Buffer{})
			asked.Wait, asked.Bounded = 10*time.Second, true
			Acquire(context.Background(), asked)
			if clock.pauses[0] != 50*time.Millisecond || clock.pauses[1] != 75*time.Millisecond {
				t.Fatalf("first pauses %v", clock.pauses[:2])
			}
			longest := time.Duration(0)
			for _, pause := range clock.pauses[:len(clock.pauses)-1] {
				longest = max(longest, pause)
			}
			if longest != each.most {
				t.Fatalf("longest pause %v, want %v", longest, each.most)
			}
		})
	}
}

func TestACancelledWaitEndsWithItsCauseAndLeavesNoTicket(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "suite.lock")
	holdFile(t, lock)
	clock := newFakeClock()
	waitContext, cancel := context.WithCancelCause(context.Background())
	stop := errors.New("asked to stop")
	clock.hook = func(count int) {
		if count == 3 {
			cancel(stop)
		}
	}
	_, err := Acquire(waitContext, request(lock, clock, &bytes.Buffer{}))
	if !errors.Is(err, stop) || len(names(t, QueueFolder(lock))) != 0 {
		t.Fatalf("err %v, line %v", err, names(t, QueueFolder(lock)))
	}
}

func TestAnOlderLockBusyIsWaitedForHoldingNothing(t *testing.T) {
	folder := t.TempDir()
	lock, older := filepath.Join(folder, "suite.lock"), filepath.Join(folder, "older", "whole-suite.lock")
	other := holdFile(t, older)
	clock := newFakeClock()
	sharedHeldMeanwhile := false
	clock.hook = func(count int) {
		if IsHeld(lock) {
			sharedHeldMeanwhile = true
		}
		if count == 5 {
			other.Close()
		}
	}
	var out bytes.Buffer
	asked := request(lock, clock, &out)
	asked.Older = []string{older}
	held, err := Acquire(context.Background(), asked)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if sharedHeldMeanwhile || !IsHeld(lock) || !IsHeld(older) {
		t.Fatalf("shared held while waiting %v, now %v %v", sharedHeldMeanwhile, IsHeld(lock), IsHeld(older))
	}
	if !strings.Contains(out.String(), "earlier lock "+older) || !strings.Contains(out.String(), "starting.") {
		t.Fatalf("said %q", out.String())
	}
	held.WriteNote("project demo: x in /y, about to start (the lock's 1), since now")
	if !strings.HasPrefix(HolderNote(older), "project demo: ") || !strings.HasPrefix(HolderNote(lock), "project demo: ") {
		t.Fatal("the note is beside every lock held")
	}
}

func TestAGaveUpRunNamesItselfNowhere(t *testing.T) {
	folder := t.TempDir()
	lock, older := filepath.Join(folder, "suite.lock"), filepath.Join(folder, "older.lock")
	holdFile(t, older)
	asked := request(lock, newFakeClock(), &bytes.Buffer{})
	asked.Older, asked.Wait, asked.Bounded = []string{older}, 2*time.Second, true
	_, err := Acquire(context.Background(), asked)
	var gaveUp *GaveUp
	if !errors.As(err, &gaveUp) || gaveUp.Lock != older || IsHeld(lock) || HolderNote(lock) != "" {
		t.Fatalf("err %v, held %v, note %q", err, IsHeld(lock), HolderNote(lock))
	}
}

func TestALockThatCannotBeOpenedIsUnusable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")
	os.WriteFile(file, nil, 0o600)
	_, err := Acquire(context.Background(), request(filepath.Join(file, "x.lock"), newFakeClock(), &bytes.Buffer{}))
	var unusable *Unusable
	if !errors.As(err, &unusable) || !strings.Contains(unusable.Words, "cannot open the lock") {
		t.Fatalf("err %v", err)
	}
}

func TestPathIsJackstayLockOrUnderTheHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	t.Setenv(PathVariable, "")
	if got, err := Path(); err != nil || got != filepath.Join(home, ".all", "heavy-suite.lock") {
		t.Fatalf("path %q %v", got, err)
	}
	t.Setenv(PathVariable, "relative.lock")
	var unusable *Unusable
	if _, err := Path(); !errors.As(err, &unusable) || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("err %v", err)
	}
}

// table is a fake process table: self (100) under 50 under 10.
func table() map[int]procs.Process {
	return map[int]procs.Process{
		10:  {PID: 10, Parent: 1, Name: "zsh"},
		50:  {PID: 50, Parent: 10, Name: "jst"},
		100: {PID: 100, Parent: 50, Name: "jst"},
		200: {PID: 200, Parent: 1, Name: "jst"},
	}
}

func TestUnderHolderIsProvedNotBelieved(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "suite.lock")
	claim := RealPath(lock) + ":50"
	if UnderHolder(lock, claim, table(), 100) {
		t.Fatal("a lock nobody holds excuses nothing")
	}
	holdFile(t, lock)
	if !UnderHolder(lock, claim, table(), 100) {
		t.Fatal("an ancestor that holds the lock")
	}
	for _, wrong := range []string{RealPath(lock) + ":200", RealPath(lock) + ":999", "/other.lock:50", "garbage", RealPath(lock) + ":x"} {
		if UnderHolder(lock, wrong, table(), 100) {
			t.Fatalf("%q was believed", wrong)
		}
	}
	if UnderHolder(lock, claim, nil, 100) {
		t.Fatal("without the table and without the note")
	}
	os.WriteFile(HolderPath(lock), []byte("project p: x in /y, about to start (the lock's 50), since now\n"), 0o600)
	if !UnderHolder(lock, claim, nil, 100) {
		t.Fatal("without the table the note names the holder")
	}
}

func TestPlanOlder(t *testing.T) {
	folder := t.TempDir()
	lock, older := filepath.Join(folder, "suite.lock"), filepath.Join(folder, "older.lock")
	os.WriteFile(lock, nil, 0o600)
	link := filepath.Join(folder, "link.lock")
	os.Symlink(lock, link)
	none := func(string) string { return "" }
	for _, refused := range [][]Older{{{Path: lock, Variable: "X"}}, {{Path: link, Variable: "X"}}, {{Path: "relative.lock", Variable: "X"}}} {
		var unusable *Unusable
		if _, err := PlanOlder(lock, refused, none, table(), 100); !errors.As(err, &unusable) {
			t.Fatalf("%v: %v", refused, err)
		}
	}
	plan, err := PlanOlder(lock, []Older{{Path: older, Variable: "A"}, {Path: older, Variable: "B"}}, none, table(), 100)
	if err != nil || len(plan.Older) != 1 || plan.Variables["A"] != RealPath(older)+":100" || plan.Variables["B"] != plan.Variables["A"] {
		t.Fatalf("plan %+v %v", plan, err)
	}
	holdFile(t, older)
	ancestor := func(name string) string {
		if name == "A" {
			return RealPath(older) + ":50"
		}
		return ""
	}
	plan, err = PlanOlder(lock, []Older{{Path: older, Variable: "A"}, {Path: older, Variable: "B"}}, ancestor, table(), 100)
	if err != nil || len(plan.Older) != 0 || len(plan.Variables) != 0 {
		t.Fatalf("an older lock an ancestor holds: plan %+v %v", plan, err)
	}
}

// TestFilesWrittenByOtherRuns reads a line and a holder note written here by hand, held as a
// living run of another process holds them.
func TestFilesWrittenByOtherRuns(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "heavy-suite.lock")
	holdFile(t, lock)
	note := "project storefront: make test in /Users/me/storefront, the command's process group 4300 (the lock's 4242), since 2026-11-16 10:00:00"
	os.WriteFile(HolderPath(lock), []byte(note+"\n"), 0o600)
	queue := QueueFolder(lock)
	first := filepath.Join(queue, "01763290000000000001-5001")
	holdFile(t, first)
	os.WriteFile(first, []byte("project widgets: just gate in /Users/me/widgets (process 5001), since 2026-11-16 10:01:00\n"), 0o600)
	second := filepath.Join(queue, "01763290000000000002-5002")
	holdFile(t, second)
	os.WriteFile(filepath.Join(queue, "01763290000000000000-5000"), []byte("dead\n"), 0o600) // nobody holds it
	report := mustStatus(t, lock, nil)
	if !report.Held || report.Holder == nil || *report.Holder != note || len(report.Waiting) != 2 ||
		!strings.HasPrefix(report.Waiting[0], "project widgets: just gate") || report.Waiting[1] != "a run that left no note (process 5002)" {
		t.Fatalf("report %+v", report)
	}
	if !UnderHolder(lock, RealPath(lock)+":4242", nil, 1) {
		t.Fatal("the note names its holder")
	}
	place, err := newTicket(queue, "", 6000, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	defer place.drop()
	if ahead := place.ahead(); ahead != 2 {
		t.Fatalf("a run behind two others: ahead %d", ahead)
	}
}

func TestStatusTextAndJSON(t *testing.T) {
	folder := t.TempDir()
	lock, older := filepath.Join(folder, "suite.lock"), filepath.Join(folder, "older.lock")
	free := mustStatus(t, lock, []string{older})
	if text := free.Text(); !strings.Contains(text, "running: nobody") || !strings.Contains(text, "waiting: 0\n") ||
		!strings.Contains(text, "older lock "+older+": free") {
		t.Fatalf("text %q", text)
	}
	data, _ := json.Marshal(free)
	var decoded map[string]any
	json.Unmarshal(data, &decoded)
	if decoded["held"] != false || decoded["holder"] != nil || decoded["lock"] != lock || len(decoded["older"].([]any)) != 1 {
		t.Fatalf("json %s", data)
	}
	holdFile(t, lock)
	os.WriteFile(HolderPath(lock), []byte("project demo: x in /y\n"), 0o600)
	holdFile(t, filepath.Join(QueueFolder(lock), "01763290000000000001-5001"))
	if text := mustStatus(t, lock, nil).Text(); !strings.Contains(text, "running: project demo: x in /y") ||
		!strings.Contains(text, "waiting: 1, first to go first:\n  1. a run that left no note (process 5001)") {
		t.Fatalf("text %q", text)
	}
}

func mustStatus(t *testing.T, lock string, older []string) Report {
	t.Helper()
	report, err := Status(lock, older)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// A lock file or a line that could not be looked at read as "the lock is
// free". Not there is free; there and unreadable is an error.
func TestAnUnreadableLockOrLineIsAnErrorNotFree(t *testing.T) {
	folder := t.TempDir()
	lock := filepath.Join(folder, "suite.lock")
	if err := os.WriteFile(lock, nil, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := Status(lock, nil); err == nil || !strings.Contains(err.Error(), "cannot be looked at") {
		t.Fatalf("an unreadable lock: %v", err)
	}
	if err := os.Chmod(lock, 0o600); err != nil {
		t.Fatal(err)
	}
	queue := QueueFolder(lock)
	if err := os.MkdirAll(queue, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(queue, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(queue, 0o700) })
	if _, err := Status(lock, nil); err == nil || !strings.Contains(err.Error(), "line of") {
		t.Fatalf("an unreadable line: %v", err)
	}
	older := filepath.Join(folder, "older.lock")
	if err := os.Chmod(queue, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(older, nil, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := Status(lock, []string{older}); err == nil {
		t.Fatal("an unreadable older lock was read as free")
	}
	if report, err := Status(filepath.Join(folder, "none.lock"), nil); err != nil || report.Held || len(report.Waiting) != 0 {
		t.Fatalf("no lock at all: %+v %v", report, err)
	}
}

func TestNoteWords(t *testing.T) {
	since := time.Date(2026, 11, 17, 9, 5, 3, 0, time.Local)
	if got := NoteWords("project p: x in /y", "about to start", 42, since); got != "project p: x in /y, about to start (the lock's 42), since 2026-11-17 09:05:03" {
		t.Fatal(got)
	}
}
