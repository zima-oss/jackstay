package baseline

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWhichCommandsStartWhichKind(t *testing.T) {
	cases := map[string]string{
		"make test":                          "whole",
		"cd x && make test 2>&1 | tail -5":   "whole",
		"make -C ../y test LOW=1":            "whole",
		"timeout 1500 nice make test":        "whole",
		"for i in 1 2; do make test; done":   "whole",
		"~/.local/bin/jst test -- make test": "whole",
		"CI=1 caffeinate -d make test":       "whole",
		"make test-changed BASE=origin/main": "changed",
		"swift test --skip-build --filter A": "focused",
		// The earliest kind of the list is the one a line counts as, whatever the order on the line.
		"swift test --filter A && make test":               "whole",
		"caffeinate -d just gate > \"$TMPDIR/g.log\" 2>&1": "gate",
		"make test-plan":                     "",
		"grep 'make test' AGENTS.md":         "",
		"git commit -m 'make test is green'": "",
		"echo make test":                     "",
		"grep -- 'make test' AGENTS.md":      "",
		"echo $(make test)":                  "",
		"echo 'open":                         "",
		"just --list":                        "",
	}
	for line, want := range cases {
		if got := kindOf(kinds(), line); got != want {
			t.Errorf("%q: %q, want %q", line, got, want)
		}
	}
	if got := plainCommand([]string{"time", "-p", "/usr/bin/make", "test"}); got != "make test" {
		t.Errorf("plain: %q", got)
	}
	if got := plainCommand([]string{"\"a sentence\"", "x"}); got != "" {
		t.Errorf("a quoted sentence is no program: %q", got)
	}
}

func TestFolderNamesAndGlobs(t *testing.T) {
	if got := FolderName("/Users/x/my_work/a.b"); got != "-Users-x-my-work-a-b" {
		t.Errorf("folder name: %q", got)
	}
	for _, check := range []struct {
		name, glob string
		want       bool
	}{{"proj-wt-a", "proj-wt-*", true}, {"proj-main", "proj-wt-*", false}, {"a1", "a?", true}, {"ab", "a[!b]", false},
		{"ac", "a[!b]", true}, {"a.b", "a.b", true}, {"axb", "a.b", false}, {"a[", "a[", true}} {
		if got := matchesGlob(check.name, check.glob); got != check.want {
			t.Errorf("%q ~ %q: %v", check.name, check.glob, got)
		}
	}
}

func TestNumbersAreWrittenAsTheReportAlwaysWroteThem(t *testing.T) {
	for _, check := range []struct {
		value Ratio
		want  string
	}{{2, "2.0"}, {0.67, "0.67"}, {0, "0.0"}, {rounded(0.125, 2), "0.12"}, {rounded(2.0/3, 2), "0.67"}, {rounded(1.0/7, 3), "0.143"}} {
		written, err := json.Marshal(check.value)
		if err != nil || string(written) != check.want {
			t.Errorf("%v: %s (%v), want %s", float64(check.value), written, err, check.want)
		}
	}
	if got := value(t, median([]float64{1, 2, 3, 4})); got != 2 { // 2.5 to even
		t.Errorf("median: %d", got)
	}
	if got := value(t, median([]float64{300, 900, 600})); got != 600 {
		t.Errorf("median: %d", got)
	}
	if median(nil) != nil || percentile90(nil) != nil {
		t.Error("no values: no median")
	}
	if got := value(t, percentile90([]float64{600, 1200, 1800})); got != 1800 {
		t.Errorf("p90: %d", got)
	}
	by := 3
	if got := value(t, ratioOrNil(2, &by)); got != 0.67 {
		t.Errorf("per merge: %v", got)
	}
	if ratioOrNil(2, nil) != nil || ratioOrNil(2, new(int)) != nil {
		t.Error("nothing to divide by: nil")
	}
	if _, err := SpanOf("2026-11-7", until); err == nil {
		t.Error("a day must be YYYY-MM-DD")
	}
}

