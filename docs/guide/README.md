# The jackstay guide

How to install jackstay, bring a project to it, and use each group of its commands. Every page says
what the commands are for, their key flags, a worked example and what can go wrong. For every flag
and exit status, `jst <command> --help` is the reference; for why a command behaves as it does, each
page links the design.

## Start here

1. [Getting started](getting-started.md): install, adopt a project, the first `jst test`, the
   guard at work.
2. [Install](install.md): the one-line install script, the release archive or a source build;
   updating and removing jackstay.
3. [Adopting a project](adopt.md): `jst adopt`, `jackstay.json`, profiles.

## The commands, by what you are doing

| Page | Commands |
|---|---|
| [Tests](tests.md) | `jst test`, `jst test list/plan/last`, receipts (`jst receipts`), `jst lock`, `jst check`, `jst patterns`, `jst baseline` |
| [The guard, discard and trash](guard.md) | `jst guard`, `jst discard`, `jst trash`; the guard's rules and their levels |
| [Branches and GitHub](branches-and-github.md) | `jst start`, `jst sync`, `jst tidy`, `jst gh` |
| [Background processes and waiting](background.md) | `jst bg`, `jst procs`, `jst wait` |
| [Numbers, records and cards](records.md) | `jst id`, `jst new`, `jst view`, `jst cards`, the `Card:` line, `jst lint range`, `jst lint tree` |
| [Changelogs and releases](releases.md) | `jst changes`, `jst release`, `jst lint pr` |
| [Reviews](review.md) | `jst review` and its `check`, `last`, `list`, `show`, `prune` |
| [What jackstay did](observability.md) | `jst log`, `jst stats`, `jst doctor`, `jst owner` |

## Integrations (both optional)

- [Claude Code](claude-code.md): the guard as a `PreToolUse` hook, the sandbox, warnings that reach
  the agent, the AGENTS block.
- [Codex](codex.md): the reviewer `jst review` runs, and the guard as a Codex hook.

## Words used on these pages

- **Project**: a git repository with a `jackstay.json` at its root. Its name is the file's `project`
  key, else the main checkout's folder name.
- **jackstay home**: `~/.jackstay`, where jackstay keeps its state (receipts, run records, the
  trash, the journal, counters). `JACKSTAY_HOME` (an absolute path) replaces it.
- **Session**: one agent conversation (a Claude Code session, say). Many sessions may work on one
  machine, in one project, at once.
- **Owner**: the person running the sessions. Some commands stop and leave a decision to them.

## The design

How each command works inside, and why, is in the repository, not on these pages. The
[current specification](../design/implementation.md) describes the binary as a whole; its
section 0 lists the feature specs: [the guard](../design/guard.md),
[discard and the clean-copy gate](../design/discard-and-clean-gate.md), [testing](../design/testing.md),
[review](../design/review.md), [releases](../design/release.md), [adoption](../design/adopt.md) and
[the journal](../design/observability.md).
