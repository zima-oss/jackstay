package adopt

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/adopt/templates"
	"github.com/zima-oss/jackstay/internal/changes"
	"github.com/zima-oss/jackstay/internal/delivery"
)

// The guard hook exactly as the three adopted projects carry it (adoption survey, section 1).
const projectsSettings = `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{"type": "command", "command": "\"$HOME/.local/bin/jst-guard\"", "timeout": 10}]
      }
    ]
  }
}
`

func project(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func optionsFor(root string) Options {
	return Options{Dir: root, WorkingDir: root, Home: root, Has: func(string) bool { return true }, Version: "0.5.0"}
}

func plan(t *testing.T, options Options) *Plan {
	t.Helper()
	made, err := Make(options)
	if err != nil {
		t.Fatal(err)
	}
	return made
}

func apply(t *testing.T, options Options) *Plan {
	t.Helper()
	made := plan(t, options)
	if err := made.Apply(); err != nil {
		t.Fatalf("apply: %v (conflicts %v)", err, ids(made.Conflicts))
	}
	return made
}

func TestApplyPreservesEditsMadeAfterPlanning(t *testing.T) {
	for _, existing := range []bool{false, true} {
		files := map[string]string{"jackstay.json": `{"jackstay_schema":2}`}
		if existing {
			files["AGENTS.md"] = "original instructions\n"
		}
		root := project(t, files)
		made := plan(t, optionsFor(root))
		target := filepath.Join(root, "AGENTS.md")
		const edited = "instructions written by another session\n"
		if err := os.WriteFile(target, []byte(edited), 0o600); err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, root)
		if err := made.Apply(); err == nil || !strings.Contains(err.Error(), "AGENTS.md") {
			t.Fatalf("stale plan was accepted: %v", err)
		}
		after := snapshot(t, root)
		if len(after) != len(before) {
			t.Fatalf("stale plan wrote extra files: %v", after)
		}
		for name, prior := range before {
			if !bytes.Equal(prior.data, after[name].data) {
				t.Errorf("stale plan overwrote %s", name)
			}
		}
		apply(t, optionsFor(root))
		content, err := os.ReadFile(target)
		if err != nil || !strings.Contains(string(content), edited) {
			t.Fatalf("fresh plan did not preserve the edit: %v, %q", err, content)
		}
	}
}

