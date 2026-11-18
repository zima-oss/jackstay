package guard

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/discard"
)

// No row's advice leads to a refusal (W-052, docs/design/discard-and-clean-gate.md section 2):
// every command an `Instead` names passes in the situation the row refused, and a step taken
// "first" or "before" is followed by a command that then passes.

// adviceFixture is a repository in the situation the rows refuse: a tracked file edited, another
// staged, an untracked file, a folder holding an ignored file, a branch `done` merged into main,
// a branch `topic` whose commit nothing else holds, a remote `origin`, a stash entry.
type adviceFixture struct {
	folder, scratch, trash string
}

func newAdviceFixture(t *testing.T) adviceFixture {
	folder := realFolder(t)
	scratch := realFolder(t)
	gitIn(t, folder, "init", "-q", "-b", "main")
	write(t, filepath.Join(folder, "file.txt"), "one\n")
	write(t, filepath.Join(folder, "staged.txt"), "one\n")
	write(t, filepath.Join(folder, "out", "kept.txt"), "kept\n")
	write(t, filepath.Join(folder, ".gitignore"), "out/build.log\n")
	gitIn(t, folder, "add", "-A")
	gitIn(t, folder, "commit", "-qm", "one")
	gitIn(t, folder, "branch", "done")
	write(t, filepath.Join(folder, "file.txt"), "two\n")
	gitIn(t, folder, "commit", "-qam", "two")
	bare := filepath.Join(realFolder(t), "origin.git")
	gitIn(t, folder, "clone", "-q", "--bare", folder, bare)
	gitIn(t, folder, "remote", "add", "origin", bare)
	gitIn(t, folder, "fetch", "-q", "origin")
	gitIn(t, folder, "checkout", "-q", "-b", "topic") // after the remote: nothing else holds its commit
	write(t, filepath.Join(folder, "file.txt"), "topic\n")
	gitIn(t, folder, "commit", "-qam", "topic")
	gitIn(t, folder, "checkout", "-q", "main")
	write(t, filepath.Join(folder, "stashed.txt"), "s\n")
	gitIn(t, folder, "add", "stashed.txt")
	gitIn(t, folder, "stash", "push", "-q")
	write(t, filepath.Join(folder, "file.txt"), "my edit\n")
	write(t, filepath.Join(folder, "staged.txt"), "staged edit\n")
	gitIn(t, folder, "add", "staged.txt")
	write(t, filepath.Join(folder, "new.txt"), "untracked\n")
	write(t, filepath.Join(folder, "out", "build.log"), "ignored\n")
	return adviceFixture{folder: folder, scratch: scratch, trash: filepath.Join(realFolder(t), ".trash")}
}

func (fixture adviceFixture) decide(command string) []string {
	return Decide(command, Settings{Folder: fixture.folder, Environ: map[string]string{"TMPDIR": "/var/folders/ab/cdef/T/"},
		Scratch: []string{fixture.scratch}, Home: "/Users/someone", ProjectRoot: fixture.folder, Git: NewGit(nil),
		Guard: map[string]any{"rules": everyRow("refuse")}}).Rules()
}

func (fixture adviceFixture) fill(snippet string) string {
	replacer := strings.NewReplacer("<path>", "file.txt", "<name>", "done", "<branch>", "done", "<commit>", "HEAD",
		"<tree>", "HEAD", "<remote>", "origin", "<entry>", "20261117-000000-1", "<scratch>", fixture.scratch,
		"<what you push>", "HEAD", "<checkout>", fixture.folder, "origin/<branch>", "origin/main", "…", ".")
	return replacer.Replace(snippet)
}

var quoted = regexp.MustCompile("`([^`]+)`")

// commandWords are the programs whose snippets are commands; everything else must be a known
// fragment (an option, a Card line) so a new row's advice cannot slip by unchecked.
var commandWords = []string{"git", "jst", "find", "cp"}

var fragments = []string{"cards", "reword", "Card: <ID>", "Card: none (why)", "env -S", "no matches found"}

// thenCommand says whether a snippet is the command a "first … then" advice leads to: it is
// checked by a scenario, after the step, not in the situation the row refused.
func thenCommand(text string, at int) bool {
	before := text[:at]
	first := strings.LastIndex(before, "first")
	then := strings.LastIndex(before, "then ")
	return first >= 0 && then > first && strings.LastIndex(before, ";") < then
}

func adviceTexts(t *testing.T) []string {
	var texts []string
	for _, row := range table {
		texts = append(texts, row.ID+"\x00"+row.Instead)
	}
	for _, one := range readCorpus(t) {
		settings := settingsOf(one)
		for _, finding := range Decide(one.Line, settings).Findings {
			if finding.Rule == "zsh-glob" || strings.HasPrefix(finding.Rule, "project:") || finding.Rule == ruleUnparsable ||
				finding.Rule == ruleTooLong || finding.Rule == ruleTooDeep {
				continue // zsh-glob's advice is the line itself, quoted (TestLiveZshPatterns); the others are not rows
			}
			texts = append(texts, finding.Rule+"\x00"+finding.Instead)
		}
	}
	slices.Sort(texts)
	return slices.Compact(texts)
}

