package baseline

import (
	"fmt"
	"math"
	"strconv"

	"github.com/zima-oss/jackstay/internal/receipts"
)

// repassWindow is how soon after a run that did not end well the same key must pass for the
// failure to count as not the tree's.
const repassWindow = 2 * 3600.0

// Situations a recorded run can be in (docs/design/implementation.md section 9.6).
const (
	besideOther = "other" // another project's recorded run shared at least shortestRun seconds with it
	besideSame  = "same"  // only another run of the same project did
	besideNone  = "alone"
)

// LogKind is one kind's executed, reused and would-hit records.
type LogKind struct {
	Executed       int   `json:"executed"`
	Passed         int   `json:"passed"`
	Reused         int   `json:"reused"`
	RunAloneHours  Ratio `json:"run_alone_hours"`
	RunAloneMedian *int  `json:"run_alone_median_s"`
	WouldHit       int   `json:"would_hit"`
}

// LogScope is one `jst test <scope>`'s runs.
type LogScope struct {
	Executed int  `json:"executed"`
	Failed   int  `json:"failed"`
	Median   *int `json:"median_s"`
	Passed   int  `json:"passed"`
}

// Situation is the runs of one situation and how many did not end well.
type Situation struct {
	NotWell             int    `json:"not_well"`
	RateThenPassed      *Ratio `json:"rate_then_passed"`
	Runs                int    `json:"runs"`
	ThenPassedUnchanged int    `json:"then_passed_unchanged"`
}

// Evidence is W-005's question, "is a problem from simultaneous runs seen?": shown only when
// every condition holds; why_not names each that does not (as identifiers), explained says it.
type Evidence struct {
	Explained []string `json:"explained"`
	Shown     bool     `json:"shown"`
	WhyNot    []string `json:"why_not"`
}

// Conditions of the evidence, as why_not names them.
const (
	whySpan      = "span-under-7-days"
	whyDays      = "runs-on-under-5-days"
	whyRuns      = "under-50-runs"
	whyAlone     = "under-20-alone"
	whyFewBeside = "under-5-repassed-beside"
	whyRate      = "rate-not-3-times-alone"
	minimumDays  = 5
	minimumRuns  = 50
	minimumAlone = 20
	minimumBad   = 5
	rateFactor   = 3
)

// LogReport is the project's records in jackstay's run log for the span.
type LogReport struct {
	ByOutcome          map[string]int       `json:"by_outcome"`
	Executed           int                  `json:"executed"`
	ExecutedOverlapped int                  `json:"executed_overlapped"`
	GaveUp             int                  `json:"gave_up"`
	Kinds              map[string]LogKind   `json:"kinds"`
	LockEvidence       Evidence             `json:"lock_evidence"`
	Path               any                  `json:"path"`
	Records            int                  `json:"records"`
	Reused             int                  `json:"reused"`
	RunAloneMedian     *int                 `json:"run_alone_median_s"`
	Scopes             map[string]LogScope  `json:"scopes"`
	Split              map[string]Situation `json:"split"`
	Unfinished         int                  `json:"unfinished"`
	WouldHit           int                  `json:"would_hit"`
}

// An executedRun is a run that actually executed.
type executedRun struct {
	begin, end float64
	project    string
	outcome    string
	key        string
	treeKept   bool // false when the run itself said its tree changed while it ran
}

// outcomeText is a record's outcome as the report names it ("None" when it has none).
func outcomeText(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	}
	return fmt.Sprint(value)
}

// commandWords is a record's command when it is a list of texts.
func commandWords(record receipts.Record) []string {
	list, isList := record["command"].([]any)
	if !isList {
		return nil
	}
	words := make([]string, 0, len(list))
	for _, item := range list {
		text, isText := item.(string)
		if !isText {
			return nil
		}
		words = append(words, text)
	}
	return words
}

