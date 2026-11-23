# Start in five minutes

By the end of this page one of your projects is adopted, its tests run through jackstay and are
counted, and you have seen the guard refuse a command. Steps 1 to 3 work in any terminal; step 4
needs a Claude Code session.

## 1. Install

On macOS or Linux, in sh, bash or zsh (in fish or another shell, run `sh` first):

```sh
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && sh -c "$script"
```

It installs `jst` into `~/.local/bin` without `sudo`. If that folder is not on your `PATH`, the
script prints the line to add; add it, open a new terminal, then check the machine:

```sh
jst doctor
```

Each problem it reports is followed by a `fix:` line. If you use Claude Code with its sandbox on,
run `jst setup`: it lets the sandbox write the folders jst needs there and keeps evidence such as
receipts read-only to it, so `jst test` and `jst review` run outside the sandbox
([Claude Code](claude-code.md#the-sandbox)). To read the script first, or to install another way:
[Install](install.md).

## 2. Adopt a project

In the project's repository, look at the plan, write it, and commit what it wrote:

```sh
cd my-service
jst adopt
jst adopt --apply
git add jackstay.json AGENTS.md CLAUDE.md .claude/settings.json
git commit -m "Adopt jackstay"
```

You now have a `jackstay.json`, a block in `AGENTS.md` that tells agents which `jst` commands to
use, and the guard hook in `.claude/settings.json`. Profiles, conflicts and later updates:
[Adopting a project](adopt.md).

## 3. Run your tests through jackstay

Tell jackstay how your tests run: add a scope to `jackstay.json`. The `evidence` pattern finds the
number of tests in the output, because only a run whose tests were counted is a pass:

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
jst check
jst test quick
```

```text
12 passed in 0.84s
jst test: the whole output: ~/.jackstay/my-service/outputs/18e9237caf5bb0c0-15904.log  (`jst test last` shows this again)
jst test: outcome=passed (0 s, counted=12)
```

Read the last `jst test:` line. `exited-0` means the command ended 0 but nothing counted its tests:
that is not a pass. After a failure, `jst test last` shows what went wrong without running the
tests again.

Run `jst adopt --apply` again whenever you add a section to `jackstay.json`, so the `AGENTS.md`
block tells agents about it. More scopes, the gate and receipts: [Tests](tests.md).

## 4. See the guard refuse a command

Open a Claude Code session in the project and accept its trust dialog: Claude Code loads a
project's hooks only after that. Ask the agent to run exactly this line:

```sh
false && rm -r jackstay-guard-probe
```

The line can delete nothing, because `false` fails and the shell never reaches `rm`. The guard
reads the whole line before it runs and refuses it for the `rm -r`. The agent's shell tool
answers:

```text
jst guard refused this command; nothing was run.
- `rm -r jackstay-guard-probe`: a recursive rm outside temporary and regenerable folders cannot be undone: jackstay-guard-probe. Instead: delete it with `jst trash <path>`, which logs what it deletes; …
```

Only the hook can stop the agent's own command, so that answer proves the hook works. If the line
ran instead, or you got a warning, either the hook is not loaded
([Claude Code](claude-code.md#what-can-go-wrong)) or the project has set `rm-recursive` to `warn`
or `off` ([The guard](guard.md#levels-and-your-own-rules)).

Without an agent, ask the guard yourself: `jst guard check -- 'rm -r build'` judges a command as
the hook would.

## Next

- [Tests](tests.md): more scopes, the gate, receipts.
- [The guard, discard and trash](guard.md): what is refused and how to undo a discard.
- [Reviews](review.md): a cold review by Codex or Claude Code.
- [Branches and GitHub](branches-and-github.md): a worktree per task, tidying what is finished.
- [Background processes](background.md): servers that stop at a deadline.
