package guard

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/config"
)

// The levels of W-053 and the split of W-055 (docs/design/discard-and-clean-gate.md section 9).

func everyRow(level string) map[string]any {
	rules := map[string]any{}
	for id, rule := range config.GuardRules {
		if rule.Configurable {
			rules[id] = map[string]any{"level": level}
		}
	}
	return rules
}

func withLevel(rules map[string]any, id, level string) map[string]any {
	copied := map[string]any{}
	for key, value := range rules {
		copied[key] = value
	}
	copied[id] = map[string]any{"level": level}
	return copied
}

func TestTheConfigListIsTheTable(t *testing.T) {
	rows := rowsOfTable()
	var listed []string
	for id := range config.GuardRules {
		listed = append(listed, id)
	}
	sort.Strings(rows)
	sort.Strings(listed)
	if !slices.Equal(rows, listed) {
		t.Fatalf("the table %v\nconfig.GuardRules %v", rows, listed)
	}
	for id, rule := range config.GuardRules {
		if rule.Paths != (id == "rm-recursive" || id == "find-delete") {
			t.Errorf("%s takes paths: %v", id, rule.Paths)
		}
	}
}

func rowsOfTable() []string {
	ids := make([]string, 0, len(table))
	for _, row := range table {
		ids = append(ids, row.ID)
	}
	return ids
}

func TestZshGlobWarnsByDefaultAndRefusesWhenSet(t *testing.T) {
	folder := realFolder(t)
	settings := Settings{Folder: folder, Environ: map[string]string{"SHELL": "/bin/zsh"}, Home: "/Users/someone", Git: NewGit(nil)}
	decision := Decide("ls *.nothing", settings)
	if len(decision.Findings) != 0 || len(decision.Warned) != 1 || decision.Warned[0].Rule != "zsh-glob" {
		t.Fatalf("default: %+v", decision)
	}
	settings.Guard = map[string]any{"rules": withLevel(nil, "zsh-glob", "refuse")}
	if got := Decide("ls *.nothing", settings).Rules(); !slices.Equal(got, []string{"zsh-glob"}) {
		t.Fatalf("set to refuse: %v", got)
	}
	settings.Guard = map[string]any{"rules": withLevel(nil, "zsh-glob", "off")}
	if decision := Decide("ls *.nothing", settings); len(decision.Findings)+len(decision.Warned) != 0 {
		t.Fatalf("off: %+v", decision)
	}
}

func TestEachLevelOfADestructiveRow(t *testing.T) {
	settings := Settings{Folder: realFolder(t), Home: "/Users/someone", Git: NoGit{}}
	for level, refused := range map[string]bool{"refuse": true, "warn": false, "off": false} {
		settings.Guard = map[string]any{"rules": withLevel(nil, "git-reset-hard", level)}
		decision := Decide("git reset --hard && rm -r /Users/someone/project", settings)
		if slices.Contains(decision.Rules(), "git-reset-hard") != refused || !slices.Contains(decision.Rules(), "rm-recursive") {
			t.Errorf("%s: %v", level, decision.Rules())
		}
		warned := slices.ContainsFunc(decision.Warned, func(finding Finding) bool { return finding.Rule == "git-reset-hard" })
		if warned != (level == "warn") {
			t.Errorf("%s: warned %v", level, decision.Warned)
		}
	}
}

func TestTheRowsThatCannotJudgeAreNeverLoosened(t *testing.T) {
	settings := Settings{Folder: realFolder(t), Home: "/Users/someone", Git: NoGit{},
		Guard: map[string]any{"rules": map[string]any{"git-config-alias": map[string]any{"level": "off"}}}}
	if got := Decide("git -c alias.x=status reset --hard", settings).Rules(); !slices.Contains(got, "git-config-alias") {
		t.Fatalf("a row that cannot judge was loosened: %v", got)
	}
}