// readLog is jackstay's run log for the project in the span: what executed and how it ended, how long
// a run alone took, what was reused, and the split W-005 asks for. Only runs that went through
// `jst test` are here, so "alone" means "no other RECORDED run was going".
func readLog(paths []string, given bool, spec Definition, span Span) LogReport {
	report := LogReport{ByOutcome: map[string]int{}, Kinds: map[string]LogKind{}, Scopes: map[string]LogScope{}, Path: paths}
	if given && len(paths) == 1 {
		report.Path = paths[0]
	}
	type kindTally struct {
		LogKind
		seconds []float64
	}
	type scopeTally struct {
		LogScope
		seconds []float64
	}
	perKind, perScope := map[string]*kindTally{}, map[string]*scopeTally{}
	var alone []float64
	var everyone, mine []executedRun
	type startedRun struct {
		project string
		moment  float64
	}
	started, startedOrder, finished := map[string]startedRun{}, []string{}, map[string]bool{}
	for _, record := range receipts.ReadRecords(paths) {
		moment, timed := record.Number("epoch")
		event := record.Text("event")
		if !timed || record.Inside() {
			continue // a nested `jst test` is part of the run it is inside: counted once
		}
		took, hasTook := record.Number("seconds")
		begin, hasBegin := record.Number("started_epoch")
		end, hasEnd := record.Number("ended_epoch")
		if !hasEnd || end == 0 {
			end = moment
		}
		if !hasBegin && hasTook {
			begin, hasBegin = end-took, true
		}
		runID := record.Text("run_id")
		if event == receipts.EventStart && runID != "" {
			if _, seen := started[runID]; !seen {
				startedOrder = append(startedOrder, runID)
			}
			started[runID] = startedRun{project: record.Text("project"), moment: moment}
		}
		if event == receipts.EventRun && runID != "" {
			finished[runID] = true
		}
		outcome := outcomeText(record["outcome"])
		executed := event == receipts.EventRun && hasBegin && outcome != "gave-up" && outcome != "stopped-waiting" && outcome != "not-started"
		if executed {
			everyone = append(everyone, executedRun{begin: begin, end: end, project: projectOf(record)})
		}
		if projectOf(record) != spec.Project || !span.holds(moment) ||
			(event != receipts.EventRun && event != receipts.EventReused && event != receipts.EventWouldHit) {
			continue
		}
		report.Records++
		kind := kindOfCommands(spec.Kinds, [][]string{commandWords(record)})
		if kind == "" {
			kind = "(no kind)"
		}
		tally := perKind[kind]
		if tally == nil {
			tally = &kindTally{}
			perKind[kind] = tally
		}
		switch {
		case event == receipts.EventReused:
			report.Reused++
			tally.Reused++
		case event == receipts.EventWouldHit:
			report.WouldHit++
			tally.WouldHit++
		case executed:
			if scopeName := record.Text("scope"); scopeName != "" {
				// `jst test <scope>`: how often each scope runs, how long, how often it fails.
				scope := perScope[scopeName]
				if scope == nil {
					scope = &scopeTally{}
					perScope[scopeName] = scope
				}
				scope.Executed++
				scope.Passed += countIf(outcome == "passed")
				scope.Failed += countIf(outcome == "failed")
				if hasTook && outcome != "stopped" {
					scope.seconds = append(scope.seconds, took)
				}
			}
			report.Executed++
			report.ByOutcome[outcome]++
			tally.Executed++
			tally.Passed += countIf(outcome == "passed")
			if hasTook && outcome != "stopped" {
				tally.seconds = append(tally.seconds, took)
				alone = append(alone, took)
			}
			notStored := receipts.NotStoredKind(record)
			mine = append(mine, executedRun{begin: begin, end: end, outcome: outcome, key: record.Text("key"),
				treeKept: notStored != receipts.NotStoredTreeChanged && notStored != receipts.NotStoredInputRewritten})
		default: // the lock was not had, or the run was stopped while it waited: nothing ran
			report.GaveUp++
		}
	}
	for _, runID := range startedOrder {
		if begun := started[runID]; !finished[runID] && begun.project == spec.Project && span.holds(begun.moment) {
			report.Unfinished++
		}
	}
	report.RunAloneMedian = median(alone)
	for name, scope := range perScope {
		scope.Median = median(scope.seconds)
		report.Scopes[name] = scope.LogScope
	}
	for name, tally := range perKind {
		tally.RunAloneMedian, tally.RunAloneHours = median(tally.seconds), hours(tally.seconds)
		report.Kinds[name] = tally.LogKind
	}

	situations := map[string]*Situation{besideOther: {}, besideSame: {}, besideNone: {}}
	days := map[int64]bool{}
	for position, run := range mine {
		days[int64(math.Floor(run.begin/day))] = true
		how := beside(run, everyone, spec.Project)
		if how != besideNone {
			report.ExecutedOverlapped++
		}
		situation := situations[how]
		situation.Runs++
		if run.outcome != "failed" && run.outcome != "unjudged" && run.outcome != "stopped" {
			continue
		}
		situation.NotWell++
		if run.key == "" {
			continue // a run without a key can never be called unchanged
		}
		for _, later := range mine[position+1:] {
			if later.key == run.key && later.outcome == "passed" && later.begin >= run.end && later.end-run.end <= repassWindow && later.treeKept {
				situation.ThenPassedUnchanged++
				break
			}
		}
	}
	report.Split = map[string]Situation{}
	for how, situation := range situations {
		if situation.Runs > 0 {
			rate := rounded(float64(situation.ThenPassedUnchanged)/float64(situation.Runs), 3)
			situation.RateThenPassed = &rate
		}
		report.Split[how] = *situation
	}
	report.LockEvidence = lockEvidence(report.Split, span, len(days))
	return report
}

