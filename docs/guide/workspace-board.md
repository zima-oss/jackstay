# Workspace board

Use the board to see local work across checkouts you name explicitly:

```sh
jst workspace board ~/src/app ~/src/service
jst workspace board --json ~/src/app ~/src/service
```

Each row summarizes active card IDs and states, the latest recorded gate and stored passed run,
and session chat counts. The board reads local state only. It does not query GitHub, inspect
running agent turns, or merge anything. Linked worktrees share chat state; cards, gates, and
receipts describe the named checkout. Independent repositories stay
separate even when they use the same project name.

A recorded gate or receipt is `current` when its recorded HEAD matches the checkout's HEAD, and
`stale` when it does not. This does not verify uncommitted changes or whether an old receipt
remains reusable. Use `jst test last` and `jst test gate` for a delivery decision.

`disabled` means the feature is off in that project's configuration. `empty` or `none` means
it is configured but has no local record. `unknown` means a source could not be read or judged;
the board still shows the other sources and repositories. Exit status 1 and JSON `partial: true`
call attention to any unknown field. Exit status 2 means the arguments are invalid. The
machine output has `schema: 1` and `observed_at` so clients can evaluate the snapshot age.

Chat `available` means the local chat namespace is readable, even with zero sessions. Its
session count follows chat retention, including old recipients with pending mail; its pending count excludes acknowledged or expired
messages. It does not prove that an agent is online. The board prints no chat body or session
identity, card body, test output, or receipt material. Its command arguments are excluded from
local telemetry, and the board does not write project or Jackstay state.

Supply at most 32 paths in one call. A path inside a checkout resolves to its top level. The
board requires that top level to have `jackstay.json`. A missing path produces an `unknown` row;
the remaining paths are still inspected.

The full source and status contract is in the [workspace board specification](../roadmap/workspace-board.md).

In a local pilot with three independent repositories sharing one project name and one linked
worktree, the board returned four separate rows. The linked worktree had its own active cards and
no gate record, while its main checkout had a gate record. Chat state followed the shared Git
directory; an older gate was reported `stale`. A malformed card in another repository made the
response partial without hiding its readable chat fields. The pilot compared file contents and modification times before and after a
read-only call and found no changes.
