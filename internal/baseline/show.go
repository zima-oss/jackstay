package baseline

import (
	"fmt"
	"io"
	"strings"
)

func orDash[Value any](value *Value) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprint(*value)
}

func counts(values map[string]int) string {
	var parts []string
	for _, name := range sortedNames(values) {
		parts = append(parts, fmt.Sprintf("%s %d", name, values[name]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// show writes the report for a person.
func show(report Report, out io.Writer) {
	head, merges, fates := report.Headline, report.Merges, report.BashCallsByFate
	fmt.Fprintf(out, "%s %s..%s (UTC), definition: %s\n", report.Project, report.Since, report.Until, report.Definition)
	fmt.Fprintf(out, "  %d Bash calls: %d refused by a hook, %d denied by another layer, %d never answered\n",
		report.BashCalls, fates[fateRefused], fates[fateDenied], fates[fateUnanswered])
	fmt.Fprintf(out, "  on %s: %s pull requests, %s commits, %s tags; one merge is a %s\n", merges.Branch, orDash(merges.PullRequests),
		orDash(merges.Commits), orDash(merges.Tags), head.Per)
	fmt.Fprintln(out, "  by kind: attempts / refused / denied / started; started per merge; transcript wall time (any wait included)")
	for _, kind := range sortedNames(report.Kinds) {
		summary := report.Kinds[kind]
		mark := ""
		if summary.Gate != nil && *summary.Gate {
			mark = " [gate]"
		}
		fmt.Fprintf(out, "    %s%s: %d / %d / %d / %d; %s per merge; %d timed, median %s s, p90 %s s, %.1f h; %d ended in failure\n",
			kind, mark, summary.Attempts, summary.Refused, summary.Denied, summary.Started, orDash(summary.StartedPerMerge),
			summary.Timed, orDash(summary.WallMedian), orDash(summary.WallP90), float64(summary.WallHours), summary.Failed)
	}
	gates := strings.Join(head.Gates, ", ")
	if gates == "" {
		gates = "no gate kind"
	}
	fmt.Fprintf(out, "  gate runs started (%s): %d, %s per %s; every listed kind: %d, %s per %s\n", gates, head.GateRunsStarted,
		orDash(head.GateRunsStartedPerMerge), head.Per, head.ListedRunsStarted, orDash(head.ListedRunsStartedPerMerge), head.Per)
	fmt.Fprintln(out, "  per day: merges; started by kind (median s); destructive ran/refused/denied; gate runs overlapped of timed")
	for _, name := range sortedNames(report.Days) {
		current := report.Days[name]
		var kinds, overlaps []string
		for _, kind := range sortedNames(current.Kinds) {
			kinds = append(kinds, fmt.Sprintf("%s %d (%s)", kind, current.Kinds[kind].Started, orDash(current.Kinds[kind].WallMedian)))
		}
		for _, kind := range sortedNames(current.Overlap) {
			overlaps = append(overlaps, fmt.Sprintf("%s %d of %d", kind, current.Overlap[kind].Overlapped, current.Overlap[kind].Timed))
		}
		fmt.Fprintf(out, "    %s: %d; %s; %d/%d/%d; %s\n", name, current.Merges, orNone(kinds), current.Destructive.Ran,
			current.Destructive.Refused, current.Destructive.Denied, orNone(overlaps))
	}
	showLog(report.JackstayLog, out)
	fmt.Fprintf(out, "  overlap of wall time between gate runs of %s (not proof that two suites executed at once):\n",
		strings.Join(report.Overlap.With, ", "))
	for _, kind := range sortedNames(report.Overlap.Kinds) {
		found := report.Overlap.Kinds[kind]
		fmt.Fprintf(out, "    %s: %d of %d timed runs overlapped (%d with the same project, %d with another); median %s s overlapped, %s s not\n",
			kind, found.Overlapped, found.Timed, found.OverlappedSame, found.OverlappedOther, orDash(found.MedianOverlapped),
			orDash(found.MedianNotOverlapped))
	}
	harm := report.Destructive
	fmt.Fprintf(out, "  destructive by today's guard: ran %d (%s); refused by a hook %d (%s); denied by another layer %d (%s)\n",
		harm.RanTotal, counts(harm.Ran), harm.RefusedTotal, counts(harm.Refused), harm.DeniedTotal, counts(harm.Denied))
}

func orNone(parts []string) string {
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func showLog(log LogReport, out io.Writer) {
	if log.Records == 0 {
		fmt.Fprintf(out, "  jst log: no record of this project in the span (%v); the run alone and executed-or-reused are not known\n", log.Path)
		return
	}
	fmt.Fprintf(out, "  jst log: %d runs executed (%s), %d reused (nothing ran), %d would-hit, %d gave up; the run alone: median %s s; "+
		"%d executed while another recorded run did\n", log.Executed, counts(log.ByOutcome), log.Reused, log.WouldHit, log.GaveUp,
		orDash(log.RunAloneMedian), log.ExecutedOverlapped)
	for _, how := range []struct{ key, words string }{{besideOther, "beside another project's recorded run"},
		{besideSame, "beside another run of this project"}, {besideNone, "alone"}} {
		part := log.Split[how.key]
		fmt.Fprintf(out, "    %s: %d runs, %d failed, unjudged or stopped, of which the unchanged tree then passed %d\n",
			how.words, part.Runs, part.NotWell, part.ThenPassedUnchanged)
	}
	if log.LockEvidence.Shown {
		fmt.Fprintln(out, "    W-005, a problem from simultaneous runs: SHOWN by this log; propose the lock to the owner")
	} else {
		fmt.Fprintf(out, "    W-005, a problem from simultaneous runs: not shown (%s)\n", strings.Join(log.LockEvidence.Explained, "; "))
	}
	if log.Unfinished > 0 {
		fmt.Fprintf(out, "    %d runs started and left no end: killed outright, or still going\n", log.Unfinished)
	}
	for _, name := range sortedNames(log.Kinds) {
		kind := log.Kinds[name]
		fmt.Fprintf(out, "    %s: %d executed (%d passed), %d reused, %d would-hit, the run alone: median %s s\n",
			name, kind.Executed, kind.Passed, kind.Reused, kind.WouldHit, orDash(kind.RunAloneMedian))
	}
	for _, name := range sortedNames(log.Scopes) {
		scope := log.Scopes[name]
		fmt.Fprintf(out, "    scope %s: %d executed (%d passed, %d failed), median %s s\n", name, scope.Executed, scope.Passed, scope.Failed,
			orDash(scope.Median))
	}
}
