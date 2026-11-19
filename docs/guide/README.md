# The jackstay guide

Use this guide in the order work happens in a repository. Install `jst`, bring one project under
its commands, then choose the tools its team needs. Each page has runnable examples and failure
recovery. `jst help` and `jst COMMAND --help` give the exact flags and exit codes installed on
your machine.

## Set up a project

1. [Getting started](getting-started.md) takes a new repository through adoption, a test run and
   a guard check.
2. [Install](install.md) covers the release script, archive and source install, updates and removal.
3. [Adopting a project](adopt.md) explains the generated files, profiles and safe upgrades.

Maintainers preparing another install channel can [generate a Homebrew tap formula](homebrew-tap.md)
from a verified release.

## Protect the work

- [The guard, discard and trash](guard.md) covers the commands that are intercepted and where
  discarded edits go.
- [Background processes and waiting](background.md) covers deadlines, process cleanup and
  waiting for files or output.
- [Branches and GitHub](branches-and-github.md) covers linked worktrees, CI checks and cleanup.

## Verify and deliver

[Local merge queue](merge-queue.md) prepares and verifies one candidate for manual landing.

- [Tests](tests.md) covers scopes, evidence, receipts and gates.
- [Maven Surefire](maven-surefire.md) shows how to judge separate XML reports from test classes.
- [Reviews](review.md) covers optional Codex and Claude Code reviewers and the recorded verdict.
- [Numbers, records and cards](records.md) covers shared IDs and project records.
- [Changelogs and releases](releases.md) covers collection, preparation and publication steps.
- [What jackstay did](observability.md) covers the local timeline, inventory, journal and diagnostics.
- [Workspace board](workspace-board.md) compares local task, queue, test and chat evidence across named repositories.
- [Reviewer pool](../roadmap/reviewer-pool.md) defines the local queue and its explicit worker.

## Agent integrations

[Session chat](session-chat.md) connects parallel local agents through MCP.
[Compaction briefs](context-brief.md) restore an explicitly maintained task note at a supported boundary.

- [Claude Code](claude-code.md) covers the project guard hook, sandbox and reviewer setup.
- [Codex](codex.md) covers the optional reviewer and guard hook.

## Terms used here

A **project** is a Git repository whose root has a `jackstay.json`. **jackstay home** is the
local state folder, normally `~/.jackstay`. Set `JACKSTAY_HOME` to an absolute path to move it.
A **session** is one agent conversation. The **owner** is the person who directs the work and
settles decisions that a tool cannot infer.

## Design and decisions

The [implementation specification](../design/implementation.md) links the contracts for each
command. [Decision records](../decisions/README.md) explain the choices behind them. Start with
[the test contract](../design/testing.md), [adoption](../design/adopt.md),
[review](../design/review.md) or [observability](../design/observability.md) when you need to
change how the framework behaves.

[Backlog assessment](../roadmap/backlog.md) evaluates proposed product work against the current
implementation. [Ecosystem research](../roadmap/ecosystem.md) records wider needs and sources.
