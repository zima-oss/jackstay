package adopt

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
)

// landsLast is the third step of the upgrade (docs/design/land.md section 5): after the pin and the
// workflows, the plan writes schema 3 and the land section, and nothing else is planned with it
// that a later apply would not write.
func landsLast(t *testing.T, options Options) {
	t.Helper()
	options.Accept = nil
	made := plan(t, options)
	if !slices.ContainsFunc(made.Notes, func(note string) bool { return strings.Contains(note, "step 3 of 3 (due)") }) {
		t.Fatalf("step 3 is not due: notes %v", made.Notes)
	}
	if !made.AsksAutonomy() || !slices.Contains(changeIDs(made.Changes), "jackstay.json") {
		t.Fatalf("step 3 plans %v, asks %v", changeIDs(made.Changes), made.AsksAutonomy())
	}
	options.Autonomy = config.AutonomyPrepare
	applyTwice(t, options)
	land, ok, err := config.LandOf([]byte(read(t, options.Dir, "jackstay.json")))
	if err != nil || !ok || land.Autonomy != config.AutonomyPrepare {
		t.Fatalf("the land section after step 3: %+v, %v, %v", land, ok, err)
	}
}

func landOptions(root, autonomy string) Options {
	options := optionsFor(root)
	options.Version, options.Autonomy = "0.6.0", autonomy
	return options
}

// The golden file of each mode: a 0.6.0 adoption writes the land section in full, with the answer.
func TestANewProjectGetsTheLandSectionInFullForEachAnswer(t *testing.T) {
	for _, autonomy := range []string{config.AutonomyMergeWhenGreen, config.AutonomyPrepare} {
		root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 3, "project": "p"}`,
			".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
		made := plan(t, landOptions(root, autonomy))
		if made.AsksAutonomy() {
			t.Fatalf("%s: --autonomy given and still asked", autonomy)
		}
		if got := changeIDs(made.Changes); !slices.Equal(got, []string{"agents-block", "jackstay.json", "workflow:card-line"}) {
			t.Fatalf("%s: changes %v", autonomy, got)
		}
		applyTwice(t, landOptions(root, autonomy))
		want := `{
  "jackstay_schema": 3,
  "project": "p",
  "land": {
    "host": "github",
    "autonomy": "` + autonomy + `",
    "method": "squash",
    "checks": [],
    "human_paths": []
  },
  "adopt": {
    "pin": "v0.6.0"
  }
}
`
		if got := read(t, root, "jackstay.json"); got != want {
			t.Fatalf("%s: jackstay.json\n%s\nwant\n%s", autonomy, got, want)
		}
		agents := read(t, root, "AGENTS.md")
		merges := strings.Contains(agents, "waits for the required\n  checks and merges")
		if merges != (autonomy == config.AutonomyMergeWhenGreen) {
			t.Fatalf("%s: the block's land item does not follow the answer:\n%s", autonomy, agents)
		}
	}
}

// A section the project has is completed, and its autonomy is never changed silently.
func TestAnExistingAutonomyIsKept(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 3, "project": "p", "land": {"autonomy": "prepare", "checks": ["build"]}}`,
		".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	made := plan(t, landOptions(root, ""))
	if made.AsksAutonomy() {
		t.Fatal("a project with land.autonomy is asked again")
	}
	var usage *UsageError
	if _, err := Make(landOptions(root, config.AutonomyMergeWhenGreen)); !errors.As(err, &usage) || !strings.Contains(err.Error(), "never changes it") {
		t.Fatalf("--autonomy against the file's: %v", err)
	}
	applyTwice(t, landOptions(root, config.AutonomyPrepare))
	land, _, err := config.LandOf([]byte(read(t, root, "jackstay.json")))
	if err != nil || land.Autonomy != config.AutonomyPrepare || !slices.Equal(land.Checks, []string{"build"}) || land.Host != "github" {
		t.Fatalf("the completed section: %+v, %v", land, err)
	}
	if text := read(t, root, "jackstay.json"); !strings.Contains(text, `"human_paths": []`) || !strings.Contains(text, `"method": "squash"`) {
		t.Fatalf("the section is not written in full:\n%s", text)
	}
}

