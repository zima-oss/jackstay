package contextbrief

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "jackstay.json"), []byte(`{"jackstay_schema":2,"project":"fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"init", "--quiet", root}, {"-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "initial"}} {
		command := exec.Command("git", arguments...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", arguments, output, err)
		}
	}
	return root
}

func useProject(t *testing.T, root string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
}

func TestBriefRevisionTombstoneAndIsolation(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	first := testProject(t)
	second := filepath.Join(t.TempDir(), "linked")
	command := exec.Command("git", "-C", first, "worktree", "add", "--quiet", "-b", "linked-task", second)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("linked worktree: %s: %v", output, err)
	}
	store, err := StoreFor(first)
	if err != nil {
		t.Fatal(err)
	}
	other, err := StoreFor(second)
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{Branch: "main", Head: "abc", RepositoryID: "repo-a"}
	brief := Brief{TaskID: "task-a", Task: "First task", Next: []string{"Run gate"}}
	initial, err := store.Save(brief, 0, defaultMaxAge, identity)
	if err != nil || initial.Revision != 1 {
		t.Fatalf("save: %+v %v", initial, err)
	}
	information, err := os.Stat(store.path)
	if err != nil || information.Mode().Perm() != 0o600 {
		t.Fatalf("private state mode: %v %v", information, err)
	}
	if _, err := store.Save(brief, 0, defaultMaxAge, identity); err == nil {
		t.Fatal("stale save succeeded")
	}
	if _, err := store.Save(Brief{TaskID: "task-b", Task: "Second", Next: []string{"Finish"}}, 1, defaultMaxAge, identity); err == nil {
		t.Fatal("task identity changed without clear")
	}
	cleared, err := store.Clear(1)
	if err != nil || cleared.Revision != 2 || cleared.Brief != nil {
		t.Fatalf("clear: %+v %v", cleared, err)
	}
	if _, err := store.Save(brief, 1, defaultMaxAge, identity); err == nil {
		t.Fatal("ABA write succeeded")
	}
	if _, err := store.Clear(1); err == nil {
		t.Fatal("ABA clear succeeded")
	}
	brief.TaskID = "task-b"
	created, err := store.Save(brief, 2, defaultMaxAge, identity)
	if err != nil || created.Revision != 3 {
		t.Fatalf("new task: %+v %v", created, err)
	}
	if state, err := other.Load(); err != nil || state.Revision != 0 || state.Brief != nil {
		t.Fatalf("cross-worktree state: %+v %v", state, err)
	}
}

func TestFreshnessWithoutTimingWaits(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	store, err := StoreFor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return start }
	identity := Identity{Branch: "feature", Head: "a", RepositoryID: "repo-a"}
	record, err := store.Save(Brief{TaskID: "task", Task: "Work", Next: []string{"Next"}}, 0, time.Hour, identity)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Freshness(record, identity); got != "current" {
		t.Fatal(got)
	}
	if got := store.Freshness(record, Identity{Branch: "feature", Head: "b", RepositoryID: "repo-a"}); got != "head-changed" {
		t.Fatal(got)
	}
	if got := store.Freshness(record, Identity{Branch: "other", Head: "a", RepositoryID: "repo-a"}); got != "branch-changed" {
		t.Fatal(got)
	}
	store.now = func() time.Time { return start.Add(2 * time.Hour) }
	if got := store.Freshness(record, identity); got != "expired" {
		t.Fatal(got)
	}
}

func TestConcurrentWritersCannotUseSameRevision(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	store, err := StoreFor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, task := range []string{"first", "second"} {
		go func(taskID string) {
			_, saveError := store.Save(Brief{TaskID: taskID, Task: "Concurrent", Next: []string{"Finish"}}, 0, defaultMaxAge, Identity{Branch: "main", Head: "abc", RepositoryID: "repo-a"})
			results <- saveError
		}(task)
	}
	successes := 0
	for range 2 {
		if result := <-results; result == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected one successful save, got %d", successes)
	}
	record, err := store.Load()
	if err != nil || record.Revision != 1 {
		t.Fatalf("concurrent state: %+v %v", record, err)
	}
}

func TestClaudeHookOnlyForMatchingCompactedWorktree(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	first := testProject(t)
	second := testProject(t)
	useProject(t, first)
	input := `{"task_id":"task-a","task":"Ship brief","next":["Run gate"],"decisions":["Use local state"]}`
	var output, diagnostics bytes.Buffer
	if code := runSave([]string{"--expect", "0"}, strings.NewReader(input), &output, &diagnostics); code != 0 {
		t.Fatalf("save: %d %s", code, diagnostics.String())
	}
	var shown bytes.Buffer
	if code := runShow(nil, nil, &shown, &diagnostics); code != 0 || !strings.Contains(shown.String(), `"revision":1`) {
		t.Fatalf("show: %d %s %s", code, shown.String(), diagnostics.String())
	}
	hookInput := func(root, source string) string {
		encoded, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "source": source, "cwd": root, "transcript_path": "/private/never-read"})
		return string(encoded)
	}
	var restored bytes.Buffer
	if code := runClaudeHook(nil, strings.NewReader(hookInput(first, "compact")), &restored, &diagnostics); code != 0 || !strings.Contains(restored.String(), "Run gate") || strings.Contains(restored.String(), "never-read") {
		t.Fatalf("hook: %d %s %s", code, restored.String(), diagnostics.String())
	}
	for _, event := range []string{hookInput(first, "startup"), hookInput(second, "compact"), `{"hook_event_name":"PostCompact","source":"compact","cwd":"` + first + `"}`} {
		var ignored bytes.Buffer
		if code := runClaudeHook(nil, strings.NewReader(event), &ignored, &diagnostics); code != 0 || ignored.Len() != 0 {
			t.Fatalf("unexpected context: %d %s", code, ignored.String())
		}
	}
	store, err := StoreFor(first)
	if err != nil {
		t.Fatal(err)
	}
	originalIdentity, err := gitIdentity(first)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-C", first, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "advance")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("advance HEAD: %s: %v", output, err)
	}
	restored.Reset()
	if code := runClaudeHook(nil, strings.NewReader(hookInput(first, "compact")), &restored, &diagnostics); code != 0 || !strings.Contains(restored.String(), "HEAD changed") {
		t.Fatalf("changed HEAD: %d %s", code, restored.String())
	}
	command = exec.Command("git", "-C", first, "branch", "-m", "other-branch")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("switch branch: %s: %v", output, err)
	}
	restored.Reset()
	if code := runClaudeHook(nil, strings.NewReader(hookInput(first, "compact")), &restored, &diagnostics); code != 0 || restored.Len() != 0 {
		t.Fatalf("changed branch injected: %d %s", code, restored.String())
	}
	command = exec.Command("git", "-C", first, "branch", "-m", originalIdentity.Branch)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restore branch: %s: %v", output, err)
	}
	stale, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	stale.UpdatedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	encoded, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	restored.Reset()
	if code := runClaudeHook(nil, strings.NewReader(hookInput(first, "compact")), &restored, &diagnostics); code != 0 || restored.Len() != 0 {
		t.Fatalf("expired brief injected: %d %s", code, restored.String())
	}
	if code := runClear([]string{"--expect", "1"}, nil, &output, &diagnostics); code != 0 {
		t.Fatalf("clear: %d %s", code, diagnostics.String())
	}
	shown.Reset()
	if code := runShow(nil, nil, &shown, &diagnostics); code != 0 || !strings.Contains(shown.String(), `"revision":2`) || !strings.Contains(shown.String(), `"brief":null`) {
		t.Fatalf("tombstone: %d %s", code, shown.String())
	}
}

func TestBadInputDoesNotReplaceBrief(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	root := testProject(t)
	useProject(t, root)
	var output, diagnostics bytes.Buffer
	valid := `{"task_id":"task-a","task":"Current","next":["Finish"]}`
	if code := runSave([]string{"--expect", "0"}, strings.NewReader(valid), &output, &diagnostics); code != 0 {
		t.Fatalf("initial save: %d %s", code, diagnostics.String())
	}
	for _, invalid := range []string{`{"task_id":"task-a","task":"Oops","next":["Finish"],"transcript":"private"}`, `{"task_id":"task-a","task":"Oops","next":[]}`, strings.Repeat("x", maxBriefBytes+1)} {
		if code := runSave([]string{"--expect", "1"}, strings.NewReader(invalid), &output, &diagnostics); code != 2 {
			t.Fatalf("accepted invalid input: %d", code)
		}
	}
	store, err := StoreFor(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load()
	if err != nil || record.Revision != 1 || record.Brief.Task != "Current" {
		t.Fatalf("invalid save replaced state: %+v %v", record, err)
	}
}

func TestReplacedCheckoutAtSamePathCannotReadOldBrief(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	root := testProject(t)
	useProject(t, root)
	secret := `old private task`
	input := `{"task_id":"old","task":"` + secret + `","next":["Finish"]}`
	var output, diagnostics bytes.Buffer
	if code := runSave([]string{"--expect", "0"}, strings.NewReader(input), &output, &diagnostics); code != 0 {
		t.Fatalf("save: %d %s", code, diagnostics.String())
	}
	if err := os.Rename(filepath.Join(root, ".git"), filepath.Join(root, "old-git")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := runShow(nil, nil, &output, &diagnostics); code != 0 || strings.Contains(output.String(), secret) || !strings.Contains(output.String(), `"freshness":"git-unavailable"`) || !strings.Contains(output.String(), `"brief":null`) {
		t.Fatalf("old brief leaked without Git identity: %d %s", code, output.String())
	}
	for _, arguments := range [][]string{{"init", "--quiet", root}, {"-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "new repository"}} {
		command := exec.Command("git", arguments...)
		if result, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", arguments, result, err)
		}
	}
	output.Reset()
	if code := runShow(nil, nil, &output, &diagnostics); code != 0 || strings.Contains(output.String(), secret) || !strings.Contains(output.String(), `"freshness":"repository-changed"`) || !strings.Contains(output.String(), `"brief":null`) {
		t.Fatalf("old brief leaked in show: %d %s", code, output.String())
	}
	event, _ := json.Marshal(map[string]string{"hook_event_name": "SessionStart", "source": "compact", "cwd": root})
	output.Reset()
	if code := runClaudeHook(nil, bytes.NewReader(event), &output, &diagnostics); code != 0 || output.Len() != 0 {
		t.Fatalf("old brief leaked in hook: %d %s", code, output.String())
	}
	if code := runSave([]string{"--expect", "1"}, strings.NewReader(`{"task_id":"new","task":"New task","next":["Finish"]}`), &output, &diagnostics); code != 2 {
		t.Fatalf("replaced checkout overwrote old brief: %d", code)
	}
	if code := runClear([]string{"--expect", "1"}, nil, &output, &diagnostics); code != 0 {
		t.Fatalf("clear old brief: %d %s", code, diagnostics.String())
	}
	if code := runSave([]string{"--expect", "2"}, strings.NewReader(`{"task_id":"new","task":"New task","next":["Finish"]}`), &output, &diagnostics); code != 0 {
		t.Fatalf("new task: %d %s", code, diagnostics.String())
	}
}
