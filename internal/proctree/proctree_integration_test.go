//go:build integration

package proctree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/procs"
)

// brokenTest ends a test that hangs; no outcome depends on it.
const brokenTest = 2 * time.Minute

// start runs a shell script that writes "ready" into its ready file once it is set up.
func start(t *testing.T, script string, attributes *syscall.SysProcAttr, folder string, extra ...string) (*exec.Cmd, string) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command("sh", "-c", script)
	command.Env = append(append(os.Environ(), "READY="+ready), extra...)
	command.SysProcAttr = attributes
	command.Dir = folder
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { _ = command.Wait(); close(reaped) }()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		<-reaped
	})
	awaitFile(t, ready)
	return command, ready
}

func awaitFile(t *testing.T, path string) {
	t.Helper()
	give := time.Now().Add(brokenTest)
	for {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return
		}
		if time.Now().After(give) {
			t.Fatalf("%s never came", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func remember(t *testing.T, pid int) *Tree {
	t.Helper()
	row, found, err := procs.Row(pid)
	if err != nil || !found {
		t.Fatalf("row %v %v", found, err)
	}
	tree := New()
	tree.Remember(pid, row.StartMicros)
	return tree
}

func TestRealStopKillsASessionThatIgnoresTerm(t *testing.T) {
	command, _ := start(t, `trap "" TERM; sleep 600 & echo ready > "$READY"; while :; do sleep 0.05; done`,
		&syscall.SysProcAttr{Setsid: true}, "")
	tree := remember(t, command.Process.Pid)
	tree.FollowSession(command.Process.Pid)
	result, err := Real().Stop(tree, Targets{Members: true}, 200*time.Millisecond)
	if err != nil || len(result.Survivors) != 0 || len(result.Refused) != 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
	if len(tree.Members()) < 2 {
		t.Fatalf("the background sleep was not found: %v", tree.Members())
	}
}

func TestRealStopEndsAGroupByTerm(t *testing.T) {
	// The grace is the broken-test bound: the group must end by TERM, acknowledged in its file.
	got := filepath.Join(t.TempDir(), "got")
	command, _ := start(t, `trap 'echo TERM > "$GOT"; exit 0' TERM; echo ready > "$READY"; while :; do sleep 0.05; done`,
		&syscall.SysProcAttr{Setpgid: true}, "", "GOT="+got)
	tree := remember(t, command.Process.Pid)
	result, err := Real().Stop(tree, Targets{Groups: []int{command.Process.Pid}}, brokenTest)
	if err != nil || len(result.Survivors) != 0 || len(result.Refused) != 0 {
		t.Fatalf("result %+v err %v", result, err)
	}
	if data, err := os.ReadFile(got); err != nil || string(data) != "TERM\n" {
		t.Fatalf("the group did not say it got TERM: %q %v", data, err)
	}
}

func TestRealFoldersInsideFindsAChild(t *testing.T) {
	folder := t.TempDir()
	command, _ := start(t, `echo ready > "$READY"; while :; do sleep 0.05; done`, nil, folder)
	view, err := FoldersInside(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := view.Inside[command.Process.Pid]; !ok {
		t.Fatalf("the child is not inside %s: %v", folder, view.Inside)
	}
}
