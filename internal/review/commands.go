package review

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zima-oss/jackstay/internal/chatevent"
	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/project"
)

func init() {
	cli.Register(
		cli.Command{Name: "review", Summary: "[REV | BASE..REV]: a cold review in rounds (docs/design/review.md)", Usage: reviewHelp, Run: runReview},
		cli.Command{Name: "review last", Summary: "[--task NAME] [--json]: the newest round's report, or the newest attempt's failure", Usage: lastHelp, Run: runLast},
		cli.Command{Name: "review check", Summary: "[--task NAME] BASE..REV: is this change reviewed and clear?", Usage: checkHelp, Run: runCheck},
		cli.Command{Name: "review list", Summary: "[--task NAME] [--rebuild] [--json]: the reviews kept", Usage: listHelp, Run: runList},
		cli.Command{Name: "review show", Summary: "TASK ROUND [--json]: one round's report", Usage: showHelp, Run: runShow},
		cli.Command{Name: "review prune", Summary: "remove tasks past review.keep_days, now", Usage: pruneHelp, Run: runPrune},
	)
}

func flagSet(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

// parseInterspersed parses flags anywhere among the words; everything after `--` is positional.
func parseInterspersed(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for len(rest) > 0 {
		if rest[0] == "--" {
			return append(positional, rest[1:]...), nil
		}
		if err := flags.Parse(rest); err != nil {
			return nil, err
		}
		rest = flags.Args()
		if len(rest) > 0 && rest[0] != "--" {
			positional = append(positional, rest[0])
			rest = rest[1:]
		}
	}
	return positional, nil
}

// opened is the project around the working folder with its settings and store.
type opened struct {
	project  project.Project
	settings Settings
	store    Store
}

func openProject(stderr io.Writer, name string) (opened, bool) {
	found, err := project.Here([]string{"review"})
	if err != nil {
		fmt.Fprintf(stderr, "jst %s: %v\n", name, err)
		return opened{}, false
	}
	settings, problems := SettingsOf(found.Config)
	if len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintf(stderr, "jst %s: %s\n", name, problem)
		}
		return opened{}, false
	}
	store, err := StoreFor(settings, found.Name)
	if err != nil {
		fmt.Fprintf(stderr, "jst %s: %v\n", name, err)
		return opened{}, false
	}
	return opened{project: found, settings: settings, store: store}, true
}

