package logs

// help is `jst log --help`.
const help = usageText + `

What jackstay did, read from its journal (docs/design/observability.md): every jackstay command, the guard's
decisions, test runs, waits for locks, the processes jackstay started, review rounds.
  (no option)        this project's commands of the last day, one line each
  --session ID       a Claude session's (every project, 30 days; $CLAUDE_CODE_SESSION_ID is this one)
  --trace ID         one run's tree: phases, waits, children with exit codes and signals, stacks
  --tool-call ID     the guard's decision on a tool call and the jackstay command it ran
  --command NAME     one command (test, review, guard, ...)
  --outcome WORD     an outcome (refused, failed, ...); error: every span that ended in error
  --since DUR|TIME   2h, 7d, 2026-11-17 or RFC 3339 (default 24h; 30d with --session/--trace)
  --tree             show each run's tree;  --json  one JSON object per span;  --limit N

exit: 0 an answer (an empty one included); 1 no journal could be read; 2 the request is wrong.

example: jst log --session "$CLAUDE_CODE_SESSION_ID" --tree
`
