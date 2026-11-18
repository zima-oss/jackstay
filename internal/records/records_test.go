package records

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/testrepo"
)

const config = `{"jackstay_schema": 2, "project": "demo",
  "cards": {"dir": "docs/tasks", "id": "APP-[0-9]{3,}", "unit": "pr", "branch": "main", "since": "2026-01-01"},
  "ids": {"CHK": {"files": "docs/checks/CHK-{n}-*.md", "width": 3}},
  "records": {"CHK": {"dir": "docs/checks", "template": "docs/t/check.md", "columns": ["id", "status", "card"], "before": "docs/ledger.md"}}}`

const template = "---\n{\"id\": \"\", \"card\": null, \"status\": \"open\"}\n---\n# What was checked\n"

func call(t *testing.T, runner func([]string, any, *bytes.Buffer, *bytes.Buffer) int, args ...string) (string, string, int) {
	t.Helper()
	var out, errs bytes.Buffer
	code := runner(args, nil, &out, &errs)
	return out.String(), errs.String(), code
}

func newCmd(args []string, _ any, out, errs *bytes.Buffer) int  { return runNew(args, nil, out, errs) }
func viewCmd(args []string, _ any, out, errs *bytes.Buffer) int { return runView(args, nil, out, errs) }

func TestNewAndView(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"jackstay.json": config, "docs/t/check.md": template, "docs/checks/CHK-004-old.md": template})
	out, errs, code := call(t, newCmd, "CHK", "--slug", "login", "--card", "APP-140", "--set", "status=passed", "--set", "who=me")
	if code != 0 || strings.TrimSpace(out) != "docs/checks/CHK-005-login.md" {
		t.Fatalf("new: %q %q %d", out, errs, code)
	}
	written, _ := os.ReadFile(filepath.Join(repo.Root, "docs/checks/CHK-005-login.md"))
	want := "---\n{\"id\": \"CHK-005\", \"card\": \"APP-140\", \"status\": \"passed\", \"who\": \"me\"}\n---\n# What was checked\n"
	if string(written) != want {
		t.Errorf("written:\n%s", written)
	}
	t.Chdir(filepath.Join(repo.Root, "docs"))
	if out, _, _ := call(t, newCmd, "CHK", "--slug", "two"); strings.TrimSpace(out) != "checks/CHK-006-two.md" {
		t.Errorf("relative to the working folder: %q", out)
	}
	t.Chdir(repo.Root)
	out, errs, code = call(t, viewCmd, "CHK")
	wantTable := "| id | status | card | summary |\n|---|---|---|---|\n|  | open |  | What was checked |\n" +
		"| CHK-005 | passed | APP-140 | What was checked |\n| CHK-006 | open |  | What was checked |\n\n(older rows: docs/ledger.md, frozen; not read here)\n"
	if code != 0 || out != wantTable {
		t.Errorf("table: %q %q %d", out, errs, code)
	}
	out, _, code = call(t, viewCmd, "CHK", "--where", "status=passed", "--format", "json")
	var rows []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0]["file"] != "docs/checks/CHK-005-login.md" ||
		rows[0]["summary"] != "What was checked" || rows[0]["who"] != "me" {
		t.Errorf("json: %q", out)
	}
	out, _, _ = call(t, viewCmd, "CHK", "--where", "card=APP-140", "--format", "list")
	if out != "CHK-005  passed  APP-140  What was checked  (docs/checks/CHK-005-login.md)\n\n(older rows: docs/ledger.md, frozen; not read here)\n" {
		t.Errorf("list: %q", out)
	}
	repo.Write(map[string]string{"docs/checks/CHK-009-bad.md": "no front matter"})
	out, errs, code = call(t, viewCmd, "CHK")
	if code != 1 || !strings.Contains(out, "CHK-006") || !strings.Contains(errs, "CHK-009-bad.md") {
		t.Errorf("an unreadable row: %q %q %d", out, errs, code)
	}
}

func TestRefusals(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"jackstay.json": config, "docs/t/check.md": template, "docs/t/bad.md": "x", "docs/checks/.keep": ""})
	for _, args := range [][]string{{}, {"CHK"}, {"CHK", "--slug", "Bad"}, {"CHK", "--slug", "a", "--slug", "b"}, {"CHK", "--slug", "a", "--card", "DEV-1"},
		{"CHK", "--slug", "a", "--set", "id=x"}, {"CHK", "--slug", "a", "--set", "novalue"}, {"NOPE", "--slug", "a"}, {"CHK", "--slug", "a", "--other", "x"}} {
		if out, errs, code := call(t, newCmd, args...); code != 2 || out != "" || errs == "" {
			t.Errorf("new %v: %q %q %d", args, out, errs, code)
		}
	}
	for _, args := range [][]string{{}, {"CHK", "--format", "csv"}, {"CHK", "--where", "x"}, {"NOPE"}} {
		if out, errs, code := call(t, viewCmd, args...); code != 2 || out != "" || errs == "" {
			t.Errorf("view %v: %q %q %d", args, out, errs, code)
		}
	}
	// No number is spent on a request that cannot be made.
	if _, err := os.Stat(filepath.Join(repo.Home, "demo", "ids", "LC.jsonl")); err == nil {
		t.Error("a refused request took a number")
	}
	repo.Write(map[string]string{"jackstay.json": strings.Replace(config, "docs/t/check.md", "docs/t/bad.md", 1)})
	if _, errs, code := call(t, newCmd, "CHK", "--slug", "a"); code != 2 || !strings.Contains(errs, "front matter") {
		t.Errorf("a bad template: %q %d", errs, code)
	}
	repo.Write(map[string]string{"jackstay.json": strings.Replace(config, "docs/t/check.md", "docs/t/none.md", 1)})
	if _, errs, code := call(t, newCmd, "CHK", "--slug", "a"); code != 2 || !strings.Contains(errs, "not there") {
		t.Errorf("no template: %q %d", errs, code)
	}
	repo.Write(map[string]string{"jackstay.json": config})
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(repo.Root, "docs/checks")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo.Root, "docs/checks")); err != nil {
		t.Fatal(err)
	}
	if _, errs, code := call(t, newCmd, "CHK", "--slug", "a"); code != 2 || !strings.Contains(errs, "leads out") {
		t.Errorf("a folder leading out: %q %d", errs, code)
	}
	if err := os.Remove(filepath.Join(repo.Root, "docs/checks")); err != nil {
		t.Fatal(err)
	}
	if out, _, code := call(t, viewCmd, "CHK"); code != 0 || !strings.HasPrefix(out, "| id |") {
		t.Errorf("no records yet is an empty table: %q %d", out, code)
	}
}