func runReview(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flagSet("jst review", stderr)
	var options Options
	reviewers := ""
	asJSON := false
	flags.StringVar(&options.Task, "task", "", "the task the rounds belong to (default: the branch)")
	flags.StringVar(&reviewers, "reviewer", "", "reviewers, comma-separated (default: review.use, else codex)")
	flags.StringVar(&options.BriefFile, "brief", "", "a brief file added to the prompt")
	flags.StringVar(&options.Focus, "focus", "", "where to look hardest (never narrows the round)")
	flags.BoolVar(&options.Fresh, "fresh", false, "start a new chain: a full review from BASE")
	flags.BoolVar(&options.PastLimit, "past-limit", false, "a round past review.round_limit, after the owner's word")
	flags.StringVar(&options.Model, "model", "", "the model, for this call")
	flags.StringVar(&options.Effort, "effort", "", "the reasoning effort, for this call")
	flags.IntVar(&options.Timeout, "timeout", 0, "minutes per run, for this call")
	flags.BoolVar(&asJSON, "json", false, "the result as one JSON object")
	background := false
	request := ""
	flags.BoolVar(&background, "background", false, "start the round under `jst bg` and return; the result comes as a chat event")
	flags.StringVar(&request, "request", "", "internal: the background request this run serves")
	positional, err := parseInterspersed(flags, args)
	if err != nil {
		return ExitUsage
	}
	if len(positional) > 1 {
		fmt.Fprintln(stderr, "usage: jst review [REV | BASE..REV] [flags]")
		return ExitUsage
	}
	if len(positional) == 1 {
		options.Revision = positional[0]
	}
	given := false
	flags.Visit(func(flag *flag.Flag) { given = given || flag.Name == "reviewer" })
	if given && strings.TrimSpace(reviewers) == "" {
		fmt.Fprintln(stderr, "jst review: --reviewer names no reviewer: give codex, claude-code or both, or leave the flag out for the default")
		return ExitUsage
	}
	if reviewers != "" {
		options.Reviewers = strings.Split(reviewers, ",")
	}
	found, ok := openProject(stderr, "review")
	if !ok {
		return ExitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if background {
		if request != "" || asJSON {
			fmt.Fprintln(stderr, "jst review: --background takes neither --request nor --json")
			return ExitUsage
		}
		return submitBackground(ctx, found, options, stdout, stderr)
	}
	machine, err := RealMachine(homedir.SharedPath)
	if err != nil {
		fmt.Fprintf(stderr, "jst review: %v\n", err)
		return ExitUsage
	}
	summary := stdout
	if asJSON {
		summary = stderr
	}
	if request != "" && options.Task == "" {
		fmt.Fprintln(stderr, "jst review: --request needs --task")
		return ExitUsage
	}
	result := Round{RepositoryRoot: found.project.Root, Settings: found.settings, Repo: RepositoryAt(found.project.Root), Store: found.store,
		Machine: machine, Out: summary, Errors: stderr, Request: request}.Run(ctx, options)
	if request != "" {
		// The result is told even when the call was stopped: a fresh, bounded context.
		finishing, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := found.store.Finish(finishing, options.Task, request, result, chatevent.Default()); err != nil {
			fmt.Fprintf(stderr, "jst review: the result is kept for the chat, not yet told: %v\n", err)
		}
		cancel()
	}
	if asJSON {
		writeResultJSON(stdout, result)
		return result.Status
	}
	outcome := OutcomeWord(result.Status)
	if result.Record != nil {
		fmt.Fprintf(stdout, "jst review: round %d of %s: %s\n", result.Record.Round, result.Record.Task, outcome)
	} else {
		fmt.Fprintf(stdout, "jst review: %s\n", outcome)
	}
	if result.Folder != "" {
		fmt.Fprintf(stdout, "jst review: report %s\n", filepath.Join(result.Folder, "report.md"))
	}
	fmt.Fprintf(stdout, "jst review: next: %s\n", result.Next)
	return result.Status
}

func submitBackground(ctx context.Context, found opened, options Options, stdout, stderr io.Writer) int {
	if options.BriefFile != "" || options.Model != "" || options.Effort != "" || options.Timeout != 0 {
		fmt.Fprintln(stderr, "jst review: --brief, --model, --effort and --timeout are for a review in the foreground; one in the background uses the configuration")
		return ExitUsage
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "jst review: %v\n", err)
		return ExitUsage
	}
	subscriber, err := chatevent.Identify(ctx, found.project.Root)
	if err != nil {
		fmt.Fprintf(stderr, "jst review: no chat event will tell this session the result: %v\n", err)
	}
	background, err := BackgroundAt(found.project.Root, executable)
	if err != nil {
		fmt.Fprintf(stderr, "jst review: %v\n", err)
		return ExitUsage
	}
	submitted, err := background.Submit(ctx, Submission{
		Revision: options.Revision, Task: options.Task, Reviewers: options.Reviewers, Fresh: options.Fresh,
		PastLimit: options.PastLimit, Focus: options.Focus, Subscriber: subscriber})
	if err != nil {
		fmt.Fprintf(stderr, "jst review: %v\n", err)
		return ExitUsage
	}
	request := submitted.Request
	word := "started"
	if submitted.Joined {
		word = "joined the running review"
	}
	fmt.Fprintf(stdout, "jst review: %s in the background: %s of task %s (request %s, `jst procs` lists it as review-%s)\n",
		word, request.rangeText(), request.Task, request.ID, request.Task)
	fmt.Fprintf(stdout, "jst review: log %s\n", request.Log)
	if subscriber != "" {
		fmt.Fprintln(stdout, "jst review: next: the result comes to this session as a chat event (review.finished)")
	} else {
		fmt.Fprintf(stdout, "jst review: next: `jst review last --task %s` once `jst procs` no longer lists it\n", request.Task)
	}
	return 0
}

func writeResultJSON(stdout io.Writer, result Result) {
	value := map[string]any{"status": result.Status, "folder": result.Folder, "next": result.Next}
	if result.Record != nil {
		value["round"] = result.Record
	} else if result.Folder != "" {
		value["runs"] = ReadRuns(result.Folder)
	}
	data, _ := json.MarshalIndent(value, "", "  ")
	fmt.Fprintln(stdout, string(data))
}

