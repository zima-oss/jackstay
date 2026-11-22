# Upgrading from 0.5.x

jackstay 0.6.0 reads a 0.5.x project's `jackstay.json` as it is, except a `resume` key under
`test.scopes` or `test.steps`, which `jst check` names for you to delete. Upgrade the machine
first, then each project in three stages, so a project's CI, still pinned to 0.5.x, never meets a
key it cannot read.

## 1. The machine

Update as in [Install](install.md#update); `jst version` then prints `jackstay 0.6.0`. Then:

```sh
jst setup --plan
jst setup
jst doctor
```

`jst setup --plan` shows what setup would change, and `jst setup` applies it at a terminal. It asks,
once per machine, who merges a change and who reviews; adopt offers those answers
first. It also narrows what Claude Code's sandbox may write: the folders `jst` writes from inside
it, and no longer receipts, gate marks or review rounds, so run `jst test` and `jst review` outside
the sandbox ([Claude Code](claude-code.md)). `jst doctor` names anything left to fix.

## 2. Each project, one pull request at a time

A file with `"jackstay_schema": 3` is refused by 0.5.10 and 0.5.11, and the project's CI downloads
the jackstay its `adopt.pin` names. So adopt moves a project in steps, each a pull request a person
merges before the next: the pin, then the workflows that download 0.6.0, then schema 3 with the
`land` section. You do not sequence them, or count the pull requests; the plan does:

```sh
jst adopt --plan
```

Repeat, on the merged base each time, until the plan finds nothing to change:

1. `jst adopt --plan` names the step that is due, and any blocker that waits on you.
2. `jst adopt --apply --settings-only` (with `--pin` when the step is the pin) writes
   `jackstay.json` alone: the pin, schema 3, and adopt's answers. If it wrote anything, land it in a
   pull request of its own (rule D0 of `jst lint pr`) and start again.
3. Only when it writes nothing, `jst adopt --apply` writes the other files (the workflows, the
   AGENTS block). Land them.

Adopt asks, at a terminal, who merges and who reviews, and, once the file is schema 3, whether the
agent chat is on; each answer goes to `jackstay.json`, which is why the `--settings-only` apply comes
first: the chat question, asked once the file is schema 3, is answered there too. Without a terminal, the plan names the flag for each: `--autonomy`, `--reviewer`, `--chat`. If the project keeps a workflow adopt does not
manage, the plan waits until a person confirms, with the exception `upgrade:workflows`, that none
of them still downloads an older jackstay.

[Adopting a project](adopt.md) explains the plan and its conflicts; every new key is in the
[configuration reference](../reference/configuration.md).

## What was removed

A removed command runs nothing; its error names the replacement.

**Which checks stop a merge changed.** 0.5.x's `pr-land` stopped on any failed check; `jst land`
waits only for the checks the base's rules require and those `land.checks` lists; any
other check is reported and never stops the merge. Make a check that must block required on the
host, or list it in `land.checks`, before you land with `jst land`.

| 0.5.x | 0.6.0 |
|---|---|
| `jst gh pr-land` | `jst land` (`--pr` is `--change`, `--dry-run` is `--plan`, `--no-merge` is `--prepare`) |
| `jst gh pr-status`, `jst gh pr-find` | `jst land status`, `jst land find` |
| `jst gh ci-wait`, `ci-status`, `run-wait` | `jst checks wait`, `jst checks status`, `jst checks run-wait`, same flags |
| `jst test gate --resume`, a `resume` key in `test.scopes` or `test.steps` | removed: a gate runs every scope it needs each time. `jst check` names the key to delete |
| `gh pr merge`, `glab mr merge` | refused by the guard: land with `jst land` |

There is no test cache in 0.6.0: a scope that passed is not skipped, except where a receipt of the
same tree is reused ([Tests](tests.md)).

The agent chat has a new store: 0.6.0 neither reads nor imports the 0.5.x chat's messages, so
answer pending requests and read pending messages before you switch a machine. The chat's 0.5.x
tool names keep working through 0.6.x: `chat.self` is `chat.whoami`, `chat.reply` is `chat.send`
with `reply_to`, `chat.send` takes `body` for `message` and `kind: request` for a request, and
`chat.ack` marks a message `acted` ([Session chat](session-chat.md)). The hook
is now `jst hook claude`; `jst chat claude-hook` is its old name.

## What is new

- **Landing** with [`jst land`](branches-and-github.md#jst-land): push, open the change request,
  wait for the required checks, merge, or stop for a person, as `land.autonomy` says.
- **Tests as a graph** of scopes, with `deps`, and a split between what runs here and what CI runs
  ([Tests](tests.md)).
- **One overview**, [`jst status`](status.md), of this repository or of several.
- **Reviews** by Codex, Claude Code or both, in the background if you like, with only the missing
  reviewers run again after an interruption ([Reviews](review.md)).
- **Agent chat** between sessions, which rings Claude Code when a message waits
  ([Session chat](session-chat.md)).
- **Compaction briefs**, portable receipts, adoption profiles as bundles, and an editor schema for
  `jackstay.json`.

Why 0.6.0 works this way: [W-090](../decisions/W-090-compatibility-of-0-6-0.md).
