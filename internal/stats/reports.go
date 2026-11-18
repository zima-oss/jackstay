package stats

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/telemetry"
	"github.com/zima-oss/jackstay/internal/telemetry/genai"
	"github.com/zima-oss/jackstay/internal/telemetry/journal"
	"github.com/zima-oss/jackstay/internal/telemetry/query"
)

// Guard outcomes (the guard's decision event).
const (
	guardAllowed    = "allowed"
	guardRefused    = "refused"
	guardFailedOpen = "failed_open"
)

// retryWindow is how soon after a refusal another call of the session counts as its retry.
const retryWindow = 2 * time.Minute

func isGuard(span journal.Span) bool {
	return query.IsCommand(span) && span.Text(telemetry.AttrCommand) == "guard"
}

// guardSpans are the hook's decisions: every hook call records its outcome (a decision, a time-out
// or a recovered panic), so a guard span without one is the command used by hand (`jst guard check`,
// `--help`), whatever its end, and is not a call the hook let through.
func guardSpans(spans []journal.Span) []journal.Span {
	var found []journal.Span
	for _, span := range spans {
		if isGuard(span) && guardOutcome(span) != "" {
			found = append(found, span)
		}
	}
	return found
}

func guardOutcome(span journal.Span) string {
	return span.Text(telemetry.AttrGuardOutcome)
}

func answerTime(span journal.Span) time.Duration {
	if millis, ok := span.Attributes[telemetry.AttrGuardMillis].(float64); ok {
		return time.Duration(millis * float64(time.Millisecond))
	}
	return span.Duration()
}

func sortedKeys[V any](table map[string]V) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func millis(duration time.Duration) float64 { return float64(duration.Microseconds()) / 1000 }

// Guard answers A3 and A4.
func Guard(input Input) Report {
	report := Report{Columns: []string{"version", "calls", "allowed", "refused", "warned", "failed open", "other", "median ms", "p95 ms"}}
	byVersion := map[string][]journal.Span{}
	var failures []journal.Span
	for _, span := range guardSpans(input.Spans) {
		if !input.Owns(span) {
			continue
		}
		byVersion[query.Version(span)] = append(byVersion[query.Version(span)], span)
		if guardOutcome(span) == guardFailedOpen {
			failures = append(failures, span)
		}
	}
	for _, version := range sortedKeys(byVersion) {
		counts := map[string]int{}
		var times []time.Duration
		for _, span := range byVersion[version] {
			counts[guardOutcome(span)]++
			times = append(times, answerTime(span))
		}
		other := len(byVersion[version]) - counts[guardAllowed] - counts[guardRefused] - counts["warned"] - counts[guardFailedOpen]
		report.Rows = append(report.Rows, []any{version, len(byVersion[version]), counts[guardAllowed], counts[guardRefused],
			counts["warned"], counts[guardFailedOpen], other, millis(query.Percentile(times, 50)), millis(query.Percentile(times, 95))})
	}
	for _, span := range failures {
		caller := strings.Join(span.Texts(telemetry.AttrCallerArgv), " ")
		if caller == "" {
			caller = "unknown"
		}
		note := span.Start.Format("2006-01-02 15:04:05") + " failed open: " + span.StatusMessage + " " + span.Text(telemetry.AttrReason) +
			"; caller pid " + itoa(span.ResourceNumber(telemetry.AttrProcessParentPID)) + ": " + caller
		for _, event := range span.Events {
			if stack := event.Text(telemetry.AttrExceptionStack); stack != "" {
				lines := strings.Split(stack, "\n")
				note += "\n    " + strings.Join(lines[:min(len(lines), 5)], "\n    ")
			}
		}
		report.Notes = append(report.Notes, note)
	}
	return report
}

