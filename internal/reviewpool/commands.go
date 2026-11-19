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
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/flock"
	"github.com/zima-oss/jackstay/internal/review"
	"github.com/zima-oss/jackstay/internal/supervise"
)

const poolHelp = `usage: jst review pool add --repo ABSOLUTE_PATH --base REF --head REF --task NAME --reviewer NAMES
       jst review pool list [--json]
       jst review pool inspect [--json]
       jst review pool prune [--apply] [--json]
       jst review pool migrate [--apply] [--json]
       jst review pool subscribers JOB_ID [--json]
       jst review pool unsubscribe JOB_ID SESSION_ID --generation TOKEN [--apply] [--json]
       jst review pool next
       jst review pool retry JOB_ID

A local, persistent, opt-in queue shared by agent sessions. add pins both Git revisions now;
NAMES is codex, claude-code, or both separated by a comma. next runs one queued review in
the foreground from its saved repository. It starts no background daemon. retry requeues a
failed attempt without changing its commits. list reads local jobs; inspect reports queue
health and notification handoffs without repository or session details. prune previews
expired terminal history; migrate previews a queue schema upgrade. Both require --apply
to write. prune retains active work and terminal outcomes awaiting chat delivery.
subscribers shows recipient IDs and the job generation. unsubscribe previews closing
one subscription, and requires --apply and that generation to make the change.

exit: 0 request or maintenance succeeded, or no pending job; 1, 3, 4 a completed review verdict;
2 invalid request or queue state; 5, 6 review failed; 2 when another worker holds the pool.

example: jst review pool add --repo /path/to/repo --base main --head HEAD --task parser --reviewer codex
`

