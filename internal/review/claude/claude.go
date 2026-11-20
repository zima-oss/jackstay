// Package claude runs the Claude Code CLI in restricted, non-interactive review mode.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/review/access"
	"github.com/zima-oss/jackstay/internal/review/runner"
	"github.com/zima-oss/jackstay/internal/strictjson"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// Name identifies the Claude Code reviewer in project settings.
const Name = "claude-code"

// UsageFile contains only provider-verified counters for the journal, never prompt or answer text.
const UsageFile = "usage.json"

// Minimum CLI version providing --restricted. Unknown version formats fail closed.
const minimumVersion = "2.1.248"

var versionPattern = regexp.MustCompile(`^(([0-9]+)\.([0-9]+)\.([0-9]+))(?:[[:blank:]]+\(Claude Code\))?$`)

func restrictedVersion(text string) string {
	parts := versionPattern.FindStringSubmatch(text)
	if parts == nil {
		return ""
	}
	var version [3]int
	for index := range version {
		number, err := strconv.Atoi(parts[index+2])
		if err != nil {
			return ""
		}
		version[index] = number
	}
	for index, minimum := range [...]int{2, 1, 248} {
		if version[index] < minimum {
			return ""
		}
		if version[index] > minimum {
			break
		}
	}
	return parts[1]
}

func environment(environ []string, temporary string) []string {
	if environ == nil {
		environ = os.Environ()
	}
	return access.Environment(environ, access.Claude, temporary)
}

func environOf(request runner.Request) []string {
	if request.Environ != nil {
		return request.Environ
	}
	return os.Environ()
}

// Temporary is the reviewer's own folder for temporary files.
func Temporary(request runner.Request) string { return filepath.Join(request.Folder, "tmp") }

// ConfigFile is the file Claude Code keeps its state in, and must be able to write.
func ConfigFile(environ []string, home string) string {
	for _, pair := range environ {
		if value, ok := strings.CutPrefix(pair, "CLAUDE_CONFIG_DIR="); ok && value != "" {
			return filepath.Join(value, ".claude.json")
		}
	}
	return filepath.Join(home, ".claude.json")
}

// CheckEnvironment refuses a run that must not start, as Codex's does (docs/design/review.md
// section 2): Claude Code cannot write its own state here, as inside an agent's sandbox. "" when
// the run may start; a missing file is made by Claude Code itself.
func CheckEnvironment(configFile string) string {
	file, err := os.OpenFile(configFile, os.O_WRONLY|os.O_APPEND, 0) // #nosec G304 -- Claude Code's own state file
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return "Claude Code cannot save its state here (" + configFile + " is not writable): run `jst review` outside the sandbox"
		}
		return "Claude Code's state cannot be opened: " + err.Error()
	}
	_ = file.Close()
	return ""
}

// Probe asks the CLI's local version. It makes no model request or login attempt.
func Probe(ctx context.Context, command string) (string, error) {
	path, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("could not find the Claude Code CLI (%s): %w", command, err)
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	captured, err := supervise.Capture(probe, []string{path, "--version"}, "", environment(nil, ""), nil)
	if err != nil || captured.Status != 0 || captured.Incomplete != "" {
		return "", fmt.Errorf("could not read a complete version from Claude Code (%s)", command)
	}
	version := restrictedVersion(strings.TrimSpace(string(captured.Stdout)))
	if version == "" {
		return "", fmt.Errorf("restricted review requires Claude Code %s or newer", minimumVersion)
	}
	return version, nil
}

// tools are what the reviewer may use: reading and searching files anywhere, and the web. No tool
// that writes or runs a command.
var tools = []string{"Read", "Glob", "Grep", "WebFetch", "WebSearch"}

// Settings is the reviewer's access profile as Claude Code settings: reading the account's
// secrets and `.env` files is denied, whatever the working folders.
func Settings(home string, environ []string) string {
	var deny []string
	for _, path := range access.SecretPaths(home, access.Claude, environ) {
		for _, tool := range []string{"Read", "Glob", "Grep"} {
			deny = append(deny, tool+"(/"+path+"/**)", tool+"(/"+path+")")
		}
	}
	for _, glob := range access.SecretFileGlobs {
		for _, tool := range []string{"Read", "Glob", "Grep"} {
			deny = append(deny, tool+"("+glob+")")
		}
	}
	data, _ := json.Marshal(map[string]any{"permissions": map[string]any{"deny": deny}, "disableAllHooks": true})
	return string(data)
}

func argv(request runner.Request, executable string, schema []byte) []string {
	allowed := slices.Clone(tools)
	servers := map[string]any{}
	for name, server := range request.MCPServers {
		servers[name] = map[string]any{"command": server.Command, "args": server.Args}
		allowed = append(allowed, "mcp__"+name+"__*")
	}
	mcp, _ := json.Marshal(map[string]any{"mcpServers": servers})
	words := []string{executable, "--print", "--restricted", "--safe-mode", "--no-session-persistence",
		"--output-format", "json", "--json-schema", string(schema),
		"--permission-mode", "dontAsk", "--tools", strings.Join(tools, ","), "--allowedTools", strings.Join(allowed, ","),
		"--add-dir", "/", "--settings", Settings(homedir.AccountHome(), environOf(request)),
		"--strict-mcp-config", "--mcp-config", string(mcp)}
	if len(request.MCPServers) == 0 {
		words = append(words, "--disallowedTools", "mcp__*")
	}
	if request.Model != "" {
		words = append(words, "--model="+request.Model)
	}
	if request.Effort != "" {
		words = append(words, "--effort="+request.Effort)
	}
	return words
}