// Refusals answers A5.
func Refusals(input Input) Report {
	report := Report{Columns: []string{"rule", "project", "refusals", "share %", "retried", "median delay s", "given up"}}
	calls := guardSpans(input.Spans)
	bySession := map[string][]journal.Span{}
	for _, span := range calls {
		bySession[genai.SessionOf(span)] = append(bySession[genai.SessionOf(span)], span)
	}
	type tally struct {
		refusals, retried, givenUp int
		delays                     []time.Duration
	}
	tallies := map[[2]string]*tally{}
	total := 0
	for _, span := range calls {
		if guardOutcome(span) != guardRefused || !input.Owns(span) {
			continue
		}
		total++
		retried, delay := retryOf(span, bySession[genai.SessionOf(span)])
		for _, rule := range span.Texts(telemetry.AttrGuardRules) {
			key := [2]string{rule, query.Project(span)}
			if tallies[key] == nil {
				tallies[key] = &tally{}
			}
			found := tallies[key]
			found.refusals++
			if retried {
				found.retried++
				found.delays = append(found.delays, delay)
			} else {
				found.givenUp++
			}
		}
	}
	keys := make([][2]string, 0, len(tallies))
	for key := range tallies {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if tallies[keys[left]].refusals != tallies[keys[right]].refusals {
			return tallies[keys[left]].refusals > tallies[keys[right]].refusals
		}
		return keys[left][0]+keys[left][1] < keys[right][0]+keys[right][1]
	})
	for _, key := range keys {
		found := tallies[key]
		share := 0.0
		if total > 0 {
			share = float64(found.refusals) * 100 / float64(total)
		}
		report.Rows = append(report.Rows, []any{key[0], dash(key[1]), found.refusals, share, found.retried,
			query.Seconds(query.Percentile(found.delays, 50)), found.givenUp})
	}
	return report
}

// retryOf finds the session's next allowed call with another command within the retry window.
func retryOf(refused journal.Span, session []journal.Span) (bool, time.Duration) {
	if genai.SessionOf(refused) == "" {
		return false, 0
	}
	digest := refused.Text(telemetry.AttrGuardCommandID)
	for _, later := range session {
		if !later.Start.After(refused.Start) || later.Start.Sub(refused.End) > retryWindow {
			continue
		}
		if guardOutcome(later) == guardAllowed && later.Text(telemetry.AttrGuardCommandID) != digest {
			return true, later.Start.Sub(refused.End)
		}
	}
	return false, 0
}

func testRuns(spans []journal.Span) []journal.Span {
	var found []journal.Span
	for _, span := range spans {
		if span.Name == "test.run" {
			found = append(found, span)
		}
	}
	return found
}

func testOutcome(span journal.Span) string {
	for _, event := range span.Events {
		if event.Name == telemetry.EventTestVerdict {
			return event.Text(telemetry.AttrTestOutcome)
		}
	}
	return query.Outcome(span)
}

// Flaky answers the first half of A6.
func Flaky(input Input) Report {
	report := Report{Columns: []string{"project", "key", "head", "passed", "failed", "last"}}
	type group struct {
		project, key, head string
		passed, failed     int
		last               time.Time
	}
	groups := map[string]*group{}
	for _, span := range testRuns(input.Spans) {
		if !input.Owns(span) {
			continue
		}
		key, head := span.Text(telemetry.AttrTestKey), span.Text(telemetry.AttrTestHead)
		if key == "" || head == "" {
			continue
		}
		name := key + "@" + head
		if groups[name] == nil {
			groups[name] = &group{project: query.Project(span), key: key, head: head}
		}
		found := groups[name]
		switch testOutcome(span) {
		case "passed":
			found.passed++
		case "failed":
			found.failed++
		}
		if span.End.After(found.last) {
			found.last = span.End
		}
	}
	for _, name := range sortedKeys(groups) {
		found := groups[name]
		if found.passed > 0 && found.failed > 0 {
			report.Rows = append(report.Rows, []any{dash(found.project), short(found.key, 12), short(found.head, 12),
				found.passed, found.failed, found.last.Format("2006-01-02 15:04")})
		}
	}
	return report
}

// Tests answers the second half of A6.
func Tests(input Input) Report {
	report := Report{Columns: []string{"project", "kind", "runs", "passed", "failed", "stopped", "other", "failed s", "stopped s"}}
	type tally struct {
		runs, passed, failed, stopped, other int
		failedTime, stoppedTime              time.Duration
	}
	tallies := map[[2]string]*tally{}
	for _, span := range input.Spans {
		kind := map[string]string{"test.run": "run", "test.gate": "gate"}[span.Name]
		if kind == "" {
			continue
		}
		key := [2]string{dash(query.Project(span)), kind}
		if tallies[key] == nil {
			tallies[key] = &tally{}
		}
		found := tallies[key]
		spent := query.Clip(span, input.From, input.To)
		owned := input.Owns(span)
		count := func(counter *int) {
			if owned {
				*counter++
			}
		}
		count(&found.runs)
		switch outcome := testOutcome(span); {
		case outcome == "passed" || outcome == "reused" || outcome == "resumed":
			count(&found.passed)
		case outcome == "failed":
			count(&found.failed)
			found.failedTime += spent
		case strings.HasPrefix(outcome, "stopped"):
			count(&found.stopped)
			found.stoppedTime += spent
		default:
			count(&found.other)
		}
	}
	keys := make([][2]string, 0, len(tallies))
	for key := range tallies {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool { return keys[left][0]+keys[left][1] < keys[right][0]+keys[right][1] })
	for _, key := range keys {
		found := tallies[key]
		report.Rows = append(report.Rows, []any{key[0], key[1], found.runs, found.passed, found.failed, found.stopped,
			found.other, query.Seconds(found.failedTime), query.Seconds(found.stoppedTime)})
	}
	return report
}

