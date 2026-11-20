// Package logs is `jst log` (docs/design/observability.md, section 12): what jackstay did, read from its
// journal — a timeline of commands, a run's tree, a Claude session, a tool call.
package logs

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/genai"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/telemetry/query"
)

func init() {
	cli.Register(cli.Command{Name: "log", Summary: "what jackstay did: commands, a run's tree, a Claude session, a tool call",
		Usage: help, Run: command})
}

// Options of `jst log`.
type Options struct {
	Since, Until   time.Time
	Scope          query.Scope
	Session, Trace string
	ToolCall       string
	Command        string
	Outcome        string
	Tree, JSON     bool
	Limit          int
	Now            time.Time
}

const usageText = `usage: jst log [--since DUR|TIME] [--until TIME] [--project P | --all-projects] [--session ID]
              [--trace ID] [--tool-call ID] [--command NAME] [--outcome WORD|error] [--tree] [--json] [--limit N]`

// toolCallWindow is how soon after the guard's answer a command counts as the call's (a join by time).
const toolCallWindow = 30 * time.Second

func command(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	options, err := parse(args, time.Now())
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, usageText)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "jst log:", err)
		fmt.Fprintln(stderr, usageText)
		return 2
	}
	folders, err := query.Folders(options.Scope)
	if err != nil {
		fmt.Fprintln(stderr, "jst log:", err)
		return 1
	}
	spans, result := query.Load(folders, options.Since, options.Until, options.Now)
	if result.Unreadable > 0 {
		fmt.Fprintf(stderr, "jst log: %d lines of the journal could not be read\n", result.Unreadable)
	}
	for _, problem := range result.Problems {
		fmt.Fprintln(stderr, "jst log: cannot read", problem)
	}
	if result.Folders == 0 && len(result.Problems) > 0 {
		return 1
	}
	return Print(stdout, spans, options)
}