func TestEveryCommandAnAdviceNamesPasses(t *testing.T) {
	fixture := newAdviceFixture(t)
	checked := map[string]bool{}
	for _, text := range adviceTexts(t) {
		row, instead, _ := strings.Cut(text, "\x00")
		for _, match := range quoted.FindAllStringSubmatchIndex(instead, -1) {
			snippet := instead[match[2]:match[3]]
			first := strings.Fields(snippet)[0]
			switch {
			case strings.HasPrefix(snippet, "-") || slices.Contains(fragments, snippet):
				continue
			case !slices.Contains(commandWords, first):
				t.Errorf("%s: %q is neither a command nor a known fragment", row, snippet)
				continue
			case thenCommand(instead, match[0]):
				continue
			}
			command := fixture.fill(snippet)
			if checked[command] {
				continue
			}
			checked[command] = true
			if got := fixture.decide(command); len(got) > 0 {
				t.Errorf("%s advises %q, which the guard refuses here: %v", row, command, got)
			}
		}
	}
	if len(checked) < 25 {
		t.Fatalf("only %d commands were checked: %v", len(checked), checked)
	}
}

// A scenario is a row whose advice takes a step first: the row refuses, the step is taken for
// real, and the command the advice leads to then passes.
type scenario struct {
	row, refused string
	// step is the advice's own snippet for the step, as the row writes it; steps takes it.
	step  string
	steps func(t *testing.T, fixture adviceFixture)
	// then is the command run after the step: the refused one, or the one the advice names as
	// thenAdvice (as the row writes it).
	then, thenAdvice string
}

func runShell(t *testing.T, folder, text string) {
	t.Helper()
	command := exec.Command("sh", "-c", text)
	command.Dir = folder
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", text, err, out)
	}
}

func discardFor(t *testing.T, fixture adviceFixture, paths ...string) {
	t.Helper()
	var out, errs bytes.Buffer
	run := discard.Run{Working: fixture.folder, Trash: fixture.trash, Project: "p", KeepDays: 7, Now: time.Now, Out: &out, Errors: &errs}
	if code := run.Discard(context.Background(), paths); code != 0 {
		t.Fatalf("jst discard %v: %d %s", paths, code, errs.String())
	}
}

var scenarios = []scenario{
	{row: "git-checkout-path", refused: "git checkout HEAD~1 -- file.txt", step: "jst discard <path>",
		steps: func(t *testing.T, fixture adviceFixture) { discardFor(t, fixture, "file.txt") }, then: "git checkout HEAD~1 -- file.txt"},
	{row: "git-checkout-path", refused: "git checkout HEAD~1 -- out", step: "jst trash <path>",
		steps: func(t *testing.T, fixture adviceFixture) {
			discardFor(t, fixture, "out")
			runShell(t, fixture.folder, "mv out/build.log '"+fixture.scratch+"/'") // as `jst trash` would, without jackstay's home
		}, then: "git checkout HEAD~1 -- out"},
	{row: "git-restore-worktree", refused: "git restore file.txt", step: "jst discard <path>",
		steps: func(t *testing.T, fixture adviceFixture) { discardFor(t, fixture, "file.txt") }, then: "git restore file.txt"},
	{row: "git-read-tree-reset", refused: "git read-tree --reset -u HEAD", step: "jst discard <path>",
		steps: func(t *testing.T, fixture adviceFixture) { discardFor(t, fixture, ".") }, then: "git read-tree -u -m HEAD",
		thenAdvice: "git read-tree -u -m <tree>"},
	{row: "git-branch-reset", refused: "git branch -f topic main", step: "git tag archive/<name> <name>",
		steps: func(t *testing.T, fixture adviceFixture) { runShell(t, fixture.folder, "git tag archive/topic topic") },
		then:  "git branch -f topic main"},
	{row: "git-branch-force-delete", refused: "git branch -D topic", step: "git tag archive/<name> <name>",
		steps: func(t *testing.T, fixture adviceFixture) { runShell(t, fixture.folder, "git tag archive/topic topic") },
		then:  "git branch -D topic"},
	{row: "find-delete", refused: "find new.txt -delete", step: "find … -print",
		steps: func(t *testing.T, fixture adviceFixture) {}, then: "jst trash new.txt", thenAdvice: "jst trash"},
}

func TestEveryStepTakenFirstLeadsToACommandThatPasses(t *testing.T) {
	covered := map[string]bool{}
	for _, each := range scenarios {
		fixture := newAdviceFixture(t)
		if got := fixture.decide(each.refused); !slices.Contains(got, each.row) {
			t.Errorf("%s: %q is not refused by the row here: %v", each.row, each.refused, got)
			continue
		}
		advice := rowOf(each.row).Instead
		if !strings.Contains(advice, "`"+each.step+"`") {
			t.Errorf("%s: the scenario's step %q is not the row's advice: %q", each.row, each.step, advice)
		}
		if (each.thenAdvice == "" && each.then != each.refused) || (each.thenAdvice != "" && !strings.Contains(advice, "`"+each.thenAdvice+"`")) {
			t.Errorf("%s: %q is neither the refused command nor one the advice names", each.row, each.then)
		}
		each.steps(t, fixture)
		if got := fixture.decide(each.then); len(got) > 0 {
			t.Errorf("%s: after the advised step, %q is still refused: %v", each.row, each.then, got)
		}
		covered[each.row] = true
	}
	// Its step is mending jackstay.json; after it a push is judged by cards-push, whose advice is walked.
	covered["card-push-unreadable-config"] = true
	for _, row := range table {
		says := strings.Contains(row.Instead, "first") || strings.Contains(row.Instead, "before")
		if says && !covered[row.ID] {
			t.Errorf("%s advises a step first and has no scenario: %q", row.ID, row.Instead)
		}
	}
}
