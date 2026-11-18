# W-004 — One machine, sequential numbers from one counter, and a card closed by the pull request that carries its code

Date: 2026-11-14.

## Context

jackstay coordinates several AI coding agents working on one machine at once. It had to decide
where coordination state lives, how numbered items (cards, decisions, records) get their numbers
without two agents taking the same one, and how a card is tied to the code that finished it.

## Decision

- **One machine.** Coordination state (the number counter, the test lock, the process table) is
  local to the machine. Cloud agents and a second machine are out of scope.
- **Sequential numbers from one counter.** Numbers stay sequential, not random. One atomic counter,
  shared by every worktree of the machine, hands them out (`jst id next`). Records of a series are
  numbered by the same counter, not inside their card (refined by
  [W-009](W-009-stage-1-scope-history-and-remote.md)).
- **A card is closed in the pull request that carries its code.** The card does not list commit
  hashes; its commits are found by the `Card: <ID>` line in the commit message, and a check on the
  pull request makes sure the line is there.

## Consequences

- A project's numbers never collide between branches, and gaps are allowed.
- Nothing jackstay keeps depends on commit hashes, so history can be rewritten (see
  [W-009](W-009-stage-1-scope-history-and-remote.md)).
- See [the specification](../design/implementation.md) sections 4.1 and 4.2, and the guide's
  [records](../guide/records.md).
