package sessionchat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/procs"
	"github.com/zima-oss/jackstay/internal/sessionchat/chatstore"
	"github.com/zima-oss/jackstay/internal/sessionchat/claudenative"
)

// The account-wide chat store (docs/design/chat.md section 7.1). A test replaces it.
var openStore = chatstore.Default

// local is the chat as seen from one folder: the store, the repository's channel, and where in the
// repository the folder is.
type local struct {
	store    *chatstore.Store
	channel  chatstore.Channel
	worktree string
	branch   string
}

// errChatOff is the answer when the folder's project does not enable chat.
var errChatOff = errors.New("chat is not enabled in jackstay.json")

// localChat is the chat of the repository around folder, when its project enables it.
func localChat(ctx context.Context, folder string) (local, error) {
	root, loaded, err := config.Load(folder, []string{"chat"})
	if err != nil {
		return local{}, err
	}
	if !config.ChatSettings(loaded.Section("chat")).Enabled {
		return local{}, errChatOff
	}
	git := gitcli.Git{Dir: folder}
	common, err := git.CommonDir(ctx)
	if err != nil {
		return local{}, errors.New("chat requires a Git repository")
	}
	project, _ := loaded["project"].(string)
	channel, err := chatstore.RepositoryChannel(common, strings.TrimSpace(project))
	if err != nil {
		return local{}, err
	}
	store, err := openStore()
	if err != nil {
		return local{}, err
	}
	found := local{store: store, channel: channel, worktree: root}
	if found.worktree == "" {
		found.worktree = folder
	}
	if branch, err := git.Line(ctx, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		found.branch = strings.TrimSpace(branch)
	}
	return found, nil
}

// tokenKey is the key a session token gives (section 3.1): "" without a token.
func tokenKey() (string, error) {
	token := os.Getenv("JACKSTAY_CHAT_SESSION_TOKEN")
	if token == "" {
		return "", nil
	}
	if len(token) < 32 || len(token) > 256 {
		return "", errors.New("JACKSTAY_CHAT_SESSION_TOKEN must be 32 to 256 characters")
	}
	digest := sha256.Sum256([]byte(token))
	return "tk:" + hex.EncodeToString(digest[:]), nil
}

// ephemeralKey is a key for a session the chat cannot name (section 3.1).
func ephemeralKey() string {
	var bits [16]byte
	if _, err := rand.Read(bits[:]); err != nil {
		return "ep:" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "ep:" + hex.EncodeToString(bits[:])
}

// claudeProcess is the Claude Code process this one runs under: the one CLAUDE_PID names when it is
// an ancestor, else the nearest ancestor that may be a session's own (section 3.1).
func claudeProcess() (procs.Process, bool) {
	table, err := procs.ReadTable()
	if err != nil {
		return procs.Process{}, false
	}
	if named, err := strconv.Atoi(os.Getenv("CLAUDE_PID")); err == nil {
		for _, above := range procs.Ancestors(table, os.Getpid()) {
			if above.PID == named {
				return table[named], true
			}
		}
	}
	own := procs.OwnSession(table, os.Getpid())
	if !own.Found {
		return procs.Process{}, false
	}
	process, ok := table[own.PID]
	return process, ok
}

// processStart is how a binding records a process's start: its start in microseconds.
func processStart(process procs.Process) string { return strconv.FormatInt(process.StartMicros, 10) }

// alive reports whether a binding's process still runs: the same id with the same start.
func alive(binding chatstore.Binding) bool {
	process, found, err := procs.Row(binding.ProcessID)
	return err == nil && found && !process.Zombie && processStart(process) == binding.ProcessStart
}

var versionInPath = regexp.MustCompile(`(?:^|/)(\d+\.\d+\.\d+)(?:/|$)`)

// claudeVersion is Claude Code's version as its executable's path names it, "" when it does not.
// Claude Code exports no version to its hooks; an unknown version is outside the verified range, so
// the session takes the hooks route (section 8.3).
func claudeVersion() string {
	match := versionInPath.FindStringSubmatch(filepath.ToSlash(os.Getenv("CLAUDE_CODE_EXECPATH")))
	if match == nil {
		return ""
	}
	return match[1]
}

// deliveryOf is a Claude Code session's route: native with a socket and a verified version, else
// hooks (section 8.3).
func deliveryOf(socket, version string) string {
	if socket != "" && claudenative.Verified(version) {
		return chatstore.DeliveryNative
	}
	return chatstore.DeliveryHooks
}

// boundKey is the key of the Claude Code session this process runs under, from its binding; it
// waits up to wait for SessionStart to write one (section 3.1).
func boundKey(ctx context.Context, store *chatstore.Store, wait time.Duration) (string, bool) {
	process, found := claudeProcess()
	if !found {
		return "", false
	}
	deadline := time.Now().Add(wait)
	for {
		bindings, err := store.Bindings(ctx)
		if err == nil {
			for _, binding := range bindings {
				if binding.ProcessID == process.PID && binding.ProcessStart == processStart(process) {
					return binding.Key, true
				}
			}
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return "", false
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(100 * time.Millisecond):
		}
	}
}
