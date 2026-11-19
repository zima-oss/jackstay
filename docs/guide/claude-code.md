# Claude Code

jackstay can connect to [Claude Code](https://code.claude.com/docs) through a `PreToolUse` guard
hook, an `AGENTS.md` tools block and an optional code reviewer. Each connection is selected by
the project's configuration or the review command.

## What works without Claude Code

Everything but the hook. You and any agent can run every `jst` command from a shell; `jst guard
check -- 'COMMAND'` judges a command without a hook. What needs Claude Code:

| Feature | Without Claude Code |
|---|---|
| The guard refusing commands before they run | not applied; use `jst guard check`, or the [Codex hook](codex.md#the-guard-as-a-codex-hook) |
| `jst bg` stopping a command when its session ends | the deadline alone (and the nearest parent process that looks like a session's) |
| `jst owner`, `jst baseline`'s transcript counts, `jst stats sessions --claude` | nothing to read |
| `jst doctor`'s sandbox check | relevant when the sandbox is enabled |
| Claude Code review rounds | choose Codex for the round, or read saved review reports |

Select Claude Code for one review round:

```sh
jst review origin/main..HEAD --reviewer claude-code --task my-change
```

The [review guide](review.md#enabling-claude-code) covers CLI version requirements,
credentials, restricted execution and the recorded verdict.

For coordination between sessions, register the optional [MCP session chat](session-chat.md)
and its prompt/tool-batch hooks. A hook announces pending messages at the next Claude Code
boundary, and the recipient acknowledges processing through `chat.ack`.

## The guard hook

`jst adopt --apply` adds this to the project's `.claude/settings.json` (committed, so every
session and worktree has it):

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {"type": "command", "command": "\"$HOME/.local/bin/jst-guard\"", "timeout": 10}
        ]
      }
    ]
  }
}
```

Each Bash call, a subagent's included, is then judged before it runs:

- **Refused**: the hook answers `permissionDecision: deny` with the reason and what to run instead;
  the agent sees it, and the command does not run.
- **Warned** (a rule at level `warn`, `zsh-glob` by default): the hook answers with
  `additionalContext` for the agent and `systemMessage` for you, and **no** permission decision, so
  the call goes through your own permission settings unchanged. The agent reads a line beginning
  `jst guard let this command run, with a warning`.
- **Allowed**: nothing is said.
- **The guard's own fault**: the command runs, and you are told the guard did not check it
  (`failed_open`, counted by `jst doctor`).

The hook answers within 5 seconds and exits within 8; the settings give it 10. Claude Code loads a
project's hooks only after you accept its trust dialog. Only the Bash tool is checked.

If you installed jackstay under another prefix, change the command to that prefix's
`bin/jst-guard`; `jst adopt` accepts any `PreToolUse` Bash hook whose command ends in `jst-guard`.

Check that it works:

```sh
jst doctor --expect my-service     # complain if this project's guard is never heard from
# … run any Bash command in a Claude Code session in that project …
jst doctor
jst log --command guard --since 1h
```

## The sandbox

Claude Code's Bash sandbox writes only the folders its settings allow. jackstay writes its state to
`~/.jackstay`: run records, receipts, the trash, the journal, background process entries, counters.
Allow it in the sandbox's `filesystem.allowWrite` list (in `~/.claude/settings.json`, or the
project's settings), for example:

```json
{
  "sandbox": {
    "enabled": true,
    "filesystem": {"allowWrite": ["~/.jackstay"]}
  }
}
```

`jst doctor` reads `~/.claude/settings.json` and reports `sandbox-blocks-state` when the sandbox is
on and `~/.jackstay` is not writable. Some commands must still run outside the sandbox:

| Command | Inside the sandbox |
|---|---|
| `jst review` with Codex | refused, exit 6: Codex must be able to write its login (`~/.codex`) |
| `jst tidy --fix` | keeps every worktree: the sandbox hides other processes, so it cannot prove a worktree unused |
| `jst procs stop`, a stop of something the sandbox did not start | refused by the system; jackstay names what to run outside |
| `jst lock`, `jst test` with the lock | exit 2 when `~/.jackstay` is not writable |

`jst trash` exists because the system's Trash cannot be written from inside the sandbox.

## Background commands

Start long-lived commands as `jst bg --for MIN --name N -- COMMAND` with the Bash tool's background
flag. The guard notes which session each process serves, so `jst bg` can stop the command when that
session ends. The guard counts background calls made without `jst bg` (`jst stats background`)
but refuses none.

## The AGENTS block

`jst adopt` writes a block into `AGENTS.md` between `jackstay-adopt` markers, and a `CLAUDE.md`
holding `@AGENTS.md` when there is none (Claude Code reads `CLAUDE.md`). The block lists, one line
per task, the tools this project has enabled: run tests, wait, background, branches, discard, the
guard, GitHub, review, cards, numbers, changelog, releases, what happened, healthy here. It names
only commands the installed jackstay has and sections the project's `jackstay.json` has.

```sh
jst adopt --block      # print the block this project gets
```

Do not edit inside the markers: `jst adopt` finds a hand edit by its hash and stops with the
conflict `agents:edited`. A profile adds lines (`agents.rules`) or leaves items out
(`agents.leave_out`).

## What can go wrong

| What you see | Why |
|---|---|
| A hook error on every Bash call, and commands run unchecked | `jst-guard` is not at the path the settings name (127). Install, or fix the path. |
| `jst doctor`: `guard-never-called` / `expected-never-called` | No session has called the hook: the project's settings are not loaded (trust dialog), or the path is wrong. |
| `jst doctor`: `sandbox-blocks-state` | Runs inside sessions are not recorded; allow `~/.jackstay`. |
| The guard says `config-unusable` on every call | `jackstay.json` is broken; the built-in rules still apply. Run `jst check`. |

The design: [the hook contract](../design/guard.md#the-hook-contract), [the hook's answers](../design/guard.md#7-the-hook),
[the AGENTS block](../design/adopt.md#8-the-agents-block).
