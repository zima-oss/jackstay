package sessionchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/zima-oss/jackstay/internal/chatevent"
	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

// reviewChat is a repository with chat enabled, its store, and the working folder moved into it,
// with review.submit launching nothing: launches are recorded and the process is "alive" while
// running says so.
type reviewChat struct {
	repository *testrepo.Repo
	store      Store
	launched   [][]string
	running    bool
}

func newReviewChat(t *testing.T) *reviewChat {
	t.Helper()
	chat := &reviewChat{repository: testrepo.New(t, map[string]string{
		"jackstay.json": `{"jackstay_schema":3,"project":"review-chat-test","chat":{"enabled":true,"claude_code":{"enabled":true}}}`,
		"file.txt":      "first\n",
	})}
	chat.repository.Commit("change", map[string]string{"file.txt": "second\n"})
	store, err := StoreFor(chat.repository.Root+"/.git", config.ChatPolicy{
		Enabled: true, MaxMessageBytes: 4096, MaxPending: 16, MaxPendingPerSession: 8, MaxSessions: 8, RetentionHours: 72,
	})
	if err != nil {
		t.Fatal(err)
	}
	chat.store = store
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(chat.repository.Root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	original := reviewBackground
	reviewBackground = func(folder string) (review.Background, error) {
		background, err := review.BackgroundAt(folder, "/usr/local/bin/jst")
		background.Started = 0
		background.Launch = func(argv []string, _, _ string) (int, int64, <-chan struct{}, error) {
			chat.launched = append(chat.launched, argv)
			chat.running = true
			return 4242, 7, nil, nil
		}
		background.Alive = func(int, int64) (bool, bool) { return chat.running, true }
		return background, err
	}
	t.Cleanup(func() { reviewBackground = original })
	return chat
}

func (chat *reviewChat) call(t *testing.T, session Session, name string, arguments any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		t.Fatal(err)
	}
	response := (&Server{store: chat.store, session: session}).call(encoded).(map[string]any)
	if response["isError"] == true {
		t.Fatalf("%s failed: %+v", name, response)
	}
	return response["structuredContent"].(map[string]any)
}

