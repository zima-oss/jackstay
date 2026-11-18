package baseline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/gitcli"
	"github.com/zima-oss/jackstay/internal/guard"
	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/version"
)

const (
	// shortestRun is in seconds: a call answered sooner is not a run's time, and a shorter
	// overlap is none.
	shortestRun = 5
	day         = 86400
	gitDeadline = 60 * time.Second
)

var pullRequestSubject = regexp.MustCompile(`\(#\d+\)$`)

// Summary is one kind's calls: what became of them and their wall time.
type Summary struct {
	Attempts        int    `json:"attempts"`
	Denied          int    `json:"denied"`
	Failed          int    `json:"failed"`
	Gate            *bool  `json:"gate,omitempty"`
	Refused         int    `json:"refused"`
	Started         int    `json:"started"`
	StartedPerMerge *Ratio `json:"started_per_merge"`
	Timed           int    `json:"timed"`
	WallHours       Ratio  `json:"wall_hours"`
	WallMedian      *int   `json:"wall_median_s"`
	WallP90         *int   `json:"wall_p90_s"`
}

// Overlap is one gate kind's timed runs and how many shared wall time with another gate run.
type Overlap struct {
	MedianNotOverlapped *int `json:"median_not_overlapped_s"`
	MedianOverlapped    *int `json:"median_overlapped_s"`
	NotOverlapped       int  `json:"not_overlapped"`
	Overlapped          int  `json:"overlapped"`
	OverlappedOther     int  `json:"overlapped_other_project"`
	OverlappedSame      int  `json:"overlapped_same_project"`
	Timed               int  `json:"timed"`
}

// Fates counts by what became of a call.
type Fates struct {
	Denied  int `json:"denied"`
	Ran     int `json:"ran"`
	Refused int `json:"refused"`
}

func (fates *Fates) add(fate string, count int) {
	switch fate {
	case fateRefused:
		fates.Refused += count
	case fateDenied:
		fates.Denied += count
	default:
		fates.Ran += count
	}
}

// Destructive is what today's guard finds in the span's commands, by rule and by fate.
type Destructive struct {
	Calls        Fates          `json:"calls"`
	Denied       map[string]int `json:"denied"`
	DeniedTotal  int            `json:"denied_total"`
	Ran          map[string]int `json:"ran"`
	RanTotal     int            `json:"ran_total"`
	Refused      map[string]int `json:"refused"`
	RefusedTotal int            `json:"refused_total"`
}

// Merges is what landed on the branch in the span.
type Merges struct {
	Branch       string `json:"branch"`
	Commits      *int   `json:"commits"`
	Count        *int   `json:"count"`
	PullRequests *int   `json:"pull_requests"`
	Tags         *int   `json:"tags"`
	Unit         string `json:"unit"`
}

// Day is one UTC day of the span.
type Day struct {
	Destructive Fates              `json:"destructive"`
	Kinds       map[string]Summary `json:"kinds"`
	Merges      int                `json:"merges"`
	Overlap     map[string]Overlap `json:"overlap"`
}

// Headline is the numbers a project's baseline is judged by.
type Headline struct {
	DestructiveRan            int             `json:"destructive_ran"`
	DestructiveRefused        int             `json:"destructive_refused"`
	ExecutedByJackstayLog     *int            `json:"executed_by_jackstay_log"`
	GateRunsStarted           int             `json:"gate_runs_started"`
	GateRunsStartedPerMerge   *Ratio          `json:"gate_runs_started_per_merge"`
	GateWallMedian            map[string]*int `json:"gate_wall_median_s"`
	Gates                     []string        `json:"gates"`
	ListedRunsStarted         int             `json:"listed_runs_started"`
	ListedRunsStartedPerMerge *Ratio          `json:"listed_runs_started_per_merge"`
	Per                       string          `json:"per"`
	ReusedByJackstayLog       *int            `json:"reused_by_jackstay_log"`
}

// Report is everything `jst baseline` says about one project and span.
type Report struct {
	BashCalls       int                `json:"bash_calls"`
	BashCallsByFate map[string]int     `json:"bash_calls_by_fate"`
	Days            map[string]Day     `json:"days"`
	Definition      string             `json:"definition"`
	Destructive     Destructive        `json:"destructive"`
	GuardVersion    string             `json:"guard_version"`
	Headline        Headline           `json:"headline"`
	Kinds           map[string]Summary `json:"kinds"`
	Merges          Merges             `json:"merges"`
	Overlap         struct {
		Kinds map[string]Overlap `json:"kinds"`
		With  []string           `json:"with"`
	} `json:"overlap"`
	Project     string    `json:"project"`
	Since       string    `json:"since"`
	Until       string    `json:"until"`
	JackstayLog LogReport `json:"jackstay_log"`
}

