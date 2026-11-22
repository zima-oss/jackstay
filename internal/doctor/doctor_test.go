package doctor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/layout"
	"github.com/zima-oss/jackstay/internal/supervise"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
)

var now = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func write(t *testing.T, path, text string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
}

// goPrefix lays out an installed version (fixed links, lib/jackstay) with a fake binary.
func goPrefix(t *testing.T, versionText string) string {
	t.Helper()
	prefix := t.TempDir()
	folder := filepath.Join(layout.Lib(prefix), "jackstay-0.5.0-abc-1-1")
	write(t, filepath.Join(folder, layout.Program), "#!/bin/sh\n")
	os.Chmod(filepath.Join(folder, layout.Program), 0o755)
	write(t, filepath.Join(folder, layout.VersionFile), versionText+"\n")
	for _, name := range layout.Launchers[1:] {
		os.Symlink(layout.Program, filepath.Join(folder, name))
	}
	os.Symlink(filepath.Base(folder), layout.Link(prefix))
	os.MkdirAll(layout.Bin(prefix), 0o755)
	for _, name := range layout.Launchers {
		os.Symlink(layout.FixedTarget(name), filepath.Join(layout.Bin(prefix), name))
	}
	return prefix
}

// inputs are a quiet, healthy machine: the guard refuses, jackstay names the live version, one project
// heard from an hour ago.
func inputs(t *testing.T, prefix string) Inputs {
	t.Helper()
	home := t.TempDir()
	write(t, filepath.Join(home, "widgets", "guard-seen"), "")
	os.Chtimes(filepath.Join(home, "widgets", "guard-seen"), now.Add(-time.Hour), now.Add(-time.Hour))
	return Inputs{Prefix: prefix, Now: now, Version: "0.5.0 (abc)", Home: home, Settings: filepath.Join(home, "no-settings.json"),
		LockFolder: filepath.Join(home, ".all"),
		Run: func(argv []string, _ []byte) (supervise.Captured, error) {
			if filepath.Base(argv[0]) == "jst-guard" {
				return supervise.Captured{Stdout: []byte(`{"hookSpecificOutput":{"permissionDecision":"deny"}}`)}, nil
			}
			return supervise.Captured{Stdout: []byte("jackstay 0.5.0 (abc)\n")}, nil
		},
		SourceCommit: func(string) (string, error) { return "", errors.New("not a checkout") },
		BuildChanged: func(string, string) (bool, error) { return true, nil },
		ProbeGuard:   func(string) error { return nil }}
}

func kinds(findings []Finding) string {
	var found []string
	for _, finding := range findings {
		found = append(found, finding.Kind)
	}
	sort.Strings(found)
	return strings.Join(found, ",")
}

func TestHealthyGoLayout(t *testing.T) {
	given := inputs(t, goPrefix(t, "0.5.0 (abc)"))
	report := Diagnose(given)
	if len(report.Problems) != 0 || len(report.Notes) != 0 {
		t.Fatalf("problems %v notes %v", report.Problems, report.Notes)
	}
	if _, err := os.Stat(given.LockFolder); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("diagnosis created lock directory: %v", err)
	}
}

func TestLockCheckRejectsFileWithoutChangingIt(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "lock-folder")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Report{Facts: map[string]any{}}
	checkLock(&report, Inputs{LockFolder: file, Settings: filepath.Join(root, "settings.json")})
	if got := kinds(report.Problems); got != "lock-unwritable" {
		t.Fatalf("lock check accepted a file: %s", got)
	}
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "keep" {
		t.Fatalf("lock check modified file: %q, %v", content, err)
	}
}

