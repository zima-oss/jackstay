package owner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
)

type object2 = map[string]any

func askCall(id, timestamp, question string) object2 {
	return object2{"type": "assistant", "timestamp": timestamp, "message": object2{"content": []any{
		object2{"type": "tool_use", "id": id, "name": "AskUserQuestion", "input": object2{"questions": []any{object2{"question": question}}}}}}}
}

func toolResult(id, timestamp string, content any, isError bool) object2 {
	block := object2{"type": "tool_result", "tool_use_id": id, "content": content}
	if isError {
		block["is_error"] = true
	}
	return object2{"type": "user", "timestamp": timestamp, "message": object2{"content": []any{block}}}
}

func typedAt(timestamp, words string) object2 {
	return object2{"type": "user", "timestamp": timestamp, "message": object2{"content": words}}
}

func queuedAt(timestamp, words string) object2 {
	return object2{"type": "queue-operation", "operation": "enqueue", "timestamp": timestamp, "content": words}
}

func copyWith(record object2, key string, value any) object2 {
	duplicate := object2{}
	for name, kept := range record {
		duplicate[name] = kept
	}
	duplicate[key] = value
	return duplicate
}

const queuedWords = "ssh keys for the VM belong in the password manager"

var (
	typed    = typedAt("2026-09-11T08:00:00.000Z", "merge when green")
	queued   = queuedAt("2026-09-11T08:30:00.000Z", queuedWords)
	ask1     = askCall("toolu_ask1", "2026-09-11T08:59:00.000Z", "Rebuild when?")
	widget1  = toolResult("toolu_ask1", "2026-09-11T09:00:00.000Z", `The user answered: "Rebuild when?"="Right after the runner"`, false)
	ask2     = askCall("toolu_ask2", "2026-09-22T13:47:00.000Z", "What do we merge?")
	widget2  = toolResult("toolu_ask2", "2026-09-22T13:47:32.000Z", `Your questions have been answered: "What do we merge?"="Split: the fix first". You can now continue with these answers in mind.`, false)
	declined = toolResult("toolu_ask2", "2026-09-22T13:47:32.000Z", "The user doesn't want to proceed with this tool use.", true)
	bashCall = object2{"type": "assistant", "timestamp": "2026-10-14T06:40:00.000Z", "message": object2{"content": []any{
		object2{"type": "tool_use", "id": "toolu_bash", "name": "Bash", "input": object2{"command": "jst owner --kind widget | tail -1"}}}}}
	bashOut = toolResult("toolu_bash", "2026-10-14T06:40:01.000Z", `[2026-10-14T06:36:43] (widget) Your questions have been answered: "What?"="Split".`, false)
	noise   = []object2{
		{"type": "assistant", "timestamp": "2026-09-11T08:10:00.000Z", "message": object2{"content": []any{object2{"type": "text", "text": "working"}}}},
		typedAt("2026-09-11T08:11:00.000Z", "<task-notification>\n<task-id>x</task-id>\n</task-notification>"),
		{"type": "user", "isMeta": true, "timestamp": "2026-09-11T08:12:00.000Z", "message": object2{"content": "meta"}},
	}
)

func join(parts ...any) []object2 {
	var records []object2
	for _, part := range parts {
		switch value := part.(type) {
		case object2:
			records = append(records, value)
		case []object2:
			records = append(records, value...)
		}
	}
	return records
}

// workspace is a fixture config folder and checkout; the package's two lookups point at it.
type workspace struct {
	t        *testing.T
	config   string
	root     string
	sessions string
}

func newWorkspace(t *testing.T) *workspace {
	t.Helper()
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	space := &workspace{t: t, config: filepath.Join(top, "claude"), root: filepath.Join(top, "work", "some_repo.x")}
	if err := os.MkdirAll(space.root, 0o755); err != nil {
		t.Fatal(err)
	}
	space.sessions = transcriptDir(space.root, space.config)
	savedConfig, savedRoot := configDir, projectRoot
	configDir = func() string { return space.config }
	projectRoot = func() string { return space.root }
	t.Cleanup(func() { configDir, projectRoot = savedConfig, savedRoot })
	return space
}