func countIf(condition bool) int {
	if condition {
		return 1
	}
	return 0
}

// projectOf is a record's project ("\x00" when it names none, so that it matches no project).
func projectOf(record receipts.Record) string {
	if name, isText := record["project"].(string); isText {
		return name
	}
	return "\x00"
}

// beside is the situation of a run: "other" when another project's recorded run shared at least
// shortestRun seconds with it, else "same" when another run of this project did, else "alone".
func beside(run executedRun, everyone []executedRun, project string) string {
	found, seenSelf := besideNone, false
	for _, peer := range everyone {
		if min(run.end, peer.end)-max(run.begin, peer.begin) < shortestRun {
			continue
		}
		if peer.project != project {
			return besideOther
		}
		if peer.begin == run.begin && peer.end == run.end && !seenSelf {
			seenSelf = true // the run itself
			continue
		}
		found = besideSame
	}
	return found
}

// lockEvidence says whether the log shows a problem from simultaneous runs (W-005): a proposal to
// the owner, never a switch. It shows an association, not a cause.
func lockEvidence(split map[string]Situation, span Span, days int) Evidence {
	besideRuns := split[besideOther].Runs + split[besideSame].Runs
	besideRepassed := split[besideOther].ThenPassedUnchanged + split[besideSame].ThenPassedUnchanged
	alone := split[besideNone]
	runs := besideRuns + alone.Runs
	rateBeside, rateAlone := 0.0, 0.0
	if besideRuns > 0 {
		rateBeside = float64(besideRepassed) / float64(besideRuns)
	}
	if alone.Runs > 0 {
		rateAlone = float64(alone.ThenPassedUnchanged) / float64(alone.Runs)
	}
	evidence := Evidence{WhyNot: []string{}, Explained: []string{}}
	add := func(id, text string) {
		evidence.WhyNot = append(evidence.WhyNot, id)
		evidence.Explained = append(evidence.Explained, text)
	}
	if span.High-span.Low < 7*day-1 {
		add(whySpan, "the span measured is shorter than 7 days")
	}
	if days < minimumDays {
		add(whyDays, fmt.Sprintf("the recorded runs fall on %d days; 5 are needed", days))
	}
	if runs < minimumRuns {
		add(whyRuns, fmt.Sprintf("%d recorded runs; 50 are needed", runs))
	}
	if alone.Runs < minimumAlone {
		add(whyAlone, fmt.Sprintf("%d runs went alone; 20 are needed to compare with", alone.Runs))
	}
	if besideRepassed < minimumBad {
		add(whyFewBeside, fmt.Sprintf("%d runs beside another failed and then passed unchanged; 5 are needed", besideRepassed))
	}
	if rateBeside < rateFactor*rateAlone || rateBeside == 0 {
		add(whyRate, fmt.Sprintf("beside another run %.3f of runs failed and then passed unchanged, alone %.3f: not three times as many",
			rateBeside, rateAlone))
	}
	evidence.Shown = len(evidence.WhyNot) == 0
	return evidence
}
