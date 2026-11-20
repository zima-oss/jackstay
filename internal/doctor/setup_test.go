package doctor

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/adopt"
	"github.com/zima-oss/jackstay/internal/config"
)

func TestPathFindingAndPlan(t *testing.T) {
	home := t.TempDir()
	prefix := filepath.Join(home, "tools")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "bin", "jst"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := Report{Facts: map[string]any{"prefix": prefix}}
	checkPath(&report, Inputs{Prefix: prefix, CheckPath: true})
	if len(report.Problems) != 1 || report.Problems[0].Kind != "path-not-first" {
		t.Fatalf("path findings: %+v", report.Problems)
	}
	plan := planSetup(report, home, "/bin/zsh")
	if len(plan.Items) != 1 || plan.Items[0].Status != "ready" || plan.Items[0].Path != filepath.Join(home, ".zshrc") {
		t.Fatalf("setup plan: %+v", plan)
	}
	if !strings.Contains(plan.Items[0].Add, "export PATH='") {
		t.Fatalf("setup line: %q", plan.Items[0].Add)
	}
	if !strings.Contains(plan.Items[0].Add, ":\"$PATH\"") {
		t.Fatalf("setup line: %q", plan.Items[0].Add)
	}
	activeReport := Report{Facts: map[string]any{}}
	checkPath(&activeReport, Inputs{Prefix: prefix, CheckPath: true, ActiveJst: filepath.Join(prefix, "bin", "jst")})
	if len(activeReport.Problems) != 0 {
		t.Fatalf("active install was reported as missing: %+v", activeReport.Problems)
	}
	if got := planSetup(report, home, "/bin/fish").Items[0]; got.Status != "manual" || got.Path != "" {
		t.Fatalf("unsupported shell: %+v", got)
	}
	if err := os.Remove(filepath.Join(prefix, "bin", "jst")); err != nil {
		t.Fatal(err)
	}
	if got := planSetup(report, home, "/bin/zsh").Items[0]; got.Status != "manual" || !strings.Contains(got.Fix, "launcher") {
		t.Fatalf("missing launcher: %+v", got)
	}
	if terminalInput(strings.NewReader("a\n")) {
		t.Fatal("piped input was treated as a terminal")
	}
	var output bytes.Buffer
	printSetupPlan(&output, plan)
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatalf("planning changed profile: %v", err)
	}
}

