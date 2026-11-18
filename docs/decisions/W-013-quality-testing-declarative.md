# W-013 — No time-sensitive tests; everything jackstay adds is declarative and general

Date: 2026-11-15.

## Context

Tests that depend on wall-clock deadlines, short waits or machine load fail at random on a busy
machine. And features built for particular projects risk tying the tool to them.

## Decision

- A test whose outcome depends on timing is a defect, to be fixed, not shielded. jackstay adds no
  feature whose purpose is to protect such tests, and its own tests do not depend on time.
- Everything jackstay adds is general and declared in `jackstay.json`: a project's particulars
  (its vetoes, leftover locations, steps, scope commands) are data, never code paths named after a
  project. Any project must be able to use any feature.

## Consequences

- Contributors do not add project-specific branches to the code; a new need becomes a setting.
- See [testing](../design/testing.md).
