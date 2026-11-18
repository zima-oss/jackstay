// Package flock is the machine-wide lock heavy runs share with each other
// (docs/design/implementation.md sections 5.2 and 5.4): `flock` on
// `~/.jackstay/.all/heavy-suite.lock`, runs taking their turn by tickets in `heavy-suite.lock.queue/`,
// the holder's words in `heavy-suite.lock.holder`, older locks held beside it, and JACKSTAY_LOCK_HELD
// proved against the process tree. The clock and the poll schedule are injected.
package flock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/procs"
)

// Variables of the environment contract.
const (
	// HeldVariable names the lock file and the process that holds it, `<realpath>:<pid>`, for
	// the command run under it (a nested run does not take the lock again).
	HeldVariable = "JACKSTAY_LOCK_HELD"
	// PathVariable replaces the lock's file (an absolute path).
	PathVariable = "JACKSTAY_LOCK"
)

// FileName is the shared lock's file under `<jackstay home>/.all`.
const FileName = "heavy-suite.lock"

// Path is the shared lock's file: JACKSTAY_LOCK when set (it must be absolute), else
// `<jackstay home>/.all/heavy-suite.lock`.
func Path() (string, error) {
	if chosen := os.Getenv(PathVariable); chosen != "" {
		if !filepath.IsAbs(chosen) {
			return "", &Unusable{Words: "the lock's path must be absolute, so every checkout names the same file: " + chosen}
		}
		return chosen, nil
	}
	path, err := homedir.SharedPath(FileName)
	if err != nil {
		return "", &Unusable{Words: err.Error()}
	}
	return path, nil
}

// HolderPath is where the holder's words are kept beside a lock.
func HolderPath(lock string) string { return lock + ".holder" }

