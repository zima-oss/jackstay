//go:build integration

package changes

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/delivery"
)

func TestBlobIDMatchesGit(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello\n")
	if err := os.WriteFile(filepath.Join(dir, "f"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "hash-object", filepath.Join(dir, "f")).Output()
	if err != nil {
		t.Skip("no git")
	}
	if got := BlobID("sha1", data); got != strings.TrimSpace(string(out)) {
		t.Fatalf("got %s want %s", got, out)
	}
}

// repo makes a git repository with a jackstay.json and returns its project.

func repo(t *testing.T, jackstayJSON string, files map[string]string) Project {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-01-02T03:04:05Z", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	files["jackstay.json"] = jackstayJSON
	for name, text := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", "start")
	project, err := LoadProjectAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

const stageOneJSON = `{"jackstay_schema": 2, "project": "p", "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version} — {date}"}}`

const releaseJSON = `{"jackstay_schema": 2, "project": "p",
 "cards": {"dir": "docs/cards", "id": "P-[0-9]+", "unit": "pr", "since": "2026-01-01"},
 "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version} — {date}", "newest": "first"},
 "release": {"first": "0.1.0", "files": [{"path": "VERSION", "match": "^(?P<version>[0-9]+\\.[0-9]+\\.[0-9]+)$"}]}}`

func TestNewStageOne(t *testing.T) {
	project := repo(t, stageOneJSON, map[string]string{})
	written, err := New(project, NewRequest{Slug: "a-fix"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(written)
	if string(data) != "- a fix\n" {
		t.Fatalf("got %q", data)
	}
	if _, err := New(project, NewRequest{Slug: "a-fix"}); err == nil || exitCode(err) != 2 {
		t.Fatalf("second: %v", err)
	}
	if _, err := New(project, NewRequest{Slug: "Bad Slug"}); exitCode(err) != 2 {
		t.Fatalf("bad slug: %v", err)
	}
}

func TestNewTyped(t *testing.T) {
	project := repo(t, releaseJSON, map[string]string{"VERSION": "0.1.0\n", "docs/cards/P-1-x.md": "card"})
	if _, err := New(project, NewRequest{Slug: "x", Type: "fix"}); exitCode(err) != 2 {
		t.Fatalf("missing flags: %v", err)
	}
	if _, err := New(project, NewRequest{Slug: "x", Type: "breaking", Audience: "consumer", Title: "T"}); exitCode(err) != 2 {
		t.Fatalf("breaking without migration: %v", err)
	}
	if _, err := New(project, NewRequest{Slug: "x", Type: "fix", Audience: "consumer", Title: "T", Card: "Q-1"}); exitCode(err) != 2 {
		t.Fatalf("bad card: %v", err)
	}
	written, err := New(project, NewRequest{Slug: "x", Type: "breaking", Audience: "consumer", Title: "T", Migration: "M", Card: "P-1"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(written)
	e, problems := Parse("x.md", data)
	if len(problems) > 0 || e.Migration != "M" || e.Card != "P-1" {
		t.Fatalf("got %+v %q\n%s", e, problems, data)
	}
	problemsAll, count, err := Check(project)
	if err != nil || count != 1 || len(problemsAll) != 0 {
		t.Fatalf("check: %v %d %q", err, count, problemsAll)
	}
}

func TestCheckUntypedInReleaseProject(t *testing.T) {
	project := repo(t, releaseJSON, map[string]string{"VERSION": "0.1.0\n", "changes/old.md": "- untyped\n", "changes/README.md": "about"})
	problems, count, err := Check(project)
	if err != nil || count != 1 || len(problems) != 1 || !strings.Contains(problems[0], "front matter") {
		t.Fatalf("got %v %d %q", err, count, problems)
	}
}

func TestCollectStageOne(t *testing.T) {
	project := repo(t, stageOneJSON, map[string]string{"changes/b.md": "- b\n", "changes/a.md": "- a\n", "CHANGELOG.md": "# Changelog\n"})
	var out, errOut bytes.Buffer
	code, err := Collect(project, "1.0.0", "2026-03-04", false, &out, &errOut)
	if err != nil || code != 0 {
		t.Fatalf("code %d err %v %s", code, err, errOut.String())
	}
	data, _ := os.ReadFile(filepath.Join(project.Root, "CHANGELOG.md"))
	if string(data) != "# Changelog\n\n## 1.0.0 — 2026-03-04\n\n- a\n\n- b\n" {
		t.Fatalf("got %q", data)
	}
	if _, err := os.Stat(filepath.Join(project.Root, "changes/a.md")); !os.IsNotExist(err) {
		t.Fatalf("entry kept: %v", err)
	}
	// A second collect with no entries and the heading there: nothing left, exit 1.
	code, _ = Collect(project, "1.0.0", "2026-03-05", false, &out, &errOut)
	if code != 1 {
		t.Fatalf("rerun with nothing: %d", code)
	}
}

func TestCollectFinishesAStoppedRun(t *testing.T) {
	project := repo(t, stageOneJSON, map[string]string{"changes/a.md": "- a\n",
		"CHANGELOG.md": "# Changelog\n"})
	indexLock := filepath.Join(project.Root, ".git", "index.lock")
	if err := os.WriteFile(indexLock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code, err := Collect(project, "1.0.0", "2026-03-04", false, &out, &errOut)
	if err == nil || code != 2 || !strings.Contains(err.Error(), "index.lock") {
		t.Fatalf("expected to stop at git rm: code %d, error %v, output %s", code, err, &out)
	}
	planPath := filepath.Join(project.Root, ".git", "jackstay-changes.json")
	first, _, err := delivery.Read[collectPlan](planPath)
	if err != nil || first.Attempt != 1 || first.Phase != "remove-entries" || first.Outcome != delivery.Interrupted {
		t.Fatalf("expected interrupted removal record, got %+v (%v)", first, err)
	}
	before, err := os.ReadFile(filepath.Join(project.Root, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(indexLock); err != nil {
		t.Fatal(err)
	}
	if err := WriteInRepo(project.Root, "changes/new.md", []byte("- a\n")); err != nil {
		t.Fatal(err)
	}
	code, err = Collect(project, "1.0.0", "2026-03-09", false, &out, &errOut)
	if err != nil || code != 0 {
		t.Fatalf("code %d %v %s %s", code, err, &out, &errOut)
	}
	if _, err := os.Stat(filepath.Join(project.Root, "changes/a.md")); !os.IsNotExist(err) {
		t.Fatalf("collected entry was kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project.Root, "changes/new.md")); err != nil {
		t.Fatalf("unrelated entry was removed: %v", err)
	}
	second, _, err := delivery.Read[collectPlan](planPath)
	if err != nil || second.ID != first.ID || second.Attempt != 2 || second.Outcome != delivery.Complete {
		t.Fatalf("collection did not complete its original delivery: %+v (%v)", second, err)
	}
	after, err := os.ReadFile(filepath.Join(project.Root, "CHANGELOG.md"))
	if err != nil || !bytes.Equal(before, after) || bytes.Count(after, []byte("## 1.0.0")) != 1 {
		t.Fatalf("collection rewrote the existing entry: %s (%v)", after, err)
	}
}

func TestCollectPreservesAnIndexedEntryWithoutAWorktreeCopy(t *testing.T) {
	project := repo(t, stageOneJSON, map[string]string{"changes/a.md": "- original\n", "CHANGELOG.md": "# Changelog\n"})
	lock := filepath.Join(project.Root, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostic bytes.Buffer
	if code, err := Collect(project, "1.0.0", "2026-03-04", false, &output, &diagnostic); code != 2 || err == nil {
		t.Fatalf("collection did not pause before removing entries: %d, %v", code, err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := WriteInRepo(project.Root, "changes/a.md", []byte("- newer staged entry\n")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := project.Git.Run(ctx, "add", "--", "changes/a.md"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(project.Root, "changes/a.md")); err != nil {
		t.Fatal(err)
	}
	if code, err := Collect(project, "1.0.0", "2026-03-04", false, &output, &diagnostic); code != 1 || err != nil || !strings.Contains(diagnostic.String(), "kept changes/a.md") {
		t.Fatalf("collection did not identify the unresolved entry: %d, %v, %s", code, err, &diagnostic)
	}
	data, err := project.Git.Run(ctx, "show", ":changes/a.md")
	if err != nil || string(data) != "- newer staged entry\n" {
		t.Fatalf("staged content was not preserved: %q, %v", data, err)
	}
}

func TestCollectMigratesAnUnfinishedLegacyPlan(t *testing.T) {
	project := repo(t, stageOneJSON, map[string]string{"changes/a.md": "- a\n", "CHANGELOG.md": "# Changelog\n"})
	head, err := project.Git.Line(context.Background(), "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	changelog := []byte("# Changelog\n\n## 1.0.0 — 2026-03-04\n\n- a\n")
	if err := WriteInRepo(project.Root, "CHANGELOG.md", changelog); err != nil {
		t.Fatal(err)
	}
	files, err := project.Worktree().List("changes")
	if err != nil {
		t.Fatal(err)
	}
	legacy := collectPlan{Head: head, Version: "1.0.0", Destination: "CHANGELOG.md",
		Changelog: BlobID(project.Format, changelog), Files: files}
	record, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	place := filepath.Join(project.Root, ".git", "jackstay-changes.json")
	if err := os.WriteFile(place, record, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code, err := Collect(project, "1.0.0", "2026-03-14", false, &out, &errOut)
	if err != nil || code != 0 {
		t.Fatalf("legacy resume exit %d: %v (%s)", code, err, &errOut)
	}
	done, _, err := delivery.Read[collectPlan](place)
	if err != nil || done.Attempt != 1 || done.Outcome != delivery.Complete || !done.Plan.same(legacy) {
		t.Fatalf("legacy plan was not migrated: %+v (%v)", done, err)
	}
	if _, err := os.Stat(filepath.Join(project.Root, "changes/a.md")); !os.IsNotExist(err) {
		t.Fatalf("legacy plan did not remove its entry: %v", err)
	}
}

func TestCollectKeepsNewEntriesThatMatchAnExistingVersion(t *testing.T) {
	for _, name := range []string{"changes/new.md", "changes/ignored.md"} {
		t.Run(name, func(t *testing.T) {
			project := repo(t, stageOneJSON, map[string]string{
				"changes/.gitignore": "ignored.md\n",
				"CHANGELOG.md":       "# Changelog\n\n## 1.0.0 — 2026-03-04\n\n- a\n",
			})
			if err := WriteInRepo(project.Root, name, []byte("- a\n")); err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			code, err := Collect(project, "1.0.0", "2026-03-04", false, &out, &errOut)
			kept, readErr := os.ReadFile(filepath.Join(project.Root, name))
			if code != 1 || err != nil || readErr != nil || string(kept) != "- a\n" {
				t.Fatalf("entry was not kept: code %d, collect %v, file %q (%v), output %s", code, err, kept, readErr, &out)
			}
		})
	}
}

func TestCollectRefusesIgnoredEntriesBeforeWritingTheChangelog(t *testing.T) {
	const original = "# Changelog\n"
	project := repo(t, stageOneJSON, map[string]string{
		"changes/.gitignore": "ignored.md\n",
		"changes/a.md":       "- a\n",
		"CHANGELOG.md":       original,
	})
	if err := WriteInRepo(project.Root, "changes/ignored.md", []byte("- ignored\n")); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code, err := Collect(project, "1.0.0", "2026-03-04", false, &out, &errOut)
	changelog, readErr := os.ReadFile(filepath.Join(project.Root, "CHANGELOG.md"))
	if code != 1 || err != nil || readErr != nil || string(changelog) != original {
		t.Fatalf("changelog changed: code %d, collect %v, file %q (%v), output %s", code, err, changelog, readErr, &out)
	}
	if _, err := os.Stat(filepath.Join(project.Root, "changes/ignored.md")); err != nil {
		t.Fatalf("ignored entry was removed: %v", err)
	}
}

func TestCollectDryRunCreatesNoLockOrPlan(t *testing.T) {
	project := repo(t, stageOneJSON, map[string]string{"changes/a.md": "- a\n", "CHANGELOG.md": "# Changelog\n"})
	t.Chdir(project.Root)
	var out, errOut bytes.Buffer
	code := runCollect([]string{"--version", "1.0.0", "--date", "2026-03-04", "--dry-run"}, nil, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "- a") {
		t.Fatalf("code %d, output %s, stderr %s", code, &out, &errOut)
	}
	for _, name := range []string{"jackstay-changes.lock", "jackstay-changes.json"} {
		if _, err := os.Stat(filepath.Join(project.Root, ".git", name)); !os.IsNotExist(err) {
			t.Fatalf("preview created %s: %v", name, err)
		}
	}
}

func TestWritesRefuseALinkOnThePath(t *testing.T) {
	outside := t.TempDir()
	project := repo(t, stageOneJSON, map[string]string{"README": "x"})
	if err := os.Symlink(outside, filepath.Join(project.Root, "changes")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(project, NewRequest{Slug: "a"}); err == nil {
		t.Fatal("wrote through a link")
	}
	if left, _ := os.ReadDir(outside); len(left) != 0 {
		t.Fatalf("written outside: %v", left)
	}
	if err := WriteInRepo(project.Root, "../escape.md", []byte("x")); err == nil {
		t.Fatal("wrote outside the repository")
	}
}

func TestCollectRefusesInvalidEntriesAndLinkedFolders(t *testing.T) {
	project := repo(t, stageOneJSON, map[string]string{"CHANGELOG.md": "# Changelog\n",
		"changes/bad.md": "---\n{\"type\": \"chore\", \"audience\": \"consumer\"}\n---\n# Bad\n"})
	var out, errOut bytes.Buffer
	if code, _ := Collect(project, "1.0.0", "2026-01-01", false, &out, &errOut); code != 1 {
		t.Fatalf("code %d", code)
	}
	if _, err := os.Stat(filepath.Join(project.Root, "changes", "bad.md")); err != nil {
		t.Fatal("an invalid entry was removed")
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "a.md"), []byte("- a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linked := repo(t, strings.Replace(stageOneJSON, `"dir": "changes"`, `"dir": "linked"`, 1), map[string]string{"CHANGELOG.md": "# Changelog\n"})
	if err := os.Symlink(outside, filepath.Join(linked.Root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(linked, "1.0.0", "2026-01-01", false, &out, &errOut); err == nil {
		t.Fatal("read through a linked folder")
	}
	if _, err := os.Stat(filepath.Join(outside, "a.md")); err != nil {
		t.Fatal("a file outside the repository was removed")
	}
}

func TestNewRefusesAMissingTemplate(t *testing.T) {
	withTemplate := strings.Replace(stageOneJSON, `"heading"`, `"template": "missing.md", "heading"`, 1)
	project := repo(t, withTemplate, map[string]string{"README": "x"})
	if _, err := New(project, NewRequest{Slug: "a"}); err == nil {
		t.Fatal("a missing template was ignored")
	}
	if _, err := os.Stat(filepath.Join(project.Root, "changes", "a.md")); !os.IsNotExist(err) {
		t.Fatal("an entry was written")
	}
}
