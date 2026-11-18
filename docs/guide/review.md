# Reviews

`jst review` runs a cold code review of a commit or a range by [Codex](codex.md), in rounds, and
gives a verdict you can act on. A cold review is one with no memory of how the change was written:
the reviewer reads the change, the project's rules and the findings of earlier rounds.

**A round needs Codex.** Codex CLI must be installed and logged in (`codex login`). Without it,
starting a round exits 6 and says Codex is not installed; `jst review check`, `last`, `list`,
`show` and `prune` still read saved rounds, and every other jackstay command works as before.

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
- `--brief FILE`: a brief added to the prompt for this call.
- `--model`, `--effort`, `--timeout`: override the configuration for this call.

Codex reads a detached worktree at the reviewed commit, in a read-only sandbox, and answers in a
fixed JSON form. jackstay keeps every round under `~/.jackstay/<project>/reviews/`.

| Exit | Meaning | What to do |
|---|---|---|
| 0 | the round is done and does not block | go on |
| 1 | the round blocks: an open finding of a blocking severity | fix, commit, review again |
| 2 | usage or configuration (also: this task busy with another run) | read the message |
| 3 | a place drew findings two rounds running: it is due for redesign | redesign that place, not patch it |
| 4 | the round limit is reached and it still blocks | the owner decides; `--past-limit` after that |
| 5 | a reviewer gave no usable answer (timeout, no slot, unparsable) | rerun; `jst review last` says why |
| 6 | the environment refuses: Codex not installed, inside a sandbox, not logged in | install Codex; run outside the sandbox; `codex login` |

Without `--json` it prints three lines: the outcome, the report's path, and the next step.

### In the background

A review takes minutes. Run it under `jst bg`, and read the result when it ends:

```sh
jst bg --for 37 --name review-parser -- jst review origin/main..HEAD --task parser
jst review last --task parser
```

`jst review` prints its whole budget on its first line (37 minutes with the defaults: 15 waiting for
a slot, 20 for the run, 2 to spare); give `jst bg --for` at least that, more if you want slack. Up to `review.max_concurrent` (4) runs share the machine.

### Run it outside the agent sandbox

Codex keeps its login in `~/.codex/auth.json` and refreshes it now and then. Refreshed where the file
cannot be written, the login can be lost for every session. So `jst review` refuses (exit 6) when it
cannot write that file, and must be run as a command that bypasses the sandbox. After a login
failure it refuses until `codex login` has been run again.

## Reading the results

```sh
jst review last [--task NAME] [--json]          # the newest round's report, or the newest attempt's failure
jst review show TASK ROUND [--json]             # one round's report
jst review list [--task NAME] [--rebuild] [--json]
jst review check [--task NAME] BASE..REV        # is this change reviewed and clear?
jst review prune                                # remove tasks past review.keep_days, now
```

`jst review check` is the one answer to "is this change reviewed and clear?": exit 0 only when the
task's newest round was made at exactly `REV`, its chain started at exactly `BASE`, nothing open
blocks, and it is not due for redesign. Otherwise exit 1 with each reason.

```sh
jst review origin/main..HEAD --task parser-fix
# … fix, commit …
jst review origin/main..HEAD --task parser-fix
jst review check origin/main..HEAD --task parser-fix && jst gh pr-land --title 'Fix the parser'
```

A branch named like a subcommand is reviewed as `jst review -- list`.

## The `review` section

Every key has a default; a project needs no section to start.

```json
"review": {
  "reviewers": {"codex": {"model": "", "effort": "medium", "timeout_minutes": 20}},
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

- `brief`: what a reviewer of this project must know (its threat model, what to ignore), put at the
  top of every prompt.
- `cite`: files and folders the reviewer is told to measure the change against.
- `blocking`: which severities fail a round. With `triage` on (the default), each finding is rated
  by likelihood and consequence, and a rare one is deferred instead of blocking.
- `repeat_rounds` (2): how many rounds running a place must draw a finding before it is called due
  for redesign.
- `reviewers.codex.isolation`: the flags that keep Codex from your own Codex configuration, MCP
  servers and environment; replace it only if you know why.

`jst check` validates the section. Paths are read from the reviewed commit's tree.

## What can go wrong

- **Exit 6 inside Claude Code**: the session's sandbox. Run the command outside it.
- **Exit 5, `no slot`**: other reviews held every slot past `queue_minutes`. Rerun later.
- **Exit 2, task busy**: another run of the same task is under way; wait for it.
- **A review costs money and minutes** and answers differently each time: it is never part of a
  test scope or the gate.

The design: [review.md](../design/review.md).
