# Shared reviewer pool

Status: local queue, session integration, and operator maintenance implemented. The pool is an explicit, local,
persistent queue in front of the existing `jst review`. It requires no account,
background service, hosted API, or selected default reviewer.

## Problem and boundary

Several agent sessions can ask for reviews at once. Today each session launches
`jst review` itself and has to remember which repository, range, task, and reviewer
it requested. The review command already isolates a pinned tree, keeps rounds,
enforces its own reviewer slots, and records a verdict. The pool coordinates
requests across sessions; it does not change review policy, duplicate the review
store, or grant a queued request authority to merge or publish.

The pool is opt in. A session can continue to call `jst review` directly. Codex
and Claude Code remain optional reviewers; the producer chooses one or both
explicitly for each request. An empty `review.use` is valid for the project.

## Request and identity

An enqueue request contains an **absolute local repository path**, base ref,
head ref, task name, and a nonempty set of known reviewer names. The CLI resolves
the repository and both refs before storing the request. Stored base and head
are full commit object IDs, not moving branch names. The path is canonicalized
through symlinks. A task must satisfy the same branch-name rule as `jst review`.

The stable job identity is a hash of canonical repository path, its Git common
directory and filesystem identity, base commit, head commit, task, sorted
reviewer set, and effective review policy fingerprint. Identical requests return the same
job, including when it has already finished. Changing any identity field creates
a new job; a changed branch tip therefore cannot silently alter a queued review.
The CLI exposes the job ID and whether it was newly queued. The pool never
deduplicates merely by task name or branch name.

The request pins the effective verdict rules, selected reviewer execution settings, budgets,
and resolved review store. The worker verifies the pin against its current checkout configuration
and the review child checks it again before starting. A mismatch records `stale-policy`
with exit 2. Submit again to pin the new policy, or restore the original policy before retrying.
Pre-pin jobs remain readable, and already
recorded work remains eligible for reconciliation with its original store. A legacy pending
job cannot launch a reviewer until a new pinned request is submitted.

MCP submissions from linked worktrees share one job when the Git common directory identity,
commits, task, selected reviewers, effective policy, and resolved store match. The original
submission keeps its execution checkout. CLI submissions remain checkout-scoped.

## Local commands and state machine

First-slice commands:

```text
jst review pool add --repo ABSOLUTE_PATH --base REF --head REF --task NAME --reviewer codex[,claude-code]
jst review pool list [--json]
jst review pool next
jst review pool retry JOB_ID
```

`next` runs **one** job in the foreground and exits when its review ends. An
operator or agent may use `jst bg` to supervise that foreground call. There is
no hidden worker, scheduling timer, network listener, or automatic credential
setup. With no pending job it returns successfully and says so. `list` is local
and read only.

| State | Meaning | Next transition |
| --- | --- | --- |
| `pending` | accepted, no attempt active | `running` on `next` |
| `running` | one worker owns the attempt | `complete` for review exits 0, 1, 3, 4; `failed` for exits 2, 5, 6 or process failure |
| `complete` | review produced a terminal verdict, which may block | no automatic retry; changed commits make a new request |
| `failed` | the reviewer or environment did not provide a terminal verdict | `pending` through explicit `retry` |

Exit 1 is a finding, not a transport failure. Exit 2 is a configuration or usage
refusal. Exits 3 and 4 are decisions for
redesign or the owner, not reasons for an automatic retry. The pool preserves
the exact review exit code. `retry` never moves the pinned revisions and accepts
only `failed` jobs. A completed request is not reopened with `retry`.

The first slice uses one machine-wide worker lock for `next`. This deliberately
caps pool work at one review command even though `jst review` has its own
reviewer slots. The lock is held across the review process and released by the
kernel if the worker dies. Pool state is a versioned JSON file under
`~/.jackstay/.all/review-pool/`, serialized by a separate file lock and replaced
atomically after sync. Files and directories are user private (0600/0700).
The worker's review command runs with its working directory set to the stored
repository root and receives the pinned `BASE..HEAD`, task, and reviewer set as
separate argv words. It is not run in the enqueuing session's current directory.