// A Span is the days measured: from the first's start to the last's end, UTC.
type Span struct {
	Since, Until string
	Low, High    float64
}

// SpanOf is the span of two days written YYYY-MM-DD.
func SpanOf(since, until string) (Span, error) {
	low, lowOK := stamp(since + "T00:00:00Z")
	high, highOK := stamp(until + "T23:59:59Z")
	if !lowOK {
		return Span{}, fmt.Errorf("not a day (YYYY-MM-DD): %s", since)
	}
	if !highOK {
		return Span{}, fmt.Errorf("not a day (YYYY-MM-DD): %s", until)
	}
	return Span{Since: since, Until: until, Low: low, High: high}, nil
}

func (span Span) holds(seconds float64) bool { return span.Low <= seconds && seconds <= span.High }

// Inputs are where a report reads from.
type Inputs struct {
	Repo        string
	ProjectsDir string
	Also        []Definition
	LogPaths    []string
	LogGiven    bool   // --jackstay-log named one file
	Examples    string // a file for each command the guard would refuse ("" none)
}

type entry struct {
	fate   string
	took   *float64
	failed bool
}

func summarise(entries []entry, merges *int) Summary {
	var times []float64
	found := Summary{Attempts: len(entries)}
	for _, one := range entries {
		switch one.fate {
		case fateRefused:
			found.Refused++
		case fateDenied:
			found.Denied++
		}
		if one.took != nil {
			times = append(times, *one.took)
		}
		if one.failed {
			found.Failed++
		}
	}
	slices.Sort(times)
	found.Started = found.Attempts - found.Refused - found.Denied
	found.Timed = len(times)
	found.WallMedian, found.WallP90, found.WallHours = median(times), percentile90(times), hours(times)
	found.StartedPerMerge = ratioOrNil(found.Started, merges)
	return found
}

// landed is what git says landed on the branch: totals and per day.
type landed struct {
	prs, commits, tags *int
	days               map[string][2]int // day: prs, commits
}

func merged(repo string, span Span, branch string) landed {
	found := landed{days: map[string][2]int{}}
	git := gitcli.Git{Dir: repo}
	ctx, cancel := context.WithTimeout(context.Background(), gitDeadline)
	defer cancel()
	output, err := git.Run(ctx, "log", "--first-parent", "--format=%ct%x09%s", "--end-of-options", branch)
	if err != nil {
		return found
	}
	prs, commits := 0, 0
	for _, line := range strings.Split(string(output), "\n") {
		when, subject, tabbed := strings.Cut(line, "\t")
		seconds, number := wholeNumber(when)
		if !tabbed || !number || !span.holds(float64(seconds)) {
			continue
		}
		counts := found.days[dayOf(float64(seconds))]
		counts[1]++
		commits++
		if pullRequestSubject.MatchString(subject) {
			counts[0]++
			prs++
		}
		found.days[dayOf(float64(seconds))] = counts
	}
	found.prs, found.commits = &prs, &commits
	tagCtx, tagCancel := context.WithTimeout(context.Background(), gitDeadline)
	defer tagCancel()
	if tagged, err := git.Run(tagCtx, "for-each-ref", "refs/tags", "--format=%(creatordate:unix)"); err == nil {
		tags := 0
		for _, word := range strings.Fields(string(tagged)) {
			if seconds, number := wholeNumber(word); number && span.holds(float64(seconds)) {
				tags++
			}
		}
		found.tags = &tags
	}
	return found
}

func wholeNumber(word string) (int64, bool) {
	if word == "" || strings.Trim(word, "0123456789") != "" {
		return 0, false
	}
	number, err := strconv.ParseInt(word, 10, 64)
	return number, err == nil
}

// A gateRun is a started call of a gate kind with its wall time.
type gateRun struct {
	project, kind, id string
	gate              bool
	at                float64
	end               *float64
}

// startedRuns are a project's calls that started a run of a listed kind; end is nil when the run
// has no matched end or was answered at once.
func startedRuns(spec Definition, found calls, keep func(seconds float64) bool) []gateRun {
	var runs []gateRun
	for _, id := range found.order {
		one := found.byID[id]
		when, timed := stamp(one.at)
		if !timed || !keep(when) || one.fate == fateRefused || one.fate == fateDenied {
			continue
		}
		kind := kindOf(spec.Kinds, one.command)
		if kind == "" {
			continue
		}
		var end *float64
		if ended, ok := stamp(one.ended); ok && ended-when >= shortestRun {
			end = &ended
		}
		gate := slices.ContainsFunc(spec.Kinds, func(listed Kind) bool { return listed.Name == kind && listed.Gate })
		runs = append(runs, gateRun{project: spec.Project, kind: kind, id: id, gate: gate, at: when, end: end})
	}
	return runs
}

