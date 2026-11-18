# W-069 — jackstay supports Linux: the process table is read from /proc, the gate passes on ubuntu-latest

Date: 2026-11-17.

## Context

jackstay read the process table only on macOS, so on Linux every `jst test` ended unjudged, and a
few tests assumed macOS paths and timings. The gate's first run on `ubuntu-latest` failed.

## Decision

- jackstay supports macOS and Linux.
- On Linux the process table is read from `/proc` (parent, process group, session ID, effective user, start
  time, name, zombie state).
- The gate passes on `ubuntu-latest`, where CI runs it on every pull request and push to `main`,
  and on macOS, where the maintainers run `just gate` before a release (it also checks the
  macOS-only code).

## Consequences

- Contributors keep tests free of platform-specific paths and check both platforms in CI.
- See [the specification](../design/implementation.md) section 5 and the guide's
  [install](../guide/install.md).