func TestApplyRejectsAProfileChangedAfterPlanning(t *testing.T) {
	const before = `{"jackstay_profile":1,"name":"policy","agents":{"rules":["Original policy."]}}`
	const after = `{"jackstay_profile":1,"name":"policy","agents":{"rules":["Updated policy."]}}`
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema":2}`, "profile.json": before})
	options := optionsFor(root)
	options.Profile = filepath.Join(root, "profile.json")
	made := plan(t, options)
	if err := os.WriteFile(options.Profile, []byte(after), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := made.Apply(); err == nil || !strings.Contains(err.Error(), "profile changed") {
		t.Fatalf("stale profile was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale plan wrote AGENTS.md: %v", err)
	}
}

func TestAdoptReplaysEachInterruptedFileBoundary(t *testing.T) {
	for _, stopAfter := range []int{1, 2, 3} {
		root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema":2}`})
		options := optionsFor(root)
		initial := plan(t, options)
		if len(initial.writes) != 3 {
			t.Fatalf("fixture should have three writes, got %d", len(initial.writes))
		}
		place, err := deliveryPath(root)
		if err != nil {
			t.Fatal(err)
		}
		written := map[string]int{}
		total := 0
		stopped := errors.New("stopped after applying a file")
		recordWrite := func(path string, data []byte) error {
			if err := changes.WriteFileAtomic(path, data); err != nil {
				return err
			}
			written[path]++
			total++
			if total == stopAfter {
				return stopped
			}
			return nil
		}
		if err := initial.apply(recordWrite); !errors.Is(err, stopped) {
			t.Fatalf("attempt did not stop at %d: %v", stopAfter, err)
		}
		first, _, err := delivery.Read[adoptionDelivery](place)
		if err != nil || first.Attempt != 1 || first.Outcome != delivery.Interrupted {
			t.Fatalf("did not record the interrupted attempt: %+v %v", first, err)
		}
		fresh := plan(t, options)
		if err := fresh.apply(recordWrite); err != nil {
			t.Fatal(err)
		}
		second, _, err := delivery.Read[adoptionDelivery](place)
		if err != nil || second.ID != first.ID || second.Attempt != 2 || second.Outcome != delivery.Complete {
			t.Fatalf("did not complete the original delivery: %+v %v", second, err)
		}
		if len(written) != 3 {
			t.Fatalf("wrong number of effects: %v", written)
		}
		for path, count := range written {
			if count != 1 {
				t.Fatalf("wrote %s %d times", path, count)
			}
		}
	}
}

func TestRelativeProfileInASymlinkedWorkingDirectory(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema":2}`})
	exampleProfile(t, root, `{}`, nil)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	options := optionsFor(root)
	options.WorkingDir, options.Profile = alias, "profiles/profile.json"
	if made := plan(t, options); made.Profile != options.Profile {
		t.Fatalf("recorded %q, want %q", made.Profile, options.Profile)
	}
}

func ids(conflicts []Conflict) []string {
	var out []string
	for _, conflict := range conflicts {
		out = append(out, conflict.ID)
	}
	return out
}

func changeIDs(changes []Change) []string {
	var out []string
	for _, change := range changes {
		out = append(out, change.ID)
	}
	return out
}

type fileState struct {
	data     []byte
	modified time.Time
}

func snapshot(t *testing.T, root string) map[string]fileState {
	t.Helper()
	files := map[string]fileState{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path) // #nosec G304 -- the test's own files
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[path] = fileState{data: data, modified: info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// applyTwice is the golden rule (spec section 13): the second apply writes nothing and the plan
// then exits clean.
func applyTwice(t *testing.T, options Options) {
	t.Helper()
	apply(t, options)
	before := snapshot(t, options.Dir)
	time.Sleep(10 * time.Millisecond)
	second := options
	second.Accept = nil
	again := apply(t, second)
	if len(again.Changes) != 0 {
		t.Fatalf("the second apply changed %v", changeIDs(again.Changes))
	}
	after := snapshot(t, options.Dir)
	if len(before) != len(after) {
		t.Fatalf("files before %d, after %d", len(before), len(after))
	}
	for path, state := range before {
		if !bytes.Equal(state.data, after[path].data) || !state.modified.Equal(after[path].modified) {
			t.Fatalf("the second apply touched %s", path)
		}
	}
	final := plan(t, second)
	if len(final.Changes)+len(final.Conflicts) != 0 {
		t.Fatalf("after two applies the plan still has %v %v", changeIDs(final.Changes), ids(final.Conflicts))
	}
}

func read(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name))) // #nosec G304 -- the test's own files
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAProjectWithEverySectionIsAdoptedOnceAndThenLeftAlone(t *testing.T) {
	root := project(t, map[string]string{
		"jackstay.json": `{"jackstay_schema": 2, "project": "sample", "cards": {"dir": "docs/cards", "id": "S-[0-9]+", "unit": "pr", "since": "2026-11-17"},
			"changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version}"}, "release": {"base": "trunk", "merge": "owner", "first": "0.1.0", "files": [{"path": "VERSION", "match": "^(?P<version>[0-9]+\\.[0-9]+\\.[0-9]+)$"}]}, "github": {"account": "someone"},
			"lock": {"enabled": true}, "guard": {"rules": {}}, "test": {"scopes": {"gate": {"run": ["just", "gate"], "judge": "exit"}}}}`,
		".claude/settings.json": projectsSettings,
		"CLAUDE.md":             "# sample\n\n@./AGENTS.md\n",
	})
	first := plan(t, optionsFor(root))
	if got := changeIDs(first.Changes); !slices.Equal(got, []string{"agents-block", "jackstay.json", "workflow:card-line", "workflow:release"}) {
		t.Fatalf("changes %v", got)
	}
	applyTwice(t, optionsFor(root))
	release := read(t, root, ".github/workflows/release.yml")
	if !strings.Contains(release, "branches: [trunk]") || !strings.Contains(release, `JACKSTAY_VERSION: "v0.5.0"`) {
		t.Fatalf("release.yml was not rendered with the project's values:\n%s", release)
	}
	if !strings.Contains(read(t, root, "jackstay.json"), `"pin": "v0.5.0"`) {
		t.Fatal("the pin was not recorded in jackstay.json")
	}
	if agents := read(t, root, "AGENTS.md"); !strings.Contains(agents, "jst lock -- COMMAND") || !strings.Contains(agents, "origin/main..HEAD") {
		t.Fatalf("the block lacks the project's items:\n%s", agents)
	}
}

func TestAnEmptyProjectGetsJackstayJSONTheBlockAndTheHook(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "bare"}`})
	first := plan(t, optionsFor(root))
	if got := changeIDs(first.Changes); !slices.Equal(got, []string{"agents-block", "claude-md", "hook:guard"}) {
		t.Fatalf("changes %v", got)
	}
	applyTwice(t, optionsFor(root))
	if read(t, root, "CLAUDE.md") != "@AGENTS.md\n" {
		t.Fatal("CLAUDE.md")
	}
	if !strings.Contains(read(t, root, ".claude/settings.json"), "jst-guard") {
		t.Fatal("no hook")
	}
}

func TestAnEditInsideTheMarkersIsNeverOverwrittenSilently(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "edit"}`, ".claude/settings.json": projectsSettings,
		"CLAUDE.md": "@AGENTS.md\n"})
	apply(t, optionsFor(root))
	path := filepath.Join(root, "AGENTS.md")
	edited := strings.Replace(read(t, root, "AGENTS.md"), "- **Wait**", "- **Wait** (our note)", 1)
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	second := plan(t, optionsFor(root))
	if got := ids(second.Conflicts); !slices.Equal(got, []string{"agents:edited"}) {
		t.Fatalf("conflicts %v", got)
	}
	options := optionsFor(root)
	options.Accept = []string{"agents:edited"}
	applyTwice(t, options)
	if strings.Contains(read(t, root, "AGENTS.md"), "our note") {
		t.Fatal("--accept did not rewrite the block")
	}
}