// pairRepository has a branch `topic` whose commit no other ref holds.
func pairRepository(t *testing.T) string {
	folder := realFolder(t)
	gitIn(t, folder, "init", "-q", "-b", "main")
	write(t, filepath.Join(folder, "file"), "a\n")
	gitIn(t, folder, "add", "-A")
	gitIn(t, folder, "commit", "-qm", "one")
	gitIn(t, folder, "checkout", "-q", "-b", "topic")
	write(t, filepath.Join(folder, "file"), "b\n")
	gitIn(t, folder, "commit", "-qam", "two")
	gitIn(t, folder, "checkout", "-q", "main")
	return folder
}

func TestRowsThatMeetOnOneCommandAreEachReported(t *testing.T) {
	folder := pairRepository(t)
	refuseAll := map[string]any{"rules": everyRow("refuse")}
	for command, rows := range map[string][]string{
		"git switch --discard-changes -C topic main": {"git-switch-discard", "git-branch-reset"},
		"git checkout -f -B topic main":              {"git-checkout-force", "git-branch-reset"},
		"git push --force origin :main":              {"git-push-force", "git-push-delete-protected"},
		"git push --force --prune origin":            {"git-push-force", "git-push-prune"},
		"git update-ref --stdin":                     {"git-update-ref-delete", "git-update-ref-move"},
	} {
		settings := Settings{Folder: folder, Home: "/Users/someone", Git: NewGit(nil), Guard: refuseAll}
		got := Decide(command, settings).Rules()
		for _, row := range rows {
			if !slices.Contains(got, row) {
				t.Errorf("%q: %v lacks %s", command, got, row)
			}
		}
		for _, off := range rows {
			settings.Guard = map[string]any{"rules": withLevel(everyRow("refuse"), off, "off")}
			got := Decide(command, settings).Rules()
			for _, row := range rows {
				if row != off && !slices.Contains(got, row) {
					t.Errorf("%q with %s off: %v lacks %s", command, off, got, row)
				}
			}
		}
	}
}

// TestLooseningOneRowChangesOnlyThatRow: over the corpus, a row set `off` takes away its own
// findings and no other row's (W-055: the checks are independent by construction; this keeps it so).
func TestLooseningOneRowChangesOnlyThatRow(t *testing.T) {
	for _, one := range readCorpus(t) {
		settings := settingsOf(one)
		section := map[string]any{}
		for key, value := range settings.Guard {
			section[key] = value
		}
		section["rules"] = everyRow("refuse")
		settings.Guard = section
		all := Decide(one.Line, settings).Rules()
		for _, row := range all {
			if !config.GuardRules[row].Configurable {
				continue
			}
			loosened := map[string]any{}
			for key, value := range section {
				loosened[key] = value
			}
			loosened["rules"] = withLevel(everyRow("refuse"), row, "off")
			settings.Guard = loosened
			got := Decide(one.Line, settings).Rules()
			want := slices.DeleteFunc(slices.Clone(all), func(id string) bool { return id == row })
			if !slices.Equal(got, want) && (len(got) != 0 || len(want) != 0) {
				t.Errorf("%s %q with %s off: %v, want %v", one.ID, one.Line, row, got, want)
			}
			settings.Guard = section
		}
	}
}

// exceptionProject is a project with a Generated folder, Sources beside it, and a link out.
func exceptionProject(t *testing.T) string {
	folder := realFolder(t)
	for _, name := range []string{"Generated/cache/x", "Sources/work/data/y", "src/Generated/cache/z"} {
		write(t, filepath.Join(folder, name), "")
	}
	if err := os.Symlink("../Sources", filepath.Join(folder, "Generated", "hop")); err != nil {
		t.Fatal(err)
	}
	return folder
}

