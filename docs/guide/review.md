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
  reviewers for this round. Without the flag, `review.use` supplies the choice, and without
  that the reviewer is Codex.

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
from a context that can access the system credential store. Check
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
default and required set; without it, both are Codex. With an empty `review.use` a round needs
`--reviewer`, and `jst review check` still requires a recorded round at the requested commits and
checks its policy and findings.

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

Every setting has a default; the reviewer's is Codex. A project can name a built-in reviewer on
the command line without a `review` section. Add `review.use` to choose another default for future
rounds and the required reviewer set for `jst review check`.

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
- `reviewers.codex.isolation`: the flags that keep Codex from your own Codex configuration and MCP
  servers; replace it only if you know why.
- `reviewers.<name>.mcp_servers`: MCP servers you give back to a reviewer, for example a
  documentation server: `{"docs": {"command": "docs-mcp", "args": ["--stdio"]}}`. Your own servers,
  hooks and rules stay off.

### What a reviewer can reach

A reviewer reads everything, read-only, has the network, your full `PATH`, your proxy and CA
settings, and its own provider's login. It cannot write outside its own temporary folder, read
`~/.ssh`, `~/.aws`, `~/.config/gh` and the other credential folders, the keychain, the other
reviewer's login, or `.env` files (Codex: those found when the run starts, in and beside the
checkout), or see token-like environment variables other than its provider's. Codex and Claude Code get
the same profile; the [design](../design/review.md#34-the-run) lists it whole.

`jst check` validates the section. Paths are read from the reviewed commit's tree.

## Run a review in the background

A review takes minutes. Start it and keep working:

```sh
jst review origin/main..HEAD --task parse-change --reviewer codex,claude-code --background
```

`--background` pins both commits now, starts the round under `jst bg` (so `jst procs` lists it as
`review-<task>`, and `jst procs stop review-<task>` stops it) and returns at once. Several reviews
run at the same time, up to `review.max_concurrent` reviewer runs on the machine. Submitting the
same review again while it runs joins it; a different review of the same task is refused until the
first ends. `--brief`, `--model`, `--effort` and `--timeout` belong to a foreground round; a round
in the background uses the configuration.

When the round ends, every session that submitted it gets a `review.finished` message in chat
(when the repository enables chat and the session has a chat identity): the outcome, the round, the
report's path and the next step. Without chat, read the result with `jst review last --task NAME`
once `jst procs` no longer lists the run. If the run dies before it finishes (the machine
restarted), its sessions are told so, and submitting it again is enough.

### Submit and track a review from an agent

With `chat.enabled` and the repository's `jst chat serve` MCP server connected, `review.submit`
takes `base`, `head`, `task` and an explicit `reviewers` list (`codex`, `claude-code` or both) and
starts the same background round. `review.status` with a `task` shows this repository's review
running in the background, if any, and how its latest one ended: outcome, round and report. The result arrives as a
`review.finished` message from `jackstay`; read it with `chat.inbox` and acknowledge it with
`chat.ack`. A result the chat could not take when the round ended is kept and delivered the next
time the session reads its inbox or reaches a configured hook.

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
