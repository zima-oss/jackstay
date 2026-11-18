package lock

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/supervise"
)

func TestParse(t *testing.T) {
	asked, ok := parse([]string{"--project", "demo", "--wait", "1.5", "--note-words", "just gate", "--also-hold",
		"/x/old.lock=OLD", "--also-hold", "/y=z.lock=", "--", "make", "test"})
	if !ok || asked.project != "demo" || asked.wait != 1500*time.Millisecond || !asked.bounded || asked.noteWords != "just gate" ||
		len(asked.alsoHold) != 2 || asked.alsoHold[0] != (flock.Older{Path: "/x/old.lock", Variable: "OLD"}) ||
		asked.alsoHold[1] != (flock.Older{Path: "/y=z.lock"}) || strings.Join(asked.argv, " ") != "make test" {
		t.Fatalf("parsed %+v %v", asked, ok)
	}
	for _, wrong := range [][]string{
		{}, {"--"}, {"true"}, {"--no-such-option", "--", "true"}, {"--wait", "-1", "--", "true"}, {"--wait", "soon", "--", "true"},
		{"--also-hold", "/x", "--", "true"}, {"--also-hold", "=X", "--", "true"}, {"--also-hold", "/x=1X", "--", "true"},
		{"--project"},
	} {
		if _, ok := parse(wrong); ok {
			t.Errorf("%q was taken", wrong)
		}
	}
}

func TestPassedOnDropsTheLocksOwnVariables(t *testing.T) {
	kept := passedOn([]string{"PATH=/bin", OutcomeFileVariable + "=/o", NoteWordsVariable + "=w", GroupWaitVariable + "=1",
		supervise.SessionsFileVariable + "=/s", flock.HeldVariable + "=/l:1"})
	if strings.Join(kept, " ") != "PATH=/bin "+supervise.SessionsFileVariable+"=/s "+flock.HeldVariable+"=/l:1" {
		t.Fatalf("kept %q", kept)
	}
}

func TestWhoWords(t *testing.T) {
	if got := whoWords("demo", []string{"sh", "-c", "exit\n 0"}, "", "/here"); got != "project demo: sh -c exit 0 in /here" {
		t.Fatal(got)
	}
	if got := whoWords("demo", []string{"true"}, "make test\n  (through jst test)", ""); got !=
		"project demo: make test (through jst test) in a folder that is gone" {
		t.Fatal(got)
	}
}

func TestGroupWait(t *testing.T) {
	t.Setenv(GroupWaitVariable, "")
	if groupWait() != 30*time.Second {
		t.Fatal("default")
	}
	t.Setenv(GroupWaitVariable, "0.5")
	if groupWait() != 500*time.Millisecond {
		t.Fatal("chosen")
	}
	t.Setenv(GroupWaitVariable, "soon")
	if groupWait() != 30*time.Second {
		t.Fatal("unreadable")
	}
}

func TestWriteOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcome")
	t.Setenv(OutcomeFileVariable, path)
	var said bytes.Buffer
	writeOutcome(OutcomeRan, &said)
	if data, _ := os.ReadFile(path); string(data) != "ran\n" {
		t.Fatalf("%q", data)
	}
	writeOutcome("", &said)
	if data, _ := os.ReadFile(path); len(data) != 0 {
		t.Fatalf("%q", data)
	}
	t.Setenv(OutcomeFileVariable, filepath.Join(path, "nowhere"))
	writeOutcome(OutcomeRan, &said)
	if !strings.Contains(said.String(), "cannot write the outcome") {
		t.Fatal(said.String())
	}
}

func TestStatusUsage(t *testing.T) {
	var out, errs bytes.Buffer
	if statusCommand([]string{"--bad"}, nil, &out, &errs) != statusUnusable || !strings.Contains(errs.String(), "usage") {
		t.Fatal(errs.String())
	}
}

// `jst lock status` over a lock it cannot look at exits 2 and names it,
// never "the lock is free".
func TestStatusOfAnUnreadableLockIsUnusable(t *testing.T) {
	folder := t.TempDir()
	lock := filepath.Join(folder, "suite.lock")
	if err := os.WriteFile(lock, nil, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Setenv(flock.PathVariable, lock)
	t.Chdir(folder)
	var out, errs bytes.Buffer
	if code := statusCommand(nil, nil, &out, &errs); code != statusUnusable || out.Len() != 0 ||
		!strings.Contains(errs.String(), "cannot be looked at") {
		t.Fatalf("exit %d, out %q, err %q", code, out.String(), errs.String())
	}
}
