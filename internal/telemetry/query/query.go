// Package query is what `jst log` and `jst stats` share (docs/design/observability.md, section 12):
// which journal folders to read, the range, the spans read, and their trees.
package query

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
)

// Scope is which folders a query reads.
type Scope struct {
	// Project is one project's folder; empty with All false means the project around the working
	// folder (or `.all` outside one).
	Project string
	All     bool
}

// Folders are the journal folders of a scope.
func Folders(scope Scope) ([]string, error) {
	home, err := homedir.Home()
	if err != nil {
		return nil, err
	}
	if scope.All {
		found, _ := filepath.Glob(filepath.Join(home, "*", telemetry.FolderName))
		return found, nil
	}
	name := scope.Project
	if name == "" {
		if folder, err := os.Getwd(); err == nil {
			if root, place := config.Find(folder); place != "" {
				loaded := config.Config{}
				if data, err := os.ReadFile(place); err == nil { // #nosec G304 -- the project's jackstay.json
					loaded, _ = config.Parse(data)
				}
				name = config.ProjectName(root, loaded, root)
			}
		}
	}
	if name == "" {
		return []string{filepath.Join(home, homedir.Shared, telemetry.FolderName)}, nil
	}
	return []string{filepath.Join(home, homedir.MarkerName(name), telemetry.FolderName)}, nil
}

// ParseTime reads `2h`, `7d`, `90m` (before now) or an RFC 3339 time.
func ParseTime(text string, now time.Time) (time.Time, error) {
	if text == "" {
		return time.Time{}, errors.New("empty time")
	}
	if moment, err := time.Parse(time.RFC3339, text); err == nil {
		return moment, nil
	}
	if moment, err := time.ParseInLocation(time.DateOnly, text, time.Local); err == nil {
		return moment, nil
	}
	unit := text[len(text)-1]
	count, err := strconv.Atoi(text[:len(text)-1])
	if err != nil || count < 0 {
		return time.Time{}, fmt.Errorf("not a time: %q (2h, 7d, 90m, 2026-11-17 or RFC 3339)", text)
	}
	switch unit {
	case 'm':
		return now.Add(-time.Duration(count) * time.Minute), nil
	case 'h':
		return now.Add(-time.Duration(count) * time.Hour), nil
	case 'd':
		return now.AddDate(0, 0, -count), nil
	}
	return time.Time{}, fmt.Errorf("not a time: %q (2h, 7d, 90m, 2026-11-17 or RFC 3339)", text)
}

// Load reads the spans of folders that overlap [from, to), in order of start.
func Load(folders []string, from, until, now time.Time) ([]journal.Span, journal.Result) {
	var spans []journal.Span
	result := journal.Read(folders, from, until, now, func(span journal.Span) { spans = append(spans, span) })
	journal.SortByStart(spans)
	return spans, result
}

// IsCommand says a span is a command span (a process's own root, whatever its parent).
func IsCommand(span journal.Span) bool {
	return strings.HasPrefix(span.Name, "jst ") && span.Text(telemetry.AttrCommand) != ""
}

// Project is a span's project ("" outside one).
func Project(span journal.Span) string { return span.ResourceText(telemetry.AttrProject) }

// Version is the jackstay version that wrote a span.
func Version(span journal.Span) string { return span.ResourceText(telemetry.AttrServiceVersion) }

// Outcome is a span's outcome word.
func Outcome(span journal.Span) string { return span.Text(telemetry.AttrOutcome) }

// IsError says a span ended in error.
func IsError(span journal.Span) bool { return span.StatusCode == journal.StatusError }

// A Node is a span and its children in a tree.
type Node struct {
	Span     journal.Span
	Children []*Node
	// Orphan says the parent is not in the journal (killed, still running, or outside the range).
	Orphan bool
}

// Trees are spans arranged by parent, roots (and orphans) first by start.
func Trees(spans []journal.Span) []*Node {
	nodes := map[string]*Node{}
	for _, span := range spans {
		nodes[span.TraceID+"/"+span.SpanID] = &Node{Span: span}
	}
	var roots []*Node
	for _, span := range spans {
		node := nodes[span.TraceID+"/"+span.SpanID]
		if parent, ok := nodes[span.TraceID+"/"+span.ParentID]; ok && span.ParentID != "" {
			parent.Children = append(parent.Children, node)
			continue
		}
		node.Orphan = span.ParentID != "" && !IsCommand(span)
		roots = append(roots, node)
	}
	byStart := func(list []*Node) {
		sort.SliceStable(list, func(left, right int) bool { return list[left].Span.Start.Before(list[right].Span.Start) })
	}
	byStart(roots)
	for _, node := range nodes {
		byStart(node.Children)
	}
	return roots
}

// Percentile is the p-th percentile (0–100) of durations, nearest rank; 0 for none.
func Percentile(values []time.Duration, percent float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	rank := int(percent/100*float64(len(sorted))+0.999999) - 1
	return sorted[max(0, min(rank, len(sorted)-1))]
}

// Seconds is a duration as seconds with one decimal.
func Seconds(duration time.Duration) float64 {
	return float64(int64(duration.Seconds()*10+0.5)) / 10
}

// Clip is the part of a span inside [from, until); a zero bound does not bound.
func Clip(span journal.Span, from, until time.Time) time.Duration {
	start, end := span.Start, span.End
	if !from.IsZero() && start.Before(from) {
		start = from
	}
	if !until.IsZero() && end.After(until) {
		end = until
	}
	if end.Before(start) {
		return 0
	}
	return end.Sub(start)
}
