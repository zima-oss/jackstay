# W-032 — `jst release …` belongs to the release features; a hold is undone by `unhold`

Date: 2026-11-16.

## Context

The word "release" had two meanings in the command set: undoing a hold, and the release features.
One command name cannot mean both.

## Decision

- `jst release …` names only the release features (`jst release prepare`, `notes`, `override` and
  the rest).
- If holds are built, a hold is undone by `unhold`, never by `release`. Holds are not built yet.

## Consequences

- See [release](../design/release.md).
