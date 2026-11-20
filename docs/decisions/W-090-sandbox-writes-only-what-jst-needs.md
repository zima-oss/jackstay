# W-090 — The agent sandbox writes only what jst needs; evidence stays read-only there

Date: 2026-11-20.

## Context

`jst setup` added the whole jackstay home to Claude Code's `sandbox.filesystem.allowWrite`
(W-078). The same folder holds what jackstay later trusts: gate marks (`jst test gate --resume`
treats a matching one as a passed step), receipts, review rounds and the run log. With that grant
an agent inside the sandbox could write a gate mark and get a green gate without running it
(#59). The owner chose to grant only the needed subfolders (W-090 in the owner's record).

## Decision

- Setup grants, by reading what jst writes from a sandboxed Bash command: the journals (shared
  and per project), each project's ids, the heavy-suite lock with its queue and holder note,
  `procs`, `chat`, `session-chat`, `context-brief` and the trash. Hooks and stdio MCP servers run outside
  the sandbox and need nothing.
- Receipts and failure notes, gate marks and bars, review rounds, the run log, the delivery record
  and the expected projects are never granted. Granting the negative records (failure notes, bars)
  would not do: write access also deletes them, which re-opens an earlier pass to reuse.
- So `jst test` and `jst review` run outside the sandbox. Every `jst test` that runs something
  writes the run log, and most also receipts or gate marks. Before running, it opens the run log for
  appending and checks it can make a file in the project's receipts, gate marks and bars; it then
  removes the receipt or mark the run would replace. When any of that fails, inside the sandbox or
  under an owner's deny rule, it runs nothing and says to run it outside the sandbox. A run whose
  log line still cannot be written at the end (a full disk) prints its output path instead of
  pointing at `jst test last`. The AGENTS block says so.
- Per-project grants are written for each project folder in the home and each expected project,
  as plain paths (glob support differs between Claude Code's macOS and Linux sandboxes). Doctor
  reports a project whose grants are missing; running setup again adds them.
- Doctor reports `sandbox-writes-evidence` when an allowWrite entry covers evidence. Setup removes
  an entry naming the home itself and leaves any broader entry to the owner.
- The settings file keeps its key order (the ordered model adopt uses), a backup beside it, and
  the plan's diff shows only the entries removed and added. A linked settings file or shell
  profile is named in the error and left alone.

## Consequences

An agent in the sandbox can no longer forge evidence, and must ask to run every `jst test` that
runs something (raw, a scope, the gate) and `jst review` outside it. `jst test` checks the run log
and the project's receipts, gate marks and bars where they are, so an owner's deny rule on one of
them stops it too. A new project's journal and ids need one more `jst setup` before sandboxed commands
write them; until then the journal loses those spans and `jst id next` fails, and doctor says so.
