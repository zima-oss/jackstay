//go:build integration

package background

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/telemetrytest"
)

// asJst makes the test binary `jst` for the processes the tests start: real `jst bg` and `jst procs`.
const asJst = "JACKSTAY_PROCS_TEST_AS_JST"

func TestMain(m *testing.M) {
	if os.Getenv(asJst) == "1" {
		os.Exit(cli.Main("jst", os.Args[1:]))
	}
	os.Exit(m.Run())
}

// outerBound ends a broken test; no test's outcome depends on it (W-013).
const outerBound = 90 * time.Second

type machineRun struct {
	t     *testing.T
	home  string
	place store
	dir   string
}

func newReal(t *testing.T) *machineRun {
	t.Helper()
	home, place := testHome(t)
	return &machineRun{t: t, home: home, place: place, dir: t.TempDir()}
}

func (r *machineRun) command(args ...string) *exec.Cmd {
	command := exec.Command(os.Args[0], args...)
	command.Dir = r.dir
	command.Env = append(os.Environ(), "JACKSTAY_HOME="+r.home, asJst+"=1")
	return command
}

// jackstay runs one jackstay command to its end: its output and status.
func (r *machineRun) jst(args ...string) (stdout, stderr string, code int) {
	r.t.Helper()
	command := r.command(args...)
	var out, errs strings.Builder
	command.Stdout, command.Stderr = &out, &errs
	if err := command.Start(); err != nil {
		r.t.Fatal(err)
	}
	code = r.wait(command)
	return out.String(), errs.String(), code
}

func (r *machineRun) wait(command *exec.Cmd) int {
	r.t.Helper()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				return 128 + int(status.Signal())
			}
			return exited.ExitCode()
		}
		if err != nil {
			r.t.Fatal(err)
		}
		return 0
	case <-time.After(outerBound):
		_ = command.Process.Kill()
		r.t.Fatalf("jst %v did not end", command.Args[1:])
		return -1
	}
}

