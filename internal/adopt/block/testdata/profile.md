## jst: the tools for working on this project

`jst` is installed on this machine. Reach for it before a hand-written shell loop:
`jst <command> --help` gives a command's details, `jst help` lists every command.

- **Run tests**: `jst test <scope>`; `jst test list` shows the scopes and when each is required.
  Only `passed` is a pass — read the last `jst test:` line. After a failure, `jst test last`, not
  a rerun. Before a pull request, `jst test gate` (`--commit HEAD`: on a clean copy jackstay makes and
  removes). No scope fits: `jst test -- COMMAND`.
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
- **Commits to the default branch** carry `Card: <ID>` or `Card: none (why)`; check with
  `jst lint range origin/trunk..HEAD` before pushing; `jst cards ID` shows what landed per card.
- **Numbers** come from `jst id next SERIES`, never by hand; `jst new SERIES --slug S` makes a
  record, `jst view SERIES` prints their table.
- **A user-facing change** gets a changelog entry: `jst changes new --slug S`; `jst changes check`.
- **Releases** are made by the release workflow's pull request, never a tag by hand;
  `jst release prepare` shows the next one.
- **What happened**: `jst log --session ID` (or `--trace ID`) and `jst stats REPORT` — after a
  surprising failure, before asking.
- **Is jackstay healthy here**: `jst doctor`; `jst check` (is `jackstay.json` usable); `jst adopt --plan` (is
  this project up to date with jackstay).

### This project's organisation

- `just gate` is the gate.
- Ask before merging.