// A jackstay before 0.6.0 writes no land section.
func TestAJackstayBefore060WritesNoLandSection(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 3, "project": "p"}`, ".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	if made := plan(t, optionsFor(root)); made.AsksAutonomy() || len(made.Changes) != 1 {
		t.Fatalf("0.5.0 plans %v", changeIDs(made.Changes))
	}
}

// The upgrade from 0.5.11 is three pull requests, in order: the pin (schema 2), the workflows that
// download 0.6.0, then schema 3 with the land section. 0.6.0 reads the schema-2 file meanwhile.
func TestTheUpgradeFrom0511IsThreeStepsInOrder(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "cards": {"dir": "docs/cards", "id": "C-[0-9]+", "unit": "pr", "since": "2026-11-17"}}`,
		".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	old := optionsFor(root)
	old.Version = "0.5.11"
	applyTwice(t, old)
	due := func(made *Plan) int {
		for step := 1; step <= 3; step++ {
			if slices.ContainsFunc(made.Notes, func(note string) bool {
				return strings.HasPrefix(note, "upgrade to the land section, step "+string(rune('0'+step))+" of 3 (due)")
			}) {
				return step
			}
		}
		return 0
	}
	newer := landOptions(root, "")
	// Step 1: the pin, in jackstay.json alone; the file stays schema 2 and gets no land section.
	if made := plan(t, newer); due(made) != 1 || made.AsksAutonomy() || slices.Contains(changeIDs(made.Changes), "jackstay.json") {
		t.Fatalf("before the pin: step %d, changes %v", due(made), changeIDs(made.Changes))
	}
	pin := newer
	pin.Pin, pin.SettingsOnly = true, true
	if got := changeIDs(apply(t, pin).Changes); !slices.Equal(got, []string{"jackstay.json"}) {
		t.Fatalf("step 1 wrote %v", got)
	}
	text := read(t, root, "jackstay.json")
	if !strings.Contains(text, `"pin": "v0.6.0"`) || !strings.Contains(text, `"jackstay_schema": 2`) || strings.Contains(text, `"land"`) {
		t.Fatalf("after step 1:\n%s", text)
	}
	if _, err := config.Parse([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if problems := config.SectionProblems(mustParse(t, text)); len(problems.Sections()) != 0 {
		t.Fatalf("0.6.0 refuses the schema-2 file without land: %v", problems.Sections())
	}
	// Step 2: the workflows that download 0.6.0; jackstay.json is left alone.
	made := plan(t, newer)
	if due(made) != 2 || slices.Contains(changeIDs(made.Changes), "jackstay.json") || !slices.Contains(changeIDs(made.Changes), "workflow:card-line") {
		t.Fatalf("step 2: due %d, changes %v", due(made), changeIDs(made.Changes))
	}
	apply(t, newer)
	if !strings.Contains(read(t, root, ".github/workflows/card-line.yml"), "v0.6.0") {
		t.Fatal("step 2 did not write the workflows for the new pin")
	}
	// Step 3: schema 3 and the land section, in jackstay.json alone; then the block follows it.
	made = plan(t, newer)
	if due(made) != 3 || !made.AsksAutonomy() {
		t.Fatalf("step 3: due %d, asks %v", due(made), made.AsksAutonomy())
	}
	settings := newer
	settings.SettingsOnly, settings.Autonomy = true, config.AutonomyMergeWhenGreen
	if got := changeIDs(apply(t, settings).Changes); !slices.Equal(got, []string{"jackstay.json"}) {
		t.Fatalf("step 3 wrote %v", got)
	}
	land, ok, err := config.LandOf([]byte(read(t, root, "jackstay.json")))
	if err != nil || !ok || land.Autonomy != config.AutonomyMergeWhenGreen || !strings.Contains(read(t, root, "jackstay.json"), `"jackstay_schema": 3`) {
		t.Fatalf("after step 3: %+v %v %v", land, ok, err)
	}
	applyTwice(t, landOptions(root, ""))
	if final := plan(t, landOptions(root, "")); due(final) != 0 || final.AsksAutonomy() {
		t.Fatalf("after the upgrade: notes %v", final.Notes)
	}
}

func mustParse(t *testing.T, text string) config.Config {
	t.Helper()
	parsed, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// runIn is `jst adopt` at version 0.6.0 with the given terminal answer.
func runIn(t *testing.T, terminal bool, stdin string, args ...string) (int, string, string) {
	t.Helper()
	savedVersion, savedTerminal := runningVersion, atTerminal
	t.Cleanup(func() { runningVersion, atTerminal = savedVersion, savedTerminal })
	runningVersion = "0.6.0"
	atTerminal = func(io.Reader, io.Writer) bool { return terminal }
	var stdout, stderr bytes.Buffer
	code := Run(args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestApplyWithoutATerminalNeedsAutonomy(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 3, "project": "p"}`, ".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	code, _, stderr := runIn(t, false, "", "--apply", root)
	if code != 2 || !strings.Contains(stderr, "--autonomy merge-when-green or --autonomy prepare") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if strings.Contains(read(t, root, "jackstay.json"), "land") {
		t.Fatal("a refused apply wrote the file")
	}
	if code, _, stderr := runIn(t, false, "", "--plan", root); code != 3 || stderr != "" {
		t.Fatalf("the plan without a terminal: exit %d: %s", code, stderr)
	}
	if code, _, stderr := runIn(t, false, "", "--apply", "--autonomy", "prepare", root); code != 0 {
		t.Fatalf("--autonomy prepare: exit %d: %s", code, stderr)
	}
	// The file has its answer now: the next apply needs no flag.
	if code, _, stderr := runIn(t, false, "", "--apply", root); code != 0 {
		t.Fatalf("an existing land.autonomy: exit %d: %s", code, stderr)
	}
	if code, _, stderr := runIn(t, false, "", "--apply", "--autonomy", "always", root); code != 2 || !strings.Contains(stderr, "--autonomy is") {
		t.Fatalf("a wrong --autonomy: exit %d: %s", code, stderr)
	}
}

func TestApplyAtATerminalAsksOnceAndOffersTheMachinesAnswerFirst(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 3, "project": "p"}`, ".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	if err := SaveMachineAutonomy(config.AutonomyPrepare); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runIn(t, true, "\n", "--apply", root)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if strings.Index(stdout, AnswerPrepare) > strings.Index(stdout, AnswerMergeWhenGreen) || strings.Count(stdout, "Answer [1]") != 1 {
		t.Fatalf("the machine's answer is not offered first, or asked twice:\n%s", stdout)
	}
	if land, _, err := config.LandOf([]byte(read(t, root, "jackstay.json"))); err != nil || land.Autonomy != config.AutonomyPrepare {
		t.Fatalf("the empty answer did not take the first offer: %+v %v", land, err)
	}
}

func TestAskAutonomy(t *testing.T) {
	for _, testCase := range []struct {
		first, input, want string
	}{
		{"", "\n", config.AutonomyMergeWhenGreen},
		{"", "2\n", config.AutonomyPrepare},
		{config.AutonomyPrepare, "\n", config.AutonomyPrepare},
		{config.AutonomyPrepare, "2\n", config.AutonomyMergeWhenGreen},
		{"", "x\nprepare\n", config.AutonomyPrepare},
	} {
		var out bytes.Buffer
		got, err := AskAutonomy(bufio.NewReader(strings.NewReader(testCase.input)), &out, testCase.first)
		if err != nil || got != testCase.want {
			t.Errorf("first %q, input %q: %q, %v", testCase.first, testCase.input, got, err)
		}
		if !strings.Contains(out.String(), "(recommended)") {
			t.Errorf("the recommended answer is not named:\n%s", out.String())
		}
	}
	if _, err := AskAutonomy(bufio.NewReader(strings.NewReader("")), io.Discard, ""); err == nil {
		t.Fatal("an ended input was taken for an answer")
	}
}

func TestTheMachinesAnswerIsKeptBesideOtherSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	if got, err := MachineAutonomy(); got != "" || err != nil {
		t.Fatalf("no answer yet: %q, %v", got, err)
	}
	path := filepath.Join(home, ".all", machineFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"other": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveMachineAutonomy(config.AutonomyPrepare); err != nil {
		t.Fatal(err)
	}
	if got, err := MachineAutonomy(); got != config.AutonomyPrepare || err != nil {
		t.Fatalf("kept: %q, %v", got, err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `"other": 1`) {
		t.Fatalf("another setting was lost: %s", data)
	}
	if err := SaveMachineAutonomy("always"); err == nil {
		t.Fatal("a wrong value was kept")
	}
}

// A pin no workflow uses now still moves with --pin, so the upgrade's first step can be taken.
func TestAnUnusedOldPinMovesWithPin(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "adopt": {"pin": "v0.5.11"}}`,
		".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	options := landOptions(root, config.AutonomyPrepare)
	options.Pin, options.SettingsOnly = true, true
	apply(t, options)
	if text := read(t, root, "jackstay.json"); !strings.Contains(text, `"pin": "v0.6.0"`) || strings.Contains(text, `"land"`) {
		t.Fatalf("step 1:\n%s", text)
	}
	// Step 2 writes the card-line workflow the coming policy needs, though the project has neither
	// cards nor changes, and leaves jackstay.json at schema 2: step 3 is not due before it lands.
	made := plan(t, landOptions(root, ""))
	if got := changeIDs(made.Changes); !slices.Contains(got, "workflow:card-line") || slices.Contains(got, "jackstay.json") || made.AsksAutonomy() {
		t.Fatalf("step 2 plans %v (notes %v)", got, made.Notes)
	}
	apply(t, landOptions(root, ""))
	if !strings.Contains(read(t, root, ".github/workflows/card-line.yml"), "v0.6.0") || !strings.Contains(read(t, root, "jackstay.json"), `"jackstay_schema": 2`) {
		t.Fatal("step 2 did not write the card-line workflow alone")
	}
	landsLast(t, landOptions(root, ""))
}

// A profile's land defaults fill a new section's other keys and never decide who merges: without a
// terminal the first policy still needs --autonomy, and --autonomy prepare is not a change.
func TestAProfilesLandDefaultsNeverChooseAutonomy(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 3, "project": "p"}`, ".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	profilePath := exampleProfile(t, root, `{"land": {"autonomy": "merge-when-green", "human_paths": ["docs/decisions/**"]}}`, nil)
	options := landOptions(root, "")
	options.Profile = profilePath
	if made := plan(t, options); !made.AsksAutonomy() {
		t.Fatal("the profile's autonomy was taken for the project's answer")
	}
	code, _, stderr := runIn(t, false, "", "--apply", "--profile", profilePath, root)
	if code != 2 || !strings.Contains(stderr, "--autonomy") {
		t.Fatalf("a non-interactive apply with the profile's autonomy: exit %d: %s", code, stderr)
	}
	options.Autonomy = config.AutonomyPrepare
	applyTwice(t, options)
	land, _, err := config.LandOf([]byte(read(t, root, "jackstay.json")))
	if err != nil || land.Autonomy != config.AutonomyPrepare || !slices.Equal(land.HumanPaths, []string{"docs/decisions/**"}) {
		t.Fatalf("the section: %+v, %v", land, err)
	}
}

// upgradeAtWorkflows is a 0.5.x project whose pin moved to 0.6.0 (step 1 merged) and whose
// workflows, card-line and release, step 2 wrote; release.yml is then the project's, kept by an
// exception, with more exceptions given.
func upgradeAtWorkflows(t *testing.T, exceptions string) string {
	t.Helper()
	settings := `"jackstay_schema": 2, "project": "p", "changes": {"dir": "changes", "into": "CHANGELOG.md", "heading": "## {version}"}, ` +
		`"release": {"merge": "owner", "first": "0.1.0", "files": [{"path": "VERSION", "match": "^(?P<version>[0-9]+\\.[0-9]+\\.[0-9]+)$"}]}, `
	root := project(t, map[string]string{"jackstay.json": `{` + settings + `"adopt": {"pin": "v0.6.0"}}`,
		".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	apply(t, landOptions(root, ""))
	path := filepath.Join(root, ".github/workflows/release.yml")
	if err := os.WriteFile(path, []byte(read(t, root, ".github/workflows/release.yml")+"# the project's own step\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "jackstay.json"), []byte(`{`+settings+
		`"adopt": {"pin": "v0.6.0", "exceptions": {"workflow:release:edited": "the project's own release steps"`+exceptions+`}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// Step 2 is judged from the whole plan, exceptions applied, and jackstay does not read a kept
// workflow's release: a person confirms it with the exception upgrade:workflows. Until then a
// blocker names the file and the exception, and the apply and doctor's check say so.
func TestStepThreeWaitsForTheKeptWorkflowsToBeConfirmed(t *testing.T) {
	unconfirmed := upgradeAtWorkflows(t, "")
	made := plan(t, landOptions(unconfirmed, ""))
	if made.AsksAutonomy() || slices.Contains(changeIDs(made.Changes), "jackstay.json") || len(made.Blockers) != 1 ||
		!strings.Contains(made.Blockers[0], ".github/workflows/release.yml") || !strings.Contains(made.Blockers[0], ConfirmWorkflows) {
		t.Fatalf("unconfirmed: changes %v, blockers %v", changeIDs(made.Changes), made.Blockers)
	}
	var out bytes.Buffer
	writeApplied(&out, made, false)
	if strings.Contains(out.String(), "up to date") || !strings.Contains(out.String(), "blocker: step 3 is not offered") {
		t.Fatalf("the apply hides the blocker:\n%s", out.String())
	}
	code, stdout, _ := runIn(t, false, "", "--plan", unconfirmed)
	if code != 3 || !strings.Contains(stdout, "blockers (1)") {
		t.Fatalf("the plan with a blocker: exit %d:\n%s", code, stdout)
	}
	if _, problems, _ := checkAt(t, unconfirmed); !slices.ContainsFunc(problems, func(problem string) bool {
		return strings.Contains(problem, "pending") && strings.Contains(problem, ConfirmWorkflows)
	}) {
		t.Fatalf("doctor's adoption check: %v", problems)
	}
	confirmed := upgradeAtWorkflows(t, `, "upgrade:workflows": "release.yml downloads v0.6.0"`)
	made = plan(t, landOptions(confirmed, ""))
	if !made.AsksAutonomy() || len(made.Blockers) != 0 || !slices.ContainsFunc(made.Notes, func(note string) bool { return strings.Contains(note, "step 3 of 3 (due)") }) {
		t.Fatalf("confirmed: blockers %v, notes %v", made.Blockers, made.Notes)
	}
}

// checkAt is adopt.Check at version 0.6.0.
func checkAt(t *testing.T, root string) (bool, []string, []string) {
	t.Helper()
	saved := runningVersion
	t.Cleanup(func() { runningVersion = saved })
	runningVersion = "0.6.0"
	return Check(root)
}

// The upgrade is judged by the project's file: a schema-3 key added after the pin merged, while the
// workflows still download 0.5.x, is refused rather than raising the file to schema 3.
func TestASchema3KeyWaitsForStepThree(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "cards": {"dir": "docs/cards", "id": "C-[0-9]+", "unit": "pr", "since": "2026-11-17"}}`,
		".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	old := optionsFor(root)
	old.Version = "0.5.11"
	applyTwice(t, old)
	pin := landOptions(root, "")
	pin.Pin, pin.SettingsOnly = true, true
	apply(t, pin)
	options := landOptions(root, "")
	options.SettingsOnly = true
	options.Profile, options.ProfileSHA256 = exportedBundle(t, root)
	var usage *UsageError
	if _, err := Make(options); !errors.As(err, &usage) || !strings.Contains(err.Error(), "step 2 of 3") {
		t.Fatalf("a profile digest before step 3: %v", err)
	}
	if !strings.Contains(read(t, root, "jackstay.json"), `"jackstay_schema": 2`) {
		t.Fatal("the file left schema 2")
	}
}

// exportedBundle is a profile bundle beside the project and its digest.
func exportedBundle(t *testing.T, root string) (string, string) {
	t.Helper()
	source := exampleProfile(t, root, `{}`, nil)
	var bundle, failure bytes.Buffer
	if code := cli.Dispatch([]string{"profile", "export", source}, nil, &bundle, &failure); code != 0 {
		t.Fatalf("export: %s", failure.String())
	}
	bundlePath := filepath.Join(root, "policy.bundle.json")
	if err := os.WriteFile(bundlePath, bundle.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(bundle.Bytes())
	return bundlePath, hex.EncodeToString(digest[:])
}

// Step 1 keeps the file at schema 2: recording a profile's digest there is refused.
func TestThePinStepRefusesAProfileDigest(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 2, "project": "p", "adopt": {"pin": "v0.5.11"}}`,
		".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	bundlePath, digest := exportedBundle(t, root)
	options := landOptions(root, "")
	options.Pin, options.SettingsOnly = true, true
	options.Profile, options.ProfileSHA256 = bundlePath, digest
	var usage *UsageError
	if _, err := Make(options); !errors.As(err, &usage) || !strings.Contains(err.Error(), "adopt.profile_sha256") ||
		!strings.Contains(err.Error(), "step 3") {
		t.Fatalf("step 1 with a profile digest: %v", err)
	}
	options.ProfileSHA256 = ""
	if _, err := Make(options); err != nil {
		t.Fatalf("step 1 with a loose profile: %v", err)
	}
}

// Doctor confirms rule D0 only for card-line.yml as adopt writes it for the project.
func TestTheLintWorkflowStateIsAdoptsOwnFileOnly(t *testing.T) {
	root := project(t, map[string]string{"jackstay.json": `{"jackstay_schema": 3, "project": "p"}`, ".claude/settings.json": projectsSettings, "CLAUDE.md": "@AGENTS.md\n"})
	state := func() string {
		return LintWorkflowState(root, mustParse(t, read(t, root, "jackstay.json")))
	}
	if got := state(); got != LintWorkflowMissing {
		t.Fatalf("before adoption: %s", got)
	}
	applyTwice(t, landOptions(root, config.AutonomyPrepare))
	if got := state(); got != LintWorkflowManaged {
		t.Fatalf("as adopt wrote it: %s", got)
	}
	path := filepath.Join(root, ".github/workflows/card-line.yml")
	if err := os.WriteFile(path, []byte(read(t, root, ".github/workflows/card-line.yml")+"      - run: echo more\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != LintWorkflowOwn {
		t.Fatalf("edited: %s", got)
	}
	if err := os.WriteFile(path, []byte("name: card-line\non: pull_request\njobs:\n  card-line:\n    steps:\n      - run: jst lint pr\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != LintWorkflowOwn {
		t.Fatalf("the project's own: %s", got)
	}
}
