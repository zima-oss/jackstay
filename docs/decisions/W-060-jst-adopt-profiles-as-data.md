# W-060 — `jst adopt`: profiles as data, conflicts settled by migration, the AGENTS block as code, drift found by `jst doctor`

Date: 2026-11-17.

## Context

`jst adopt` ([W-058](W-058-projects-adopt-through-jst-adopt.md)) needed a concrete design: what it owns in
a project, what happens when a project's files disagree with jackstay's reference, and how the
agent instructions stay current as tools change.

## Decision

- `jst adopt` owns four places in a project and settles conflicts by migration; a deliberate
  difference is accepted with `--accept` or listed in `adopt.exceptions`.
- Profiles are data ([W-059](W-059-runner-agnostic-core-and-profiles.md)).
- The `card-line` and `release` workflow templates are embedded in the binary; `adopt.pin` pins
  their actions.
- Every command has `jst <command> --help`, so the AGENTS block stays short and points there.
- **The AGENTS block is code**: its source and golden file live in jackstay's repository. A tool
  added or changed means the block's item and golden file change in the same commit.
- `jst doctor`'s `adoption` check reports drift after an upgrade; the project's agent then runs
  the adoption prompt again.

## Consequences

- See [adopt](../design/adopt.md) and the guide's [adopt](../guide/adopt.md).
