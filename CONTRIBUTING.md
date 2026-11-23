# Contributing to jackstay

Thank you for helping. jackstay is one Go binary, `jst`, that sits beside many parallel AI coding
sessions: a guard for their shell commands, recorded test runs with receipts, background processes
that end, cold reviews, releases and housekeeping. This page says how to build it, how a change
gets in, and what a change has to keep true.

## Before you start

- **A bug**: open an issue with what you ran, what jackstay did and what you expected, with
  `jst version` and your system (macOS or Linux). A security problem goes privately instead
  ([SECURITY.md](SECURITY.md)).
- **A feature or a change of behaviour**: open an issue first and say what problem it solves. A
  change that adds a command, a setting or a rule is easier to agree on before it is written.
- Everyone here follows the [code of conduct](CODE_OF_CONDUCT.md).

## Building and testing

You need Go (the version in `go.mod`'s `toolchain` line), [`just`](https://github.com/casey/just),
git, [ShellCheck](https://www.shellcheck.net) (for `install.sh`) and `curl` (its tests); `lsof` on
Linux. Hugo, for the documentation site, is run by the justfile at its pinned version, with nothing
to install.

    just build          # .build/jst from this checkout; bin/jst builds and runs it
    bin/jst test gate   # every check a pull request needs: gofmt, go vet, golangci-lint, every test
                        # (unit and integration, with the race detector), govulncheck, gosec, the docs
                        # check (the documents, and the site built from them)

The `gate` scope (`jackstay.json`) runs the scopes `x-check`, `x-go`, `x-sec` and `x-docs`, each
once. CI runs the same scopes on Linux in one job, `jst test ci (linux)`, on every pull request.
Run the gate before you push; a pull request whose gate is red is not reviewed. While you work, run the tests of what you changed (`go test
./internal/<package>/`, with `-tags integration` for the tests that start real processes or real
git), and the whole gate at the end.

## What a change keeps true

- **Tests come with the change.** A fix comes with the test that failed without it. Tests must not
  depend on timing: no test passes only because a machine is fast or idle.
- **Readable names.** Framework code uses whole words for names, never single letters or
  abbreviations (tests may use short names). Comments say why, in plain sentences.
- **jackstay stays general.** It must not know any one project, runner or organisation: what differs
  between projects is data in `jackstay.json` or a profile, never a code path named after someone.
- **Both systems.** jackstay runs on macOS and Linux. CI runs the gate's scopes on Linux only; code under
  `*_darwin.go` is checked on a Mac by a maintainer before an install and a release. Run `just darwin-smoke` for a focused local
  check of process shutdown, the hook, installation recovery, receipts and clean-copy tests
  ([W-083](docs/decisions/W-083-no-macos-job-in-ci.md)).
- **The tools describe themselves.** A new command answers `jst <command> --help`. If it changes
  what an agent should do, its item in the AGENTS block (`internal/adopt/block`, with its golden
  files) changes in the same commit, and `jst adopt --apply` is re-run so this repository's own
  `AGENTS.md` matches.
- **Dependencies** are few. A new module needs a reason in the pull request and a row in
  [docs/licences.md](docs/licences.md) (Apache-2.0, MIT or BSD only; the gate checks the row).

## Writing documentation

Pages, help texts and the AGENTS block follow [docs/STYLE.md](docs/STYLE.md): outcome first, plain
English, one meaning in one place, and command examples that run. `just docs` checks what it can.

## Pull requests

- One change per pull request, with a title that says what it changes. Keep refactoring apart from
  behaviour changes.
- Commit messages say what changed and why, in English.
- A change of design is recorded as a decision in `docs/decisions/` (context, decision,
  consequences), under the next W number a maintainer gives you.
- By submitting a pull request you agree that your contribution is licensed under the Apache
  License 2.0, as the rest of jackstay ([LICENSE](LICENSE), section 5). There is no separate
  contributor agreement.

## Releases

Maintainers release. Each release is one version and one annotated tag `vX.Y.Z`; a tag is never
moved.
