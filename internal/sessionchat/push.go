package sessionchat

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
	"github.com/zima-oss/jackstay/internal/sessionchat/claudenative"
	"github.com/zima-oss/jackstay/internal/sessionchat/pusher"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// The pusher's pace (section 8.3): a pass every few seconds, and an exit after ten minutes with
// nothing to ring.
var (
	pushEvery = 3 * time.Second
	pushIdle  = 10 * time.Minute
)

const pushUsage = `usage: jst chat push

Ring the native doorbells of idle Claude Code sessions on their messaging sockets (docs/design/chat.md
section 8.3), one process per account. The hooks and jst chat serve start it, detached, when a
doorbell is due; it exits after ten minutes with nothing to ring.

exit: 0 stopped, or another pusher runs; 2 the store or its lock is unavailable.

example: jst chat push
`

func init() {
	cli.Register(cli.Command{Name: "chat push", Summary: "ring the chat's native doorbells", Usage: pushUsage, Run: runPush})
}

// pusherFolder holds the pusher's lock and log, beside the store.
func pusherFolder() (string, error) { return homedir.SharedPath("chat") }

func runPush(args []string, _ io.Reader, _ io.Writer, diagnostics io.Writer) int {
	switch {
	case len(args) == 1 && args[0] == "--detach":
		return detachPusher()
	case len(args) != 0:
		fmt.Fprint(diagnostics, pushUsage)
		return 2
	}
	// Started through an intermediary (startPusher): wait until it has exited and this process no
	// longer descends from the session that started it, before any ring.
	if os.Getenv(detachedMark) == "1" && !orphaned(os.Getppid(), 5*time.Second) {
		fmt.Fprintln(diagnostics, "chat push: still a descendant of the process that started it; not ringing")
		return 0
	}
	folder, err := pusherFolder()
	if err != nil {
		fmt.Fprintf(diagnostics, "chat push: %v\n", err)
		return 2
	}
	held, err := flock.TryFile(filepath.Join(folder, "pusher.lock"))
	if err != nil {
		return 0 // another pusher runs
	}
	defer held.Release()
	store, err := openStore()
	if err != nil {
		fmt.Fprintf(diagnostics, "chat push: %v\n", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pass := pusher.Pass{Store: store, Ringer: claudenative.Writer{}, Alive: alive, Verified: claudenative.Verified}
	quietSince := time.Now()
	for ctx.Err() == nil && time.Since(quietSince) < pushIdle {
		reapDeadBindings(ctx, store)
		rung, err := pass.Run(ctx)
		if err != nil {
			fmt.Fprintf(diagnostics, "chat push: %v\n", err)
		}
		if rung > 0 {
			quietSince = time.Now()
		}
		select {
		case <-ctx.Done():
		case <-time.After(pushEvery):
		}
	}
	return 0
}

// startPusher reaps the bindings of processes that are gone, then starts `jst chat push` when a
// doorbell is due. It starts it through an intermediary (`jst chat push --detach`) that starts the
// pusher in a session of its own and exits, so the pusher descends from no Claude Code session
// (section 8.3); neither chat token is in its environment. A pusher already running keeps the lock,
// and the new one exits at once. Errors are ignored: the hooks still ring.
func startPusher(ctx context.Context, store *chatstore.Store) {
	reapDeadBindings(ctx, store)
	if due, err := store.DueRings(ctx); err != nil || len(due) == 0 {
		return
	}
	_ = launchPusher("--detach")
}

// detachedMark is set in the environment of a pusher its intermediary started.
const detachedMark = "JACKSTAY_CHAT_PUSHER_DETACHED"

// launchPusher starts `jst chat push` with args; a test replaces it.
var launchPusher = launch

// launch starts `jst chat push` with args, detached, without the chat tokens.
func launch(args ...string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	folder, err := pusherFolder()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return err
	}
	var environment []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CLAUDE_CODE_MESSAGING_TOKEN=") && !strings.HasPrefix(entry, "JACKSTAY_CHAT_SESSION_TOKEN=") &&
			!strings.HasPrefix(entry, detachedMark+"=") {
			environment = append(environment, entry)
		}
	}
	if len(args) == 0 {
		environment = append(environment, detachedMark+"=1")
	}
	_, _, err = supervise.Detach(append([]string{executable, "chat", "push"}, args...), folder, environment, filepath.Join(folder, "pusher.log"))
	return err
}

// detachPusher is the intermediary: it starts the pusher and exits at once, so the pusher is
// reparented away from the session.
func detachPusher() int {
	if err := launch(); err != nil {
		return 2
	}
	return 0
}

// orphaned waits up to wait for this process's parent to change from start: the intermediary has
// exited, and the process is no longer anyone's child in the session's tree.
func orphaned(start int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for os.Getppid() == start {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// reapDeadBindings removes the bindings whose process is gone (section 7.4), and ends a session no
// live binding names any more: a Claude Code process killed before its SessionEnd hook leaves both.
func reapDeadBindings(ctx context.Context, store *chatstore.Store) {
	bindings, err := store.Bindings(ctx)
	if err != nil {
		return
	}
	living := map[string]bool{}
	var dead []chatstore.Binding
	for _, binding := range bindings {
		if alive(binding) {
			living[binding.Key] = true
		} else {
			dead = append(dead, binding)
		}
	}
	for _, binding := range dead {
		if store.Unbind(ctx, binding.ProcessID, binding.ProcessStart) != nil || living[binding.Key] {
			continue
		}
		_, _ = store.Observe(ctx, binding.Key, chatstore.ObserveGone, false)
	}
}