// Usage is the token counters emitted by Claude Code in a result envelope.
type Usage struct {
	Input       int64 `json:"input_tokens"`
	CachedInput int64 `json:"cache_read_input_tokens"`
	CacheWrite  int64 `json:"cache_creation_input_tokens"`
	Output      int64 `json:"output_tokens"`
}

type response struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	IsError    *bool           `json:"is_error"`
	Structured json.RawMessage `json:"structured_output"`
	Usage      *Usage          `json:"usage"`
}

func authenticationProblem(captured supervise.Captured) bool {
	words := strings.ToLower(string(captured.Stdout) + "\n" + string(captured.Stderr))
	for _, diagnostic := range []string{"not logged in", "please run /login", "authentication_error", "invalid api key"} {
		if strings.Contains(words, diagnostic) {
			return true
		}
	}
	return false
}

// Run reads the structured result of one restricted Claude Code session. The prompt is passed
// on stdin, and the only file accepted as the verdict is written by jackstay.
func Run(ctx context.Context, request runner.Request) runner.Result {
	result := runner.Result{Started: time.Now(), Exit: -1, AnswerPath: filepath.Join(request.Folder, "answer.json")}
	finish := func(outcome, reason string) runner.Result {
		result.Ended, result.Outcome, result.Reason = time.Now(), outcome, reason
		return result
	}
	if request.Answered != nil {
		release := sync.OnceFunc(request.Answered)
		timer := time.AfterFunc(2*time.Minute, release)
		defer timer.Stop()
		defer release()
	}

	path, err := exec.LookPath(request.Command)
	if err != nil {
		return finish(runner.OutcomeRefused, "Claude Code is not installed")
	}
	schema, err := os.ReadFile(request.SchemaPath) // #nosec G304 -- jackstay's own verdict schema
	if err != nil || len(schema) > strictjson.DefaultLimit || !json.Valid(schema) {
		return finish(runner.OutcomeFailed, "the review schema could not be read")
	}
	result.Argv = argv(request, path, schema)
	temporary := Temporary(request)
	if err := os.MkdirAll(temporary, 0o700); err != nil {
		return finish(runner.OutcomeFailed, "the reviewer's temporary folder could not be made")
	}
	bounded, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	captured, err := supervise.CaptureTraced(bounded, result.Argv, access.RedactedArgv(result.Argv), request.Tree,
		environment(request.Environ, temporary), request.Prompt)
	result.Exit = captured.Status
	switch {
	case ctx.Err() != nil:
		return finish(runner.OutcomeFailed, "jackstay was asked to stop Claude Code")
	case bounded.Err() != nil:
		return finish(runner.OutcomeTimeout, fmt.Sprintf("Claude Code did not finish within %s", request.Timeout))
	case err != nil:
		return finish(runner.OutcomeFailed, "Claude Code could not be run")
	case captured.Incomplete != "":
		return finish(runner.OutcomeFailed, "Claude Code returned incomplete output")
	}
	var answer response
	decodeErr := strictjson.UnmarshalOpen(captured.Stdout, &answer)
	if captured.Status != 0 || answer.IsError != nil && *answer.IsError {
		if authenticationProblem(captured) {
			return finish(runner.OutcomeLogin, "Claude Code authentication failed; check `claude auth status` and sign in if needed")
		}
		return finish(runner.OutcomeFailed, fmt.Sprintf("Claude Code did not complete the review (exit %d)", captured.Status))
	}
	structured := bytes.TrimSpace(answer.Structured)
	if decodeErr != nil || answer.Type != "result" || answer.Subtype != "success" ||
		answer.IsError == nil || len(structured) == 0 || bytes.Equal(structured, []byte("null")) {
		return finish(runner.OutcomeNoAnswer, "Claude Code returned no successful structured verdict")
	}
	if len(structured) > strictjson.DefaultLimit {
		return finish(runner.OutcomeFailed, "Claude Code's structured verdict exceeds the review answer limit")
	}
	if err := os.WriteFile(result.AnswerPath, structured, 0o600); err != nil {
		return finish(runner.OutcomeFailed, "could not save Claude Code's structured verdict")
	}
	if usage := answer.Usage; usage != nil && usage.Input >= 0 && usage.CachedInput >= 0 && usage.CacheWrite >= 0 && usage.Output >= 0 {
		if data, err := json.Marshal(usage); err == nil {
			_ = os.WriteFile(filepath.Join(request.Folder, UsageFile), data, 0o600)
		}
	}
	return finish(runner.OutcomeDone, "")
}
