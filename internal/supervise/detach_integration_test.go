//go:build integration

package supervise

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/zima-oss/jackstay/internal/procs"
)

func TestDetachedCommandLeadsItsOwnGroupAndWritesItsLog(t *testing.T) {
	folder := t.TempDir()
	log, gate := filepath.Join(folder, "run.log"), filepath.Join(folder, "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	// The command waits on the fifo until the test has looked at it: no timing.
	pid, done, err := Detach([]string{"/bin/sh", "-c", "read line < \"$0\"; echo out; echo err >&2", gate}, folder, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	row, found, err := procs.Row(pid)
	release, openErr := os.OpenFile(gate, os.O_WRONLY, 0)
	if openErr != nil {
		t.Fatal(openErr)
	}
	_, _ = release.WriteString("go\n")
	_ = release.Close()
	<-done
	if err != nil || !found || row.Group != pid {
		t.Fatalf("the command does not lead its own group: %+v, %v, %v", row, found, err)
	}
	data, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(data), "out") || !strings.Contains(string(data), "err") {
		t.Fatalf("log: %q, %v", data, err)
	}
	if _, _, err := Detach(nil, "", nil, log); err == nil {
		t.Fatal("an empty command was started")
	}
}