func taskOf(found opened, task string, stderr io.Writer, name string) (string, bool) {
	if task != "" {
		return task, true
	}
	branch, err := RepositoryAt(found.project.Root).Branch(context.Background())
	if err != nil || branch == "" {
		fmt.Fprintf(stderr, "jst %s: no branch is checked out: name the task with --task\n", name)
		return "", false
	}
	return branch, true
}

func runLast(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flagSet("jst review last", stderr)
	task := flags.String("task", "", "the task (default: the branch)")
	asJSON := flags.Bool("json", false, "as JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		return ExitUsage
	}
	found, ok := openProject(stderr, "review last")
	if !ok {
		return ExitUsage
	}
	name, ok := taskOf(found, *task, stderr, "review last")
	if !ok {
		return ExitUsage
	}
	entries, err := found.store.Entries(name)
	if err != nil || len(entries) == 0 {
		fmt.Fprintf(stderr, "jst review last: task %q has no review\n", name)
		return ExitUsage
	}
	newest := entries[len(entries)-1]
	if *asJSON {
		if newest.Round > 0 {
			record, err := readRecord(newest.Path)
			if err != nil {
				fmt.Fprintf(stderr, "jst review last: %v\n", err)
				return ExitUsage
			}
			writeResultJSON(stdout, Result{Folder: newest.Path, Record: &record})
		} else {
			writeResultJSON(stdout, Result{Status: ExitReviewer, Folder: newest.Path})
		}
		return 0
	}
	return printFile(filepath.Join(newest.Path, "report.md"), stdout, stderr)
}

func printFile(path string, stdout, stderr io.Writer) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "jst review: %v\n", err)
		return ExitUsage
	}
	_, _ = stdout.Write(data)
	return 0
}

// CheckReasons are why a change is not reviewed and clear (none: it is), and notes (section 7).
func CheckReasons(settings Settings, store Store, task, base, commit string) ([]string, []string) {
	entries, err := store.Entries(task)
	if err != nil {
		return []string{err.Error()}, nil
	}
	rounds := Rounds(entries)
	if len(rounds) == 0 {
		return []string{fmt.Sprintf("task %q has no round", task)}, nil
	}
	var notes []string
	if newest := entries[len(entries)-1]; newest.Round == 0 {
		notes = append(notes, fmt.Sprintf("a newer attempt (%d) did not become a round", newest.Attempt))
	}
	record, err := readRecord(rounds[len(rounds)-1].Path)
	if err != nil {
		return []string{err.Error()}, notes
	}
	var reasons []string
	if record.Commit != commit {
		reasons = append(reasons, fmt.Sprintf("the newest round (%d) was made at %s, not at %s", record.Round, short(record.Commit), short(commit)))
	}
	if record.Base != base {
		reasons = append(reasons, fmt.Sprintf("its chain reviews from %s, not from %s", short(record.Base), short(base)))
	}
	if record.Policy == "" {
		reasons = append(reasons, "the newest round has no recorded review policy; run a full review with --fresh")
	} else if record.Policy != settings.policyID() {
		reasons = append(reasons, "the review policy changed since the newest round; run a full review with --fresh")
	}
	completed := completedReviewers(record.Runs)
	if len(completed) == 0 {
		reasons = append(reasons, "the newest round has no completed reviewer")
	}
	for _, name := range settings.Use {
		if !completed[name] {
			reasons = append(reasons, fmt.Sprintf("required reviewer %q did not complete the newest round", name))
		}
	}
	for _, finding := range Open(record.Outcome.Ledger) {
		if settings.Blocks(finding.Severity, finding.Likelihood, finding.Consequence) {
			reasons = append(reasons, fmt.Sprintf("%s blocks: %s", finding.ID, finding.Title))
		}
	}
	if len(record.Outcome.RedesignDue) > 0 {
		reasons = append(reasons, "redesign due (W-019): "+strings.Join(record.Outcome.RedesignDue, "; "))
	}
	if record.OverLimit {
		reasons = append(reasons, fmt.Sprintf("past the round limit of %d without --past-limit", settings.RoundLimit))
	}
	return reasons, notes
}

