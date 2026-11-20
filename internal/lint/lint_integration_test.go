//go:build integration

package lint

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/release"
)

type repoFixture struct {
	t   *testing.T
	dir string
}

func (r repoFixture) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-01-02T03:04:05Z", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r repoFixture) write(files map[string]string) {
	r.t.Helper()
	for name, text := range files {
		full := filepath.Join(r.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if text == "" {
			_ = os.Remove(full)
			continue
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r repoFixture) commit(message string, files map[string]string) string {
	r.write(files)
	r.git("add", "-A")
	r.git("commit", "-q", "-m", message)
	return r.git("rev-parse", "HEAD")
}

func newRepo(t *testing.T, jackstayJSON string, files map[string]string) repoFixture {
	t.Helper()
	r := repoFixture{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	files["jackstay.json"] = jackstayJSON
	r.commit("start", files)
	return r
}

func ruleIDs(found []Finding) string {
	var ids []string
	for _, finding := range found {
		ids = append(ids, finding.Rule)
	}
	return strings.Join(ids, ",")
}

const treeJSON = `{"jackstay_schema": 2, "project": "p",
 "cards": {"dir": "docs/cards", "id": "P-[0-9]+", "unit": "pr", "since": "2026-01-01", "first_new": "P-10"},
 "ids": {"P": {"files": "docs/cards/P-{n}-*.md", "width": 3}, "CHK": {"files": "docs/checks/LC-{n}-*.md", "width": 3}},
 "records": {"CHK": {"dir": "docs/checks", "columns": ["id"], "before": "docs/ledger.md"}}}`

func TestTreeRules(t *testing.T) {
	r := newRepo(t, treeJSON, map[string]string{
		"docs/cards/P-001-a.md":    "---\n{\"commits\": [\"abc\"]}\n---\nold card",
		"docs/cards/P-001-b.md":    "---\n{}\n---\n",
		"docs/cards/P-010-x.md":    "---\n{\"commits\": [\"abc\"]}\n---\nnew card with hashes",
		"docs/checks/CHK-005-c.md": "row",
		"docs/CHK-006-stray.md":    "stray",
		"docs/ledger.md":           "| CHK-004 | old |\n| CHK-007 | new row |\n",
	})
	loaded, err := config.Parse([]byte(treeJSON))
	if err != nil {
		t.Fatal(err)
	}
	git := gitcli.Git{Dir: r.dir}
	found, err := TreeRules(loaded, worktreeFiles{root: r.dir, git: git}, gitHistory{revisions: []string{"HEAD"}, git: git})
	if err != nil {
		t.Fatal(err)
	}
	want := "duplicate-number,new-card-commits,record-out-of-place,frozen-ledger-row"
	if got := ruleIDs(found); got != want {
		t.Fatalf("got %s want %s\n%+v", got, want, found)
	}
}

func TestCardsCanBeInTheRepositoryRoot(t *testing.T) {
	const settings = `{"jackstay_schema":2,"cards":{"dir":".","id":"P-[0-9]+","unit":"pr","since":"2026-01-01"}}`
	repo := newRepo(t, settings, map[string]string{"P-1-example.md": "# Example"})
	loaded, err := config.Parse([]byte(settings))
	if err != nil {
		t.Fatal(err)
	}
	git := gitcli.Git{Dir: repo.dir}
	tree := worktreeFiles{root: repo.dir, git: git}
	found, err := TreeRules(loaded, tree, gitHistory{revisions: []string{"HEAD"}, git: git})
	if err != nil || len(found) != 0 {
		t.Fatalf("root cards in tree: %v %v", found, err)
	}
	found, err = CardLines(loaded.Section("cards"), tree, "Card: P-1")
	if err != nil || len(found) != 0 {
		t.Fatalf("root cards in PR: %v %v", found, err)
	}
}

func TestTreeRulesWithoutCards(t *testing.T) {
	r := newRepo(t, treeJSON, map[string]string{"README.md": "x"})
	loaded, _ := config.Parse([]byte(treeJSON))
	git := gitcli.Git{Dir: r.dir}
	if _, err := TreeRules(loaded, worktreeFiles{root: r.dir, git: git}, gitHistory{revisions: []string{"HEAD"}, git: git}); err == nil {
		t.Fatal("no card files was not an error")
	}
}

const releaseJSON = `{"jackstay_schema": 2, "project": "p",
 "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version} — {date}"},
 "release": {"first": "0.1.0", "files": [{"path": "Config.xcconfig", "match": "^MARKETING_VERSION = (?P<version>[0-9.]+)$"}]}}`

func releaseChange(t *testing.T, r repoFixture, base string, branch, body string) ([]Finding, []string) {
	t.Helper()
	repo := release.NewRepo(r.dir)
	head := r.git("rev-parse", "HEAD")
	mergeBase := r.git("merge-base", base, head)
	baseConfig, err := repo.ConfigAt(base)
	if err != nil {
		t.Fatal(err)
	}
	found, notes, err := ReleaseRules(repo, baseConfig, Change{Base: base, Head: head, MergeBase: mergeBase, HeadBranch: branch, Body: body,
		Judged: revisionFiles{revision: head, git: gitcli.Git{Dir: r.dir}}})
	if err != nil {
		t.Fatal(err)
	}
	return found, notes
}

func TestReleaseRules(t *testing.T) {
	start := map[string]string{"Config.xcconfig": "OTHER = 1\nMARKETING_VERSION = 0.1.0\n", "CHANGELOG.md": "# Changelog\n"}
	entry := "---\n{\"type\": \"fix\", \"audience\": \"consumer\"}\n---\n# A fix\n"
	cases := []struct {
		name  string
		files map[string]string
		body  string
		want  string
	}{
		{name: "undocumented", files: map[string]string{"src.txt": "x"}, want: "D1-undocumented"},
		{name: "none with reason", files: map[string]string{"src.txt": "x"}, body: "Changes: none (docs only)", want: ""},
		{name: "none without reason", files: map[string]string{"src.txt": "x"}, body: "Changes: none", want: "D1-undocumented"},
		{name: "entry", files: map[string]string{"changes/a.md": entry}, want: ""},
		{name: "entry and none", files: map[string]string{"changes/a.md": entry}, body: "Changes: none (x)", want: "D1-entry-and-none"},
		{name: "untyped entry", files: map[string]string{"changes/a.md": "- plain\n"}, want: "D2-invalid-entry"},
		{name: "other xcconfig setting", files: map[string]string{"Config.xcconfig": "OTHER = 2\nMARKETING_VERSION = 0.1.0\n", "changes/a.md": entry}, want: ""},
		{name: "version raised", files: map[string]string{"Config.xcconfig": "OTHER = 1\nMARKETING_VERSION = 0.9.0\n", "changes/a.md": entry},
			want: "D3-version-changed,D3-merge-version"},
		{name: "changelog edited", files: map[string]string{"CHANGELOG.md": "# Changelog\nmore\n", "changes/a.md": entry}, want: "D3-changelog-changed,D3-changelog-changed"},
		{name: "a request-named fixture elsewhere", files: map[string]string{"testdata/_release-request.json": "{}", "changes/a.md": entry}, want: ""},
		{name: "request written", files: map[string]string{"changes/_release-request.json": "{}", "changes/a.md": entry}, want: "D3-request-changed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t, releaseJSON, map[string]string{"Config.xcconfig": start["Config.xcconfig"], "CHANGELOG.md": start["CHANGELOG.md"]})
			base := r.git("rev-parse", "HEAD")
			r.git("checkout", "-q", "-b", "work")
			r.commit("change", c.files)
			found, _ := releaseChange(t, r, base, "work", c.body)
			if got := ruleIDs(found); got != c.want {
				t.Fatalf("got %q want %q\n%+v", got, c.want, found)
			}
		})
	}
}

func TestReleaseBranchRules(t *testing.T) {
	r := newRepo(t, releaseJSON, map[string]string{"Config.xcconfig": "MARKETING_VERSION = 0.1.0\n", "CHANGELOG.md": "# Changelog\n",
		"changes/a.md": "---\n{\"type\": \"fix\", \"audience\": \"consumer\"}\n---\n# A fix\n"})
	base := r.git("rev-parse", "HEAD")
	repo := release.NewRepo(r.dir)
	state, err := repo.StateAt(base)
	if err != nil {
		t.Fatal(err)
	}
	next, _, _, listed, err := release.Next(repo.Tree(base), state.Settings, state.History.Last())
	if err != nil {
		t.Fatal(err)
	}
	request := release.RequestOf(listed, next, base)
	r.git("checkout", "-q", "-b", "jackstay/release")
	r.commit("request", map[string]string{"changes/_release-request.json": string(request.Encode())})
	found, notes := releaseChange(t, r, base, "jackstay/release", "")
	if len(found) != 0 || len(notes) != 0 {
		t.Fatalf("a fresh request: %+v %q", found, notes)
	}
	// The base moves: the request is stale, which is said and not failed.
	r.git("checkout", "-q", "main")
	movedBase := r.commit("late", map[string]string{"changes/b.md": "---\n{\"type\": \"feature\", \"audience\": \"consumer\"}\n---\n# Late\n"})
	r.git("checkout", "-q", "jackstay/release")
	found, notes = releaseChange(t, r, movedBase, "jackstay/release", "")
	if len(found) != 0 || len(notes) != 1 {
		t.Fatalf("a stale request: %+v %q", found, notes)
	}
	// Anything else on the release branch fails.
	r.commit("extra", map[string]string{"src.txt": "x"})
	found, _ = releaseChange(t, r, base, "jackstay/release", "")
	if got := ruleIDs(found); got != "D4-not-request-only" {
		t.Fatalf("got %s", got)
	}
}

func TestOverrideAndRequestModesAndBaseExemption(t *testing.T) {
	start := map[string]string{"Config.xcconfig": "MARKETING_VERSION = 0.1.0\n", "CHANGELOG.md": "# Changelog\n"}
	t.Run("invalid override", func(t *testing.T) {
		r := newRepo(t, releaseJSON, map[string]string{"Config.xcconfig": start["Config.xcconfig"], "CHANGELOG.md": start["CHANGELOG.md"]})
		base := r.git("rev-parse", "HEAD")
		r.git("checkout", "-q", "-b", "work")
		r.commit("override", map[string]string{"changes/_release.json": "{}"})
		found, _ := releaseChange(t, r, base, "work", "")
		if got := ruleIDs(found); got != "D2-invalid-entry" {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("override not above the last release", func(t *testing.T) {
		r := newRepo(t, releaseJSON, map[string]string{"Config.xcconfig": start["Config.xcconfig"], "CHANGELOG.md": start["CHANGELOG.md"]})
		base := r.git("rev-parse", "HEAD")
		r.git("checkout", "-q", "-b", "work")
		r.commit("override", map[string]string{"changes/_release.json": `{"version": "0.1.0", "reason": "x"}`})
		found, _ := releaseChange(t, r, base, "work", "")
		if got := ruleIDs(found); got != "D2-invalid-entry" {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("a pull request cannot exempt itself", func(t *testing.T) {
		withCards := strings.Replace(releaseJSON, `"changes":`, `"cards": {"dir": "docs/cards", "id": "P-[0-9]+", "unit": "pr", "since": "2026-01-01"}, "changes":`, 1)
		r := newRepo(t, withCards, map[string]string{"Config.xcconfig": start["Config.xcconfig"], "CHANGELOG.md": start["CHANGELOG.md"], "docs/cards/P-1-x.md": "c"})
		base := r.git("rev-parse", "HEAD")
		r.git("checkout", "-q", "-b", "sneaky")
		exempted := strings.Replace(withCards, `"since": "2026-01-01"`, `"since": "2026-01-01", "exempt_branches": ["sneaky"]`, 1)
		r.commit("exempt me", map[string]string{"jackstay.json": exempted, "src.txt": "x"})
		found, _ := releaseChange(t, r, base, "sneaky", "")
		if got := ruleIDs(found); got != "D0-settings-alone,D1-undocumented" {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("a request that is a link", func(t *testing.T) {
		r := newRepo(t, releaseJSON, map[string]string{"Config.xcconfig": start["Config.xcconfig"], "CHANGELOG.md": start["CHANGELOG.md"],
			"changes/a.md": "---\n{\"type\": \"fix\", \"audience\": \"consumer\"}\n---\n# A\n"})
		base := r.git("rev-parse", "HEAD")
		r.git("checkout", "-q", "-b", "jackstay/release")
		r.write(map[string]string{"target.json": `{"version": "0.1.1", "entries": {}, "override": null, "made_on": "x"}`})
		if err := os.Symlink("../target.json", filepath.Join(r.dir, "changes", "_release-request.json")); err != nil {
			t.Fatal(err)
		}
		r.git("add", "changes/_release-request.json")
		r.git("commit", "-q", "-m", "link")
		found, _ := releaseChange(t, r, base, "jackstay/release", "")
		if !strings.Contains(ruleIDs(found), "D4-invalid-request") {
			t.Fatalf("got %s", ruleIDs(found))
		}
	})
}

func TestMergeKeepsTheReleasedValuesAfterABaseMigration(t *testing.T) {
	r := newRepo(t, releaseJSON, map[string]string{"Config.xcconfig": "MARKETING_VERSION = 0.1.0\n", "CHANGELOG.md": "# Changelog\n",
		"Other.xcconfig": "MARKETING_VERSION = 0.1.0\n"})
	start := r.git("rev-parse", "HEAD")
	// The base moves the version file to Other.xcconfig.
	moved := strings.Replace(releaseJSON, "Config.xcconfig", "Other.xcconfig", 1)
	base := r.commit("move the version file", map[string]string{"jackstay.json": moved})
	// An older pull request changes Other.xcconfig, which was no version file where it started.
	r.git("checkout", "-q", "-b", "older", start)
	head := r.commit("raise it", map[string]string{"Other.xcconfig": "MARKETING_VERSION = 0.9.0\n", "changes/a.md": "---\n{\"type\": \"fix\", \"audience\": \"consumer\"}\n---\n# A\n"})
	r.git("checkout", "-q", "-b", "merged", base)
	r.git("merge", "-q", "--no-edit", head)
	merged := r.git("rev-parse", "HEAD")
	repo := release.NewRepo(r.dir)
	baseConfig, err := repo.ConfigAt(base)
	if err != nil {
		t.Fatal(err)
	}
	found, _, err := ReleaseRules(repo, baseConfig, Change{Base: base, Head: head, MergeBase: start, HeadBranch: "older",
		Judged: revisionFiles{revision: merged, git: gitcli.Git{Dir: r.dir}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ruleIDs(found), "D3-merge-version") {
		t.Fatalf("got %s", ruleIDs(found))
	}
}

func TestLintTreeValidatesTheSelectedRevisionSettings(t *testing.T) {
	repo := newRepo(t, `{"jackstay_schema":999}`, map[string]string{"README.md": "a"})
	historical := repo.git("rev-parse", "HEAD")
	repo.commit("usable settings", map[string]string{"jackstay.json": `{"jackstay_schema":2}`})
	t.Chdir(repo.dir)
	var stdout, stderr bytes.Buffer
	code := runTree([]string{"--rev", historical}, nil, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "jackstay_schema") {
		t.Fatalf("code %d, stdout %s, stderr %s", code, &stdout, &stderr)
	}
}

func TestD0RejectsSemanticallyInvalidTestSettings(t *testing.T) {
	repo := newRepo(t, `{"jackstay_schema":2}`, map[string]string{"README.md": "a"})
	base := repo.git("rev-parse", "HEAD")
	head := repo.commit("invalid settings", map[string]string{"jackstay.json": `{"jackstay_schema":2,
		"receipts":{"toolchain":[]}, "test":{"scopes":{"quick":{"run":["true"],"steps":["format"]}},"steps":{"format":{"run":["true"]}}}}`})
	baseConfig, _ := config.Parse([]byte(`{"jackstay_schema":2}`))
	changes, _, err := ReleaseRules(release.NewRepo(repo.dir), baseConfig,
		Change{Base: base, Head: head, MergeBase: base})
	if err != nil || ruleIDs(changes) != RuleSettingsAlone || !strings.Contains(changes[0].Text, "test.scopes.quick") {
		t.Fatalf("invalid test settings accepted: %v, %v", changes, err)
	}
}

func TestSettingsLandAlone(t *testing.T) {
	r := newRepo(t, releaseJSON, map[string]string{"Config.xcconfig": "MARKETING_VERSION = 0.1.0\n", "CHANGELOG.md": "# Changelog\n"})
	base := r.git("rev-parse", "HEAD")
	r.git("checkout", "-q", "-b", "settings")
	tagged := strings.Replace(releaseJSON, `"first": "0.1.0"`, `"first": "0.1.0", "tag": "release-{version}"`, 1)
	r.commit("settings only", map[string]string{"jackstay.json": tagged})
	found, _ := releaseChange(t, r, base, "settings", "Changes: none (settings)")
	if len(found) != 0 {
		t.Fatalf("a settings-only pull request: %+v", found)
	}
	r.commit("and code", map[string]string{"src.txt": "x"})
	found, _ = releaseChange(t, r, base, "settings", "Changes: none (settings)")
	if got := ruleIDs(found); got != "D0-settings-alone" {
		t.Fatalf("got %s", got)
	}
}

func TestPullRequestRulesComeFromTheBase(t *testing.T) {
	withCards := `{"jackstay_schema": 2, "project": "p", "cards": {"dir": "docs/cards", "id": "P-[0-9]+", "unit": "pr", "since": "2026-01-01"}}`
	r := newRepo(t, withCards, map[string]string{"docs/cards/P-1-x.md": "c"})
	base := r.git("rev-parse", "HEAD")
	r.git("checkout", "-q", "-b", "sneaky")
	exempted := strings.Replace(withCards, `"since": "2026-01-01"`, `"since": "2026-01-01", "exempt_branches": ["sneaky"]`, 1)
	head := r.commit("exempt me", map[string]string{"jackstay.json": exempted})
	loaded, _ := config.Parse([]byte(exempted))
	space := workspace{root: r.dir, config: loaded, git: gitcli.Git{Dir: r.dir}}
	found, _, err := space.judgePullRequest(inputs{body: "no card line", headBranch: "sneaky", base: base, head: head}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := ruleIDs(found); got != "card-line-missing" {
		t.Fatalf("got %s", got)
	}
}

func TestARequestInAMovedEntriesFolderIsRefused(t *testing.T) {
	r := newRepo(t, releaseJSON, map[string]string{"Config.xcconfig": "MARKETING_VERSION = 0.1.0\n", "CHANGELOG.md": "# Changelog\n"})
	base := r.git("rev-parse", "HEAD")
	r.git("checkout", "-q", "-b", "work")
	moved := strings.Replace(releaseJSON, `"dir": "changes"`, `"dir": "notes"`, 1)
	r.commit("move", map[string]string{"jackstay.json": moved, "notes/_release-request.json": "{}", "notes/a.md": "x"})
	found, _ := releaseChange(t, r, base, "work", "Changes: none (x)")
	if !strings.Contains(ruleIDs(found), "D0-settings-alone") {
		t.Fatalf("got %s", ruleIDs(found))
	}
}

func TestRevMustBeACommit(t *testing.T) {
	r := newRepo(t, treeJSON, map[string]string{"docs/cards/P-001-a.md": "---\n{}\n---\n"})
	loaded, _ := config.Parse([]byte(treeJSON))
	space := workspace{root: r.dir, config: loaded, git: gitcli.Git{Dir: r.dir}}
	tree := r.git("rev-parse", "HEAD^{tree}")
	if _, _, err := space.judged(tree); err == nil {
		t.Fatal("a tree object was accepted as --rev")
	}
}

func TestTheMergeIsJudgedForAPullRequest(t *testing.T) {
	r := newRepo(t, treeJSON, map[string]string{"docs/cards/P-001-a.md": "---\n{}\n---\n", "docs/ledger.md": "| x |\n"})
	base := r.git("rev-parse", "HEAD")
	r.git("checkout", "-q", "-b", "dup")
	head := r.commit("duplicate", map[string]string{"docs/cards/P-001-b.md": "---\n{}\n---\n"})
	r.git("checkout", "-q", "main")
	loaded, _ := config.Parse([]byte(treeJSON))
	space := workspace{root: r.dir, config: loaded, git: gitcli.Git{Dir: r.dir}}
	found, _, err := space.judgePullRequest(inputs{body: "Card: P-001", headBranch: "dup", base: base, head: head, mergeOf: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := ruleIDs(found); got != "duplicate-number" {
		t.Fatalf("got %s (the checkout is main, the merge has the duplicate)", got)
	}
}

func TestConfigurationMovesDoNotLiftTheRules(t *testing.T) {
	start := map[string]string{"Config.xcconfig": "MARKETING_VERSION = 0.1.0\n", "CHANGELOG.md": "# Changelog\n"}
	t.Run("an invalid entry under a moved folder", func(t *testing.T) {
		r := newRepo(t, releaseJSON, map[string]string{"Config.xcconfig": start["Config.xcconfig"], "CHANGELOG.md": start["CHANGELOG.md"]})
		base := r.git("rev-parse", "HEAD")
		r.git("checkout", "-q", "-b", "work")
		moved := strings.Replace(releaseJSON, `"dir": "changes"`, `"dir": "notes"`, 1)
		r.commit("move", map[string]string{"jackstay.json": moved, "notes/a.md": "- untyped\n"})
		found, _ := releaseChange(t, r, base, "work", "")
		if !strings.Contains(ruleIDs(found), "D0-settings-alone") {
			t.Fatalf("got %s", ruleIDs(found))
		}
	})
	t.Run("removing the release section with a version change", func(t *testing.T) {
		r := newRepo(t, releaseJSON, map[string]string{"Config.xcconfig": start["Config.xcconfig"], "CHANGELOG.md": start["CHANGELOG.md"]})
		base := r.git("rev-parse", "HEAD")
		r.git("checkout", "-q", "-b", "work")
		without := `{"jackstay_schema": 2, "project": "p", "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version} — {date}"}}`
		r.commit("drop release", map[string]string{"jackstay.json": without, "Config.xcconfig": "MARKETING_VERSION = 0.9.0\n",
			"changes/a.md": "---\n{\"type\": \"fix\", \"audience\": \"consumer\"}\n---\n# A\n"})
		found, _ := releaseChange(t, r, base, "work", "")
		if !strings.Contains(ruleIDs(found), "D3-version-changed") || !strings.Contains(ruleIDs(found), "D3-merge-version") {
			t.Fatalf("got %s", ruleIDs(found))
		}
	})
}

func TestASnapshotUnderANonASCIIFolderIsKept(t *testing.T) {
	cfg := `{"jackstay_schema": 2, "project": "p", "cards": {"dir": "docs/cards", "id": "P-[0-9]+", "unit": "pr", "since": "2026-01-01", "snapshots": "docs/история"}}`
	r := newRepo(t, cfg, map[string]string{"docs/cards/P-1-x.md": "c", "docs/история/landed-2026-01-01.json": "{}"})
	r.git("rm", "-q", "docs/история/landed-2026-01-01.json")
	r.git("commit", "-q", "-m", "delete the snapshot")
	loaded, _ := config.Parse([]byte(cfg))
	git := gitcli.Git{Dir: r.dir}
	found, err := TreeRules(loaded, worktreeFiles{root: r.dir, git: git}, gitHistory{revisions: []string{"HEAD"}, git: git})
	if err != nil {
		t.Fatal(err)
	}
	if got := ruleIDs(found); got != "snapshot-deleted" {
		t.Fatalf("got %s", got)
	}
}

func TestANewCardsCommitsMustBeAnEmptyList(t *testing.T) {
	r := newRepo(t, treeJSON, map[string]string{"docs/cards/P-010-x.md": "---\n{\"commits\": \"deadbeef\"}\n---\n"})
	loaded, _ := config.Parse([]byte(treeJSON))
	git := gitcli.Git{Dir: r.dir}
	found, err := TreeRules(loaded, worktreeFiles{root: r.dir, git: git}, gitHistory{revisions: []string{"HEAD"}, git: git})
	if err != nil || ruleIDs(found) != "new-card-commits" {
		t.Fatalf("got %v %+v", err, found)
	}
}

func TestASettingsPullRequestMustLeaveWorkingSettings(t *testing.T) {
	r := newRepo(t, `{"jackstay_schema": 2, "project": "p", "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version}"}}`, map[string]string{"CHANGELOG.md": "# Changelog\n"})
	base := r.git("rev-parse", "HEAD")
	r.git("checkout", "-q", "-b", "adopt")
	r.commit("adopt release", map[string]string{"jackstay.json": releaseJSON})
	found, _ := releaseChange(t, r, base, "adopt", "Changes: none (settings)")
	if !strings.Contains(ruleIDs(found), "D0-settings-alone") || !strings.Contains(found[0].Text, "missing") {
		t.Fatalf("an adoption naming a missing version file passed: %+v", found)
	}
}

func TestAdoptionRefusesANonNumericBuild(t *testing.T) {
	r := newRepo(t, `{"jackstay_schema": 2, "project": "p", "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version}"}}`,
		map[string]string{"CHANGELOG.md": "# Changelog\n", "Config.xcconfig": "MARKETING_VERSION = 0.1.0\nBUILD = 1.2.3\n"})
	base := r.git("rev-parse", "HEAD")
	r.git("checkout", "-q", "-b", "adopt")
	withBuild := strings.Replace(releaseJSON, `"files":`, `"build": {"path": "Config.xcconfig", "match": "^BUILD = (?P<build>.+)$"}, "files":`, 1)
	r.commit("adopt", map[string]string{"jackstay.json": withBuild})
	found, _ := releaseChange(t, r, base, "adopt", "Changes: none (settings)")
	if !strings.Contains(ruleIDs(found), "D0-settings-alone") {
		t.Fatalf("a non-numeric build number passed: %+v", found)
	}
}

// adoptingRepo is a repository whose main has no jackstay.json (a project's earlier configuration
// stands beside, which jackstay never reads), and a branch adopt that replaces it with added files.
func adoptingRepo(t *testing.T, added map[string]string) (repoFixture, string, string) {
	t.Helper()
	r := repoFixture{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	base := r.commit("start", map[string]string{"legacy.json": `{"schema": 2}`, "docs/cards/P-1-x.md": "c",
		"Config.xcconfig": "MARKETING_VERSION = 0.1.0\n", "CHANGELOG.md": "# Changelog\n"})
	r.git("checkout", "-q", "-b", "adopt")
	added["legacy.json"] = ""
	head := r.commit("adopt", added)
	return r, base, head
}

func hasNote(notes []string, part string) bool {
	for _, note := range notes {
		if strings.Contains(note, part) {
			return true
		}
	}
	return false
}

// The pull request that adopts jackstay (the base has no jackstay.json) is judged by its own:
// its Card lines and tree rules; the release rules wait for a base that has settings, and the new
// settings must work (found when a project adopted 0.5.2).
func TestThePullRequestThatAdoptsJackstayIsJudgedByItsOwnSettings(t *testing.T) {
	withCards := `{"jackstay_schema": 2, "project": "p", "cards": {"dir": "docs/cards", "id": "P-[0-9]+", "unit": "pr", "since": "2026-01-01"}}`
	r, base, head := adoptingRepo(t, map[string]string{"jackstay.json": withCards, "AGENTS.md": "rules\n"})
	loaded, _ := config.Parse([]byte(withCards))
	space := workspace{root: r.dir, config: loaded, git: gitcli.Git{Dir: r.dir}}
	found, notes, err := space.judgePullRequest(inputs{body: "Card: P-1", headBranch: "adopt", base: base, head: head, mergeOf: true}, "")
	if err != nil || len(found) != 0 || !hasNote(notes, "this pull request adopts jackstay") {
		t.Fatalf("found %s, notes %v, err %v", ruleIDs(found), notes, err)
	}
	found, _, err = space.judgePullRequest(inputs{body: "no card line", headBranch: "adopt", base: base, head: head, mergeOf: true}, "")
	if err != nil || ruleIDs(found) != "card-line-missing" {
		t.Fatalf("the head's own Card rule: %s %v", ruleIDs(found), err)
	}
	// With release settings: no base state to judge D1–D4 by; the new settings are checked.
	r, base, head = adoptingRepo(t, map[string]string{"jackstay.json": releaseJSON, "AGENTS.md": "rules\n"})
	loaded, _ = config.Parse([]byte(releaseJSON))
	space = workspace{root: r.dir, config: loaded, git: gitcli.Git{Dir: r.dir}}
	found, notes, err = space.judgePullRequest(inputs{body: "Changes: none (adoption)", headBranch: "adopt", base: base, head: head, mergeOf: true}, "")
	if err != nil || len(found) != 0 || !hasNote(notes, "adopts jackstay") {
		t.Fatalf("found %v, notes %v, err %v", found, notes, err)
	}
	broken := strings.Replace(releaseJSON, `"first": "0.1.0"`, `"first": "0.2.0"`, 1)
	r, base, head = adoptingRepo(t, map[string]string{"jackstay.json": broken, "AGENTS.md": "rules\n"})
	loaded, _ = config.Parse([]byte(broken))
	space = workspace{root: r.dir, config: loaded, git: gitcli.Git{Dir: r.dir}}
	found, _, err = space.judgePullRequest(inputs{body: "Changes: none (adoption)", headBranch: "adopt", base: base, head: head, mergeOf: true}, "")
	if err != nil || !strings.Contains(fmt.Sprint(found), "the new settings") {
		t.Fatalf("settings that do not work were let through: %v %v", found, err)
	}
}

// Neither the base nor the pull request has a jackstay.json: as before, the rules cannot be read.
func TestAPullRequestWithNoJackstayJSONOnEitherSideCannotBeJudged(t *testing.T) {
	r, base, head := adoptingRepo(t, map[string]string{"README.md": "x\n"})
	space := workspace{root: r.dir, config: config.Config{}, git: gitcli.Git{Dir: r.dir}}
	_, _, err := space.judgePullRequest(inputs{body: "x", headBranch: "adopt", base: base, head: head, mergeOf: true}, "")
	var refused *problem
	if !errors.As(err, &refused) || refused.code != 2 || !strings.Contains(refused.message, "neither has the pull request") {
		t.Fatalf("got %v", err)
	}
}

// A base that has a jackstay.json keeps judging the pull request: no adoption note.
func TestABaseWithJackstayJSONIsNotAnAdoption(t *testing.T) {
	withCards := `{"jackstay_schema": 2, "project": "p", "cards": {"dir": "docs/cards", "id": "P-[0-9]+", "unit": "pr", "since": "2026-01-01"}}`
	r := newRepo(t, withCards, map[string]string{"docs/cards/P-1-x.md": "c"})
	base := r.git("rev-parse", "HEAD")
	r.git("checkout", "-q", "-b", "work")
	head := r.commit("work", map[string]string{"README.md": "x\n"})
	loaded, _ := config.Parse([]byte(withCards))
	space := workspace{root: r.dir, config: loaded, git: gitcli.Git{Dir: r.dir}}
	found, notes, err := space.judgePullRequest(inputs{body: "no card line", headBranch: "work", base: base, head: head, mergeOf: true}, "")
	if err != nil || ruleIDs(found) != "card-line-missing" || hasNote(notes, "adopts jackstay") {
		t.Fatalf("found %s, notes %v, err %v", ruleIDs(found), notes, err)
	}
}

// Asked from a folder below the top, ConfigIfAt still finds the jackstay.json at the top, as
// ConfigAt reads it: a pull request cannot pass for the adopting one by adding a jackstay.json in a
// subfolder and running lint there.
func TestTheBasesJackstayJSONIsFoundFromASubfolder(t *testing.T) {
	withCards := `{"jackstay_schema": 2, "project": "p", "cards": {"dir": "docs/cards", "id": "P-[0-9]+", "unit": "pr", "since": "2026-01-01"}}`
	r := newRepo(t, withCards, map[string]string{"docs/cards/P-1-x.md": "c"})
	base := r.git("rev-parse", "HEAD")
	r.git("checkout", "-q", "-b", "loose")
	permissive := `{"jackstay_schema": 2, "project": "p"}`
	head := r.commit("loosen", map[string]string{"sub/jackstay.json": permissive, "jackstay.json": permissive})
	sub := filepath.Join(r.dir, "sub")
	if _, exists, err := release.NewRepo(sub).ConfigIfAt(base); err != nil || !exists {
		t.Fatalf("the base's jackstay.json from a subfolder: exists %v, %v", exists, err)
	}
	if _, exists, err := release.NewRepo(sub).ConfigIfAt(head); err != nil || !exists {
		t.Fatalf("the head's top jackstay.json from a subfolder: exists %v, %v", exists, err)
	}
}

// ChangelogLine is D1 alone: the diff and the description, never the release history, so it judges
// in a shallow clone of a release-enabled base, where ReleaseRules cannot read the history.
func TestChangelogLineNeedsNoReleaseHistory(t *testing.T) {
	start := map[string]string{"Config.xcconfig": "OTHER = 1\nMARKETING_VERSION = 0.1.0\n", "CHANGELOG.md": "# Changelog\n"}
	origin := newRepo(t, releaseJSON, start)
	origin.commit("more", map[string]string{"a.txt": "a"})
	shallow := repoFixture{t: t, dir: filepath.Join(t.TempDir(), "shallow")}
	origin.git("clone", "-q", "--depth", "1", "--no-local", origin.dir, shallow.dir)
	base := shallow.git("rev-parse", "HEAD")
	shallow.git("checkout", "-q", "-b", "feat")
	shallow.commit("work", map[string]string{"src.txt": "x"})
	head := shallow.git("rev-parse", "HEAD")
	repo := release.NewRepo(shallow.dir)
	baseConfig, err := repo.ConfigAt(base)
	if err != nil {
		t.Fatal(err)
	}
	change := Change{Base: base, Head: head, MergeBase: base, HeadBranch: "feat"}
	if _, _, err := ReleaseRules(repo, baseConfig, change); err == nil {
		t.Fatal("ReleaseRules read a shallow clone's release history: this test no longer shows the difference")
	}
	for body, want := range map[string]string{"": "D1-undocumented", "Changes: none (a test)": "", "Changes: none": "D1-undocumented"} {
		change.Body = body
		found, err := ChangelogLine(repo, baseConfig, change)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if got := ruleIDs(found); got != want {
			t.Errorf("%q: %q, want %q", body, got, want)
		}
	}
}
