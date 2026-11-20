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
- **No test runs twice on one commit in CI.** jackstay runs each scope at most once in a
  system's CI job. It cannot see inside a command, so a project keeps the tests of its CI scopes on one system
  disjoint: one scope does not rerun another's tests.

## Consequences

The [landing design](../design/land.md) section 7 is the specification. `jackstay.json` gains
`deps`, `inputs` and `cache` beside `ci` and `systems`, with schema 3; a schema-2 `steps` list is
read as `deps`, so existing projects keep their order. The gate's step marks and bars, `--resume`,
the `resumed` and `refused` outcomes and receipts reuse for scopes go; a cached pass reads `passed`,
because it was counted for identical inputs, and a scope's `requires` still run before every cache
lookup. A cached pass never crosses between machines: a local run reuses only local passes and CI
only CI passes, kept in CI's own cache store; `jst test` makes no new network calls, and there are no
signed receipts and no trust root. A person who runs a CI-covered scope locally by name runs it, at
the cost of local minutes, a stated limit.

The cache is a plain `jst test` mechanism: a folder (configurable) of verdicts keyed by the input
hash, written the moment a scope passes, knowing nothing of CI jobs or workflows. CI has one job per
system, which runs that system's covered graph once in graph order and is what the base's rules
require; the workflow around it is two `actions/cache` steps keyed by system and commit. Written
limits: jobs running at once on one commit may both run a scope, an evicted entry means a rerun,
and a release or a manual dispatch reuses what the cache holds and runs the rest. Integration beyond
that minimum (concurrency groups, a release reusing the push run's result) is a separate, later
task.

The cache keeps verdicts, never outputs, so a scope whose files other scopes use, such as a build,
is not cached, nor is any scope that depends on it; neither is one that reads the network or a
database outside the repository. A composite carries only its deps; its checks move to the scopes
that need them, and a schema-2 list keeps its own. A scope is covered by CI on a system only when
it is eligible there by one recursive rule over its graph.

Only an install refuses without the local `jst test uncovered` record; a release does not, and a
full gate on a Mac runs on the version-bump commit before the tag.

jackstay's own scopes change with the code, so that no test runs twice on one commit in Linux CI
(land.md section 7.6): one Go scope runs `go test -race -tags integration ./...`, the unit and the
integration tests each once, the `internal/docs` tests among them; `x-check` (formatting, vet,
lint), `x-sec` (vulnerability and security scans) and `x-docs` (the site build and link check only)
run no test of the Go scope, and `x-sec` is not cached. The local `gate` keeps all four, so a Mac
also runs `go test` over the `*_darwin.go` code.