func TestPathExceptionsOfTheDeletingRules(t *testing.T) {
	folder := exceptionProject(t)
	guard := map[string]any{"rules": map[string]any{
		"rm-recursive": map[string]any{"paths": []any{"Generated/**", "Linked/nested/**", "Literal"}},
		"find-delete":  map[string]any{"paths": []any{"Generated/**"}},
	}}
	// A link above a pattern's fixed folder: Linked leads to Sources.
	write(t, filepath.Join(folder, "Sources", "nested", "work", "w"), "")
	if err := os.Symlink("Sources", filepath.Join(folder, "Linked")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(folder, "Literal", "inner", "i"), "")
	// cd -P follows deep before its `..`: the folder is Sources/work, not Generated.
	if err := os.Symlink("../Sources/work/data", filepath.Join(folder, "Generated", "deep")); err != nil {
		t.Fatal(err)
	}
	settings := Settings{Folder: folder, Home: "/Users/someone", ProjectRoot: folder, Guard: guard, Git: NoGit{},
		Environ: map[string]string{"TMPDIR": "/var/folders/ab/cdef/T/"}}
	for command, refused := range map[string]bool{
		"rm -r Generated/cache":                                               false,
		"rm -rf Generated/cache Generated/other":                              false,
		"find Generated/cache -delete":                                        false,
		"rm -r Generated/cache Sources":                                       true,
		"rm -r Sources/work":                                                  true,
		"cd src && rm -r Generated/cache":                                     true,
		"rm -r Generated/hop/../work":                                         true,
		"rm -r Generated/hop/work":                                            true,
		"ln -s ../Sources Generated/new && rm -r Generated/new/data":          true,
		"find -L Generated/cache -delete":                                     true,
		`rm -r "$X"`:                                                          true,
		"rm -r Generated":                                                     true,
		"rm -r Linked/nested/work":                                            true,
		"rm -r Literal":                                                       false,
		"rm -r Literal/inner":                                                 true,
		"cd -P Generated/deep/.. && rm -r data":                               true,
		"cd . && rm -r Generated/cache":                                       true,
		"ln -sfn ../Sources Generated/x; rm -r Generated/cache":               true,
		"set -P; rm -r Generated/cache":                                       true,
		"(ln -s ../Sources Generated/link) && rm -r Generated/link/data":      true,
		"sudo rm -r Generated/cache":                                          true,
		"rm -r Generated/cache &":                                             true,
		"find Generated/hop/. -delete":                                        true,
		"rm -r Generated/*/work":                                              true,
		"rm -r Generated/hop":                                                 true,
		"find Sources -maxdepth 0 -execdir sh -c 'rm -r Generated/cache' \\;": true,
		"find . -maxdepth 0 -exec bash -c 'rm -r Generated/cache' \\;":        true,
		"cd -P Generated/hop/.. && rm -r work":                                true,
	} {
		got := Decide(command, settings).Rules()
		if (len(got) > 0) != refused {
			t.Errorf("%q: %v, want refused=%v", command, got, refused)
		}
	}
}

func TestAPatternOverTheTopIsNotApplied(t *testing.T) {
	folder := exceptionProject(t)
	home := filepath.Dir(folder)
	guard := map[string]any{"rules": map[string]any{
		"rm-recursive": map[string]any{"paths": []any{"~/" + filepath.Base(folder)}},
	}}
	decision := Decide("rm -r Sources", Settings{Folder: folder, Home: home, ProjectRoot: folder, Guard: guard, Git: NoGit{}})
	if !slices.Contains(decision.Rules(), "rm-recursive") ||
		!slices.ContainsFunc(decision.Warnings, func(warning Warning) bool { return warning.ID == warnException }) {
		t.Fatalf("%+v", decision)
	}
}

func TestTheHookTellsTheAgentOfAWarnedCommand(t *testing.T) {
	home, folder := t.TempDir(), realFolder(t)
	t.Setenv("SHELL", "/bin/zsh")
	out, _ := runHook(t, home, bashCall("ls *.nothing", folder), "")
	if !toldTheAgentOnly(out, "[zsh-glob]") {
		t.Fatalf("answer %v", out)
	}
	log := readLog(t, home)
	if len(log) != 1 || log[0]["event"] != "warned" || !strings.Contains(strings.Join(anyTexts(log[0]["rules"]), ","), "zsh-glob") {
		t.Fatalf("log %v", log)
	}
}

func anyTexts(value any) []string {
	items, _ := value.([]any)
	texts := make([]string, 0, len(items))
	for _, item := range items {
		text, _ := item.(string)
		texts = append(texts, text)
	}
	return texts
}
