//go:build integration

package sessionchat

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
)

func TestLinkedWorktreesShareChatNamespace(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	repository := filepath.Join(t.TempDir(), "main")
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(folder string, args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", folder}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	run(repository, "init")
	run(repository, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	run(repository, "worktree", "add", "--detach", linked)
	contextWithDeadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	primary, err := (gitcli.Git{Dir: repository}).CommonDir(contextWithDeadline)
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := (gitcli.Git{Dir: linked}).CommonDir(contextWithDeadline)
	if err != nil {
		t.Fatal(err)
	}
	policy := config.ChatPolicy{Enabled: true, MaxMessageBytes: 4096, MaxPending: 10, MaxSessions: 5, RetentionHours: 72}
	senderStore, err := StoreFor(primary, policy)
	if err != nil {
		t.Fatal(err)
	}
	recipientStore, err := StoreFor(secondary, policy)
	if err != nil {
		t.Fatal(err)
	}
	if senderStore.path != recipientStore.path {
		t.Fatalf("worktrees use separate stores: %s and %s", senderStore.path, recipientStore.path)
	}
	sender, err := senderStore.Register("sender", "")
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := recipientStore.Register("recipient", "")
	if err != nil {
		t.Fatal(err)
	}
	sent, _, err := senderStore.Send(sender.ID, recipient.ID, "message", "hello", "one", "")
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := recipientStore.Inbox(recipient.ID, 10)
	if err != nil || len(inbox) != 1 || inbox[0].ID != sent.ID {
		t.Fatalf("linked delivery: %+v %v", inbox, err)
	}
}