func (space *workspace) write(name string, records []object2) string {
	space.t.Helper()
	var content strings.Builder
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			space.t.Fatal(err)
		}
		content.Write(encoded)
		content.WriteByte('\n')
	}
	return space.writeRaw(name, content.String())
}

func (space *workspace) writeRaw(name, content string) string {
	space.t.Helper()
	if err := os.MkdirAll(space.sessions, 0o755); err != nil {
		space.t.Fatal(err)
	}
	path := filepath.Join(space.sessions, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		space.t.Fatal(err)
	}
	return path
}

func (space *workspace) fixture(name, as string) {
	space.t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		space.t.Fatal(err)
	}
	space.writeRaw(as, string(content))
}

func (space *workspace) read(records []object2) ([]message, error) {
	space.t.Helper()
	return read(space.write("s.jsonl", records))
}

func kinds(messages []message) []string {
	names := []string{}
	for _, one := range messages {
		names = append(names, one.kind)
	}
	return names
}

func sorted(names []string) []string {
	copied := append([]string{}, names...)
	for outer := range copied {
		for inner := outer + 1; inner < len(copied); inner++ {
			if copied[inner] < copied[outer] {
				copied[outer], copied[inner] = copied[inner], copied[outer]
			}
		}
	}
	return copied
}

func runOwner(args ...string) (code int, stdout, stderr string) {
	var out, errs bytes.Buffer
	code = run(args, strings.NewReader(""), &out, &errs)
	return code, out.String(), errs.String()
}

func codeOf(err error) int {
	if refusal, isFailure := err.(*failure); isFailure {
		return refusal.code
	}
	return -1
}

