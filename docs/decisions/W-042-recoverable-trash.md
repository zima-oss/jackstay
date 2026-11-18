# W-042 — `jst trash`: a recoverable delete that works inside the sandbox, named by the guard's advice

Date: 2026-11-16.

## Context

The guard refuses `rm -r` and used to advise moving files to the system trash, which an agent in
Claude Code's sandbox cannot write to. Following the advice was impossible, so the agent was stuck.

## Decision

- `jst trash <path>` moves a file or folder into `~/.jackstay/.trash/`, which the sandbox can
  write, and records it so that `jst trash restore` puts it back.
- Entries older than `trash.keep_days` are removed (default 7 days,
  [W-043](W-043-guard-go-design-approved-trash-seven-days.md)).
- The guard's advice for a refused recursive delete names `jst trash`.

## Consequences

- Every refusal of a recursive delete has a way through that loses nothing.
- See [the guard's design](../design/guard.md) section 10 and the guide's [guard](../guide/guard.md).
