## jst: the tools for working on this project

`jst` is installed here; use it before a hand-written shell loop. `jst help` lists the commands,
`jst <command> --help` gives a command's flags, an example and its exit codes.

### Instead of a hand-written command

- **Instead of `go test`, `pytest`, `npm test` or another runner**, run `jst test SCOPE`
  (`jst test list` names them and when each is due). Only `passed` is a pass: read the last
  `jst test:` line. After a failure, `jst test last`, not a rerun (a run not logged names its
  output file).
  Before a pull request: `jst test gate --commit HEAD` (on a clean copy of the commit).
  Preview path-based selection with `jst test plan changed --base REF`. No scope fits: `jst test -- COMMAND`.
  `jst test` records evidence the agent sandbox may only read: run it outside the sandbox.
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

- **Share a counted pass**: `jst receipts export KEY`. Check one with `jst receipts verify`, taking
  the expected digest, revision and key from a trusted source, never from the receipt.
- **A heavy command** others on this machine must not overlap: `jst lock -- COMMAND`.
- **When the guard refuses** a command, run what it names instead, never a workaround. Read a
  `jst guard let this command run, with a warning` line before going on. Its rules: `jackstay.json` (`guard`).
- **Land a change**: `jst land --title T` pushes, opens the change request, waits for the required
  checks and merges. It stops at `jst land: ready for a person` for `jackstay.json`, CI workflows,
  `.claude/settings.json` and `land.human_paths`: say so and stop; never merge on the host
  (`gh pr merge` is refused), never run `--confirm`.
- **Review**, outside the agent sandbox (it records the round): `jst review BASE..REV --task NAME`
  (the project's reviewers; `--reviewer claude-code` or `codex,claude-code` for others);
  `--background` returns at once and the result comes as a chat
  event; `jst review check BASE..REV --task NAME` says if it is clear.
  Before calling a finding a blocker, check its rating: an unusual one blocks only with an ordinary-work path.
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
- **The owner's words**: `jst owner --grep RE` to find a ruling to quote.
- **Share an adoption profile** with `jst profile export`; verify its digest before adopting it.
- **Coordinate with other sessions**: `jst setup --chat` shows the MCP host setup. Use chat tools
  to send a request, read your inbox and acknowledge messages you process.
- **Chat in Codex**: read your inbox with `chat.inbox` from `jst chat serve`; the Codex bridge's push
  returns in jackstay 1.
- **Chat in Claude Code**: the owner-configured `jst hook claude` rings when messages wait; read them
  with `chat.inbox` and answer a request with `chat.respond`.
- **Is jackstay healthy here**: `jst doctor`; `jst check` (is `jackstay.json` usable); `jst adopt --plan` (is
  this project up to date with jackstay). For machine PATH repairs, preview `jst setup --plan`.
