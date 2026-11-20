# W-092 — A thin `jst land`; where tests run is the project's declared split between local and CI

Date: 2026-11-20.

## Context

The landing design for 0.6.0 grew a local gate on the merge tree, landing-evidence records, review
carry-over, status publishing and provenance checks, and two review rounds kept finding gaps in
them (W-019). The local queue was first meant to run fewer tests while several projects were worked
on at once; that need is gone. Tests are still worth running locally, while CI is the gate that
catches a change sent without them. Not every project can run every test in CI: jackstay has no
macOS runner, so its macOS code is tested only on a Mac.

## Decision

- **`jst land` is thin**: push, open the change request, wait for the checks the host's branch
  rules require, merge with the expected head. The host's native merge queue is used where the base
  requires one.
- **The hosting adapter** is a handful of operations: open, read checks, read rules, update the
  branch, merge.
- **Human-only paths** are kept by `jst lint pr` (rule D0), by `jst land` reading them from the base
  and the change, and by the guard refusing `gh pr merge`/`glab mr merge`.
- **Dropped**: the local gate on the merge tree, landing-evidence records and the reconcile after a
  merge, review carry-over, publishing statuses to the host, provenance in landing (it stays in
  release and install), and jackstay's own queue. The batching queue is not built.
- **Where tests run is declared per project.** A test scope names the systems CI runs it on. Before
  a pull request, `jst test changed` requires the selected scopes CI does not cover; CI runs the
  covered ones as required checks; before an install or a tag, every scope CI does not cover on this
  system passes locally on that commit. The AGENTS block states the split.

## Consequences

See the [landing design](../design/land.md). For jackstay itself the full gate still runs on a Mac
before an install and a release ([W-083](W-083-no-macos-job-in-ci.md)).
