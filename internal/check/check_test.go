package check

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/homedir"
)

func TestCheck(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	folder := t.TempDir()
	write := func(text string) {
		if err := os.WriteFile(filepath.Join(folder, "jackstay.json"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	call := func(args ...string) (string, string, int) {
		var out, errs bytes.Buffer
		code := run(args, nil, &out, &errs)
		return out.String(), errs.String(), code
	}
	if _, errs, code := call(folder); code != 2 || !strings.Contains(errs, "no jackstay.json") {
		t.Errorf("no file: %q %d", errs, code)
	}
	write(`{"jackstay_schema": 2, "procs": {"default_minutes": 50, "max_minutes": 10}, "ids": {"A": {"files": "x.md"}}}`)
	if _, errs, code := call(folder); code != 2 || !strings.Contains(errs, "procs.default_minutes") || !strings.Contains(errs, "ids.A") {
		t.Errorf("every problem named: %q %d", errs, code)
	}
	write(`{"jackstay_schema": 2, "project": "demo", "receipts": {"toolchain": []}, "test": {"scopes": {"full": {"run": ["make"]}, "gate": {"steps": ["slow", "fast"]}},
	  "steps": {"slow": {"run": ["a"], "timeout": 1}, "fast": {"run": ["b"], "timeout": 1}}}}`)
	runs, err := homedir.SharedPath("runs.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []map[string]any{
		{"event": "run", "project": "demo", "step": "slow", "seconds": 100.0, "outcome": "passed"},
		{"event": "run", "project": "demo", "step": "slow", "seconds": 120.0, "outcome": "failed"},
		{"event": "run", "project": "demo", "step": "fast", "seconds": 2.0, "outcome": "passed"},
		{"event": "run", "project": "other", "step": "slow", "seconds": 1.0, "outcome": "passed"},
	} {
		homedir.AppendLine(runs, record)
	}
	out, errs, code := call(folder)
	if code != 0 || !strings.Contains(out, "`test.scopes.full` has no timeout") ||
		!strings.Contains(out, "the step slow (median 110 s) comes before fast (median 2.0 s)") {
		t.Errorf("warnings: %q %q %d", out, errs, code)
	}
	if _, _, code := call(folder, folder); code != 2 {
		t.Error("two folders")
	}
}

func TestLeaves(t *testing.T) {
	section := map[string]any{
		"steps":  map[string]any{"a": map[string]any{"run": []any{"x"}}, "t": map[string]any{"run": []any{"x", "{tests}"}}, "s": map[string]any{"run": []any{"${tests}"}}},
		"scopes": map[string]any{"g": map[string]any{"steps": []any{"a", "h"}}, "h": map[string]any{"steps": []any{"s", "a"}}, "c": map[string]any{"steps": []any{"c"}}, "u": map[string]any{"steps": []any{"t"}}},
	}
	if got, ok := leaves(section, "g", nil); !ok || strings.Join(got, ",") != "a,s,a" {
		t.Errorf("%v %v", got, ok)
	}
	for _, bad := range []string{"c", "u", "nope"} {
		if _, ok := leaves(section, bad, nil); ok {
			t.Errorf("%s expanded", bad)
		}
	}
}

func TestReleaseValidator(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	folder := t.TempDir()
	if err := exec.Command("git", "init", "-q", folder).Run(); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"jackstay.json":  `{"jackstay_schema": 2, "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version}"}}`,
		"changes/fix.md": "---\n{\"card\": \"APP-1\"}\n---\nFixed it\n",
	}
	for name, text := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(folder, name)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errs bytes.Buffer
	if code := run([]string{folder}, nil, &out, &errs); code != 2 || !strings.Contains(errs.String(), "changes/fix.md") {
		t.Errorf("an invalid entry: %q %q %d", out.String(), errs.String(), code)
	}
	if err := os.WriteFile(filepath.Join(folder, "changes/fix.md"), []byte("Fixed it\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errs.Reset()
	if code := run([]string{folder}, nil, &out, &errs); code != 0 {
		t.Errorf("a valid entry: %q %d", errs.String(), code)
	}
}