func TestLayoutProblems(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(t *testing.T) (string, func(*Inputs))
		want string
	}{
		{"empty prefix", func(t *testing.T) (string, func(*Inputs)) { return t.TempDir(), nil },
			"launcher-missing,launcher-missing,launcher-missing,link-missing"},
		{"guard allows", func(t *testing.T) (string, func(*Inputs)) {
			return goPrefix(t, "0.5.0 (abc)"), func(in *Inputs) {
				in.ProbeGuard = func(string) error { return errors.New("allowed") }
			}
		}, "guard-does-not-refuse"},
		{"version differs", func(t *testing.T) (string, func(*Inputs)) { return goPrefix(t, "0.5.1 (def)"), nil }, "version-mismatch"},
		{"checkout moved", func(t *testing.T) (string, func(*Inputs)) {
			prefix := goPrefix(t, "0.5.0 (abc)")
			checkout := t.TempDir()
			write(t, filepath.Join(checkout, "go.mod"), "module github.com/zima-oss/jackstay\n")
			write(t, filepath.Join(layout.Lib(prefix), "jackstay-0.5.0-abc-1-1", layout.SourceFile), checkout+"\n")
			return prefix, func(in *Inputs) { in.SourceCommit = func(string) (string, error) { return "def", nil } }
		}, "checkout-moved"},
		// Only docs, the justfile or the release files moved on: no install is due (W-020).
		{"checkout moved in docs only", func(t *testing.T) (string, func(*Inputs)) {
			prefix := goPrefix(t, "0.5.0 (abc)")
			checkout := t.TempDir()
			write(t, filepath.Join(checkout, "go.mod"), "module github.com/zima-oss/jackstay\n")
			write(t, filepath.Join(layout.Lib(prefix), "jackstay-0.5.0-abc-1-1", layout.SourceFile), checkout+"\n")
			return prefix, func(in *Inputs) {
				in.SourceCommit = func(string) (string, error) { return "def", nil }
				in.BuildChanged = func(_, installed string) (bool, error) {
					if installed != "abc" {
						t.Errorf("compared with %q", installed)
					}
					return false, nil
				}
			}
		}, ""},
		// Uncommitted changes to the build inputs are a change whatever the commits say.
		{"checkout with uncommitted code", func(t *testing.T) (string, func(*Inputs)) {
			prefix := goPrefix(t, "0.5.0 (abc)")
			checkout := t.TempDir()
			write(t, filepath.Join(checkout, "go.mod"), "module github.com/zima-oss/jackstay\n")
			write(t, filepath.Join(layout.Lib(prefix), "jackstay-0.5.0-abc-1-1", layout.SourceFile), checkout+"\n")
			return prefix, func(in *Inputs) {
				in.SourceCommit = func(string) (string, error) { return "abc+changes", nil }
				in.BuildChanged = func(string, string) (bool, error) { return false, nil }
			}
		}, "checkout-moved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix, adjust := tc.make(t)
			given := inputs(t, prefix)
			if adjust != nil {
				adjust(&given)
			}
			if got := kinds(Diagnose(given).Problems); got != tc.want {
				t.Fatalf("problems %s, want %s", got, tc.want)
			}
		})
	}
}

// guardSpan is a guard command span of a project, ended at a moment, with its attributes.
func guardSpan(project string, end time.Time, attributes map[string]any) journal.Span {
	attributes[telemetry.AttrCommand] = "guard"
	return journal.Span{Name: "jst guard", Start: end.Add(-time.Millisecond), End: end, Attributes: attributes,
		Resource: map[string]any{telemetry.AttrProject: project}}
}