On restart, a `running` record with no worker lock is checked against the
review store. A new attempt carries a random correlation token in the pool state,
`attempt.json`, and `round.json`. A matching completed round with the exact
task, base and head commits, reviewer set, and terminal exit completes the job
without rerunning it. If the store cannot be read or evidence conflicts, the
running job remains `unresolved` with its token; later `next` calls recheck it
while other jobs may proceed. If no completed round exists, the interrupted job runs
again before later pending work. Older state without a token uses that latter
path. The pool cannot claim exactly once reviewer invocation: an interrupted
attempt may have launched reviewers without committing a round.

## Fairness, limits, and backoff

The pool is FIFO among eligible pending jobs, with interrupted running work
recovered first. A single worker lock prevents concurrent `next` invocations
from choosing the same or different jobs. The existing `review.max_concurrent`,
`review.queue_minutes`, reviewer timeout, and total review budget still govern
the actual reviewer invocation. No pool job starts when its explicit `next`
worker is absent.

The machine-wide queue caps pending or running jobs at 256, including jobs
returned to pending by `retry`. Each repository may have 32 jobs pending or
running; a job gets 5 launches, and its retry delay starts at 30 seconds and is
capped at 900. These limits are constants, saved with each job at enqueue, so a
later jackstay does not silently alter its retry schedule. An explicit `retry` after a
reviewer failure (exit 5) records a durable exponential delay; `next` skips a
job until its deadline. Configuration and environment refusals require an
operator retry and receive no automatic delay. No retries launch themselves.
Further work may add per-repository fairness, per-reviewer concurrency,
cancellation, and producer quotas. A single explicit worker
remains the global concurrency limit; deadline timestamps survive restart.

## Operator policy and history lifecycle

`review.pool.accepting` defaults to `true`. Setting it to `false` stops new job
identities for that repository while preserving idempotent submissions of an
existing job and allowing `list`, `inspect`, `next`, `retry`, and status reads.
The value is checked at submission. This is an admission control for a local
queue, not an agent host permission boundary.

A job's history is kept for 30 days. The value is saved with each new job,
including jobs that later fail; old jobs keep their saved value. A terminal
job becomes eligible for pruning when its most recent update is older than that
period. An active job remains regardless of its age.

After pruning, an identical request may be submitted again. It keeps the same
deterministic job ID but starts a new generation of work and notifications.
An acknowledgment from the old generation cannot suppress the new result.

`jst review pool prune [--apply] [--json]` previews eligible complete and failed
jobs by default. `--apply` removes those jobs and their subscriptions and
terminal event rows in one locked state update. A job with any terminal event
not yet materialized in a subscriber's chat remains until a later prune. The
chat store retains an accepted but unacknowledged message until the recipient
calls `chat.ack`. Pruning the pool does not remove that message or a separate
`jst review` round report. No automatic pruning or background worker is added.

An operator can list opaque recipient IDs and the job generation with `subscribers`, then
preview closing a subscriber with `unsubscribe`. Applying requires the exact generation
so a stale preview cannot close a replacement job. Closing releases that recipient's pending
handoffs and keeps the other subscriptions. Messages already in chat remain readable and
acknowledgeable. An in-flight handoff may already have reached chat. A closed subscription
cannot be recreated for that job generation.

`jst review pool inspect [--json]` reads aggregate state without writing it.
It shows state counts, eligible and delayed work, unresolved recovery, oldest
pending age, outcome counts, and notification delivery backlog. The output has
no repository path, task name, or job identifier. `list --json` retains the
individual records for explicit local inspection. Pool transition events attach
to an active caller trace and carry an opaque job ID, action, state, attempt
count, and exit code. Operator CLI commands keep their journal disabled.

