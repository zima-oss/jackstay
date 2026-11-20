# Codex

[Codex CLI](https://github.com/openai/codex) is jackstay's default reviewer for `jst review`, and
Codex sessions can load jackstay's guard hook.

Without Codex, every `jst` command still runs: a review round can choose Claude Code instead, and
stored reviews stay readable by `jst review check`, `last`, `list`, `show` and `prune`.

## Codex as the reviewer: `jst review`

Install Codex CLI 0.169.0 or newer (for scoped review permissions) and log in once:

```sh
codex login
codex --version
```

Choose Codex for a round:

```sh
jst review origin/main..HEAD --task parser --reviewer codex
```

`jst review` starts Codex in a read-only view of the pinned revisions, where it can use local shell
navigation and search, under a filesystem permission profile of its own. By default it also turns
off your own Codex config, rules, MCP servers and apps. The optional
`review.reviewers.codex.isolation` list customizes launch options within the required permissions.
An answer that fails schema validation is `unparsable` (exit 5).
See [the review design](../design/review.md#1-the-review-section-of-jackstayjson).

Set the model and effort per project, or per call:

```json
"review": {"reviewers": {"codex": {"command": "codex", "model": "", "effort": "medium", "timeout_minutes": 20}}}
```

```sh
jst review origin/main..HEAD --task parser --reviewer codex --effort high
```

`command` may be an absolute path when `codex` is not on the `PATH` of the shell that runs
`jst review`.

**Run `jst review` outside an agent sandbox.** Codex refreshes its login by writing
`~/.codex/auth.json`; a refresh where that file cannot be written can lose the login. jackstay checks
that it can write the file before it starts Codex, and refuses with exit 6 otherwise. After Codex
reports a login failure, every `jst review` refuses until `codex login` has been run again.

Rounds, verdicts and the `review` section: [Reviews](review.md).

## The guard as a Codex hook

Codex has a `PreToolUse` hook for its shell tool with the same input and the same `deny` answer as
Claude Code's, so `jst-guard` serves it too. `jst adopt` does not write it; add it yourself in the
project's `.codex/hooks.json`, as a `PreToolUse` hook with matcher `Bash` whose command is
`"$HOME/.local/bin/jst-guard"`, in the format Codex's hooks documentation gives.

Know its limits before you rely on it:

- Codex loads a project's hooks only when the project's `.codex/` layer is trusted **and** you have
  approved the hook in `/hooks`. The approval is tied to the hook's text: an edit needs approval
  again. Until then Codex runs without the guard and says nothing.
- `codex exec` skips a hook that was not approved, silently.
- In a linked worktree Codex reads the hooks of the **main checkout's** `.codex/hooks.json`; the
  worktree's own file is ignored (the worktree still needs a `.codex/` folder).

So check that it is heard from: tell `jst doctor` to expect the project, run one shell command in
a Codex session there, and ask again.

```sh
jst doctor --expect my-service
jst doctor
```

What the guard refuses and why is on the [guard page](guard.md).

## What can go wrong

| What you see | Why |
|---|---|
| `jst review` exit 6, "Codex is not installed" | `codex` is not on `PATH` (or the reviewer's `command`). Install it. |
| `jst review` exit 6, "Codex cannot save its login here" | Run inside a sandbox. Run it outside. |
| `jst review` exit 6, "run `codex login`" | No `auth.json`, or a login failure since the last login. |
| `jst review` exit 5 | Codex timed out, answered outside the schema, or no slot was free; `jst review last` says which. |
| The Codex hook never runs | Not trusted or not approved in `/hooks`, or a worktree whose main checkout lacks the file. |

The design: [review.md](../design/review.md), [the guard and Codex](../design/guard.md#codex).
