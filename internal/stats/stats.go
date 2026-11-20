// Package stats is `jst stats` (docs/design/observability.md, sections 11 and 12): named reports
// computed from the journal's spans when asked, so their definitions can improve without
// touching what was written. Each report is a function from spans to rows.
package stats

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zima-oss/jackstay/internal/cli"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/telemetry/query"
)

func init() {
	cli.Register(cli.Command{Name: "stats", Summary: "reports over jackstay's journal",
		Usage: help, Run: command})
}

// Report is a report's answer: named columns and rows of plain values.
type Report struct {
	Name    string   `json:"report"`
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	// Notes are lines said under the table (a join by time, what could not be read).
	Notes []string `json:"notes,omitempty"`
}

// Input is what a report is computed from.
type Input struct {
	Spans    []journal.Span
	From, To time.Time
	// Claude reads each session's transcript for its tokens and cost (sessions only).
	Claude bool
}

// Owns says a span's counts and measures (a call, a run, its tokens, its verdict) fall in the
// range: they belong to the moment it ended — the day file it is in. Its time is another matter:
// a report that sums time clips every span overlapping the range (query.Clip). One rule for every
// report, and for --by day, so nothing is counted twice and nothing is lost at a boundary. A zero bound does not bound.
func (input Input) Owns(span journal.Span) bool { return input.OwnsAt(span.End) }

// OwnsAt says a moment (a span's end, an event's time) falls in the range.
func (input Input) OwnsAt(moment time.Time) bool {
	return (input.From.IsZero() || !moment.Before(input.From)) && (input.To.IsZero() || moment.Before(input.To))
}

type reportFunc func(Input) Report

type entry struct {
	name    string
	summary string
	compute reportFunc
}

// Reports are the named reports, in the order `jst stats` lists them.
var reports = []entry{
	{"guard", "calls by outcome, answer latency (median, p95) by version, failures with their caller and stack", Guard},
	{"refusals", "per rule: refusals, share, retried within 2 minutes, median delay, given up; by project", Refusals},
	{"flaky", "test runs of one key and head with both a pass and a failure", Flaky},
	{"tests", "per project: runs by outcome and the time in failed and stopped runs; gates", Tests},
	{"phases", "each phase: count, median, p95, max; each lock: waits, total, p95, gave up", Phases},
	{"reviews", "per task: rounds, attempts, Codex runs and failures, tokens, minutes, last verdict", Reviews},
	{"background", "jst bg starts, ends, stops, holds and cleanups; background commands asked for outside jst bg, with the guard's answer", Background},
	{"sessions", "per Claude session: jackstay's time (guard, tests, waits, reviews), refusals; --claude adds the session's own tokens and cost", Sessions},
	{"load", "per project and day: busy time of jackstay commands and of their children, the most at once", Load},
}

func reportNamed(name string) reportFunc {
	for _, candidate := range reports {
		if candidate.name == name {
			return candidate.compute
		}
	}
	return nil
}

// Compute runs a report by name.
func Compute(name string, input Input) (Report, bool) {
	compute := reportNamed(name)
	if compute == nil {
		return Report{}, false
	}
	report := compute(input)
	report.Name = name
	return report, true
}

// By splits a report by a key (version, project, day), the key a first column. A version or a
// project is one process's (its resource), so its spans are a part of their own. A day is a range:
// each day's report is computed over every span, with that day as its range, so a span keeps its
// parent whatever day it began, its counts fall on the day it ended and its time is clipped to each
// day it overlaps (Input.Owns).
func By(name, key string, input Input) (Report, error) {
	compute := reportNamed(name)
	if compute == nil {
		return Report{}, fmt.Errorf("no report %q (jst stats lists them)", name)
	}
	parts := map[string]Input{}
	switch key {
	case "version", "project":
		keyOf := map[string]func(journal.Span) string{"version": query.Version, "project": query.Project}[key]
		for _, span := range input.Spans {
			part := parts[keyOf(span)]
			part.Spans, part.From, part.To, part.Claude = append(part.Spans, span), input.From, input.To, input.Claude
			parts[keyOf(span)] = part
		}
	case "day":
		if len(input.Spans) == 0 {
			break
		}
		first, last := input.Spans[0].Start, input.Spans[0].End
		for _, span := range input.Spans {
			first, last = minTime(first, span.Start), maxTime(last, span.End)
		}
		if !input.From.IsZero() {
			first = maxTime(first, input.From)
		}
		if !input.To.IsZero() {
			last = minTime(last, input.To)
		}
		// Days of the local calendar (the journal's), whatever zone the bounds were given in.
		for day := dayOf(first.In(time.Local)); !day.After(last); day = day.AddDate(0, 0, 1) {
			from, until := maxTime(day, first), day.AddDate(0, 0, 1)
			if !input.To.IsZero() {
				until = minTime(until, input.To)
			}
			if !until.After(from) {
				continue
			}
			parts[day.Format(time.DateOnly)] = Input{Spans: input.Spans, From: from, To: until, Claude: input.Claude}
		}
	default:
		return Report{}, fmt.Errorf("--by takes version, project or day, not %q", key)
	}
	keys := make([]string, 0, len(parts))
	for found := range parts {
		keys = append(keys, found)
	}
	sort.Strings(keys)
	combined := Report{Name: name}
	for _, found := range keys {
		report := compute(parts[found])
		combined.Columns = append([]string{key}, report.Columns...)
		for _, row := range report.Rows {
			combined.Rows = append(combined.Rows, append([]any{found}, row...))
		}
		combined.Notes = append(combined.Notes, report.Notes...)
	}
	return combined, nil
}

