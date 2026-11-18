//go:build integration

package flock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	helperVariable = "JACKSTAY_FLOCK_HELPER"
	lockVariable   = "JACKSTAY_FLOCK_HELPER_LOCK"
	readyVariable  = "JACKSTAY_FLOCK_HELPER_READY"
	goVariable     = "JACKSTAY_FLOCK_HELPER_GO"
	logVariable    = "JACKSTAY_FLOCK_HELPER_LOG"
)

// outerTimeout only ends a broken test; no outcome is decided by it (W-013).
const outerTimeout = 90 * time.Second

func TestMain(m *testing.M) {
	mode := os.Getenv(helperVariable)
	if mode == "" {
		os.Exit(m.Run())
	}
	held, err := Acquire(context.Background(), Request{Lock: os.Getenv(lockVariable), Words: "helper " + os.Args[1], Out: os.Stderr})
	if err != nil {
		os.Exit(2)
	}
	switch mode {
	case "hold":
		_ = os.WriteFile(os.Getenv(readyVariable), nil, 0o600)
		deadline := time.Now().Add(outerTimeout)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(os.Getenv(goVariable)); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	case "log":
		file, _ := os.OpenFile(os.Getenv(logVariable), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		fmt.Fprintf(file, "in %s\n", os.Args[1])
		fmt.Fprintf(file, "out %s\n", os.Args[1])
		file.Close()
	}
	held.Release()
	os.Exit(0)
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(outerTimeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("never happened: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type fixture struct {
	folder, lock string
}

func newFixture(t *testing.T) fixture {
	folder := t.TempDir()
	return fixture{folder: folder, lock: filepath.Join(folder, "nested", "suite.lock")}
}

func (fix fixture) start(t *testing.T, mode, name string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], name)
	command.Env = append(os.Environ(), helperVariable+"="+mode, lockVariable+"="+fix.lock,
		readyVariable+"="+filepath.Join(fix.folder, name+".ready"), goVariable+"="+filepath.Join(fix.folder, name+".go"),
		logVariable+"="+filepath.Join(fix.folder, "log"))
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _, _ = command.Process.Wait() })
	return command
}

func (fix fixture) holder(t *testing.T, name string) *exec.Cmd {
	t.Helper()
	command := fix.start(t, "hold", name)
	waitFor(t, "the holder holds the lock", func() bool {
		_, err := os.Stat(filepath.Join(fix.folder, name+".ready"))
		return err == nil
	})
	return command
}

func (fix fixture) release(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fix.folder, name+".go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (fix fixture) inLine() int {
	entries, _ := os.ReadDir(QueueFolder(fix.lock))
	count := 0
	for _, entry := range entries {
		if ticketName.MatchString(entry.Name()) {
			count++
		}
	}
	return count
}

func TestARunWaitsForTheHolderAndGivesUpWhenTold(t *testing.T) {
	fix := newFixture(t)
	holder := fix.holder(t, "holder")
	var out bytes.Buffer
	_, err := Acquire(context.Background(), Request{Lock: fix.lock, Wait: 300 * time.Millisecond, Bounded: true, Out: &out})
	var gaveUp *GaveUp
	if !errors.As(err, &gaveUp) || !strings.Contains(out.String(), "Nothing was run") || fix.inLine() != 0 {
		t.Fatalf("err %v, said %q, line %d", err, out.String(), fix.inLine())
	}
	fix.release(t, "holder")
	out.Reset()
	held, err := Acquire(context.Background(), Request{Lock: fix.lock, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	held.Release()
	if err := holder.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestRunsTakeTheirTurnInTheOrderTheyCame(t *testing.T) {
	fix := newFixture(t)
	fix.holder(t, "head")
	var line []*exec.Cmd
	for index, name := range []string{"one", "two", "three", "four", "five"} {
		line = append(line, fix.start(t, "log", name))
		waitFor(t, name+" takes its place", func() bool { return fix.inLine() == index+1 })
	}
	fix.release(t, "head")
	for _, each := range line {
		if err := each.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(fix.folder, "log"))
	if got := strings.Fields(strings.ReplaceAll(strings.ReplaceAll(string(data), "in ", ""), "out ", "")); strings.Join(got, ",") !=
		"one,one,two,two,three,three,four,four,five,five" {
		t.Fatalf("order %q", got)
	}
	if entries, _ := os.ReadDir(QueueFolder(fix.lock)); len(entries) != 0 {
		t.Fatalf("left in line %v", entries)
	}
}

func TestADeadWaitersTicketIsPassed(t *testing.T) {
	fix := newFixture(t)
	fix.holder(t, "head")
	dead := fix.start(t, "log", "dead")
	waitFor(t, "the run to be killed is in line", func() bool { return fix.inLine() == 1 })
	behind := fix.start(t, "log", "behind")
	waitFor(t, "a run behind it is in line", func() bool { return fix.inLine() == 2 })
	_ = dead.Process.Kill()
	_, _ = dead.Process.Wait()
	fix.release(t, "head")
	if err := behind.Wait(); err != nil {
		t.Fatal(err)
	}
	if fix.inLine() != 0 {
		t.Fatalf("the dead run's ticket is left: %d", fix.inLine())
	}
}

func TestAKilledHoldersLockIsFreeAtOnce(t *testing.T) {
	fix := newFixture(t)
	holder := fix.holder(t, "killed")
	_ = holder.Process.Kill()
	_, _ = holder.Process.Wait()
	held, err := Acquire(context.Background(), Request{Lock: fix.lock, Wait: 0, Bounded: true})
	if err != nil {
		t.Fatal(err)
	}
	held.Release()
}

func TestACrowdNeverOverlaps(t *testing.T) {
	fix := newFixture(t)
	var crowd []*exec.Cmd
	for number := range 8 {
		crowd = append(crowd, fix.start(t, "log", fmt.Sprint(number)))
	}
	for _, each := range crowd {
		if err := each.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(fix.folder, "log"))
	pairs := regexp.MustCompile(`(?m)^in (\d+)\nout (\d+)$`).FindAllStringSubmatch(string(data), -1)
	if len(pairs) != 8 {
		t.Fatalf("log %q", data)
	}
	for _, pair := range pairs {
		if pair[1] != pair[2] {
			t.Fatalf("overlap: %q", data)
		}
	}
	if fix.inLine() != 0 {
		t.Fatal("a ticket is left")
	}
}