// Phases answers A7.
func Phases(input Input) Report {
	report := Report{Columns: []string{"kind", "name", "count", "median s", "p95 s", "max s", "gave up"}}
	type tally struct {
		times  []time.Duration
		gaveUp int
	}
	phases, waits := map[string]*tally{}, map[string]*tally{}
	for _, span := range input.Spans {
		if !input.Owns(span) {
			continue
		}
		var table map[string]*tally
		name := span.Name
		switch {
		case strings.HasPrefix(name, "wait "):
			table, name = waits, span.Text(telemetry.AttrLockName)
		case query.IsCommand(span), strings.HasPrefix(name, "exec "), genai.IsAgentRun(span):
			continue
		default:
			table = phases
		}
		if table[name] == nil {
			table[name] = &tally{}
		}
		// A phase's duration is a property of the phase, not time spent in the range: each phase
		// that ended in the range is one sample, whole (the rule of Input.Owns).
		table[name].times = append(table[name].times, span.Duration())
		if gaveUp, _ := span.Attributes[telemetry.AttrWaitGaveUp].(bool); gaveUp {
			table[name].gaveUp++
		}
	}
	for kind, table := range map[string]map[string]*tally{"phase": phases, "wait": waits} {
		for _, name := range sortedKeys(table) {
			found := table[name]
			report.Rows = append(report.Rows, []any{kind, name, len(found.times), query.Seconds(query.Percentile(found.times, 50)),
				query.Seconds(query.Percentile(found.times, 95)), query.Seconds(query.Percentile(found.times, 100)), found.gaveUp})
		}
	}
	sort.SliceStable(report.Rows, func(left, right int) bool {
		return report.Rows[left][0].(string) < report.Rows[right][0].(string)
	})
	return report
}

// Reviews answers A8.
func Reviews(input Input) Report {
	report := Report{Columns: []string{"project", "task", "rounds", "attempts", "codex runs", "codex failures", "input", "cached",
		"output", "reasoning", "minutes", "last verdict"}}
	type tally struct {
		rounds, attempts map[int64]bool
		runs             int
		failures         map[string]int
		usage            genai.Usage
		minutes          float64
		lastVerdict      string
		lastVerdictAt    time.Time
	}
	attempts := map[string]journal.Span{}
	for _, span := range input.Spans {
		if span.Name == "review.attempt" {
			attempts[span.TraceID+"/"+span.SpanID] = span
		}
	}
	tallies := map[string]*tally{}
	// A task is one project's (its store is the project's): the key is the project and the task.
	get := func(span journal.Span, task string) *tally {
		key := dash(query.Project(span)) + "\x00" + task
		if tallies[key] == nil {
			tallies[key] = &tally{rounds: map[int64]bool{}, attempts: map[int64]bool{}, failures: map[string]int{}}
		}
		return tallies[key]
	}
	for _, attempt := range attempts {
		found := get(attempt, attempt.Text(telemetry.AttrReviewTask))
		if input.Owns(attempt) {
			found.attempts[attempt.Number(telemetry.AttrReviewAttempt)] = true
		}
		for _, event := range attempt.Events {
			if event.Name == telemetry.EventReviewVerdict && input.OwnsAt(event.Time) {
				found.rounds[event.Number(telemetry.AttrReviewRound)] = true
				if attempt.End.After(found.lastVerdictAt) {
					found.lastVerdict, found.lastVerdictAt = event.Text(telemetry.AttrReviewOutcome), attempt.End
				}
			}
		}
	}
	for _, span := range input.Spans {
		run, ok := genai.AgentRunOf(span)
		if !ok {
			continue
		}
		attempt, known := attempts[span.TraceID+"/"+span.ParentID]
		task := "?"
		if known {
			task = attempt.Text(telemetry.AttrReviewTask)
		}
		found := get(span, task)
		found.minutes += query.Clip(span, input.From, input.To).Minutes()
		if !input.Owns(span) {
			continue
		}
		found.runs++
		found.usage = found.usage.Add(run.Turns[0])
		if query.IsError(span) {
			found.failures[span.Text(telemetry.AttrErrorType)]++
		}
	}
	for _, key := range sortedKeys(tallies) {
		project, task, _ := strings.Cut(key, "\x00")
		found := tallies[key]
		var failures []string
		for _, kind := range sortedKeys(found.failures) {
			failures = append(failures, kind+"×"+itoa(int64(found.failures[kind])))
		}
		failureText := "0"
		if len(failures) > 0 {
			failureText = strings.Join(failures, ",")
		}
		report.Rows = append(report.Rows, []any{project, task, len(found.rounds), len(found.attempts), found.runs, failureText,
			found.usage.Input, found.usage.CachedInput, found.usage.Output, found.usage.ReasoningOutput,
			float64(int64(found.minutes*10+0.5)) / 10, dash(found.lastVerdict)})
	}
	return report
}