// The 7-day window on both sides of its edge, a config error on an allowed call, and ignored tools
// as a fact: read from the guard's spans in the journal.
func TestLogWindow(t *testing.T) {
	given := inputs(t, goPrefix(t, "0.5.0 (abc)"))
	edge := now.Add(-7 * 24 * time.Hour)
	given.GuardSpans = []journal.Span{
		guardSpan("old", edge.Add(-time.Second), map[string]any{telemetry.AttrGuardOutcome: "failed_open"}),
		guardSpan("widgets", edge.Add(time.Second), map[string]any{telemetry.AttrGuardOutcome: "failed_open"}),
		guardSpan("storefront", now, map[string]any{telemetry.AttrGuardOutcome: "allowed", telemetry.AttrGuardConfigError: "bad"}),
		guardSpan("widgets", now, map[string]any{telemetry.AttrGuardOutcome: "ignored_tool", telemetry.AttrGuardTool: "Write"}),
	}
	// `jst guard check --help` used by hand: an error span with no outcome is not a call let through.
	byHand := guardSpan("handheld", now, map[string]any{})
	byHand.StatusCode = journal.StatusError
	given.GuardSpans = append(given.GuardSpans, byHand)
	report := Diagnose(given)
	if got := kinds(report.Problems); got != "config-error,failed-open" {
		t.Fatalf("problems %s", got)
	}
	if !strings.Contains(report.Problems[0].Text+report.Problems[1].Text, "widgets") {
		t.Fatalf("problems %v", report.Problems)
	}
	ignored := report.Facts["guard_last_7_days"].(map[string]any)["ignored_tools"].(map[string]int)
	if ignored["Write"] != 1 {
		t.Fatalf("ignored %v", ignored)
	}
}

// The 3-day quiet note on both sides of its edge; a marked project's hours; expected projects
// never heard.
func TestMarks(t *testing.T) {
	given := inputs(t, goPrefix(t, "0.5.0 (abc)"))
	mark := func(folder, name string, at time.Time) {
		path := filepath.Join(folder, name)
		write(t, path, "")
		os.Chtimes(path, at, at)
	}
	mark(given.Home, "quiet/guard-seen", now.Add(-3*24*time.Hour-time.Minute))
	mark(given.Home, "fresh/guard-seen", now.Add(-3*24*time.Hour+time.Minute))
	mark(given.Home, "^my%20^project/guard-seen", now.Add(-time.Hour))
	given.Expected = []string{"fresh", "never"}
	report := Diagnose(given)
	if got := kinds(report.Notes); got != "guard-quiet" || !strings.Contains(report.Notes[0].Text, "quiet") {
		t.Fatalf("notes %v", report.Notes)
	}
	if got := kinds(report.Problems); got != "expected-never-called" || !strings.Contains(report.Problems[0].Text, "never") {
		t.Fatalf("problems %v", report.Problems)
	}
	seen := report.Facts["guard_last_call_hours_ago"].(map[string]float64)
	if seen["^my%20^project"] != 1 {
		t.Fatalf("a project's mark: %v", seen)
	}
}

func TestNeverCalled(t *testing.T) {
	given := inputs(t, goPrefix(t, "0.5.0 (abc)"))
	given.Home = t.TempDir()
	if got := kinds(Diagnose(given).Problems); got != "guard-never-called" {
		t.Fatalf("problems %s", got)
	}
	given.ExpectedErr = errors.New("unreadable")
	if got := kinds(Diagnose(given).Problems); !strings.Contains(got, "expected-unreadable") {
		t.Fatalf("problems %s", got)
	}
}

