package landed

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/testrepo"
)

func config(unit string) string {
	return `{"jackstay_schema": 2, "project": "demo", "cards": {"dir": "docs/tasks", "id": "APP-[0-9]{3,}", "unit": "` + unit +
		`", "branch": "main", "since": "2026-03-01", "first_new": "APP-150", "snapshots": "docs/landed"}}`
}

func card(state, commits string) string {
	return "---\n{\"state\": \"" + state + "\", \"commits\": " + commits + "}\n---\n# Card\n"
}

func cardsRun(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errs bytes.Buffer
	code := runCards(args, nil, &out, &errs)
	return out.String(), errs.String(), code
}

func landedOf(t *testing.T, out string) map[string][]string {
	t.Helper()
	var views []cardView
	if err := json.Unmarshal([]byte(out), &views); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	found := map[string][]string{}
	for _, view := range views {
		for _, entry := range view.Landed {
			found[view.Card] = append(found[view.Card], entry.Subject)
		}
	}
	return found
}

func TestFirstParentAndNoMerges(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"jackstay.json": config("pr"), "docs/tasks/APP-150-a.md": card("open", "[]")})
	t.Setenv("GIT_AUTHOR_DATE", "2026-03-02T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-03-02T00:00:00Z")
	repo.Git("checkout", "-q", "-b", "side")
	repo.Commit("side work\n\nCard: APP-150", map[string]string{"s": "1"})
	repo.Git("checkout", "-q", "main")
	repo.Commit("main work\n\nCard: APP-150", map[string]string{"m": "1"})
	repo.Git("merge", "-q", "--no-ff", "-m", "merge side\n\nCard: APP-150", "side")
	out, errs, code := cardsRun(t, "--json")
	if code != 0 {
		t.Fatal(errs)
	}
	if got := strings.Join(landedOf(t, out)["APP-150"], "|"); got != "merge side|main work" {
		t.Errorf("pr reads the first parent: %q", got)
	}
	repo.Write(map[string]string{"jackstay.json": config("commit")})
	out, _, _ = cardsRun(t, "--json")
	got := landedOf(t, out)["APP-150"]
	if len(got) != 2 || !strings.Contains(strings.Join(got, "|"), "side work") || strings.Contains(strings.Join(got, "|"), "merge") {
		t.Errorf("commit reads every non-merge commit: %q", got)
	}
}

func TestBranchChoice(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"jackstay.json": config("pr"), "docs/tasks/APP-150-a.md": card("open", "[]")})
	here := repo2(repo.Root)
	if ref, err := here.resolveBranch("main"); err != nil || ref != "refs/heads/main" {
		t.Errorf("local only: %q %v", ref, err)
	}
	old := repo.Git("rev-parse", "HEAD")
	repo.Git("remote", "add", "origin", repo.Root)
	newer := repo.Commit("ahead", nil)
	repo.Git("update-ref", "refs/remotes/origin/main", newer)
	repo.Git("update-ref", "refs/heads/main", old)
	repo.Git("checkout", "-q", "--detach", old)
	if ref, _ := here.resolveBranch("main"); ref != "refs/remotes/origin/main" {
		t.Errorf("the remote copy ahead: %q", ref)
	}
	repo.Git("update-ref", "refs/heads/main", newer)
	repo.Git("update-ref", "refs/remotes/origin/main", old)
	if ref, _ := here.resolveBranch("main"); ref != "refs/heads/main" {
		t.Errorf("the local branch ahead: %q", ref)
	}
	if _, err := here.resolveBranch("trunk"); err == nil {
		t.Error("no such branch")
	}
}

func repo2(root string) repo { return repo{git: gitDir(root)} }