// A Clock tells the time and wakes after a pause; tests give one that moves only when told (W-013).
type Clock interface {
	Now() time.Time
	After(pause time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                             { return time.Now() }
func (realClock) After(pause time.Duration) <-chan time.Time { return time.After(pause) }

// RealClock is the machine's clock.
func RealClock() Clock { return realClock{} }

// A Schedule is how often a waiting run looks again and speaks.
type Schedule struct {
	// First is the first pause; each pause is 1.5 times the last, up to Most, or up to Next for
	// the run that is next in line. The pauses start over when the line moves.
	First, Most, Next time.Duration
	// SayEvery is how often a waiting run says that it waits, and for whom.
	SayEvery time.Duration
}

// DefaultSchedule is implementation.md section 5.4's: 0.05 s growing to 1 s, 0.2 s for the run that is next, words every minute.
func DefaultSchedule() Schedule {
	return Schedule{First: 50 * time.Millisecond, Most: time.Second, Next: 200 * time.Millisecond, SayEvery: time.Minute}
}

// An Older lock is a project's earlier lock held beside the shared one, and the variable its script reads.
type Older struct {
	Path     string
	Variable string
}

// Unusable says a lock cannot be used: status 2, with these words; nothing was run.
type Unusable struct{ Words string }

func (problem *Unusable) Error() string { return problem.Words }

// GaveUp says the wait was longer than it was allowed to be: status 75; nothing was run.
type GaveUp struct{ Lock string }

func (problem *GaveUp) Error() string { return "gave up waiting for " + problem.Lock }

// errBusy is a lock that cannot be had without waiting, when waiting was not asked for.
var errBusy = errors.New("busy")

// A Request is what a run asks of the lock.
type Request struct {
	// Lock is the shared lock's file; Older the earlier locks to hold beside it (already planned:
	// see Plan).
	Lock  string
	Older []string
	// Words say who this run is, for the tickets (`project P: COMMAND in DIR (process N), since …`).
	Words string
	// Wait bounds the wait when Bounded; without it a run waits as long as it takes.
	Wait    time.Duration
	Bounded bool
	// Out hears the waiting run's words.
	Out      io.Writer
	Clock    Clock
	Schedule Schedule
}

// Held are the locks a run holds; Release gives them back.
type Held struct {
	files  map[string]*os.File
	wanted []string
}

// Release gives every lock back.
func (held *Held) Release() {
	if held == nil {
		return
	}
	for path, file := range held.files {
		_ = file.Close()
		delete(held.files, path)
	}
}

// Locks are the files held, the shared lock first.
func (held *Held) Locks() []string { return append([]string(nil), held.wanted...) }

// WriteNote writes the holder's words beside every lock held: only a run holding every lock it
// needs writes them, so a run that gives up never names itself as the holder.
func (held *Held) WriteNote(words string) {
	for _, path := range held.wanted {
		_ = os.WriteFile(HolderPath(path), []byte(words+"\n"), 0o600) // #nosec G703 -- beside jackstay's own lock
	}
}

// HolderNote is the holder's words beside a lock ("" when there are none).
func HolderNote(lock string) string {
	data, err := os.ReadFile(HolderPath(lock)) // #nosec G304 -- beside jackstay's own lock
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// NoteWords are the holder note's text: who, at which stage, the lock's process, since when.
func NoteWords(who, stage string, pid int, since time.Time) string {
	return fmt.Sprintf("%s, %s (the lock's %d), since %s", who, stage, pid, since.Format("2006-01-02 15:04:05"))
}

// Acquire holds the shared lock and every older lock, waiting in one line only while it holds
// none: it waits for one lock in its turn, then tries the rest without waiting; when one is busy it
// gives everything back and waits in that lock's line. A *GaveUp when the wait is over, an
// *Unusable when a lock cannot be used, the context's cause when it was cancelled.
func Acquire(ctx context.Context, request Request) (*Held, error) {
	if request.Clock == nil {
		request.Clock = RealClock()
	}
	if request.Schedule == (Schedule{}) {
		request.Schedule = DefaultSchedule()
	}
	wanted := append([]string{request.Lock}, request.Older...)
	waiter := &waiter{request: request, began: request.Clock.Now()}
	if request.Bounded {
		waiter.giveUpAt = waiter.began.Add(request.Wait)
	}
	held := &Held{files: map[string]*os.File{}, wanted: wanted}
	waitFor := request.Lock
	waiter.span = &waitSpan{path: request.Lock, began: waiter.began}
	holderBefore := HolderNote(request.Lock)
	for {
		file, err := waiter.take(ctx, waitFor, false)
		if err != nil {
			held.Release()
			waiter.span.end(err, holderBefore)
			return nil, err
		}
		held.files[waitFor] = file
		busy := ""
		for _, path := range wanted {
			if held.files[path] != nil {
				continue
			}
			file, err := waiter.take(ctx, path, true)
			if errors.Is(err, errBusy) {
				busy = path
				break
			}
			if err != nil {
				held.Release()
				waiter.span.end(err, holderBefore)
				return nil, err
			}
			held.files[path] = file
		}
		if busy == "" {
			break
		}
		held.Release()
		waitFor = busy
	}
	waiter.span.end(nil, holderBefore)
	if waiter.waited && request.Out != nil {
		fmt.Fprintf(request.Out, "jst lock: the other run ended after %d s; starting.\n",
			int(request.Clock.Now().Sub(waiter.began).Seconds()))
	}
	return held, nil
}

// waiter is one run's wait, over every lock it takes.
type waiter struct {
	request  Request
	began    time.Time
	giveUpAt time.Time
	waited   bool
	span     *waitSpan
}

func openFailure(path string, err error) error {
	hint := ""
	if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
		hint = " Heavy runs go outside the agent sandbox."
	}
	return &Unusable{Words: fmt.Sprintf("cannot open the lock %s: %v.%s", path, err, hint)}
}

// take is a descriptor holding the lock at path, after waiting in line; with immediate it does
// not wait (errBusy when the lock is held or another run is in line for it).
func (wait *waiter) take(ctx context.Context, path string, immediate bool) (*os.File, error) {
	clock, schedule := wait.request.Clock, wait.request.Schedule
	whom := "another heavy run on this machine"
	if path != wait.request.Lock {
		whom = fmt.Sprintf("a run that holds the project's earlier lock %s (the shared lock is not held meanwhile)", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, openFailure(path, err)
	}
	place, err := newTicket(QueueFolder(path), wait.request.Words, os.Getpid(), clock)
	if err != nil {
		return nil, openFailure(path, err)
	}
	defer place.drop()
	pause, before := schedule.First, -1
	var said time.Time
	for {
		ahead := place.ahead()
		if ahead == 0 {
			file, err := lockNow(path)
			if file != nil || err != nil {
				return file, err
			}
		}
		if immediate {
			return nil, errBusy
		}
		if wait.span != nil {
			wait.span.waiting()
		}
		now := clock.Now()
		if !wait.giveUpAt.IsZero() && !now.Before(wait.giveUpAt) {
			wait.say("jst lock: gave up after waiting %d s for %s%s. Nothing was run.", int(now.Sub(wait.began).Seconds()),
				whom, lastHolder(path))
			return nil, &GaveUp{Lock: path}
		}
		if said.IsZero() || now.Sub(said) >= schedule.SayEvery {
			line := ""
			if ahead > 0 {
				line = fmt.Sprintf(", %d more in line before this one", ahead)
			}
			wait.say("jst lock: waiting for %s (%d s so far%s)%s.", whom, int(now.Sub(wait.began).Seconds()), line, lastHolder(path))
			said = now
			wait.waited = true
		}
		// Soon at first, then less often, and from the start again when the line moved; the run
		// that is next looks more often than those behind it, which wait for it and not for the lock.
		if ahead != before {
			pause, before = schedule.First, ahead
		}
		limit := schedule.Most
		if ahead == 0 {
			limit = min(schedule.Most, schedule.Next)
		}
		sleep := min(pause, limit)
		if !wait.giveUpAt.IsZero() {
			sleep = max(min(sleep, wait.giveUpAt.Sub(now)), 0)
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-clock.After(sleep):
		}
		pause = min(time.Duration(float64(pause)*1.5), limit)
	}
}

func (wait *waiter) say(format string, values ...any) {
	if wait.request.Out != nil {
		fmt.Fprintf(wait.request.Out, format+"\n", values...)
	}
}

func lastHolder(path string) string {
	if who := HolderNote(path); who != "" {
		return "; the last to take the lock: " + who
	}
	return ""
}

// lockNow locks the file at path without waiting: the file, or (nil, nil) when another holds it.
// The file locked must still be the file at the path: one removed and made again meanwhile would
// be a second lock, so that is (nil, nil) too and the caller looks again.
func lockNow(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, openFailure(path, err)
	}
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) // #nosec G115 -- a descriptor number
	if errors.Is(err, unix.EWOULDBLOCK) {
		_ = file.Close()
		return nil, nil
	}
	if err != nil {
		_ = file.Close()
		return nil, &Unusable{Words: fmt.Sprintf("cannot lock %s: %v", path, err)}
	}
	opened, openedErr := file.Stat()
	named, namedErr := os.Stat(path)
	if openedErr != nil || namedErr != nil || !os.SameFile(opened, named) {
		_ = file.Close()
		return nil, nil
	}
	return file, nil
}

// IsHeld says whether some process holds the lock at path now; a lock that cannot be looked at is
// not held (HeldAt says why).
func IsHeld(path string) bool { return isLocked(path, false) }

// HeldAt says whether some process holds the lock at path now. No file is not held; a file that
// cannot be opened or locked for the asking is an error, never a free lock.
func HeldAt(path string) (bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("the lock %s cannot be looked at: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) // #nosec G115 -- a descriptor number
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, unix.EWOULDBLOCK):
		return true, nil
	default:
		return false, fmt.Errorf("the lock %s cannot be looked at: %w", path, err)
	}
}

