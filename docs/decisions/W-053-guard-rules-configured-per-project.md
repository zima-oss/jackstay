# W-053 — The guard's rules are configured per project; `zsh-glob` warns by default

Date: 2026-11-17.

## Context

Measurements showed that most of the guard's refusals came from one rule, `zsh-glob`, and that a
fixed set of refusals limited agents more than it protected them.

## Decision

- A `guard` section in `jackstay.json` sets each rule's level: `refuse`, `warn` (the command runs
  and the agent is told what is wrong) or `off`, with path exceptions where a rule deletes paths.
  Unknown rule ids and keys are refused, as everywhere in `jackstay.json`.
- The guard keeps reading the parsed command; it does not use prefix allow/deny lists.
- Defaults: `zsh-glob` warns; the destructive rules refuse. A project may loosen them for its own
  paths.

## Consequences

- See [W-055](W-055-every-guard-rule-configurable-now.md) for which rules take a level, the
  [guard's design](../design/guard.md) and the guide's [guard](../guide/guard.md).
