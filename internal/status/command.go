package status

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/config"
)

const usage = `usage: jst status [--json] [--since DURATION] [--limit N] [REPO...]

What is going on locally, read without writing anything and without contacting a remote service.

With no REPO it covers this repository, in detail: its worktrees, the agent sessions and delivery
events (pull requests, tests, reviews, background jobs) the journal recorded in the window, chat
counts, the brief, background jobs, the last gate and stored receipt (and whether they are for
HEAD) and active cards. When this project's jackstay.json lists repositories in status.repos, no
REPO means those instead.

With one REPO it is that repository in detail; with several, one row per repository (at most 32).
No message, receipt, card or brief text is shown, and events carry no arguments or prompts.

  --json            one versioned JSON envelope (schema, kind "repository" or "board", the
                    window, partial, and the repositories)
  --since DURATION  the window for sessions and events, back from now (default 24h, maximum 720h)
  --limit N         keep at most the N newest events (default 20, maximum 1000)

exit: 0 every source read; 1 at least one source unknown or partial (the report still printed); 2 the
request is wrong (an option, a path, more than 32 repositories, or an unusable status.repos).

example: jst status --since 4h
         jst status --json ~/src/app ~/src/service
`

const (
	defaultWindow   = 24 * time.Hour
	maximumWindow   = 30 * 24 * time.Hour
	defaultLimit    = 20
	maximumLimit    = 1000
	maximumRequests = 32
)

func init() {
	cli.Register(cli.Command{Name: "status", Summary: "this repository's sessions, events, chat, brief and jobs; or one row per repository",
		Usage: usage, Run: run, PrivateArgs: true, SkipTelemetry: true})
}

// request is one call: the repositories, whether in detail, the window and the format.
type request struct {
	paths  []string
	detail bool
	asked  window
	asJSON bool
}

func run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	now := time.Now()
	asked, err := parse(args, now)
	if err != nil {
		fmt.Fprintf(stderr, "jst status: %v\n\n%s", err, usage)
		return 2
	}
	report := Read(context.Background(), asked.paths, asked.asked, asked.detail, now)
	if asked.asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintln(stderr, "jst status:", err)
			return 2
		}
	} else {
		writeText(stdout, report, now)
	}
	if report.Partial {
		fmt.Fprintln(stderr, "jst status: some sources could not be read whole; see their errors")
		return 1
	}
	return 0
}

func parse(args []string, now time.Time) (request, error) {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "")
	since := flags.Duration("since", defaultWindow, "")
	limit := flags.Int("limit", defaultLimit, "")
	if err := flags.Parse(args); err != nil {
		return request{}, err
	}
	if *since <= 0 || *since > maximumWindow {
		return request{}, fmt.Errorf("--since must be more than 0 and at most %s", maximumWindow)
	}
	if *limit < 1 || *limit > maximumLimit {
		return request{}, fmt.Errorf("--limit must be 1 to %d", maximumLimit)
	}
	paths := flags.Args()
	for _, path := range paths {
		if strings.TrimSpace(path) == "" || strings.HasPrefix(path, "-") {
			return request{}, fmt.Errorf("not a repository path: %q (options go before the paths)", path)
		}
	}
	detail := len(paths) == 1
	if len(paths) == 0 {
		working, err := os.Getwd()
		if err != nil {
			return request{}, err
		}
		root, loaded, err := config.Load(working, []string{"status"})
		if err != nil {
			return request{}, fmt.Errorf("status.repos cannot be read: %w", err)
		}
		paths = config.StatusRepositories(loaded.Section("status"), root)
		if len(paths) == 0 {
			paths, detail = []string{working}, true
		}
	}
	if len(paths) > maximumRequests {
		return request{}, fmt.Errorf("at most %d repositories in one call", maximumRequests)
	}
	asked := window{from: now.Add(-*since).UTC(), until: now.UTC(), limit: *limit}
	return request{paths: paths, detail: detail, asked: asked, asJSON: *asJSON}, nil
}
