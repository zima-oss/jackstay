# Reviews

`jst review` records a cold code review of a commit or range in rounds. Choose [Codex](codex.md),
[Claude Code](claude-code.md) or both with `--reviewer` or `review.use`. Each selected reviewer
reads the change, the project's rules and findings from earlier rounds without the writing
session's memory.

Starting a round needs the selected CLI and its credentials. The `check`, `last`, `list`, `show`
and `prune` commands read stored rounds without starting a reviewer.

## `jst review`

```sh
jst review [REV | BASE..REV] [--task NAME] [--reviewer NAME,...] [--brief FILE] [--focus TEXT]
           [--fresh] [--model M] [--effort E] [--timeout MIN] [--past-limit] [--json]
```

- No revision: HEAD against its first parent. `BASE..REV`: the whole range.
- `--task NAME`: the rounds belong to a task (default: the branch's name). A task's rounds form a
  chain: each later round reviews what changed since the round before, and re-examines every
  finding still open.
- `--fresh`: start a new chain, a full review from `BASE`.
- `--focus TEXT`: where to look hardest; it never narrows the round.
- `--brief FILE`: a brief made available as a file for this call.
- `--model`, `--effort`, `--timeout`: override the configuration for this call.
- `--reviewer codex`, `--reviewer claude-code` or `--reviewer codex,claude-code` selects the
  reviewers for this round. Without the flag, `review.use` supplies the choice. If neither is
  set, the command exits 2 and asks you to select a reviewer.

Both reviewers get the same local, read-only snapshot of the review range. They can browse and
search the reviewed commit under `head/`, compare files with `base/` and, for an incremental
round, inspect `previous/`. The `changes/` directory contains changed-path indexes and patches
for on-demand reading. The initial prompt contains instructions and references. The selected
provider receives source excerpts when the reviewer reads them during the session.
Jackstay keeps the result of each round under `~/.jackstay/<project>/reviews/`. If Git reports
missing objects in a partial clone, fetch the required history and objects before retrying.
A projection error or an incomplete verdict leaves a failed attempt visible through `jst review last`.

| Exit | Meaning | What to do |
|---|---|---|
| 0 | the round is done and does not block | go on |
| 1 | the round blocks: an open finding of a blocking severity | fix, commit, review again |
| 2 | usage or configuration (also: this task busy with another run) | read the message |
| 3 | a place drew findings two rounds running: it is due for redesign | redesign that place, not patch it |
| 4 | the round limit is reached and it still blocks | the owner decides; `--past-limit` after that |
| 5 | a reviewer could not complete the review or gave no usable answer | rerun; `jst review last` says why |
| 6 | the environment refuses: reviewer unavailable, sandbox or authentication issue | follow the diagnostic for the selected reviewer |

Without `--json` it prints three lines: the outcome, the report's path, and the next step.

### In the background

A review takes minutes. Run it under `jst bg`, and read the result when it ends:

```sh
jst bg --for 37 --name review-parser -- jst review origin/main..HEAD --task parser --reviewer codex
jst review last --task parser
```

`jst review` prints its whole budget on its first line (37 minutes with the defaults: 15 waiting for
a slot, 20 for the run, 2 to spare); give `jst bg --for` at least that, more if you want slack. Up to `review.max_concurrent` (4) runs share the machine.

### Running Codex outside the agent sandbox

Codex keeps its login in `~/.codex/auth.json` and refreshes it now and then. Refreshed where the file
cannot be written, the login can be lost for every session. So `jst review` refuses (exit 6) when it
cannot write that file, and must be run as a command that bypasses the sandbox. After a login
failure it refuses until `codex login` has been run again.

### Enabling Claude Code

Install Claude Code 2.1.248 or newer. Check your existing login with `claude auth status` or
provide `ANTHROPIC_API_KEY` through the environment. With a desktop login on macOS, run `jst review`
or `jst review pool next` from a context that can access the system credential store. Check
`claude auth status` in that same context if the adapter reports an authentication failure. [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference)

Select Claude Code directly for one round:

```sh
jst review origin/main..HEAD --task parser-fix --reviewer claude-code --fresh
```

For a project that regularly asks both reviewers, configure them as the required set:

```json
"review": {
  "reviewers": {
    "claude-code": {"timeout_minutes": 20}
  },
  "use": ["codex", "claude-code"]
}
```

`review.reviewers` supplies optional per-provider settings. Built-in names work directly with
`--reviewer`. Set `review.use` to either name or both to make those reviewers the project's
default and required set. With an empty `review.use`, `jst review check` still requires a
recorded round at the requested commits and checks its policy and findings.

Claude Code uses its `Read`, `Glob` and `Grep` tools. Codex can use read-only shell navigation and
search. Both start in the temporary snapshot and source is limited to tracked Git objects at the
selected revisions. Files from the working checkout and submodule contents are excluded. Symlinks
are available as plain files containing the link target. Managed Claude Code policies, including
managed hooks, still apply. [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference)

## Reading the results

```sh
jst review last [--task NAME] [--json]          # the newest round's report, or the newest attempt's failure
jst review show TASK ROUND [--json]             # one round's report
jst review list [--task NAME] [--rebuild] [--json]
jst review check [--task NAME] BASE..REV        # is this change reviewed and clear?
jst review prune                                # remove tasks past review.keep_days, now
```

`jst review check` exits 0 only when a real recorded round was made at exactly `REV`, its chain
started at exactly `BASE`, every configured required reviewer finished under the current policy,
nothing open blocks, and no redesign is due. With no required reviewers, a recorded round from
either provider can satisfy it. Otherwise it exits 1 with each reason. Use `--fresh` with the full
range when the policy or reviewer set changes, or when the previous review predates the policy record.

```sh
jst review origin/main..HEAD --task parser-fix --reviewer codex
# … fix, commit …
jst review origin/main..HEAD --task parser-fix --reviewer codex
jst review check origin/main..HEAD --task parser-fix && jst gh pr-land --title 'Fix the parser'
```

A branch named like a subcommand is reviewed as `jst review -- list`.

## The `review` section

Every setting has a default except the reviewer choice. A project can name a built-in reviewer on
the command line without a `review` section. Add `review.use` to establish a default for future
rounds and a required reviewer set for `jst review check`.

```json
"review": {
  "reviewers": {"codex": {"effort": "medium", "timeout_minutes": 20}},
  "use": ["codex"],
  "brief": "docs/review-brief.md",
  "cite": ["AGENTS.md", "docs/design/"],
  "severities": ["blocker", "major", "minor"],
  "blocking": ["blocker"],
  "round_limit": 6,
  "max_concurrent": 4,
  "keep_days": 30
}
```

- `brief`: the project context a reviewer needs, such as its threat model. The prompt points to the
  brief in the pinned source view for the reviewer to read. Changes to a configured brief or
  template require a fresh review round.
- `cite`: files and folders the reviewer is told to measure the change against.
- `blocking`: which severities fail a round. With `triage` on (the default), each finding is rated
  by likelihood and consequence, and a rare one is deferred instead of blocking.
- `repeat_rounds` (2): how many rounds running a place must draw a finding before it is called due
  for redesign.
- `reviewers.codex.isolation`: the flags that keep Codex from your own Codex configuration, MCP
  servers and environment; replace it only if you know why.

`jst check` validates the section. Paths are read from the reviewed commit's tree.

## Share a review queue

Use the optional local pool when several repositories submit review work.

```sh
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
- **Exit 2, no reviewer selected**: name one with `--reviewer` or configure `review.use`.
- **A review costs money and minutes** and answers differently each time: it is never part of a
  test scope or the gate.

The design: [review.md](../design/review.md).
