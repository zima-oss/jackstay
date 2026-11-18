package ids

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/zima-oss/jackstay/internal/testrepo"
)

const config = `{"jackstay_schema": 2, "project": "demo", "ids": {
  "APP": {"files": "docs/tasks/APP-{n}-*.md", "width": 3},
  "CHK": {"files": "docs/checks/CHK-{n}-*.md", "width": 3, "also_in": ["docs/ledger.md"]}}}`

func jackstay(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errs bytes.Buffer
	code := run(args, nil, &out, &errs)
	return out.String(), errs.String(), code
}

func TestNextSeesEverything(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"jackstay.json": config, "docs/tasks/APP-003-a.md": "", "docs/ledger.md": "| CHK-041 | x |\n"})
	if out, errs, code := jackstay(t, "id", "peek", "APP"); code != 2 || out != "" || !strings.Contains(errs, "usage") {
		t.Fatalf("an unknown verb: %q %q %d", out, errs, code)
	}
	cases := []struct {
		name string
		make func()
		want string
	}{
		{"committed file", func() {}, "APP-004"},
		{"untracked file", func() { repo.Write(map[string]string{"docs/tasks/APP-005.md": ""}) }, "APP-006"},
		{"another worktree", func() {
			other := filepath.Join(filepath.Dir(repo.Root), "other")
			repo.Git("worktree", "add", "-q", "-b", "w", other)
			if err := os.WriteFile(filepath.Join(other, "docs/tasks/APP-010-w.md"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "APP-011"},
		{"a branch", func() {
			repo.Git("checkout", "-q", "-b", "b")
			repo.Commit("b", map[string]string{"docs/tasks/APP-020-b.md": ""})
			repo.Git("checkout", "-q", "main")
			_ = os.Remove(filepath.Join(repo.Root, "docs/tasks/APP-020-b.md"))
		}, "APP-021"},
		{"a remote-tracking ref", func() {
			repo.Git("checkout", "-q", "-b", "r")
			commit := repo.Commit("r", map[string]string{"docs/tasks/APP-030-r.md": ""})
			repo.Git("checkout", "-q", "main")
			repo.Git("branch", "-q", "-D", "r")
			repo.Git("update-ref", "refs/remotes/origin/r", commit)
		}, "APP-031"},
	}
	for _, c := range cases {
		c.make()
		out, errs, code := jackstay(t, "peek", "APP")
		if code != 0 || strings.TrimSpace(out) != c.want {
			t.Errorf("%s: %q %q %d, want %s", c.name, out, errs, code, c.want)
		}
	}
	out, _, code := jackstay(t, "next", "APP", "--count", "2", "--json")
	var answer struct {
		Series string   `json:"series"`
		IDs    []string `json:"ids"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &answer) != nil || strings.Join(answer.IDs, ",") != "APP-031,APP-032" {
		t.Fatalf("next --count 2: %q %d", out, code)
	}
	if out, _, _ := jackstay(t, "next", "APP"); strings.TrimSpace(out) != "APP-033" {
		t.Errorf("after its own record: %q", out)
	}
	if out, _, _ := jackstay(t, "next", "CHK"); strings.TrimSpace(out) != "CHK-042" {
		t.Errorf("also_in: %q", out)
	}
	out, _, code = jackstay(t, "list", "APP", "--json")
	var rows []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 3 || rows[0]["file"] != false || rows[0]["session"] != nil {
		t.Errorf("list: %q", out)
	}
	repo.Write(map[string]string{"docs/tasks/APP-031-made.md": ""})
	if out, _, _ := jackstay(t, "list", "APP", "--json"); !strings.Contains(out, `"file": true`) {
		t.Errorf("list after a file: %q", out)
	}
}

func TestRefusals(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"jackstay.json": config})
	for _, args := range [][]string{{}, {"next"}, {"next", "APP", "--count", "0"}, {"next", "APP", "--count", "101"},
		{"next", "APP", "--count"}, {"next", "NOPE"}, {"peek"}, {"list", "a", "b"}, {"next", "CHK"}} {
		out, errs, code := jackstay(t, args...)
		if code != 2 || out != "" || errs == "" {
			t.Errorf("%v: %q %q %d", args, out, errs, code)
		}
	}
	record := filepath.Join(repo.Home, "demo", "ids", "APP.jsonl")
	if _, _, code := jackstay(t, "next", "APP"); code != 0 {
		t.Fatal("first number")
	}
	data, _ := os.ReadFile(record)
	if err := os.WriteFile(record, append(data, []byte("{\"number\": true}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, errs, code := jackstay(t, "next", "APP"); code != 2 || out != "" || !strings.Contains(errs, "line 2") {
		t.Errorf("a damaged record: %q %q %d", out, errs, code)
	}
	if err := os.WriteFile(record, data, 0o600); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(filepath.Dir(repo.Root), "gone")
	repo.Git("worktree", "add", "-q", "-b", "g", gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if out, errs, code := jackstay(t, "next", "APP"); code != 2 || out != "" || !strings.Contains(errs, "worktree prune") {
		t.Errorf("a worktree gone: %q %q %d", out, errs, code)
	}
	repo.Git("worktree", "prune")
	folder := filepath.Join(repo.Home, "demo", "ids")
	if err := os.Chmod(record, 0o400); err != nil {
		t.Fatal(err)
	}
	if out, errs, code := jackstay(t, "next", "APP"); code != 2 || out != "" || !strings.Contains(errs, "no number was handed out") {
		t.Errorf("a record that cannot be written: %q %q %d", out, errs, code)
	}
	_ = os.Chmod(record, 0o600)
	_ = folder
}

func TestConcurrentTakers(t *testing.T) {
	testrepo.New(t, map[string]string{"jackstay.json": config})
	var group sync.WaitGroup
	var mutex sync.Mutex
	var all []string
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 5 {
				var out, errs bytes.Buffer
				if code := run([]string{"next", "APP"}, nil, &out, &errs); code != 0 {
					t.Error(errs.String())
					return
				}
				mutex.Lock()
				all = append(all, strings.TrimSpace(out.String()))
				mutex.Unlock()
			}
		}()
	}
	group.Wait()
	sort.Strings(all)
	if len(all) != 40 || all[0] != "APP-001" || all[39] != "APP-040" {
		t.Errorf("%v", all)
	}
	for index := 1; index < len(all); index++ {
		if all[index] == all[index-1] {
			t.Errorf("%s handed out twice", all[index])
		}
	}
}

func TestStrictRecordsBoundsAndPaths(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"jackstay.json": config, "docs/ledger.md": ""})
	record := filepath.Join(repo.Home, "demo", "ids", "APP.jsonl")
	if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{`{"number": 1}{"number": 100}`, `{"number": 1} x`, `{"number": 1, "number": 100}`, `{"number": 1.0}`,
		`{"number": "1"}`, `{"number": 10000000000000}`, `[1]`} {
		if err := os.WriteFile(record, []byte(line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, _, code := jackstay(t, "next", "APP"); code != 2 || out != "" {
			t.Errorf("%s: %q %d", line, out, code)
		}
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	repo.Write(map[string]string{"docs/tasks/APP-999999999999-x.md": ""})
	if out, _, code := jackstay(t, "next", "APP", "--count", "2"); code != 2 || out != "" {
		t.Errorf("past the bound: %q %d", out, code)
	}
	if out, _, code := jackstay(t, "next", "APP"); code != 0 || strings.TrimSpace(out) != "APP-1000000000000" {
		t.Errorf("at the bound: %q %d", out, code)
	}
	repo.Write(map[string]string{"docs/tasks/APP-9223372036854775807-x.md": ""})
	if out, _, code := jackstay(t, "next", "APP"); code != 2 || out != "" {
		t.Errorf("a number Go cannot hold: %q %d", out, code)
	}
	if err := os.Remove(filepath.Join(repo.Root, "docs/tasks/APP-9223372036854775807-x.md")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "ledger.md"), []byte("CHK-500\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(repo.Root, "docs/ledger.md"))
	if err := os.Symlink(filepath.Join(outside, "ledger.md"), filepath.Join(repo.Root, "docs/ledger.md")); err != nil {
		t.Fatal(err)
	}
	if out, errs, code := jackstay(t, "next", "CHK"); code != 2 || !strings.Contains(errs, "leads out") {
		t.Errorf("also_in leading out: %q %q %d", out, errs, code)
	}
	_ = os.RemoveAll(filepath.Join(repo.Root, "docs/tasks"))
	if err := os.Symlink(outside, filepath.Join(repo.Root, "docs/tasks")); err != nil {
		t.Fatal(err)
	}
	if out, errs, code := jackstay(t, "next", "APP"); code != 2 || !strings.Contains(errs, "leads out") {
		t.Errorf("a series folder leading out: %q %q %d", out, errs, code)
	}
}

// TestLockAcrossProcesses runs takers in other processes (this test binary again), each taking the
// same flock on the same file.
func TestLockAcrossProcesses(t *testing.T) {
	if os.Getenv("JACKSTAY_IDS_TAKER") != "" {
		var out, errs bytes.Buffer
		for range 10 {
			out.Reset()
			if code := run([]string{"next", "APP"}, nil, &out, &errs); code != 0 {
				t.Fatal(errs.String())
			}
			fmt.Fprint(os.Stdout, "TAKEN "+out.String())
		}
		return
	}
	repo := testrepo.New(t, map[string]string{"jackstay.json": config})
	var group sync.WaitGroup
	outputs := make([][]byte, 4)
	for index := range outputs {
		group.Add(1)
		go func() {
			defer group.Done()
			command := exec.Command(os.Args[0], "-test.run", "^TestLockAcrossProcesses$")
			command.Dir = repo.Root
			command.Env = append(os.Environ(), "JACKSTAY_IDS_TAKER=1")
			outputs[index], _ = command.CombinedOutput()
		}()
	}
	group.Wait()
	var all []string
	for _, output := range outputs {
		for _, line := range strings.Split(string(output), "\n") {
			if id, ok := strings.CutPrefix(line, "TAKEN "); ok {
				all = append(all, id)
			}
		}
	}
	sort.Strings(all)
	if len(all) != 40 || all[0] != "APP-001" || all[39] != "APP-040" {
		t.Fatalf("%v", all)
	}
	recorded, err := os.ReadFile(filepath.Join(repo.Home, "demo", "ids", "APP.jsonl"))
	if err != nil || strings.Count(string(recorded), "\n") != 40 {
		t.Errorf("every number printed was recorded: %v", err)
	}
}
