package workspaceboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
)

const usage = `usage: jst workspace board [--json] [REPO...]

Read a summary of each explicitly supplied Git checkout. Linked worktrees share chat
state; gate and receipt summaries are for the named checkout. No remote service is contacted.
No message bodies, receipt contents, or card bodies are printed.
At most 32 repositories may be supplied in one call. With none named, the repositories are the
current project's status.repos in jackstay.json (relative to its folder).

exit: 0 every requested source read; 1 at least one source is unknown or partial; 2 invalid use.

example: jst workspace board --json ~/src/app ~/src/service
`

func init() {
	cli.Register(cli.Command{Name: "workspace board", Summary: "read local status across named repositories", Usage: usage,
		Run: runBoard, PrivateArgs: true, SkipTelemetry: true})
}

func runBoard(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	paths := []string{}
	asJSON := false
	for _, word := range args {
		if word == "--json" {
			asJSON = true
			continue
		}
		if strings.HasPrefix(word, "-") || strings.TrimSpace(word) == "" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		paths = append(paths, word)
	}
	if len(paths) == 0 {
		working, err := os.Getwd()
		if err == nil {
			var root string
			var loaded config.Config
			root, loaded, err = config.Load(working, []string{"status"})
			paths = config.StatusRepositories(loaded.Section("status"), root)
		}
		if err != nil {
			fmt.Fprintf(stderr, "jst workspace board: %v\n", err)
			return 2
		}
	}
	if len(paths) == 0 || len(paths) > 32 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	board := Read(context.Background(), paths)
	if asJSON {
		encoded, err := json.MarshalIndent(board, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "jst workspace board: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(encoded))
	} else {
		for _, repository := range board.Repositories {
			fmt.Fprintf(stdout, "%s (%s): cards %s", repository.Path, repository.Project, repository.Cards.Status)
			if repository.Cards.Status == "ok" {
				fmt.Fprintf(stdout, " %d active", len(repository.Cards.Active))
			}
			fmt.Fprintf(stdout, "; gate %s", repository.Gate.Status)
			if repository.Gate.Status == "recorded" {
				fmt.Fprintf(stdout, " %s (%s)", repository.Gate.Outcome, repository.Gate.Freshness)
			}
			fmt.Fprintf(stdout, "; receipt %s", repository.Receipt.Status)
			if repository.Receipt.Status == "recorded" {
				fmt.Fprintf(stdout, " (%s)", repository.Receipt.Freshness)
			}
			fmt.Fprintf(stdout, "; chat %s", repository.Chat.Status)
			if repository.Chat.Status == "available" {
				fmt.Fprintf(stdout, " %d sessions, %d pending", repository.Chat.Sessions, repository.Chat.Pending)
			}
			fmt.Fprintln(stdout)
			for _, source := range []struct{ label, errorText string }{{"repository", repository.Error}, {"cards", repository.Cards.Error},
				{"gate", repository.Gate.Error}, {"receipt", repository.Receipt.Error}, {"chat", repository.Chat.Error}} {
				if source.errorText != "" {
					fmt.Fprintf(stdout, "  %s: %s\n", source.label, source.errorText)
				}
			}
		}
	}
	if board.Partial {
		return 1
	}
	return 0
}
