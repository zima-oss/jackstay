package procs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNoteSessionNotesTheNearestSessionProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	table := map[int]Process{
		os.Getpid(): {Parent: 300, Start: 3, Name: "jst"},
		300:         {Parent: 200, Start: 2, Name: "zsh"},
		200:         {Parent: 1, Start: 1, Name: "claude"},
	}
	place := NoteSession("s1", table)
	if place != filepath.Join(home, ".all", "procs", "sessions", "200-1") {
		t.Fatalf("noted %q", place)
	}
	text, err := os.ReadFile(place)
	if err != nil || string(text) != `{"session":"s1","pid":200,"start":1,"name":"claude"}` {
		t.Fatalf("note %q %v", text, err)
	}
	if NoteSession("", table) != "" || NoteSession("s1", nil) != "" {
		t.Fatal("a note without a session or a table")
	}
}

func TestTableHasThisProcess(t *testing.T) {
	table := Table()
	if !tableIsRead {
		if table != nil {
			t.Fatal("a process table where none is read")
		}
		return
	}
	if _, ok := table[os.Getpid()]; !ok {
		t.Fatal("this process is not in the table")
	}
}

func TestTableRowsCarryIdentityAccountAndSession(t *testing.T) {
	if !tableIsRead {
		if _, err := ReadTable(); err == nil {
			t.Fatal("a process table where none is read")
		}
		return
	}
	table, err := ReadTableWithSessions()
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := ReadTable(); err != nil || plain[os.Getpid()].Session != 0 {
		t.Fatalf("the plain table asks for sessions (the guard's hook pays for it): %v", err)
	}
	own, ok := table[os.Getpid()]
	if !ok {
		t.Fatal("this process is not in the table")
	}
	session, err := unix.Getsid(0)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		good bool
	}{
		{"pid", own.PID == os.Getpid()},
		{"parent", own.Parent == os.Getppid()},
		{"uid", own.UID == os.Getuid()},
		{"group", own.Group == unix.Getpgrp()},
		{"session", own.Session == session},
		{"whole seconds", own.StartMicros/1_000_000 == own.Start},
		{"running", !own.Zombie},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if !check.good {
				t.Fatalf("row %+v", own)
			}
		})
	}
	row, found, err := Row(os.Getpid())
	if err != nil || !found || row.StartMicros != own.StartMicros {
		t.Fatalf("row %+v found %v err %v, table %+v", row, found, err, own)
	}
}

func TestRowOfNoProcess(t *testing.T) {
	if !tableIsRead {
		t.Run("not read", func(t *testing.T) {
			if _, _, err := Row(os.Getpid()); err == nil {
				t.Fatal("a row where none is read")
			}
		})
		return
	}
	for _, pid := range []int{0, -1, 99_999_999} {
		row, found, err := Row(pid)
		if err != nil || found {
			t.Fatalf("pid %d: row %+v found %v err %v", pid, row, found, err)
		}
	}
}

// tableIsRead says whether this system's process table is read (macOS and Linux).
var tableIsRead = runtime.GOOS == "darwin" || runtime.GOOS == "linux"
