package claude

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/review/access"
	"github.com/zima-oss/jackstay/internal/review/runner"
)

func TestTheReviewerReadsEverywhereButWritesNothingAndRunsNoCommand(t *testing.T) {
	words := argv(runner.Request{PinnedSource: true, Tree: "/projected/review"}, "claude", []byte(`{"type":"object"}`))
	for _, want := range []string{"--restricted", "--safe-mode", "Read,Glob,Grep,WebFetch,WebSearch", "dontAsk", "mcp__*", "--strict-mcp-config"} {
		if !slices.Contains(words, want) {
			t.Errorf("missing %q: %v", want, words)
		}
	}
	if index := slices.Index(words, "--add-dir"); index < 0 || words[index+1] != "/" {
		t.Errorf("the reviewer cannot read outside the source view: %v", words)
	}
	tools := words[slices.Index(words, "--tools")+1]
	for _, forbidden := range []string{"Bash", "Edit", "Write", "NotebookEdit"} {
		if strings.Contains(tools, forbidden) {
			t.Errorf("a tool that writes or runs commands: %q", tools)
		}
	}
	if slices.Contains(words, "--dangerously-skip-permissions") {
		t.Errorf("permissions bypassed: %v", words)
	}
	settings := words[slices.Index(words, "--settings")+1]
	for _, want := range []string{"Read(//home/r/.ssh/**)", "Grep(//home/r/.aws/**)", "Read(//home/r/.config/gh/**)", "Read(//**/.env)", "Read(//**/.env.*)", "Grep(//**/.env)", `"disableAllHooks":true`} {
		if !strings.Contains(Settings("/home/r", nil), want) {
			t.Errorf("the settings lack %s: %s", want, Settings("/home/r", nil))
		}
	}
	if !strings.Contains(settings, `"deny"`) {
		t.Errorf("no deny rules passed: %s", settings)
	}
}

func TestClaudeCodeCannotReadCodexsLogin(t *testing.T) {
	for environ, folder := range map[string]string{"": "/home/r/.codex", "CODEX_HOME=/elsewhere/codex": "/elsewhere/codex"} {
		settings := Settings("/home/r", []string{environ})
		if !strings.Contains(settings, "Read(/"+folder+"/**)") {
			t.Errorf("Codex's login %s is readable: %s", folder, settings)
		}
	}
}

func TestAProjectCanGiveBackChosenMCPServers(t *testing.T) {
	words := argv(runner.Request{MCPServers: map[string]access.MCPServer{"docs": {Command: "docs-server", Args: []string{"--stdio"}}}},
		"claude", []byte(`{}`))
	if slices.Contains(words, "--disallowedTools") {
		t.Fatalf("given servers are still denied: %v", words)
	}
	if allowed := words[slices.Index(words, "--allowedTools")+1]; !strings.Contains(allowed, "mcp__docs__*") {
		t.Fatalf("the server's tools are not allowed: %s", allowed)
	}
	if config := words[slices.Index(words, "--mcp-config")+1]; !strings.Contains(config, `"docs":{"args":["--stdio"],"command":"docs-server"}`) {
		t.Fatalf("mcp config: %s", config)
	}
}

func TestClaudeRefusesToStartWhereItCannotSaveItsState(t *testing.T) {
	folder := t.TempDir()
	config := ConfigFile([]string{"CLAUDE_CONFIG_DIR=" + folder}, "/home/r")
	if config != filepath.Join(folder, ".claude.json") || ConfigFile(nil, "/home/r") != "/home/r/.claude.json" {
		t.Fatalf("config file: %s", config)
	}
	if reason := CheckEnvironment(config); reason != "" {
		t.Fatalf("a missing state file refused: %s", reason)
	}
	if err := os.WriteFile(config, []byte("{}"), 0o400); err != nil {
		t.Fatal(err)
	}
	if reason := CheckEnvironment(config); !strings.Contains(reason, "outside the sandbox") {
		t.Fatalf("an unwritable state file: %q", reason)
	}
	if err := os.Chmod(config, 0o600); err != nil {
		t.Fatal(err)
	}
	if reason := CheckEnvironment(config); reason != "" {
		t.Fatalf("a writable state file refused: %s", reason)
	}
}

func TestClaudeGetsItsOwnLoginAndNotCodexs(t *testing.T) {
	environ := environment([]string{"ANTHROPIC_API_KEY=a", "OPENAI_API_KEY=o", "CLAUDE_CODE_OAUTH_TOKEN=c", "CLAUDECODE=1",
		"CLAUDE_CODE_SESSION_ID=s", "CLAUDE_CONFIG_DIR=/c", "PATH=/p"}, "/t")
	joined := strings.Join(environ, " ")
	for _, kept := range []string{"ANTHROPIC_API_KEY=a", "CLAUDE_CODE_OAUTH_TOKEN=c", "CLAUDE_CONFIG_DIR=/c", "PATH=/p", "TMPDIR=/t"} {
		if !strings.Contains(joined, kept) {
			t.Errorf("%s did not reach Claude Code: %s", kept, joined)
		}
	}
	for _, gone := range []string{"OPENAI_API_KEY", "CLAUDECODE", "CLAUDE_CODE_SESSION_ID"} {
		if strings.Contains(joined, gone) {
			t.Errorf("%s reached Claude Code: %s", gone, joined)
		}
	}
}
