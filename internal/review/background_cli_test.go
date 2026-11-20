package review

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/chatevent"
	"github.com/zima-oss/jackstay/internal/testrepo"
)

func inFolder(t *testing.T, folder string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
}

func TestTheCommandLinesBackgroundRequestKnowsItsRepository(t *testing.T) {
	repository := testrepo.New(t, map[string]string{"jackstay.json": `{"jackstay_schema":3,"project":"cli-background"}`, "a.txt": "a\n"})
	repository.Commit("change", map[string]string{"a.txt": "b\n"})
	inFolder(t, repository.Root)
	var launched []string
	original := launchDetached
	launchDetached = func(argv []string, _, _ string) (int, int64, <-chan struct{}, error) {
		launched = argv
		done := make(chan struct{})
		close(done) // the run ended at once: nothing to wait for
		return 0, 0, done, nil
	}
	t.Cleanup(func() { launchDetached = original })
	var stdout, stderr bytes.Buffer
	if status := runReview([]string{"--background", "--task", "t", "--reviewer", "codex", "HEAD^..HEAD"}, nil, &stdout, &stderr); status != 0 {
		t.Fatalf("exit %d: %s %s", status, stdout.String(), stderr.String())
	}
	background, err := BackgroundAt(repository.Root, "")
	if err != nil {
		t.Fatal(err)
	}
	request, found, err := background.Store.ReadRequest("t")
	if err != nil || !found || request.Repository == "" || request.Repository != background.Repository || len(launched) == 0 {
		t.Fatalf("request %+v (want repository %q), launched %v, %v", request, background.Repository, launched, err)
	}
	if !strings.Contains(stdout.String(), "started in the background") {
		t.Fatalf("stdout: %s", stdout.String())
	}
}

func TestARepositoryMadeAgainAtTheSamePathIsAnotherOne(t *testing.T) {
	repository := testrepo.New(t, map[string]string{"a.txt": "a\n"})
	first, err := repositoryIdentity(repository.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repository.Root, repository.Root+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repository.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", repository.Root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	second, err := repositoryIdentity(repository.Root)
	if err != nil || second == first || !strings.HasPrefix(second, filepath.Join(must(filepath.EvalSymlinks(repository.Root)), ".git")) {
		t.Fatalf("identities %q and %q (%v)", first, second, err)
	}
}

func must(path string, err error) string {
	if err != nil {
		panic(err)
	}
	return path
}

func TestAnEndWhoseFolderWasTakenByAnotherReviewShowsNoReport(t *testing.T) {
	h := newBackgroundHarness(t)
	submitted, err := h.background.Submit(t.Context(), Submission{Revision: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(h.background.Store.TaskPath("feature/x"), "round-1")
	if err := writeJSON(filepath.Join(folder, AttemptName), AttemptRecord{Task: "feature/x", Attempt: 1, Request: "someone-else"}); err != nil {
		t.Fatal(err)
	}
	if err := h.background.Store.Finish(t.Context(), "feature/x", submitted.Request.ID, Result{Status: ExitClear, Folder: folder,
		Record: &RoundRecord{Round: 1}}, &recording{}); err != nil {
		t.Fatal(err)
	}
	h.alive = false
	status, err := h.background.StatusOf("feature/x")
	if err != nil || status.Last == nil || status.Report != "" || status.Last.Folder != "" || status.Last.Outcome != "clear" {
		t.Fatalf("status: %+v, %v", status, err)
	}
}

func TestAnEventOfAFolderTakenByAnotherReviewNamesNoReport(t *testing.T) {
	h := newBackgroundHarness(t)
	submitted, err := h.background.Submit(t.Context(), Submission{Revision: "HEAD", Subscriber: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(h.background.Store.TaskPath("feature/x"), "round-1")
	if err := writeJSON(filepath.Join(folder, AttemptName), AttemptRecord{Task: "feature/x", Attempt: 1, Request: submitted.Request.ID}); err != nil {
		t.Fatal(err)
	}
	if err := h.background.Store.Finish(t.Context(), "feature/x", submitted.Request.ID, Result{Status: ExitClear, Folder: folder,
		Record: &RoundRecord{Round: 1}}, refusingPublisher{}); err == nil {
		t.Fatal("refused publication not said")
	}
	// Pruned, then taken by another review before the event was delivered.
	if err := writeJSON(filepath.Join(folder, AttemptName), AttemptRecord{Task: "feature/x", Attempt: 1, Request: "later"}); err != nil {
		t.Fatal(err)
	}
	published := &recording{}
	h.alive = false
	if err := h.background.DeliverOwed(t.Context(), "s1", published); err != nil || len(published.events) != 1 {
		t.Fatalf("delivery: %+v, %v", published.events, err)
	}
	event := published.events[0]
	if strings.Contains(event.Body, folder) || len(event.Attachments) != 0 || event.Meta["round"] != "" || event.Meta["verdict"] != "clear" {
		t.Fatalf("the event names another review's report: %+v", event)
	}
}

type refusingPublisher struct{}

func (refusingPublisher) Publish(context.Context, chatevent.Event) error { return errors.New("full") }

func TestTheCommandLinePointsAtThisRequestsOwnOutcome(t *testing.T) {
	repository := testrepo.New(t, map[string]string{"jackstay.json": `{"jackstay_schema":3,"project":"cli-next"}`, "a.txt": "a\n"})
	repository.Commit("change", map[string]string{"a.txt": "b\n"})
	inFolder(t, repository.Root)
	original := launchDetached
	launchDetached = func([]string, string, string) (int, int64, <-chan struct{}, error) {
		done := make(chan struct{})
		close(done)
		return 0, 0, done, nil
	}
	t.Cleanup(func() { launchDetached = original })
	var stdout, stderr bytes.Buffer
	if status := runReview([]string{"--background", "--task", "t", "--reviewer", "codex", "HEAD^..HEAD"}, nil, &stdout, &stderr); status != 0 {
		t.Fatalf("exit %d: %s", status, stderr.String())
	}
	if strings.Contains(stdout.String(), "jst review last") || !strings.Contains(stdout.String(), "the end of the log") {
		t.Fatalf("the next step could show an older round: %s", stdout.String())
	}
}
