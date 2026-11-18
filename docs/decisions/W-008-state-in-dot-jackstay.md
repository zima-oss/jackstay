# W-008 — State lives in `~/.jackstay`, one folder per project and `.all` for what is shared

Date: 2026-11-15.

## Context

jackstay keeps state outside the repository: receipts, kept outputs, the run record, the lock,
counters, the process table. Claude Code's Bash sandbox writes only the folders listed in
`sandbox.filesystem.allowWrite`, so a run inside a sandboxed agent could not record anything in a
system folder such as `~/Library/Application Support`.

## Decision

- State lives in one user-level folder, `~/.jackstay` (the name since
  [W-066](W-066-jackstay-zima-oss-apache.md)); `JACKSTAY_HOME` replaces it.
- What belongs to one project lives in `~/.jackstay/<project>/`; what every project shares (the run
  record, the machine-wide lock, the process table, the trash) lives in `~/.jackstay/.all/`. A
  project's folder name is encoded so no project can be `.all` or leave the folder.
- `~/.jackstay` must be in the sandbox's `allowWrite` list for runs inside a sandboxed agent to be
  recorded; `jst doctor` says so when it is not.

## Consequences

- A user of Claude Code's sandbox adds `~/.jackstay` to `allowWrite` once.
- Anything in `~/.jackstay` can be written by any process of the same account: it is a record, not
  a security boundary.
- See [the specification](../design/implementation.md) section 3.1 and the guide's
  [Claude Code](../guide/claude-code.md) page.
