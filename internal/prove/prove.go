// Package prove runs a version folder's entry points before any rename makes it live
// (docs/design/implementation.md section 7.3): `jst version` and `jackstay version` name the folder's
// version, and `jst-guard` refuses `git reset --hard`.
// Every probe runs through supervise.Capture (a process group of its own, stopped whole at the
// deadline) with JACKSTAY_HOME a fresh temporary folder, so a probe never writes the account's state.
package prove

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/supervise"
)

// Timeout is each probe's deadline.
const Timeout = 30 * time.Second

// GuardCall is the tool call every probe of a guard is given.
const GuardCall = `{"tool_name":"Bash","cwd":"/","session_id":"jackstay-prove","tool_input":{"command":"git reset --hard"}}`

// Folder proves the version folder at path: its `jst version` and `jst version` print
// `jackstay <versionText>`, and its `jst-guard` refuses `git reset --hard`.
func Folder(ctx context.Context, path, versionText string) error {
	home, err := os.MkdirTemp("", "jackstay-prove.")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(home) }()
	env := environment(os.Environ(), home)
	for _, name := range []string{"jst", "jackstay"} {
		said, err := probe(ctx, []string{filepath.Join(path, name), "version"}, env, nil)
		if err != nil {
			return fmt.Errorf("`%s version` did not run: %w", name, err)
		}
		if want := "jackstay " + versionText; said.Status != 0 || strings.TrimSpace(string(said.Stdout)) != want {
			return fmt.Errorf("`%s version` said %q (exit %d), not %q", name, firstLine(said), said.Status, want)
		}
	}
	return Guard(ctx, filepath.Join(path, "jst-guard"), env)
}

// Guard proves that the guard at path refuses `git reset --hard`. A nil env is jackstay's own with
// JACKSTAY_HOME a fresh temporary folder.
func Guard(ctx context.Context, path string, env []string) error {
	if env == nil {
		home, err := os.MkdirTemp("", "jackstay-prove.")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(home) }()
		env = environment(os.Environ(), home)
	}
	answer, err := probe(ctx, []string{path}, env, []byte(GuardCall))
	if err != nil {
		return fmt.Errorf("%s did not run: %w", path, err)
	}
	if !Refused(answer.Stdout) {
		return fmt.Errorf("%s did not refuse `git reset --hard` (exit %d: %s)", path, answer.Status, firstLine(answer))
	}
	return nil
}

// Refused says whether a hook's answer is a PreToolUse denial.
func Refused(stdout []byte) bool {
	var answer struct {
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout, &answer); err != nil {
		return false
	}
	return answer.HookSpecificOutput.PermissionDecision == "deny"
}

func probe(parent context.Context, argv, env []string, input []byte) (supervise.Captured, error) {
	ctx, cancel := context.WithTimeout(parent, Timeout)
	defer cancel()
	captured, err := supervise.Capture(ctx, argv, "/", env, input)
	if errors.Is(err, supervise.ErrDeadline) {
		return captured, fmt.Errorf("no answer in %s", Timeout)
	}
	return captured, err
}

// environment is environ with JACKSTAY_HOME set to home and every other JACKSTAY_ setting dropped, so a probe
// takes nothing from the caller's own jackstay (a lock path, a run id).
func environment(environ []string, home string) []string {
	kept := make([]string, 0, len(environ)+1)
	for _, pair := range environ {
		if !strings.HasPrefix(pair, "JACKSTAY_") {
			kept = append(kept, pair)
		}
	}
	return append(kept, "JACKSTAY_HOME="+home)
}

func firstLine(captured supervise.Captured) string {
	text := strings.TrimSpace(string(captured.Stdout))
	if text == "" {
		text = strings.TrimSpace(string(captured.Stderr))
	}
	line, _, _ := strings.Cut(text, "\n")
	if len(line) > 300 {
		line = line[:300]
	}
	return line
}