func TestMalformedMarkersAndAnUnreadableSettingsFileCannotBeExcepted(t *testing.T) {
	root := project(t, map[string]string{
		"jackstay.json":         `{"jackstay_schema": 2, "project": "broken", "adopt": {"exceptions": {"agents:markers": "x", "hook:settings-unreadable": "y"}}}`,
		"AGENTS.md":             "<!-- jackstay-adopt: begin agents-block sha256:000000000000 -->\nno end\n",
		".claude/settings.json": "[1, 2]\n",
	})
	made := plan(t, optionsFor(root))
	if got := ids(made.Conflicts); !slices.Equal(got, []string{"agents:markers", "hook:settings-unreadable"}) {
		t.Fatalf("conflicts %v", got)
	}
	if len(made.Exceptions) != 0 {
		t.Fatal("a malformed file was excepted")
	}
}

func TestWidgetsHandMadeCardLineIsUnmarkedAndAnExceptionKeepsIt(t *testing.T) {
	files := map[string]string{
		"jackstay.json":                   `{"jackstay_schema": 2, "project": "cards", "cards": {"dir": "docs/cards", "id": "C-[0-9]+", "unit": "pr", "since": "2026-11-17"}}`,
		".github/workflows/card-line.yml": "name: card-line\n# copied by hand\n",
		".github/workflows/pr-checks.yml": "name: pr-checks\n",
		".claude/settings.json":           projectsSettings, "CLAUDE.md": "@AGENTS.md\n", "AGENTS.md": "",
	}
	root := project(t, files)
	made := plan(t, optionsFor(root))
	if got := ids(made.Conflicts); !slices.Equal(got, []string{"workflow:card-line:unmarked"}) {
		t.Fatalf("conflicts %v", got)
	}
	if !slices.ContainsFunc(made.Notes, func(note string) bool { return strings.Contains(note, "pr-checks.yml is the project's own") }) {
		t.Fatalf("the project's own workflow is not listed: %v", made.Notes)
	}
	kept := project(t, files)
	if err := os.WriteFile(filepath.Join(kept, "jackstay.json"), []byte(`{"jackstay_schema": 2, "project": "cards", "cards": {"dir": "docs/cards", "id": "C-[0-9]+", "unit": "pr", "since": "2026-11-17"},
		"adopt": {"exceptions": {"workflow:card-line:unmarked": "ours runs on our runners", "file:gone:present": "stale"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	keptPlan := plan(t, optionsFor(kept))
	if len(keptPlan.Conflicts) != 0 || len(keptPlan.Exceptions) != 1 || keptPlan.Exceptions[0].Why != "ours runs on our runners" {
		t.Fatalf("%+v", keptPlan)
	}
	if !slices.ContainsFunc(keptPlan.Notes, func(note string) bool {
		return strings.Contains(note, "file:gone:present") && strings.Contains(note, "stale")
	}) {
		t.Fatalf("a stale exception is not noted: %v", keptPlan.Notes)
	}
	applyTwice(t, optionsFor(kept))
	if read(t, kept, ".github/workflows/card-line.yml") != "name: card-line\n# copied by hand\n" {
		t.Fatal("an excepted file was written")
	}
	accepted := project(t, files)
	options := optionsFor(accepted)
	options.Accept = []string{"workflow:card-line:unmarked"}
	applyTwice(t, options)
	if marked, _, intact := templates.Header(read(t, accepted, ".github/workflows/card-line.yml")); !marked || !intact {
		t.Fatal("--accept did not write jackstay's template")
	}
}

func TestAcceptNeedsAConflictThatIsThereAndSettledByAccept(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "x"}`, ".claude/settings.json": "[]"})
	options := optionsFor(root)
	options.Accept = []string{"agents:edited"}
	var usage *UsageError
	if _, err := Make(options); !errors.As(err, &usage) || !strings.Contains(err.Error(), "no such conflict") {
		t.Fatalf("got %v", err)
	}
	options.Accept = []string{"hook:settings-unreadable"}
	if _, err := Make(options); !errors.As(err, &usage) || !strings.Contains(err.Error(), "not by --accept") {
		t.Fatalf("got %v", err)
	}
}

