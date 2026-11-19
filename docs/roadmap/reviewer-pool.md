# Shared reviewer pool

Status: approved for local implementation. The first slice is an explicit, local,
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
directory and filesystem identity, base commit, head commit, task, and sorted
reviewer set. Identical requests return the same
job, including when it has already finished. Changing any identity field creates
a new job; a changed branch tip therefore cannot silently alter a queued review.
The CLI exposes the job ID and whether it was newly queued. The pool never
deduplicates merely by task name or branch name.

The request is a snapshot of *what to review*. Review configuration and policy
remain the checked-in `jackstay.json` and the existing review store. At execution,
`jst review` validates them. A later policy change may refuse an old request or
require a fresh review. A future slice may pin a configuration digest and show
staleness before execution; it must not silently accept a verdict under changed
policy.

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

On restart, a `running` record with no worker lock is taken before later
`pending` records. The attempt counter increments; the underlying `jst review`
may have already written a round before the worker died. This is **at least
once** execution. The review store's own task locking and round state remain
authoritative; the pool must not claim exactly once reviewer invocation. A
later milestone will reconcile a persisted review attempt/round before retrying
so completed work is not repeated unnecessarily.

## Fairness, limits, and backoff

The first slice is FIFO among pending jobs, with interrupted running work
recovered first. A single worker lock prevents concurrent `next` invocations
from choosing the same or different jobs. The existing `review.max_concurrent`,
`review.queue_minutes`, reviewer timeout, and total review budget still govern
the actual reviewer invocation. No pool job starts when its explicit `next`
worker is absent.

The later worker slice should add configurable global/per-reviewer concurrency,
per-repository fairness, bounded queue length, per-producer quotas, and bounded
exponential backoff with jitter for retryable failures. Authentication refusal,
bad configuration, and policy changes require operator action and must not spin.
Time budgets and backoff must use a monotonic runtime clock while running and
persist wall-clock deadlines for restart. Expired work must be visible rather
than silently dropped.

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
   sessions and both reviewer providers.
4. **Operations and policy**: configurable operator controls in `jackstay.json`
   and its editor schema, observability views, retention/prune, and a migration
   path for the versioned state. No hosted or Enterprise-only dependency.

The first slice is accepted when two independent local sessions can enqueue
the same pinned request once, `next` executes it from the specified repository
with the selected reviewer, a second `next` cannot overlap it, interruption
leaves a recoverable state, and every result can be inspected through both
pool status and the existing `jst review` commands.
