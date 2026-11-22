# W-090 — One `jst status` replaces the timeline, the inventory and the workspace board

Date: 2026-11-20.

## Context

Three read-only overview commands came in one wave and were never released: `jst timeline`
(delivery stages from the journal), `jst inventory` (worktrees and observed sessions) and
`jst workspace board` (cards, merge queue, gate, receipt and chat across named repositories). They
overlapped, each scanned the journal or Git on its own, and the board read other packages' state
files directly. The owner chose one command (W-090 in the owner's record, "Overview, setup and
docs").

## Decision

- `jst status` is the one overview. With no path it shows this repository in detail: worktrees,
  sessions and delivery events from one journal scan, chat counts, the brief, background jobs, the
  last gate and receipt, active cards. With one path it shows that repository; with several, or
  with none when `status.repos` lists them, one row per repository.
- `--json` prints one envelope (`schema` 1, `kind` `repository` or `board`); `--since` sets the
  window, `--limit` the events kept. Exit 0 complete, 1 partial, 2 a wrong request (an unusable
  `status.repos` included).
- It reads other packages through small public read functions (`receipts.LatestRuns`,
  `contextbrief.SummaryOf`, `background.Jobs`, the chat store's overview), never their files.
- The merge-queue source is not carried over: the local queue leaves with the thin `jst land`
  (W-092), whose host adapter reads the host's own queue.
- The three commands, their packages and their AGENTS-block items are removed; the block gets one
  `jst status` item. As none was released, no alias is kept.

## Consequences

A tool that parsed the old JSON reports moves to the envelope: the timeline's rows are a
repository's `events`, the inventory's lists its `worktrees` and `sessions`, the board's rows the
envelope's `repositories`. The roadmap's board specification is kept as history.