func TestSandbox(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".jackstay")
	os.MkdirAll(home, 0o700)
	real := realPath(home)
	parent := filepath.Dir(real)
	settings := filepath.Join(t.TempDir(), "settings.json")
	for _, tc := range []struct {
		name, json string
		blocks     bool
	}{
		{"off", `{"sandbox": {"enabled": false}}`, false},
		{"no sandbox", `{}`, false},
		{"on, nothing allowed", `{"sandbox": {"enabled": true}}`, true},
		{"the folder itself", `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": ["` + home + `"]}}}`, false},
		{"its parent with /**", `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": ["` + parent + `/**"]}}}`, false},
		{"a star in the middle", `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": ["` + filepath.Dir(parent) + `/*/.jackstay"]}}}`, false},
		{"a star that does not match", `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": ["` + parent + `/x*"]}}}`, true},
		{"denied wins", `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": ["` + home + `"], "denyWrite": ["` + parent + `"]}}}`, true},
		{"denied inside", `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": ["` + home + `"], "denyWrite": ["` + home + `/.all/**"]}}}`, true},
		{"denied inside through a star", `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": ["` + home + `"], "denyWrite": ["` + filepath.Dir(parent) + `/*/.jackstay/.all/**"]}}}`, true},
		{"denied beside, not inside", `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": ["` + home + `"], "denyWrite": ["` + filepath.Dir(parent) + `/*/.other/**"]}}}`, false},
		{"not json", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(t, settings, tc.json)
			blocked, fix := SandboxBlocks(settings, home, []string{"app"})
			if got := blocked != ""; got != tc.blocks || (blocked != "") != (fix != "") {
				t.Fatalf("blocks %v, want %v: %s; fix %q", got, tc.blocks, blocked, fix)
			}
		})
	}
	// /tmp is /private/tmp on the Mac: a pattern through the link covers the real folder.
	if linked, err := filepath.EvalSymlinks("/tmp"); err == nil && linked != "/tmp" {
		folder, _ := os.MkdirTemp("/tmp", "jackstay-doctor.")
		defer os.RemoveAll(folder)
		write(t, settings, `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": ["/tmp/*"]}}}`)
		if blocked, _ := SandboxBlocks(settings, folder, nil); blocked != "" {
			t.Fatalf("/tmp/* did not cover %s: %s", folder, blocked)
		}
	}
}

