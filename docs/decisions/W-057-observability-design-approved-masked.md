# W-057 — Command text is recorded `masked` by default; `skeleton` is a setting

Date: 2026-11-17.

## Context

The telemetry journal ([W-056](W-056-observability-otel-traces-local.md)) records the commands agents run.
Command text can carry secrets and private content, but without it the telemetry journal answers few
questions. No classifier of arbitrary shell text can keep exactly what is safe.

## Decision

- `telemetry.commands` defaults to `masked`: secrets are always masked, content in known positions
  (such as a commit message body) is removed, and the rest of the typed text is kept, as an agent's
  transcript already keeps it.
- `skeleton` is a setting in `jackstay.json`: it keeps only each program's base name, its
  subcommand and the shape of its arguments.

## Consequences

- A project with stricter needs sets `skeleton`.
- See [observability](../design/observability.md) section 7.