func TestTheHookIsAddedBesideOtherHooksAndNotTwice(t *testing.T) {
	root := project(t, map[string]string{
		"jackstay.json": `{"jackstay_schema": 2, "project": "hooks"}`, "CLAUDE.md": "@AGENTS.md\n",
		".claude/settings.json": `{"model": "x", "hooks": {"PreToolUse": [{"matcher": "Edit", "hooks": []}], "Stop": []}}`,
	})
	applyTwice(t, optionsFor(root))
	settings := read(t, root, ".claude/settings.json")
	if strings.Index(settings, `"model"`) > strings.Index(settings, `"hooks"`) || !strings.Contains(settings, `"Stop"`) ||
		!strings.Contains(settings, `"Edit"`) || strings.Count(settings, "jst-guard") != 1 {
		t.Fatalf("settings:\n%s", settings)
	}
	present := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "hooks"}`, ".claude/settings.json": projectsSettings})
	for _, change := range plan(t, optionsFor(present)).Changes {
		if change.ID == "hook:guard" {
			t.Fatal("the projects' own hook line was not recognised")
		}
	}
}

func exampleProfile(t *testing.T, root string, defaults string, workflows map[string]string) string {
	t.Helper()
	folder := filepath.Join(root, "profiles")
	if err := os.MkdirAll(folder, 0o750); err != nil {
		t.Fatal(err)
	}
	entries := []string{}
	for name, text := range workflows {
		if err := os.WriteFile(filepath.Join(folder, name+".yml"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, `"`+name+`": {"file": "`+name+`.yml"}`)
	}
	slices.Sort(entries)
	text := `{"jackstay_profile": 1, "name": "ours", "jackstay_json": {"defaults": ` + defaults + `},
		"workflows": {` + strings.Join(entries, ", ") + `}, "agents": {"rules": ["Our rule."]}}`
	path := filepath.Join(folder, "profile.json")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestADefaultThatEnablesAnItemLandsWithItInOneApply(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 1, "project": "p"}`, ".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	exampleProfile(t, root, `{"lock": {"enabled": true}}`, nil)
	options := optionsFor(root)
	options.Profile = "profiles/profile.json"
	applyTwice(t, options)
	text := read(t, root, "jackstay.json")
	if !strings.Contains(text, `"jackstay_schema": 2`) || !strings.Contains(text, `"profile": "profiles/profile.json"`) {
		t.Fatalf("jackstay.json:\n%s", text)
	}
	if agents := read(t, root, "AGENTS.md"); !strings.Contains(agents, "jst lock -- COMMAND") || !strings.Contains(agents, "- Our rule.") {
		t.Fatalf("AGENTS.md:\n%s", agents)
	}
}

func TestADefaultBesideTheProjectsValueThatBreaksACheckIsAConflict(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "procs": {"default_minutes": 300}}`})
	exampleProfile(t, root, `{"procs": {"default_minutes": 30, "max_minutes": 240}}`, nil)
	options := optionsFor(root)
	options.Profile = "profiles/profile.json"
	made := plan(t, options)
	if !slices.Contains(ids(made.Conflicts), "jackstay.json:invalid") {
		t.Fatalf("conflicts %v", ids(made.Conflicts))
	}
	if slices.Contains(changeIDs(made.Changes), "jackstay.json") {
		t.Fatal("an invalid jackstay.json is planned for writing")
	}
}

func TestThePlanIsTheSameEveryTime(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 1, "project": "p"}`})
	exampleProfile(t, root, `{"review": {"round_limit": 6}, "procs": {"max_minutes": 240, "default_minutes": 30}, "lock": {"enabled": true}}`,
		map[string]string{"zeta": "name: z\n", "alpha": "name: a\n"})
	options := optionsFor(root)
	options.Profile = "profiles/profile.json"
	first := plan(t, options)
	for range 20 {
		again := plan(t, options)
		if !slices.Equal(changeIDs(first.Changes), changeIDs(again.Changes)) || len(first.writes) != len(again.writes) {
			t.Fatal("two plans of one input differ")
		}
		for index := range first.writes {
			if !bytes.Equal(first.writes[index].data, again.writes[index].data) {
				t.Fatalf("%s differs between two plans", first.writes[index].path)
			}
		}
	}
}

