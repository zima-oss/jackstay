package contextbrief

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/project"
)

func init() {
	cli.Register(
		cli.Command{Name: "brief save", Summary: "save a task brief for this worktree", Usage: "usage: jst brief save --expect REV [--max-age DURATION] < brief.json\n\nSave explicit JSON with task_id, task, next (1–12 strings), optional decisions (up to 12 strings). REV is the current revision from `brief show`, initially 0. Max age defaults to 168h and must be 1h–720h. No transcript is read.\n\nexit: 0 saved; 2 invalid input, conflict or unavailable state.\n\nexample: jst brief save --expect 0 < brief.json\n", PrivateArgs: true, Run: runSave},
		cli.Command{Name: "brief show", Summary: "show this worktree's saved task brief", Usage: "usage: jst brief show\n\nPrint JSON with revision, freshness and brief (null after clear).\n\nexit: 0 printed; 2 invalid arguments or unavailable state.\n\nexample: jst brief show\n", PrivateArgs: true, Run: runShow},
		cli.Command{Name: "brief clear", Summary: "clear this worktree's brief while preserving its revision", Usage: "usage: jst brief clear --expect REV\n\nLeave a revision tombstone, so stale writers cannot overwrite a later task.\n\nexit: 0 cleared; 2 invalid arguments, conflict or unavailable state.\n\nexample: jst brief clear --expect 1\n", PrivateArgs: true, Run: runClear},
		cli.Command{Name: "brief claude-hook", Summary: "restore a brief after Claude Code compaction", Usage: "usage: jst brief claude-hook\n\nUse in an opt-in Claude Code SessionStart hook with matcher compact. Emits context only for this exact worktree and a fresh brief.\n\nexit: 0 context or no context; 2 invalid arguments.\n\nexample: jst brief claude-hook\n", PrivateArgs: true, Run: runClaudeHook},
	)
}

func currentStore() (Store, string, error) {
	current, err := project.Here(nil)
	if err != nil {
		return Store{}, "", err
	}
	store, err := StoreFor(current.Root)
	return store, current.Root, err
}

func gitIdentity(root string) (Identity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	git := gitcli.Git{Dir: root}
	branch, err := git.Line(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Identity{}, err
	}
	head, err := git.Line(ctx, "rev-parse", "HEAD")
	if err != nil {
		return Identity{}, err
	}
	if branch == "HEAD" {
		branch = "detached:" + head
	}
	commonDirectory, err := git.CommonDir(ctx)
	if err != nil {
		return Identity{}, err
	}
	info, err := os.Stat(commonDirectory)
	if err != nil {
		return Identity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		return Identity{}, fmt.Errorf("cannot identify Git common directory")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)))
	return Identity{Branch: branch, Head: head, RepositoryID: hex.EncodeToString(digest[:])}, nil
}

func expectedRevision(args []string, name string) (uint64, string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	expect := flags.String("expect", "", "current revision")
	maxAge := flags.String("max-age", "", "maximum age")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *expect == "" {
		return 0, "", fmt.Errorf("%s requires --expect REV and valid options", name)
	}
	revision, err := strconv.ParseUint(*expect, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("%s requires --expect REV and valid options", name)
	}
	return revision, *maxAge, nil
}

func runSave(args []string, input io.Reader, output, diagnostics io.Writer) int {
	expected, ageOption, err := expectedRevision(args, "brief save")
	if err != nil {
		fmt.Fprintf(diagnostics, "brief: %v\n", err)
		return 2
	}
	maxAge := defaultMaxAge
	if ageOption != "" {
		maxAge, err = time.ParseDuration(ageOption)
		if err != nil {
			fmt.Fprintln(diagnostics, "brief: invalid max age")
			return 2
		}
	}
	store, root, err := currentStore()
	if err == nil {
		var encoded []byte
		encoded, err = io.ReadAll(io.LimitReader(input, maxBriefBytes+1))
		if err == nil {
			if len(encoded) > maxBriefBytes {
				err = fmt.Errorf("brief exceeds %d bytes", maxBriefBytes)
			} else {
				var brief Brief
				decoder := json.NewDecoder(bytes.NewReader(encoded))
				decoder.DisallowUnknownFields()
				if err = decoder.Decode(&brief); err == nil {
					var extra any
					if decoder.Decode(&extra) != io.EOF {
						err = fmt.Errorf("brief input must contain one JSON object")
					} else {
						var identity Identity
						identity, err = gitIdentity(root)
						if err == nil {
							_, err = store.Save(brief, expected, maxAge, identity)
						}
					}
				}
			}
		}
	}
	if err != nil {
		fmt.Fprintf(diagnostics, "brief: %v\n", err)
		return 2
	}
	_, _ = fmt.Fprintln(output, "brief saved")
	return 0
}