func TestSandboxGrantsAndEvidence(t *testing.T) {
	home := filepath.Join(realPath(t.TempDir()), ".jackstay")
	for _, folder := range []string{"app", ".all", ".trash", "node"} {
		if err := os.MkdirAll(filepath.Join(home, folder), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Expected names are stored as marker names already: one with capitals stays as it is.
	projects := SandboxProjects(home, []string{"service", "app", homedir.MarkerName("My App")})
	wantProjects := []string{"app", "node", "service", homedir.MarkerName("My App")}
	sort.Strings(wantProjects)
	if strings.Join(projects, ",") != strings.Join(wantProjects, ",") {
		t.Fatalf("projects %v", projects)
	}
	grants := SandboxGrants(home, []string{"app"})
	want := []string{".all/telemetry", ".all/procs", ".all/chat", ".all/session-chat", ".all/context-brief", ".all/heavy-suite.lock",
		".all/heavy-suite.lock.queue", ".all/heavy-suite.lock.holder", ".trash", "app/telemetry", "app/ids"}
	if len(grants) != len(want) {
		t.Fatalf("grants %v", grants)
	}
	for index, grant := range grants {
		if grant != filepath.Join(home, want[index]) {
			t.Fatalf("grant %d is %s, want %s", index, grant, want[index])
		}
	}
	settings := filepath.Join(t.TempDir(), "settings.json")
	quoted := func(paths []string) string {
		encoded, _ := json.Marshal(paths)
		return string(encoded)
	}
	write(t, settings, `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": `+quoted(grants)+`}}}`)
	if blocked, _ := SandboxBlocks(settings, home, []string{"app"}); blocked != "" {
		t.Fatalf("the grants were not enough: %s", blocked)
	}
	if exposed, _, _ := SandboxWritesEvidence(settings, home, []string{"app"}); exposed != "" {
		t.Fatalf("the grants expose evidence: %s", exposed)
	}
	if blocked, _ := SandboxBlocks(settings, home, []string{"app", "service"}); !strings.Contains(blocked, filepath.Join(home, "service", "ids")) {
		t.Fatalf("a new project's folders were not asked for: %q", blocked)
	}
	write(t, settings, `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": ["`+home+`"]}}}`)
	exposed, fix, entries := SandboxWritesEvidence(settings, home, []string{"app"})
	if !strings.Contains(exposed, filepath.Join(home, "app", "receipts")) || !strings.Contains(exposed, filepath.Join(home, ".all", "runs.jsonl")) ||
		len(entries) != 1 || entries[0] != home || !strings.Contains(fix, "jst setup") {
		t.Fatalf("the whole home: %q %q %v", exposed, fix, entries)
	}
	write(t, settings, `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": `+quoted(append(grants, home+"/app/receipts/*.json"))+`}}}`)
	if exposed, _, entries := SandboxWritesEvidence(settings, home, []string{"app"}); !strings.Contains(exposed, "app/receipts") ||
		len(entries) != 1 || entries[0] != home+"/app/receipts/*.json" {
		t.Fatalf("an entry for files inside the receipts: %q %v", exposed, entries)
	}
	write(t, settings, `{"sandbox": {"enabled": true, "filesystem": {"allowWrite": ["`+home+`"], "denyWrite": ["`+home+`/app/receipts", "`+home+`/app/reviews", "`+home+`/.all/runs.jsonl", "`+home+`/.all/reviews", "`+home+`/.all/review-pool", "`+home+`/.all/delivery", "`+home+`/.all/expected-projects"]}}}`)
	if exposed, _, _ := SandboxWritesEvidence(settings, home, []string{"app"}); exposed != "" {
		t.Fatalf("denied evidence counted as exposed: %s", exposed)
	}
}

func TestUsageAndPrefix(t *testing.T) {
	for _, args := range [][]string{{"--json", "--json"}, {"--prefix"}, {"extra"}, {"--expect"}} {
		var out bytes.Buffer
		if code := Run(args, nil, &out, &out); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	// --prefix wins over the binary's own prefix.
	prefix := t.TempDir()
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	var out bytes.Buffer
	Run([]string{"--json", "--prefix", prefix}, nil, &out, &out)
	if !strings.Contains(out.String(), `"prefix": "`+prefix+`"`) {
		t.Fatalf("output %s", out.String())
	}
}

func TestExpectAndForget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	var out bytes.Buffer
	if code := Run([]string{"--expect", "My Project"}, nil, &out, &out); code != 0 || !strings.Contains(out.String(), "^my%20^project") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	write(t, filepath.Join(home, "^my%20^project", "guard-seen"), "")
	out.Reset()
	if code := Run([]string{"--forget", "My Project"}, nil, &out, &out); code != 0 || !strings.Contains(out.String(), "none") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "^my%20^project", "guard-seen")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the mark was kept")
	}
	if code := Run([]string{"--expect", "  "}, nil, &out, &out); code != 2 {
		t.Fatalf("a blank name: exit %d", code)
	}
}

// The real probe runs the guard with a JACKSTAY_HOME of its own: it leaves no mark in the home doctor
// inspects, so it cannot pass for a project's hook.
func TestProbeLeavesNoMark(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	guard := filepath.Join(t.TempDir(), "jst-guard")
	write(t, guard, "#!/bin/sh\nmkdir -p \"$JACKSTAY_HOME/unknown\" && touch \"$JACKSTAY_HOME/unknown/guard-seen\"\n"+
		`echo '{"hookSpecificOutput":{"permissionDecision":"deny"}}'`+"\n")
	if err := RealInputs(t.TempDir()).ProbeGuard(guard); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("the probe wrote into the inspected home: %v", entries)
	}
}

