# Changelog

What changed in each jackstay release, newest first. The form follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Releases before 0.6.0 are on the
[releases page](https://github.com/zima-oss/jackstay/releases).

## 0.6.0 — unreleased

0.6.0 lands changes with one thin command, runs a project's test scopes as a graph split between
this machine and CI, folds the review pool into `jst review`, gives parallel agent sessions a chat
that rings Claude Code, and puts what is going on in one `jst status`. Upgrading from 0.5.x is
[at the end](#upgrading-from-05x).

### Added

**Landing**
- `jst land` pushes the branch, opens the change request, waits for the checks the base's rules
  require, and merges at the head that was checked. The `land` section says who merges:
  `merge-when-green` lets the agent merge, `prepare` stops ready for a person; `jackstay.json`, CI
  workflows, `.claude/settings.json` and `land.human_paths` always need a person. The host is
  reached through an adapter, GitHub first. [#88](https://github.com/zima-oss/jackstay/pull/88)
- `jst checks wait`, `status` and `run-wait` wait on a change request's checks and a workflow's
  runs. [#88](https://github.com/zima-oss/jackstay/pull/88)
- `jst setup` asks once per machine who merges and who reviews (Codex recommended); `--autonomy`
  and `--reviewer` answer without a terminal. `jst adopt` offers those answers and also asks whether
  the agent chat is on; its `--autonomy`, `--reviewer` and `--chat on|off` answer without a
  terminal, and its plan shows where each value came from. [#104](https://github.com/zima-oss/jackstay/pull/104),
  [#112](https://github.com/zima-oss/jackstay/pull/112)

**Tests**
- Scopes run as a task graph: a scope's `deps` run first, each node once. `ci` and `systems` say
  where a scope runs: `jst test ci` is the CI job of a system, `jst test uncovered` runs on a clean
  copy what CI does not cover, and `jst test changed` picks the scopes for the changed paths. When a
  project's committed configuration leaves a scope to CI, `jst install` from its checkout needs a
  passed `jst test uncovered` for that commit.
  [#108](https://github.com/zima-oss/jackstay/pull/108),
  [#117](https://github.com/zima-oss/jackstay/pull/117)
- Structured judges for Go JSON, JUnit XML (Vitest, Maven Surefire), Jest JSON, VSTest TRX and
  verified Xcode XCResult reports, with test durations and typed Go and Jest selectors.
  [#25](https://github.com/zima-oss/jackstay/pull/25)
- A test that passes only on retry gets its own outcome, `passed only on retry` (exit 78), not a
  clean pass. [#71](https://github.com/zima-oss/jackstay/pull/71)
- `jst receipts export` and `jst receipts verify` share a counted pass as a portable record, checked
  against a digest, revision and key from a trusted source.
  [#26](https://github.com/zima-oss/jackstay/pull/26)

**Review**
- Choose Codex, Claude Code (2.1.248 or newer) or both with `--reviewer`, or `review.use`; Codex
  stays the default. Reviewers read pinned snapshots of the base and head on demand.
  [#52](https://github.com/zima-oss/jackstay/pull/52),
  [#72](https://github.com/zima-oss/jackstay/pull/72)
- `jst review --background` returns at once; the result arrives as a chat event. At most
  `review.max_concurrent` rounds run at a time. [#94](https://github.com/zima-oss/jackstay/pull/94)
- After an interrupted round, only the reviewers whose answer is missing run again.
  [#95](https://github.com/zima-oss/jackstay/pull/95)
- One access profile for both reviewers: they read the whole tree, cannot read the known credential
  paths (the other reviewer's login included), and write only their temporary folder. On macOS they
  also cannot read `.env` files; on Linux Codex still can
  ([#101](https://github.com/zima-oss/jackstay/issues/101)), so keep secrets out of a `.env` in the
  tree you review there.
  [#96](https://github.com/zima-oss/jackstay/pull/96)
- A finding carries `ordinary_path`: an `unusual` and `severe` finding blocks only when it names
  the ordinary-work path that leads to it, and one that only asks to cover more exotic cases never
  blocks. [#102](https://github.com/zima-oss/jackstay/pull/102),
  [#106](https://github.com/zima-oss/jackstay/pull/106)
- `just review-bench`, a harness that measures the review wrapper against direct calls (results
  below). [#97](https://github.com/zima-oss/jackstay/pull/97)

**Several agents**
- The agent chat: `jst chat serve` serves one session over MCP, with an account-wide store,
  threads, structured requests, receipts and notices to senders. Claude Code's messaging socket is a
  doorbell: it rings with no message text, and a message counts as delivered when the recipient
  reads it from its inbox. `jst hook claude` tells of unread messages and holds a turn for an
  urgent one. Review results arrive as events.
  [#100](https://github.com/zima-oss/jackstay/pull/100),
  [#103](https://github.com/zima-oss/jackstay/pull/103),
  [#107](https://github.com/zima-oss/jackstay/pull/107),
  [#111](https://github.com/zima-oss/jackstay/pull/111),
  [#114](https://github.com/zima-oss/jackstay/pull/114),
  [#116](https://github.com/zima-oss/jackstay/pull/116)
- `jst status` shows this repository's worktrees, agent sessions, recent events, chat, the brief,
  background jobs, the last gate and receipt; with several paths, or `status.repos`, one row per
  repository. [#89](https://github.com/zima-oss/jackstay/pull/89)
- `jst brief save` and `jst brief show` keep a short task note per worktree, restored after a
  Claude Code compaction. [#27](https://github.com/zima-oss/jackstay/pull/27)

**Setup and adoption**
- `jst setup` plans and applies machine repairs: PATH, and Claude Code's sandbox, which may now
  write only the folders `jst` writes from inside it.
  [#24](https://github.com/zima-oss/jackstay/pull/24),
  [#105](https://github.com/zima-oss/jackstay/pull/105)
- `install.sh` says before it starts setup that a terminal is there and what setup will ask, and
  says so when it does not start it. [#113](https://github.com/zima-oss/jackstay/pull/113)
- `jst profile export` and `jst profile verify`: an organisation's adoption profile as a bundle,
  pinned by its digest. [#26](https://github.com/zima-oss/jackstay/pull/26)
- An editor schema for `jackstay.json` (`$schema`), and the `brief`, `status`, `land` and `chat`
  sections, with config schema 3. [#75](https://github.com/zima-oss/jackstay/pull/75)

**Documentation**
- A style guide checked by `just docs`; command and configuration references generated from the
  code; `jst help` grouped by task; the site organised by task, with `llms.txt` and
  `llms-full.txt`. [#77](https://github.com/zima-oss/jackstay/pull/77),
  [#82](https://github.com/zima-oss/jackstay/pull/82),
  [#86](https://github.com/zima-oss/jackstay/pull/86),
  [#91](https://github.com/zima-oss/jackstay/pull/91)

### Changed

- The AGENTS block opens with "instead of X, run Y" lines for what agents most often do by hand:
  raw test runners, `gh` waits, `sleep`, `git worktree`.
  [#99](https://github.com/zima-oss/jackstay/pull/99)
- `jst test` and `jst review` run outside the agent sandbox: they write evidence the sandbox may
  only read. `jst test` first checks that it can write and undo its evidence, and runs nothing
  otherwise. [#105](https://github.com/zima-oss/jackstay/pull/105)
- The guard refuses `gh pr merge` and `glab mr merge`: a change is merged through `jst land`, under
  the project's landing policy.
  [#88](https://github.com/zima-oss/jackstay/pull/88)
- State files go through one durable store: atomic writes, folders synced.
  [#74](https://github.com/zima-oss/jackstay/pull/74)

### Removed

- `jst gh`: use `jst land` and `jst checks` (the table below).
  [#88](https://github.com/zima-oss/jackstay/pull/88)
- `jst test gate --resume` and a scope's `resume` key: a gate runs every scope it needs each time.
  There is no test cache in 0.6.0. [#117](https://github.com/zima-oss/jackstay/pull/117)
- The Codex bridge's push into a running turn: a Codex session reads its inbox through
  `jst chat serve`. [#116](https://github.com/zima-oss/jackstay/pull/116)

### Fixed

- A cached `go test -cover` result, and `attr` and `artifacts` events, are judged correctly.
  [#64](https://github.com/zima-oss/jackstay/pull/64),
  [#65](https://github.com/zima-oss/jackstay/pull/65)
- `jst test changed` counts working-tree paths. [#67](https://github.com/zima-oss/jackstay/pull/67)
- Reviewer isolation refuses attached short flags it owns (`-sVALUE`).
  [#68](https://github.com/zima-oss/jackstay/pull/68)
- `jst discard` restores a missing tracked file through the prepared replacement.
  [#62](https://github.com/zima-oss/jackstay/pull/62)
- `jst procs stop` no longer says a running job "had ended already" when its supervisor answers
  between two checks. [#115](https://github.com/zima-oss/jackstay/pull/115)
- `jst adopt --plan` names `jst adopt --apply --settings-only` when the changes touch
  `jackstay.json` and other files, and the upgrade's schema-3 step names it too, so nobody splits a
  mixed write by hand. [#124](https://github.com/zima-oss/jackstay/pull/124)
- GitHub check results and pull request identities, interrupted installs and deliveries, trash
  recovery and setup planning, from the release-range audit.
  [#53](https://github.com/zima-oss/jackstay/pull/53),
  [#54](https://github.com/zima-oss/jackstay/pull/54)

### Measured

The review wrapper against direct calls to the same reviewers, on one machine (macOS), a small
fixture with seeded defects and five runs per arm; every finding labelled by a person, none false
([#97](https://github.com/zima-oss/jackstay/pull/97)):

| | Codex through `jst review` | Codex direct | Claude Code through `jst review` | Claude Code direct |
|---|---|---|---|---|
| seeded defects found | all | all | all | all |
| wall time | 35 ± 5 s | 77 ± 49 s | 28 ± 4 s | 27 ± 8 s |
| tokens | 116k | 326k | 39k | 172k |
| cost | not reported | not reported | $0.092 | $0.293 |

Through `jst review`, Codex finished in under half the time with about a third of the tokens, and
Claude Code cost 69 % less at the same speed. The direct calls reported 2.5 to 4 times more real
issues outside the seeded ones, because they read the code freely; on a larger codebase the
difference in what is found may matter more than on this fixture.

### Upgrading from 0.5.x

0.6.0 reads a 0.5.x `jackstay.json` as it is, except a `resume` key under `test.scopes` or
`test.steps`: `jst check` names it, and you delete it. Install it on the machine, run `jst setup`
and `jst doctor`, then move each project the way `jst adopt --plan` plans it: the pin, the
workflows that download 0.6.0, then config schema 3 with the `land` section, each a pull request a
person merges. A step that changes `jackstay.json` is applied with `--settings-only` and lands
alone (rule D0). The [upgrading guide](docs/guide/upgrading.md) has the steps.

Keep that order: `jst adopt` refuses a key that needs schema 3 (`$schema`, `land`,
`adopt.profile_sha256` …) until the workflows that download 0.6.0 are written, so CI's 0.5.x never
reads a schema-3 file. Don't add such a key by hand before then, and don't carry `jackstay.json`
onto a branch without the new workflows. Install 0.6.0 on every machine whose agents work in the
project before step 3 merges: an older jst cannot use the schema-3 file, and its guard then
applies only its built-in rules.

`jst land` waits only for the checks the base's rules require and those `land.checks` lists, where
`jst gh pr-land` stopped on any failed check: make a check that must block required on the host, or
list it in `land.checks`.

| 0.5.x | 0.6.0 |
|---|---|
| `jst gh pr-land` | `jst land` (`--pr` is `--change`, `--dry-run` is `--plan`, `--no-merge` is `--prepare`) |
| `jst gh pr-status`, `jst gh pr-find` | `jst land status`, `jst land find` |
| `jst gh ci-wait`, `ci-status`, `run-wait` | `jst checks wait`, `status`, `run-wait`, same flags |
| `jst test gate --resume`, the `resume` key in `test.scopes` and `test.steps` | removed; `jst check` names the key to delete |
| `"jackstay_schema": 2` | 3 for the new keys; 0.5.10 and 0.5.11 refuse a schema-3 file |
| chat tools `chat.self`, `chat.reply`, `chat.ack` | `chat.whoami`, `chat.send` with `reply_to`, `chat.ack` marks `acted`; the old names keep working through 0.6.x |

A removed command runs nothing, and its error names the replacement.

The agent chat has a new store: 0.6.0 neither reads nor imports the 0.5.x chat's messages. Answer
pending requests and read pending messages before you switch a machine to 0.6.0.
