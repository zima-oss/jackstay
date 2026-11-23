## jst: the tools for working on this project

`jst` is installed here; use it before a hand-written shell loop. `jst help` lists the commands,
`jst <command> --help` gives a command's flags, an example and its exit codes.

### Instead of a hand-written command

- **Instead of `go test`, `pytest`, `npm test` or another runner**, run `jst test SCOPE`
  (`jst test list` names them and when each is due). Only `passed` is a pass: read the last
  `jst test:` line. After a failure, `jst test last`, not a rerun (a run not logged names its
  output file).
  Preview path-based selection with `jst test plan changed --base REF`. No scope fits: `jst test -- COMMAND`.
  `jst test` records evidence the agent sandbox may only read: run it outside the sandbox.
- **Where tests run**: CI runs x-build, x-docs, x-go on Linux; x-ui on macOS and Linux and is the
  gate. Before a pull request: `jst test changed`. Before an install or a tag: `jst test uncovered`.
- **Instead of `sleep N`**, run `jst wait … --timeout S`: until a process ends, a file appears or a
  log line matches.
- **Instead of `git worktree add` or `remove`**, run `jst start NAME` and, once it is merged,
  `jst tidy --fix --branch NAME`. `jst sync` merges the default branch in; never rebase.
- **Instead of `&` or `nohup`**, run `jst bg --for MIN --name N -- COMMAND`; `jst procs stop N`
  stops it.
- **Instead of `git restore` or `git checkout -- PATH`**, run `jst discard PATH…`;
  it logs the changes it drops (`jst trash list`).

### The other tools

- **When the guard refuses** a command, run what it names instead, never a workaround. Read a
  `jst guard let this command run, with a warning` line before going on.
- **After a surprising failure**, read `jst log --session ID` (or `--trace ID`) and
  `jst stats REPORT` before asking.
- **What is going on here**: `jst status` shows worktrees, agent sessions, recent events, chat,
  the brief and background jobs; `jst status PATH…` gives one row per repository.
- **Carry task context through compaction** with `jst brief save` and `jst brief show`. Keep the brief short and free of secrets.
- **The owner's words**: `jst owner --grep RE` to find a ruling to quote.
- **Is jackstay healthy here**: `jst doctor`; `jst check` (is `jackstay.json` usable); `jst adopt --plan` (is
  this project up to date with jackstay). For machine PATH repairs, preview `jst setup --plan`.