// RealPath is the path with every link on its way resolved, as far as it exists.
func RealPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return absolute
	}
	return filepath.Join(RealPath(parent), filepath.Base(absolute))
}

// UnderHolder says whether process self runs under the holder a claim (`<realpath>:<pid>`, as
// HeldVariable holds it) names. The claim is inherited by everything a holder starts, also by
// what outlives it, so it is proved, not believed: it names this lock, the lock is held, the named
// process lives and is an ancestor of self. Where the table cannot be read (nil), the holder note
// must name it instead ("(the lock's PID)").
func UnderHolder(lock, claim string, table map[int]procs.Process, self int) bool {
	cut := strings.LastIndex(claim, ":")
	if cut < 0 {
		return false
	}
	named, digits := claim[:cut], claim[cut+1:]
	pid, err := strconv.Atoi(digits)
	if err != nil || pid <= 0 || named != RealPath(lock) || !IsHeld(lock) {
		return false
	}
	if table == nil {
		return strings.Contains(HolderNote(lock), fmt.Sprintf("(the lock's %d)", pid))
	}
	if row, present := table[pid]; !present || row.Zombie {
		return false
	}
	for _, above := range procs.Ancestors(table, self) {
		if above.PID == pid {
			return true
		}
	}
	return false
}

// variableName is an environment variable's name.
var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsVariableName says whether name can name an environment variable.
func IsVariableName(name string) bool { return variableName.MatchString(name) }