func TestAWorkflowNoTemplateWantsIsRemovedOrIsAConflictWhenEdited(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p"}`, ".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	profilePath := exampleProfile(t, root, `{}`, map[string]string{"deploy": "name: deploy\nenv:\n  JACKSTAY_VERSION: \"@@jackstay_version@@\"\n"})
	options := optionsFor(root)
	options.Profile = "profiles/profile.json"
	applyTwice(t, options)
	// The profile renames deploy to ship.
	if err := os.Remove(filepath.Join(root, "profiles", "deploy.yml")); err != nil {
		t.Fatal(err)
	}
	exampleProfile(t, root, `{}`, map[string]string{"ship": "name: ship\n"})
	_ = profilePath
	renamed := plan(t, optionsFor(root))
	if got := changeIDs(renamed.Changes); !slices.Equal(got, []string{"workflow:deploy", "workflow:ship"}) {
		t.Fatalf("changes %v, conflicts %v", got, ids(renamed.Conflicts))
	}
	applyTwice(t, optionsFor(root))
	if _, err := os.Stat(filepath.Join(root, ".github/workflows/deploy.yml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the renamed template's old file is still there")
	}
	// An edited marked file no template wants is a conflict.
	exampleProfile(t, root, `{}`, nil)
	ship := filepath.Join(root, ".github/workflows/ship.yml")
	if err := os.WriteFile(ship, []byte(read(t, root, ".github/workflows/ship.yml")+"# ours\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ids(plan(t, optionsFor(root)).Conflicts); !slices.Equal(got, []string{"workflow:ship:edited"}) {
		t.Fatalf("conflicts %v", got)
	}
}

func TestThePinMovesOnlyWithPin(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version}"}, ` +
		`"release": {"merge": "owner", "first": "0.1.0", "files": [{"path": "VERSION", "match": "^(?P<version>[0-9]+\\.[0-9]+\\.[0-9]+)$"}]}}`, ".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	applyTwice(t, optionsFor(root))
	newer := optionsFor(root)
	newer.Version = "0.6.0"
	if made := plan(t, newer); len(made.Changes) != 0 {
		t.Fatalf("a newer jackstay moved the pin by itself: %v", changeIDs(made.Changes))
	}
	// A repaired workflow keeps its pin under a newer jackstay.
	release := filepath.Join(root, ".github/workflows/release.yml")
	if err := os.WriteFile(release, []byte(read(t, root, ".github/workflows/release.yml")+"# edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newer.Accept = []string{"workflow:release:edited"}
	apply(t, newer)
	if !strings.Contains(read(t, root, ".github/workflows/release.yml"), `JACKSTAY_VERSION: "v0.5.0"`) {
		t.Fatal("--accept moved the pin")
	}
	newer.Accept = nil
	newer.Pin = true
	applyTwice(t, newer)
	if !strings.Contains(read(t, root, ".github/workflows/release.yml"), `JACKSTAY_VERSION: "v0.6.0"`) {
		t.Fatal("--pin did not move the pin")
	}
}

func TestTheProfilesRulesBecomeConflicts(t *testing.T) {
	root := project(t, map[string]string{
		"jackstay.json": `{"jackstay_schema": 2, "project": "p", "test": {"scopes": {"quick": {"steps": ["unit"]}}, "steps": {"unit": {"run": ["make", "unit"]}}}, "receipts": {"toolchain": []}}`,
		"Makefile":      "all:\n",
	})
	folder := filepath.Join(root, "profiles")
	if err := os.MkdirAll(folder, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "profile.json"), []byte(`{"jackstay_profile": 1, "name": "ours",
		"files": {"required": [{"path": "justfile", "why": "recipes"}], "forbidden": [{"path": "Makefile", "why": "one runner", "instead": "just"}]},
		"scopes": {"required": ["gate"], "run_with": ["just"]},
		"jackstay_json": {"required": ["github.account"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	options := optionsFor(root)
	options.Profile = "profiles/profile.json"
	got := ids(plan(t, options).Conflicts)
	want := []string{"file:Makefile:present", "file:justfile:missing", "jackstay.json:github.account:missing", "scope:gate:missing", "scope:quick:runner"}
	if !slices.Equal(got, want) {
		t.Fatalf("conflicts %v, want %v", got, want)
	}
}

func TestAProfileThatCannotBeReadIsAUsageError(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "adopt": {"profile": "missing.json"}}`})
	var usage *UsageError
	if _, err := Make(optionsFor(root)); !errors.As(err, &usage) || !strings.Contains(err.Error(), "the profile") {
		t.Fatalf("got %v", err)
	}
	inProject, problems, _ := Check(root)
	if !inProject || len(problems) != 1 || !strings.Contains(problems[0], "cannot plan") {
		t.Fatalf("doctor: %v %v", inProject, problems)
	}
}

func TestDoctorsCheckReportsDriftAndKeepsExceptionsAsNotes(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "drift", "adopt": {"exceptions": {"hook:guard": "x"}}}`})
	inProject, problems, notes := Check(root)
	if !inProject || len(problems) != 1 || !strings.Contains(problems[0], "not up to date") || len(notes) != 0 {
		t.Fatalf("%v %v %v", inProject, problems, notes)
	}
	outside := t.TempDir()
	if inProject, _, _ := Check(outside); inProject {
		t.Fatal("a folder with no jackstay.json above it counted as a project")
	}
}

// A marked workflow renamed to .yaml is still jackstay's, and is removed.
func TestAMarkedWorkflowUnderAnotherExtensionIsInTheInventory(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p"}`, ".claude/settings.json": projectsSettings,
		"CLAUDE.md": "@AGENTS.md\n", ".github/workflows/deploy.yaml": templates.Marked("deploy", "name: deploy\n")})
	made := plan(t, optionsFor(root))
	if got := changeIDs(made.Changes); !slices.Contains(got, "workflow:deploy") {
		t.Fatalf("changes %v, notes %v", got, made.Notes)
	}
	applyTwice(t, optionsFor(root))
	if _, err := os.Stat(filepath.Join(root, ".github/workflows/deploy.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the marked .yaml workflow outlived its template")
	}
}

// A default that fills in part of a section the project has is judged
// on the merged file, not on its own.
func TestAPartialDefaultIsJudgedMerged(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version}"}}`,
		".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	exampleProfile(t, root, `{"changes": {"newest": "first"}}`, nil)
	options := optionsFor(root)
	options.Profile = "profiles/profile.json"
	options.Accept = nil
	made := plan(t, options)
	if len(made.Conflicts) != 0 {
		t.Fatalf("a valid merged file was judged invalid: %+v", made.Conflicts)
	}
}

