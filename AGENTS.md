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

<!-- jackstay-adopt: begin agents-block sha256:84028b9352ac — written by `jst adopt` from jackstay.json and the profile; change those, not this text -->
## jst: the tools for working on this project

`jst` is installed on this machine. Reach for it before a hand-written shell loop:
`jst <command> --help` gives a command's details, `jst help` lists every command.

- **Run tests**: `jst test <scope>`; `jst test list` shows the scopes and when each is required.
  Only `passed` is a pass — read the last `jst test:` line. After a failure, `jst test last`, not
  a rerun. Before a pull request, `jst test gate` (`--commit HEAD`: on a clean copy jackstay makes and
  removes). No scope fits: `jst test -- COMMAND`.
- **Share a counted pass** with `jst receipts export KEY`. Verify it with `jst receipts verify`
  using the expected digest, revision and key from a trusted source.
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
- **Review a change**: `jst review BASE..REV --task NAME --reviewer NAME` runs a cold review.
  Pick `codex` or `claude-code`, or use the project's `review.use`.
  `jst review check BASE..REV --task NAME` checks the saved round.
- **Numbers** come from `jst id next SERIES`, never by hand.
- **What happened**: `jst log --session ID` (or `--trace ID`) and `jst stats REPORT` — after a
  surprising failure, before asking.
- **Follow work across tools** with `jst timeline`, the local PR, test, review and background history.
- **Which agents are here**: `jst inventory` reports this repository's worktrees and observed sessions.
- **The owner's words**: `jst owner --grep RE` to find a ruling to quote.
- **Is jackstay healthy here**: `jst doctor`; `jst check` (is `jackstay.json` usable); `jst adopt --plan` (is
  this project up to date with jackstay). For machine PATH repairs, preview `jst setup --plan`.
<!-- jackstay-adopt: end agents-block -->
