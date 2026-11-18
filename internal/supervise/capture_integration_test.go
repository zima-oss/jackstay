//go:build integration

package supervise

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestInputAndRawOutputAreGivenUnchanged(t *testing.T) {
	var stdout, stderr bytes.Buffer
	input := []byte("one\r\n\x1b[1mtwo")
	outcome, err := Run(nil, Spec{Mode: Group, Input: input, StdoutTo: &stdout, StderrTo: &stderr,
		Argv: []string{"sh", "-c", `cat; printf 'err\r\n' >&2; exit 3`}})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status() != 3 || outcome.Unread != "" || stdout.String() != string(input) || stderr.String() != "err\r\n" {
		t.Fatalf("status %d, unread %q, stdout %q, stderr %q", outcome.Status(), outcome.Unread, stdout.String(), stderr.String())
	}
}

func TestInputTheCommandDoesNotReadDoesNotHoldTheRun(t *testing.T) {
	outcome, err := Run(nil, Spec{Mode: Group, Input: bytes.Repeat([]byte("x"), 4<<20), Argv: []string{"sh", "-c", "exit 0"}})
	if err != nil || outcome.Status() != 0 {
		t.Fatalf("outcome %+v, error %v", outcome, err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("full") }

func TestARawWritersErrorIsAProblemAndTheStreamIsStillRead(t *testing.T) {
	outcome, err := Run(nil, Spec{Mode: Group, StdoutTo: failingWriter{},
		Argv: []string{"sh", "-c", `head -c 1048576 /dev/zero; exit 0`}})
	if err != nil || outcome.Status() != 0 || len(outcome.Problems) != 1 || outcome.Unread != "" {
		t.Fatalf("outcome %+v, error %v", outcome, err)
	}
}

func TestTheContextsEndStopsTheWholeGroup(t *testing.T) {
	outer, cancelOuter := context.WithTimeout(context.Background(), outerTimeout)
	defer cancelOuter()
	runContext, stop := context.WithCancel(outer)
	defer stop()
	left := filepath.Join(t.TempDir(), "left")
	type ended struct {
		outcome Outcome
		err     error
	}
	result := make(chan ended, 1)
	go func() {
		outcome, err := Run(nil, Spec{Mode: Group, Context: runContext, Grace: 5 * time.Second,
			Argv: []string{"sh", "-c", `sleep 600 & echo $! > "$0.tmp"; mv "$0.tmp" "$0"; wait`, left}})
		result <- ended{outcome, err}
	}()
	sleeper := readPID(t, left)
	killLater(t, sleeper)
	stop()
	var got ended
	select {
	case got = <-result:
	case <-outer.Done():
		t.Fatal("the run did not end")
	}
	if got.err != nil || got.outcome.Signal != unix.SIGTERM || got.outcome.Killed || !gone(sleeper) {
		t.Fatalf("outcome %+v, error %v, sleep gone %v", got.outcome, got.err, gone(sleeper))
	}
}

func TestAContextEndedBeforeTheStartStartsNothing(t *testing.T) {
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	marker := filepath.Join(t.TempDir(), "ran")
	_, err := Run(nil, Spec{Mode: Group, Context: ended, Argv: []string{"touch", marker}})
	var startErr *StartError
	if !errors.As(err, &startErr) || !errors.Is(err, context.Canceled) || !errors.Is(statErr(marker), os.ErrNotExist) {
		t.Fatalf("error %v", err)
	}
}

func statErr(path string) error {
	_, err := os.Stat(path)
	return err
}