// Release's rules are among the checks of the prospective file.
func TestReleaseRulesJudgeTheFile(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p",
		"changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version}"}, "release": {"tag": "latest"}}`})
	var usage *UsageError
	if _, err := Make(optionsFor(root)); !errors.As(err, &usage) || !strings.Contains(err.Error(), "{version}") {
		t.Fatalf("got %v", err)
	}
}

// An exception's reason may not be empty.
func TestAnEmptyExceptionReasonIsRefused(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "adopt": {"exceptions": {"file:Makefile:present": "  "}}}`})
	var usage *UsageError
	if _, err := Make(optionsFor(root)); !errors.As(err, &usage) || !strings.Contains(err.Error(), "adopt.exceptions") {
		t.Fatalf("got %v", err)
	}
}

// Issue #10: rule D0 lands jackstay.json alone, so a pin move goes in two pull requests: the
// settings (--settings-only), then, once they merged, a plain apply for the files.
func TestAPinMoveLandsSettingsFirst(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version}"}, ` +
		`"release": {"merge": "owner", "first": "0.1.0", "files": [{"path": "VERSION", "match": "^(?P<version>[0-9]+\\.[0-9]+\\.[0-9]+)$"}]}}`, ".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	applyTwice(t, optionsFor(root))
	release := filepath.Join(root, ".github/workflows/release.yml")
	if err := os.WriteFile(release, []byte(read(t, root, ".github/workflows/release.yml")+"# edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newer := optionsFor(root)
	newer.Version, newer.Pin = "0.6.0", true
	if made := plan(t, newer); len(made.Conflicts) == 0 {
		t.Fatal("the edited workflow is not a conflict")
	}
	// A file's conflict does not block the settings, and its --accept belongs to the second apply.
	newer.SettingsOnly, newer.Accept = true, []string{"workflow:release:edited"}
	if _, err := Make(newer); err == nil || !strings.Contains(err.Error(), "give it to the apply after the settings merge") {
		t.Fatalf("an --accept of a file with --settings-only: %v", err)
	}
	newer.Accept = nil
	made := plan(t, newer)
	if got := changeIDs(made.Changes); !slices.Equal(got, []string{"jackstay.json"}) || len(made.Conflicts) != 0 {
		t.Fatalf("--settings-only plans %v, conflicts %v", got, made.Conflicts)
	}
	var out bytes.Buffer
	apply(t, newer)
	writeApplied(&out, made, true)
	if !strings.Contains(out.String(), "then, on the merged base, run `jst adopt --apply` as before") {
		t.Fatalf("no next step:\n%s", out.String())
	}
	if !strings.Contains(read(t, root, "jackstay.json"), `"v0.6.0"`) || !strings.Contains(read(t, root, ".github/workflows/release.yml"), "# edit") {
		t.Fatal("--settings-only wrote more than jackstay.json")
	}
	// The settings merged: the apply of the files finds the conflict again and settles it.
	after := optionsFor(root)
	after.Version = "0.6.0"
	if made := plan(t, after); slices.Contains(changeIDs(made.Changes), "jackstay.json") || len(made.Conflicts) != 1 {
		t.Fatalf("after the settings: changes %v, conflicts %v", changeIDs(made.Changes), made.Conflicts)
	}
	after.Accept = []string{"workflow:release:edited"}
	applyTwice(t, after)
	if text := read(t, root, ".github/workflows/release.yml"); strings.Contains(text, "# edit") || !strings.Contains(text, `JACKSTAY_VERSION: "v0.6.0"`) {
		t.Fatal("the files did not follow the settings")
	}
}

func TestAnApplyOfBothPartsSaysToSplitThem(t *testing.T) {
	both := &Plan{Changes: []Change{{ID: "jackstay.json", Path: "jackstay.json"}, {ID: "workflow:release", Path: ".github/workflows/release.yml"}}}
	var out bytes.Buffer
	writeApplied(&out, both, false)
	if !both.SplitsSettings() || !strings.Contains(out.String(), "rule D0 refuses them in one pull request") {
		t.Fatalf("%s", out.String())
	}
	out.Reset()
	writeApplied(&out, &Plan{Project: "p", Changes: []Change{}}, true)
	if !strings.Contains(out.String(), "nothing to write in jackstay.json; `jst adopt --apply` writes the other files") {
		t.Fatalf("%s", out.String())
	}
}

func TestSettingsOnlyGoesWithPlanOrApply(t *testing.T) {
	if _, _, _, err := parseArgs([]string{"--block", "--settings-only"}); err == nil {
		t.Fatal("--block --settings-only: no error")
	}
	options, mode, _, err := parseArgs([]string{"--apply", "--pin", "--settings-only"})
	if err != nil || mode != "apply" || !options.SettingsOnly || !options.Pin {
		t.Fatalf("%+v %s %v", options, mode, err)
	}
}

// A settings-only plan with nothing left is empty arrays in JSON, as the documented shape says.
func TestASettingsOnlyPlanIsArraysInJSON(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p"}`, ".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	applyTwice(t, optionsFor(root))
	options := optionsFor(root)
	options.SettingsOnly = true
	encoded, err := json.Marshal(plan(t, options))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"changes":[]`, `"conflicts":[]`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("no %s in %s", want, encoded)
		}
	}
}

func TestSettingsOnlyLeavesOtherFileReadsForTheFilesStage(t *testing.T) {
	for _, test := range []struct {
		name string
		bad  func(*testing.T, string)
	}{
		{name: "AGENTS file is a directory", bad: func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, "AGENTS.md"), 0o750); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "workflows directory is a file", bad: func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, ".github"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".github", "workflows"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema":2,"project":"p",
				"cards":{"dir":"docs/cards","id":"C-[0-9]+","unit":"pr","since":"2026-11-17"}}`})
			options := optionsFor(root)
			test.bad(t, root)
			if _, err := Make(options); err == nil {
				t.Fatal("the files stage accepted the broken file")
			}
			options.SettingsOnly = true
			made := plan(t, options)
			if got := changeIDs(made.Changes); !slices.Equal(got, []string{"jackstay.json"}) || len(made.Conflicts) != 0 {
				t.Fatalf("settings plan: changes %v, conflicts %v", got, made.Conflicts)
			}
			if err := made.Apply(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReleaseWorkflowUsesCardsBranchWhenReleaseBaseIsOmitted(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema":2,"project":"p",
		"cards":{"dir":"docs/cards","id":"C-[0-9]+","unit":"pr","since":"2026-11-17","branch":"develop"},
		"changes":{"dir":"changes","into":"CHANGELOG.md","heading":"## {version}"},
		"release":{"first":"0.1.0","files":[{"path":"VERSION","match":"^(?P<version>[0-9]+\\.[0-9]+\\.[0-9]+)$"}]}}`})
	applyTwice(t, optionsFor(root))
	if text := read(t, root, ".github/workflows/release.yml"); !strings.Contains(text, "branches: [develop]") {
		t.Fatalf("release workflow did not use the effective base:\n%s", text)
	}
}

func TestAdoptParsesExpectedProfileArtifactDigest(t *testing.T) {
	digest := strings.Repeat("a", 64)
	options, mode, _, err := parseArgs([]string{"--plan", "--profile", "policy.bundle.json", "--profile-sha256", digest})
	if err != nil || mode != "plan" || options.Profile != "policy.bundle.json" || options.ProfileSHA256 != digest {
		t.Fatalf("profile pin arguments: %+v, %q, %v", options, mode, err)
	}
}

func TestSettingsOnlyPinsAProfileWorkflowBeforeApplyingIt(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema":2,"project":"p"}`})
	options := optionsFor(root)
	options.Profile = exampleProfile(t, root, `{}`, map[string]string{"custom": "name: custom\nenv:\n  VERSION: '@@jackstay_version@@'\n"})
	options.SettingsOnly = true
	if got := changeIDs(apply(t, options).Changes); !slices.Equal(got, []string{"jackstay.json"}) {
		t.Fatalf("settings plan changes: %v", got)
	}
	options.Profile = ""
	options.SettingsOnly = false
	if made := plan(t, options); made.SplitsSettings() || slices.Contains(changeIDs(made.Changes), "jackstay.json") {
		t.Fatalf("the files stage also changes settings: %v", changeIDs(made.Changes))
	}
	applyTwice(t, options)
	if got := read(t, root, ".github/workflows/custom.yml"); !strings.Contains(got, "VERSION: 'v0.5.0'") {
		t.Fatalf("custom workflow did not use the pin:\n%s", got)
	}
}

