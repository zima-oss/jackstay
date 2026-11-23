# W-107 — `jst trash` and `jst discard` delete and log by default; keeping is opt-in

Date: 2026-11-23.

## Context

`jst trash` ([W-042](W-042-recoverable-trash.md)) and `jst discard`
([W-052](W-052-guard-dead-end-and-clean-copy-gate.md)) kept every item they removed as an entry
that `jst trash restore` brings back, for `trash.keep_days` ([W-043](W-043-guard-go-design-approved-trash-seven-days.md)).
Keeping copies has costs of its own: the trash grows with whatever sessions delete, a cross-volume
move copies the whole item, and the held and pruned entries draw corner cases. What a deletion
most needs is a record of what went, and when, and by which session.

## Decision

- A new setting, `trash.keep`, takes `off` (the default) or `on`.
- `off`: `jst trash` and `jst discard` delete at once, like a delete that skips the system's Trash,
  and log each deletion: its path, kind, size, SHA-256, project, session and command.
  `jst trash list` shows the log; `jst trash restore` works only for what was kept.
- `on`: they keep a copy for `trash.keep_days`, as before; this mode is experimental and is marked
  so in the help and the documentation.
- The commands, and the guard's advice that names them, stay as they are.

## Consequences

- By default nothing that `jst trash` or `jst discard` removes can be restored; the log says what it
  was. A project that wants copies sets `"trash": {"keep": "on"}`, which needs
  `"jackstay_schema": 3`.
- See [the guard's design](../design/guard.md) section 10 and
  [the guard guide](../guide/guard.md#delete-with-a-log-jst-trash).
