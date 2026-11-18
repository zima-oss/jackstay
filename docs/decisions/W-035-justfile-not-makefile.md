# W-035 — jackstay's developer recipes live in a justfile, never a Makefile

Date: 2026-11-16.

## Context

jackstay's own development needs recipes for building, linting, vetting, race tests, vulnerability
checks and a release dry run.

## Decision

- jackstay's developer recipes live in a `justfile`; there is no Makefile.
- The `jst test` scopes in jackstay's own `jackstay.json` call the `just` recipes where one exists.
- This is a rule for jackstay's repository only: jackstay itself does not care which runner a
  project uses ([W-059](W-059-runner-agnostic-core-and-profiles.md)).

## Consequences

- Contributors install `just` and run `just gate` before opening a pull request.
