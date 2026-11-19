# jackstay

jackstay gives coding agents a set of reliable commands for working in Git repositories. Its
single Go binary, `jst`, guards against destructive shell commands, runs tests with recorded
evidence, manages short-lived processes, and helps finish branches, reviews and releases. It keeps
a local history of what happened across linked worktrees and agent sessions.

## Start with one repository

Install on macOS or Linux, then inspect the changes jackstay proposes for your project.

```sh
script=$(curl -q --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/zima-oss/jackstay/main/install.sh) && sh -c "$script"
jst doctor
cd your-project
jst adopt --plan
jst adopt --apply
```

The script downloads a release, checks its archive against `checksums.txt` and installs in
`~/.local` without `sudo`. Read the [install guide](docs/guide/install.md) for a manual install,
source build or update. Commit the project files written by `jst adopt --apply`; the
[getting-started guide](docs/guide/getting-started.md) walks through this and the first test run.

## Follow the work

| Task | Command and guide |
|---|---|
| Recover from a destructive command | The [guard, discard and trash](docs/guide/guard.md) refuse unsafe shell commands and keep discarded edits recoverable. |
| Run and prove tests | [`jst test`](docs/guide/tests.md) records output and counts evidence. Receipts identify the tree that passed. |
| Keep a process under control | [`jst bg`, `jst procs` and `jst wait`](docs/guide/background.md) give jobs a deadline and wait for a result. |
| Manage branches and GitHub checks | [`jst start`, `jst sync`, `jst tidy` and `jst gh`](docs/guide/branches-and-github.md) cover worktrees, CI and cleanup. |
| Review a change | [`jst review`](docs/guide/review.md) records a round and its verdict. Select Codex, Claude Code or both. |
| Collect changes and release | [`jst changes` and `jst release`](docs/guide/releases.md) keep changelog entries and release steps recoverable. |
| Reconstruct a session | [`jst timeline`, `jst log`, `jst stats` and `jst inventory`](docs/guide/observability.md) read local records. |

`jst help` lists commands. Every command answers `--help` with its flags and exit statuses.
Project-specific runners, paths and rules live in `jackstay.json` and optional
[profiles](docs/guide/adopt.md). The [guide](docs/guide/README.md) is organized around the tasks
above.

## Agents and platforms

jackstay works from a shell with or without an agent. [Claude Code](docs/guide/claude-code.md)
and [Codex](docs/guide/codex.md) can use its guard hook and can be selected for reviews. Both
reviewers are optional. The guard helps prevent accidental loss of work; see the
[security notes](SECURITY.md) for its trust boundary.

Releases support macOS on Apple silicon and Intel, and Linux on x86-64 and arm64. `lsof` is
required for background process tracking and worktree cleanup on Linux. Windows is currently
unsupported.

## Build and contribute

The [design](docs/design/implementation.md) describes the current contracts, and
[decisions](docs/decisions/README.md) explain why they were chosen. The
[backlog assessment](docs/roadmap/backlog.md) and [ecosystem research](docs/roadmap/ecosystem.md)
track proposed work. Run `just gate` before pushing
a change. [CONTRIBUTING.md](CONTRIBUTING.md) covers the development workflow, and
[SECURITY.md](SECURITY.md) explains how to report a vulnerability.

jackstay is distributed under [Apache License 2.0](LICENSE). Third-party module licenses are
listed in [docs/licences.md](docs/licences.md).