// overlapOf is, per gate kind of subjects, how many shared at least shortestRun seconds of wall
// time with another timed gate run of pool, and the medians.
func overlapOf(subjects, pool []gateRun) map[string]Overlap {
	type tally struct {
		timed, same, other, either int
		over, alone                []float64
	}
	tallies := map[string]*tally{}
	for _, run := range subjects {
		found := tallies[run.kind]
		if found == nil {
			found = &tally{}
			tallies[run.kind] = found
		}
		found.timed++
		same, other := false, false
		for _, peer := range pool {
			if peer.id == run.id || peer.end == nil || min(*run.end, *peer.end)-max(run.at, peer.at) < shortestRun {
				continue
			}
			if peer.project == run.project {
				same = true
			} else {
				other = true
			}
		}
		if same {
			found.same++
		}
		if other {
			found.other++
		}
		if same || other {
			found.either++
			found.over = append(found.over, *run.end-run.at)
		} else {
			found.alone = append(found.alone, *run.end-run.at)
		}
	}
	report := map[string]Overlap{}
	for kind, found := range tallies {
		report[kind] = Overlap{Timed: found.timed, Overlapped: found.either, OverlappedSame: found.same, OverlappedOther: found.other,
			NotOverlapped: found.timed - found.either, MedianOverlapped: median(found.over), MedianNotOverlapped: median(found.alone)}
	}
	return report
}

// destructive puts every call through today's guard. What git would have answered then cannot be
// asked now (guard.NoGit); no TMPDIR is given, so the guard's stand-in is used.
func destructive(spec []*call, examples string) (Destructive, map[string]Fates, error) {
	found := Destructive{Ran: map[string]int{}, Refused: map[string]int{}, Denied: map[string]int{}}
	perDay := map[string]Fates{}
	var shown []map[string]any
	home := homedir.AccountHome()
	for _, one := range spec {
		folder := one.cwd
		if folder == "" {
			folder = "/"
		}
		findings := decide(one.command, guard.Settings{Folder: folder, Environ: map[string]string{}, Home: home, Git: guard.NoGit{}})
		if len(findings) == 0 {
			continue
		}
		fate := one.fate
		if fate != fateRefused && fate != fateDenied {
			fate = fateRan // no result at all: taken to have run
		}
		found.Calls.add(fate, 1)
		when, _ := stamp(one.at)
		counts := perDay[dayOf(when)]
		var rules, matched []string
		for _, finding := range findings {
			rules = append(rules, finding.Rule)
			matched = append(matched, finding.Command)
		}
		distinct := slices.Clone(rules)
		slices.Sort(distinct)
		for _, rule := range slices.Compact(distinct) {
			byRule := map[string]map[string]int{fateRan: found.Ran, fateRefused: found.Refused, fateDenied: found.Denied}[fate]
			byRule[rule]++
			counts.add(fate, 1)
		}
		perDay[dayOf(when)] = counts
		shown = append(shown, map[string]any{"fate": fate, "rules": rules, "matched": matched, "command": firstRunes(one.command, 600)})
	}
	for _, count := range found.Ran {
		found.RanTotal += count
	}
	for _, count := range found.Refused {
		found.RefusedTotal += count
	}
	for _, count := range found.Denied {
		found.DeniedTotal += count
	}
	if examples != "" {
		var built strings.Builder
		for _, row := range shown {
			line, err := json.Marshal(row)
			if err != nil {
				return found, perDay, err
			}
			built.Write(line)
			built.WriteByte('\n')
		}
		if err := os.WriteFile(examples, []byte(built.String()), 0o600); err != nil {
			return found, perDay, err
		}
	}
	return found, perDay, nil
}

// decide is the guard's findings; a line the guard stops on is counted as its own rule, not lost.
func decide(command string, settings guard.Settings) (findings []guard.Finding) {
	defer func() {
		if problem := recover(); problem != nil {
			findings = []guard.Finding{{Rule: fmt.Sprintf("guard-error:%T", problem), Command: firstRunes(command, 120)}}
		}
	}()
	return guard.Decide(command, settings).Findings
}

func firstRunes(text string, count int) string {
	runes := []rune(text)
	if len(runes) <= count {
		return text
	}
	return string(runes[:count])
}