func TestAdoptionDriftIsAProblemAndExceptionsAreNotes(t *testing.T) {
	given := inputs(t, goPrefix(t, "0.5.0 (abc)"))
	given.Adoption = func() (bool, []string, []string) {
		return true, []string{"sample is not up to date with jackstay 0.5.0"}, []string{"sample keeps file:Makefile:present: later"}
	}
	report := Diagnose(given)
	if got := kinds(report.Problems); got != "adoption" {
		t.Fatalf("problems %s", got)
	}
	if got := kinds(report.Notes); !strings.Contains(got, "adoption") || report.Facts["adoption"] == "up to date" {
		t.Fatalf("notes %s, facts %v", got, report.Facts["adoption"])
	}
	given.Adoption = func() (bool, []string, []string) { return true, nil, nil }
	if report := Diagnose(given); len(report.Problems) != 0 || report.Facts["adoption"] != "up to date" {
		t.Fatalf("%+v", report)
	}
	given.Adoption = func() (bool, []string, []string) { return false, []string{"ignored"}, nil }
	if report := Diagnose(given); len(report.Problems) != 0 || report.Facts["adoption"] != nil {
		t.Fatal("outside a project the check must be skipped")
	}
}

// signing is a Mac's inputs with identity configured: the keychain lists listed, and the live
// binary's signature is display.
func signing(t *testing.T, identity, listed, display string) Inputs {
	t.Helper()
	given := inputs(t, goPrefix(t, "0.5.0 (abc)"))
	given.Mac, given.CodesignIdentity, given.CodesignSetting = true, identity, "/h/.all/codesign-identity"
	answer := given.Run
	given.Run = func(argv []string, input []byte) (supervise.Captured, error) {
		switch argv[0] {
		case "security":
			return supervise.Captured{Stdout: []byte(listed)}, nil
		case "codesign":
			return supervise.Captured{Stderr: []byte(display)}, nil
		}
		return answer(argv, input)
	}
	return given
}

// #12: doctor judges the live binary by install's rule (install.KeepsItsSignature): a binary signed
// by the configured identity or by another certificate (a release's) is no problem only while its
// signature verifies strictly; one that does not is reported, as install would sign it again.
func TestACertifiedBinaryThatDoesNotVerifyIsAProblem(t *testing.T) {
	const owner = "Apple Development: Max Example (ABCDE12345)"
	listed := `  1) 0123456789ABCDEF0123456789ABCDEF01234567 "` + owner + `"` + "\n     1 valid identities found\n"
	for _, display := range []string{
		"Identifier=jst\nAuthority=" + owner + "\n",
		"Identifier=jst\nAuthority=Developer ID Application: Someone Else (ZZZZZ99999)\n",
	} {
		for _, verifies := range []bool{true, false} {
			checkVerified(t, signing(t, owner, listed, display), verifies)
		}
	}
}

func checkVerified(t *testing.T, given Inputs, verifies bool) {
	t.Helper()
	answer := given.Run
	given.Run = func(argv []string, input []byte) (supervise.Captured, error) {
		if argv[0] == "codesign" && argv[1] == "--verify" && !verifies {
			return supervise.Captured{Status: 1, Stderr: []byte("invalid signature\n")}, nil
		}
		return answer(argv, input)
	}
	want := ""
	if !verifies {
		want = "binary-not-signed"
	}
	if got := kinds(Diagnose(given).Problems); got != want {
		t.Fatalf("verifies %v: problems %s, want %s", verifies, got, want)
	}
}

// #12: binary-not-signed says the next install replaces the binary and signs the replacement only
// when it does not keep its own signature (a release's).
func TestBinaryNotSignedSaysWhatTheNextInstallDoes(t *testing.T) {
	const owner = "Apple Development: Max Example (ABCDE12345)"
	listed := `  1) 0123456789ABCDEF0123456789ABCDEF01234567 "` + owner + `"` + "\n     1 valid identities found\n"
	report := Diagnose(signing(t, owner, listed, "Identifier=a.out\nSignature=adhoc\n"))
	if len(report.Problems) != 1 {
		t.Fatalf("problems %+v", report.Problems)
	}
	text := fmt.Sprintf("%+v", report.Problems[0])
	for _, want := range []string{"the next install replaces it", "unless it keeps its own signature", "jst install --binary FILE", "prints exactly what the live one prints", "install a newer version"} {
		if !strings.Contains(text, want) {
			t.Fatalf("%q is not in %s", want, text)
		}
	}
}

