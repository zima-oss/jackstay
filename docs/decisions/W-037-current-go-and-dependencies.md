# W-037 — The latest stable Go and dependencies, checked at their source and recorded; the Go practices jackstay follows

Date: 2026-11-16.

## Context

A tool that guards and runs other projects' work must stay current and be built with well-known Go
practices, not invented ones.

## Decision

- jackstay is built with the latest stable Go release (the `toolchain` line of `go.mod`) and the
  latest stable version of every dependency and tool, each checked at its source when added or
  updated. The versions and the date they were checked are recorded in the specification,
  section 10.
- GitHub Actions steps use the current release of each action, pinned by its full commit hash.
- Practices: a `cmd/` + `internal/` layout; golangci-lint v2; the checks as `just` recipes and
  `jst test` scopes (fmt, vet, lint, race tests, govulncheck, gosec); `log/slog`; version and
  commit injected at build with a fallback to the build info; strict JSON; interfaces declared
  where they are used, with hand-written fakes; a LIFO cleanup model; signal handling in phases;
  atomic file writes.

## Consequences

- A contributor who adds or updates a dependency records its version and the date it was checked.
- See [the specification](../design/implementation.md) section 10.