func TestNewerKeysWaitUntilThePinBefore060HasMoved(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "adopt": {"pin": "v0.5.11"}, ` +
		`"changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version}"}}`, ".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	options := optionsFor(root)
	options.Version = "0.5.11"
	apply(t, options)
	brief := filepath.Join(root, "profiles", "profile.json")
	options.Version, options.Pin = "0.6.0", true
	options.Profile = exampleProfile(t, root, `{"brief": {"max_age_hours": 24}}`, nil)
	if _, err := Make(options); err == nil || !strings.Contains(err.Error(), "the pinned jackstay v0.5.11") {
		t.Fatalf("moving the pin and adding a schema-3 key at once: %v", err)
	}
	// The settings land before the workflows (W-085): the pin moves alone.
	options.Profile, options.SettingsOnly = "", true
	apply(t, options)
	if text := read(t, root, "jackstay.json"); !strings.Contains(text, `"pin": "v0.6.0"`) || !strings.Contains(text, `"jackstay_schema": 2`) {
		t.Fatalf("the pin alone:\n%s", text)
	}
	// While card-line.yml still downloads v0.5.11, a schema-3 key waits.
	options.Pin, options.Profile = false, brief
	if _, err := Make(options); err == nil || !strings.Contains(err.Error(), "v0.5.11 that .github/workflows/card-line.yml downloads") {
		t.Fatalf("a schema-3 key before the workflows landed: %v", err)
	}
	options.Profile, options.SettingsOnly = "", false
	apply(t, options)
	options.Profile, options.SettingsOnly = brief, true
	apply(t, options)
	if text := read(t, root, "jackstay.json"); !strings.Contains(text, `"jackstay_schema": 3`) || !strings.Contains(text, `"max_age_hours": 24`) {
		t.Fatalf("after the workflows landed:\n%s", text)
	}
}

func TestNewerKeysAreRefusedForAProjectPinnedBefore060(t *testing.T) {
	for pin, refused := range map[string]bool{"v0.5.11": true, "v0.6.0": false} {
		root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema":2,"adopt":{"pin":"` + pin + `"}}`})
		options := optionsFor(root)
		options.Profile = exampleProfile(t, root, `{"brief": {"max_age_hours": 24}}`, nil)
		_, err := Make(options)
		if refused != (err != nil && strings.Contains(err.Error(), "the pinned jackstay "+pin)) {
			t.Errorf("pin %s: %v", pin, err)
		}
	}
	// A key the file has already is not added by this apply: what reads the file reads it already.
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema":3,"adopt":{"pin":"v0.5.11"},"brief":{"max_age_hours":24}}`})
	if _, err := Make(optionsFor(root)); err != nil {
		t.Fatalf("a key the file already has: %v", err)
	}
}

