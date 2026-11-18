# W-036 — Go written natively from the specification; RE2 patterns; logs as a field schema; a real shell parser

Date: 2026-11-16.

## Context

jackstay was first written in Python and then rewritten in Go. A port that follows the old code
function by function carries over the old language's habits and emulations instead of using Go's
strengths.

## Decision

- **The specification is the measure** (`docs/design/`), not an earlier implementation's behaviour.
  Each command is designed anew from its spec and written the Go way.
- **No emulation** of another language's string, JSON or regular-expression behaviour.
- **Regular expressions in `jackstay.json` are RE2**, Go's dialect.
- **Logs and state files are a schema of fields**, not byte-exact formats.
- **The guard parses commands with a shell parser library** (`mvdan.cc/sh/v3/syntax`) producing a
  syntax tree, with small checks of its own for what only zsh does; rules are data; one model of
  git's options. A command that cannot be parsed is refused
  ([W-039](W-039-unparsable-command-refused.md)).

## Consequences

- A behaviour question is answered by the spec; when code and spec disagree, one of them is fixed.
- See [the specification](../design/implementation.md) and [the guard's design](../design/guard.md).
