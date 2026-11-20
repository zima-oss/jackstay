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

Or let `jst review` do it and return at once:

```sh
jst review origin/main..HEAD --task parser --reviewer codex,claude-code --background
```

It pins both commits now and starts the round under `jst bg` (`jst procs` lists it as
`review-<task>`). The same review submitted again while it runs joins it; a different review of
the same task is refused until the first ends. `--brief`, `--model`, `--effort` and `--timeout`
belong to a round in the foreground. When the round ends, each session that submitted it gets a
`review.finished` chat message (with chat enabled and a chat identity): outcome, round, report and
next step. Without chat, read `jst review last --task parser` once `jst procs` no longer lists it.
If the run dies first, its sessions are told it was lost; submit it again.

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
jst review check origin/main..HEAD --task parser-fix && jst gh pr-land --title 'Fix the parser'
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

### Submit and track a review from an agent

With `chat.enabled` and the repository's `jst chat serve` MCP server, `review.submit` takes `base`,
`head`, `task` and a `reviewers` list (`codex`, `claude-code` or both) and starts the same
background round. `review.status` with a `task` shows this repository's review running in the
background, if any, and how its latest one ended: outcome, round and report. The result arrives as
a `review.finished` message from `jackstay`; read it with `chat.inbox` and acknowledge it with
`chat.ack`. A result the chat could not take is delivered the next time the session reads its inbox
or reaches a configured hook.

## What can go wrong

- **Exit 6 running Codex in the Claude Code sandbox**: run the command outside that sandbox.
- **Exit 5, one reviewer failed or the run was killed**: run the same command again. A reviewer
  that already answered this exact question is not run again: its answer is reused, and the
  report says from which attempt.
- **Exit 5, `no slot`**: other reviews held every slot past `queue_minutes`. Rerun later. An
  inaccessible lock instead fails immediately and names the underlying error.
- **Exit 2, task busy**: another run of the same task is under way; wait for it.
- **Exit 2, no reviewer selected**: `review.use` is empty; name one with `--reviewer` or list it in `review.use`.
- **A review costs money and minutes** and answers differently each time: it is never part of a
  test scope or the gate.

The design: [review.md](../design/review.md).
