## jst: the tools for working on this project

`jst` is installed on this machine. Reach for it before a hand-written shell loop:
`jst <command> --help` gives a command's details, `jst help` lists every command.

- **Run tests**: `jst test <scope>`; `jst test list` shows the scopes and when each is required.
  Only `passed` is a pass — read the last `jst test:` line. After a failure, `jst test last`, not
  a rerun. Before a pull request, `jst test gate` (`--commit HEAD`: on a clean copy jackstay makes and
  removes).
  Preview path-based selection with `jst test plan changed --base REF`. No scope fits: `jst test -- COMMAND`.
- **Share a counted pass** with `jst receipts export KEY`. Verify it with `jst receipts verify`
  using the expected digest, revision and key from a trusted source.
- **Verify CI evidence** with `jst receipts verify-ci`. Obtain the trusted key and expected CI identity
  independently of the uploaded receipt and attestation.
- **Wait** for a process, a file or a log line: `jst wait … --timeout S`, never a `sleep` loop.
- **Run something in the background**: `jst bg --for MIN --name N -- COMMAND`, never `&` or
  `nohup`; `jst procs` lists them, `jst procs stop NAME` stops yours.
- **A heavy command** others on this machine must not overlap: `jst lock -- COMMAND`.
- **Branches**: `jst start NAME` (a branch and its worktree from the fetched default branch),
  `jst sync` (merges the default branch in; never rebases), `jst tidy` (what is finished;
  `--fix --branch NAME` removes only what you made).
- **Drop uncommitted changes**: `jst discard PATH…` — what it drops goes to jackstay's trash
  (`jst trash restore`); never `git restore` or `git checkout -- PATH`.
- **The guard** refuses destructive commands and names what to run instead: run that, never a
  workaround. A line `jst guard let this command run, with a warning` means read it before going
  on. This project sets the guard's rules in `jackstay.json` (`guard`).
- **GitHub**: `jst gh ci-wait` / `ci-status` (a pull request's checks), `run-wait`, `pr-status`,
  `pr-land` (push, open, wait, squash-merge the head that was checked). jackstay acts as the account in
  `jackstay.json` (`github.account`).
- **Review a change**: `jst review BASE..REV --task NAME --reviewer NAME` runs a cold review.
  Pick `codex` or `claude-code`, or use the project's `review.use`.
  `jst review check BASE..REV --task NAME` checks the saved round.
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
- **What happened**: `jst log --session ID` (or `--trace ID`) and `jst stats REPORT` — after a
  surprising failure, before asking.
- **Follow work across tools** with `jst timeline`, the local PR, test, review and background history.
- **Which agents are here**: `jst inventory` reports this repository's worktrees and observed sessions.
- **Inspect local work across repositories** with `jst workspace board PATH…`. Read source status and freshness before acting.
- **Carry task context through compaction** with `jst brief save` and `jst brief show`. Keep the brief short and free of secrets.
- **The owner's words**: `jst owner --grep RE` to find a ruling to quote.
- **Share an adoption profile** with `jst profile export`. Verify its digest before adopting the bundle.
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
