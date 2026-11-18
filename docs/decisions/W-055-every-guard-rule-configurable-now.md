# W-055 — Every guard rule takes a level, judged by one independent check per rule; the rows that stay fixed

Date: 2026-11-17.

## Context

Making rules configurable ([W-053](W-053-guard-rules-configured-per-project.md)) exposed a problem:
several git rules shared one judge, so loosening one rule could hide another.

## Decision

- Every guard rule takes a level (`refuse`, `warn`, `off`).
- The git judges are split into one check per rule: no check depends on another rule's outcome, so
  `off` or `warn` on one rule cannot hide another.
- Not configurable: the parser's own refusals (`unparsable` and its kin) and the rows that say a
  command cannot be judged (`git-config-alias`, `git-config-worktree`, `env-split-string`,
  `card-push-unreadable-config`).
- Path exceptions belong to the deleting rules (`rm-recursive`, `find-delete`) and hold only for a
  line that is one plain `rm` or `find` call with no symbolic link on the target's way.

## Consequences

- See [discard and the clean gate](../design/discard-and-clean-gate.md) section 9 and the
  [guard's design](../design/guard.md).
