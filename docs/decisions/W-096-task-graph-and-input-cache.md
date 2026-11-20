# W-096 — Test scopes run as a task graph, cached by their inputs

Date: 2026-11-20.

## Context

W-092 made where tests run a project's declared split between local and CI. Its first
implementation had to merge, deduplicate and order composite scopes (the `steps` lists of a gate)
by hand, and review rounds kept finding the same kind of fault in it: a scope two lists share could
run twice, or run before the scope it needs. Monorepo task runners solved this long ago with a task
graph and a cache keyed by a hash of the task's inputs. CI minutes are limited and cost money, so a
test that runs twice on one commit in CI is waste.

## Decision

- **A task graph.** A scope declares `deps`, the scopes that must pass before it. A run builds the
  graph of the scopes asked for and their deps, refuses a cycle, sorts it topologically in a stable
  order, and runs each scope once, after its deps. A composite such as `gate` is an ordinary scope
  with deps and no command.
- **A cache by input hash, in 0.6.0.** A scope's hash covers its definition, the content of its
  declared inputs, the system, the toolchain, jackstay's version and its deps' hashes, after Nx,
  Turborepo and Bazel. A scope whose hash has a counted pass is not run. A failure is never cached.
- **Simple behaviour, written limits.** Every mechanism has a rule a reader can check and a list of
  what it does not see. Where a place keeps drawing findings, it is narrowed, or a solved approach
  is adopted, instead of growing more machinery.
- **No test runs twice on one commit in CI.** A scope runs in at most one CI job per system.

## Consequences

The [landing design](../design/land.md) section 7 is the specification. `jackstay.json` gains
`deps`, `inputs` and `cache` beside `ci` and `systems`, with schema 3; a schema-2 `steps` list is
read as `deps`, so existing projects keep their order. The gate's step marks and bars, `--resume`,
the `resumed` and `refused` outcomes and receipts reuse for scopes go; a cached pass reads `passed`,
because it was counted for identical inputs. Whether a cached pass may cross between a person's
machine and CI is left open; the design works with no crossing.