func (chat *reviewChat) register(t *testing.T, name, token string) Session {
	t.Helper()
	session, err := chat.store.Register(name, token)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func (chat *reviewChat) background(t *testing.T) review.Background {
	t.Helper()
	background, err := reviewBackground(chat.repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	return background
}

func TestReviewSubmitStartsOneBackgroundRunAndEverySubscriberHearsTheResult(t *testing.T) {
	chat := newReviewChat(t)
	submitter := chat.register(t, "submitter", strings.Repeat("s", 32))
	other := chat.register(t, "other", strings.Repeat("o", 32))
	request := map[string]any{"base": "HEAD^", "head": "HEAD", "task": "review-task", "reviewers": []string{"codex"}}

	first := chat.call(t, submitter, "review.submit", request)
	if first["joined"] != false || len(chat.launched) != 1 {
		t.Fatalf("the first submission did not start a run: %+v, %v", first, chat.launched)
	}
	argv := chat.launched[0]
	if argv[1] != "bg" || !slices.Contains(argv, "review") || !slices.Contains(argv, "--request") || argv[len(argv)-2] != "--" ||
		!strings.Contains(argv[len(argv)-1], "..") {
		t.Fatalf("the run is not `jst bg -- jst review … -- BASE..HEAD`: %v", argv)
	}
	identity := first["request"].(map[string]any)["id"].(string)
	if second := chat.call(t, other, "review.submit", request); second["joined"] != true ||
		second["request"].(map[string]any)["id"] != identity || len(chat.launched) != 1 {
		t.Fatalf("an equal submission did not join the running review: %+v", second)
	}
	if _, found := chat.call(t, other, "review.submit", request)["request"].(map[string]any)["subscribers"]; found {
		t.Fatal("a submission's answer shows the other sessions subscribed")
	}
	different := map[string]any{"base": "HEAD^", "head": "HEAD^", "task": "review-task", "reviewers": []string{"codex"}}
	encoded, _ := json.Marshal(map[string]any{"name": "review.submit", "arguments": different})
	if response := (&Server{store: chat.store, session: other}).call(encoded).(map[string]any); response["isError"] != true ||
		!strings.Contains(response["content"].([]map[string]string)[0]["text"], "busy") {
		t.Fatalf("a different review of a busy task was not refused: %+v", response)
	}
	status := chat.call(t, submitter, "review.status", map[string]any{"task": "review-task"})
	if status["running"].(map[string]any)["id"] != identity {
		t.Fatalf("status does not show the running review: %+v", status)
	}

	background := chat.background(t)
	if err := background.Store.Finish(context.Background(), "review-task", identity,
		review.Result{Status: review.ExitBlocks, Next: "fix the open blocking findings"}, publisher{}); err != nil {
		t.Fatal(err)
	}
	chat.running = false
	for _, session := range []Session{submitter, other} {
		inbox := chat.call(t, session, "chat.inbox", map[string]any{})["messages"].([]Message)
		if len(inbox) != 1 || inbox[0].From != systemSender || !strings.Contains(inbox[0].Body, "blocks") ||
			!strings.Contains(inbox[0].Body, chatevent.KindReviewFinished) {
			t.Fatalf("%s did not hear the result: %+v", session.Name, inbox)
		}
		chat.call(t, session, "chat.ack", map[string]any{"message_id": inbox[0].ID})
	}
	if status := chat.call(t, submitter, "review.status", map[string]any{"task": "review-task"}); status["running"] != nil {
		t.Fatalf("a finished review still shows as running: %+v", status)
	}
	if _, found, err := background.Store.ReadRequest("review-task"); err != nil || found {
		t.Fatalf("the finished request was kept: %v, %v", found, err)
	}
}

type refusing struct{}

func (refusing) Publish(context.Context, chatevent.Event) error {
	return errors.New("chat store is full")
}

func TestAResultTheChatCouldNotTakeIsOwedAndPulledByTheRecipient(t *testing.T) {
	chat := newReviewChat(t)
	submitter := chat.register(t, "submitter", strings.Repeat("s", 32))
	answer := chat.call(t, submitter, "review.submit", map[string]any{"base": "HEAD^", "head": "HEAD", "task": "owed", "reviewers": []string{"codex"}})
	identity := answer["request"].(map[string]any)["id"].(string)
	background := chat.background(t)
	if err := background.Store.Finish(context.Background(), "owed", identity, review.Result{Status: review.ExitClear, Next: "nothing blocks"},
		refusing{}); err == nil {
		t.Fatal("a refused publication was not said")
	}
	owed, err := background.Store.Owed(submitter.ID)
	if err != nil || len(owed) != 1 {
		t.Fatalf("the result is not owed: %+v, %v", owed, err)
	}

	t.Setenv("JACKSTAY_CHAT_SESSION_TOKEN", strings.Repeat("s", 32))
	var output, diagnostics bytes.Buffer
	if status := runClaudeHook([]string{"--name", "submitter", "--event", "UserPromptSubmit"}, nil, &output, &diagnostics); status != 0 ||
		!strings.Contains(output.String(), "1 pending") {
		t.Fatalf("the hook did not pull the owed result: exit %d, %s, %s", status, output.String(), diagnostics.String())
	}
	if left, err := background.Store.Owed(submitter.ID); err != nil || len(left) != 0 {
		t.Fatalf("a delivered result is still owed: %+v, %v", left, err)
	}
	inbox := chat.call(t, submitter, "chat.inbox", map[string]any{})["messages"].([]Message)
	if len(inbox) != 1 || !strings.Contains(inbox[0].Body, "clear") {
		t.Fatalf("inbox: %+v", inbox)
	}
	// Published twice (a crash between storing and clearing), it is stored once.
	if err := chat.store.publish(owed[0]); err != nil {
		t.Fatal(err)
	}
	if again := chat.call(t, submitter, "chat.inbox", map[string]any{})["messages"].([]Message); len(again) != 1 {
		t.Fatalf("a repeated event was stored twice: %+v", again)
	}
	call := chat.call(t, submitter, "chat.ack", map[string]any{"message_id": inbox[0].ID})
	if call["acknowledged"] != true {
		t.Fatalf("ack: %+v", call)
	}
	if pending := chat.call(t, submitter, "chat.inbox", map[string]any{})["messages"].([]Message); len(pending) != 0 {
		t.Fatalf("acknowledged event was redelivered: %+v", pending)
	}
}

func TestAProcessFindsItsOwnChatSessionByItsToken(t *testing.T) {
	chat := newReviewChat(t)
	t.Setenv("JACKSTAY_CHAT_SESSION_TOKEN", strings.Repeat("s", 32))
	if _, err := identify(context.Background(), chat.repository.Root); err == nil {
		t.Fatal("an unregistered token was taken for a session")
	}
	session := chat.register(t, "submitter", strings.Repeat("s", 32))
	found, err := chatevent.Identify(context.Background(), chat.repository.Root)
	if err != nil || found != session.ID {
		t.Fatalf("identify: %q, %v (want %q)", found, err, session.ID)
	}
	t.Setenv("JACKSTAY_CHAT_SESSION_TOKEN", "")
	if found, err := identify(context.Background(), chat.repository.Root); err != nil || found != "" {
		t.Fatalf("a process without a token has a session: %q, %v", found, err)
	}
}

func TestASubmissionFromANestedRepositoryIsRefused(t *testing.T) {
	chat := newReviewChat(t)
	nested := testrepo.New(t, map[string]string{"file.txt": "nested\n"})
	original := reviewBackground
	reviewBackground = func(string) (review.Background, error) { return original(chat.repository.Root) }
	nestedStore, err := StoreFor(nested.Root+"/.git", chat.store.policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested.Root); err != nil {
		t.Fatal(err)
	}
	nestedSession, err := nestedStore.Register("nested", strings.Repeat("n", 32))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{"name": "review.submit",
		"arguments": map[string]any{"base": "HEAD^", "head": "HEAD", "task": "t", "reviewers": []string{"codex"}}})
	response := (&Server{store: nestedStore, session: nestedSession}).call(encoded).(map[string]any)
	if response["isError"] != true || !strings.Contains(response["content"].([]map[string]string)[0]["text"], "another repository") ||
		len(chat.launched) != 0 {
		t.Fatalf("a review of the parent repository was started from a nested one: %+v", response)
	}
}

func TestAnIncompleteSubmissionIsRefused(t *testing.T) {
	chat := newReviewChat(t)
	submitter := chat.register(t, "submitter", strings.Repeat("s", 32))
	for _, arguments := range []map[string]any{
		{"base": "HEAD^", "task": "t", "reviewers": []string{"codex"}},
		{"base": "HEAD^", "head": "HEAD", "reviewers": []string{"codex"}},
		{"base": "HEAD^", "head": "HEAD", "task": "t", "reviewers": []string{}},
	} {
		encoded, _ := json.Marshal(map[string]any{"name": "review.submit", "arguments": arguments})
		if response := (&Server{store: chat.store, session: submitter}).call(encoded).(map[string]any); response["isError"] != true {
			t.Fatalf("an incomplete submission was taken: %v", arguments)
		}
	}
	if len(chat.launched) != 0 {
		t.Fatal("an incomplete submission started a run")
	}
}

func TestALongEventFitsTheChatsMessageLimit(t *testing.T) {
	long := strings.Repeat("é", 3000)
	message := boundMessage([]string{"review of t: redesign due", long, "attached: jst:review/t/3"}, "event: review.finished; call chat.ack once read.", 4096)
	if len(message) > 4096 || !utf8.ValidString(message) || !strings.HasPrefix(message, "review of t: redesign due") ||
		!strings.HasSuffix(message, "call chat.ack once read.") {
		t.Fatalf("message of %d bytes: %q…", len(message), message[:80])
	}
	if short := boundMessage([]string{"a", "b"}, "c", 4096); short != "a\nb\nc" {
		t.Fatalf("a short message changed: %q", short)
	}
}

func TestReviewStatusSaysWhenTheNewestRequestFailedAfterAnOlderClearRound(t *testing.T) {
	chat := newReviewChat(t)
	submitter := chat.register(t, "submitter", strings.Repeat("s", 32))
	answer := chat.call(t, submitter, "review.submit", map[string]any{"base": "HEAD^", "head": "HEAD", "task": "t", "reviewers": []string{"codex"}})
	identity := answer["request"].(map[string]any)["id"].(string)
	background := chat.background(t)
	if err := background.Store.Finish(context.Background(), "t", identity, review.Result{Status: review.ExitEnvironment,
		Next: "Codex has no login"}, refusing{}); err == nil {
		t.Fatal("refused publication not said")
	}
	chat.running = false
	status := chat.call(t, submitter, "review.status", map[string]any{"task": "t"})
	last, ok := status["last"].(map[string]any)
	if !ok || last["outcome"] != "the environment refuses" || last["next"] != "Codex has no login" ||
		last["request"].(map[string]any)["head"] == "" || status["running"] != nil {
		t.Fatalf("status hides how the newest request ended: %+v", status)
	}
	if _, found := last["request"].(map[string]any)["subscribers"]; found {
		t.Fatal("status shows the sessions subscribed")
	}
}

func TestReviewStatusFromANestedRepositoryIsRefused(t *testing.T) {
	chat := newReviewChat(t)
	nested := testrepo.New(t, map[string]string{"file.txt": "nested\n"})
	original := reviewBackground
	reviewBackground = func(string) (review.Background, error) { return original(chat.repository.Root) }
	nestedStore, err := StoreFor(nested.Root+"/.git", chat.store.policy)
	if err != nil {
		t.Fatal(err)
	}
	session, err := nestedStore.Register("nested", strings.Repeat("n", 32))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{"name": "review.status", "arguments": map[string]any{"task": "t"}})
	if response := (&Server{store: nestedStore, session: session}).call(encoded).(map[string]any); response["isError"] != true {
		t.Fatalf("the parent repository's status was read from a nested one: %+v", response)
	}
}