// Background answers A9.
func Background(input Input) Report {
	report := Report{Columns: []string{"project", "session", "kind", "count", "refused by the guard"}}
	type key struct{ project, session, kind string }
	counts, refused := map[key]int{}, map[key]int{}
	for _, span := range input.Spans {
		for _, event := range span.Events {
			if !input.OwnsAt(event.Time) {
				continue
			}
			kind := map[string]string{telemetry.EventBgStarted: "jst bg started", telemetry.EventBgEnded: "jst bg ended",
				telemetry.EventBgStopped: "jst bg stopped", telemetry.EventBgHeld: "jst bg held", telemetry.EventBgCleaned: "jst bg cleaned",
				telemetry.EventBgRequestOutside: "asked outside jst bg"}[event.Name]
			if kind == "" {
				continue
			}
			found := key{dash(query.Project(span)), dash(short(genai.SessionOf(span), 8)), kind}
			counts[found]++
			if event.Name == telemetry.EventBgRequestOutside && guardOutcome(span) == guardRefused {
				refused[found]++
			}
		}
	}
	keys := make([]key, 0, len(counts))
	for found := range counts {
		keys = append(keys, found)
	}
	sort.Slice(keys, func(left, right int) bool {
		return keys[left].project+keys[left].session+keys[left].kind < keys[right].project+keys[right].session+keys[right].kind
	})
	for _, found := range keys {
		report.Rows = append(report.Rows, []any{found.project, found.session, found.kind, counts[found], refused[found]})
	}
	return report
}

// OfSession is the spans of one Claude session (and the traces they are in).
func OfSession(spans []journal.Span, session string) []journal.Span {
	traces := map[string]bool{}
	for _, span := range spans {
		if genai.SessionOf(span) == session {
			traces[span.TraceID] = true
		}
	}
	var found []journal.Span
	for _, span := range spans {
		if traces[span.TraceID] {
			found = append(found, span)
		}
	}
	return found
}

// Sessions answers A10.
func Sessions(input Input) Report {
	report := Report{Columns: []string{"session", "projects", "guard calls", "refused", "commands", "jackstay s", "tests s",
		"waits s", "reviews s", "codex tokens"}}
	if input.Claude {
		report.Columns = append(report.Columns, "claude input", "claude output", "claude cost $")
	}
	type tally struct {
		projects                            map[string]bool
		guard, refused, commands            int
		jackstayTime, tests, waits, reviews time.Duration
		tokens                              int64
		transcript                          string
	}
	tallies := map[string]*tally{}
	traceSession := map[string]string{}
	for _, span := range input.Spans {
		if session := genai.SessionOf(span); session != "" && query.IsCommand(span) {
			traceSession[span.TraceID] = session
		}
	}
	for _, span := range input.Spans {
		session := genai.SessionOf(span)
		if session == "" {
			session = traceSession[span.TraceID]
		}
		if session == "" {
			continue
		}
		if tallies[session] == nil {
			tallies[session] = &tally{projects: map[string]bool{}}
		}
		found := tallies[session]
		spent := query.Clip(span, input.From, input.To)
		owned := input.Owns(span)
		switch {
		case isGuard(span):
			if owned {
				found.guard++
				if guardOutcome(span) == guardRefused {
					found.refused++
				}
			}
			if transcript := span.Text(telemetry.AttrClaudeTranscript); transcript != "" {
				found.transcript = transcript
			}
			found.jackstayTime += spent
		case query.IsCommand(span):
			if owned {
				found.commands++
			}
			if project := query.Project(span); project != "" {
				found.projects[project] = true
			}
			if span.ParentID == "" || traceSession[span.TraceID] == "" || !hasCommandParent(span, input.Spans) {
				found.jackstayTime += spent
			}
		case span.Name == "test.run" || span.Name == "test.gate":
			if span.Name == "test.run" {
				found.tests += spent
			}
		case strings.HasPrefix(span.Name, "wait "):
			found.waits += spent
		case genai.IsAgentRun(span):
			found.reviews += spent
			if run, ok := genai.AgentRunOf(span); ok && owned {
				found.tokens += run.Turns[0].Input + run.Turns[0].Output
			}
		}
	}
	for _, session := range sortedKeys(tallies) {
		found := tallies[session]
		row := []any{session, strings.Join(sortedKeys(found.projects), ","), found.guard, found.refused, found.commands,
			query.Seconds(found.jackstayTime), query.Seconds(found.tests), query.Seconds(found.waits), query.Seconds(found.reviews), found.tokens}
		if input.Claude {
			usage := ReadTranscript(found.transcript, session)
			row = append(row, usage.Input, usage.Output, usage.Cost)
			if !usage.Read {
				report.Notes = append(report.Notes, "session "+session+": no transcript read")
			}
		}
		report.Rows = append(report.Rows, row)
	}
	return report
}

