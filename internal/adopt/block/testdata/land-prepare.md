## jst: the tools for working on this project

`jst` is installed here; use it before a hand-written shell loop. `jst help` lists the commands,
`jst <command> --help` gives a command's flags, an example and its exit codes.

### Instead of a hand-written command

- **Instead of `go test`, `pytest`, `npm test` or another runner**, run `jst test -- COMMAND`.
  Only `passed` is a pass: read the last `jst test:` line. After a failure, `jst test last`.
- **Instead of `gh pr checks --watch` or `gh run watch`**, run `jst checks wait` or
  `jst checks run-wait`; `jst checks status` reads them once. jackstay acts as `github.account`.
- **Instead of `sleep N`**, run `jst wait … --timeout S`: until a process ends, a file appears or a
  log line matches.
- **Instead of `git worktree add` or `remove`**, run `jst start NAME` and, once it is merged,
  `jst tidy --fix --branch NAME`. `jst sync` merges the default branch in; never rebase.
- **Instead of `&` or `nohup`**, run `jst bg --for MIN --name N -- COMMAND`; `jst procs stop N`
  stops it.
- **Instead of `git restore` or `git checkout -- PATH`**, run `jst discard PATH…`;
  `jst trash restore` brings the changes back.

### The other tools

- **When the guard refuses** a command, run what it names instead, never a workaround. Read a
  `jst guard let this command run, with a warning` line before going on.
- **Land a change**: `jst land --title T` pushes, opens the change request and waits for the
  required checks; a person merges. At `jst land: ready for a person`, say so and stop; never merge
  on the host (`gh pr merge` is refused), never run `--confirm`.
- **After a surprising failure**, read `jst log --session ID` (or `--trace ID`) and
  `jst stats REPORT` before asking.
- **What is going on here**: `jst status` shows worktrees, agent sessions, recent events, chat,
  the brief and background jobs; `jst status PATH…` gives one row per repository.
- **Carry task context through compaction** with `jst brief save` and `jst brief show`. Keep the brief short and free of secrets.
- **The owner's words**: `jst owner --grep RE` to find a ruling to quote.
- **Is jackstay healthy here**: `jst doctor`; `jst check` (is `jackstay.json` usable); `jst adopt --plan` (is
  this project up to date with jackstay). For machine PATH repairs, preview `jst setup --plan`.
