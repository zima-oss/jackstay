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
- `--brief FILE`: a brief added to the prompt for this call.
- `--model`, `--effort`, `--timeout`: override the configuration for this call.
- `--reviewer codex`, `--reviewer claude-code` or `--reviewer codex,claude-code` selects the
  reviewers for this round. Without the flag, `review.use` supplies the choice. If neither is
  set, the command exits 2 and asks you to select a reviewer.

Each reviewer reads a detached worktree at the reviewed commit and answers in a fixed JSON form. jackstay keeps every round under `~/.jackstay/<project>/reviews/`.

| Exit | Meaning | What to do |
|---|---|---|
| 0 | the round is done and does not block | go on |
| 1 | the round blocks: an open finding of a blocking severity | fix, commit, review again |
| 2 | usage or configuration (also: this task busy with another run) | read the message |
| 3 | a place drew findings two rounds running: it is due for redesign | redesign that place, not patch it |
| 4 | the round limit is reached and it still blocks | the owner decides; `--past-limit` after that |
| 5 | a reviewer gave no usable answer (timeout, no slot, unparsable) | rerun; `jst review last` says why |
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
provide `ANTHROPIC_API_KEY` through the environment. Starting a review never opens an interactive
login. [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference)

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

Claude Code receives the pinned patch and can read files in the detached tree using `Read`,
`Glob` and `Grep`. The adapter disables regular local customizations and MCP tools. Managed
Claude Code policies, including managed hooks, still apply. The full prompt, including both patches
in an incremental review, has a 128 KiB limit. Use a smaller range if an attempt reports that
limit. [Claude Code restricted mode](https://code.claude.com/docs/en/cli-reference)

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

- **Exit 6 running Codex in the Claude Code sandbox**: run the command outside that sandbox.
- **Exit 5, `no slot`**: other reviews held every slot past `queue_minutes`. Rerun later. An
  inaccessible lock instead fails immediately and names the underlying error.
- **Exit 2, task busy**: another run of the same task is under way; wait for it.
- **Exit 2, no reviewer selected**: name one with `--reviewer` or configure `review.use`.
- **A review costs money and minutes** and answers differently each time: it is never part of a
  test scope or the gate.

The design: [review.md](../design/review.md).
