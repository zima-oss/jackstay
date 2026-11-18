# W-051 — `jst review`: Codex review rounds, with effort medium by default

Date: 2026-11-17.

## Context

Projects ran AI code review rounds through their own scripts, each with different limits and no
shared record of rounds and findings.

## Decision

- `jst review` runs Codex review rounds and keeps their ledger.
- Defaults: `effort` medium, `round_limit` 6, a redesign after 2 rounds running with a blocker or
  major finding in one place ([W-019](W-019-repeated-findings-mean-redesign.md)),
  `timeout_minutes` 20, `max_concurrent` 4, `keep_days` 30.
- Settings live in the `review` section of `jackstay.json`; there are no flags standing in for
  configuration.

## Consequences

- A project replaces its own review scripts with `jst review`.
- See [review](../design/review.md) and the guide's [review](../guide/review.md) and
  [Codex](../guide/codex.md) pages.