func TestShapesOfAnOwnerMessage(t *testing.T) {
	declinedBlockWithImage := toolResult("toolu_ask2", "2026-09-22T13:47:32.000Z", []any{
		object2{"type": "image", "source": object2{"type": "base64", "data": "AAAA"}},
		object2{"type": "text", "text": `Your questions have been answered: "What do we merge?"="Yes"`}}, false)
	reminder := object2{"type": "user", "timestamp": "2026-09-11T08:13:00.000Z",
		"message": object2{"content": []any{object2{"type": "text", "text": "<system-reminder>x</system-reminder>"}}}}
	continued := typedAt("2026-09-11T08:14:00.000Z", "This session is being continued from a previous conversation.")
	validation := toolResult("toolu_ask2", "2026-09-22T13:47:32.000Z", "InputValidationError: questions[0].options must have 2-4 items", true)
	resumedWidget := toolResult("toolu_gone", "2026-09-22T13:47:32.000Z", `Your questions have been answered: "What?"="Yes"`, false)
	erroredLeadIn := toolResult("toolu_gone", "2026-09-22T13:47:32.000Z", `The user answered: "What?"="Yes"`, true)

	cases := []struct {
		name    string
		records []object2
		kinds   []string
		code    int // 0 when the transcript reads
	}{
		{"all three shapes", join(typed, queued, ask1, widget1, noise), []string{"queued", "typed", "widget"}, 0},
		{"both spellings of a widget answer", join(ask1, widget1, ask2, widget2, noise), []string{"widget", "widget"}, 0},
		{"the tool's own output pasted back is not an answer", join(typed, bashCall, bashOut), []string{"typed"}, 0},
		{"a declined widget", join(typed, ask2, declined), []string{"declined", "typed"}, 0},
		{"an error the owner never saw is not a decline", join(typed, ask2, validation), []string{"typed"}, 0},
		{"an image before the answer does not hide its lead-in", join(typed, ask2, declinedBlockWithImage), []string{"typed", "widget"}, 0},
		{"a widget record written twice is one answer", join(ask2, widget2, copyWith(widget2, "uuid", "again")), []string{"widget"}, 0},
		{"a declined record written twice is one decline", join(typed, ask2, declined, copyWith(declined, "uuid", "again")), []string{"declined", "typed"}, 0},
		{"an answer whose call is not in the file is still read", join(typed, widget2), []string{"typed", "widget"}, 0},
		{"a lead-in that is an error without its call is not read", join(typed, erroredLeadIn), []string{"typed"}, 0},
		{"a resumed session's answer", join(typed, resumedWidget), []string{"typed", "widget"}, 0},
		{"notifications, meta, reminders and continuations are dropped", join(typed, reminder, continued, noise), []string{"typed"}, 0},
		{"a queued command requeued is one message", join(queued, copyWith(queued, "uuid", "again")), []string{"queued"}, 0},
		{"no owner message is a changed format", join(noise), nil, 5},
		{"an unknown widget spelling is refused", join(typed, ask2, toolResult("toolu_ask2", "2026-09-22T13:47:32.000Z", `Selected: "What?"="This"`, false)), nil, 6},
		{"a spelling without the word answer is refused", join(typed, ask2, toolResult("toolu_ask2", "2026-09-22T13:47:32.000Z", "Picked: yes", false)), nil, 6},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			space := newWorkspace(t)
			messages, err := space.read(test.records)
			if test.code != 0 {
				if codeOf(err) != test.code {
					t.Fatalf("want exit %d, got err %v", test.code, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := sorted(kinds(messages)); !reflect.DeepEqual(got, test.kinds) {
				t.Fatalf("kinds %v, want %v", got, test.kinds)
			}
		})
	}
}

func TestWidgetTextAndDeclinedText(t *testing.T) {
	space := newWorkspace(t)
	messages, err := space.read(join(ask1, widget1, ask2, widget2))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`The user answered: "Rebuild when?"="Right after the runner"`,
		`Your questions have been answered: "What do we merge?"="Split: the fix first".`}
	for index, one := range messages {
		if one.text != want[index] {
			t.Errorf("text %d is %q, want %q (the harness's trailing instruction is dropped)", index, one.text, want[index])
		}
	}
	messages, err = space.read(join(typed, ask2, declined))
	if err != nil {
		t.Fatal(err)
	}
	if messages[1].kind != kindDeclined || messages[1].text != "What do we merge?" {
		t.Errorf("declined message is %+v", messages[1])
	}
}

