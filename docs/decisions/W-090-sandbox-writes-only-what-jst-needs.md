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
  and per project), each project's test outputs and ids, the heavy-suite lock and its queue,
  `procs`, `session-chat`, `context-brief` and the trash. Hooks and stdio MCP servers run outside
  the sandbox and need nothing.
- Receipts and failure notes, gate marks and bars, review rounds, the run log, the delivery record
  and the expected projects are never granted. `jst test gate` and `jst review` run outside the
  sandbox; `jst test SCOPE` inside it runs and keeps its output, without a receipt.
- Per-project grants are written for each project folder in the home and each expected project,
  as plain paths (glob support differs between Claude Code's macOS and Linux sandboxes). Doctor
  reports a project whose grants are missing; running setup again adds them.
- Doctor reports `sandbox-writes-evidence` when an allowWrite entry covers evidence. Setup removes
  an entry naming the home itself and leaves any broader entry to the owner.
- The settings file keeps its key order (the ordered model adopt uses), a backup beside it, and
  the plan's diff shows only the entries removed and added. A linked settings file or shell
  profile is named in the error and left alone.

## Consequences

An agent in the sandbox can no longer forge evidence, and must ask to run the gate and reviews
outside it. A new project's journal and ids need one more `jst setup` before sandboxed commands
write them; until then the journal loses those spans and `jst id next` fails, and doctor says so.
