## jst: the tools for working on this project

`jst` is installed here; use it before a hand-written shell loop. `jst help` lists the commands,
`jst <command> --help` gives a command's flags, an example and its exit codes.

- **Tests**: `jst test SCOPE` (`jst test list` names them and when each is due). Only `passed`
  is a pass: read the last `jst test:` line. After a failure, `jst test last`, not a rerun. No scope fits: `jst test -- COMMAND`.
- **Wait** for a process, a file or a log line with `jst wait … --timeout S`, never a `sleep` loop.
- **Background**: `jst bg --for MIN --name N -- COMMAND`, never `&` or `nohup`; `jst procs stop N`
  stops it.
- **Branches**: `jst start NAME` makes a branch and its worktree; `jst sync` merges the default branch
  in (never rebase); `jst tidy --fix --branch NAME` removes yours once it is finished.
- **Drop uncommitted changes** with `jst discard PATH…`, never `git restore` or
  `git checkout -- PATH`; `jst trash restore` brings them back.
- **When the guard refuses** a command, run what it names instead, never a workaround. Read a
  `jst guard let this command run, with a warning` line before going on.
- **After a surprising failure**, read `jst log --session ID` (or `--trace ID`) and
  `jst stats REPORT` before asking.
- **Follow work across tools** with `jst timeline`, the local PR, test, review and background history.
- **Which agents are here**: `jst inventory` reports this repository's worktrees and observed sessions.
- **Inspect local work across repositories** with `jst workspace board PATH…`. Read source status and freshness before acting.
- **Carry task context through compaction** with `jst brief save` and `jst brief show`. Keep the brief short and free of secrets.
- **The owner's words**: `jst owner --grep RE` to find a ruling to quote.
- **Is jackstay healthy here**: `jst doctor`; `jst check` (is `jackstay.json` usable); `jst adopt --plan` (is
  this project up to date with jackstay). For machine PATH repairs, preview `jst setup --plan`.
