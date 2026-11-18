# W-073 — The public tree: decisions in force as short records, the backlog in GitHub Issues, `docs/guide` and `docs/design`

Date: 2026-11-17.

## Context

The public repository must stand on its own: an agent or a new contributor who opens it should know
how to use and change jackstay without any outside material.

## Decision

- **`docs/decisions/`** holds every decision in force about how jackstay is built and works, as a
  short English record under its W number. Superseded decisions and decisions about matters outside
  jackstay are not published. Every W number the code or docs cite has a public record.
- **The backlog** lives in GitHub Issues.
- **`docs/guide/`** says how to use jackstay: installing it, adopting a project, each group of
  commands, and the optional Claude Code and Codex integrations.
- **`docs/design/`** holds one specification of the current implementation and the feature specs,
  for whoever changes the code.
- **`AGENTS.md`** is for contributors.

## Consequences

- A new idea starts as an issue; a decision that follows gets a record here.
- See the guide's [overview](../guide/README.md) and [the specification](../design/implementation.md).