func TestAProfileWorkflowNamesTheReleaseItDownloads(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema":2,"project":"p","adopt":{"pin":"v0.5.11"}}`})
	options := optionsFor(root)
	options.Version = "0.5.11"
	workflow := map[string]string{"custom": "name: custom\nenv:\n  VERSION: '@@jackstay_version@@'\n"}
	options.Profile = exampleProfile(t, root, `{}`, workflow)
	apply(t, options)
	// The pin moves alone, settings first (W-085); custom.yml still downloads v0.5.11.
	options.Version, options.Pin, options.SettingsOnly = "0.6.0", true, true
	apply(t, options)
	exampleProfile(t, root, `{"brief": {"max_age_hours": 24}}`, workflow)
	options.Pin = false
	if _, err := Make(options); err == nil || !strings.Contains(err.Error(), "v0.5.11 that .github/workflows/custom.yml downloads") {
		t.Fatalf("a schema-3 key while a profile workflow downloads v0.5.11: %v", err)
	}
	exampleProfile(t, root, `{}`, workflow)
	options.SettingsOnly = false
	apply(t, options)
	exampleProfile(t, root, `{"brief": {"max_age_hours": 24}}`, workflow)
	options.SettingsOnly = true
	apply(t, options)
	if text := read(t, root, "jackstay.json"); !strings.Contains(text, `"jackstay_schema": 3`) {
		t.Fatalf("after the workflows landed:\n%s", text)
	}
}

func TestAWorkflowWhoseReleaseCannotBeToldHoldsNewerKeysBack(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema":2,"project":"p","adopt":{"pin":"v0.6.0"}}`})
	options := optionsFor(root)
	options.Version = "0.6.0"
	old := map[string]string{"custom": "name: custom\nenv:\n  VERSION: '@@jackstay_version@@-@@channel_2@@'\n"}
	options.Profile = exampleProfile(t, root, `{}`, old)
	setValue := func(name, value string) {
		t.Helper()
		path := filepath.Join(root, "profiles", "profile.json")
		text := strings.Replace(read(t, root, "profiles/profile.json"), `"agents"`, `"values": {"`+name+`": "`+value+`"}, "agents"`, 1)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setValue("channel_2", "stable")
	apply(t, options)
	stale := strings.Replace(read(t, root, ".github/workflows/custom.yml"), "v0.6.0", "v0.5.11", 1)
	if err := os.WriteFile(filepath.Join(root, ".github/workflows/custom.yml"), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	// Its own template's line, other placeholder and all, tells the release: v0.5.11.
	exampleProfile(t, root, `{"brief": {"max_age_hours": 24}}`, old)
	setValue("channel_2", "stable")
	options.SettingsOnly = true
	if _, err := Make(options); err == nil || !strings.Contains(err.Error(), "v0.5.11 that .github/workflows/custom.yml downloads") {
		t.Fatalf("an old release behind another placeholder: %v", err)
	}
	// The profile renames the variable in the same apply: the release on disk cannot be told.
	exampleProfile(t, root, `{"brief": {"max_age_hours": 24}}`, map[string]string{"custom": "name: custom\nenv:\n  JST: '@@jackstay_version@@'\n"})
	if _, err := Make(options); err == nil || !strings.Contains(err.Error(), "cannot be told") {
		t.Fatalf("a release that cannot be told: %v", err)
	}
	// The profile drops the template in the same apply: the workflow still runs, release unknown.
	exampleProfile(t, root, `{"brief": {"max_age_hours": 24}}`, map[string]string{"tool": "name: tool\n"})
	if _, err := Make(options); err == nil || !strings.Contains(err.Error(), "update or removal") {
		t.Fatalf("a dropped template's workflow: %v", err)
	}
	// Another managed workflow's VERSION for another tool is not jackstay's.
	other := map[string]string{"custom": "name: custom\nenv:\n  JST: '@@jackstay_version@@'\n", "tool": "name: tool\nenv:\n  VERSION: 'v0.5.11'\n"}
	exampleProfile(t, root, `{}`, other)
	options.SettingsOnly = false
	options.Accept = []string{"workflow:custom:edited"}
	apply(t, options)
	options.Accept = nil
	exampleProfile(t, root, `{"brief": {"max_age_hours": 24}}`, other)
	options.SettingsOnly = true
	apply(t, options)
	if text := read(t, root, "jackstay.json"); !strings.Contains(text, `"jackstay_schema": 3`) {
		t.Fatalf("after the workflows landed:\n%s", text)
	}
}