// W-070: a configured identity that the keychain lacks (or that expired), and a live binary not signed
// by it, are problems; the signature is a fact either way.
func TestSigning(t *testing.T) {
	const owner = "Apple Development: Max Example (ABCDE12345)"
	listed := `  1) 0123456789ABCDEF0123456789ABCDEF01234567 "` + owner + `"` + "\n     1 valid identities found\n"
	signed := "Identifier=jst\nAuthority=" + owner + "\nAuthority=Apple Root CA\n"
	adHoc := "Identifier=a.out\nSignature=adhoc\n"
	for _, tc := range []struct {
		name, identity, listed, display, want string
	}{
		{"signed by the configured identity", owner, listed, signed, ""},
		{"installed before the setting", owner, listed, adHoc, "binary-not-signed"},
		// W-075: an install of 0.5.4-0.5.8 carries the earlier identifier; the next install signs it again.
		{"signed with the earlier identifier", owner, listed, "Identifier=dev.jackstay.jst\nAuthority=" + owner + "\n", "binary-not-signed"},
		// #12: a release's binary, signed by a Developer ID and notarised, is kept as it is by install.
		{"a release's binary, another certificate", owner, listed, "Identifier=jst\nAuthority=Developer ID Application: Someone Else (ZZZZZ99999)\n", ""},
		{"the identity is not in the keychain", owner, "     0 valid identities found\n", signed, "codesign-identity-invalid"},
		{"no identity configured", "", "", adHoc, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := Diagnose(signing(t, tc.identity, tc.listed, tc.display))
			if got := kinds(report.Problems); got != tc.want {
				t.Fatalf("problems %s, want %s", got, tc.want)
			}
			facts := report.Facts["signature"].(map[string]any)
			if facts["identity"] != tc.identity || facts["identifier"] == "" {
				t.Fatalf("facts %v", facts)
			}
		})
	}
	off := inputs(t, goPrefix(t, "0.5.0 (abc)"))
	if _, present := Diagnose(off).Facts["signature"]; present {
		t.Fatal("off macOS nothing is said of signing")
	}
}

// What cannot be inspected is said, not passed over.
func TestSigningThatCannotBeInspected(t *testing.T) {
	const owner = "Apple Development: Max Example (ABCDE12345)"
	signed := "Identifier=jst\nAuthority=" + owner + "\n"
	locked := signing(t, owner, "", signed)
	answer := locked.Run
	locked.Run = func(argv []string, input []byte) (supervise.Captured, error) {
		if argv[0] == "security" {
			return supervise.Captured{Status: 51, Stderr: []byte("security: SecKeychainSearchCopyNext: The specified keychain could not be found.\n")}, nil
		}
		return answer(argv, input)
	}
	if got := kinds(Diagnose(locked).Problems); got != "codesign-uninspected" {
		t.Fatalf("a keychain that does not answer: %s", got)
	}
	unread := signing(t, owner, `  1) 0123456789ABCDEF0123456789ABCDEF01234567 "`+owner+`"`+"\n", signed)
	listing := unread.Run
	unread.Run = func(argv []string, input []byte) (supervise.Captured, error) {
		if argv[0] == "codesign" {
			return supervise.Captured{}, errors.New("no answer in 30s")
		}
		return listing(argv, input)
	}
	if got := kinds(Diagnose(unread).Problems); got != "codesign-uninspected" {
		t.Fatalf("a signature that cannot be read: %s", got)
	}
	broken := signing(t, "", "", "Identifier=a.out\nSignature=adhoc\n")
	broken.CodesignErr = errors.New("/h/.all/codesign-identity holds more than one line")
	if got := kinds(Diagnose(broken).Problems); got != "codesign-setting-unreadable" {
		t.Fatalf("a setting that cannot be read: %s", got)
	}
}