func TestQueuedAndDeliveredCopyArePairedOnlyWithinFiveSeconds(t *testing.T) {
	delivered := typedAt("2026-09-11T08:30:00.022Z", queuedWords)
	again := queuedAt("2026-09-11T09:30:00.000Z", queuedWords)
	copyOfSecond := typedAt("2026-09-11T09:30:00.020Z", queuedWords)
	later := typedAt("2026-09-11T10:00:00.000Z", queuedWords)
	sixSeconds := typedAt("2026-09-11T08:30:06.000Z", queuedWords)
	fiveSeconds := typedAt("2026-09-11T08:30:05.000Z", queuedWords)
	otherWords := typedAt("2026-09-11T08:30:00.022Z", "something else")
	unreadable := typedAt("later", queuedWords)
	cases := []struct {
		name    string
		records []object2
		want    [][2]string
	}{
		{"one message, queued then typed", join(queued, delivered), [][2]string{{kindTyped, "2026-09-11T08:30:00.022Z"}}},
		{"written in the other order", join(delivered, queued), [][2]string{{kindTyped, "2026-09-11T08:30:00.022Z"}}},
		{"the same words an hour apart are two", join(queued, again), [][2]string{{kindQueued, "2026-09-11T08:30:00.000Z"}, {kindQueued, "2026-09-11T09:30:00.000Z"}}},
		{"a typed copy pairs with the queued one just before it", join(queued, again, copyOfSecond), [][2]string{{kindQueued, "2026-09-11T08:30:00.000Z"}, {kindTyped, "2026-09-11T09:30:00.020Z"}}},
		{"typed much later: another message, the queued one stays", join(queued, later), [][2]string{{kindQueued, "2026-09-11T08:30:00.000Z"}, {kindTyped, "2026-09-11T10:00:00.000Z"}}},
		{"six seconds is two messages", join(queued, sixSeconds), [][2]string{{kindQueued, "2026-09-11T08:30:00.000Z"}, {kindTyped, "2026-09-11T08:30:06.000Z"}}},
		{"five seconds is one", join(queued, fiveSeconds), [][2]string{{kindTyped, "2026-09-11T08:30:05.000Z"}}},
		{"different words are two", join(queued, otherWords), [][2]string{{kindQueued, "2026-09-11T08:30:00.000Z"}, {kindTyped, "2026-09-11T08:30:00.022Z"}}},
		{"a timestamp that cannot be read pairs with nothing", join(queued, unreadable), [][2]string{{kindQueued, "2026-09-11T08:30:00.000Z"}, {kindTyped, "later"}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			space := newWorkspace(t)
			messages, err := space.read(test.records)
			if err != nil {
				t.Fatal(err)
			}
			var got [][2]string
			for _, one := range messages {
				got = append(got, [2]string{one.kind, one.timestamp})
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestMessagesComeOutInTimeOrder(t *testing.T) {
	space := newWorkspace(t)
	messages, err := space.read(join(ask1, widget1, queued, typed))
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(messages); !reflect.DeepEqual(got, []string{kindTyped, kindQueued, kindWidget}) {
		t.Fatalf("order %v", got)
	}
}

func TestSecondsBetween(t *testing.T) {
	gap, ok := secondsBetween("2026-09-11T08:30:00.000Z", "2026-09-11T08:30:01.500Z")
	if !ok || gap != 1500*time.Millisecond {
		t.Fatalf("gap %v ok %v", gap, ok)
	}
	if _, ok := secondsBetween("2026-09-11T08:30:00Z", "2026-09-11T08:30:01.500Z"); ok {
		t.Fatal("a timestamp without milliseconds is unreadable")
	}
}

func TestMangleAndTranscriptDir(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/a/b_c/.claude/d", "-a-b-c--claude-d"},
		{"/a/b", "-a-b"},
		{"/x/проект 1", "-x--------1"},
	}
	for _, test := range cases {
		if got := mangle(test.path); got != test.want {
			t.Errorf("mangle(%q) = %q, want %q", test.path, got, test.want)
		}
	}
	if got := transcriptDir("/a/b", "/h/.claude"); got != "/h/.claude/projects/-a-b" {
		t.Errorf("transcriptDir = %q", got)
	}
}

func TestTheThreeStatesOfTheFolder(t *testing.T) {
	space := newWorkspace(t)
	projects := filepath.Join(space.config, "projects")
	state := func() (string, []string) {
		got, _, names := sessionDirState(space.root, space.config)
		return got, names
	}
	if got, _ := state(); got != stateNone {
		t.Fatalf("no projects folder: %s", got)
	}
	mkdir := func(name string) {
		if err := os.MkdirAll(filepath.Join(projects, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mkdir("-unrelated-checkout")
	if got, _ := state(); got != stateNone {
		t.Fatalf("unrelated folder: %s", got)
	}
	// Sharing only the basename is another checkout, not drift.
	mkdir("-elsewhere-other-some-repo-x")
	if got, _ := state(); got != stateNone {
		t.Fatalf("a shared basename is not drift: %s", got)
	}
	code, out, errs := runOwner()
	if code != 1 || !strings.Contains(errs, "no Claude Code session has run") {
		t.Fatalf("none: code %d out %q err %q", code, out, errs)
	}
	// An older mangling that kept `_` and `.` carries this checkout's parent and name: drift.
	mkdir("-old-layout-work-some_repo.x")
	if got, names := state(); got != stateDrifted || !reflect.DeepEqual(names, []string{"-old-layout-work-some_repo.x"}) {
		t.Fatalf("drift: %s %v", got, names)
	}
	code, out, errs = runOwner()
	if code != 5 || !strings.Contains(errs, "storage layout changed") {
		t.Fatalf("drifted: code %d out %q err %q", code, out, errs)
	}
	space.write("s.jsonl", join(typed))
	if got, _ := state(); got != statePresent {
		t.Fatalf("present: %s", got)
	}
}

func TestAFolderWithNoSessionsIsExit1(t *testing.T) {
	space := newWorkspace(t)
	if err := os.MkdirAll(space.sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runOwner(); code != 1 {
		t.Fatalf("code %d", code)
	}
}

func TestAnEmptyTranscriptIsExit1(t *testing.T) {
	space := newWorkspace(t)
	space.writeRaw("s.jsonl", "")
	if code, _, errs := runOwner(); code != 1 || !strings.Contains(errs, "is empty") {
		t.Fatalf("code %d err %q", code, errs)
	}
}

func TestCommandLineFixtures(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		args     []string
		code     int
		inOut    []string
		inErr    []string
		notInOut []string
	}{
		{"every shape", "basic.jsonl", nil, 0,
			[]string{"=== s.jsonl  (4 of 4 messages)", "(typed) merge when green", "(queued) ssh keys", "(widget) The user answered:", "(declined) What do we merge?",
				"[2026-09-11T08:00:00] (typed)"}, nil, nil},
		{"filters compose", "basic.jsonl", []string{"--kind", "queued", "--grep", "PASSWORD"}, 0,
			[]string{"(1 of 4 messages)", "(queued)"}, nil, []string{"(typed)"}},
		{"matching nothing says so", "basic.jsonl", []string{"--since", "2026-09-11T08:45", "--max-chars", "10"}, 0,
			[]string{"the filters, not the transcript"}, nil, nil},
		{"since keeps the later ones", "basic.jsonl", []string{"--since", "2026-09-22"}, 0,
			[]string{"(1 of 4 messages)", "(declined)"}, nil, []string{"(typed)"}},
		{"max-chars keeps the short ones", "basic.jsonl", []string{"--max-chars", "16"}, 0,
			[]string{"(1 of 4 messages)", "merge when green"}, nil, nil},
		{"no owner message is exit 5", "noise.jsonl", nil, 5, nil, []string{"no owner message"}, nil},
		{"an unknown widget spelling is exit 6", "unknown_spelling.jsonl", nil, 6, nil, []string{"a spelling this tool does not know"}, nil},
		{"stats of a session with no tool calls is exit 5", "noise.jsonl", []string{"--stats"}, 5, nil, []string{"no tool calls"}, nil},
		{"stats counts calls", "basic.jsonl", []string{"--stats"}, 0, []string{"2 tool calls", "      2  AskUserQuestion"}, nil, nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			space := newWorkspace(t)
			space.fixture(test.fixture, "s.jsonl")
			code, out, errs := runOwner(test.args...)
			if code != test.code {
				t.Fatalf("code %d, want %d\nout: %s\nerr: %s", code, test.code, out, errs)
			}
			for _, want := range test.inOut {
				if !strings.Contains(out, want) {
					t.Errorf("stdout lacks %q:\n%s", want, out)
				}
			}
			for _, want := range test.inErr {
				if !strings.Contains(errs, want) {
					t.Errorf("stderr lacks %q:\n%s", want, errs)
				}
			}
			for _, unwanted := range test.notInOut {
				if strings.Contains(out, unwanted) {
					t.Errorf("stdout has %q:\n%s", unwanted, out)
				}
			}
		})
	}
}

func TestSessionPicksTheNewestASubstringOrAll(t *testing.T) {
	space := newWorkspace(t)
	old := space.write("old-111.jsonl", join(typed))
	if err := os.Chtimes(old, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	space.write("new-222.jsonl", join(queued))
	if _, out, _ := runOwner(); !strings.Contains(out, "=== new-222.jsonl") || strings.Contains(out, "old-111") {
		t.Errorf("newest: %s", out)
	}
	if _, out, _ := runOwner("--session", "111"); !strings.Contains(out, "=== old-111.jsonl") {
		t.Errorf("substring: %s", out)
	}
	_, out, _ := runOwner("--session", "all")
	if strings.Index(out, "new-222") > strings.Index(out, "old-111") || !strings.Contains(out, "old-111") {
		t.Errorf("all, newest first: %s", out)
	}
	if code, _, _ := runOwner("--session", "999"); code != 1 {
		t.Errorf("no match: code %d", code)
	}
}

func TestStatsCountsToolCallsAndBashPatterns(t *testing.T) {
	space := newWorkspace(t)
	space.write("s.jsonl", join(typed, ask1, widget1, bashCall, object2{"type": "assistant", "message": object2{"content": []any{
		object2{"type": "tool_use", "name": "Bash", "input": object2{"command": "gh pr checks 7; sleep 30"}}}}}))
	code, out, errs := runOwner("--stats")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	for _, want := range []string{"3 tool calls", "      2  Bash", "      1  AskUserQuestion", "2 Bash commands, by pattern", "      1  gh pr checks", "      1  waiting (sleep/until)"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "  Bash") > strings.Index(out, "  AskUserQuestion") {
		t.Errorf("the most used tool comes first:\n%s", out)
	}
}

func TestStatsWithoutToolCallsIsAChangedFormat(t *testing.T) {
	space := newWorkspace(t)
	space.write("s.jsonl", join(typed))
	if code, _, _ := runOwner("--stats"); code != 5 {
		t.Fatalf("code %d", code)
	}
}

func TestBadRequestsAreExit2(t *testing.T) {
	space := newWorkspace(t)
	space.write("s.jsonl", join(typed))
	cases := [][]string{
		{"--stats", "--kind", "typed"},
		{"--stats", "--grep", "x"},
		{"--stats", "--since", "2026"},
		{"--stats", "--max-chars", "3"},
		{"--kind", "nope"},
		{"--grep", "("},
		{"--grep", `(?=lookahead)x`}, // a lookahead, refused by RE2 (W-036)
		{"--max-chars", "-1"},
		{"--max-chars", "many"},
		{"--nope"},
		{"stray"},
	}
	for _, args := range cases {
		if code, _, errs := runOwner(args...); code != 2 {
			t.Errorf("%v: code %d err %q", args, code, errs)
		}
	}
	if code, out, _ := runOwner("--help"); code != 0 || !strings.Contains(out, "usage: jst owner") {
		t.Errorf("--help: code %d out %q", code, out)
	}
}

func TestGrepIsCaseInsensitive(t *testing.T) {
	space := newWorkspace(t)
	space.write("s.jsonl", join(typed, queued))
	if _, out, _ := runOwner("--grep", "MERGE WHEN"); !strings.Contains(out, "(1 of 2 messages)") {
		t.Errorf("out: %s", out)
	}
}

func TestOneUnreadableLineIsSkippedAndCounted(t *testing.T) {
	space := newWorkspace(t)
	space.writeRaw("s.jsonl", "not json\n[1,2]\n"+`{"type":"user","timestamp":"2026-09-11T08:00:00.000Z","message":{"content":"hello"}}`)
	if _, out, _ := runOwner(); !strings.Contains(out, "(1 of 1 messages)") {
		t.Errorf("out: %s", out)
	}
	if code, _, errs := runOwner("--stats"); code != 5 || !strings.Contains(errs, "3 records") {
		t.Errorf("stats: code %d err %q", code, errs)
	}
}

func TestRegisteredAsOwner(t *testing.T) {
	entry, words, found := cli.Find([]string{"owner", "--stats"})
	if !found || words != 1 || entry.Name != "owner" || entry.Run == nil {
		t.Fatalf("owner is not registered: %+v %d %v", entry, words, found)
	}
}
