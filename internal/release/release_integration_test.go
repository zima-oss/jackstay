//go:build integration

package release

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/gitcli"
)

type fakeGitHub struct {
	t        *testing.T
	remote   string
	pulls    map[int]*PullRequest
	next     int
	releases []Release
	drafted  int
	latest   int64
}

func (fake *fakeGitHub) headOf(branch string) string {
	out, err := exec.Command("git", "-C", fake.remote, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (fake *fakeGitHub) OpenPullRequests(context.Context) ([]PullRequest, error) {
	var out []PullRequest
	for _, pull := range fake.pulls {
		if pull.State == "open" {
			pull.HeadSHA = fake.headOf(pull.Head)
			out = append(out, *pull)
		}
	}
	return out, nil
}

func (fake *fakeGitHub) PullRequest(_ context.Context, number int) (PullRequest, error) {
	pull := fake.pulls[number]
	pull.HeadSHA = fake.headOf(pull.Head)
	return *pull, nil
}

func (fake *fakeGitHub) CreatePullRequest(_ context.Context, head, base, title, body string, draft bool) (PullRequest, error) {
	fake.next++
	pull := &PullRequest{Number: fake.next, NodeID: "node", Head: head, Base: base, Title: title, Body: body, Draft: draft, State: "open", Author: "bot"}
	fake.pulls[fake.next] = pull
	return *pull, nil
}

func (fake *fakeGitHub) UpdatePullRequest(_ context.Context, number int, title, body string) error {
	fake.pulls[number].Title, fake.pulls[number].Body = title, body
	return nil
}

func (fake *fakeGitHub) ClosePullRequest(_ context.Context, number int, _ string) error {
	fake.pulls[number].State = "closed"
	return nil
}

func (fake *fakeGitHub) ConvertToDraft(context.Context, string) error {
	fake.drafted++
	for _, pull := range fake.pulls {
		pull.Draft = true
	}
	return nil
}

func (fake *fakeGitHub) Releases(context.Context) ([]Release, error) {
	return append([]Release{}, fake.releases...), nil
}

func (fake *fakeGitHub) CreateRelease(_ context.Context, tag, target, name, body string, draft bool) (Release, error) {
	release := Release{ID: int64(len(fake.releases) + 1), Tag: tag, Target: target, Name: name, Body: body, Draft: draft}
	fake.releases = append(fake.releases, release)
	return release, nil
}

func (fake *fakeGitHub) EditRelease(_ context.Context, id int64, change Edit) (Release, error) {
	for index := range fake.releases {
		release := &fake.releases[index]
		if release.ID != id {
			continue
		}
		if change.Name != nil {
			release.Name = *change.Name
		}
		if change.Body != nil {
			release.Body = *change.Body
		}
		if change.Draft != nil {
			release.Draft = *change.Draft
		}
		if change.Prerelease != nil {
			release.Prerelease = *change.Prerelease
		}
		if change.MakeLatest != nil && *change.MakeLatest == "true" {
			fake.latest = id
		}
		if change.MakeLatest != nil && *change.MakeLatest == "false" && fake.latest == id {
			fake.latest = 0
		}
		return *release, nil
	}
	return Release{}, nil
}

type world struct {
	t      *testing.T
	remote string
	work   string
	hub    *fakeGitHub
	runner string // the workflow's clone
}

var fixedEnv = []string{"GIT_AUTHOR_DATE=2026-03-04T05:06:07Z", "GIT_COMMITTER_DATE=2026-03-04T05:06:07Z",
	"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}

func (w *world) git(dir string, args ...string) string {
	w.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitcli.WithoutRepository(os.Environ()), fixedEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (w *world) write(name, text string) {
	w.t.Helper()
	full := filepath.Join(w.work, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) land(message string) {
	w.git(w.work, "add", "-A")
	w.git(w.work, "commit", "-q", "-m", message)
	w.git(w.work, "push", "-q", "origin", "main")
}

const projectJSON = `{"jackstay_schema": 2, "project": "p",
 "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version} — {date}", "newest": "first"},
 "release": {"merge": "owner", "first": "0.1.0", "files": [{"path": "VERSION", "match": "^(?P<version>[0-9]+\\.[0-9]+\\.[0-9]+)$"}]}}`

func newWorld(t *testing.T) *world {
	t.Helper()
	base := t.TempDir()
	w := &world{t: t, remote: filepath.Join(base, "origin.git"), work: filepath.Join(base, "work"), runner: filepath.Join(base, "runner")}
	w.git(base, "init", "-q", "--bare", "-b", "main", w.remote)
	w.git(base, "clone", "-q", w.remote, w.work)
	w.write("jackstay.json", projectJSON)
	w.write("VERSION", "0.1.0\n")
	w.write("CHANGELOG.md", "# Changelog\n")
	w.write("changes/activity.md", "---\n{\"type\": \"feature\", \"audience\": \"consumer\"}\n---\n# Activity timeline\n")
	w.write("changes/tidy.md", "---\n{\"type\": \"note\", \"audience\": \"internal\"}\n---\n# Internal tidy\n")
	w.land("start")
	w.git(base, "clone", "-q", w.remote, w.runner)
	w.hub = &fakeGitHub{t: t, remote: w.remote, pulls: map[int]*PullRequest{}}
	return w
}

func (w *world) flow() (Flow, *bytes.Buffer) {
	out := &bytes.Buffer{}
	repo := NewRepo(w.runner)
	return Flow{Repo: repo, GitHub: w.hub, Remote: "origin", Repository: "o/p", Author: "bot",
		Identity: Identity{Name: "bot", Email: "bot@example.invalid"}, Out: out, Context: context.Background()}, out
}

// mergeRequest lands the release branch as a squash on main, as the owner's merge does.

func (w *world) mergeRequest() {
	w.git(w.work, "pull", "-q", "--ff-only", "origin", "main")
	w.git(w.work, "fetch", "-q", "origin", "jackstay/release")
	w.git(w.work, "merge", "-q", "--squash", "FETCH_HEAD")
	w.git(w.work, "commit", "-q", "-m", "Release request (#1)")
	w.git(w.work, "push", "-q", "origin", "main")
}

func TestReleaseEndToEnd(t *testing.T) {
	w := newWorld(t)
	flow, out := w.flow()
	if code, err := flow.PullRequestFlow("main"); err != nil || code != 0 {
		t.Fatalf("pr: %d %v\n%s", code, err, out)
	}
	if len(w.hub.pulls) != 1 || !w.hub.pulls[1].Draft || w.hub.pulls[1].Title != "Release 0.2.0" {
		t.Fatalf("pulls %+v", w.hub.pulls)
	}
	if !strings.Contains(w.hub.pulls[1].Body, "Activity timeline") || strings.Contains(w.hub.pulls[1].Body, "### Notes\n\n- **Internal tidy**\n\n## ") {
		t.Fatalf("body:\n%s", w.hub.pulls[1].Body)
	}
	w.mergeRequest()
	flow, out = w.flow()
	if code, err := flow.Publish("main"); err != nil || code != 0 {
		t.Fatalf("publish: %d %v\n%s", code, err, out)
	}
	w.git(w.work, "pull", "-q", "--ff-only", "origin", "main")
	if data, _ := os.ReadFile(filepath.Join(w.work, "VERSION")); string(data) != "0.2.0\n" {
		t.Fatalf("VERSION %q\n%s", data, out)
	}
	changelog, _ := os.ReadFile(filepath.Join(w.work, "CHANGELOG.md"))
	if !strings.Contains(string(changelog), "## 0.2.0 — 2026-03-04\n\n### Features\n\n- **Activity timeline**") ||
		strings.Contains(string(changelog), "Internal tidy") {
		t.Fatalf("changelog:\n%s", changelog)
	}
	if left, _ := os.ReadDir(filepath.Join(w.work, "changes")); len(left) != 0 {
		t.Fatalf("entries left: %v", left)
	}
	if tag := w.git(w.work, "ls-remote", "--tags", "origin", "v0.2.0"); tag == "" {
		t.Fatal("no tag")
	}
	if len(w.hub.releases) != 1 || w.hub.releases[0].Draft || !strings.Contains(w.hub.releases[0].Body, "Activity timeline") {
		t.Fatalf("releases %+v", w.hub.releases)
	}
	message := w.git(w.work, "log", "-1", "--format=%B")
	if !strings.HasSuffix(message, "Jackstay-Release: 0.2.0") || !strings.Contains(message, "Internal tidy") {
		t.Fatalf("message %q", message)
	}
	// A second run finds nothing to do, and the history proves the release.
	flow, out = w.flow()
	if code, err := flow.Publish("main"); err != nil || code != 0 || strings.Contains(out.String(), "tagged") {
		t.Fatalf("second publish: %d %v\n%s", code, err, out)
	}
	repo := NewRepo(w.work)
	state, err := repo.StateAt("HEAD")
	if err != nil || state.History.Last().String() != "0.2.0" || len(state.History.Unproven) != 0 {
		t.Fatalf("history %+v %v", state.History, err)
	}
	// Nothing to ask for now: the pull request closes and the branch goes.
	flow, out = w.flow()
	if code, err := flow.PullRequestFlow("main"); err != nil || code != 0 {
		t.Fatalf("pr after: %d %v\n%s", code, err, out)
	}
}

func TestLateEntryReleasesNothing(t *testing.T) {
	w := newWorld(t)
	flow, out := w.flow()
	if code, err := flow.PullRequestFlow("main"); err != nil || code != 0 {
		t.Fatalf("pr: %d %v\n%s", code, err, out)
	}
	// An entry lands before the owner merges the request.
	w.write("changes/late.md", "---\n{\"type\": \"breaking\", \"audience\": \"consumer\"}\n---\n# Late\n\n## Migration\n\nDo it.\n")
	w.land("late entry")
	w.mergeRequest()
	flow, out = w.flow()
	if code, err := flow.Publish("main"); err != nil || code != 0 || !strings.Contains(out.String(), "releases nothing") {
		t.Fatalf("publish: %d %v\n%s", code, err, out)
	}
	if tag := w.git(w.work, "ls-remote", "--tags", "origin"); tag != "" {
		t.Fatalf("tagged: %s", tag)
	}
	// Reverting the late entry does not bring the stale request back.
	w.git(w.work, "pull", "-q", "--ff-only", "origin", "main")
	w.git(w.work, "rm", "-q", "changes/late.md")
	w.land("revert late")
	flow, out = w.flow()
	if code, err := flow.Publish("main"); err != nil || code != 0 || !strings.Contains(out.String(), "when it was merged") {
		t.Fatalf("publish after revert: %d %v\n%s", code, err, out)
	}
	// The workflow's next request differs by made_on, and its merge releases.
	flow, out = w.flow()
	if code, err := flow.PullRequestFlow("main"); err != nil || code != 0 {
		t.Fatalf("pr again: %d %v\n%s", code, err, out)
	}
	w.mergeRequest()
	flow, out = w.flow()
	if code, err := flow.Publish("main"); err != nil || code != 0 || !strings.Contains(out.String(), "pushed the release commit") {
		t.Fatalf("publish again: %d %v\n%s", code, err, out)
	}
}

func TestTrailerWithoutProofIsReported(t *testing.T) {
	w := newWorld(t)
	w.write("other.txt", "x")
	w.git(w.work, "add", "-A")
	w.git(w.work, "commit", "-q", "-m", "fake\n\nJackstay-Release: 9.9.9")
	w.git(w.work, "push", "-q", "origin", "main")
	flow, out := w.flow()
	if code, _ := flow.Publish("main"); code != 1 || !strings.Contains(out.String(), "no release commit") {
		t.Fatalf("got %d\n%s", code, out)
	}
}

func TestShallowCloneIsRefused(t *testing.T) {
	w := newWorld(t)
	shallow := filepath.Join(t.TempDir(), "shallow")
	w.git(w.work, "clone", "-q", "--depth", "1", "file://"+w.remote, shallow)
	if _, err := NewRepo(shallow).StateAt("HEAD"); err == nil || !strings.Contains(err.Error(), "shallow") {
		t.Fatalf("got %v", err)
	}
}

func TestYankMarksTheTitle(t *testing.T) {
	w := newWorld(t)
	release := func() {
		flow, out := w.flow()
		if code, err := flow.PullRequestFlow("main"); err != nil || code != 0 {
			t.Fatalf("pr: %d %v\n%s", code, err, out)
		}
		w.mergeRequest()
		flow, out = w.flow()
		if code, err := flow.Publish("main"); err != nil || code != 0 {
			t.Fatalf("publish: %d %v\n%s", code, err, out)
		}
		w.git(w.work, "pull", "-q", "--ff-only", "origin", "main")
	}
	release() // 0.2.0
	w.write("changes/second.md", "---\n{\"type\": \"fix\", \"audience\": \"consumer\"}\n---\n# Second\n")
	w.land("second")
	release() // 0.2.1
	if len(w.hub.releases) != 2 {
		t.Fatalf("releases %+v", w.hub.releases)
	}
	w.write("changes/yank-0-2-1.md", "---\n{\"type\": \"note\", \"audience\": \"consumer\", \"yanks\": \"0.2.1\"}\n---\n# 0.2.1 is yanked\n\nIt broke.\n")
	w.land("yank")
	flow, out := w.flow()
	if code, err := flow.Publish("main"); err != nil || code != 0 {
		t.Fatalf("publish: %d %v\n%s", code, err, out)
	}
	if !strings.HasSuffix(w.hub.releases[1].Name, "(yanked)") || strings.HasSuffix(w.hub.releases[0].Name, "(yanked)") {
		t.Fatalf("after yank: %+v", w.hub.releases)
	}
}

func TestVersionChangedOutsideARelease(t *testing.T) {
	w := newWorld(t)
	w.write("VERSION", "0.5.0\n")
	w.land("by hand")
	flow, out := w.flow()
	if code, err := flow.Publish("main"); code != 2 || err == nil || !strings.Contains(err.Error(), "the version files say 0.5.0") {
		t.Fatalf("got %d %v\n%s", code, err, out)
	}
}

func TestPushLeasedSeesARefusal(t *testing.T) {
	w := newWorld(t)
	repo := NewRepo(w.runner)
	w.git(w.runner, "fetch", "-q", "origin")
	before := w.git(w.runner, "rev-parse", "origin/main")
	w.write("other.txt", "x")
	w.land("moves main")
	commit, err := repo.commitOn(before, map[string][]byte{"z.txt": []byte("z")}, nil, "x\n", Identity{Name: "t", Email: "t@t"})
	if err != nil {
		t.Fatal(err)
	}
	refused, err := repo.pushLeased("origin", "main", commit, before)
	if err != nil || !refused {
		t.Fatalf("refused %v err %v", refused, err)
	}
}

func TestSparkleKeepsTheMigration(t *testing.T) {
	entry, _ := changes.Parse("a.md", []byte("---\n{\"type\": \"breaking\", \"audience\": \"consumer\"}\n---\n# Rows <new>\n\n## Migration\n\nUse the builder.\n"))
	out, err := RenderNotes([]changes.Entry{entry}, "1.0.0", "sparkle-html")
	if err != nil || !strings.Contains(out, "Migration:</strong> Use the builder.") || !strings.Contains(out, "Rows &lt;new&gt;") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestProofNeedsTheVersionToMove(t *testing.T) {
	w := newWorld(t)
	flow, out := w.flow()
	if code, err := flow.PullRequestFlow("main"); err != nil || code != 0 {
		t.Fatalf("pr: %d %v\n%s", code, err, out)
	}
	w.mergeRequest()
	w.git(w.work, "pull", "-q", "--ff-only", "origin", "main")
	// By hand: the version first, then a trailer commit that removes the request and entries and
	// writes the changelog without moving the version.
	w.write("VERSION", "0.2.0\n")
	w.git(w.work, "add", "-A")
	w.git(w.work, "commit", "-q", "-m", "version by hand")
	w.git(w.work, "rm", "-q", "changes/_release-request.json", "changes/activity.md", "changes/tidy.md")
	w.write("CHANGELOG.md", "# Changelog\n\n## 0.2.0\n")
	w.git(w.work, "add", "-A")
	w.git(w.work, "commit", "-q", "-m", "fake release\n\nJackstay-Release: 0.2.0")
	repo := NewRepo(w.work)
	history, err := repo.HistoryOf("HEAD", Version{Minor: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Releases) != 0 || len(history.Unproven) != 1 {
		t.Fatalf("history %+v", history)
	}
}

func TestAForkPullRequestOnTheReleaseBranchIsLeftAlone(t *testing.T) {
	w := newWorld(t)
	w.hub.next = 1
	w.hub.pulls[1] = &PullRequest{Number: 1, Head: "jackstay/release", Base: "main", State: "open", HeadRepository: "someone/p", BaseRepository: "o/p"}
	flow, out := w.flow()
	if code, err := flow.PullRequestFlow("main"); err != nil || code != 0 {
		t.Fatalf("pr: %d %v\n%s", code, err, out)
	}
	if w.hub.pulls[1].State != "open" || len(w.hub.pulls) != 2 {
		t.Fatalf("pulls %+v", w.hub.pulls)
	}
}

func TestAMalformedTemplateIsRefused(t *testing.T) {
	w := newWorld(t)
	withTemplate := strings.Replace(projectJSON, `"heading"`, `"template": "entry.md", "heading"`, 1)
	w.write("jackstay.json", withTemplate)
	w.write("entry.md", "---\n{not json}\n---\n# {slug}\n")
	w.land("template")
	loaded, err := changes.LoadProjectAt(w.work)
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := SettingsOf(loaded.Config)
	if problems := Validate(w.work, NewRepo(w.work), settings); len(problems) == 0 {
		t.Fatal("a malformed template passed")
	}
}

func TestALinkedVersionFileStopsPublishing(t *testing.T) {
	w := newWorld(t)
	w.write("real-version", "0.1.0\n")
	if err := os.Remove(filepath.Join(w.work, "VERSION")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real-version", filepath.Join(w.work, "VERSION")); err != nil {
		t.Fatal(err)
	}
	w.land("a linked VERSION")
	flow, out := w.flow()
	if code, err := flow.Publish("main"); code != 2 || err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("got %d %v\n%s", code, err, out)
	}
}

func TestPathsAreLiteral(t *testing.T) {
	w := newWorld(t)
	w.write("changesX/keep.md", "keep me\n")
	w.land("a folder a pattern would match")
	repo := NewRepo(w.work)
	if _, err := repo.run("rm", "-q", "--", "changes*/keep.md"); err == nil {
		t.Fatal("a literal path matched as a pattern")
	}
	if _, err := os.Stat(filepath.Join(w.work, "changesX", "keep.md")); err != nil {
		t.Fatal(err)
	}
}

func TestABadTagNameStopsTheWorkflowBeforeAnything(t *testing.T) {
	w := newWorld(t)
	w.write("jackstay.json", strings.Replace(projectJSON, `"first": "0.1.0"`, `"first": "0.1.0", "tag": "bad..{version}"`, 1))
	w.land("bad tag")
	flow, out := w.flow()
	if code, err := flow.PullRequestFlow("main"); code != 2 || err == nil || !strings.Contains(err.Error(), "ref name") {
		t.Fatalf("got %d %v\n%s", code, err, out)
	}
	if len(w.hub.pulls) != 0 {
		t.Fatal("a pull request was opened")
	}
}

func TestAYankOfNoReleaseIsInvalid(t *testing.T) {
	w := newWorld(t)
	w.write("changes/yank.md", "---\n{\"type\": \"note\", \"audience\": \"consumer\", \"yanks\": \"0.9.0\"}\n---\n# Yank\n")
	w.land("yank nothing")
	repo := NewRepo(w.work)
	state, err := repo.StateAt("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Prepare(state); err == nil || !strings.Contains(err.Error(), "no release") {
		t.Fatalf("got %v", err)
	}
}
