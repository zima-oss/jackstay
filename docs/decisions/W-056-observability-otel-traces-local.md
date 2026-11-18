# W-056 — Monitoring as OpenTelemetry traces in a local OTLP-JSON telemetry journal; GenAI conventions behind one adapter; `jst log` and `jst stats`

Date: 2026-11-17.

## Context

During development and analysis one needs to find out exactly what happened: where an agent spent
time, which tool call failed, which operations belong together. OpenTelemetry's tracing is stable;
its GenAI semantic conventions are still evolving.

## Decision

- **Stable OpenTelemetry now.** jackstay's telemetry journal is OpenTelemetry traces: a span per command,
  phase and child process; span events for decisions (the guard's, review rounds); status and
  errors. W3C `TRACEPARENT` is given to every child and honoured when inherited.
  Stored locally as OTLP-JSON lines (stable OTLP v1) by a small synchronous exporter, so a
  collector or viewer can read them later. No network, no collector, no metrics SDK.
- **GenAI conventions behind one adapter** with their revision pinned; what has no convention lives
  under `jackstay.*`. Golden tests for five scenarios (a model call, a tool call, a retry, an error,
  a nested agent) check the telemetry and the queries over it on every update.
- **jackstay's own stores stay the source of truth**; telemetry only observes and links to them.
  Losing a span loses no state.
- **Queries:** `jst log` (a timeline, a run's tree, filters) and `jst stats` (named reports
  computed from spans at query time).
- **The guard** records allowed commands briefly and refusals and crashes in full, within about a
  millisecond of its median latency.
- **Privacy and retention:** local only; command text kept with secrets masked
  ([W-057](W-057-observability-design-approved-masked.md)); 180 days, compressed after 14, both
  configurable.

## Consequences

- See [observability](../design/observability.md) and the guide's
  [observability](../guide/observability.md).