func runCheck(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flagSet("jst review check", stderr)
	task := flags.String("task", "", "the task (default: the branch)")
	positional, err := parseInterspersed(flags, args)
	if err != nil {
		return ExitUsage
	}
	if len(positional) != 1 || !strings.Contains(positional[0], "..") {
		fmt.Fprintln(stderr, "usage: jst review check [--task NAME] BASE..REV")
		return ExitUsage
	}
	found, ok := openProject(stderr, "review check")
	if !ok {
		return ExitUsage
	}
	name, ok := taskOf(found, *task, stderr, "review check")
	if !ok {
		return ExitUsage
	}
	repo := RepositoryAt(found.project.Root)
	baseName, revisionName, _ := strings.Cut(positional[0], "..")
	ctx := context.Background()
	base, baseErr := repo.Resolve(ctx, baseName)
	commit, commitErr := repo.Resolve(ctx, revisionName)
	if baseErr != nil || commitErr != nil {
		fmt.Fprintf(stderr, "jst review check: %v\n", firstError(baseErr, commitErr))
		return ExitUsage
	}
	reasons, notes := CheckReasons(found.settings, found.store, name, base, commit)
	for _, note := range notes {
		fmt.Fprintf(stdout, "jst review check: note: %s\n", note)
	}
	if len(reasons) == 0 {
		fmt.Fprintf(stdout, "jst review check: %s..%s is reviewed and clear (task %s)\n", short(base), short(commit), name)
		return 0
	}
	for _, reason := range reasons {
		fmt.Fprintf(stdout, "jst review check: not clear: %s\n", reason)
	}
	return 1
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func runList(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flagSet("jst review list", stderr)
	task := flags.String("task", "", "only this task")
	rebuild := flags.Bool("rebuild", false, "write the index anew from the folders first")
	asJSON := flags.Bool("json", false, "as JSON lines")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		return ExitUsage
	}
	found, ok := openProject(stderr, "review list")
	if !ok {
		return ExitUsage
	}
	if *rebuild {
		if err := found.store.Rebuild(); err != nil {
			fmt.Fprintf(stderr, "jst review list: %v\n", err)
			return ExitUsage
		}
	}
	lines, skipped, err := found.store.ReadIndex()
	if err != nil {
		fmt.Fprintf(stderr, "jst review list: %v\n", err)
		return ExitUsage
	}
	if skipped > 0 {
		fmt.Fprintf(stderr, "jst review list: %d line(s) of the index did not read; `--rebuild` writes it anew\n", skipped)
	}
	for _, line := range lines {
		if *task != "" && line.Task != *task {
			continue
		}
		if *asJSON {
			data, _ := json.Marshal(line)
			fmt.Fprintln(stdout, string(data))
			continue
		}
		round := "-"
		if line.Round != nil {
			round = strconv.Itoa(*line.Round)
		}
		reused := ""
		if line.Reused != "" {
			reused = " reused from " + line.Reused
		}
		fmt.Fprintf(stdout, "%s  %-30s round %-3s attempt %-3d %-8s %-12s blocks=%t realistic=%d rare=%d %.0fs %s%s\n",
			line.At, line.Task, round, line.Attempt, line.Reviewer, line.Outcome, line.Blocks, line.Realistic, line.Rare,
			line.Seconds, short(line.Commit), reused)
	}
	return 0
}

func runShow(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flagSet("jst review show", stderr)
	asJSON := flags.Bool("json", false, "the round's record")
	positional, err := parseInterspersed(flags, args)
	if err != nil {
		return ExitUsage
	}
	if len(positional) != 2 {
		fmt.Fprintln(stderr, "usage: jst review show TASK ROUND [--json]")
		return ExitUsage
	}
	number, err := strconv.Atoi(positional[1])
	if err != nil || number < 1 {
		fmt.Fprintln(stderr, "jst review show: ROUND is a number from 1")
		return ExitUsage
	}
	found, ok := openProject(stderr, "review show")
	if !ok {
		return ExitUsage
	}
	folder := filepath.Join(found.store.TaskPath(positional[0]), fmt.Sprintf("round-%d", number))
	if *asJSON {
		return printFile(filepath.Join(folder, RecordName), stdout, stderr)
	}
	return printFile(filepath.Join(folder, "report.md"), stdout, stderr)
}

func runPrune(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: jst review prune")
		return ExitUsage
	}
	found, ok := openProject(stderr, "review prune")
	if !ok {
		return ExitUsage
	}
	removed, err := found.store.Prune(found.settings.KeepDays, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "jst review prune: %v\n", err)
		return ExitUsage
	}
	fmt.Fprintf(stdout, "jst review prune: removed %d task(s) older than %d days\n", len(removed), found.settings.KeepDays)
	return 0
}