const usageText = `usage: jst stats [REPORT] [--since DUR|TIME] [--until TIME] [--project P | --all-projects]
                [--session ID] [--by version|project|day] [--json] [--claude]`

func command(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet("jst stats", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	since := flags.String("since", "30d", "")
	untilText := flags.String("until", "", "")
	scope := query.Scope{}
	flags.StringVar(&scope.Project, "project", "", "")
	flags.BoolVar(&scope.All, "all-projects", false, "")
	session := flags.String("session", "", "")
	splitBy := flags.String("by", "", "")
	asJSON := flags.Bool("json", false, "")
	claude := flags.Bool("claude", false, "")
	err := flags.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, usageText)
		return 0
	}
	if err == nil && flags.NArg() > 0 {
		err = fmt.Errorf("unexpected %q", flags.Arg(0))
	}
	if err != nil {
		fmt.Fprintln(stderr, "jst stats:", err)
		fmt.Fprintln(stderr, usageText)
		return 2
	}
	if name == "" {
		writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		for _, candidate := range reports {
			fmt.Fprintf(writer, "  %s\t%s\n", candidate.name, candidate.summary)
		}
		_ = writer.Flush()
		return 0
	}
	now := time.Now()
	from, err := query.ParseTime(*since, now)
	until := now.Add(time.Minute)
	if err == nil && *untilText != "" {
		until, err = query.ParseTime(*untilText, now)
	}
	if err != nil {
		fmt.Fprintln(stderr, "jst stats:", err)
		return 2
	}
	if *session != "" || name == "sessions" {
		scope.All = true
	}
	folders, err := query.Folders(scope)
	if err != nil {
		fmt.Fprintln(stderr, "jst stats:", err)
		return 1
	}
	spans, result := query.Load(folders, from, until, now)
	if *session != "" {
		spans = OfSession(spans, *session)
	}
	input := Input{Spans: spans, From: from, To: until, Claude: *claude}
	var report Report
	if *splitBy != "" {
		report, err = By(name, *splitBy, input)
	} else {
		var found bool
		report, found = Compute(name, input)
		if !found {
			err = fmt.Errorf("no report %q (jst stats lists them)", name)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "jst stats:", err)
		return 2
	}
	if result.Unreadable > 0 {
		report.Notes = append(report.Notes, fmt.Sprintf("%d lines of the journal could not be read", result.Unreadable))
	}
	for _, problem := range result.Problems {
		fmt.Fprintln(stderr, "jst stats: cannot read", problem)
	}
	if result.Folders == 0 && len(result.Problems) > 0 {
		return 1
	}
	Write(stdout, report, from, until, *asJSON)
	return 0
}

// Write prints a report as a table, or as one JSON object.
func Write(out io.Writer, report Report, from, until time.Time, asJSON bool) {
	if asJSON {
		if report.Rows == nil {
			report.Rows = [][]any{}
		}
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(map[string]any{"report": report.Name, "range": map[string]string{
			"from": from.Format(time.RFC3339), "to": until.Format(time.RFC3339)}, "columns": report.Columns,
			"rows": report.Rows, "notes": report.Notes})
		return
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, strings.Join(report.Columns, "\t"))
	for _, row := range report.Rows {
		cells := make([]string, len(row))
		for index, cell := range row {
			cells[index] = cellText(cell)
		}
		fmt.Fprintln(writer, strings.Join(cells, "\t"))
	}
	_ = writer.Flush()
	if len(report.Rows) == 0 {
		fmt.Fprintln(out, "(nothing in the range)")
	}
	for _, note := range report.Notes {
		fmt.Fprintln(out, note)
	}
}

func cellText(cell any) string {
	switch typed := cell.(type) {
	case float64:
		return fmt.Sprintf("%.1f", typed)
	case []string:
		return strings.Join(typed, ",")
	case nil:
		return "-"
	}
	return fmt.Sprint(cell)
}
