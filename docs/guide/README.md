# The jackstay guide

The guide follows the order work happens in a repository: install `jst`, adopt one project, then
use the commands each day. `jst help` lists the commands on your machine, and
`jst <command> --help` gives each one's flags, an example and its exit codes.

## Start

Install `jst` and bring one project under it.

- [Getting started](getting-started.md): from install to an adopted project, a counted test run
  and a guard check, in five minutes.
- [Install](install.md): the release script, an archive, a source build, updates and removal.
- [Adopting a project](adopt.md): the files jackstay writes, profiles and safe upgrades.

## Daily work

The commands an agent or a person runs while changing code.

- [Tests](tests.md): scopes, counted evidence, receipts and the gate.
- [The guard, discard and trash](guard.md): what is refused, and where discarded edits go.
- [Branches and GitHub](branches-and-github.md): a worktree per task, CI checks, landing a change
  with `jst land`, tidying up.
- [Reviews](review.md): cold reviews by Codex or Claude Code, and the check of a round.
- [Background processes and waiting](background.md): deadlines, cleanup, waiting for a file or
  a line.
- [Numbers, records and cards](records.md): shared IDs and project records.
- [Changelogs and releases](releases.md): entries, preparation and publication.
- [What jackstay did](observability.md): the journal, statistics and diagnostics.
- [Maven Surefire](maven-surefire.md): judging separate XML reports.
- [A Homebrew tap formula](homebrew-tap.md): for maintainers of another install channel.

## Working with several agents

Several sessions in one repository, or several repositories at once.

- [Session chat](session-chat.md): parallel local agents talking through MCP.
- [Compaction briefs](context-brief.md): a task note restored after a compaction.
- [`jst status`](status.md): what is going on in this repository, or one row per repository.
- [Claude Code](claude-code.md): the guard hook, the sandbox and the reviewer.
- [Codex](codex.md): the reviewer and the guard hook.

## Configuration

[Configuration](configuration.md): `jackstay.json`, its sections, and profiles for many projects.

## Reference

[Commands and configuration](../reference/README.md): every flag, exit code and key, generated
from the code.

## Upgrading

[Upgrading from 0.5.x](upgrading.md): the machine first, then each project in three steps.

## Contributing

- [Contributing](../../CONTRIBUTING.md): building, testing and proposing a change.
- [How the documentation is written](../STYLE.md): the rules every page and help text follows.
- The [design](../design/implementation.md) says how each command works and why;
  [decision records](../decisions/README.md) say what was decided; the
  [backlog](../roadmap/backlog.md) groups the open issues.

## Terms used here

A **project** is a Git repository whose root has a `jackstay.json`. **jackstay home** is the local
state folder, `~/.jackstay`; set `JACKSTAY_HOME` to an absolute path to move it. A **session** is
one agent conversation. The **owner** is the person who directs the work and settles what a tool
cannot decide.
