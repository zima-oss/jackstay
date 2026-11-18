# W-017 — The guard protects against accidents: cheap deliberate bypasses are closed, the rest are known limits

Date: 2026-11-15.

## Context

A review found ways to get a destructive command past the guard on purpose. The guard runs as the
same user as the agent, so it cannot be a boundary against an adversary; the question was how far
to go.

## Decision

- The guard is a guard against accidents, not against an adversary.
- Deliberate bypasses that one rule closes are closed: `env -S`, `git -c alias.*` (and `-c` of
  options that switch protections off, such as `clean.requireForce`), `grm`/`gxargs`/`gfind`,
  `busybox <applet>`, `git submodule foreach`, `$(echo|printf <constant>)`.
- What a rule cannot close cheaply is written down as a known limit: substitutions through `$@`
  after `set --`, functions' parameters, arrays built elsewhere, `printf -v`, `read`, and forged
  receipts or marks in `~/.jackstay`.
- Code review of jackstay follows the same threat model.

## Consequences

- Users should not rely on the guard against a hostile process.
- A reported bypass is fixed when one rule closes it; otherwise it is documented as a limit.
- See [the guard's design](../design/guard.md) and the guide's [guard](../guide/guard.md).
