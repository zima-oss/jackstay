package logs

// help is `jst log --help`.
const help = usageText + `

Reads jackstay's journal: commands, the guard's decisions, test runs, lock waits, started processes,
review rounds.
  (no option)        this project's commands of the last day, one line each
  --session ID       a Claude session's (every project, 30 days; $CLAUDE_CODE_SESSION_ID is this one)
  --trace ID         one run's tree: phases, waits, children with exit codes and signals, stacks
  --tool-call ID     the guard's decision on a tool call and the jackstay command it ran
  --command NAME     one command (test, review, guard, ...)
  --outcome WORD     an outcome (refused, failed, ...); error: every span that ended in error
  --since DUR|TIME   2h, 7d, 2026-11-17 or RFC 3339 (default 24h; 30d with --session/--trace)
  --until TIME       the end of the range (default now)
  --project P        that project's; --all-projects: every project's
  --tree             show each run's tree
  --json             one JSON object per span
  --limit N          only the newest N

exit: 0 an answer (an empty one included); 1 no journal could be read; 2 the request is wrong.

example: jst log --session "$CLAUDE_CODE_SESSION_ID" --tree
`