func TestDefinitionFromJackstayJSONFlagsAndConfig(t *testing.T) {
	top := t.TempDir()
	repo := filepath.Join(top, "fourth")
	section := `{"kinds": [{"name": "all", "pattern": "^npm test$", "gate": true}, {"name": "one", "pattern": "^npm test -- \\S+"}],
		"merge_unit": "commit", "branch": "trunk", "transcripts": ["sessions-of-fourth*"]}`
	write(t, filepath.Join(repo, "jackstay.json"), `{"jackstay_schema": 1, "project": "fourth", "baseline": `+section+`}`)
	spec, err := Define("", repo, "", Flags{})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Project != "fourth" || spec.MergeUnit != "commit" || spec.Branch != "trunk" || spec.Source != repo+"/jackstay.json" ||
		len(spec.Kinds) != 2 || !spec.Kinds[0].Gate || spec.Kinds[1].Gate || spec.Transcripts[0] != "sessions-of-fourth*" {
		t.Fatalf("definition: %+v", spec)
	}
	flagged, err := Define("", repo, "", Flags{Kinds: []string{"everything=^npm test"}, Gates: []string{"everything"}, MergeUnit: "pr",
		Branch: "main", Transcripts: []string{"x"}})
	if err != nil || len(flagged.Kinds) != 1 || !flagged.Kinds[0].Gate || flagged.MergeUnit != "pr" || flagged.Branch != "main" ||
		!reflect.DeepEqual(flagged.Transcripts, []string{"x"}) {
		t.Fatalf("flags replace the file: %+v %v", flagged, err)
	}
	// No jackstay.json: no kinds, the folder's name, the default transcripts.
	plain, err := Define("", filepath.Join(top, "new thing"), "", Flags{})
	if err != nil || plain.Project != "new thing" || plain.Source != "none" || len(plain.Kinds) != 0 ||
		plain.Transcripts[1] != plain.Transcripts[0]+"--claude-worktrees-*" || !strings.HasSuffix(plain.Transcripts[0], "-new-thing") {
		t.Fatalf("no jackstay.json: %+v %v", plain, err)
	}
	// A file that is the section itself.
	bare := filepath.Join(top, "section.json")
	write(t, bare, section)
	if spec, err := Define("x", top, bare, Flags{}); err != nil || len(spec.Kinds) != 2 || spec.Source != bare {
		t.Fatalf("a bare section: %+v %v", spec, err)
	}
	bad := map[string]string{
		"twice":         `{"jackstay_schema": 1, "baseline": {"kinds": []}, "baseline": {"kinds": []}}`,
		"not RE2":       `{"jackstay_schema": 1, "baseline": {"kinds": [{"name": "a", "pattern": "("}]}}`,
		"no pattern":    `{"jackstay_schema": 1, "baseline": {"kinds": [{"name": "a"}]}}`,
		"gate not bool": `{"jackstay_schema": 1, "baseline": {"kinds": [{"name": "a", "pattern": "x", "gate": "yes"}]}}`,
		"one name":      `{"jackstay_schema": 1, "baseline": {"kinds": [{"name": "a", "pattern": "x"}, {"name": "a", "pattern": "y"}]}}`,
		"unit":          `{"jackstay_schema": 1, "baseline": {"merge_unit": "tag"}}`,
		"transcripts":   `{"jackstay_schema": 1, "baseline": {"transcripts": "x"}}`,
		"slash":         `{"jackstay_schema": 1, "baseline": {"transcripts": ["a/b"]}}`,
		"unknown":       `{"jackstay_schema": 1, "baseline": {"colour": 1}}`,
		"blank branch":  `{"jackstay_schema": 1, "baseline": {"branch": " "}}`,
		"no section":    `{"jackstay_schema": 1}`,
	}
	for name, text := range bad {
		place := filepath.Join(top, "bad.json")
		write(t, place, text)
		var problem *DefinitionError
		if _, err := Define("x", top, place, Flags{}); !errors.As(err, &problem) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, flags := range map[string]Flags{"gate": {Gates: []string{"nothing"}}, "kind": {Kinds: []string{"x"}},
		"kind pattern": {Kinds: []string{"x=("}}} {
		if _, err := Define("x", repo, "", flags); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A folder or a broken link where the project's jackstay.json is: an error, not empty settings.
	folder := filepath.Join(top, "folder")
	if err := os.MkdirAll(filepath.Join(folder, "jackstay.json"), 0o750); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(top, "broken")
	if err := os.MkdirAll(broken, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(broken, "nowhere"), filepath.Join(broken, "jackstay.json")); err != nil {
		t.Fatal(err)
	}
	for _, place := range []string{folder, broken} {
		if _, err := Define("x", place, "", Flags{}); err == nil {
			t.Errorf("%s: accepted", place)
		}
	}
}

func write(t *testing.T, place, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(place), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(place, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheCommand(t *testing.T) {
	found := newFixture(t, map[string]string{"One (#1)": "2026-11-08T12:00:00Z"})
	write(t, filepath.Join(found.repo.Root, "jackstay.json"), `{"jackstay_schema": 1, "project": "proj", "baseline": {"kinds": [{"name": "whole",
		"pattern": "^make test$", "gate": true}], "transcripts": ["proj-main"]}}`)
	start := at(t, "2026-11-08T10:00:00Z")
	writeLines(t, filepath.Join(found.projects, "proj-main", "a.jsonl"), append(ran("t1", start, 300, "make test"),
		ran("k1", start+400, 1, "git reset --hard")...), "")
	examples := filepath.Join(found.repo.Home, "examples.jsonl")
	base := []string{"--repo", found.repo.Root, "--since", since, "--until", until, "--projects-dir", found.projects}
	var stdout, stderr bytes.Buffer
	if code := run(append(base, "--json", "--examples", examples), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var report map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	kinds := report["kinds"].(map[string]any)["whole"].(map[string]any)
	if kinds["started"] != 1.0 || kinds["wall_median_s"] != 300.0 || report["project"] != "proj" {
		t.Errorf("report: %v", report)
	}
	shown, err := os.ReadFile(examples)
	if err != nil || !strings.Contains(string(shown), `"fate":"ran"`) || strings.Count(string(shown), "\n") != 1 {
		t.Errorf("examples: %s %v", shown, err)
	}
	stdout.Reset()
	if code := run(base, nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "whole [gate]: 1 / 0 / 0 / 1") ||
		!strings.Contains(stdout.String(), "ran 1 (git-reset-hard 1)") {
		t.Errorf("text (%d):\n%s", code, stdout.String())
	}
	for name, args := range map[string][]string{
		"no repo":    {"--since", since, "--until", until},
		"not a day":  {"--repo", ".", "--since", "2026-11-7", "--until", until},
		"unit":       {"--repo", ".", "--since", since, "--until", until, "--per", "tag"},
		"also":       {"--repo", ".", "--since", since, "--until", until, "--also", "x"},
		"positional": {"--repo", ".", "--since", since, "--until", until, "extra"},
		"unknown":    {"--nothing"},
	} {
		if code := run(args, nil, &stdout, &stderr); code != exitBadRequest {
			t.Errorf("%s: exit %d", name, code)
		}
	}
	stdout.Reset()
	if code := run([]string{"--help"}, nil, &stdout, &stderr); code != 0 || !strings.HasPrefix(stdout.String(), "usage: jst baseline") {
		t.Errorf("help: %d %q", code, stdout.String())
	}
}
