package sessionchat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/gitcli"
)

const bridgeUsage = `usage: jst chat codex-bridge --socket PATH --thread ID --name NAME

Attach to an owner-managed Codex app-server Unix socket and one thread. Start the
MCP server and the bridge with the same JACKSTAY_CHAT_SESSION_TOKEN. Enable
chat.codex_bridge in jackstay.json. The bridge delivers to a running turn and
leaves the message pending until the recipient calls chat.ack.

exit: 0 stopped; 2 invalid setup or another bridge owns the session.

example: jst chat codex-bridge --socket "$APP_SOCKET" --thread "$APP_THREAD" --name reviewer
`

func init() {
	cli.Register(cli.Command{
		Name: "chat codex-bridge", Summary: "deliver pending chat to a managed Codex turn",
		Usage: bridgeUsage, PrivateArgs: true, Run: runCodexBridge,
	})
}

// localPolicy is the chat policy of jackstay.json's `chat` section; a test lowers its limits.
var localPolicy = config.ChatSettings

func loadLocalChat(ctx context.Context) (Store, config.ChatPolicy, error) {
	here, err := os.Getwd()
	if err != nil {
		return Store{}, config.ChatPolicy{}, errors.New("cannot read working directory")
	}
	_, loaded, err := config.Load(here, []string{"chat"})
	if err != nil {
		return Store{}, config.ChatPolicy{}, err
	}
	policy := localPolicy(loaded.Section("chat"))
	if !policy.Enabled {
		return Store{}, config.ChatPolicy{}, errors.New("chat is not enabled in jackstay.json")
	}
	common, err := (gitcli.Git{Dir: here}).CommonDir(ctx)
	if err != nil {
		return Store{}, config.ChatPolicy{}, errors.New("chat requires a Git repository")
	}
	store, err := StoreFor(common, policy)
	if err != nil {
		return Store{}, config.ChatPolicy{}, errors.New("chat state is unavailable")
	}
	return store, policy, nil
}

func runCodexBridge(args []string, _ io.Reader, _ io.Writer, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("chat codex-bridge", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", "", "app-server Unix socket")
	thread := flags.String("thread", "", "managed Codex thread")
	name := flags.String("name", "", "recipient label")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || !filepath.IsAbs(*socket) ||
		strings.TrimSpace(*thread) == "" || len(*thread) > 128 || strings.IndexFunc(*thread, unicode.IsControl) >= 0 ||
		strings.TrimSpace(*name) == "" {
		fmt.Fprint(diagnostics, bridgeUsage)
		return 2
	}
	token := os.Getenv("JACKSTAY_CHAT_SESSION_TOKEN")
	if token == "" {
		fmt.Fprintln(diagnostics, "chat: the bridge and MCP server need the same session token")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	setup, cancel := context.WithTimeout(ctx, 5*time.Second)
	store, policy, err := loadLocalChat(setup)
	cancel()
	if err != nil {
		fmt.Fprintf(diagnostics, "chat: %v\n", err)
		return 2
	}
	if !policy.CodexBridgeEnabled {
		fmt.Fprintln(diagnostics, "chat: enable chat.codex_bridge in jackstay.json")
		return 2
	}
	session, err := store.Register(*name, token)
	if err != nil {
		fmt.Fprintf(diagnostics, "chat: %v\n", err)
		return 2
	}
	lockID := sha256.Sum256([]byte(session.ID))
	held, err := flock.TryFile(filepath.Join(filepath.Dir(store.path), "codex-"+hex.EncodeToString(lockID[:])+".lock"))
	if err != nil {
		fmt.Fprintln(diagnostics, "chat: cannot acquire the session bridge lock")
		return 2
	}
	defer held.Release()

	bridge := CodexBridge{Store: store, SessionID: session.ID, SocketPath: *socket, ThreadID: *thread}
	runBridge(ctx, bridge, time.Duration(policy.CodexPollIntervalMS)*time.Millisecond,
		time.Duration(policy.CodexAckTimeoutSeconds)*time.Second, diagnostics)
	return 0
}

func runBridge(ctx context.Context, bridge CodexBridge, interval, ackTimeout time.Duration, diagnostics io.Writer) {
	delay := interval
	disconnected := false
	notificationUnavailable := false
	lastWarning := ""
	nextWarningCheck := time.Time{}
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 15*time.Second)
		accepted, err := bridge.Deliver(attempt)
		cancel()
		if accepted > 0 {
			fmt.Fprintf(diagnostics, "chat: Codex accepted %d message(s); awaiting recipient acknowledgment\n", accepted)
		}
		if ctx.Err() != nil {
			return
		}
		var notificationFailure reviewNotificationSyncError
		switch {
		case errors.As(err, &notificationFailure):
			if !notificationUnavailable {
				fmt.Fprintf(diagnostics, "chat: %v; pending review events will be retried\n", err)
			}
			notificationUnavailable = true
			disconnected = false
			delay = interval
		case err != nil:
			notificationUnavailable = false
			if !disconnected {
				fmt.Fprintln(diagnostics, "chat: Codex delivery unavailable; pending messages will be retried")
			}
			disconnected = true
			delay = min(2*delay, 30*time.Second)
		default:
			notificationUnavailable = false
			disconnected = false
			delay = interval
		}
		if now := time.Now(); !now.Before(nextWarningCheck) {
			nextWarningCheck = now.Add(min(ackTimeout/2, time.Minute))
			if messageID := overdueCodexAcknowledgment(bridge, now, ackTimeout); messageID != "" && messageID != lastWarning {
				lastWarning = messageID
				fmt.Fprintln(diagnostics, "chat: a message accepted by Codex is still pending recipient acknowledgment")
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func overdueCodexAcknowledgment(bridge CodexBridge, now time.Time, timeout time.Duration) string {
	messages, err := bridge.Store.pending(bridge.SessionID, 0)
	if err != nil {
		return ""
	}
	for _, message := range messages {
		acceptance := message.CodexAccepted
		if acceptance == nil || acceptance.ThreadID != bridge.ThreadID {
			continue
		}
		when, err := time.Parse(time.RFC3339Nano, acceptance.At)
		if err == nil && now.Sub(when) >= timeout {
			return message.ID
		}
	}
	return ""
}
