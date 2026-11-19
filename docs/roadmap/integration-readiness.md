# Integration review series

This map covers the 50 commits on `codex/audit-remediation` from local
`origin/main` at `a24967f` through `2407f44` on 2026-11-19. It records a
review order for the existing linear history. New commits after `2407f44`
need their own review entry before integration.

## Review order

Each row is a contiguous commit range. Review its final diff against the
previous row's tip, then inspect the individual commits for migrations and
behavior changes. Later rows inherit the preceding rows. A green check on
`2407f44` proves that tree only; checks for a changed tip must be rerun.

| Series | Commits, inclusive | Review focus | Focused verification |
| --- | --- | --- | --- |
| 1. Foundations | `f673f8b`–`24dc548` (7) | Audit fixes, runner inventory, durable local delivery, portable evidence and profiles, reviewer choice, common test verdict, documentation site. | Config, delivery, review and testing packages; `just docs`. |
| 2. Existing workflows | `581dff4`–`1dba57c` (8) | GitHub check supersession, process readiness, archive provenance, structured receipts, Vitest, installer channels, setup and test-scope selection. | GitHub, process, release, setup and testing packages; release and process integration tests. |
| 3. Test producers and pilots | `3560284`–`74f3533` (9) | Surefire, Jest, TRX and Xcode evidence, retry and partition checks, typed selection and duration; queue and App specifications. | Testing package, structured producer fixtures, docs and schema checks. |
| 4. Session transport and queue core | `57a8102`–`4230dea` (7) | Early external mail experiments, editor schema, local merge candidate, final repository-local MCP chat model, configurable host policies. Review the final session chat implementation, since `b8bb0f4` replaces the external mail model. | Session chat and merge queue packages; adoption goldens; MCP isolation and recovery tests. |
| 5. Trust and handoff | `fcdda59`–`bf64d94` (8) | Fresh review and CI checks, Codex and Claude chat delivery, manual queue outcome, signed receipts and pinned profile bundles. | Queue and receipt integration tests; Codex and Claude host pilots; profile upgrade and rollback fixtures. |
| 6. Coordination proof | `19652e9`–`830040e` (8) | Compaction and workspace specifications and CLI, queue recovery fix, signed CI and profile pilots, trash identity fix, host pilots. | Context brief, workspace board, trash and queue tests; real host and multi-repository fixtures. |
| 7. Reviewer pool | `e0a35e1`–`2407f44` (3) | Crash reconciliation, retries, MCP submission, ACK delivery, policy settings, observability, history pruning and state migration. | Review pool and session chat tests, both host pilots, v1 migration and prune smoke. |
| 8. Pool hardening and integration evidence | Changes above `2407f44` | Policy fingerprints, stale-job refusal, subscriber closure, linked-worktree deduplication, and the branch review map. | Policy drift and closure tests, a mixed-host pilot, and the final-tree gate on macOS and Linux. |

For every series, verify that the diff contains only its stated work and that
the package tests and relevant integration fixtures pass at the series tip.
The repository gate is `just gate`. The macOS gate passed at `2407f44` with
3,705 tests. `go test -count=1 ./...` passed on Linux arm64 at all seven
historical series tips. The Linux runs used tracked snapshots, a non-root
user, and an init process in an isolated container. Record the full gate and
public CI results against the final integration tree before merging.

## History and privacy check

The 50 commits through `2407f44` have a neutral contributor identity for both author and
committer. Their added lines, commit messages and metadata were checked for
personal home paths, known personal handles, email addresses and common
credential prefixes. A `gitleaks git` scan over `origin/main..2407f44`
reported one `generic-api-key` match. It is a made-up 32-character hexadecimal
attempt token assigned to a queue test's `AttemptID`. The scan found no
credential requiring removal. The scan does not prove that every possible
secret shape is absent. Repeat it on each new tip and inspect any finding
before publication. Keep scan output redacted.

## Integration dependencies

1. Review foundation and test evidence before the queue and host integrations
   that consume them. The test producers depend on the shared verdict and
   report transport in series 1.
2. Review the editor schema before the chat and queue settings that use it.
   The earlier external mail commits in series 4 are historical steps toward
   the repository-local session chat contract.
3. Review the queue's tested candidate before remote freshness and manual
   landing evidence. Review the session chat transport before the Codex and
   Claude delivery integrations.
4. Review signed CI evidence and pinned profiles before their two-repository
   proof. Review the coordination tool specifications before their pilots.
5. Review reviewer pool recovery before MCP delivery, then history pruning.
   Its terminal notification records depend on the session chat ACK contract.

The integration branch should keep the tree that passes the gate. If review
changes a series, rerun its focused checks, both affected host pilots and the
full gate on the final tree. Record the Linux result and any public CI result
with the exact head commit before merging.