Versions 1 and 2 remain readable. An explicit `jst review pool migrate [--apply] [--json]`
previews or applies an upgrade to version 3, which adds policy fingerprints and subscriber
closure. The first queue write can upgrade in the same atomic replacement. Legacy jobs without
a saved retention period use 30 days. The migration preserves pinned revisions, attempts, and
events. Unknown versions, invalid records, and corrupt or oversized files fail closed.
Operators who may downgrade back up the old state file before applying the migration or making
a queue write. Older binaries cannot read version 3. Before restoring the backup, stop pool
writers and account for jobs added after it was made.

## Privacy and trust

Queue state can contain local paths, task names, and reviewer choice. It stays
on this machine in the jackstay home; no queue payload goes to a remote account.
The CLI and telemetry should treat arguments as private. `list --json` is an
explicit local disclosure for tools; logs and traces should record job ID,
status, duration, and exit class, not paths, prompts, credentials, or reviewer
output. The pool never copies authentication material. Reviewer isolation and
the detached-tree read-only contract are those of `jst review`.

Treat the persisted queue as untrusted local input on load. Unknown schema
versions or corrupt/oversized state fail closed without truncating the file.
Before executing a recovered job, validate its path, reviewer names, full
commit IDs, and task; reject stale or tampered records. Persist the Git common
directory and filesystem device/inode identities of the checkout root and
common directory, then compare at execution. Do not follow a repository path
that has been retargeted or replaced since enqueue. A copied queue state on
another machine or filesystem may require a new request. The first slice must
make a bounded, explicit failure visible if it cannot prove the original
repository identity.

## Portability and observability

Use existing Go file-lock and process-supervision primitives on macOS and
Linux. Keep paths through `filepath` and Git object IDs through Git. Avoid
shell command construction. Show the job ID, state, attempts, selected reviewer
set, and last exit in human and JSON status output. `next` prints which job it
started and where to inspect the existing review report. Queue state changes
should produce structured internal telemetry without sensitive arguments.

## Staged delivery and acceptance

1. **Local queue and explicit worker**: add/list/next/retry, pinned identity,
   durable atomic state, process-wide worker lock, restart recovery, tests for
   deduplication, concurrent workers, crash state, failure retry, path validation,
   and both reviewer choices. `just gate` passes. No daemon or external account.
2. **Reconciliation and fairness**: detect an already recorded round after a
   worker crash, add fair scheduling and configurable concurrency/queue limits,
   classify retryable errors, bounded backoff, cancellation, and queue retention.
   Tests cover starvation and no duplicate completed round after restart.
3. **Session integration**: producers in Codex, Claude Code, and other agents
   submit and observe jobs through a common local contract, optionally via the
   session-chat MCP server. Delivery of a status message to an active turn uses
   the engine-specific integration and acknowledgement contract; the pool itself
   does not claim to interrupt an agent turn. End-to-end pilots use two local
   sessions and both reviewer providers. The MCP tools are `review.submit` and
   `review.status`. Terminal events are persisted with the job and appear in
   session chat until the recipient acknowledges them with `chat.ack`.
4. **Operations and policy**: configurable admission and history retention in
   `jackstay.json` and its editor schema, read-only aggregate inspection,
   previewed manual prune, and an atomic version 1 to 2 migration. No hosted or
   Enterprise-only dependency.

The first slice is accepted when two independent local sessions can enqueue
the same pinned request once, `next` executes it from the specified repository
with the selected reviewer, a second `next` cannot overlap it, interruption
leaves a recoverable state, and every result can be inspected through both
pool status and the existing `jst review` commands.

To rerun the third-slice pilots locally, build `jst` with `just build` and run both
scripts with `"$PWD/.build/jst"` as their argument:

- [Codex pilot](../../tests/review-pool-codex-pilot.py) exercises the installed Codex
  app-server and active-turn delivery.
- [Claude Code pilot](../../tests/review-pool-claude-pilot.py) exercises the installed
  Claude Code CLI and delivery at a tool boundary.
- [Mixed-host pilot](../../tests/review-pool-mixed-pilot.py) submits the same request from
  linked worktrees, restarts the Codex bridge, and checks one execution and two separate
  acknowledgments.

The pilots use synthetic repositories, reviewer executables, and loopback model
endpoints. They need no provider account.
