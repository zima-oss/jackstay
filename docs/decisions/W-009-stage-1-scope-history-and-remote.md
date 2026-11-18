# W-009 — The `Card:` line and its check, the number counter, one file per record and change entry, the process registry, and a snapshot before a history collapse

Date: 2026-11-15.

## Context

jackstay ties work to cards, hands out numbers, and keeps ledgers and changelogs. Files that every
change appends to (ledgers, changelogs) caused most merge conflicts between parallel agents, and
background processes started by agents were lost track of. History rewrites also had to be
considered, since the link from a card to its commits lives in commit messages.

## Decision

- **The `Card:` line.** A change says which card it belongs to with a `Card: <ID>` line in its
  commit or pull-request message; `jst cards` reads what landed from history and `jst lint` checks
  that the line is there. `Card: none (<reason>)` states that a change belongs to no card.
- **The counter** (`jst id`, [W-004](W-004-stage-0-and-ground-rules.md)) numbers every series of a
  project: cards, decisions and records alike.
- **One file per record and per changelog entry** (`jst new`, `jst view`, `jst changes`); a
  rendered table is never committed.
- **A registry of background processes** (`jst bg`, `jst procs`).
- **History.** jackstay writes no commit hash into any file it keeps. A rewrite that keeps messages
  keeps the `Card:` lines. Before a collapse of old history into one commit, `jst cards snapshot`
  writes a snapshot file once, never edited; `jst cards` then reads old cards from snapshots and new
  ones from history.

## Consequences

- Parallel branches no longer conflict on shared ledgers or changelogs.
- See [the specification](../design/implementation.md) sections 4 and 5.1, and the guide's
  [records](../guide/records.md) and [background](../guide/background.md) pages.