func TestMacBashSetupUsesActiveLoginProfile(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing []string
		want     string
	}{
		{name: "default", want: ".bash_profile"},
		{name: "bash profile", existing: []string{".bash_profile", ".bash_login", ".profile"}, want: ".bash_profile"},
		{name: "bash login", existing: []string{".bash_login", ".profile"}, want: ".bash_login"},
		{name: "profile", existing: []string{".profile"}, want: ".profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			for _, name := range tc.existing {
				if err := os.WriteFile(filepath.Join(home, name), []byte("# active\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := shellProfileForOS(home, "/bin/bash", "darwin"); got != filepath.Join(home, tc.want) {
				t.Fatalf("selected %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSandboxRepairReplacesTheWholeHomeKeepingOrderAndBackup(t *testing.T) {
	home := realPath(t.TempDir())
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	stateHome := filepath.Join(home, ".jackstay")
	original := []byte(`{
  "permissions": {"allow": ["Bash(ls:*)"]},
  "sandbox": {
    "enabled": true,
    "filesystem": {"denyWrite": ["/unrelated"], "allowWrite": ["/other", "` + stateHome + `/**", "` + stateHome + `/.all/procs"]},
    "other": 17
  },
  "custom": {"long": 12345678901234567890}
}
`)
	if err := os.WriteFile(settings, original, 0o600); err != nil {
		t.Fatal(err)
	}
	report := Report{Facts: map[string]any{"settings": settings, "home": stateHome, "sandbox_projects": []string{"app"}},
		Problems: []Finding{{Kind: "sandbox-blocks-state", Text: "blocked", Fix: "add allowWrite"},
			{Kind: "sandbox-writes-evidence", Text: "exposed", Fix: "narrow"}}}
	plan := planSetup(report, home, "/bin/zsh")
	if len(plan.Items) != 1 {
		t.Fatalf("one sandbox item expected: %+v", plan.Items)
	}
	item := plan.Items[0]
	if item.Status != "ready" || !strings.Contains(item.Diff, "-\""+stateHome+"/**\"") ||
		!strings.Contains(item.Diff, "+\""+filepath.Join(stateHome, "app", "ids")+"\"") ||
		strings.Contains(item.Diff, "+\""+filepath.Join(stateHome, ".all", "procs")+"\"") || strings.Contains(item.Diff, "Bash(ls:*)") {
		t.Fatalf("sandbox plan: %+v\n%s", item, item.Diff)
	}
	var output, diagnostics bytes.Buffer
	if status := executeSetup(plan, true, map[string]bool{"sandbox": true}, strings.NewReader(""), &output, &diagnostics); status != 0 {
		t.Fatalf("sandbox apply: %d, %s", status, diagnostics.String())
	}
	backup := strings.TrimSpace(strings.TrimPrefix(strings.Split(output.String(), "\n")[0], "jst setup: saved previous profile at "))
	stored, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(stored, original) {
		t.Fatalf("sandbox backup: %q, %v", stored, err)
	}
	updated, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	order := []string{`"permissions"`, `"sandbox"`, `"enabled"`, `"filesystem"`, `"denyWrite"`, `"allowWrite"`, `"/other"`,
		`"other": 17`, `"custom"`, `12345678901234567890`}
	last := -1
	for _, piece := range order {
		at := strings.Index(text, piece)
		if at <= last {
			t.Fatalf("%s out of order in:\n%s", piece, text)
		}
		last = at
	}
	if strings.Contains(text, stateHome+"/**") || !strings.Contains(text, filepath.Join(stateHome, ".all", "heavy-suite.lock")) {
		t.Fatalf("allowWrite not narrowed: %s", text)
	}
	for _, grant := range SandboxGrants(stateHome, []string{"app"}) {
		if _, err := os.Stat(grant); err != nil {
			t.Fatalf("grant not made: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(stateHome, "app", "receipts")); err == nil {
		t.Fatal("setup made an evidence folder")
	}
	report.Facts["settings"] = settings
	if again := planSetup(report, home, "/bin/zsh"); again.Items[0].Status == "ready" {
		t.Fatalf("a second plan changes again: %+v", again.Items[0])
	}
}

func TestSandboxRepairLeavesAnOwnersBroadEntryAndSaysSo(t *testing.T) {
	home := realPath(t.TempDir())
	settings := filepath.Join(home, "settings.json")
	stateHome := filepath.Join(home, ".jackstay")
	if err := os.WriteFile(settings, []byte(`{"sandbox":{"enabled":true,"filesystem":{"allowWrite":["`+home+`/**"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Report{Facts: map[string]any{"settings": settings, "home": stateHome},
		Problems: []Finding{{Kind: "sandbox-writes-evidence", Text: "exposed", Fix: "narrow"}}}
	item := planSetup(report, home, "/bin/zsh").Items[0]
	if item.Status != "manual" || !strings.Contains(item.Fix, "narrow \""+home+"/**\"") {
		t.Fatalf("broad entry: %+v", item)
	}
}

func TestSandboxRepairRefusesUnsafeInputAndConcurrentEdit(t *testing.T) {
	home := t.TempDir()
	settings := filepath.Join(home, "settings.json")
	stateHome := filepath.Join(home, ".jackstay")
	for name, content := range map[string]string{
		"malformed":        `{`,
		"duplicate keys":   `{"sandbox":{"enabled":false,"enabled":true}}`,
		"deny":             `{"sandbox":{"enabled":true,"filesystem":{"denyWrite":["` + stateHome + `"]}}}`,
		"deny inside":      `{"sandbox":{"enabled":true,"filesystem":{"denyWrite":["` + stateHome + `/.all/procs/sessions"]}}}`,
		"wrong allow type": `{"sandbox":{"enabled":true,"filesystem":{"allowWrite":"bad"}}}`,
		"not enabled":      `{"sandbox":{"enabled":"yes"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(settings, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, _, _, err := prepareSandbox(settings, stateHome, nil); err == nil {
				t.Fatal("unsafe settings accepted")
			}
		})
	}
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "elsewhere"), settings); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err := prepareSandbox(settings, stateHome, nil)
	if err == nil || !strings.Contains(err.Error(), settings+" is a symbolic link (to "+filepath.Join(home, "elsewhere")+")") {
		t.Fatalf("symlink settings: %v", err)
	}
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"sandbox":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	original, replacement, mode, _, err := prepareSandbox(settings, stateHome, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"sandbox":{"enabled":true},"user":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := replaceWithBackup(settings, original, replacement, mode); err == nil {
		t.Fatal("concurrent edit overwritten")
	}
	content, err := os.ReadFile(settings)
	if err != nil || !bytes.Contains(content, []byte(`"user":1`)) {
		t.Fatalf("concurrent edit lost: %s, %v", content, err)
	}
}

func TestASymlinkedProfileIsNamedInTheError(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, ".zshrc")
	if err := os.Symlink(filepath.Join(home, "dotfiles", "zshrc"), profile); err != nil {
		t.Fatal(err)
	}
	_, err := applyProfile(profile, "export PATH='/x':\"$PATH\"")
	if err == nil || !strings.Contains(err.Error(), profile+" is a symbolic link (to "+filepath.Join(home, "dotfiles", "zshrc")+")") {
		t.Fatalf("error %v", err)
	}
}

func TestApplyProfileBackupAndRepeat(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, ".zshrc")
	original := []byte("# user setting\nexport EDITOR=vi")
	if err := os.WriteFile(profile, original, 0o640); err != nil {
		t.Fatal(err)
	}
	line := "export PATH='/example/bin':\"$PATH\""
	backup, err := applyProfile(profile, line)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(stored, original) {
		t.Fatalf("backup: %q, %v", stored, err)
	}
	updated, err := os.ReadFile(profile)
	if err != nil || !bytes.Equal(updated, []byte(string(original)+"\n# jackstay setup: PATH\n"+line+"\n")) {
		t.Fatalf("updated profile: %q, %v", updated, err)
	}
	info, err := os.Stat(profile)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("profile mode: %v, %v", info, err)
	}
	if _, err := applyProfile(profile, line); err == nil {
		t.Fatal("repeat apply wrote another entry")
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 2 {
		t.Fatalf("repeat apply left extra files: %+v", entries)
	}
}

func TestProfileConflictsStayManual(t *testing.T) {
	home := t.TempDir()
	prefix := filepath.Join(home, "tools")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "bin", "jst"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := Report{Facts: map[string]any{"prefix": prefix},
		Problems: []Finding{{Kind: "path-not-first", Text: "missing", Fix: "fix path"}}}
	profile := filepath.Join(home, ".zshrc")
	if err := os.Symlink(filepath.Join(home, "elsewhere"), profile); err != nil {
		t.Fatal(err)
	}
	if item := planSetup(report, home, "/bin/zsh").Items[0]; item.Status != "manual" {
		t.Fatalf("symlink profile: %+v", item)
	}
	if _, err := applyProfile(profile, "line"); err == nil {
		t.Fatal("symlink profile was overwritten")
	}
	if err := os.Remove(profile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile, []byte("# jackstay setup: PATH\nexport PATH='/old':\"$PATH\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if item := planSetup(report, home, "/bin/zsh").Items[0]; item.Status != "manual" {
		t.Fatalf("existing entry: %+v", item)
	}
}

func TestSetupSelectionAndTerminalChoices(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, ".profile")
	prefix := filepath.Join(home, "tools")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "bin", "jst"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	matchingLine := "export PATH=" + quotedShellWord(filepath.Join(prefix, "bin")) + ":\"$PATH\""
	plan := SetupPlan{Items: []SetupItem{{ID: "path", Status: "ready", Path: profile, Add: matchingLine}}}
	var output, diagnostics bytes.Buffer
	if status := executeSetup(plan, false, nil, strings.NewReader("s\nk\n"), &output, &diagnostics); status != 0 {
		t.Fatalf("skip status: %d, %s", status, diagnostics.String())
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("skip changed profile: %v", err)
	}
	if !strings.Contains(output.String(), "+export PATH=") {
		t.Fatalf("show did not print addition: %q", output.String())
	}
	output.Reset()
	if status := executeSetup(plan, true, map[string]bool{"path": true}, strings.NewReader(""), &output, &diagnostics); status != 0 {
		t.Fatalf("explicit selection status: %d, %s", status, diagnostics.String())
	}
	// A pre-existing entry for the same prefix is considered complete even when this shell has
	// not loaded the profile yet.
	report := Report{Facts: map[string]any{"prefix": prefix}, Problems: []Finding{{Kind: "path-not-first"}}}
	repeated := planSetup(report, home, "/bin/sh")
	if repeated.Items[0].Status != "done" {
		t.Fatalf("repeat plan: %+v", repeated)
	}
	if status := executeSetup(repeated, true, map[string]bool{"path": true}, strings.NewReader(""), &output, &diagnostics); status != 0 {
		t.Fatalf("repeat selection status: %d", status)
	}
	content, err := os.ReadFile(profile)
	if err != nil || strings.Count(string(content), "# jackstay setup: PATH") != 1 {
		t.Fatalf("repeat profile: %q, %v", content, err)
	}
}

func TestChatEngineSetupIsExplicitAndDoesNotPromiseDelivery(t *testing.T) {
	items := chatEngineSetupItems(func(name string) (string, error) {
		if name == "codex" {
			return "/fake/bin/codex", nil
		}
		return "", errors.New("not found")
	})
	if len(items) != 3 || items[0].ID != "chat-codex" || items[1].ID != "chat-claude" || items[2].ID != "chat-other" {
		t.Fatalf("chat engine steps: %+v", items)
	}
	for _, item := range items {
		if item.Status != "manual" || !strings.Contains(item.Fix, "roll back") {
			t.Fatalf("premature delivery claim or missing rollback: %+v", item)
		}
	}
	if !strings.Contains(items[0].Text, "poll-only") || !strings.Contains(items[1].Text, "next-boundary") || !strings.Contains(items[2].Text, "poll-only") {
		t.Fatalf("incorrect host capabilities: %+v", items)
	}
	if !strings.Contains(items[0].Text, "detected on PATH") || !strings.Contains(items[1].Text, "not found on PATH") {
		t.Fatalf("engine detection: %+v", items)
	}
	var output bytes.Buffer
	reader := bufio.NewReader(strings.NewReader("s\nk\nk\na\n"))
	promptChatEngineSetup(SetupPlan{Items: items}, reader, &output)
	if !strings.Contains(output.String(), "jst chat codex-bridge") || strings.Contains(output.String(), "jst chat claude-hook") {
		t.Fatalf("interactive choice did not show only selected route: %s", output.String())
	}
	if answer, err := reader.ReadString('\n'); err != nil || answer != "a\n" {
		t.Fatalf("chat prompts consumed the next repair choice: %q, %v", answer, err)
	}
}

// Setup asks who merges once per machine and keeps the answer adopt offers first.
func TestSetupAsksWhoMergesOncePerMachine(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	unanswered := autonomyItem(adopt.MachineAutonomy())
	if unanswered.Status != "ask" {
		t.Fatalf("a new machine: %+v", unanswered)
	}
	var output bytes.Buffer
	plan := SetupPlan{Items: []SetupItem{unanswered}}
	if err := askMachineAutonomy(plan, bufio.NewReader(strings.NewReader("2\n")), &output, adopt.SaveMachineAutonomy); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), adopt.AnswerMergeWhenGreen) {
		t.Fatalf("the question was not asked:\n%s", output.String())
	}
	saved, err := adopt.MachineAutonomy()
	if err != nil || saved != config.AutonomyPrepare {
		t.Fatalf("kept %q, %v", saved, err)
	}
	answered := autonomyItem(saved, nil)
	if answered.Status != "done" {
		t.Fatalf("an answered machine: %+v", answered)
	}
	output.Reset()
	if err := askMachineAutonomy(SetupPlan{Items: []SetupItem{answered}}, bufio.NewReader(strings.NewReader("")), &output, adopt.SaveMachineAutonomy); err != nil || output.Len() != 0 {
		t.Fatalf("asked again: %q, %v", output.String(), err)
	}
	if item := autonomyItem("", errors.New("broken")); item.Status != "manual" {
		t.Fatalf("an unreadable answer: %+v", item)
	}
	var stdout, stderr bytes.Buffer
	if code := RunSetup([]string{"--autonomy", "merge-when-green"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("--autonomy: exit %d: %s", code, stderr.String())
	}
	if saved, _ := adopt.MachineAutonomy(); saved != config.AutonomyMergeWhenGreen {
		t.Fatalf("--autonomy kept %q", saved)
	}
	for _, args := range [][]string{{"--autonomy", "always"}, {"--autonomy", "prepare", "--plan"}} {
		if code := RunSetup(args, strings.NewReader(""), &stdout, &stderr); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
}

// An unattended first run (--yes --select path, stdin closed) applies the selected repair and
// asks no question, nor keeps an answer nobody gave.
func TestAnUnattendedSetupAsksNothing(t *testing.T) {
	t.Setenv("JACKSTAY_HOME", t.TempDir())
	plan := SetupPlan{Items: []SetupItem{{ID: "path", Status: "done"}, autonomyItem(adopt.MachineAutonomy())}}
	var stdout, stderr bytes.Buffer
	if code := applySetup(plan, true, false, map[string]bool{"path": true}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "Who merges") {
		t.Fatalf("asked unattended:\n%s", stdout.String())
	}
	if saved, err := adopt.MachineAutonomy(); saved != "" || err != nil {
		t.Fatalf("kept %q, %v", saved, err)
	}
}
