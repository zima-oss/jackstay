package delivery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAttemptsKeepIdentityAndPlan(t *testing.T) {
	type plan struct{ Before, After string }
	pending := plan{Before: Digest([]byte("old")), After: Digest([]byte("new"))}
	place := filepath.Join(t.TempDir(), "operation.json")
	initial := New(pending)
	if err := initial.Begin(place, "before"); err != nil {
		t.Fatal(err)
	}
	if err := initial.Checkpoint(place, "effect", Interrupted); err != nil {
		t.Fatal(err)
	}
	partial, _, err := Read[plan](place)
	if err != nil || partial.ID != initial.ID || partial.Attempt != 1 ||
		partial.Phase != "effect" || partial.Outcome != Interrupted || partial.Plan != pending {
		t.Fatalf("interrupted: %+v %v", partial, err)
	}
	if err := partial.Begin(place, "effect"); err != nil {
		t.Fatal(err)
	}
	if err := partial.Checkpoint(place, "verify", Complete); err != nil {
		t.Fatal(err)
	}
	done, _, err := Read[plan](place)
	if err != nil || done.ID != initial.ID || done.Attempt != 2 ||
		done.Phase != "verify" || done.Outcome != Complete || done.Plan != pending {
		t.Fatalf("completed: %+v %v", done, err)
	}
	if mode, err := os.Stat(place); err != nil || mode.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint mode: %v %v", mode, err)
	}
}

func TestUnversionedUnsupportedDuplicateAndOversizeRecords(t *testing.T) {
	place := filepath.Join(t.TempDir(), "record.json")
	cases := []struct {
		data string
		old  bool
	}{
		{`{"old_plan":true}`, true},
		{`{"schema":1,"schema":1}`, false},
		{`{"schema":2}`, false},
		{strings.Repeat(" ", Limit+1), false},
	}
	for _, each := range cases {
		if err := os.WriteFile(place, []byte(each.data), 0o600); err != nil {
			t.Fatal(err)
		}
		record, legacy, err := Read[string](place)
		if record != nil || err == nil || errors.Is(err, ErrUnversioned) != each.old {
			t.Fatal("accepted invalid or misclassified legacy record")
		}
		if each.old && string(legacy) != each.data {
			t.Fatalf("legacy bytes: %s", legacy)
		}
	}
}

func TestReadRefusesFIFOWithoutAWriter(t *testing.T) {
	place := filepath.Join(t.TempDir(), "record")
	if err := unix.Mkfifo(place, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Read[string](place); err == nil {
		t.Fatal("FIFO was accepted as a record")
	}
}