// Measure is the report for one project.
func Measure(spec Definition, span Span, inputs Inputs) (Report, error) {
	everything := readCalls(transcriptFiles(spec.Transcripts, inputs.ProjectsDir))
	var inSpan []*call
	for _, id := range everything.order {
		if at, ok := stamp(everything.byID[id].at); ok && span.holds(at) {
			inSpan = append(inSpan, everything.byID[id])
		}
	}
	found := merged(inputs.Repo, span, spec.Branch)
	unit := "commit"
	if spec.MergeUnit == "pr" && found.prs != nil && *found.prs > 0 {
		unit = "pr"
	}
	merges := found.commits
	if unit == "pr" {
		merges = found.prs
	}

	byKind, byDay := map[string][]entry{}, map[string]map[string][]entry{}
	fates := map[string]int{fateRan: 0, fateRefused: 0, fateDenied: 0, fateUnanswered: 0}
	for _, one := range inSpan {
		fates[one.fate]++
		kind := kindOf(spec.Kinds, one.command)
		if kind == "" {
			continue
		}
		when, _ := stamp(one.at)
		ran := one.fate != fateRefused && one.fate != fateDenied
		var took *float64
		if ended, ok := stamp(one.ended); ran && ok && ended-when >= shortestRun {
			took = new(float64)
			*took = ended - when
		}
		row := entry{fate: one.fate, took: took, failed: ran && one.failed}
		byKind[kind] = append(byKind[kind], row)
		name := dayOf(when)
		if byDay[name] == nil {
			byDay[name] = map[string][]entry{}
		}
		byDay[name][kind] = append(byDay[name][kind], row)
	}
	harmful, harmfulDays, err := destructive(inSpan, inputs.Examples)
	if err != nil {
		return Report{}, err
	}

	// Overlap: this project's timed gate runs of the span against every timed gate run that could
	// reach into it (a day on each side), its own and the other projects'.
	near := func(seconds float64) bool { return span.Low-day <= seconds && seconds <= span.High+day }
	var pool []gateRun
	keepTimedGates := func(runs []gateRun) {
		for _, run := range runs {
			if run.gate && run.end != nil {
				pool = append(pool, run)
			}
		}
	}
	keepTimedGates(startedRuns(spec, everything, near))
	with := []string{spec.Project}
	for _, other := range inputs.Also {
		keepTimedGates(startedRuns(other, readCalls(transcriptFiles(other.Transcripts, inputs.ProjectsDir)), near))
		with = append(with, other.Project)
	}
	var subjects []gateRun
	for _, run := range pool {
		if run.project == spec.Project && span.holds(run.at) {
			subjects = append(subjects, run)
		}
	}

	report := Report{Project: spec.Project, Since: span.Since, Until: span.Until, Definition: spec.Source, BashCalls: len(inSpan),
		BashCallsByFate: fates, Kinds: map[string]Summary{}, Days: map[string]Day{}, Destructive: harmful,
		GuardVersion: version.Version}
	report.Merges = Merges{Unit: unit, Count: merges, Branch: spec.Branch, PullRequests: found.prs, Commits: found.commits, Tags: found.tags}
	var gates []string
	gateStarted, listed := 0, 0
	gateMedians := map[string]*int{}
	for _, kind := range spec.Kinds {
		summary := summarise(byKind[kind.Name], merges)
		gate := kind.Gate
		summary.Gate = &gate
		report.Kinds[kind.Name] = summary
		listed += summary.Started
		if kind.Gate {
			gates = append(gates, kind.Name)
			gateStarted += summary.Started
			gateMedians[kind.Name] = summary.WallMedian
		}
	}
	report.Overlap.With, report.Overlap.Kinds = with, overlapOf(subjects, pool)
	for moment := span.Low; moment <= span.High; moment += day {
		name := dayOf(moment)
		counts := found.days[name]
		then := counts[1]
		if unit == "pr" {
			then = counts[0]
		}
		kinds := map[string]Summary{}
		for kind, entries := range byDay[name] {
			kinds[kind] = summarise(entries, &then)
		}
		var onDay []gateRun
		for _, run := range subjects {
			if dayOf(run.at) == name {
				onDay = append(onDay, run)
			}
		}
		report.Days[name] = Day{Merges: then, Kinds: kinds, Destructive: harmfulDays[name], Overlap: overlapOf(onDay, pool)}
	}
	report.JackstayLog = readLog(inputs.LogPaths, inputs.LogGiven, spec, span)
	per := "commit on " + spec.Branch
	if unit == "pr" {
		per = "pull request"
	}
	if gates == nil {
		gates = []string{}
	}
	report.Headline = Headline{Gates: gates, GateRunsStarted: gateStarted, GateRunsStartedPerMerge: ratioOrNil(gateStarted, merges),
		GateWallMedian: gateMedians, ListedRunsStarted: listed, ListedRunsStartedPerMerge: ratioOrNil(listed, merges), Per: per,
		DestructiveRan: harmful.RanTotal, DestructiveRefused: harmful.RefusedTotal}
	if report.JackstayLog.Records > 0 {
		report.Headline.ExecutedByJackstayLog, report.Headline.ReusedByJackstayLog = intPointer(report.JackstayLog.Executed), intPointer(report.JackstayLog.Reused)
	}
	return report, nil
}

// sortedNames are a map's keys in order.
func sortedNames[Value any](values map[string]Value) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
