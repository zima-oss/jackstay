//go:build integration

package adopt

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A git work tree with no jackstay.json: the project is the tree's top, and jst adopt starts its jackstay.json.
func TestAGitProjectWithNoJackstayJSONGetsOne(t *testing.T) {
	root := project(t, map[string]string{"src/main.txt": "x\n"})
	if output, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	options := optionsFor(root)
	options.Dir = filepath.Join(root, "src")
	made := plan(t, options)
	if got := changeIDs(made.Changes); !slices.Equal(got, []string{"agents-block", "claude-md", "hook:guard", "jackstay.json"}) {
		t.Fatalf("changes %v", got)
	}
	if made.Project != filepath.Base(root) {
		t.Fatalf("project %q", made.Project)
	}
	applyTwice(t, options)
	text := read(t, root, "jackstay.json")
	if !strings.Contains(text, `"$schema":`) || !strings.Contains(text, `"jackstay_schema": 2`) || !strings.Contains(text, `"project": "`+filepath.Base(root)+`"`) {
		t.Fatalf("jackstay.json:\n%s", text)
	}
}

func TestAFolderInNoProjectIsAUsageError(t *testing.T) {
	options := optionsFor(t.TempDir())
	if _, err := Make(options); err == nil || !strings.Contains(err.Error(), "in no project") {
		t.Fatalf("got %v", err)
	}
}

// A relative adopt.profile is the main checkout's: from a linked worktree under .claude/worktrees/
// it names the same file, and a --profile given in the worktree is recorded relative to the main
// checkout, so the value is one in every checkout (found when a project adopted 0.5.1).
func TestARecordedProfileIsTheMainCheckoutsInALinkedWorktree(t *testing.T) {
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exampleProfile(t, filepath.Join(top, "hub"), `{}`, nil)
	main := filepath.Join(top, "main")
	recorded := "../hub/profiles/profile.json"
	writeFiles(t, main, map[string]string{
		"jackstay.json": `{"jackstay_schema": 2, "project": "p", "adopt": {"profile": "` + recorded + `"}}`,
		".gitignore":    ".claude/worktrees/\n",
	})
	worktree := filepath.Join(main, ".claude", "worktrees", "x")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "start"},
		{"worktree", "add", "-q", "-b", "x", worktree}} {
		command := exec.Command("git", append([]string{"-C", main}, args...)...)
		command.Env = append(command.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	for _, folder := range []string{main, worktree} {
		made := plan(t, optionsFor(folder))
		if made.ProfileName != "ours" || made.Profile != recorded {
			t.Fatalf("%s: profile %q named %q", folder, made.Profile, made.ProfileName)
		}
	}
	given := optionsFor(worktree)
	given.Profile = filepath.Join("..", "..", "..", "..", "hub", "profiles", "profile.json")
	if made := plan(t, given); made.Profile != recorded {
		t.Fatalf("a --profile given in the worktree recorded as %q, not %q", made.Profile, recorded)
	}
	// --block reads the same profile: recorded, and given in the worktree.
	for _, options := range []Options{optionsFor(worktree), given} {
		text, err := BlockOf(options)
		if err != nil || !strings.Contains(text, "Our rule.") {
			t.Fatalf("--block in the worktree (profile %q): %v\n%s", options.Profile, err, text)
		}
	}
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