func init() {
	cli.Register(cli.Command{Name: "review pool", Summary: "queue, inspect and maintain local reviews across sessions",
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
			failure := ""
			if job.Status == "failed" && job.FailureClass != "" || job.FailureClass == "unresolved" {
				failure = "  reason=" + job.FailureClass
			}
			fmt.Fprintf(stdout, "%s  %-8s  attempts=%d  exit=%d  %s  %s%s\n", job.ID, job.Status, job.Attempts, job.Exit, job.Task, job.ReviewersText(), failure)
		}
		return 0
	case "inspect", "prune", "migrate":
		return runMaintenance(ctx, pool, args, stdout, stderr)
	case "subscribers", "unsubscribe":
		return runSubscriptionCommand(ctx, pool, args, stdout, stderr)
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
			environment := slices.DeleteFunc(os.Environ(), func(pair string) bool {
				return strings.HasPrefix(pair, "JACKSTAY_REVIEW_POOL_ATTEMPT=") ||
					strings.HasPrefix(pair, review.PoolPolicyIDVariable+"=")
			})
			environment = append(environment, "JACKSTAY_REVIEW_POOL_ATTEMPT="+job.AttemptID, review.PoolPolicyIDVariable+"="+job.PolicyID)
			captured, err := supervise.Capture(ctx, []string{executable, "review", job.Base + ".." + job.Head,
				"--task", job.Task, "--reviewer", job.ReviewersText()}, job.Repository, environment, nil)
			_, _ = stdout.Write(captured.Stdout)
			_, _ = stderr.Write(captured.Stderr)
			if captured.Incomplete != "" {
				return review.ExitReviewer, fmt.Errorf("review output incomplete: %s", captured.Incomplete)
			}
			if err == nil && captured.Status == review.ExitUsage &&
				strings.TrimSpace(string(captured.Stderr)) == review.PoolPolicyStaleMarker {
				return review.ExitUsage, review.ErrPoolPolicyStale
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

func runMaintenance(ctx context.Context, pool Pool, args []string, stdout, stderr io.Writer) int {
	command := args[0]
	flags := flag.NewFlagSet("jst review pool "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var asJSON, apply bool
	flags.BoolVar(&asJSON, "json", false, "write a JSON report")
	if command != "inspect" {
		flags.BoolVar(&apply, "apply", false, "write the proposed changes")
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		fmt.Fprintf(stderr, "jst review pool %s: invalid arguments\n", command)
		return 2
	}

	var report any
	var err error
	switch command {
	case "inspect":
		report, err = pool.Inspect(ctx, time.Now())
	case "prune":
		report, err = pool.Prune(ctx, time.Now(), apply)
	case "migrate":
		report, err = pool.Migrate(ctx, apply)
	}
	return writePoolReport(command, report, err, asJSON, stdout, stderr)
}

func runSubscriptionCommand(ctx context.Context, pool Pool, args []string, stdout, stderr io.Writer) int {
	command := args[0]
	required := 2
	if command == "unsubscribe" {
		required = 3
	}
	if len(args) < required {
		fmt.Fprintf(stderr, "jst review pool %s: missing required IDs\n", command)
		return 2
	}
	flags := flag.NewFlagSet("jst review pool "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	asJSON := flags.Bool("json", false, "write a JSON report")
	var apply bool
	var generation string
	if command == "unsubscribe" {
		flags.BoolVar(&apply, "apply", false, "close the subscription")
		flags.StringVar(&generation, "generation", "", "generation shown by subscribers")
	}
	if err := flags.Parse(args[required:]); err != nil || flags.NArg() != 0 {
		fmt.Fprintf(stderr, "jst review pool %s: invalid arguments\n", command)
		return 2
	}
	if command == "subscribers" {
		report, err := pool.ListSubscriptions(ctx, args[1])
		return writePoolReport(command, report, err, *asJSON, stdout, stderr)
	}
	report, err := pool.CloseSubscription(ctx, args[1], args[2], generation, apply)
	return writePoolReport(command, report, err, *asJSON, stdout, stderr)
}

func writePoolReport(command string, report any, err error, asJSON bool, stdout, stderr io.Writer) int {
	if err == nil {
		if asJSON {
			err = json.NewEncoder(stdout).Encode(report)
		} else {
			err = printMaintenance(stdout, report)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "jst review pool %s: %v\n", command, err)
		return 2
	}
	return 0
}

func printMaintenance(output io.Writer, report any) error {
	switch result := report.(type) {
	case Overview:
		_, err := fmt.Fprintf(output, "Review pool state v%d\n"+
			"Jobs: %d total, %d pending (%d eligible, %d delayed), %d running (%d recoverable, %d unresolved), %d complete, %d failed\n"+
			"Work eligible: %t; oldest pending age: %ds\n"+
			"Outcomes: %d clear, %d blocked, %d redesign, %d owner, %d other, %d retryable failures, %d operator failures\n"+
			"Notifications: %d awaiting chat delivery, %d delivered to chat\n",
			result.Version, result.Total, result.Pending, result.Eligible, result.Delayed, result.Running, result.Recoverable,
			result.Unresolved, result.Complete, result.Failed, result.Ready, result.OldestPendingAgeSeconds,
			result.Outcomes.Clear, result.Outcomes.Blocks, result.Outcomes.Redesign, result.Outcomes.Owner, result.Outcomes.Other,
			result.Outcomes.Retryable, result.Outcomes.Operator, result.Notifications.Undelivered, result.Notifications.Materialized)
		return err
	case PruneReport:
		_, err := fmt.Fprintf(output, "Review pool history: %d eligible, %d protected by pending delivery, %d removed\n",
			result.Eligible, result.Protected, result.Removed)
		return err
	case Migration:
		_, err := fmt.Fprintf(output, "Review pool migration: v%d to v%d, needed: %t, applied: %t\n",
			result.From, result.To, result.Needed, result.Applied)
		return err
	case SubscriptionOverview:
		if _, err := fmt.Fprintf(output, "Review job %s (%s), generation %s\n", result.JobID, result.Status, result.Generation); err != nil {
			return err
		}
		for _, subscriber := range result.Subscribers {
			if _, err := fmt.Fprintf(output, "%s  closed=%t  undelivered=%d  delivered=%d\n", subscriber.SessionID,
				subscriber.Closed, subscriber.Undelivered, subscriber.Materialized); err != nil {
				return err
			}
		}
		return nil
	case SubscriptionClosure:
		_, err := fmt.Fprintf(output, "Review subscription %s / %s, generation %s: %d awaiting delivery, %d delivered, already closed: %t, applied: %t\n",
			result.JobID, result.SessionID, result.Generation, result.Undelivered, result.Materialized, result.AlreadyClosed, result.Applied)
		return err
	default:
		return errors.New("unknown review pool report")
	}
}
