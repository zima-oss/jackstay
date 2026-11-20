package stats

// help is `jst stats --help`.
const help = usageText + `

Computed from jackstay's journal when asked. A span counts at the moment it ended; its time is
clipped to the range. Without REPORT, the list of reports:
  guard        calls by outcome, answer latency by version, failures with caller and stack
  refusals     per rule: refusals, share, retried within 2 minutes, given up
  flaky        test runs of one key and head with both a pass and a failure
  tests        per project: runs by outcome, time in failed and stopped runs
  phases       each phase's and lock wait's count, median, p95, max
  reviews      per task: rounds, attempts, Codex runs and failures, tokens, minutes
  background   jst bg starts and ends; background asked for outside jst bg
  sessions     per Claude session: jackstay's time, refusals; --claude adds the session's tokens and cost
  load         per project and day: busy time, the most commands at once
  --since DUR|TIME   the start of the range (default 30d); --until TIME: its end
  --project P        that project's; --all-projects: every project's
  --session ID       one Claude session's
  --by WHAT          group by version, project or day
  --json             as JSON
  --claude           with sessions: add the session's tokens and cost

exit: 0 an answer; 1 no journal could be read; 2 the request is wrong.

example: jst stats refusals --by project --since 7d
`
