# Backlog

The backlog is the [issue list](https://github.com/zima-oss/jackstay/issues) on GitHub; an issue
holds the request, its discussion and its state. This page says where jackstay goes next and groups
the open issues by the part of jackstay they change. When an issue closes, its row goes.

## Next: jackstay 1

jackstay 1 is the next major version. What the specifications already name for it:

- **A test cache by input hash**: a scope that passed with the same inputs is not run again
  ([land.md section 7.3](../design/land.md#73-the-cache-not-in-060-later)). 0.6.0 removed
  `--resume` and its marks until then.
- **The rest of the agent chat**: the Codex bridge's push into a running turn, events to a whole
  channel, the `related` key, the review round's mailbox, among others
  ([chat.md section 14](../design/chat.md#14-deferred-after-060)).

Each arrives with its own decision record and specification; until one is written, nothing here is
a promise.

## Bugs

| Issue | Area |
|---|---|
| [#109](https://github.com/zima-oss/jackstay/issues/109) a failed restore in `jst discard` leaves a tracked file deleted, its content only in the trash | discard |
| [#85](https://github.com/zima-oss/jackstay/issues/85) a background job's state trusts a reused supervisor PID when the process table cannot be read | background processes |
| [#83](https://github.com/zima-oss/jackstay/issues/83), [#78](https://github.com/zima-oss/jackstay/issues/78), [#80](https://github.com/zima-oss/jackstay/issues/80) `jst tidy` and the guard keep finished branches and worktrees after a squash merge or when `lsof` cannot answer | branches |
| [#87](https://github.com/zima-oss/jackstay/issues/87) the guard repeats the config-unusable message on every call | guard |
| [#70](https://github.com/zima-oss/jackstay/issues/70) schema-3 keys can reach CI's old jackstay between the pin and the workflows | adoption |
| [#69](https://github.com/zima-oss/jackstay/issues/69) fsync of a store's folder chain misses search-only parents and symlinked homes | state |

## Agents and the guard

| Issue | Area |
|---|---|
| [#79](https://github.com/zima-oss/jackstay/issues/79) name the jst equivalent when an agent runs a sleep loop, a raw test runner or `git worktree` | guard |
| [#48](https://github.com/zima-oss/jackstay/issues/48) explain guard policy and prove host hook coverage | guard |
| [#101](https://github.com/zima-oss/jackstay/issues/101) the reviewer access profile on Linux: deny secrets and `.env` under Codex's sandbox | review |
| [#39](https://github.com/zima-oss/jackstay/issues/39) review pinned Git snapshots on demand | review |
| [#45](https://github.com/zima-oss/jackstay/issues/45) measure Claude Code chat delivery during active turns | chat |
| [#51](https://github.com/zima-oss/jackstay/issues/51) Codex compaction delivery and saved workspace views | briefs, status |

## Tests, delivery and install

| Issue | Area |
|---|---|
| [#46](https://github.com/zima-oss/jackstay/issues/46) complete structured SwiftPM mixed-run evidence | tests ([pilot](../design/swift-structured-pilot.md)) |
| [#49](https://github.com/zima-oss/jackstay/issues/49) explain test environment provenance and trusted reuse | receipts |
| [#47](https://github.com/zima-oss/jackstay/issues/47) reconcile interrupted remote delivery and protect local Git action inputs | landing, releases |
| [#2](https://github.com/zima-oss/jackstay/issues/2), [#43](https://github.com/zima-oss/jackstay/issues/43), [#44](https://github.com/zima-oss/jackstay/issues/44) the GitHub App for the release workflow: setup, permissions, key rotation, doctor checks | releases ([pilot](github-app-pilot.md)) |
| [#1](https://github.com/zima-oss/jackstay/issues/1), [#11](https://github.com/zima-oss/jackstay/issues/11), [#92](https://github.com/zima-oss/jackstay/issues/92) install channels, download provenance, installing the default branch between releases | install |

## Research

[Ecosystem research](ecosystem.md) records what users of neighbouring coding-agent tools report
and ask for: the evidence behind several issues above.
