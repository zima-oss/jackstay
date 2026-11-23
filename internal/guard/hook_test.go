package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/telemetrytest"
)

// generousBudgets give the hook's real work room on a loaded machine; only a budget's own test
// shortens one.
var generousBudgets = budgets{answer: testBudget, exit: testBudget, reading: testBudget, question: testBudget,
	call: testBudget}

// runHook runs the hook as the guard command runs it, its span written to home's journal.
func runHook(t *testing.T, home string, payload any, raw string) (map[string]any, string) {
	t.Helper()
	return runHookWithin(t, generousBudgets, home, payload, raw)
}

// runHookWithin is runHook with the hook's budgets set to within for the run.
func runHookWithin(t *testing.T, within budgets, home string, payload any, raw string) (map[string]any, string) {
	t.Helper()
	t.Setenv("JACKSTAY_HOME", home)
	// The journal is read back: Finish waits for its write however long a loaded machine takes.
	t.Setenv(telemetry.FinishWaitVariable, telemetrytest.GenerousFinishWait)
	telemetry.Reset()
	t.Cleanup(telemetry.Reset)
	telemetry.Begin("guard", nil)
	defer telemetry.Finish(0)
	text := raw
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		text = string(data)
	}
	// Locked: a worker that outlived its answer budget may still write after Hook returned.
	var stdout, stderr lockedBuffer
	if code := hookWith(within, strings.NewReader(text), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	written, said := stdout.snapshot(), string(stderr.snapshot())
	if len(written) == 0 {
		return nil, said
	}
	// The first answer is the hook's; a late worker's write, if any, follows it.
	var out map[string]any
	if err := json.NewDecoder(bytes.NewReader(written)).Decode(&out); err != nil {
		t.Fatalf("stdout %q: %v", written, err)
	}
	return out, said
}

// lockedBuffer is a buffer safe to write from a goroutine the test does not wait for.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (locked *lockedBuffer) Write(data []byte) (int, error) {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.Write(data)
}

func (locked *lockedBuffer) snapshot() []byte {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return bytes.Clone(locked.buffer.Bytes())
}

func bashCall(command, cwd string) map[string]any {
	return map[string]any{"session_id": "s", "cwd": cwd, "hook_event_name": "PreToolUse", "tool_name": "Bash",
		"tool_input": map[string]any{"command": command}, "tool_use_id": "t"}
}

func decisionOf(out map[string]any) string {
	specific, _ := out["hookSpecificOutput"].(map[string]any)
	text, _ := specific["permissionDecision"].(string)
	return text
}

// readLog is the guard's decisions in home's journal, one per call, as records: the event (the
// outcome), its rules, the project, the tool and the config error.
func readLog(t *testing.T, home string) []map[string]any {
	t.Helper()
	events := map[string]string{outcomeRefused: "refused", outcomeWarned: "warned", outcomeFailedOpen: "failed-open",
		outcomeIgnoredTool: "ignored-tool", outcomeAllowed: "allowed"}
	var records []map[string]any
	for _, span := range telemetrytest.Named(telemetrytest.Spans(t, home), "jst guard") {
		record := map[string]any{"event": events[span.Text(telemetry.AttrGuardOutcome)], "project": span.ResourceText(telemetry.AttrProject),
			"tool_name": span.Text(telemetry.AttrGuardTool), "config_error": span.Text(telemetry.AttrGuardConfigError)}
		if rules, ok := span.Attributes[telemetry.AttrGuardRules].([]any); ok {
			record["rules"] = rules
			if len(rules) > 0 {
				record["rule"] = rules[0]
			}
		}
		records = append(records, record)
	}
	return records
}

