# W-012 — One vocabulary of test scopes, run as `jst test <scope>`

Date: 2026-11-15.

## Context

Projects run tests at different breadths for different needs, each with its own names and scripts.
An agent moving between projects had to learn each one's conventions.

## Decision

- jackstay defines one vocabulary of scopes: `focused`, `changed`, `quick`, `full`, `display` and
  `gate`. They are run as `jst test <scope>`.
- Each project maps the names to its own commands in `jackstay.json`. Test selection (which tests a
  change affects) stays in the project; jackstay does not choose tests itself.

## Consequences

- An agent knows what `jst test gate` or `jst test changed` means in any project that adopts
  jackstay.
- A project keeps full control over how each scope runs.
- See [testing](../design/testing.md) and the guide's [tests](../guide/tests.md).