// started starts `jst bg …` and returns once it said it started; its standard error goes on being read.
func (r *machineRun) started(args ...string) (*exec.Cmd, string) {
	r.t.Helper()
	command := r.command(append([]string{"bg"}, args...)...)
	pipe, err := command.StderrPipe()
	if err != nil {
		r.t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() {
		r.killGroups()
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	lines := bufio.NewReader(pipe)
	first, err := lines.ReadString('\n')
	if err != nil {
		r.t.Fatalf("jst bg said nothing: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, lines) }()
	return command, first
}

// killGroups ends whatever a failed test left of the groups it started.
// killGroups kills every group a subprocess of this run started: those still listed (their entries,
// so a failing test that left a supervisor running is cleaned up too) and those the journal names.
func (r *machineRun) killGroups() {
	groups := map[int]bool{}
	for _, entry := range r.place.entries() {
		groups[entry.Group] = true
	}
	for _, record := range r.journalEvents() {
		if group, ok := record["group"].(int64); ok && record["event"] == "start" {
			groups[int(group)] = true
		}
	}
	for group := range groups {
		if group > 1 {
			_ = unix.Kill(-group, unix.SIGKILL)
		}
	}
}

// journalEvents are the bg events every command of this run wrote to the journal, in order of time:
// the event's short name, its reason, the group, and the command whose span holds it.
func (r *machineRun) journalEvents() []map[string]any {
	r.t.Helper()
	names := map[string]string{telemetry.EventBgStarted: "start", telemetry.EventBgEnded: "end", telemetry.EventBgStopped: "stop",
		telemetry.EventBgHeld: "held", telemetry.EventBgCleaned: "cleaned"}
	type timed struct {
		at     time.Time
		record map[string]any
	}
	var found []timed
	for _, span := range telemetrytest.Spans(r.t, r.home) {
		for _, event := range span.Events {
			short, ok := names[event.Name]
			if !ok {
				continue
			}
			record := map[string]any{"event": short, "command": span.Name}
			if reason, ok := event.Attributes[telemetry.AttrReason].(string); ok {
				record["reason"] = reason
			}
			if group, ok := event.Attributes[telemetry.AttrBgGroup]; ok {
				record["group"] = group
			}
			for name, key := range map[string]string{telemetry.AttrBgFiles: "files", telemetry.AttrBgGone: "gone", telemetry.AttrBgLooked: "looked"} {
				if value, ok := event.Attributes[name]; ok {
					record[key] = value
				}
			}
			found = append(found, timed{event.Time, record})
		}
	}
	sort.SliceStable(found, func(left, right int) bool { return found[left].at.Before(found[right].at) })
	records := make([]map[string]any, 0, len(found))
	for _, item := range found {
		records = append(records, item.record)
	}
	return records
}

// pairs are the journal's (event, reason) pairs of this run.
func (r *machineRun) pairs() [][2]string {
	var pairs [][2]string
	for _, record := range r.journalEvents() {
		event, _ := record["event"].(string)
		reason, _ := record["reason"].(string)
		pairs = append(pairs, [2]string{event, reason})
	}
	return pairs
}

func (r *machineRun) listJSON() []map[string]any {
	r.t.Helper()
	out, errs, code := r.jst("procs", "--all", "--json")
	var listed []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &listed) != nil {
		r.t.Fatalf("jst procs: %d %q %q", code, out, errs)
	}
	return listed
}

func (r *machineRun) only() map[string]any {
	r.t.Helper()
	listed := r.listJSON()
	if len(listed) != 1 {
		r.t.Fatalf("listed %v", listed)
	}
	return listed[0]
}

// awaitFile waits for a file a child writes as its acknowledgement.
func (r *machineRun) awaitFile(path string, command *exec.Cmd) {
	r.t.Helper()
	until := time.Now().Add(outerBound)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if command != nil && command.ProcessState != nil {
			r.t.Fatal("jst bg ended before the command was ready")
		}
		if time.Now().After(until) {
			if command != nil {
				row, found, err := procs.Row(command.Process.Pid)
				r.t.Fatalf("%s never appeared: supervisor pid %d found %t row %+v error %v; process state %v",
					path, command.Process.Pid, found, row, err, command.ProcessState)
			}
			r.t.Fatalf("%s never appeared", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitEvent waits for the journal to hold an event (written when the command that saw it ends).
// awaitHeld waits for the only entry to say its group is held: the supervisor's acknowledgement.
func (r *machineRun) awaitHeld() {
	r.t.Helper()
	until := time.Now().Add(outerBound)
	for {
		if listed := r.listJSON(); len(listed) == 1 && listed[0]["status"] == "held" {
			return
		}
		if time.Now().After(until) {
			r.t.Fatalf("no held entry in %v", r.listJSON())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitRunning waits until both the child and the registry are visible to a separate procs call.
// The startup line confirms the registry write, but does not confirm that a process-table scan
// can classify the entry as running yet.
func (r *machineRun) awaitRunning(name, session string, command *exec.Cmd) map[string]any {
	r.t.Helper()
	until := time.Now().Add(outerBound)
	for {
		listed := r.listJSON()
		if len(listed) == 1 && listed[0]["name"] == name && listed[0]["state"] == "running" && listed[0]["session"] == session {
			return listed[0]
		}
		if time.Now().After(until) {
			row, found, err := procs.Row(command.Process.Pid)
			r.t.Fatalf("%s never became ready: listed %v; supervisor pid %d found %t row %+v error %v; process state %v",
				name, listed, command.Process.Pid, found, row, err, command.ProcessState)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (r *machineRun) awaitEvent(event, reason string) {
	r.t.Helper()
	until := time.Now().Add(outerBound)
	for {
		for _, pair := range r.pairs() {
			if pair == [2]string{event, reason} {
				return
			}
		}
		if time.Now().After(until) {
			r.t.Fatalf("no %s %s in %v", event, reason, r.pairs())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// groupGone says nothing runs in the group any more.
func groupGone(t *testing.T, group int) bool {
	t.Helper()
	table, err := procs.ReadTable()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range table {
		if row.Group == group && !row.Zombie {
			return false
		}
	}
	return true
}

func startedGroup(t *testing.T, r *machineRun) int {
	t.Helper()
	for _, record := range r.journalEvents() {
		if record["event"] == "start" {
			switch group := record["group"].(type) {
			case int64:
				return int(group)
			case float64:
				return int(group)
			}
		}
	}
	t.Fatal("no start with its group in the journal")
	return 0
}

func TestRealTheCommandStatusIsPassedThrough(t *testing.T) {
	r := newReal(t)
	if _, errs, code := r.jst("bg", "--", "sh", "-c", "exit 3"); code != 3 {
		t.Fatalf("exit %d %q", code, errs)
	}
	if _, errs, code := r.jst("bg", "--", "no-such-command-anywhere"); code != 127 || !strings.Contains(errs, "command not found") {
		t.Fatalf("exit %d %q", code, errs)
	}
	if _, _, code := r.jst("bg", "--for", "0", "--", "true"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if got := r.pairs(); len(got) != 2 || got[0][0] != "start" || got[1] != [2]string{"end", "ended"} {
		t.Fatalf("events %v", got)
	}
	if found, _ := os.ReadDir(r.place.folder); len(found) != 0 {
		t.Fatalf("left %v", found)
	}
}

func TestRealTheDeadlineStopsTheGroup(t *testing.T) {
	r := newReal(t)
	if _, errs, code := r.jst("bg", "--for", "0.002", "--", "sh", "-c", "sleep 300 & sleep 300"); code != 143 {
		t.Fatalf("exit %d %q", code, errs)
	}
	if got := r.pairs()[1:]; len(got) != 2 || got[0] != [2]string{"stop", "deadline"} || got[1] != [2]string{"end", "deadline"} {
		t.Fatalf("events %v", got)
	}
	if !groupGone(t, startedGroup(t, r)) {
		t.Fatal("the background sleep was left")
	}
}

func TestRealAGoneSessionStopsTheGroup(t *testing.T) {
	r := newReal(t)
	watched := exec.Command("sleep", "300")
	if err := watched.Start(); err != nil {
		t.Fatal(err)
	}
	child, first := r.started("--watch", strconv.Itoa(watched.Process.Pid), "--check-every", "0.1", "--", "sleep", "300")
	if !strings.Contains(first, "started") {
		t.Fatalf("said %q", first)
	}
	_ = watched.Process.Kill()
	_ = watched.Wait()
	if code := r.wait(child); code != 143 {
		t.Fatalf("exit %d", code)
	}
	r.awaitEvent("stop", "session-gone")
}

func TestRealAFullStandardErrorDoesNotStopTheDeadline(t *testing.T) {
	r := newReal(t)
	command := r.command("bg", "--for", "0.002", "--", "sh", "-c", "yes x >&2 & sleep 300")
	pipe, err := command.StderrPipe() // never read: the command fills it
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.killGroups() })
	code := r.wait(command)
	_ = pipe.Close()
	if code != 143 {
		t.Fatalf("exit %d", code)
	}
}

// noteThisSession writes what the guard hook writes for the Claude Code process: here, for this
// test's process, the parent of every `jst` it runs.
func noteThisSession(t *testing.T, place store, session string) {
	t.Helper()
	row, found, err := procs.Row(os.Getpid())
	if err != nil || !found {
		t.Fatalf("this process has no row: %v", err)
	}
	noteSession(t, place, os.Getpid(), row.Start, session)
}

func TestRealKeptIsListedAndStoppedByName(t *testing.T) {
	r := newReal(t)
	noteThisSession(t, r.place, "this-session")
	ready := filepath.Join(r.dir, "server-ready")
	child, first := r.started("--keep", "--name", "server", "--", "sh", "-c",
		"touch \"$1\"; exec sleep 300", "sh", ready)
	if !strings.Contains(first, "kept") {
		t.Fatalf("said %q", first)
	}
	r.awaitFile(ready, child)
	entry := r.awaitRunning("server", "this-session", child)
	if entry["name"] != "server" || entry["state"] != "running" || entry["deadline"] != nil || entry["session"] != "this-session" {
		t.Fatalf("listed %v", entry)
	}
	if out, errs, code := r.jst("procs", "stop", "server"); code != 0 || !strings.Contains(out, "stopped") {
		t.Fatalf("stop: %d %q %q", code, out, errs)
	}
	if code := r.wait(child); code != 143 {
		t.Fatalf("jst bg exit %d", code)
	}
	if listed := r.listJSON(); len(listed) != 0 {
		t.Fatalf("listed %v", listed)
	}
	if _, _, code := r.jst("procs", "stop", "server"); code != 1 {
		t.Fatalf("a second stop: %d", code)
	}
	if got := r.pairs(); len(got) != 3 || got[1] != [2]string{"stop", "asked"} || got[2] != [2]string{"end", "asked"} {
		t.Fatalf("events %v", got)
	}
}

func TestRealStopByIdIsAnsweredByTheSupervisor(t *testing.T) {
	r := newReal(t)
	child, _ := r.started("--keep", "--", "sleep", "300")
	id := r.only()["id"].(string)
	out, errs, code := r.jst("procs", "stop", id)
	if code != 0 || out != "jst procs: "+id+" stopped\n" {
		t.Fatalf("stop: %d %q %q", code, out, errs)
	}
	if code := r.wait(child); code != 143 {
		t.Fatalf("jst bg exit %d", code)
	}
	for _, record := range r.journalEvents() {
		if record["event"] == "stop" {
			if signalledHere := record["command"] == "jst procs stop"; signalledHere {
				t.Fatalf("the group was signalled by jst procs, not its supervisor: %v", record)
			}
		}
	}
	if r.place.stopAsked(id) || r.place.listed(id) {
		t.Fatal("the request or the entry stays")
	}
}

func TestRealAnOrphanedGroupIsShownAndStopped(t *testing.T) {
	r := newReal(t)
	child, _ := r.started("--keep", "--name", "left", "--", "sleep", "300")
	_ = child.Process.Kill() // the supervisor dies outright; its group goes on
	r.wait(child)
	entry := r.only()
	if entry["state"] != "orphaned" {
		t.Fatalf("listed %v", entry)
	}
	group := int(entry["group"].(float64))
	if out, errs, code := r.jst("procs", "stop", entry["id"].(string)); code != 0 {
		t.Fatalf("stop: %d %q %q", code, out, errs)
	}
	if listed := r.listJSON(); len(listed) != 0 {
		t.Fatalf("listed %v", listed)
	}
	if !groupGone(t, group) {
		t.Fatal("the orphan's group runs on")
	}
}

func TestRealAHeldWriterIsNotStoppedUntilAsked(t *testing.T) {
	r := newReal(t)
	// Outside every temporary folder, which lsof's look leaves out (the checkout itself may be in
	// one: the gate runs from a temporary worktree); ~/.cache is writable in the agent sandbox.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".cache", fmt.Sprintf("jackstay-procs-test-%d.bin", os.Getpid()))
	ready := filepath.Join(r.dir, "ready")
	t.Cleanup(func() { _ = os.Remove(target) })
	watched := exec.Command("sleep", "300")
	if err := watched.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watched.Process.Kill(); _ = watched.Wait() })
	script := fmt.Sprintf("exec 3>'%s'; touch '%s'; exec sleep 300", target, ready)
	child, _ := r.started("--watch", strconv.Itoa(watched.Process.Pid), "--check-every", "0.1", "--name", "writer", "--",
		"sh", "-c", script)
	r.awaitFile(ready, child)
	_ = watched.Process.Kill()
	r.awaitHeld()
	entry := r.only()
	held, _ := entry["held"].([]any)
	if entry["status"] != "held" || len(held) != 1 || !samePath(held[0].(string), target) {
		t.Fatalf("listed %v", entry)
	}
	if out, errs, code := r.jst("procs", "stop", "--all", "writer"); code != 0 {
		t.Fatalf("stop: %d %q %q", code, out, errs)
	}
	if code := r.wait(child); code != 143 {
		t.Fatalf("jst bg exit %d", code)
	}
	// The journal keeps the files that held the group (the log's field before W-056).
	heldInJournal := false
	for _, record := range r.journalEvents() {
		if files, _ := record["files"].([]any); record["event"] == "held" && len(files) == 1 && samePath(files[0].(string), target) {
			heldInJournal = true
		}
	}
	if !heldInJournal {
		t.Fatalf("no held event naming %s: %v", target, r.journalEvents())
	}
}

func samePath(left, right string) bool {
	leftReal, leftErr := filepath.EvalSymlinks(left)
	rightReal, rightErr := filepath.EvalSymlinks(right)
	return leftErr == nil && rightErr == nil && leftReal == rightReal
}

func TestRealACommandThatIgnoresTermIsKilledAfterAStopRequest(t *testing.T) {
	r := newReal(t)
	ready := filepath.Join(r.dir, "trapped")
	child, _ := r.started("--for", "60", "--", "sh", "-c", fmt.Sprintf("trap '' TERM; touch '%s'; sleep 300", ready))
	r.awaitFile(ready, child) // the trap is set: TERM alone cannot end it now
	if err := child.Process.Signal(unix.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := r.wait(child); code != 143 {
		t.Fatalf("exit %d", code)
	}
	if got := r.pairs(); len(got) != 2 || got[0][0] != "start" || got[1] != [2]string{"end", "signal"} {
		t.Fatalf("events %v", got)
	}
	if !groupGone(t, startedGroup(t, r)) {
		t.Fatal("the group runs on")
	}
}

func TestRealAnEntryWrittenByAnotherProcessIsListed(t *testing.T) {
	r := newReal(t)
	supervisor := exec.Command("sleep", "300")
	group := exec.Command("sleep", "300")
	group.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	for _, process := range []*exec.Cmd{supervisor, group} {
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	}
	start := func(pid int) string {
		row, found, err := procs.Row(pid)
		if err != nil || !found {
			t.Fatalf("no row of %d: %v", pid, err)
		}
		return strconv.FormatInt(row.Start, 10)
	}
	data, err := os.ReadFile(filepath.Join("testdata", "1763300000-70001.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer("70001", strconv.Itoa(supervisor.Process.Pid), "1763299990", start(supervisor.Process.Pid),
		"70002", strconv.Itoa(group.Process.Pid), "1763300001", start(group.Process.Pid)).Replace(string(data))
	id := "1763300000-" + strconv.Itoa(supervisor.Process.Pid)
	if err := os.MkdirAll(r.place.folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.place.entryPath(id), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := r.only()
	if entry["id"] != id || entry["name"] != "watcher" || entry["state"] != "running" || entry["session"] != "s-1" {
		t.Fatalf("listed %v", entry)
	}
	out, _, _ := r.jst("procs", "--all")
	if !strings.Contains(out, id+"  watcher  running") || !strings.Contains(out, "session s-1  /work/project  sh -c tail -f log") {
		t.Fatalf("listed %q", out)
	}
	_ = supervisor.Process.Kill()
	_ = supervisor.Wait()
	_ = group.Process.Kill()
	_ = group.Wait()
	if listed := r.listJSON(); len(listed) != 0 {
		t.Fatalf("listed %v", listed)
	}
	if got := r.pairs(); len(got) != 1 || got[0][0] != "cleaned" {
		t.Fatalf("events %v", got)
	}
}
