# W-107 — Step kinds: a test scope counts tests, a check scope passes on exit 0

Date: 2026-11-23.

## Context

A gate whose steps were judged only by their exit status printed `outcome=passed`, while the task
graph's design ranked `exited-0` (a command that ended 0 without counting tests) before `passed`
and called it not a pass. Linters, audits and builds have no tests to count, yet a gate needs them;
a test runner that counts nothing should not pass. Bazel tells test targets from build targets, and
Nx names its targets, for the same reason.

## Decision

- A scope with `run` may declare `kind`: `test` or `check`.
- A `test` scope must count tests to pass; its judge cannot be `exit`.
- A `check` scope (a linter, an audit, a build) passes on exit 0: it is judged by its status, and
  its judge can only be `exit`.
- A gate or composite passes when every `test` scope passed and every `check` scope exited 0.
- A scope that declares no `kind` and counts no tests stays `exited-0`: not evidence, as before,
  and so is a run that contains it. `jst test ci` fails on it, since a required job is green only
  on a pass.

## Consequences

- Projects mark their exit-judged linters and builds `"kind": "check"`; until they do, a gate that
  runs them ends `exited-0`. `kind` needs `"jackstay_schema": 3`.
- See [land.md](../design/land.md) sections 7.1 and 7.2 and [the tests guide](../guide/tests.md).
