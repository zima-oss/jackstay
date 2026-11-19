## jst: the tools for working on this project

`jst` is installed on this machine. Reach for it before a hand-written shell loop:
`jst <command> --help` gives a command's details, `jst help` lists every command.

- **Run tests**: `jst test <scope>`; `jst test list` shows the scopes and when each is required.
  Only `passed` is a pass — read the last `jst test:` line. After a failure, `jst test last`, not
  a rerun. No scope fits: `jst test -- COMMAND`.
- **Wait** for a process, a file or a log line: `jst wait … --timeout S`, never a `sleep` loop.
- **Run something in the background**: `jst bg --for MIN --name N -- COMMAND`, never `&` or
  `nohup`; `jst procs` lists them, `jst procs stop NAME` stops yours.
- **Branches**: `jst start NAME` (a branch and its worktree from the fetched default branch),
  `jst sync` (merges the default branch in; never rebases), `jst tidy` (what is finished;
  `--fix --branch NAME` removes only what you made).
- **Drop uncommitted changes**: `jst discard PATH…` — what it drops goes to jackstay's trash
  (`jst trash restore`); never `git restore` or `git checkout -- PATH`.
- **The guard** refuses destructive commands and names what to run instead: run that, never a
  workaround. A line `jst guard let this command run, with a warning` means read it before going
  on.
- **What happened**: `jst log --session ID` (or `--trace ID`) and `jst stats REPORT` — after a
  surprising failure, before asking.
- **Follow work across tools** with `jst timeline`, the local PR, test, review and background history.
- **Which agents are here**: `jst inventory` reports this repository's worktrees and observed sessions.
- **The owner's words**: `jst owner --grep RE` to find a ruling to quote.
- **Is jackstay healthy here**: `jst doctor`; `jst check` (is `jackstay.json` usable); `jst adopt --plan` (is
  this project up to date with jackstay). For machine PATH repairs, preview `jst setup --plan`.
