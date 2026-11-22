# W-096 — Test scopes run as a task graph; a cache by their inputs comes later

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
- **A cache by input hash, later.** Not in 0.6.0: it is planned for a later version, and its
  design there may differ from what this record and the specification say of it. A scope's hash covers its definition, the content of its
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
`deps` beside `ci` and `systems`, with schema 3; a schema-2 `steps` list is read as `deps`, so
existing projects keep their order.

The cache is not in 0.6.0 (land.md section 7.3 records its direction only), so 0.6.0 accepts no
`inputs` or `cache` key, and the gate's step marks and bars, `--resume`, the `resumed` and
`refused` outcomes, the `resume` key and receipts reuse for scopes stay, per node of the graph; a
node resumes only when none of its deps ran in the same run. CI has one job per system, which runs
that system's covered graph once in graph order and is what the base's rules require, with no
cache steps: it runs every covered scope each time. Written limits: jobs running at once on one
commit both run every scope, and a release or a manual dispatch runs the job in full.

A composite carries only its deps; its checks move to the scopes
that need them, and a schema-2 list keeps its own. A scope is covered by CI on a system only when
it is eligible there by one recursive rule over its graph.

Only an install refuses without the local `jst test uncovered` record; a release does not, and a
full gate on a Mac runs on the version-bump commit before the tag.

jackstay's own scopes change with the code, so that no test runs twice on one commit in Linux CI
(land.md section 7.6): one Go scope runs `go test -race -tags integration ./...`, the unit and the
integration tests each once, the `internal/docs` tests among them; `x-check` (formatting, vet,
lint), `x-sec` (vulnerability and security scans) and `x-docs` (the site build and link check only)
run no test of the Go scope. The local `gate` keeps all four, so a Mac
also runs `go test` over the `*_darwin.go` code.
