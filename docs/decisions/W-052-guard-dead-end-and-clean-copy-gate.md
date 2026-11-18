# W-052 — `jst discard` ends the guard's keep-a-copy dead end; the gate runs on a clean copy it removes

Date: 2026-11-17.

## Context

The guard refused `git restore <path>` and `git checkout -- <path>` over uncommitted changes and
advised keeping a copy first; after the copy was kept, the same command was refused again, so an
agent following the advice could never get through. Separately, agents made clean copies of a
commit by hand to run the gate and left them behind.

## Decision

- **`jst discard <path>…`** keeps the change in jackstay's trash, restorable with
  `jst trash restore`, then restores the file to HEAD.
- **No advice leads to a refusal.** The guard's advice points at `jst discard` (and `jst trash`).
- **`jst test <scope> --commit REV`** runs a scope (meant for `gate`) on a clean copy of that
  commit, which it pins, locks and always removes.

## Consequences

- An agent never has to ask a person to unblock a refused restore.
- No stray worktrees are left by gate runs.
- See [discard and the clean gate](../design/discard-and-clean-gate.md) and the guide's
  [guard](../guide/guard.md).
