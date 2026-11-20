# Getting started

Install jackstay, add its project files, run a test with evidence and check the guard. The first
three steps work in a terminal. The fourth checks the optional agent hook in a Claude Code
session.

## What an install leaves to you

The install script installs `jst` and changes nothing else. These steps are yours, and this page
walks through each:

| Step | Where | Needed for |
|---|---|---|
| Put `~/.local/bin` on `PATH` | your shell's profile; the script prints the exact line when it is missing | running `jst` |
| Let Claude Code's sandbox write jackstay's logs, locks, chat and jobs (`jst setup`) | `sandbox.filesystem.allowWrite` in `~/.claude/settings.json` | jst in sandboxed sessions; evidence stays read-only there ([Claude Code](claude-code.md#the-sandbox)) |
| `jst adopt --apply`, then commit what it wrote | each project | the guard hook and the `AGENTS.md` block |
| Accept Claude Code's trust dialog | the first session in each project | Claude Code loading the hook |
| Optional: install Codex or Claude Code and authenticate | your terminal | Review rounds with the selected provider ([Reviews](review.md)) |
| Optional: `gh auth login`, and `github.account` in `jackstay.json` | your terminal; the project | `jst gh` and GitHub in `jst tidy` ([Branches and GitHub](branches-and-github.md#the-account)) |

`jst doctor` checks what it can of these, and names the fix of each problem it finds.

## 1. Install

On macOS or Linux, x86-64 or arm64, in sh, bash or zsh (in fish or another shell, run `sh`
first):

```sh
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && sh -c "$script"
```

It installs `jst` into `~/.local/bin`, without `sudo`, after checking the download against the
release's checksums. When `~/.local/bin` is not on your `PATH`, it prints the line to add (for sh,
bash or zsh; for another shell, the folder); add it, open a new terminal, then:

```sh
jst doctor
```

If you use Claude Code with its sandbox on, run `jst setup`: it lets the sandbox write the folders
of `~/.jackstay` jst needs there (logs, locks, chat, jobs) and keeps evidence such as receipts and
gate marks read-only to it ([Claude Code](claude-code.md#the-sandbox)). `jst doctor` reports
`sandbox-blocks-state` until then.

To read the script first, or to install another way: [Install](install.md).

## 2. Adopt a project

In the project's repository:

```sh
cd my-service
jst adopt            # what jackstay would write here; nothing is written
jst adopt --apply    # write it
git add jackstay.json AGENTS.md CLAUDE.md .claude/settings.json
git commit -m "Adopt jackstay"
```

This writes `jackstay.json`, a block in `AGENTS.md` that teaches agents the tools, and the guard
hook in `.claude/settings.json`. More, and how to bring an organisation's rules with a profile:
[Adopting a project](adopt.md).

## 3. Run your tests through jackstay

Declare how your tests run, as a scope in `jackstay.json`. The `evidence` pattern finds the number
of tests in the output; only a run whose tests were counted is a pass:

```json
"test": {
  "judge": "steps",
  "scopes": {
    "quick": {"run": ["python3", "-m", "pytest", "-q"], "evidence": "([0-9]+) passed",
              "timeout": 600, "when": "while working"}
  }
}
```

```sh
jst check            # is jackstay.json usable
jst test quick
```

```
12 passed in 0.84s
jst test: the whole output: ~/.jackstay/my-service/outputs/18e9237caf5bb0c0-15904.log  (`jst test last` shows this again)
jst test: outcome=passed (0 s, counted=12)
```

Read the last `jst test:` line. `exited-0` means the command ended 0 but nothing counted its tests,
which is not a pass. After a failure, `jst test last` shows what went wrong without running again
(a run that warns it was not logged names its output file instead).
Add a `gate` scope for what a change must pass before it merges: [Tests](tests.md).

Run `jst adopt --apply` again after adding a section: the `AGENTS.md` block then tells agents about
it.

## 4. See the guard at work

Open a Claude Code session in the project and accept its trust dialog: Claude Code loads a
project's hooks only after it. Then ask the agent to run exactly this line in its shell:

```sh
false && rm -r jackstay-guard-probe
```

The line can delete nothing: `false` fails, so the shell never reaches `rm`. The guard reads the
whole line before it runs, and refuses it for the `rm -r`. With the hook working, the agent's shell
tool answers with that refusal, in the session itself:

```
jst guard refused this command; nothing was run.
- `rm -r jackstay-guard-probe`: a recursive rm outside temporary and regenerable folders cannot be undone: jackstay-guard-probe. Instead: move it to jackstay's trash, which can be undone: `jst trash <path>`; …
```

Only the hook can stop the agent's own command, so that answer is the proof. Anything else (the
line ran and ended with status 1, or a warning instead of a refusal) means the check failed: either
the hook is not loaded ([Claude Code](claude-code.md#what-can-go-wrong) says what to look at), or
the project's `jackstay.json` has set `rm-recursive` to `warn` or `off` ([The guard](guard.md#levels-and-your-own-rules)).

To ask the guard about a command yourself, without an agent, use `jst guard check -- 'COMMAND'`: it
judges the command as the hook would, whether or not any session has loaded the hook. What the
guard refuses, and how to change a rule's level: [The guard](guard.md). In Claude Code's sandbox,
`jst setup` lets jst write what it needs, and `jst test` and `jst review` run outside it:
[Claude Code](claude-code.md#the-sandbox).

Codex reviews by default:

```sh
jst review origin/main..HEAD --task my-change
```

Claude Code can review instead: choose it for a task with `--reviewer claude-code` (a task keeps
its reviewers from round to round), or for the project in `review.use`.

See [Reviews](review.md) for the result and the check that ties it to a revision.

## Next

- [Tests](tests.md): scopes, the gate, receipts.
- [Branches and GitHub](branches-and-github.md): worktrees per task, tidying what is finished.
- [Background processes](background.md): servers that stop at a deadline.