func TestSnapshotsAndStale(t *testing.T) {
	repo := testrepo.New(t, map[string]string{"jackstay.json": config("pr"),
		"docs/tasks/APP-140-a.md": card("done", `["abc1234..abc9999", "1111111"]`), "docs/tasks/APP-150-b.md": card("open", "[]")})
	first := repo.Git("rev-parse", "HEAD")
	t.Setenv("GIT_AUTHOR_DATE", "2026-03-02T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-03-02T00:00:00Z")
	repo.Commit("work (#9)\n\nCard: APP-150", map[string]string{"w": "1"})
	out, errs, code := cardsRun(t, "snapshot", "--write")
	if code != 0 || !strings.Contains(out, "docs/landed/landed-") {
		t.Fatalf("write: %q %q", out, errs)
	}
	out, _, _ = cardsRun(t, "snapshot", "--write")
	if !strings.Contains(out, ".json") || !strings.Contains(out, "-2.json") {
		t.Errorf("a second snapshot the same day takes the next name: %q", out)
	}
	names, _ := filepath.Glob(filepath.Join(repo.Root, "docs/landed/landed-*.json"))
	if len(names) != 2 {
		t.Fatalf("%v", names)
	}
	relative, _ := filepath.Rel(repo.Root, names[0])
	kept := mustRead(t, names[0])
	if out, _, code := cardsRun(t, "compare", relative); code != 0 || !strings.Contains(out, "every one of the 1 entries") {
		t.Errorf("compare before a rewrite: %q %d", out, code)
	}
	repo.Commit("more\n\nCard: APP-150", map[string]string{"m": "1"})
	if out, _, code := cardsRun(t, "compare", relative); code != 1 || !strings.Contains(out, "1 commits landed") {
		t.Errorf("a commit after the snapshot: %q %d", out, code)
	}
	repo.Git("reset", "-q", "--hard", first)
	repo.Write(map[string]string{relative: kept})
	out, _, code = cardsRun(t, "compare", relative)
	if code != 1 || !strings.Contains(out, "lost: APP-150 landed") {
		t.Errorf("a collapse: %q %d", out, code)
	}
	out, _, _ = cardsRun(t, "--json")
	if got := landedOf(t, out)["APP-150"]; len(got) != 1 || got[0] != "work (#9)" {
		t.Errorf("history joined with the snapshot: %q", got)
	}
	mapFile := filepath.Join(t.TempDir(), "commit-map")
	if err := os.WriteFile(mapFile, []byte("old new\nabc1234ffff 2222222\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code = cardsRun(t, "--stale", "--commit-map", mapFile)
	if code != 0 || !strings.Contains(out, "APP-140\tabc1234..abc9999\tnot on refs/heads/main\tnow 2222222") ||
		!strings.Contains(out, "1111111\tnot on") || !strings.Contains(out, "2 entries") {
		t.Errorf("stale: %q", out)
	}
}

func mustRead(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSnapshotsReadStrictly(t *testing.T) {
	entry := `{"card": "APP-150", "kind": "landed", "author_date": "d", "author": "a", "subject": "s", "pr": 3, "hash_then": "h"}`
	for _, bad := range []string{
		`{"format": "jackstay-cards-snapshot", "entries": []} {"format": "jackstay-cards-snapshot", "entries": [` + entry + `]}`,
		`{"format": "jackstay-cards-snapshot", "entries": [` + entry + `], "entries": []}`,
		`{"format": "jackstay-cards-snapshot", "entries": [1]}`,
		`{"format": "other", "entries": []}`,
	} {
		if _, _, err := readSnapshot(bad, "s.json"); err == nil {
			t.Errorf("%s read", bad)
		}
	}
	found, _, err := readSnapshot(`{"format": "jackstay-cards-snapshot", "extra": 1, "entries": [`+entry+`]}`, "s.json")
	if err != nil || len(found) != 1 || found[0].PR == nil || *found[0].PR != 3 {
		t.Errorf("%v %v", found, err)
	}
}

func TestTruthy(t *testing.T) {
	for _, zero := range []any{nil, false, "", []any{}, map[string]any{}, json.Number("0"), json.Number("-0"), json.Number("0.00"), json.Number("0e0")} {
		if truthy(zero) {
			t.Errorf("%#v", zero)
		}
	}
	for _, some := range []any{true, "x", []any{1}, json.Number("1"), json.Number("0.1")} {
		if !truthy(some) {
			t.Errorf("%#v", some)
		}
	}
}