// A Plan is which older locks a run takes and which variables it sets for its command.
type Plan struct {
	Older     []string
	Variables map[string]string
}

// PlanOlder decides the older locks: each must be absolute and not the shared lock's own file
// (an *Unusable); a file named twice is taken once; one an ancestor already holds (its variable
// names a proved holder) is skipped and its variable left as it is; every other variable is set
// to `<realpath>:<pid>`.
func PlanOlder(lock string, older []Older, getenv func(string) string, table map[int]procs.Process, self int) (Plan, error) {
	for _, each := range append([]Older{{Path: lock}}, older...) {
		if !filepath.IsAbs(each.Path) {
			return Plan{}, &Unusable{Words: "the lock's path must be absolute, so every checkout names the same file: " + each.Path}
		}
	}
	mainReal := RealPath(lock)
	plan := Plan{Variables: map[string]string{}}
	seen := map[string]bool{}
	ours := map[string]bool{}
	for _, each := range older {
		real := RealPath(each.Path)
		if real == mainReal || sameFile(lock, each.Path) {
			return Plan{}, &Unusable{Words: fmt.Sprintf("the older lock %s is the very file of this lock (%s); one run cannot hold "+
				"a file twice and would wait for itself. Take it out of --also-hold or of jackstay.json's lock.also_hold.", each.Path, lock)}
		}
		if !seen[real] {
			seen[real] = true
			plan.Older = append(plan.Older, each.Path)
		}
		switch {
		case each.Variable != "" && UnderHolder(each.Path, getenv(each.Variable), table, self):
			ours[real] = true
		case each.Variable != "":
			plan.Variables[each.Variable] = fmt.Sprintf("%s:%d", real, self)
		}
	}
	kept := plan.Older[:0]
	for _, path := range plan.Older {
		if !ours[RealPath(path)] {
			kept = append(kept, path)
		}
	}
	plan.Older = kept
	for variable, claim := range plan.Variables {
		// The same file under two variables, one of them an ancestor's: nothing is set anew.
		if ours[claim[:strings.LastIndex(claim, ":")]] {
			delete(plan.Variables, variable)
		}
	}
	return plan, nil
}

func sameFile(one, other string) bool {
	first, err := os.Stat(one)
	if err != nil {
		return false
	}
	second, err := os.Stat(other)
	return err == nil && os.SameFile(first, second)
}