func parse(args []string, now time.Time) (Options, error) {
	flags := flag.NewFlagSet("jst log", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	since := flags.String("since", "", "")
	until := flags.String("until", "", "")
	options := Options{Now: now}
	flags.StringVar(&options.Scope.Project, "project", "", "")
	flags.BoolVar(&options.Scope.All, "all-projects", false, "")
	flags.StringVar(&options.Session, "session", "", "")
	flags.StringVar(&options.Trace, "trace", "", "")
	flags.StringVar(&options.ToolCall, "tool-call", "", "")
	flags.StringVar(&options.Command, "command", "", "")
	flags.StringVar(&options.Outcome, "outcome", "", "")
	flags.BoolVar(&options.Tree, "tree", false, "")
	flags.BoolVar(&options.JSON, "json", false, "")
	flags.IntVar(&options.Limit, "limit", 0, "")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() > 0 {
		return options, fmt.Errorf("unexpected %q", flags.Arg(0))
	}
	wide := options.Session != "" || options.Trace != "" || options.ToolCall != ""
	defaultSince := "24h"
	if wide {
		// A session, a run or a call crosses projects and may be older than a day.
		options.Scope.All = true
		defaultSince = "30d"
	}
	if *since == "" {
		*since = defaultSince
	}
	var err error
	if options.Since, err = query.ParseTime(*since, now); err != nil {
		return options, err
	}
	options.Until = now.Add(time.Minute)
	if *until != "" {
		if options.Until, err = query.ParseTime(*until, now); err != nil {
			return options, err
		}
	}
	if options.Trace != "" {
		options.Tree = true
	}
	return options, nil
}

// Print writes what options select of spans; its result is the exit status.
func Print(out io.Writer, spans []journal.Span, options Options) int {
	if options.ToolCall != "" {
		answer := FindToolCall(spans, options.ToolCall)
		if !options.JSON {
			printToolCall(out, answer, options.ToolCall)
			return 0
		}
		spans = append(append(append([]journal.Span{}, answer.Decisions...), answer.Exact...), answer.Candidates...)
		journal.SortByStart(spans)
	}
	selected := Select(spans, options)
	if options.JSON {
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(false)
		for _, span := range selected {
			_ = encoder.Encode(span)
		}
		return 0
	}
	if options.Tree {
		for _, root := range query.Trees(selected) {
			printNode(out, root, 0)
		}
		return 0
	}
	for _, span := range selected {
		if query.IsCommand(span) {
			fmt.Fprintln(out, commandLine(span))
		}
	}
	return 0
}

// Select is the spans of the options: whole traces when a filter names a command, a session or a
// trace (a tree needs its spans), in order of start.
func Select(spans []journal.Span, options Options) []journal.Span {
	match := func(span journal.Span) bool {
		if options.Trace != "" && !strings.HasPrefix(span.TraceID, options.Trace) {
			return false
		}
		if options.Session != "" && genai.SessionOf(span) != options.Session {
			return false
		}
		if options.Command != "" && span.Text(telemetry.AttrCommand) != options.Command {
			return false
		}
		switch options.Outcome {
		case "":
		case "error":
			if !query.IsError(span) {
				return false
			}
		default:
			if query.Outcome(span) != options.Outcome && span.Text(telemetry.AttrGuardOutcome) != options.Outcome {
				return false
			}
		}
		return true
	}
	traces := map[string]bool{}
	for _, span := range spans {
		if match(span) {
			traces[span.TraceID] = true
		}
	}
	var selected []journal.Span
	for _, span := range spans {
		keep := match(span)
		if options.Tree && traces[span.TraceID] && options.Outcome == "" && options.Command == "" {
			keep = true
		}
		if keep {
			selected = append(selected, span)
		}
	}
	if options.Limit > 0 && len(selected) > options.Limit {
		selected = selected[len(selected)-options.Limit:]
	}
	return selected
}

func commandLine(span journal.Span) string {
	project := query.Project(span)
	if project == "" {
		project = "-"
	}
	outcome := query.Outcome(span)
	if guard := span.Text(telemetry.AttrGuardOutcome); guard != "" {
		outcome = guard
	}
	if outcome == "" {
		outcome = "?"
	}
	words := span.Name
	if args := span.Texts(telemetry.AttrArgs); len(args) > 2 {
		words = strings.Join(args, " ")
	}
	if guarded := span.Text(telemetry.AttrGuardCommand); guarded != "" {
		words += ": " + guarded
	}
	session := genai.SessionOf(span)
	if len(session) > 8 {
		session = session[:8]
	}
	return fmt.Sprintf("%s  %-12s %-10s %8s  %-8s %s", span.Start.Format("2006-01-02 15:04:05"), project, outcome,
		duration(span.Duration()), session, words)
}

func duration(spent time.Duration) string {
	switch {
	case spent < time.Second:
		return fmt.Sprintf("%dms", spent.Milliseconds())
	case spent < time.Minute:
		return fmt.Sprintf("%.1fs", spent.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(spent.Minutes()), int(spent.Seconds())%60)
}

func printNode(out io.Writer, node *query.Node, depth int) {
	span := node.Span
	indent := strings.Repeat("  ", depth)
	outcome := query.Outcome(span)
	if query.IsError(span) {
		outcome += " ERROR " + span.Text(telemetry.AttrErrorType)
	}
	orphan := ""
	if node.Orphan {
		orphan = " (parent not recorded)"
	}
	head := span.Name
	if argv := span.Texts(telemetry.AttrChildArgv); len(argv) > 0 && strings.HasPrefix(span.Name, "exec ") {
		head += ": " + strings.Join(argv, " ")
	}
	if depth == 0 {
		head = fmt.Sprintf("%s [%s] %s", span.Start.Format("2006-01-02 15:04:05"), span.TraceID, head)
	}
	fmt.Fprintf(out, "%s%s  %s  %s%s\n", indent, head, duration(span.Duration()), strings.TrimSpace(outcome), orphan)
	for _, event := range span.Events {
		fmt.Fprintf(out, "%s  · %s%s\n", indent, event.Name, eventWords(event))
	}
	for _, child := range node.Children {
		printNode(out, child, depth+1)
	}
}

func eventWords(event journal.Event) string {
	if stack := event.Text(telemetry.AttrExceptionStack); stack != "" {
		lines := strings.Split(stack, "\n")
		return ": " + event.Text(telemetry.AttrExceptionMessage) + "\n      " + strings.Join(lines[:min(len(lines), 6)], "\n      ")
	}
	var parts []string
	for key, value := range event.Attributes {
		parts = append(parts, fmt.Sprintf("%s=%v", strings.TrimPrefix(key, "jackstay."), value))
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return " " + strings.Join(parts, " ")
}

// ToolCallAnswer is what `jst log --tool-call` finds: the guard's decisions on the call and the
// jackstay commands it ran. Exact are proven by the trace: a command span under the same Claude tool
// span as the guard's decision (Claude's beta tracing on). Candidates are only likely: the same
// session, started within toolCallWindow after the answer, naming the command the guard saw.
type ToolCallAnswer struct {
	Decisions  []journal.Span
	Exact      []journal.Span
	Candidates []journal.Span
}

// FindToolCall answers A13.
func FindToolCall(spans []journal.Span, callID string) ToolCallAnswer {
	answer := ToolCallAnswer{}
	for _, span := range spans {
		if call, ok := genai.ToolCallOf(span); ok && call.ID == callID {
			answer.Decisions = append(answer.Decisions, span)
		}
	}
	if len(answer.Decisions) == 0 {
		return answer
	}
	decision := answer.Decisions[len(answer.Decisions)-1]
	session := genai.SessionOf(decision)
	for _, span := range spans {
		if !query.IsCommand(span) || span.Text(telemetry.AttrCommand) == "guard" {
			continue
		}
		if decision.ParentID != "" && span.TraceID == decision.TraceID && span.ParentID == decision.ParentID {
			answer.Exact = append(answer.Exact, span)
			continue
		}
		if session == "" || genai.SessionOf(span) != session || span.Start.Before(decision.End) ||
			span.Start.Sub(decision.End) > toolCallWindow {
			continue
		}
		command := decision.Text(telemetry.AttrGuardCommand)
		if command == "" || strings.Contains(command, "jst "+span.Text(telemetry.AttrCommand)) {
			answer.Candidates = append(answer.Candidates, span)
		}
	}
	if len(answer.Exact) > 0 {
		answer.Candidates = nil
	}
	return answer
}

func printToolCall(out io.Writer, answer ToolCallAnswer, callID string) {
	if len(answer.Decisions) == 0 {
		fmt.Fprintf(out, "no guard decision on tool call %s in the range\n", callID)
		return
	}
	for _, decision := range answer.Decisions {
		fmt.Fprintln(out, "guard: "+commandLine(decision))
	}
	switch {
	case len(answer.Exact) > 0:
		fmt.Fprintln(out, "jackstay commands of this tool call (by its trace):")
		for _, span := range answer.Exact {
			fmt.Fprintln(out, "  "+commandLine(span))
		}
	case len(answer.Candidates) > 0:
		fmt.Fprintf(out, "jackstay commands of the same session started within %s after the answer (joined by time, not by trace):\n", toolCallWindow)
		for _, span := range answer.Candidates {
			fmt.Fprintln(out, "  "+commandLine(span))
		}
	default:
		fmt.Fprintln(out, "no jackstay command followed it")
	}
}
