# Claude Code

With [Claude Code](https://code.claude.com/docs), jackstay's guard refuses destructive commands
before they run, `jst bg` stops a session's processes when it ends, and Claude Code can review your
changes. `jst adopt` writes the hook and the `AGENTS.md` block it reads
([Adopting a project](adopt.md#the-agents-block)).

## What works without Claude Code

Everything but the hook. You and any agent can run every `jst` command from a shell, and
`jst guard check -- 'rm -r build'` judges a command without a hook. What needs Claude Code:

| Feature | Without Claude Code |
|---|---|
| The guard refusing commands before they run | not applied; use `jst guard check`, or the [Codex hook](codex.md#the-guard-as-a-codex-hook) |
| `jst bg` stopping a command when its session ends | the deadline alone (and the nearest parent process that looks like a session's) |
| `jst owner`, `jst baseline`'s transcript counts, `jst stats sessions --claude` | nothing to read |
| `jst doctor`'s sandbox check | relevant when the sandbox is enabled |
| Claude Code review rounds | choose Codex for the round, or read saved review reports |

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

To check that it works, tell `jst doctor` to expect the project, run any Bash command in a Claude
Code session there, and ask again:

```sh
jst doctor --expect my-service
jst doctor
jst log --command guard --since 1h
```

The walk-through with a probe command that the guard must refuse:
[Start in five minutes](getting-started.md#4-see-the-guard-refuse-a-command). What the guard
refuses: [The guard](guard.md).

## The sandbox

Claude Code's Bash sandbox writes only the folders its settings allow. jackstay keeps two kinds of
things in `~/.jackstay`. Some it writes while an agent's command runs in the sandbox; the rest is
evidence it later trusts (receipts, gate marks, review rounds, the run log), which the sandbox may
only read, so an agent cannot record a pass it did not run. `jst test` and `jst review` record
that evidence, so they run outside the sandbox: inside it, `jst test` runs nothing and says so.
`jst setup` adds the rest to `sandbox.filesystem.allowWrite` in `~/.claude/settings.json`, one
set per project it knows:

| Path under `~/.jackstay` | Why jst writes it from a sandboxed command |
|---|---|
| `.all/telemetry`, `<project>/telemetry` | the journal |
| `.all/heavy-suite.lock`, its `.queue` and `.holder` | `jst lock` |
| `.all/procs` | `jst bg` and `jst procs` |
| `.all/session-chat` | chat commands |
| `.all/context-brief` | `jst brief save` |
| `<project>/ids` | `jst id next` |
| `.trash` | `jst discard` |

`jst doctor` reports `sandbox-blocks-state` when one of them is not writable (a new project, say:
run `jst setup` again) and `sandbox-writes-evidence` when an entry lets the sandbox write evidence,
such as the whole `~/.jackstay` an earlier setup added; `jst setup` replaces that entry, keeping a
backup of the settings. What else runs outside the sandbox:

| Command | Inside the sandbox |
|---|---|
| `jst review` with Codex | refused, exit 6: Codex must be able to write its login (`~/.codex`) |
| `jst tidy --fix` | keeps every worktree: the sandbox hides other processes, so it cannot prove a worktree unused |
| `jst procs stop`, a stop of something the sandbox did not start | refused by the system; jackstay names what to run outside |
| `jst lock` | exit 2 when the lock is not writable: run `jst setup` |
| `jst test`, any form that runs something | runs nothing: it records evidence the sandbox may only read |
| `jst review` | its rounds are evidence too: run it outside the sandbox |

## Claude Code as the reviewer

Install Claude Code 2.1.248 or newer. Check your login with `claude auth status`, or provide
`ANTHROPIC_API_KEY` through the environment. With a desktop login on macOS, run `jst review`
or `jst review pool next` from a context that can access the system credential store. Check
`claude auth status` in that same context if the adapter reports an authentication failure.
[Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference)

Choose Claude Code for one round:

```sh
jst review origin/main..HEAD --task parser-fix --reviewer claude-code --fresh
```

A task keeps its reviewers from round to round. To make Claude Code a project's default, list it
in `review.use`. Rounds, verdicts and the `review` section: [Reviews](review.md).

## What can go wrong

| What you see | Why |
|---|---|
| A hook error on every Bash call, and commands run unchecked | `jst-guard` is not at the path the settings name (127). Install, or fix the path. |
| `jst doctor`: `guard-never-called` / `expected-never-called` | No session has called the hook: the project's settings are not loaded (trust dialog), or the path is wrong. |
| `jst doctor`: `sandbox-blocks-state` | jst cannot keep its logs, locks, chat or jobs in sandboxed sessions; run `jst setup`, which adds only the folders listed above. |
| `jst doctor`: `sandbox-writes-evidence` | the sandbox may write receipts or gate marks; run `jst setup` to replace a whole-home entry, or narrow a broader one yourself. |
| `jst test`: `Run it outside the sandbox.` | the run records evidence the sandbox may only read; run that command outside it. |
| The guard says `config-unusable` on every call | `jackstay.json` is broken; the built-in rules still apply. Run `jst check`. |

The design: [the hook contract](../design/guard.md#the-hook-contract), [the hook's answers](../design/guard.md#7-the-hook).
