# W-006 — Agents tidy finished branches and worktrees with `jst tidy`; GitHub helpers and `jst owner`

Date: 2026-11-15.

## Context

Agents leave finished branches and worktrees behind, but the guard refuses the raw destructive
commands (`git branch -D`, `git worktree remove --force`, `rm -r`) because it cannot tell finished
work from unfinished by the text of a command. Agents also wait on GitHub with hand-written loops,
some of them wrong.

## Decision

- **Tidying is a command, not a loosening of the guard.** `jst tidy` reports finished branches and
  worktrees and removes them only with `--fix`, each after a check that nothing is lost. The guard's
  refusals point to it.
- **Finished** for a branch: its pull request is merged on GitHub and the local tip is the merged
  head, or the branch adds nothing to the default branch. An open pull request always keeps a
  branch. For a worktree: its branch is finished, it holds no uncommitted or untracked work, and no
  process is working in it.
- **GitHub helpers** under `jst gh`: `ci-wait`, `ci-status`, `run-wait`, `pr-find`, `pr-status`,
  `pr-land`.
- **`jst owner`** reads what the person running the agents actually said in a Claude Code
  transcript, in all three shapes a message arrives in (typed, queued, a question's answer).

## Consequences

- Cleaning up is safe by default: reporting is the default, removal is explicit and checked.
- See [the specification](../design/implementation.md) sections 8.3, 8.4 and 8.6, and the guide's
  [branches and GitHub](../guide/branches-and-github.md).
