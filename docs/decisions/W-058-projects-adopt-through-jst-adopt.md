# W-058 — Projects adopt jackstay through an idempotent `jst adopt` and one short prompt; existing workflows are classified

Date: 2026-11-17.

## Context

Bringing a project up to date with jackstay touches its settings, its agent instructions, its hooks
and its CI workflows. Handing an agent a long prose prompt gives a different result in every
project; only a tool is idempotent.

## Decision

- **`jst adopt --plan | --apply`** brings a project to jackstay's reference: the `jackstay.json`
  sections, the AGENTS block between markers (printed by jackstay from the project's settings, only
  the tools it enables), the hooks in `.claude/settings.json`, and jackstay's standard workflows
  from embedded templates with actions pinned by hash. A second run changes nothing; `jst doctor`
  reports drift.
- **One short, unchanging prompt** for an agent: run `jst adopt --plan`, settle the listed
  conflicts, apply, get `jst doctor` clean, open the pull request. The result is accepted by
  `jst doctor`, not by the agent's word.
- **Each existing workflow is classified**: kept, replaced or merged. Project-specific workflows
  stay.

## Consequences

- See [adopt](../design/adopt.md) and the guide's [adopt](../guide/adopt.md).