func writeConfig(t *testing.T, folder, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(folder, "jackstay.json"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHookRefusesWithTheDocumentedAnswer(t *testing.T) {
	home := t.TempDir()
	out, _ := runHook(t, home, bashCall("git reset --hard", t.TempDir()), "")
	if decisionOf(out) != "deny" {
		t.Fatalf("answer %v", out)
	}
	specific := out["hookSpecificOutput"].(map[string]any)
	if specific["hookEventName"] != "PreToolUse" || !strings.Contains(specific["permissionDecisionReason"].(string), "jst discard <path>") {
		t.Fatalf("reason %v", specific)
	}
}

func TestHookSaysNothingForAnAllowedCommand(t *testing.T) {
	home := t.TempDir()
	if out, _ := runHook(t, home, bashCall("git status", t.TempDir()), ""); out != nil {
		t.Fatalf("answer %v", out)
	}
}

func TestHookDoesNotLookAtAnotherTool(t *testing.T) {
	home := t.TempDir()
	out, _ := runHook(t, home, map[string]any{"tool_name": "Write", "tool_input": map[string]any{"command": "git reset --hard"}}, "")
	if out != nil {
		t.Fatalf("answer %v", out)
	}
}

func TestHookFailsOpenVisiblyOnInputItCannotRead(t *testing.T) {
	home := t.TempDir()
	out, stderr := runHook(t, home, nil, "{not json")
	if !strings.Contains(out["systemMessage"].(string), "did NOT check") || !strings.Contains(stderr, "did NOT check") {
		t.Fatalf("answer %v %q", out, stderr)
	}
}

func TestHookRefusesAToolInputItCannotRead(t *testing.T) {
	home := t.TempDir()
	folder := t.TempDir()
	given := bashCall("git reset --hard", folder)
	given["tool_input"] = `{"command": "git reset --hard"}`
	if out, _ := runHook(t, home, given, ""); decisionOf(out) != "deny" {
		t.Fatalf("text JSON: %v", out)
	}
	given["tool_input"] = "git reset --hard"
	out, _ := runHook(t, home, given, "")
	if decisionOf(out) != "deny" || !strings.Contains(out["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string), "cannot be read") {
		t.Fatalf("not JSON: %v", out)
	}
	given["tool_input"] = map[string]any{"command": []string{"git", "reset", "--hard"}}
	if out, _ := runHook(t, home, given, ""); decisionOf(out) != "deny" {
		t.Fatalf("not text: %v", out)
	}
}

func TestHookKeepsTheBuiltInRulesWhenJackstayJSONCannotBeUsed(t *testing.T) {
	for _, text := range []string{"{broken", "[1, 2]", `{"jackstay_schema": 1, "gaurd": {}}`,
		`{"jackstay_schema": 1, "project": "demo", "guard": {"deny": [{"id": "x", "command": "("}]}}`} {
		home, folder := t.TempDir(), t.TempDir()
		writeConfig(t, folder, text)
		out, _ := runHook(t, home, bashCall("git reset --hard", folder), "")
		if decisionOf(out) != "deny" || !strings.Contains(out["systemMessage"].(string), "jackstay.json cannot be used") ||
			strings.Contains(out["systemMessage"].(string), "did NOT check") {
			t.Fatalf("%s: %v", text, out)
		}
		second := bashCall("git status", folder)
		second["session_id"] = "another"
		out, _ = runHook(t, home, second, "")
		if !strings.Contains(out["systemMessage"].(string), "jackstay.json cannot be used") || !toldTheAgentOnly(out, "jackstay.json cannot be used") {
			t.Fatalf("%s: %v", text, out)
		}
		var events []string
		for _, record := range readLog(t, home) {
			events = append(events, record["event"].(string))
			if record["config_error"] == "" {
				t.Fatalf("%s: a call without its config error: %v", text, record)
			}
		}
		if strings.Join(events, " ") != "refused allowed" {
			t.Fatalf("%s: events %v", text, events)
		}
	}
}

// An unusable jackstay.json is said once to a session within toldEvery, again when the file changes,
// and every time to a call without a session; every call still logs the reason.
func TestHookSaysAnUnusableConfigOnceInASession(t *testing.T) {
	home, folder := t.TempDir(), t.TempDir()
	writeConfig(t, folder, "{broken")
	said := func(given map[string]any) bool {
		t.Helper()
		out, _ := runHook(t, home, given, "")
		message, _ := out["systemMessage"].(string)
		return strings.Contains(message, "jackstay.json cannot be used")
	}
	if !said(bashCall("git status", folder)) {
		t.Fatal("the first call of a session was not told")
	}
	if said(bashCall("git status", folder)) {
		t.Fatal("the second call of the same session was told again")
	}
	if out, _ := runHook(t, home, bashCall("git reset --hard", folder), ""); decisionOf(out) != "deny" ||
		strings.Contains(fmt.Sprint(out), "cannot be used") {
		t.Fatalf("a refusal later in the session: %v", out)
	}
	other := bashCall("git status", folder)
	other["session_id"] = "other"
	if !said(other) {
		t.Fatal("another session was not told")
	}
	writeConfig(t, folder, "{broken again")
	if !said(bashCall("git status", folder)) {
		t.Fatal("a changed file was not told")
	}
	alone := bashCall("git status", folder)
	delete(alone, "session_id")
	for range 2 {
		if !said(alone) {
			t.Fatal("a call without a session was not told each time")
		}
	}
	for _, record := range readLog(t, home) {
		if record["config_error"] == "" {
			t.Fatalf("a call without its config error: %v", record)
		}
	}
}

func TestHookStillRefusesWhenTheLogCannotBeWritten(t *testing.T) {
	notAFolder := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notAFolder, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _ := runHook(t, notAFolder, bashCall("git reset --hard", t.TempDir()), ""); decisionOf(out) != "deny" {
		t.Fatalf("answer %v", out)
	}
	if out, _ := runHook(t, "relative-home", bashCall("git reset --hard", t.TempDir()), ""); decisionOf(out) != "deny" {
		t.Fatalf("answer %v", out)
	}
}

func TestHookWarnsOfANearWildcardAndKeepsTheOtherRules(t *testing.T) {
	home, folder := t.TempDir(), t.TempDir()
	writeConfig(t, folder, `{"jackstay_schema": 1, "guard": {"regenerable": ["?*", "out-*"]}}`)
	out, _ := runHook(t, home, bashCall("git reset --hard", folder), "")
	if decisionOf(out) != "deny" || !strings.Contains(out["systemMessage"].(string), "would match nearly every name") {
		t.Fatalf("answer %v", out)
	}
	out, _ = runHook(t, home, bashCall("git status", folder), "")
	if !toldTheAgentOnly(out, "would match nearly every name") || !strings.Contains(out["systemMessage"].(string), "would match nearly every name") {
		t.Fatalf("answer %v", out)
	}
	var events []string
	for _, record := range readLog(t, home) {
		events = append(events, record["event"].(string))
	}
	if strings.Join(events, " ") != "refused warned" {
		t.Fatalf("events %v", events)
	}
}

func TestHookMarksEveryCallWithASessionAndOnlyThose(t *testing.T) {
	home, folder := t.TempDir(), t.TempDir()
	writeConfig(t, folder, `{"jackstay_schema": 1, "project": "demo"}`)
	given := bashCall("git reset --hard", folder)
	delete(given, "session_id")
	runHook(t, home, given, "")
	if _, err := os.Stat(filepath.Join(home, "demo", "guard-seen")); err == nil {
		t.Fatal("a probe without a session left a mark")
	}
	runHook(t, home, bashCall("git status", folder), "")
	if _, err := os.Stat(filepath.Join(home, "demo", "guard-seen")); err != nil {
		t.Fatal("no mark")
	}
	runHook(t, home, map[string]any{"tool_name": "Write", "cwd": folder, "session_id": "s", "tool_input": map[string]any{"file_path": "x"}}, "")
	log := readLog(t, home)
	last := log[len(log)-1]
	if len(log) != 3 || last["event"] != "ignored-tool" || last["tool_name"] != "Write" || last["project"] != "demo" {
		t.Fatalf("log %v", log)
	}
}

func TestHookFindsTheProjectFromTheWorkingFolder(t *testing.T) {
	home, folder := t.TempDir(), t.TempDir()
	writeConfig(t, folder, `{"jackstay_schema": 1, "project": "demo", "guard": {"deny": [{"id": "rec", "env": {"REC": "1"}, "why": "it rewrites every reference image", "instead": "i"}]}}`)
	sub := filepath.Join(folder, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	out, _ := runHook(t, home, bashCall("REC=1 swift test", sub), "")
	if !strings.Contains(out["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string), "rewrites every reference image") {
		t.Fatalf("answer %v", out)
	}
	if log := readLog(t, home); log[0]["project"] != "demo" {
		t.Fatalf("log %v", log)
	}
}

func TestCheckCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := checkWith(generousBudgets, []string{"--cwd", "/tmp", "--", "git clean -fd"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "git clean -n") {
		t.Fatalf("%d %q", code, stderr.String())
	}
	if code := checkWith(generousBudgets, []string{"--cwd", "/tmp", "--", "git clean -n"}, &stdout, &stderr); code != 0 {
		t.Fatalf("%d", code)
	}
}

// toldTheAgentOnly says the answer gives the agent the text (additionalContext) and makes no
// permission decision: the person's own permission flow is unchanged (W-053).
func toldTheAgentOnly(out map[string]any, text string) bool {
	specific, ok := out["hookSpecificOutput"].(map[string]any)
	if !ok {
		return false
	}
	_, decided := specific["permissionDecision"]
	context, _ := specific["additionalContext"].(string)
	return !decided && specific["hookEventName"] == "PreToolUse" && strings.Contains(context, text)
}

// The mark is taken only once the answer is written, and lasts toldEvery: a hook stopped before
// it tells again, and so does a session toldEvery later.
func TestASessionIsToldAgainUnlessTheAnswerWasWrittenRecently(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	found := project{name: "demo", problem: "broken", stamp: "jackstay.json\x001\x002"}
	now := time.Now()
	mark, news := configNews("s", found, now)
	if !news || mark == "" {
		t.Fatalf("first: %q %v", mark, news)
	}
	if _, news := configNews("s", found, now); !news {
		t.Fatal("a session whose answer was never written counts as told")
	}
	markTold(mark, now)
	if _, news := configNews("s", found, now.Add(toldEvery-time.Second)); news {
		t.Fatal("a session just told is told again")
	}
	if _, news := configNews("s", found, now.Add(toldEvery+time.Second)); !news {
		t.Fatal("a session is not told again after toldEvery")
	}
	changed := found
	changed.stamp = ""
	if _, news := configNews("s", changed, now); !news {
		t.Fatal("a file that changed while read is not told")
	}
}

// An answer that cannot be written takes no mark.
func TestAnUnwrittenAnswerTakesNoMark(t *testing.T) {
	if (reply{message: "m"}).write(failingWriter{}) {
		t.Fatal("a failed write said written")
	}
	if !(reply{}).write(failingWriter{}) {
		t.Fatal("an empty answer is not written")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

// The advice follows trash.keep: an undo only when the project keeps what jst trash removes.
func TestTrashAdviceFollowsTheKeepSetting(t *testing.T) {
	for keep, want := range map[bool]string{false: "which logs what was deleted", true: "which can be undone"} {
		decision := Decide("rm -r src", Settings{Folder: t.TempDir(), TrashKeep: keep, Git: testGit(), Deadline: testBudget})
		if len(decision.Findings) == 0 || !strings.Contains(decision.Findings[0].Instead, want) {
			t.Fatalf("keep %v: %+v", keep, decision.Findings)
		}
	}
	decision := Decide("git reset --hard", Settings{Folder: t.TempDir(), Git: testGit(), Deadline: testBudget})
	if len(decision.Findings) == 0 || strings.Contains(decision.Findings[0].Instead, "restore") {
		t.Fatalf("discard advice with keep off: %+v", decision.Findings)
	}
}
