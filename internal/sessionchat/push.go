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
	if len(args) != 0 {
		fmt.Fprint(diagnostics, pushUsage)
		return 2
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

// startPusher starts `jst chat push`, detached, when a doorbell is due: in a session of its own, so it
// descends from no Claude Code session (section 8.3), with neither chat token in its environment. A
// pusher already running keeps the lock, and the new one exits at once. Errors are ignored: the
// hooks still ring.
func startPusher(ctx context.Context, store *chatstore.Store) {
	if due, err := store.DueRings(ctx); err != nil || len(due) == 0 {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		return
	}
	folder, err := pusherFolder()
	if err != nil || os.MkdirAll(folder, 0o700) != nil {
		return
	}
	var environment []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CLAUDE_CODE_MESSAGING_TOKEN=") && !strings.HasPrefix(entry, "JACKSTAY_CHAT_SESSION_TOKEN=") {
			environment = append(environment, entry)
		}
	}
	_, _, _ = supervise.Detach([]string{executable, "chat", "push"}, folder, environment, filepath.Join(folder, "pusher.log"))
}
