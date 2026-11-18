package baseline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/testrepo"
)

// The span every test measures: 2026-11-07..2026-11-13 (UTC). Nothing reads a clock.
const (
	since = "2026-11-07"
	until = "2026-11-13"
)

func at(t *testing.T, text string) float64 {
	t.Helper()
	moment, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatal(err)
	}
	return float64(moment.Unix())
}

func iso(seconds float64) string {
	whole := int64(seconds)
	return time.Unix(whole, int64((seconds-float64(whole))*1e9)).UTC().Format("2006-01-02T15:04:05.000Z")
}

func use(id string, when float64, command string, background bool) map[string]any {
	given := map[string]any{"command": command}
	if background {
		given["run_in_background"] = true
	}
	return map[string]any{"type": "assistant", "timestamp": iso(when), "cwd": "/Users/someone/proj",
		"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": given}}}}
}

func answer(id string, when float64, isError bool, text string, more map[string]any) map[string]any {
	record := map[string]any{"type": "user", "timestamp": iso(when),
		"message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": isError, "content": text}}}}
	for key, value := range more {
		record[key] = value
	}
	return record
}

func notified(id string, when float64, status string, asUser bool) map[string]any {
	text := "<task-notification>\n<task-id>b1</task-id>\n<tool-use-id>" + id + "</tool-use-id>\n<status>" + status +
		"</status>\n<summary>Background command \"x\" " + status + " (exit code 0)</summary>\n</task-notification>"
	if asUser {
		return map[string]any{"type": "user", "timestamp": iso(when), "message": map[string]any{"content": text}}
	}
	return map[string]any{"type": "queue-operation", "timestamp": iso(when), "content": text}
}

// ran is a foreground call answered after took seconds.
func ran(id string, start, took float64, command string) []map[string]any {
	return []map[string]any{use(id, start, command, false), answer(id, start+took, false, "", nil)}
}

func writeLines(t *testing.T, place string, records []map[string]any, extra string) {
	t.Helper()
	var built strings.Builder
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		built.Write(line)
		built.WriteByte('\n')
	}
	built.WriteString(extra)
	if err := os.MkdirAll(filepath.Dir(place), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(place, []byte(built.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// kinds are a test project's kinds (RE2).
func kinds() []Kind {
	return []Kind{
		{Name: "whole", Pattern: regexp.MustCompile(`^make( -C \S+| -\S+| [A-Z_]+=\S*)* test( [A-Z_]+=\S*)*$`), Gate: true},
		{Name: "changed", Pattern: regexp.MustCompile(`^make( -C \S+)* test-changed\b`), Gate: true},
		{Name: "focused", Pattern: regexp.MustCompile(`^swift test\b.*--filter\b|^make test-feature\b`)},
		{Name: "gate", Pattern: regexp.MustCompile(`^just gate$`), Gate: true},
	}
}

// fixture is a repository with dated commits on main, a projects folder and a log path.
type fixture struct {
	repo     *testrepo.Repo
	projects string
	log      string
}

func newFixture(t *testing.T, subjects map[string]string) *fixture {
	t.Helper()
	repo := testrepo.New(t, nil)
	for subject, date := range subjects {
		t.Setenv("GIT_COMMITTER_DATE", date)
		t.Setenv("GIT_AUTHOR_DATE", date)
		repo.Commit(subject, nil)
	}
	return &fixture{repo: repo, projects: filepath.Join(repo.Home, "projects"), log: filepath.Join(repo.Home, "runs.jsonl")}
}

func (found *fixture) spec(project string) Definition {
	return Definition{Project: project, Kinds: kinds(), MergeUnit: "pr", Branch: "main", Transcripts: []string{project + "-main", project + "-wt-*"},
		Source: "none"}
}

func (found *fixture) measure(t *testing.T, spec Definition, also ...Definition) Report {
	t.Helper()
	span, err := SpanOf(since, until)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Measure(spec, span, Inputs{Repo: found.repo.Root, ProjectsDir: found.projects, Also: also, LogPaths: []string{found.log},
		LogGiven: true})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func value[Value any](t *testing.T, pointer *Value) Value {
	t.Helper()
	if pointer == nil {
		t.Fatal("a value is missing")
	}
	return *pointer
}
