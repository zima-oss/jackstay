# Upgrading from 0.5.x

jackstay 0.6.0 reads a 0.5.x project's `jackstay.json` as it is, except a `resume` key under
`test.scopes` or `test.steps`, which `jst check` names for you to delete. Upgrade the machine
first, then each project in three pull requests, so a project's CI, still pinned to 0.5.x, never
meets a key it cannot read.

## 1. The machine

Update as in [Install](install.md#update); `jst version` then prints `jackstay 0.6.0`. Then:

```sh
jst setup --plan
jst doctor
```

`jst setup` asks, once per machine, who merges a change and who reviews; adopt offers those answers
first. It also narrows what Claude Code's sandbox may write: the folders `jst` writes from inside
it, and no longer receipts, gate marks or review rounds, so run `jst test` and `jst review` outside
the sandbox ([Claude Code](claude-code.md)). `jst doctor` names anything left to fix.

## 2. Each project, in three pull requests

A file with `"jackstay_schema": 3` is refused by 0.5.10 and 0.5.11, and the project's CI downloads
the jackstay its `adopt.pin` names. `jst adopt --plan` plans the three steps and names the one that
is due; a person merges each before the next.

1. **The pin.** It changes `jackstay.json` alone:

   ```sh
   jst adopt --apply --pin --settings-only
   ```

2. **The files.** On the merged base, the AGENTS block, the hook and the workflows that download
   0.6.0:

   ```sh
   jst adopt --apply
   ```

   If the project keeps a workflow adopt does not manage, the plan waits until a person confirms,
   with the exception `upgrade:workflows`, that none of them still downloads an older jackstay.

3. **Schema 3.** `"jackstay_schema": 3`, the `land` section and the other new keys. At a terminal,
   adopt asks who merges (`--autonomy`), who reviews (`--reviewer`) and whether the agent chat is
   on (`--chat`); without one, give the flags:

   ```sh
   jst adopt --apply --autonomy merge-when-green --reviewer codex --chat off
   ```

[Adopting a project](adopt.md) explains the plan and its conflicts; every new key is in the
[configuration reference](../reference/configuration.md).

## What was removed

A removed command runs nothing; its error names the replacement.

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
