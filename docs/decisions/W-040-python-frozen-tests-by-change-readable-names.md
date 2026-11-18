# W-040 — Tests chosen by what changed; a finished step pushed after one run; readable names in jackstay's code

Date: 2026-11-16.

## Context

Running every test for every edit wastes time, and pushing each edit to `main` fills it with
half-finished steps. Single-letter and cryptic names made the code hard to read.

## Decision

- **Tests by what changed:** before a push, run what the change touches: Go code or `go.mod`, the
  Go checks; documentation only, the docs check. `just gate` runs the whole gate.
- **Push in steps:** `main` gets a finished step, checked with one run, not every edit.
- **Readable names:** no single-letter or cryptic identifiers in jackstay's own code; tests may use
  them. Enforced by a linter rule where one exists, and by review.

## Consequences

- Contributors name things for what they hold and expect review to ask for it.
- See [the specification](../design/implementation.md) section 1.
