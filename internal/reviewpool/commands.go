package reviewpool

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/supervise"
)

const poolHelp = `usage: jst review pool add --repo ABSOLUTE_PATH --base REF --head REF --task NAME --reviewer NAMES
       jst review pool list [--json]
       jst review pool next
       jst review pool retry JOB_ID

A local, persistent, opt-in queue shared by agent sessions. add pins both Git revisions now;
NAMES is codex, claude-code, or both separated by a comma. next runs one queued review in
the foreground from its saved repository. It starts no background daemon. retry requeues a
failed attempt without changing its commits. list reads local state only.

exit: 0 accepted, listed, retried, or no pending job; 1, 3, 4 a completed review verdict;
2 invalid request or queue state; 5, 6 review failed; 2 when another worker holds the pool.

example: jst review pool add --repo /path/to/repo --base main --head HEAD --task parser --reviewer codex
`

func init() {
	cli.Register(cli.Command{Name: "review pool", Summary: "add|list|next|retry: queue local reviews across sessions",
		Usage: poolHelp, Run: runPool, PrivateArgs: true, SkipTelemetry: true})
}

func runPool(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, poolHelp)
		return 2
	}
	pool, err := Default()
	if err != nil {
		fmt.Fprintf(stderr, "jst review pool: %v\n", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "add":
		flags := flag.NewFlagSet("jst review pool add", flag.ContinueOnError)
		flags.SetOutput(stderr)
		var request Request
		var reviewers string
		flags.StringVar(&request.Repository, "repo", "", "absolute local repository path")
		flags.StringVar(&request.Base, "base", "", "base commit or ref")
		flags.StringVar(&request.Head, "head", "", "head commit or ref")
		flags.StringVar(&request.Task, "task", "", "review task")
		flags.StringVar(&reviewers, "reviewer", "", "codex, claude-code, or both")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			fmt.Fprintln(stderr, "usage: jst review pool add --repo PATH --base REF --head REF --task NAME --reviewer NAMES")
			return 2
		}
		if reviewers != "" {
			request.Reviewers = strings.Split(reviewers, ",")
		}
		job, created, err := pool.Enqueue(ctx, request)
		if err != nil {
			fmt.Fprintf(stderr, "jst review pool add: %v\n", err)
			return 2
		}
		word := "already queued"
		if created {
			word = "queued"
		}
		fmt.Fprintf(stdout, "jst review pool: %s %s (%s)\n", word, job.ID, job.Status)
		return 0
	case "list":
		if len(args) > 2 || len(args) == 2 && args[1] != "--json" {
			fmt.Fprintln(stderr, "usage: jst review pool list [--json]")
			return 2
		}
		jobs, err := pool.List(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "jst review pool list: %v\n", err)
			return 2
		}
		if len(args) == 2 {
			if err := json.NewEncoder(stdout).Encode(jobs); err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
			return 0
		}
		for _, job := range jobs {
			fmt.Fprintf(stdout, "%s  %-8s  attempts=%d  exit=%d  %s  %s\n", job.ID, job.Status, job.Attempts, job.Exit, job.Task, job.ReviewersText())
		}
		return 0
	case "retry":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: jst review pool retry JOB_ID")
			return 2
		}
		if err := pool.Retry(ctx, args[1]); err != nil {
			fmt.Fprintf(stderr, "jst review pool retry: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "jst review pool: requeued %s\n", args[1])
		return 0
	case "next":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: jst review pool next")
			return 2
		}
		job, found, err := pool.RunNext(ctx, func(ctx context.Context, job Job) (int, error) {
			fmt.Fprintf(stdout, "jst review pool: running %s (%s..%s, %s)\n", job.ID, job.Base, job.Head, job.ReviewersText())
			executable, err := os.Executable()
			if err != nil {
				return review.ExitEnvironment, err
			}
			captured, err := supervise.Capture(ctx, []string{executable, "review", job.Base + ".." + job.Head,
				"--task", job.Task, "--reviewer", job.ReviewersText()}, job.Repository, nil, nil)
			_, _ = stdout.Write(captured.Stdout)
			_, _ = stderr.Write(captured.Stderr)
			if captured.Incomplete != "" {
				return review.ExitReviewer, fmt.Errorf("review output incomplete: %s", captured.Incomplete)
			}
			return captured.Status, err
		})
		if errors.Is(err, flock.ErrBusy) {
			fmt.Fprintln(stderr, "jst review pool: another worker is running")
			return 2
		}
		if err != nil {
			fmt.Fprintf(stderr, "jst review pool next: %v\n", err)
			return 2
		}
		if !found {
			fmt.Fprintln(stdout, "jst review pool: no pending jobs")
			return 0
		}
		fmt.Fprintf(stdout, "jst review pool: %s %s, review exit %d; inspect with jst review last --task %s\n", job.Status, job.ID, job.Exit, job.Task)
		return job.Exit
	default:
		fmt.Fprint(stderr, poolHelp)
		return 2
	}
}