func TestReviewStatusOfAnotherCloneOfTheSameProjectIsNotShown(t *testing.T) {
	chat := newReviewChat(t)
	submitter := chat.register(t, "submitter", strings.Repeat("s", 32))
	answer := chat.call(t, submitter, "review.submit", map[string]any{"base": "HEAD^", "head": "HEAD", "task": "t", "reviewers": []string{"codex"}})
	identity := answer["request"].(map[string]any)["id"].(string)
	if err := chat.background(t).Store.Finish(context.Background(), "t", identity, review.Result{Status: review.ExitClear}, publisher{}); err != nil {
		t.Fatal(err)
	}
	chat.running = false
	// A clear round of the task saved in the store both clones share.
	shared := chat.background(t).Store
	round := filepath.Join(shared.TaskPath("t"), "round-1")
	if err := os.MkdirAll(round, 0o700); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(review.RoundRecord{Task: "t", Round: 1, Chain: 1, Base: "b", Commit: "c"})
	if err := os.WriteFile(filepath.Join(round, review.RecordName), record, 0o600); err != nil {
		t.Fatal(err)
	}
	clone := testrepo.New(t, map[string]string{
		"jackstay.json": `{"jackstay_schema":3,"project":"review-chat-test","chat":{"enabled":true}}`,
		"file.txt":      "first\n",
	})
	t.Setenv("JACKSTAY_HOME", chat.repository.Home)
	cloneStore, err := StoreFor(clone.Root+"/.git", chat.store.policy)
	if err != nil {
		t.Fatal(err)
	}
	session, err := cloneStore.Register("clone", strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(clone.Root); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{"name": "review.status", "arguments": map[string]any{"task": "t"}})
	response := (&Server{store: cloneStore, session: session}).call(encoded).(map[string]any)
	if response["isError"] == true {
		t.Fatalf("status: %+v", response)
	}
	if status := response["structuredContent"].(map[string]any); status["last"] != nil || status["running"] != nil ||
		status["report"] != "" || status["outcome"] != nil || status["blocks"] != nil {
		t.Fatalf("a clone was shown another clone's review: %+v", status)
	}
}
