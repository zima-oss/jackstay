# Backlog

The backlog is the [issue list](https://github.com/zima-oss/jackstay/issues) on GitHub; an issue
holds the request, its discussion and its state. This page groups the open issues by the part of
jackstay they change, so related work is found together. When an issue closes, its row goes.

## Bugs

| Issue | Area |
|---|---|
| [#13](https://github.com/zima-oss/jackstay/issues/13) `jst gh pr-land` reads a run its concurrency group cancelled as a failure | landing |
| [#20](https://github.com/zima-oss/jackstay/issues/20) `TestRealKeptIsListedAndStoppedByName` fails occasionally on Linux CI | background processes |
| [#63](https://github.com/zima-oss/jackstay/issues/63) `jst procs stop` can answer "had ended already" for a running job | background processes |
| [#69](https://github.com/zima-oss/jackstay/issues/69) fsync of a store's folder chain misses search-only parents and symlinked homes | state |
| [#70](https://github.com/zima-oss/jackstay/issues/70) schema-3 keys can reach CI's old jackstay between the pin and the workflows | adoption |
| [#73](https://github.com/zima-oss/jackstay/issues/73) `TestConcurrentAppendsLeaveWholeLines` fails under load | telemetry |
| [#78](https://github.com/zima-oss/jackstay/issues/78) `jst tidy` misses squash-merged pull requests of branches in a worktree | branches |
| [#59](https://github.com/zima-oss/jackstay/issues/59) `jst setup` grants the agent sandbox write access to jackstay's evidence | setup |

## 0.6.0

| Issue | Area |
|---|---|
| [#76](https://github.com/zima-oss/jackstay/issues/76) Agent chat: formal model first, then the spec's open review gaps | chat ([design](../design/chat.md)) |
| [#4](https://github.com/zima-oss/jackstay/issues/4) An idempotent machine setup that asks before sensitive changes | setup |
| [#79](https://github.com/zima-oss/jackstay/issues/79) Guard: name the jst equivalent of sleep loops, raw test runners and `git worktree` | guard |

## Tests and evidence

| Issue | Area |
|---|---|
| [#5](https://github.com/zima-oss/jackstay/issues/5) Choose the test scope from the paths a range changes | tests |
| [#46](https://github.com/zima-oss/jackstay/issues/46) Complete structured SwiftPM mixed-run evidence | tests ([pilot](../design/swift-structured-pilot.md)) |
| [#49](https://github.com/zima-oss/jackstay/issues/49) Explain test environment provenance and trusted reuse | receipts |

## Delivery and GitHub

| Issue | Area |
|---|---|
| [#40](https://github.com/zima-oss/jackstay/issues/40), [#41](https://github.com/zima-oss/jackstay/issues/41), [#42](https://github.com/zima-oss/jackstay/issues/42) Queue landing: the operator's decision, exact-candidate landing, ordered batches | landing ([pilot](merge-queue-pilot.md)) |
| [#47](https://github.com/zima-oss/jackstay/issues/47) Reconcile interrupted remote delivery and protect local Git action inputs | landing, releases |
| [#2](https://github.com/zima-oss/jackstay/issues/2), [#43](https://github.com/zima-oss/jackstay/issues/43), [#44](https://github.com/zima-oss/jackstay/issues/44) GitHub App setup for the release workflow: registration, permissions, key rotation, doctor checks | releases ([pilot](github-app-pilot.md)) |
| [#1](https://github.com/zima-oss/jackstay/issues/1), [#11](https://github.com/zima-oss/jackstay/issues/11) Install channels and download provenance | install |

## Agents and observation

| Issue | Area |
|---|---|
| [#39](https://github.com/zima-oss/jackstay/issues/39) Review pinned Git snapshots on demand | review ([pool](reviewer-pool.md)) |
| [#45](https://github.com/zima-oss/jackstay/issues/45) Measure Claude Code chat delivery during active turns | chat |
| [#48](https://github.com/zima-oss/jackstay/issues/48) Explain guard policy and prove host hook coverage | guard |
| [#50](https://github.com/zima-oss/jackstay/issues/50) Aggregate workspace timeline coverage and measured review usage | observation ([board](workspace-board.md)) |
| [#51](https://github.com/zima-oss/jackstay/issues/51) Codex compaction delivery and saved workspace views | briefs, board |

## Research

[Ecosystem research](ecosystem.md) records what users of neighbouring coding-agent tools report
and ask for: the evidence behind several issues above.
