# Codex

jackstay can use [Codex CLI](https://github.com/openai/codex) as a reviewer selected for
`jst review`. Codex sessions can also load jackstay's guard hook.

## What works without Codex

`jst` commands run without Codex. A review round can select Claude Code instead. Stored reviews
remain available to `jst review check`, `last`, `list`, `show` and `prune`, and `jst stats reviews`
reports them. The AGENTS block written by `jst adopt` mentions review when the project has a
`review` section. Reviewer selection is described on the [reviews page](review.md).

## Codex as the reviewer: `jst review`

Install Codex and log in once, in your own terminal:

```sh
codex login
codex --version
```

Choose Codex for a round:

```sh
jst review origin/main..HEAD --task parser --reviewer codex
```

`jst review` starts Codex read only in a detached worktree at the reviewed commit, so Codex
cannot change your checkout. It is isolated from your own Codex setup by default (no user config,
rules, MCP servers or apps); `review.reviewers.codex.isolation` replaces that list. An answer that
does not fit the review's schema is `unparsable` (exit 5), never guessed at. How Codex is started,
exactly, is in [the review design](../design/review.md#1-the-review-section-of-jackstayjson).

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

The [reviews page](review.md) covers rounds, verdicts and the `review` section.

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

So check that it is heard from:

```sh
jst doctor --expect my-service
# … one shell command in a Codex session in that project …
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