// hasCommandParent says a command span runs inside another jackstay command of the spans (its time is
// that command's already).
func hasCommandParent(span journal.Span, spans []journal.Span) bool {
	byID := map[string]journal.Span{}
	for _, other := range spans {
		if other.TraceID == span.TraceID {
			byID[other.SpanID] = other
		}
	}
	for parent, ok := byID[span.ParentID]; ok; parent, ok = byID[parent.ParentID] {
		if query.IsCommand(parent) {
			return true
		}
	}
	return false
}

// Load answers A11.
func Load(input Input) Report {
	report := Report{Columns: []string{"project", "day", "commands", "busy s", "children busy s", "most at once"}}
	type tally struct {
		commands        int
		busy, childBusy time.Duration
		intervals       [][2]time.Time
	}
	tallies := map[[2]string]*tally{}
	for _, span := range input.Spans {
		command, child := query.IsCommand(span), strings.HasPrefix(span.Name, "exec ")
		if !command && !child {
			continue
		}
		if command && input.Owns(span) {
			// Counted once, on the local day it ended (Input.Owns), apart from its time below.
			key := [2]string{dash(query.Project(span)), dayOf(span.End.In(time.Local)).Format(time.DateOnly)}
			if tallies[key] == nil {
				tallies[key] = &tally{}
			}
			tallies[key].commands++
		}
		until := span.End
		if !input.To.IsZero() {
			until = minTime(span.End, input.To)
		}
		for day := dayOf(maxTime(span.Start, input.From).In(time.Local)); day.Before(until); day = day.AddDate(0, 0, 1) {
			next := day.AddDate(0, 0, 1)
			spent := query.Clip(span, maxTime(day, input.From), minTime(next, until))
			if spent <= 0 {
				continue
			}
			key := [2]string{dash(query.Project(span)), day.Format(time.DateOnly)}
			if tallies[key] == nil {
				tallies[key] = &tally{}
			}
			found := tallies[key]
			if command {
				found.busy += spent
				found.intervals = append(found.intervals, [2]time.Time{maxTime(span.Start, day), minTime(span.End, next)})
			} else {
				found.childBusy += spent
			}
		}
	}
	keys := make([][2]string, 0, len(tallies))
	for key := range tallies {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool { return keys[left][0]+keys[left][1] < keys[right][0]+keys[right][1] })
	for _, key := range keys {
		found := tallies[key]
		report.Rows = append(report.Rows, []any{key[0], key[1], found.commands, query.Seconds(found.busy),
			query.Seconds(found.childBusy), mostAtOnce(found.intervals)})
	}
	return report
}

func mostAtOnce(intervals [][2]time.Time) int {
	type edge struct {
		at    time.Time
		delta int
	}
	edges := make([]edge, 0, len(intervals)*2)
	for _, interval := range intervals {
		edges = append(edges, edge{interval[0], 1}, edge{interval[1], -1})
	}
	sort.Slice(edges, func(left, right int) bool {
		if !edges[left].at.Equal(edges[right].at) {
			return edges[left].at.Before(edges[right].at)
		}
		return edges[left].delta < edges[right].delta
	})
	most, now := 0, 0
	for _, found := range edges {
		now += found.delta
		most = max(most, now)
	}
	return most
}

func dayOf(moment time.Time) time.Time {
	year, month, day := moment.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, moment.Location())
}

func maxTime(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}

func minTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func dash(text string) string {
	if text == "" {
		return "-"
	}
	return text
}

func short(text string, most int) string {
	if len(text) > most {
		return text[:most]
	}
	return text
}

func itoa(number int64) string { return strconv.FormatInt(number, 10) }
