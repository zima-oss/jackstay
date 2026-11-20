# What is going on: `jst status`

`jst status` is one read-only overview of local work. It reads the journal, Git and jackstay's
local state; it writes nothing and contacts no remote service.

```sh
jst status                         # this repository, in detail
jst status --since 4h --limit 50   # a wider window, more events
jst status ~/src/app ~/src/service # one row per repository
jst status --json                  # one versioned JSON envelope
```

## One repository

With no path, `jst status` covers the repository you are in (or, with one path, that repository):

- **worktrees**: every checkout Git lists for the repository, with its branch.
- **sessions**: the Claude Code and Codex sessions the journal saw in the window, the worktree each
  worked in, when it was last seen and whether a guard decision was recorded for it.
- **events**: delivery stages in the window, oldest first: pull-request checks and landings, test
  runs and gates, review rounds and their agents (with token counts when recorded), and background
  job lifecycles. `--limit` keeps the newest (default 20, at most 1000).
- **brief**: the task id of the worktree's saved brief and whether it is still `current`.
- **chat**: the number of chat sessions and pending messages, when chat is enabled.
- **background**: the `jst bg` jobs started in the repository and their states. A job is placed by
  the repository `jst bg` recorded when it started (its Git folder's device and inode), so a
  renamed folder, a moved worktree or a renamed checkout does not hide it. A running job that can
  be placed nowhere is counted as unattributed, and the report is partial.
- **gate** and **receipt**: the last recorded gate and stored passed run of this checkout, and
  whether each is for its HEAD (`current`) or an older commit (`stale`).
- **cards**: the active cards and their states, when the project uses cards.

`--since` sets the window for sessions and events (default 24h, at most 720h).

A session or event belongs to a worktree through the path it recorded, or its trace ancestor's.
Linked worktrees share the repository's identity (derived from Git's common directory); an
independent clone, or a checkout nested inside another, keeps its own, even under the same project
name. Records without enough evidence show the runner as `unknown`.

## Several repositories

With two or more paths, or with none when this project's `jackstay.json` lists them, `jst status`
prints one row per repository:

```json
"status": {"repos": [".", "../service", "../site"]}
```

Paths in `status.repos` are relative to the folder of that `jackstay.json`. `jst status .` still
shows this repository in detail. At most 32 repositories are read in one call; one that cannot be
read gets an `unknown` row and does not hide the others.

## What the words mean

`disabled`: the project does not use that feature. `none`: it is used but nothing is recorded yet.
`unknown`: the source could not be read; its `error` says why. A gate or receipt that is `current`
matches HEAD; it says nothing about uncommitted changes. For a delivery decision use
`jst test last` and `jst test gate`.

Nothing private is printed: no message, brief, card or receipt text, and events carry no
arguments, prompts, paths or model names. Session ids and local paths do appear, so consider the
audience before sharing the output. The command's own arguments are kept out of the journal.

## JSON

`--json` prints one envelope:

```json
{
  "schema": 1,
  "kind": "repository",
  "observed_at": "2026-11-20T10:00:00Z",
  "since": "2026-11-19T10:00:00Z",
  "until": "2026-11-20T10:00:00Z",
  "partial": false,
  "repositories": [{"path": "…", "project": "…", "counts": {"worktrees": 2, "sessions": 1, "events": 14, "omitted_events": 0}, "…": "…"}]
}
```

`kind` is `repository` (lists of worktrees, sessions and events filled) or `board` (counts only).
A change that removes or renames a field raises `schema`.

## Exit status

0: every source was read. 1: at least one source is `unknown`, or the journal was read only in
part (an unreadable line, a file that cannot be read, or more than 8192 spans to relate); the
report is still printed and `partial` is true. 2: the request is wrong, or `status.repos` cannot be read.