func runShow(args []string, _ io.Reader, output, diagnostics io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(diagnostics, "brief show takes no arguments")
		return 2
	}
	store, root, err := currentStore()
	if err != nil {
		fmt.Fprintf(diagnostics, "brief: %v\n", err)
		return 2
	}
	record, err := store.Load()
	if err != nil {
		fmt.Fprintf(diagnostics, "brief: %v\n", err)
		return 2
	}
	identity, _ := gitIdentity(root)
	freshness := store.Freshness(record, identity)
	brief := record.Brief
	if freshness == "repository-changed" || freshness == "git-unavailable" {
		brief = nil
	}
	response := struct {
		Revision  uint64 `json:"revision"`
		Freshness string `json:"freshness"`
		Brief     *Brief `json:"brief"`
	}{record.Revision, freshness, brief}
	if err := json.NewEncoder(output).Encode(response); err != nil {
		fmt.Fprintf(diagnostics, "brief: %v\n", err)
		return 2
	}
	return 0
}

func runClear(args []string, _ io.Reader, output, diagnostics io.Writer) int {
	expected, ageOption, err := expectedRevision(args, "brief clear")
	if err == nil && ageOption != "" {
		err = fmt.Errorf("brief clear does not accept --max-age")
	}
	if err == nil {
		var store Store
		store, _, err = currentStore()
		if err == nil {
			_, err = store.Clear(expected)
		}
	}
	if err != nil {
		fmt.Fprintf(diagnostics, "brief: %v\n", err)
		return 2
	}
	_, _ = fmt.Fprintln(output, "brief cleared")
	return 0
}

func runClaudeHook(args []string, input io.Reader, output, diagnostics io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(diagnostics, "brief claude-hook takes no arguments")
		return 2
	}
	encoded, err := io.ReadAll(io.LimitReader(input, (16<<10)+1))
	if err != nil || len(encoded) > 16<<10 {
		return 0
	}
	var event struct {
		Name   string `json:"hook_event_name"`
		Source string `json:"source"`
		CWD    string `json:"cwd"`
	}
	if json.Unmarshal(encoded, &event) != nil || event.Name != "SessionStart" || event.Source != "compact" || event.CWD == "" {
		return 0
	}
	store, currentRoot, err := currentStore()
	if err != nil {
		return 0
	}
	hookProject, err := project.Open(event.CWD, nil)
	if err != nil || !sameRoot(currentRoot, hookProject.Root) {
		return 0
	}
	record, err := store.Load()
	if err != nil || record.Brief == nil {
		return 0
	}
	identity, err := gitIdentity(currentRoot)
	if err != nil {
		return 0
	}
	freshness := store.Freshness(record, identity)
	if freshness != "current" && freshness != "head-changed" {
		return 0
	}
	var context strings.Builder
	context.WriteString("Saved local Jackstay task brief for this worktree; verify claims against current repository state.\n")
	if freshness == "head-changed" {
		context.WriteString("HEAD changed since this note was saved; verify the note before acting.\n")
	}
	context.WriteString("Task ID: " + record.Brief.TaskID + "\nTask: " + record.Brief.Task + "\nNext:\n")
	for _, item := range record.Brief.Next {
		context.WriteString("- " + item + "\n")
	}
	if len(record.Brief.Decisions) > 0 {
		context.WriteString("Decisions:\n")
		for _, item := range record.Brief.Decisions {
			context.WriteString("- " + item + "\n")
		}
	}
	response := struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}{}
	response.HookSpecificOutput.HookEventName = "SessionStart"
	response.HookSpecificOutput.AdditionalContext = context.String()
	if err := json.NewEncoder(output).Encode(response); err != nil {
		fmt.Fprintf(diagnostics, "brief: cannot write hook response: %v\n", err)
	}
	return 0
}

func sameRoot(first, second string) bool {
	firstResolved, firstError := filepath.EvalSymlinks(first)
	secondResolved, secondError := filepath.EvalSymlinks(second)
	return firstError == nil && secondError == nil && firstResolved == secondResolved
}
