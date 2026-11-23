# W-107 — Trash that deletes and logs, and step kinds

Date: 2026-11-23.

One ruling in 2 parts (trash, step kinds); each part has its own context, decision and
consequences.

## Trash

`jst trash` and `jst discard` delete and log by default; keeping is opt-in.

### Context

`jst trash` ([W-042](W-042-recoverable-trash.md)) and `jst discard`
([W-052](W-052-guard-dead-end-and-clean-copy-gate.md)) kept every item they removed as an entry
that `jst trash restore` brings back, for `trash.keep_days` ([W-043](W-043-guard-go-design-approved-trash-seven-days.md)).
Keeping copies has costs of its own: the trash grows with whatever sessions delete, a cross-volume
move copies the whole item, and the held and pruned entries draw corner cases. What a deletion
most needs is a record of what went, and when, and by which session.

### Decision

- A new setting, `trash.keep`, takes `off` (the default) or `on`.
- `off`: `jst trash` and `jst discard` delete at once, like a delete that skips the system's Trash,
  and log each deletion: its path, kind, size, SHA-256, project, session and command.
  `jst trash list` shows the log; `jst trash restore` works only for what was kept.
- `on`: they keep a copy for `trash.keep_days`, as before; this mode is experimental and is marked
  so in the help and the documentation.
- The commands stay as they are, and the guard's advice and the AGENTS block still name them;
  their words follow the setting, promising an undo only when a copy is kept.

### Consequences

- By default nothing that `jst trash` or `jst discard` removes can be restored; the log says what it
  was. A project that wants copies sets `"trash": {"keep": "on"}`, which needs
  `"jackstay_schema": 3`.
- See [the guard's design](../design/guard.md) section 10 and
  [the guard guide](../guide/guard.md#delete-with-a-log-jst-trash).

## Step kinds

Step kinds: a test scope counts tests, a check scope passes on exit 0.

### Context

A gate whose steps were judged only by their exit status printed `outcome=passed`, while the task
graph's design ranked `exited-0` (a command that ended 0 without counting tests) before `passed`
and called it not a pass. Linters, audits and builds have no tests to count, yet a gate needs them;
a test runner that counts nothing should not pass. Bazel tells test targets from build targets, and
Nx names its targets, for the same reason.

### Decision

- A scope with `run` may declare `kind`: `test` or `check`.
- A `test` scope must count tests to pass; its judge cannot be `exit`.
- A `check` scope (a linter, an audit, a build) passes on exit 0: it is judged by its status, and
  its judge can only be `exit`.
- A gate or composite passes when every `test` scope passed and every `check` scope exited 0.
- A scope that declares no `kind` and counts no tests stays `exited-0`: not evidence, as before,
  and so is a run that contains it. `jst test ci` fails on it, since a required job is green only
  on a pass.

### Consequences

- Projects mark their exit-judged linters and builds `"kind": "check"`; until they do, a gate that
  runs them ends `exited-0`. `kind` needs `"jackstay_schema": 3`.
- See [land.md](../design/land.md) sections 7.1 and 7.2 and [the tests guide](../guide/tests.md).
