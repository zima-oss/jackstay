package sessionchat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/reviewpool"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

func TestMCPReviewSubmissionStatusAndAcknowledgedOutcome(t *testing.T) {
	repository := testrepo.New(t, map[string]string{
		"jackstay.json": `{"jackstay_schema":3,"project":"review-chat-test","chat":{"enabled":true,"claude_code":{"enabled":true}}}`,
		"file.txt":      "first\n",
	})
	repository.Commit("change", map[string]string{"file.txt": "second\n"})
	store, err := StoreFor(repository.Root+"/.git", config.ChatPolicy{
		Enabled: true, MaxMessageBytes: 4096, MaxPending: 16,
		MaxPendingPerSession: 8, MaxSessions: 8, RetentionHours: 72,
	})
	if err != nil {
		t.Fatal(err)
	}
	submitter, err := store.Register("submitter", strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Register("other", strings.Repeat("o", 32))
	if err != nil {
		t.Fatal(err)
	}
	server := Server{store: store, session: submitter}
	call := func(server Server, name string, arguments any) map[string]any {
		t.Helper()
		encoded, err := json.Marshal(map[string]any{"name": name, "arguments": arguments})
		if err != nil {
			t.Fatal(err)
		}
		response := server.call(encoded).(map[string]any)
		if response["isError"] == true {
			t.Fatalf("%s failed: %+v", name, response)
		}
		return response["structuredContent"].(map[string]any)
	}
	request := map[string]any{"base": "HEAD^", "head": "HEAD", "task": "review-task", "reviewers": []string{"codex"}}
	first := call(server, "review.submit", request)
	if first["created"] != true {
		t.Fatalf("request was not created: %+v", first)
	}
	jobID := first["job"].(map[string]any)["id"].(string)
	second := call(server, "review.submit", request)
	if second["created"] != false || second["job"].(map[string]any)["id"] != jobID {
		t.Fatalf("repeated request changed job: %+v", second)
	}
	ephemeral, err := store.Register("without-token", "")
	if err != nil {
		t.Fatal(err)
	}
	ephemeralServer := Server{store: store, session: ephemeral}
	if duplicate := call(ephemeralServer, "review.submit", request); duplicate["created"] != false ||
		duplicate["job"].(map[string]any)["id"] != jobID {
		t.Fatalf("tokenless session could not subscribe to the existing job: %+v", duplicate)
	}
	pool, err := reviewpool.Default()
	if err != nil {
		t.Fatal(err)
	}
	_, found, err := pool.RunNext(context.Background(), func(context.Context, reviewpool.Job) (int, error) { return 0, nil })
	if err != nil || !found {
		t.Fatalf("review did not finish: %v, %v", found, err)
	}
	store.policy.MaxPending = 1
	ordinary, _, err := store.Send(other.ID, submitter.ID, "message", "ordinary pending chat", "ordinary-1", "")
	if err != nil {
		t.Fatal(err)
	}
	restarted := Server{store: store, session: submitter}
	status := call(restarted, "review.status", map[string]any{"job_id": jobID})
	job := status["job"].(map[string]any)
	if job["status"] != "complete" || job["attempts"] != 1 || strings.Contains(jobID, repository.Root) {
		t.Fatalf("status did not survive restart or leaked path: %+v", job)
	}
	otherServer := Server{store: store, session: other}
	if response := otherServer.call([]byte(`{"name":"review.status","arguments":{"job_id":"` + jobID + `"}}`)).(map[string]any); response["isError"] != true {
		t.Fatalf("unsubscribed session read status: %+v", response)
	}
	firstResult := call(restarted, "chat.inbox", map[string]any{})
	firstInbox := firstResult["messages"].([]Message)
	if len(firstInbox) != 1 || firstInbox[0].ID != ordinary.ID {
		t.Fatalf("review sync displaced ordinary chat at capacity: %+v", firstInbox)
	}
	if warning, ok := firstResult["notification_error"].(string); !ok || !strings.Contains(warning, "capacity") {
		t.Fatalf("partial inbox hid review sync failure: %+v", firstResult)
	}
	fake, socket := startFakeCodex(t)
	bridge := CodexBridge{Store: store, SessionID: submitter.ID, SocketPath: socket, ThreadID: "thread-1"}
	var notificationFailure reviewNotificationSyncError
	if delivered, err := bridge.Deliver(context.Background()); delivered != 1 || !errors.As(err, &notificationFailure) || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("bridge hid partial review sync failure: delivered %d, error %v", delivered, err)
	}
	if fake.steers != 1 {
		t.Fatalf("bridge did not deliver ordinary message: %d", fake.steers)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repository.Root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	t.Setenv("JACKSTAY_CHAT_SESSION_TOKEN", strings.Repeat("s", 32))
	original := localPolicy
	localPolicy = func(section map[string]any) config.ChatPolicy {
		policy := original(section)
		policy.MaxPending = 1
		return policy
	}
	t.Cleanup(func() { localPolicy = original })
	var hookOutput, hookDiagnostics bytes.Buffer
	if status := runClaudeHook([]string{"--name", "submitter", "--event", "PostToolBatch"}, nil, &hookOutput, &hookDiagnostics); status != 0 ||
		!strings.Contains(hookOutput.String(), "Review notification sync failed") || !strings.Contains(hookDiagnostics.String(), "capacity") {
		t.Fatalf("hook hid partial review sync failure: exit %d, %s, %s", status, hookOutput.String(), hookDiagnostics.String())
	}
	call(restarted, "chat.ack", map[string]any{"message_id": ordinary.ID})
	inbox := call(restarted, "chat.inbox", map[string]any{})["messages"].([]Message)
	if len(inbox) != 1 || inbox[0].From != "review-pool" || !strings.Contains(inbox[0].Body, jobID) {
		t.Fatalf("terminal notification missing: %+v", inbox)
	}
	ephemeralInbox := call(ephemeralServer, "chat.inbox", map[string]any{})["messages"].([]Message)
	if len(ephemeralInbox) != 1 || ephemeralInbox[0].ID == inbox[0].ID || !strings.Contains(ephemeralInbox[0].Body, jobID) {
		t.Fatalf("tokenless subscriber did not receive an independent notification: %+v", ephemeralInbox)
	}
	call(ephemeralServer, "chat.ack", map[string]any{"message_id": ephemeralInbox[0].ID})
	store.now = func() time.Time { return time.Now().Add(96 * time.Hour) }
	restarted.store = store
	retained := call(restarted, "chat.inbox", map[string]any{})["messages"].([]Message)
	if len(retained) != 1 || retained[0].ID != inbox[0].ID {
		t.Fatalf("unacknowledged review outcome expired: %+v", retained)
	}
	call(restarted, "chat.ack", map[string]any{"message_id": inbox[0].ID})
	if pending := call(restarted, "chat.inbox", map[string]any{})["messages"].([]Message); len(pending) != 0 {
		t.Fatalf("acknowledged event was redelivered: %+v", pending)
	}
}

func TestPrunedReviewCanNotifyAgainForTheSamePinnedRequest(t *testing.T) {
	ctx := context.Background()
	repository := testrepo.New(t, map[string]string{
		"jackstay.json": `{"jackstay_schema":3,"project":"prune-chat-test","review":{"pool":{"accepting":true}}}`,
		"file.txt":      "first\n",
	})
	repository.Commit("change", map[string]string{"file.txt": "second\n"})
	store, err := StoreFor(repository.Root+"/.git", config.ChatPolicy{
		Enabled: true, MaxMessageBytes: 4096, MaxPending: 8, MaxPendingPerSession: 8, MaxSessions: 8, RetentionHours: 72,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.Register("submitter", strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := reviewpool.Default()
	if err != nil {
		t.Fatal(err)
	}
	request := reviewpool.Request{Repository: repository.Root, Base: "HEAD^", Head: "HEAD", Task: "repeat", Reviewers: []string{"codex"}}
	complete := func() reviewpool.Job {
		t.Helper()
		if _, created, err := pool.EnqueueForSession(ctx, request, session.ID, store.commonDirectory); err != nil || !created {
			t.Fatalf("enqueue: %v, %v", created, err)
		}
		job, found, err := pool.RunNext(ctx, func(context.Context, reviewpool.Job) (int, error) { return 0, nil })
		if err != nil || !found {
			t.Fatalf("worker: %v, %v", found, err)
		}
		if err := syncReviewNotifications(ctx, store, session.ID); err != nil {
			t.Fatal(err)
		}
		return job
	}
	first := complete()
	messages, err := store.Inbox(session.ID, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("first inbox: %+v, %v", messages, err)
	}
	firstMessage := messages[0]
	if err := store.Acknowledge(session.ID, firstMessage.ID); err != nil {
		t.Fatal(err)
	}
	pruned, err := pool.Prune(ctx, first.UpdatedAt.Add(review.PoolHistoryDays*24*time.Hour), true)
	if err != nil || pruned.Removed != 1 {
		t.Fatalf("prune after handoff: %+v, %v", pruned, err)
	}
	second := complete()
	if second.ID != first.ID || second.Generation == first.Generation {
		t.Fatalf("job identity or delivery generation: %+v, %+v", first, second)
	}
	messages, err = store.Inbox(session.ID, 10)
	if err != nil || len(messages) != 1 || messages[0].ID == firstMessage.ID {
		t.Fatalf("old ACK suppressed new event: %+v, %v", messages, err)
	}
}
