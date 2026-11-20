## jst: the tools for working on this project

`jst` is installed here; use it before a hand-written shell loop. `jst help` lists the commands,
`jst <command> --help` gives a command's flags, an example and its exit codes.

- **Tests**: `jst test SCOPE` (`jst test list` names them and when each is due). Only `passed`
  is a pass: read the last `jst test:` line. After a failure, `jst test last`, not a rerun.
  Before a pull request: `jst test gate --commit HEAD` (on a clean copy of the commit).
  Preview path-based selection with `jst test plan changed --base REF`. No scope fits: `jst test -- COMMAND`.
- **Share a counted pass**: `jst receipts export KEY`. Check one with `jst receipts verify`, taking
  the expected digest, revision and key from a trusted source, never from the receipt.
- **Verify CI evidence** with `jst receipts verify-ci`. Obtain the trusted key and expected CI identity
  independently of the uploaded receipt and attestation.
- **Wait** for a process, a file or a log line with `jst wait … --timeout S`, never a `sleep` loop.
- **Background**: `jst bg --for MIN --name N -- COMMAND`, never `&` or `nohup`; `jst procs stop N`
  stops it.
- **A heavy command** others on this machine must not overlap: `jst lock -- COMMAND`.
- **Branches**: `jst start NAME` makes a branch and its worktree; `jst sync` merges the default branch
  in (never rebase); `jst tidy --fix --branch NAME` removes yours once it is finished.
- **Drop uncommitted changes** with `jst discard PATH…`, never `git restore` or
  `git checkout -- PATH`; `jst trash restore` brings them back.
- **When the guard refuses** a command, run what it names instead, never a workaround. Read a
  `jst guard let this command run, with a warning` line before going on. Its rules: `jackstay.json` (`guard`).
- **GitHub**: `jst gh ci-wait` / `ci-status` (a pull request's checks), `run-wait`, `pr-status`,
  `pr-land` (push, open, wait, squash-merge the head that was checked). jackstay acts as the account in
  `jackstay.json` (`github.account`).
- **Queue optional reviews across local repositories** with `jst review pool`. An explicit worker runs one pinned request.
  Use `jst review pool inspect` to check health and `jst review pool prune` to preview history cleanup.
  Use `jst review pool subscribers JOB_ID` to inspect notification subscriptions.
- **Commits to the default branch** carry `Card: <ID>` or `Card: none (why)`; check with
  `jst lint range origin/trunk..HEAD` before pushing; `jst cards ID` shows what landed per card.
- **Numbers** come from `jst id next SERIES`, never by hand; `jst new SERIES --slug S` makes a
  record, `jst view SERIES` prints their table.
- **A user-facing change** gets a changelog entry: `jst changes new --slug S`; `jst changes check`.
- **Releases** are made by the release workflow's pull request, never a tag by hand;
  `jst release prepare` shows the next one.
- **After a surprising failure**, read `jst log --session ID` (or `--trace ID`) and
  `jst stats REPORT` before asking.
- **What is going on here**: `jst status` shows worktrees, agent sessions, recent events, chat,
  the brief and background jobs; `jst status PATH…` gives one row per repository.
- **Carry task context through compaction** with `jst brief save` and `jst brief show`. Keep the brief short and free of secrets.
- **Share an adoption profile** with `jst profile export`; verify its digest before adopting it.
- **Verify a queued merge candidate**: `jst queue status`, `jst queue test`, then `jst queue handoff`
  for the operator's landing decision. After landing, run `jst queue reconcile --commit SHA`.
- **Coordinate with other sessions**: `jst setup --chat` shows the MCP host setup. Use chat tools
  to send a request, read your inbox and acknowledge messages you process.
- **Deliver chat to managed Codex**: `jst chat codex-bridge` uses the owner's local socket, thread and session.
  Use `chat.ack` after processing a message.
- **Check chat at a Claude Code boundary** with the owner-configured `jst chat claude-hook`.
  When notified, use the MCP inbox and acknowledge each processed message.
- **Is jackstay healthy here**: `jst doctor`; `jst check` (is `jackstay.json` usable); `jst adopt --plan` (is
  this project up to date with jackstay). For machine PATH repairs, preview `jst setup --plan`.

### This project's organisation

- `just gate` is the gate.
- Ask before merging.
