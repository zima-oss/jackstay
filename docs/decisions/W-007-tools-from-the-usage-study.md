# W-007 — Tools from a study of agents' commands: zsh globs, `jst wait`, the test summary, `jst sync` and `jst start`

Date: 2026-11-15.

## Context

A study of the commands agents run most often showed repeated failures and hand-written patterns
that a tool could replace. The most common failure was an unquoted glob in zsh (such as
`--include=*.swift`): zsh aborts with `no matches found`, so the command never runs.

## Decision

- **The guard catches zsh globs.** A word zsh would treat as a glob is reported with the same
  command, the word quoted. The rule `zsh-glob` now warns by default rather than refusing
  ([W-053](W-053-guard-rules-configured-per-project.md)).
- **`jst wait`** waits on one thing (a background task's output, a process, a file, a line in a log)
  until a deadline, with the same exit codes as `jst gh`.
- **`jst test`** ends with a short summary, and `jst test last` prints the last run's summary
  without running anything.
- **`jst sync`** brings a branch up to date with the default branch, whatever the remote is called,
  and stops at a conflict; it merges only ([W-019](W-019-repeated-findings-mean-redesign.md)).
  **`jst start NAME`** makes a worktree on a fresh default branch, the counterpart of `jst tidy`.

## Consequences

- Agents read `jst test last` instead of rerunning a suite to see a failure.
- See [the specification](../design/implementation.md) sections 5.3 and 8.5, the guide's
  [tests](../guide/tests.md) and [guard](../guide/guard.md).
