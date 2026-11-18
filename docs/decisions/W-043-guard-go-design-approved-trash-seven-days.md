# W-043 — `jst trash` keeps entries 7 days by default

Date: 2026-11-16.

## Context

`jst trash` ([W-042](W-042-recoverable-trash.md)) keeps what it moves so it can be
restored, but it must not grow without bound. A retention period had to be chosen.

## Decision

- `trash.keep_days` in `jackstay.json` defaults to 7. Entries older than that are removed.

## Consequences

- A deleted file can be restored for a week by default; a project may keep entries longer or
  shorter.
- See [the guard's design](../design/guard.md) section 10.
