# Reviews

`jst review` gets you a cold code review of a commit or a range, recorded in rounds, and
`jst review check` tells you whether a change is reviewed and clear. Each reviewer, [Codex](codex.md),
[Claude Code](claude-code.md) or both, reads the change, the project's rules and the findings of
earlier rounds without the writing session's memory.

Starting a round needs the selected reviewer's CLI, installed and signed in (see its page). Reading
stored rounds (`check`, `last`, `list`, `show`, `prune`) starts no reviewer.

## Review a change

```sh
jst review origin/main..HEAD --task parser-fix
jst review last --task parser-fix
```

- No revision reviews HEAD against its first parent; `BASE..REV` reviews the whole range. A
  branch named like a subcommand is reviewed as `jst review -- list`.
- `--task NAME`: the rounds belong to a task (default: the branch's name). A task's rounds form a
  chain: each later round reviews what changed since the round before, and re-examines every
  finding still open. `--fresh` starts a new chain, a full review from `BASE`.
- `--reviewer codex`, `--reviewer claude-code` or `--reviewer codex,claude-code` chooses the
  reviewers for this round. Without it, `review.use` chooses, and without that, Codex reviews.
- `--focus TEXT` says where to look hardest; it never narrows the round.

Every flag: `jst review --help`. Without `--json` it prints three lines: the outcome, the report's
path, and the next step. `jst review last` shows the newest round's report, or why the newest
attempt failed; `jst review show TASK ROUND` shows any round's.

| Exit | Meaning | What to do |
|---|---|---|
| 0 | the round is done and does not block | go on |
| 1 | the round blocks: an open finding of a blocking severity | fix, commit, review again |
| 2 | usage or configuration (also: this task busy with another run) | read the message |
| 3 | a place drew findings two rounds running: it is due for redesign | redesign that place, not patch it |
| 4 | the round limit is reached and it still blocks | the owner decides; `--past-limit` after that |
| 5 | a reviewer could not complete the review or gave no usable answer | rerun; `jst review last` says why |
| 6 | the environment refuses: reviewer unavailable, sandbox or authentication issue | follow the diagnostic for the selected reviewer |

### Run it in the background

A review takes minutes. Run it under [`jst bg`](background.md), and read the result when it ends:

```sh
jst bg --for 37 --name review-parser -- jst review origin/main..HEAD --task parser --reviewer codex
jst review last --task parser
```

`jst review` prints its whole budget on its first line (37 minutes with the defaults: 15 waiting
for a slot, 20 for the run, 2 to spare); give `jst bg --for` at least that. Up to
`review.max_concurrent` (4) runs share the machine.

Run Codex reviews outside an agent sandbox: Codex must be able to save its login
([Codex](codex.md#codex-as-the-reviewer-jst-review)).

### What a reviewer sees

Both reviewers get the same local, read-only snapshot of the review range. They can browse and
search the reviewed commit under `head/`, compare files with `base/` and, for an incremental
round, inspect `previous/`. The `changes/` directory holds changed-path indexes and patches to read
on demand. The first prompt holds instructions and references; the provider receives source
excerpts when the reviewer reads them during the session.

Source is limited to tracked Git objects at the selected revisions: files from your working
checkout and submodule contents are left out, and a symlink appears as a plain file holding its
target. Claude Code uses its `Read`, `Glob` and `Grep` tools; Codex can use read-only shell
navigation and search. Managed Claude Code policies, managed hooks included, still apply.

jackstay keeps each round under `~/.jackstay/<project>/reviews/`. If Git reports missing objects in
a partial clone, fetch the history and objects needed before retrying. A projection error or an
incomplete verdict leaves a failed attempt that `jst review last` shows.

## Check that a change is reviewed: `jst review check`

```sh
jst review origin/main..HEAD --task parser-fix --reviewer codex
jst review origin/main..HEAD --task parser-fix --reviewer codex
jst review check origin/main..HEAD --task parser-fix && jst land --title 'Fix the parser'
```

Review, fix and commit, review again, then check. `jst review check` exits 0 only when a real
recorded round was made at exactly `REV`, its chain started at exactly `BASE`, every required
reviewer finished under the current policy, nothing open blocks, and no redesign is due. With no
required reviewers, a recorded round from either provider satisfies it. Otherwise it exits 1 with
each reason. Use `--fresh` with the full range when the policy or the reviewer set changes, or when
the previous review predates the policy record.

## The `review` section

Every setting has a default, and a project can name a built-in reviewer with `--reviewer` without
any `review` section. Add the section to choose the default reviewers and tune the rounds:

```json
"review": {
  "reviewers": {"codex": {"effort": "medium", "timeout_minutes": 20},
                "claude-code": {"timeout_minutes": 20}},
  "use": ["codex", "claude-code"],
  "brief": "docs/review-brief.md",
  "cite": ["AGENTS.md", "docs/design/"],
  "severities": ["blocker", "major", "minor"],
  "blocking": ["blocker"],
  "round_limit": 6,
  "max_concurrent": 4,
  "keep_days": 30
}
```

- `use`: the default reviewers for new rounds, and the required set for `jst review check`.
  Without it, both are Codex. With an empty `use`, a round needs `--reviewer`, and
  `jst review check` still requires a recorded round at the requested commits and checks its
  policy and findings.
- `reviewers`: optional settings per reviewer. `reviewers.codex.isolation` holds the flags that keep
  Codex from your own Codex configuration, MCP servers and environment; replace it only if you know
  why.
- `brief`: the project context a reviewer needs, such as its threat model. The prompt points to the
  brief in the pinned source view. A change to a configured brief or template needs a fresh round.
- `cite`: files and folders the reviewer is told to measure the change against.
- `blocking`: which severities fail a round. With `triage` on (the default), each finding is rated
  by likelihood and consequence, and a rare one is deferred instead of blocking.
- `repeat_rounds` (2): how many rounds running a place must draw a finding before it is due for
  redesign.
- `keep_days`: how long rounds are kept; `jst review prune` removes older tasks now.

`jst check` validates the section. Paths are read from the reviewed commit's tree.

## Share a review queue

Use the optional local pool when several repositories submit review work.

```text
jst review pool add --repo "$PWD" --base main --head HEAD --task parse-change --reviewer codex
jst review pool list --json
jst review pool inspect --json
jst review pool next
jst review pool retry JOB_ID
jst review pool prune
jst review pool migrate
```

The pool pins both commits and the effective review policy when a job is added. If verdict
rules, selected reviewer settings, budgets, or the resolved review store change before launch,
the job fails with `stale-policy`. Submit again to pin the new policy, or restore the original
policy before retrying. Older pending
jobs without a policy pin also require resubmission. An already recorded round can still be
reconciled when its original store remains available. One foreground worker runs an eligible
job at a time across the machine. If the worker loses its state update after the review
saved a completed round, the next worker can recover its exact verdict from that round.

Set admission with `review.pool.accepting` in `jackstay.json` (default `true`; it needs
`"jackstay_schema": 3`). With `false`, the repository accepts no new job identities; existing
jobs can still be queried, retried, and processed. The pool's limits are fixed:

| Limit | Value |
| --- | ---: |
| Jobs pending or running for one repository (the machine-wide cap is 256) | 32 |
| Launches for one pinned job, including recovery after interruption | 5 |
| Initial delay when explicitly retrying a retryable failure | 30 s |
| Upper bound of the exponential retry delay | 900 s |
| Days a terminal job is kept after its last update (saved when the job is added) | 30 |

JSON status exposes when delayed work becomes eligible. Configuration errors and other
operator failures need attention before retry. See the [reviewer pool specification](../roadmap/reviewer-pool.md)
for the recovery guarantees and current limits.

### Inspect and clean up the pool

`jst review pool inspect` shows aggregate queue counts and eligibility without exposing repository
paths, task names, or job identifiers. Add `--json` for machine-readable output. `list --json`
still exposes individual local jobs for an operator who needs them.

`jst review pool prune` previews expired `complete` and `failed` jobs. Add `--json` for a
machine-readable preview, then run `jst review pool prune --apply` to remove the eligible jobs
and their pool subscriptions and events. Pending and running jobs remain. A terminal job remains
while any subscriber's result has yet to reach chat. Once chat has durably accepted the result,
the recipient can read and acknowledge it through `chat.inbox` and `chat.ack` even if the pool
job has been pruned. The review round reports have their own retention setting,
`review.keep_days`. Submitting the same request after pruning starts a new job
generation, and an older acknowledgment does not hide its result.

If a session has permanently left a job, use `jst review pool subscribers JOB_ID` to find its
opaque recipient ID and the job generation. Preview with
`jst review pool unsubscribe JOB_ID SESSION_ID --generation TOKEN`; add `--apply` to close
that recipient's subscription. Closed subscriptions stop blocking history cleanup and cannot
be reopened for that generation. Other recipients remain subscribed. Chat messages already
accepted or in flight can still arrive and be acknowledged normally.

`jst review pool migrate` previews the state-file upgrade. Use
`jst review pool migrate --apply` to perform it explicitly. Versions 1 and 2 can be read without a
write. The first queue write also upgrades the file atomically to version 3, which adds policy
pins and subscription closure. Jobs without a saved history period use the 30-day default.
An unknown version or damaged state is refused. Back up the local pool state file before its
first write when you may need to run an older jackstay. Older binaries cannot read version 3.
Before restoring a backup for a downgrade, stop pool writers and account for newer jobs. No worker or cleanup timer runs automatically.

### Submit and track a review from an agent

Enable `chat.enabled` and connect the repository's `jst chat serve` MCP server. Use
`review.submit` with `base`, `head`, `task`, and an explicit `reviewers` list containing
`codex`, `claude-code`, or both. Keep the returned `job.id` and pass it as `job_id` to
`review.status` to see the pinned commits, state, attempts, and exit code. Submitting the
same pinned request again returns the same job. Linked worktrees share an MCP job when commits,
task, reviewers, effective policy, and resolved review store agree. The first submission's
checkout still runs the review. The operator still runs `jst review pool next`
to process eligible work.

The requesting session receives a chat message when an attempt settles. Read the result
with `review.status`, then acknowledge the message with `chat.ack` and its `message_id`.
Use `chat.inbox` to recover unacknowledged messages. The Codex bridge can deliver to an
active turn. Claude Code can receive pending messages at its next configured prompt or tool
boundary. Keep the same chat session token when reconnecting.

## What can go wrong

- **Exit 6 running Codex in the Claude Code sandbox**: run the command outside that sandbox.
- **Exit 5, `no slot`**: other reviews held every slot past `queue_minutes`. Rerun later. An
  inaccessible lock instead fails immediately and names the underlying error.
- **Exit 2, task busy**: another run of the same task is under way; wait for it.
- **Exit 2, no reviewer selected**: `review.use` is empty; name one with `--reviewer` or list it in `review.use`.
- **A review costs money and minutes** and answers differently each time: it is never part of a
  test scope or the gate.

The design: [review.md](../design/review.md).
