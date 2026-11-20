# Rules for agents working on jackstay

For any AI coding agent working in this repository. People: see [CONTRIBUTING.md](CONTRIBUTING.md);
the same rules hold.

- **Read before writing.** Start from the README, the guide (`docs/guide/`) and the design of the
  part you change (`docs/design/`). A change of design is proposed in an issue first; once agreed,
  it is recorded as a short decision in `docs/decisions/` (context, decision, consequences).
- **jackstay uses itself.** This repository is adopted by jackstay: its guard hook judges your
  shell commands (`.claude/settings.json`), and its tools are listed in the block at the end of this
  file. When the guard refuses a command, run what it names instead; never rephrase the command to
  get around it.
- **The gate before a push**: `just gate` (or `jst test gate`), green. While working, run the tests
  of the package you change; `-tags integration` for the tests that start real processes or git.
- **Tests**: every fix comes with the test that failed without it; no test depends on timing or on
  an idle machine.
- **Names**: whole words in framework code, never single letters or cryptic abbreviations (tests
  may use short names).
- **General, not particular**: jackstay knows no project, runner or organisation. What differs
  between projects is data (`jackstay.json`, a profile), never a code path named after one.
- **macOS and Linux**: code that reads the system has a variant for each (`*_darwin.go`,
  `*_linux.go`); CI runs the gate on Linux.
- **The AGENTS block is code**: a new command, or a change in what an agent should do with one,
  changes its item in `internal/adopt/block` and the golden files in the same commit; then
  `jst adopt --apply` here, and commit this file with it.
- **Dependencies** need a reason and a row in `docs/licences.md` (Apache-2.0, MIT or BSD).
- **Commits and pull requests** are in English, one change each, saying what changed and why. No
  force-push to a shared branch.
- **Secrets**: never write a token, key or password into a file, a test, a log or a commit message;
  tests use made-up values.
- **Public evidence**: use synthetic fixture identities and neutral paths. Keep personal account
  identifiers and private pilot repository references out of tracked files and commit metadata.

<!-- jackstay-adopt: begin agents-block sha256:87bb35b7520e — written by `jst adopt` from jackstay.json and the profile; change those, not this text -->
## jst: the tools for working on this project

`jst` is installed here; use it before a hand-written shell loop. `jst help` lists the commands,
`jst <command> --help` gives a command's flags, an example and its exit codes.

### Instead of a hand-written command

- **Instead of `go test`, `pytest`, `npm test` or another runner**, run `jst test SCOPE`
  (`jst test list` names them and when each is due). Only `passed` is a pass: read the last
  `jst test:` line. After a failure, `jst test last`, not a rerun.
  Before a pull request: `jst test gate --commit HEAD` (on a clean copy of the commit). No scope fits: `jst test -- COMMAND`.
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
- **When the guard refuses** a command, run what it names instead, never a workaround. Read a
  `jst guard let this command run, with a warning` line before going on.
- **Review**: `jst review BASE..REV --task NAME` (the project's reviewers; `--reviewer claude-code`
  or `codex,claude-code` for others); `--background` returns at once and the result comes as a chat
  event; `jst review check BASE..REV --task NAME` says if it is clear.
  Before calling a finding a blocker, check its rating: an unusual one blocks only with an ordinary-work path.
- **Numbers** come from `jst id next SERIES`, never by hand.
- **After a surprising failure**, read `jst log --session ID` (or `--trace ID`) and
  `jst stats REPORT` before asking.
- **What is going on here**: `jst status` shows worktrees, agent sessions, recent events, chat,
  the brief and background jobs; `jst status PATH…` gives one row per repository.
- **Carry task context through compaction** with `jst brief save` and `jst brief show`. Keep the brief short and free of secrets.
- **The owner's words**: `jst owner --grep RE` to find a ruling to quote.
- **Is jackstay healthy here**: `jst doctor`; `jst check` (is `jackstay.json` usable); `jst adopt --plan` (is
  this project up to date with jackstay). For machine PATH repairs, preview `jst setup --plan`.
<!-- jackstay-adopt: end agents-block -->
